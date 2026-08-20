package execution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/executionsemantics"
	"github.com/sanix-darker/git-ci/internal/parsers"
	"github.com/sanix-darker/git-ci/pkg/types"
	yaml "gopkg.in/yaml.v3"
)

const (
	maxReusableWorkflowDepth = 10
	maxReusableWorkflows     = 50
	maxReusableWorkflowBytes = 2 << 20
)

var (
	reusableInputExpression  = regexp.MustCompile(`\$\{\{\s*inputs\.([A-Za-z0-9_-]+)\s*\}\}`)
	reusableSecretExpression = regexp.MustCompile(`\$\{\{\s*secrets\.([A-Za-z0-9_-]+)\s*\}\}`)
)

type reusableExpansionState struct {
	root   string
	unique map[string]struct{}
	stack  []string
}

type reusableWorkflowMetadata struct {
	Inputs  map[string]reusableInput
	Secrets map[string]reusableSecret
}

type reusableInput struct {
	Default  interface{}
	Required bool
	Type     string
}

type reusableSecret struct {
	Required bool
}

func expandLocalReusableWorkflows(root, source string, pipeline *types.Pipeline) error {
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("resolve caller workflow: %w", err)
	}
	state := &reusableExpansionState{root: canonicalRoot, unique: make(map[string]struct{}), stack: []string{canonicalSource}}
	return state.expand(pipeline, 0)
}

func (state *reusableExpansionState) expand(pipeline *types.Pipeline, depth int) error {
	if pipeline == nil {
		return errors.New("called parser returned no pipeline")
	}
	keys := make([]string, 0, len(pipeline.Jobs))
	for key := range pipeline.Jobs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	replacements := make(map[string][]string)
	for _, callerKey := range keys {
		caller := pipeline.Jobs[callerKey]
		if caller == nil || caller.WorkflowCall == nil || !isLocalReusableWorkflow(caller.WorkflowCall.Uses) {
			continue
		}
		if caller.Strategy != nil && (len(caller.Strategy.Matrix) > 0 || len(caller.Strategy.Include) > 0) {
			return fmt.Errorf("job %q: matrix reusable workflow calls are not yet supported", callerKey)
		}
		if depth >= maxReusableWorkflowDepth {
			return fmt.Errorf("job %q: reusable workflow nesting exceeds %d levels", callerKey, maxReusableWorkflowDepth)
		}
		path, reference, err := state.resolve(caller.WorkflowCall.Uses)
		if err != nil {
			return fmt.Errorf("job %q: %w", callerKey, err)
		}
		for _, ancestor := range state.stack {
			if ancestor == path {
				return fmt.Errorf("job %q: reusable workflow cycle reaches %s", callerKey, reference)
			}
		}
		state.unique[path] = struct{}{}
		if len(state.unique) > maxReusableWorkflows {
			return fmt.Errorf("workflow references more than %d unique reusable workflows", maxReusableWorkflows)
		}
		metadata, err := readReusableWorkflowMetadata(path)
		if err != nil {
			return fmt.Errorf("job %q: %w", callerKey, err)
		}
		inputs, err := resolveReusableInputs(caller.WorkflowCall.With, metadata.Inputs)
		if err != nil {
			return fmt.Errorf("job %q: %w", callerKey, err)
		}
		secrets, inherit, err := resolveReusableSecrets(caller.WorkflowCall.Secrets, metadata.Secrets)
		if err != nil {
			return fmt.Errorf("job %q: %w", callerKey, err)
		}
		called, err := parsers.NewGithubParser().Parse(path)
		if err != nil {
			return fmt.Errorf("job %q: parse %s: %w", callerKey, reference, err)
		}
		state.stack = append(state.stack, path)
		err = state.expand(called, depth+1)
		state.stack = state.stack[:len(state.stack)-1]
		if err != nil {
			return fmt.Errorf("job %q: %w", callerKey, err)
		}
		children, terminals, err := expandReusableCall(callerKey, caller, reference, called, inputs, secrets, inherit)
		if err != nil {
			return err
		}
		delete(pipeline.Jobs, callerKey)
		for key, child := range children {
			if _, exists := pipeline.Jobs[key]; exists {
				return fmt.Errorf("job %q: expanded job key %q conflicts with caller workflow", callerKey, key)
			}
			pipeline.Jobs[key] = child
		}
		replacements[callerKey] = terminals
	}
	for _, job := range pipeline.Jobs {
		job.Needs = rewriteReusableDependencies(job.Needs, replacements, nil)
		job.Requires = rewriteReusableDependencies(job.Requires, replacements, nil)
	}
	return nil
}

