package execution

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/sanix-darker/git-ci/internal/store"
)

const (
	maxWorkflowCommandBytes = 8 << 10
	maxWorkflowMasks        = 100
)

type workflowCommandContextKey struct{}

type workflowCommandState struct {
	mu           sync.Mutex
	masks        []string
	dynamicMasks int
	stopToken    string
	counts       map[string]int
}

type workflowCommand struct {
	name       string
	properties map[string]string
	data       string
}

type workflowCommandResult struct {
	line       string
	annotation *store.AppendStepAnnotationParams
	diagnostic string
}

func newWorkflowCommandState(secrets map[string]string) *workflowCommandState {
	state := &workflowCommandState{counts: make(map[string]int)}
	for _, secret := range secrets {
		state.addMaskLocked(secret, false)
	}
	return state
}

func withWorkflowCommandState(ctx context.Context, state *workflowCommandState) context.Context {
	return context.WithValue(ctx, workflowCommandContextKey{}, state)
}

func workflowCommandStateFromContext(ctx context.Context) *workflowCommandState {
	state, _ := ctx.Value(workflowCommandContextKey{}).(*workflowCommandState)
	return state
}

func (state *workflowCommandState) redact(value string) string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.redactLocked(value)
}

func (state *workflowCommandState) process(stepID string, stream store.LogStream, line string) workflowCommandResult {
	state.mu.Lock()
	defer state.mu.Unlock()
	result := workflowCommandResult{line: state.redactLocked(line)}
	if stream != store.LogStreamStdout {
		return result
	}
	if state.stopToken != "" {
		if strings.EqualFold(line, "::"+state.stopToken+"::") {
			state.stopToken = ""
			result.line = "::***::"
		}
		return result
	}
	command, candidate, err := parseWorkflowCommand(line)
	if !candidate {
		return result
	}
	if err != nil {
		name := workflowCommandName(line)
		if isSupportedWorkflowCommand(name) {
			result.diagnostic = "workflow command ignored: malformed or oversized command"
			if name == "add-mask" || name == "stop-commands" {
				result.line = "::" + name + "::***"
			}
		}
		return result
	}
	switch command.name {
	case "add-mask":
		if !state.addMaskLocked(command.data, true) {
			result.diagnostic = "workflow command ignored: mask is empty or the job mask limit was reached"
		}
		result.line = "::add-mask::***"
	case "stop-commands":
		if !validStopToken(command.data) {
			result.diagnostic = "workflow command ignored: invalid stop token"
			result.line = "::stop-commands::***"
			return result
		}
		state.stopToken = command.data
		if len(command.data) > 6 {
			state.addMaskLocked(command.data, false)
		}
		result.line = "::stop-commands::***"
	case "notice", "warning", "error":
		if state.counts[stepID] >= store.MaxStepAnnotations {
			result.diagnostic = "workflow command ignored: step annotation limit reached"
			return result
		}
		annotation, buildErr := state.annotationLocked(stepID, command)
		if buildErr != nil {
			result.diagnostic = "workflow command ignored: invalid annotation"
			return result
		}
		state.counts[stepID]++
		result.annotation = &annotation
		result.line = state.redactLocked(line)
	}
	return result
}

func (state *workflowCommandState) annotationLocked(stepID string, command workflowCommand) (store.AppendStepAnnotationParams, error) {
	if strings.TrimSpace(command.data) == "" || len(command.data) > store.MaxAnnotationMessageSize {
		return store.AppendStepAnnotationParams{}, fmt.Errorf("invalid annotation message")
	}
	annotation := store.AppendStepAnnotationParams{
		StepID: stepID, Level: store.AnnotationLevel(command.name),
		Message: state.redactLocked(command.data), Title: state.redactLocked(command.properties["title"]),
		File: state.redactLocked(command.properties["file"]),
	}
	if len(annotation.Title) > store.MaxAnnotationFieldSize || len(annotation.File) > store.MaxAnnotationFieldSize {
		return store.AppendStepAnnotationParams{}, fmt.Errorf("annotation field too large")
	}
	if annotation.File == "" {
		annotation.File = ".github"
	}
	line, endLine := 1, 1
	annotation.StartLine, annotation.EndLine = &line, &endLine
	properties := []struct {
		name   string
		target **int
	}{
		{name: "line", target: &annotation.StartLine},
		{name: "endline", target: &annotation.EndLine},
		{name: "col", target: &annotation.StartColumn},
		{name: "endcolumn", target: &annotation.EndColumn},
	}
	for _, property := range properties {
		value := strings.TrimSpace(command.properties[property.name])
		if value == "" {
			continue
		}
		position, err := strconv.Atoi(value)
		if err != nil || position < 1 {
			return store.AppendStepAnnotationParams{}, fmt.Errorf("invalid annotation position")
		}
		positionCopy := position
		*property.target = &positionCopy
	}
	return annotation, nil
}

