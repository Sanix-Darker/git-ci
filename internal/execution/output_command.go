package execution

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sanix-darker/git-ci/internal/store"
)

const (
	maxGitHubOutputBytes          = 1 << 20
	stepOutputMappingsEnvironment = "GCI_STEP_OUTPUT_MAPPINGS_JSON"
)

var (
	outputNamePattern       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	runtimeOutputExpression = regexp.MustCompile(`\$\{\{\s*((?:steps|needs)\.[A-Za-z_][A-Za-z0-9_-]*\.outputs\.[A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)
)

type runtimeOutputContext struct {
	jobs         map[string]map[string]string
	steps        map[string]map[string]string
	dependencies []string
	stepBytes    int
	environment  map[string]string
	paths        []string
	summaryCount int
	dotenvJobs   map[string]map[string]string
	dotenvStages map[string]string
	dotenvOrder  []string
}

func newRuntimeOutputContext() *runtimeOutputContext {
	return &runtimeOutputContext{
		jobs: make(map[string]map[string]string), dotenvJobs: make(map[string]map[string]string),
		dotenvStages: make(map[string]string),
	}
}

func (context *runtimeOutputContext) beginJob(dependencies []string) {
	context.dependencies = append([]string(nil), dependencies...)
	context.steps = make(map[string]map[string]string)
	context.stepBytes = 0
	context.environment = make(map[string]string)
	context.paths = nil
	context.summaryCount = 0
}

func (context *runtimeOutputContext) recordDotenv(job store.Job, semantics *frozenJobSemantics, variables map[string]string) {
	if len(variables) == 0 {
		return
	}
	key := pointerValue(job.Key)
	if _, exists := context.dotenvJobs[key]; !exists {
		context.dotenvOrder = append(context.dotenvOrder, key)
	}
	context.dotenvJobs[key] = copyStringMap(variables)
	if semantics != nil {
		context.dotenvStages[key] = semantics.Stage
	}
}

func (context *runtimeOutputContext) dotenvEnvironment(semantics *frozenJobSemantics) map[string]string {
	if semantics == nil || semantics.Provider != string(ProviderGitLabCI) {
		return nil
	}
	selected := make(map[string]bool)
	switch {
	case semantics.DependenciesDefined:
		for _, dependency := range semantics.ArtifactDependencies {
			selected[dependency] = true
		}
	case len(context.dependencies) > 0:
		for _, dependency := range context.dependencies {
			artifacts, configured := semantics.NeedsArtifacts[dependency]
			selected[dependency] = !configured || artifacts
		}
	default:
		for _, dependency := range context.dotenvOrder {
			selected[dependency] = context.dotenvStages[dependency] != semantics.Stage
		}
	}
	result := make(map[string]string)
	for _, dependency := range context.dotenvOrder {
		if !selected[dependency] {
			continue
		}
		for name, value := range context.dotenvJobs[dependency] {
			result[name] = value
		}
	}
	return result
}

func (context *runtimeOutputContext) recordStep(key string, outputs map[string]string) error {
	if key == "" || len(outputs) == 0 {
		return nil
	}
	context.stepBytes += outputMapBytes(outputs)
	if context.stepBytes > maxGitHubOutputBytes {
		return fmt.Errorf("execution: job outputs exceed %d bytes", maxGitHubOutputBytes)
	}
	context.steps[key] = copyStringMap(outputs)
	return nil
}

func (context *runtimeOutputContext) finishJob(job store.Job, semantics *frozenJobSemantics) []string {
	if semantics == nil || len(semantics.Outputs) == 0 {
		return nil
	}
	values := context.expressionValues()
	outputs := make(map[string]string, len(semantics.Outputs))
	for name, expression := range semantics.Outputs {
		outputs[name] = resolveOutputExpressions(expression, values)
	}
	key := pointerValue(job.Key)
	context.jobs[key] = outputs
	if semantics.SourceKey != "" && semantics.SourceKey != key {
		if context.jobs[semantics.SourceKey] == nil {
			context.jobs[semantics.SourceKey] = make(map[string]string)
		}
		for name, value := range outputs {
			context.jobs[semantics.SourceKey][name] = value
		}
	}
	return sortedOutputNames(outputs)
}

func (context *runtimeOutputContext) expressionValues() map[string]string {
	values := make(map[string]string)
	for step, outputs := range context.steps {
		for name, value := range outputs {
			values["steps."+step+".outputs."+name] = value
		}
	}
	for _, dependency := range context.dependencies {
		for name, value := range context.jobs[dependency] {
			values["needs."+dependency+".outputs."+name] = value
		}
	}
	return values
}

func (context *runtimeOutputContext) addConditionValues(values map[string]interface{}, dependencies []string) {
	for step, outputs := range context.steps {
		for name, value := range outputs {
			values["steps."+step+".outputs."+name] = value
		}
	}
	for _, dependency := range dependencies {
		for name, value := range context.jobs[dependency] {
			values["needs."+dependency+".outputs."+name] = value
		}
	}
}

func resolveOutputExpressions(value string, values map[string]string) string {
	return runtimeOutputExpression.ReplaceAllStringFunc(value, func(expression string) string {
		matches := runtimeOutputExpression.FindStringSubmatch(expression)
		if len(matches) != 2 {
			return expression
		}
		return values[matches[1]]
	})
}

func resolveRuntimeStep(step store.Step, values map[string]string) (store.Step, error) {
	resolvePointer := func(value *string) *string {
		if value == nil {
			return nil
		}
		resolved := resolveOutputExpressions(*value, values)
		return &resolved
	}
	step.Command = resolvePointer(step.Command)
	step.WorkingDirectory = resolvePointer(step.WorkingDirectory)
	step.Shell = resolvePointer(step.Shell)
	environment, err := resolveRuntimeEnvironment(step.Environment, values, true)
	if err != nil {
		return store.Step{}, err
	}
	step.Environment = environment
	return step, nil
}

func resolveRuntimeEnvironment(environment json.RawMessage, values map[string]string, actionInputs bool) (json.RawMessage, error) {
	decoded := decodeEnvironmentJSON(environment)
	for key, value := range decoded {
		if key == "GCI_ACTION_INPUTS_JSON" && actionInputs {
			inputs := make(map[string]string)
			if err := json.Unmarshal([]byte(value), &inputs); err != nil {
				return nil, fmt.Errorf("execution: decode action inputs: %w", err)
			}
			for name, input := range inputs {
				inputs[name] = resolveOutputExpressions(input, values)
			}
			encoded, err := json.Marshal(inputs)
			if err != nil {
				return nil, err
			}
			decoded[key] = string(encoded)
			continue
		}
		if !strings.HasPrefix(key, "GCI_") {
			decoded[key] = resolveOutputExpressions(value, values)
		}
	}
	encoded, err := json.Marshal(decoded)
	return encoded, err
}

func applyStepOutputMappings(environment json.RawMessage, values map[string]string, outputs map[string]string) (map[string]string, error) {
	encoded := decodeEnvironmentJSON(environment)[stepOutputMappingsEnvironment]
	if strings.TrimSpace(encoded) == "" {
		return outputs, nil
	}
	mappings := make(map[string]string)
	if err := json.Unmarshal([]byte(encoded), &mappings); err != nil {
		return outputs, fmt.Errorf("execution: decode composite output mappings: %w", err)
	}
	result := copyStringMap(outputs)
	for name, expression := range mappings {
		result[name] = resolveOutputExpressions(expression, values)
	}
	return result, nil
}

func prepareGitHubOutputFile(workspace, stepID string, container bool) (string, string, error) {
	return prepareGitHubCommandFile(workspace, stepID, "output", container)
}

func prepareGitHubCommandFile(workspace, stepID, kind string, container bool) (string, string, error) {
	directory := filepath.Join(workspace, ".gci", "command-files")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(stepID))
	name := fmt.Sprintf("%x.%s", digest[:16], kind)
	hostPath := filepath.Join(directory, name)
	file, err := os.OpenFile(hostPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	if err := file.Close(); err != nil {
		return "", "", err
	}
	runtimePath := hostPath
	if container {
		runtimePath = filepath.ToSlash(filepath.Join("/workspace", ".gci", "command-files", name))
	}
	return hostPath, runtimePath, nil
}

func parseGitHubOutput(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("execution: GITHUB_OUTPUT must remain a regular non-symlink file")
	}
	if info.Size() > maxGitHubOutputBytes {
		return nil, fmt.Errorf("execution: GITHUB_OUTPUT exceeds %d bytes", maxGitHubOutputBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(contents) || strings.IndexByte(string(contents), 0) >= 0 {
		return nil, errors.New("execution: GITHUB_OUTPUT must be valid UTF-8 without null bytes")
	}
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	outputs := make(map[string]string)
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		if line == "" {
			continue
		}
		if marker := strings.Index(line, "<<"); marker > 0 && !strings.Contains(line[:marker], "=") {
			name, delimiter := line[:marker], line[marker+2:]
			if !outputNamePattern.MatchString(name) || delimiter == "" {
				return nil, fmt.Errorf("execution: invalid GITHUB_OUTPUT command %q", line)
			}
			start := index + 1
			for index = start; index < len(lines) && strings.TrimSuffix(lines[index], "\r") != delimiter; index++ {
			}
			if index >= len(lines) {
				return nil, fmt.Errorf("execution: unterminated GITHUB_OUTPUT value %q", name)
			}
			outputs[name] = strings.Join(lines[start:index], "\n")
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || !outputNamePattern.MatchString(name) {
			return nil, fmt.Errorf("execution: invalid GITHUB_OUTPUT command %q", line)
		}
		outputs[name] = value
	}
	return outputs, nil
}

func outputMapBytes(outputs map[string]string) int {
	total := 0
	for name, value := range outputs {
		total += len(name) + len(value)
	}
	return total
}

func sortedOutputNames(outputs map[string]string) []string {
	result := make([]string, 0, len(outputs))
	for name := range outputs {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