func (state *reusableExpansionState) resolve(reference string) (string, string, error) {
	if strings.Contains(reference, "\\") || strings.Contains(reference, "@") || !strings.HasPrefix(reference, "./.github/workflows/") {
		return "", "", fmt.Errorf("invalid local reusable workflow reference %q", reference)
	}
	relative := strings.TrimPrefix(reference, "./")
	clean := filepath.Clean(filepath.FromSlash(relative))
	workflowRoot := filepath.Join(state.root, ".github", "workflows")
	candidate := filepath.Join(state.root, clean)
	if filepath.Dir(candidate) != workflowRoot {
		return "", "", errors.New("reusable workflows must be direct files in .github/workflows")
	}
	extension := strings.ToLower(filepath.Ext(candidate))
	if extension != ".yml" && extension != ".yaml" {
		return "", "", errors.New("reusable workflow must use .yml or .yaml")
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", "", fmt.Errorf("open reusable workflow %q: %w", reference, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", errors.New("reusable workflow must be a regular non-symlink file")
	}
	if info.Size() > maxReusableWorkflowBytes {
		return "", "", fmt.Errorf("reusable workflow exceeds %d bytes", maxReusableWorkflowBytes)
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve reusable workflow: %w", err)
	}
	contained, err := filepath.Rel(workflowRoot, canonical)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", "", errors.New("reusable workflow leaves .github/workflows")
	}
	return canonical, "./" + filepath.ToSlash(clean), nil
}

func isLocalReusableWorkflow(reference string) bool {
	lower := strings.ToLower(strings.TrimSpace(reference))
	return strings.HasPrefix(lower, "./.github/workflows/") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml"))
}

func readReusableWorkflowMetadata(path string) (reusableWorkflowMetadata, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return reusableWorkflowMetadata{}, err
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return reusableWorkflowMetadata{}, fmt.Errorf("decode workflow_call contract: %w", err)
	}
	on, exists := document["on"]
	if !exists {
		return reusableWorkflowMetadata{}, errors.New("called workflow does not declare workflow_call")
	}
	metadata := reusableWorkflowMetadata{Inputs: make(map[string]reusableInput), Secrets: make(map[string]reusableSecret)}
	switch value := on.(type) {
	case string:
		if value != "workflow_call" {
			return reusableWorkflowMetadata{}, errors.New("called workflow does not declare workflow_call")
		}
		return metadata, nil
	case map[string]interface{}:
		contract, exists := value["workflow_call"]
		if !exists {
			return reusableWorkflowMetadata{}, errors.New("called workflow does not declare workflow_call")
		}
		if contract == nil {
			return metadata, nil
		}
		configuration, ok := contract.(map[string]interface{})
		if !ok {
			return reusableWorkflowMetadata{}, errors.New("workflow_call contract must be an object")
		}
		if values, ok := configuration["inputs"].(map[string]interface{}); ok {
			for name, raw := range values {
				item, _ := raw.(map[string]interface{})
				metadata.Inputs[name] = reusableInput{Default: item["default"], Required: reusableBoolValue(item["required"]), Type: reusableStringValue(item["type"])}
			}
		}
		if values, ok := configuration["secrets"].(map[string]interface{}); ok {
			for name, raw := range values {
				item, _ := raw.(map[string]interface{})
				metadata.Secrets[name] = reusableSecret{Required: reusableBoolValue(item["required"])}
			}
		}
		return metadata, nil
	default:
		return reusableWorkflowMetadata{}, errors.New("called workflow does not declare workflow_call")
	}
}

func resolveReusableInputs(passed map[string]interface{}, definitions map[string]reusableInput) (map[string]string, error) {
	for name := range passed {
		if _, exists := definitions[name]; !exists {
			return nil, fmt.Errorf("input %q is not declared by workflow_call", name)
		}
	}
	result := make(map[string]string, len(definitions))
	for _, name := range sortedReusableKeys(definitions) {
		definition := definitions[name]
		value, provided := passed[name]
		if !provided {
			value = definition.Default
		}
		if value == nil {
			if definition.Required {
				return nil, fmt.Errorf("required input %q was not provided", name)
			}
			switch strings.ToLower(definition.Type) {
			case "boolean":
				value = false
			case "number":
				value = 0
			default:
				value = ""
			}
		}
		normalized, err := normalizeReusableInput(value, definition.Type)
		if err != nil {
			return nil, fmt.Errorf("input %q: %w", name, err)
		}
		result[name] = normalized
	}
	return result, nil
}