func (state *workflowCommandState) addMaskLocked(value string, dynamic bool) bool {
	if value == "" || len(value) > maxWorkflowCommandBytes {
		return false
	}
	candidates := append([]string{value}, strings.FieldsFunc(value, unicode.IsSpace)...)
	accepted := false
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if containsString(state.masks, candidate) {
			accepted = true
			continue
		}
		if dynamic && state.dynamicMasks >= maxWorkflowMasks {
			return accepted
		}
		state.masks = append(state.masks, candidate)
		if dynamic {
			state.dynamicMasks++
		}
		accepted = true
	}
	sort.SliceStable(state.masks, func(i, j int) bool { return len(state.masks[i]) > len(state.masks[j]) })
	return accepted
}

func (state *workflowCommandState) redactLocked(value string) string {
	for _, mask := range state.masks {
		if mask != "" {
			value = strings.ReplaceAll(value, mask, "***")
		}
	}
	return value
}

func parseWorkflowCommand(line string) (workflowCommand, bool, error) {
	if !strings.HasPrefix(line, "::") {
		return workflowCommand{}, false, nil
	}
	if len(line) > maxWorkflowCommandBytes {
		return workflowCommand{}, true, fmt.Errorf("command too large")
	}
	remainder := line[2:]
	separator := strings.Index(remainder, "::")
	if separator < 0 {
		return workflowCommand{}, true, fmt.Errorf("missing command separator")
	}
	header, data := remainder[:separator], remainder[separator+2:]
	name, propertyText := header, ""
	if split := strings.IndexAny(header, " \t"); split >= 0 {
		name, propertyText = header[:split], strings.TrimSpace(header[split+1:])
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return workflowCommand{}, true, fmt.Errorf("missing command name")
	}
	command := workflowCommand{name: name, properties: make(map[string]string), data: decodeWorkflowValue(data, false)}
	if propertyText == "" {
		return command, true, nil
	}
	for _, item := range strings.Split(propertyText, ",") {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return workflowCommand{}, true, fmt.Errorf("invalid command property")
		}
		command.properties[strings.ToLower(strings.TrimSpace(parts[0]))] = decodeWorkflowValue(parts[1], true)
	}
	return command, true, nil
}

func decodeWorkflowValue(value string, property bool) string {
	var output strings.Builder
	output.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' || index+2 >= len(value) {
			output.WriteByte(value[index])
			continue
		}
		code := strings.ToUpper(value[index+1 : index+3])
		replacement, ok := map[string]byte{"25": '%', "0A": '\n', "0D": '\r'}[code]
		if property {
			if code == "3A" {
				replacement, ok = ':', true
			} else if code == "2C" {
				replacement, ok = ',', true
			}
		}
		if !ok {
			output.WriteByte(value[index])
			continue
		}
		output.WriteByte(replacement)
		index += 2
	}
	return output.String()
}

func workflowCommandName(line string) string {
	if !strings.HasPrefix(line, "::") {
		return ""
	}
	remainder := line[2:]
	end := strings.IndexAny(remainder, " :\t")
	if end < 0 {
		end = len(remainder)
	}
	return strings.ToLower(strings.TrimSpace(remainder[:end]))
}

func isSupportedWorkflowCommand(name string) bool {
	return name == "add-mask" || name == "stop-commands" || name == "notice" || name == "warning" || name == "error"
}

func validStopToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