func resolveReusableSecrets(passed map[string]string, definitions map[string]reusableSecret) (map[string]string, bool, error) {
	if passed["*"] == "inherit" {
		return nil, true, nil
	}
	for name := range passed {
		if _, exists := definitions[name]; !exists {
			return nil, false, fmt.Errorf("secret %q is not declared by workflow_call", name)
		}
	}
	result := make(map[string]string, len(definitions))
	for name, definition := range definitions {
		value, provided := passed[name]
		if !provided && definition.Required {
			return nil, false, fmt.Errorf("required secret %q was not provided", name)
		}
		result[name] = value
	}
	return result, false, nil
}

func expandReusableCall(callerKey string, caller *types.Job, reference string, called *types.Pipeline, inputs, secrets map[string]string, inherit bool) (map[string]*types.Job, []string, error) {
	if len(called.Jobs) == 0 {
		return nil, nil, fmt.Errorf("job %q: called workflow %s has no jobs", callerKey, reference)
	}
	calledKeys := make([]string, 0, len(called.Jobs))
	keyMap := make(map[string]string, len(called.Jobs))
	for key := range called.Jobs {
		calledKeys = append(calledKeys, key)
		keyMap[key] = callerKey + "/" + key
	}
	sort.Strings(calledKeys)
	referenced := make(map[string]bool)
	for _, child := range called.Jobs {
		for _, dependency := range append(append([]string(nil), child.Needs...), child.Requires...) {
			referenced[dependency] = true
		}
	}
	resolvedWith := make(map[string]interface{}, len(inputs))
	for key, value := range inputs {
		resolvedWith[key] = value
	}
	call := &types.WorkflowCall{Uses: reference, With: resolvedWith, Secrets: copyStringMap(secrets)}
	if inherit {
		call.Secrets = map[string]string{"*": "inherit"}
	}
	children := make(map[string]*types.Job, len(called.Jobs))
	var terminals []string
	for _, oldKey := range calledKeys {
		child := called.Jobs[oldKey]
		if child == nil {
			return nil, nil, fmt.Errorf("job %q: called job %q is nil", callerKey, oldKey)
		}
		child.Environment = mergeReusableEnvironment(called.Environment, child.Environment)
		if err := resolveReusableJobTemplates(child, inputs, secrets, inherit); err != nil {
			return nil, nil, fmt.Errorf("job %q/%s: %w", callerKey, oldKey, err)
		}
		root := len(child.Needs) == 0 && len(child.Requires) == 0
		child.Needs = namespaceReusableDependencies(child.Needs, keyMap)
		child.Requires = namespaceReusableDependencies(child.Requires, keyMap)
		if root {
			child.Needs = append(child.Needs, caller.Needs...)
			child.Requires = append(child.Requires, caller.Requires...)
		}
		child.If = combineReusableConditions(caller.If, child.If)
		if child.Name == "" {
			child.Name = oldKey
		}
		if caller.Name != "" {
			child.Name = caller.Name + " / " + child.Name
		}
		if child.WorkflowCall == nil {
			child.WorkflowCall = copyWorkflowCall(call)
		} else {
			child.WorkflowCall.Uses = reference + " > " + child.WorkflowCall.Uses
		}
		newKey := keyMap[oldKey]
		children[newKey] = child
		if !referenced[oldKey] {
			terminals = append(terminals, newKey)
			if caller.ContinueOnErr || caller.AllowFailure {
				child.ContinueOnErr = true
			}
		}
	}
	sort.Strings(terminals)
	return children, terminals, nil
}

func resolveReusableJobTemplates(job *types.Job, inputs, secrets map[string]string, inherit bool) error {
	resolver := func(value string) (string, error) {
		var resolveErr error
		value = reusableInputExpression.ReplaceAllStringFunc(value, func(expression string) string {
			matches := reusableInputExpression.FindStringSubmatch(expression)
			resolved, exists := inputs[matches[1]]
			if !exists {
				resolveErr = fmt.Errorf("input %q is not declared", matches[1])
				return expression
			}
			return resolved
		})
		if resolveErr != nil || inherit {
			return value, resolveErr
		}
		value = reusableSecretExpression.ReplaceAllStringFunc(value, func(expression string) string {
			matches := reusableSecretExpression.FindStringSubmatch(expression)
			resolved, exists := secrets[matches[1]]
			if !exists {
				resolveErr = fmt.Errorf("secret %q is not declared", matches[1])
				return expression
			}
			return resolved
		})
		return value, resolveErr
	}
	return resolveReusableStrings(reflect.ValueOf(job), resolver)
}

func resolveReusableStrings(value reflect.Value, resolver func(string) (string, error)) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return resolveReusableStrings(value.Elem(), resolver)
	}
	switch value.Kind() {
	case reflect.String:
		if value.CanSet() {
			resolved, err := resolver(value.String())
			if err != nil {
				return err
			}
			value.SetString(resolved)
		}
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		copy := reflect.New(value.Elem().Type()).Elem()
		copy.Set(value.Elem())
		if err := resolveReusableStrings(copy, resolver); err != nil {
			return err
		}
		value.Set(copy)
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if err := resolveReusableStrings(value.Field(index), resolver); err != nil {
				return err
			}
		}
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			if err := resolveReusableStrings(value.Index(index), resolver); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, key := range value.MapKeys() {
			item := value.MapIndex(key)
			copy := reflect.New(item.Type()).Elem()
			copy.Set(item)
			if err := resolveReusableStrings(copy, resolver); err != nil {
				return err
			}
			value.SetMapIndex(key, copy)
		}
	}
	return nil
}

func rewriteReusableDependencies(values []string, replacements map[string][]string, seen map[string]bool) []string {
	var result []string
	for _, value := range values {
		replacement, exists := replacements[value]
		if !exists {
			result = append(result, value)
			continue
		}
		if seen == nil {
			seen = make(map[string]bool)
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, rewriteReusableDependencies(replacement, replacements, seen)...)
		delete(seen, value)
	}
	return sortedUnique(result)
}

func namespaceReusableDependencies(values []string, keys map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if mapped, exists := keys[value]; exists {
			result = append(result, mapped)
		} else {
			result = append(result, value)
		}
	}
	return sortedUnique(result)
}

func resolveMatrixWorkflowCall(call *types.WorkflowCall, matrix map[string]string) error {
	if call == nil {
		return nil
	}
	resolved, err := executionsemantics.ResolveMatrixTemplate(call.Uses, matrix)
	if err != nil {
		return err
	}
	call.Uses = resolved
	for key, value := range call.With {
		text, ok := value.(string)
		if !ok {
			continue
		}
		resolved, err := executionsemantics.ResolveMatrixTemplate(text, matrix)
		if err != nil {
			return err
		}
		call.With[key] = resolved
	}
	return nil
}

func normalizeReusableInput(value interface{}, kind string) (string, error) {
	if text, ok := value.(string); ok && strings.Contains(text, "${{") {
		return text, nil
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "string":
		switch value.(type) {
		case map[string]interface{}, []interface{}:
			return "", errors.New("must be a scalar string")
		default:
			return fmt.Sprintf("%v", value), nil
		}
	case "boolean":
		switch typed := value.(type) {
		case bool:
			return strconv.FormatBool(typed), nil
		case string:
			parsed, err := strconv.ParseBool(typed)
			if err != nil {
				return "", errors.New("must be boolean")
			}
			return strconv.FormatBool(parsed), nil
		default:
			return "", errors.New("must be boolean")
		}
	case "number":
		text := fmt.Sprintf("%v", value)
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return "", errors.New("must be numeric")
		}
		return text, nil
	default:
		return "", fmt.Errorf("uses unsupported type %q", kind)
	}
}

func mergeReusableEnvironment(global, local map[string]string) map[string]string {
	result := make(map[string]string, len(global)+len(local))
	for key, value := range global {
		result[key] = value
	}
	for key, value := range local {
		result[key] = value
	}
	return result
}

func combineReusableConditions(caller, child string) string {
	caller, child = strings.TrimSpace(caller), strings.TrimSpace(child)
	if caller == "" {
		return child
	}
	if child == "" {
		return caller
	}
	return "(" + caller + ") && (" + child + ")"
}

func sortedReusableKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func reusableBoolValue(value interface{}) bool {
	result, _ := value.(bool)
	return result
}

func reusableStringValue(value interface{}) string {
	result, _ := value.(string)
	return result
}
