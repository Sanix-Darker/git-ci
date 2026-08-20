package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	"gopkg.in/yaml.v3"
)

const (
	maxCompositeActionDepth     = 10
	maxCompositeActionUnique    = 50
	maxCompositeManifestBytes   = 1 << 20
	containerCompositeWorkspace = "/workspace"
)

var (
	compositeInputExpression      = regexp.MustCompile(`\$\{\{\s*inputs\.([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}`)
	compositeStepOutputExpression = regexp.MustCompile(`steps\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs\.`)
)

type compositeActionManifest struct {
	Name    string                           `yaml:"name"`
	Inputs  map[string]compositeActionInput  `yaml:"inputs"`
	Outputs map[string]compositeActionOutput `yaml:"outputs"`
	Runs    compositeActionRuns              `yaml:"runs"`
}

type compositeActionOutput struct {
	Description string `yaml:"description"`
	Value       string `yaml:"value"`
}

type compositeActionInput struct {
	Description string      `yaml:"description"`
	Required    bool        `yaml:"required"`
	Default     interface{} `yaml:"default"`
}

type compositeActionRuns struct {
	Using string       `yaml:"using"`
	Steps []types.Step `yaml:"steps"`
}

type compositeExpansionState struct {
	root     string
	unique   map[string]struct{}
	sequence int
}

func expandGitHubLocalCalls(root, workflowPath string, pipeline *types.Pipeline) error {
	if err := expandLocalReusableWorkflows(root, workflowPath, pipeline); err != nil {
		return err
	}
	return expandLocalCompositeActions(root, pipeline)
}

func expandLocalCompositeActions(root string, pipeline *types.Pipeline) error {
	if pipeline == nil {
		return nil
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("local composite actions: resolve project root: %w", err)
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return fmt.Errorf("local composite actions: resolve absolute project root: %w", err)
	}
	state := &compositeExpansionState{root: filepath.Clean(canonicalRoot), unique: make(map[string]struct{})}
	keys := make([]string, 0, len(pipeline.Jobs))
	for key := range pipeline.Jobs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		job := pipeline.Jobs[key]
		if job == nil {
			continue
		}
		runtimeRoot := state.root
		if job.Container != nil {
			runtimeRoot = containerCompositeWorkspace
		}
		expanded, err := state.expandSteps(job.Steps, runtimeRoot, nil, 0)
		if err != nil {
			return fmt.Errorf("local composite actions: job %q: %w", key, err)
		}
		job.Steps = expanded
	}
	return nil
}

func (s *compositeExpansionState) expandSteps(steps []types.Step, runtimeRoot string, stack []string, depth int) ([]types.Step, error) {
	expanded := make([]types.Step, 0, len(steps))
	for index, step := range steps {
		ref := strings.TrimSpace(step.Uses)
		if strings.HasPrefix(ref, `.\`) {
			return nil, fmt.Errorf("step %d uses a backslash local action reference %q", index+1, ref)
		}
		if !strings.HasPrefix(ref, "./") {
			expanded = append(expanded, step)
			continue
		}
		children, err := s.expandAction(step, ref, runtimeRoot, stack, depth)
		if err != nil {
			return nil, fmt.Errorf("step %q: %w", compositeStepLabel(step, index), err)
		}
		expanded = append(expanded, children...)
	}
	return expanded, nil
}

func (s *compositeExpansionState) expandAction(caller types.Step, ref, runtimeRoot string, stack []string, depth int) ([]types.Step, error) {
	if depth >= maxCompositeActionDepth {
		return nil, fmt.Errorf("local action nesting exceeds %d levels", maxCompositeActionDepth)
	}
	if caller.ContinueOnErr {
		return nil, fmt.Errorf("continue-on-error on a composite action caller is not supported yet")
	}
	if caller.TimeoutMin > 0 || strings.TrimSpace(caller.Timeout) != "" {
		return nil, fmt.Errorf("timeout on a composite action caller is not supported yet")
	}
	relative, err := normalizeCompositeReference(ref)
	if err != nil {
		return nil, err
	}
	for _, active := range stack {
		if active == relative {
			chain := append(append([]string(nil), stack...), relative)
			return nil, fmt.Errorf("local action cycle: %s", strings.Join(chain, " -> "))
		}
	}
	if _, exists := s.unique[relative]; !exists {
		if len(s.unique) >= maxCompositeActionUnique {
			return nil, fmt.Errorf("workflow references more than %d unique local actions", maxCompositeActionUnique)
		}
		s.unique[relative] = struct{}{}
	}
	manifest, _, err := s.loadManifest(relative)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(manifest.Runs.Using), "composite") {
		return nil, fmt.Errorf("%s must declare runs.using: composite", ref)
	}
	if len(manifest.Runs.Steps) == 0 {
		return nil, fmt.Errorf("%s has no composite steps", ref)
	}
	inputs, err := resolveCompositeInputs(ref, manifest.Inputs, caller.With)
	if err != nil {
		return nil, err
	}
	provenance := strings.TrimSpace(caller.Name)
	if provenance == "" {
		provenance = strings.TrimSpace(manifest.Name)
	}
	if provenance == "" {
		provenance = ref
	}
	runtimeActionPath := filepath.Join(runtimeRoot, filepath.FromSlash(relative))
	if runtime.GOOS == "windows" && runtimeRoot == containerCompositeWorkspace {
		runtimeActionPath = path.Join(runtimeRoot, relative)
	}
	s.sequence++
	scope := fmt.Sprintf("__gci_action_%03d_", s.sequence)
	templates := make([]types.Step, 0, len(manifest.Runs.Steps))
	stepIDs := make(map[string]string)
	for index, template := range manifest.Runs.Steps {
		child, err := resolveCompositeStep(template, inputs)
		if err != nil {
			return nil, fmt.Errorf("%s step %d: %w", ref, index+1, err)
		}
		if (strings.TrimSpace(child.Run) == "") == (strings.TrimSpace(child.Uses) == "") {
			return nil, fmt.Errorf("%s step %d must define exactly one of run or uses", ref, index+1)
		}
		if child.Run != "" && strings.TrimSpace(child.Shell) == "" {
			return nil, fmt.Errorf("%s step %d run command requires shell", ref, index+1)
		}
		if child.ID != "" {
			if !outputNamePattern.MatchString(child.ID) {
				return nil, fmt.Errorf("%s step %d has invalid id %q", ref, index+1, child.ID)
			}
			if _, duplicate := stepIDs[child.ID]; duplicate {
				return nil, fmt.Errorf("%s declares duplicate step id %q", ref, child.ID)
			}
			stepIDs[child.ID] = scope + child.ID
			child.ID = stepIDs[child.ID]
		}
		templates = append(templates, child)
	}

	result := make([]types.Step, 0, len(templates)+1)
	active := append(append([]string(nil), stack...), relative)
	for index, child := range templates {
		child = rewriteCompositeStepOutputs(child, stepIDs)
		child.Env = mergeCompositeEnvironment(caller.Env, child.Env)
		child.Env["GITHUB_ACTION_PATH"] = runtimeActionPath
		child.If = combineCompositeConditions(caller.If, child.If)
		if strings.TrimSpace(child.Name) == "" {
			child.Name = fmt.Sprintf("STEP %02d", index+1)
		}
		nested, err := s.expandSteps([]types.Step{child}, runtimeRoot, active, depth+1)
		if err != nil {
			return nil, err
		}
		for nestedIndex := range nested {
			nested[nestedIndex].Name = provenance + " / " + nested[nestedIndex].Name
		}
		result = append(result, nested...)
	}
	if len(manifest.Outputs) > 0 {
		mappings := make(map[string]string, len(manifest.Outputs))
		keys := make([]string, 0, len(manifest.Outputs))
		for key := range manifest.Outputs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !outputNamePattern.MatchString(key) {
				return nil, fmt.Errorf("%s declares invalid output name %q", ref, key)
			}
			value := strings.TrimSpace(manifest.Outputs[key].Value)
			if value == "" {
				return nil, fmt.Errorf("%s output %q requires a value", ref, key)
			}
			value, err = resolveCompositeString(value, inputs)
			if err != nil {
				return nil, fmt.Errorf("%s output %q: %w", ref, key, err)
			}
			mappings[key] = rewriteCompositeStepOutputString(value, stepIDs)
		}
		if strings.TrimSpace(caller.ID) != "" {
			encoded, err := json.Marshal(mappings)
			if err != nil {
				return nil, fmt.Errorf("%s encode output mappings: %w", ref, err)
			}
			environment := mergeCompositeEnvironment(caller.Env, nil)
			environment[stepOutputMappingsEnvironment] = string(encoded)
			result = append(result, types.Step{
				ID: caller.ID, Name: provenance + " / OUTPUTS", Run: ":", Shell: "sh",
				Env: environment, If: caller.If,
			})
		}
	}
	return result, nil
}

func rewriteCompositeStepOutputs(step types.Step, identifiers map[string]string) types.Step {
	rewrite := func(value string) string { return rewriteCompositeStepOutputString(value, identifiers) }
	step.Name = rewrite(step.Name)
	step.Run = rewrite(step.Run)
	step.Uses = rewrite(step.Uses)
	step.Command = rewrite(step.Command)
	step.Task = rewrite(step.Task)
	step.If = rewrite(step.If)
	step.When = rewrite(step.When)
	step.Shell = rewrite(step.Shell)
	step.WorkingDir = rewrite(step.WorkingDir)
	for index := range step.Script {
		step.Script[index] = rewrite(step.Script[index])
	}
	for index := range step.Arguments {
		step.Arguments[index] = rewrite(step.Arguments[index])
	}
	for key, value := range step.Env {
		step.Env[key] = rewrite(value)
	}
	for key, value := range step.With {
		step.With[key] = rewrite(value)
	}
	for key, value := range step.Parameters {
		step.Parameters[key] = rewrite(value)
	}
	for key, value := range step.Inputs {
		step.Inputs[key] = rewrite(value)
	}
	return step
}

func rewriteCompositeStepOutputString(value string, identifiers map[string]string) string {
	return compositeStepOutputExpression.ReplaceAllStringFunc(value, func(expression string) string {
		matches := compositeStepOutputExpression.FindStringSubmatch(expression)
		if len(matches) != 2 {
			return expression
		}
		identifier, exists := identifiers[matches[1]]
		if !exists {
			return expression
		}
		return "steps." + identifier + ".outputs."
	})
}

func (s *compositeExpansionState) loadManifest(relative string) (compositeActionManifest, string, error) {
	actionDirectory := filepath.Join(s.root, filepath.FromSlash(relative))
	current := s.root
	for _, segment := range strings.Split(relative, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return compositeActionManifest{}, "", fmt.Errorf("inspect local action path %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return compositeActionManifest{}, "", fmt.Errorf("local action path %q contains a symlink", relative)
		}
	}
	info, err := os.Stat(actionDirectory)
	if err != nil || !info.IsDir() {
		return compositeActionManifest{}, "", fmt.Errorf("local action %q is not a directory", relative)
	}
	manifestPath := ""
	for _, name := range []string{"action.yml", "action.yaml"} {
		candidate := filepath.Join(actionDirectory, name)
		info, err := os.Lstat(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return compositeActionManifest{}, "", fmt.Errorf("inspect %s: %w", candidate, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return compositeActionManifest{}, "", fmt.Errorf("local action manifest %q must be a regular non-symlink file", candidate)
		}
		if info.Size() > maxCompositeManifestBytes {
			return compositeActionManifest{}, "", fmt.Errorf("local action manifest %q exceeds %d bytes", candidate, maxCompositeManifestBytes)
		}
		manifestPath = candidate
		break
	}
	if manifestPath == "" {
		return compositeActionManifest{}, "", fmt.Errorf("local action %q has no action.yml or action.yaml", relative)
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return compositeActionManifest{}, "", fmt.Errorf("open local action manifest: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxCompositeManifestBytes+1))
	if err != nil {
		return compositeActionManifest{}, "", fmt.Errorf("read local action manifest: %w", err)
	}
	if len(content) > maxCompositeManifestBytes {
		return compositeActionManifest{}, "", fmt.Errorf("local action manifest %q exceeds %d bytes", manifestPath, maxCompositeManifestBytes)
	}
	var manifest compositeActionManifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return compositeActionManifest{}, "", fmt.Errorf("parse local action manifest %q: %w", manifestPath, err)
	}
	return manifest, actionDirectory, nil
}

func normalizeCompositeReference(ref string) (string, error) {
	if strings.ContainsRune(ref, '\x00') || strings.Contains(ref, "\\") || strings.Contains(ref, "@") {
		return "", fmt.Errorf("unsafe local action reference %q", ref)
	}
	relative := strings.TrimPrefix(ref, "./")
	cleaned := path.Clean(relative)
	if relative == "" || cleaned == "." || cleaned != relative || path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe local action reference %q", ref)
	}
	return cleaned, nil
}

func resolveCompositeInputs(ref string, definitions map[string]compositeActionInput, provided map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(definitions)+len(provided))
	for key, value := range provided {
		result[key] = value
	}
	keys := make([]string, 0, len(definitions))
	for key := range definitions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := provided[key]; exists {
			continue
		}
		definition := definitions[key]
		if definition.Default != nil {
			value, err := compositeScalar(definition.Default)
			if err != nil {
				return nil, fmt.Errorf("%s input %q default: %w", ref, key, err)
			}
			result[key] = value
			continue
		}
		if definition.Required {
			return nil, fmt.Errorf("%s requires input %q", ref, key)
		}
		result[key] = ""
	}
	return result, nil
}

func compositeScalar(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(typed), nil
	default:
		return "", fmt.Errorf("must be a scalar, got %T", value)
	}
}

func resolveCompositeStep(step types.Step, inputs map[string]string) (types.Step, error) {
	var err error
	resolve := func(value string) string {
		if err != nil {
			return value
		}
		value, err = resolveCompositeString(value, inputs)
		return value
	}
	step.ID = resolve(step.ID)
	step.Name = resolve(step.Name)
	step.Run = resolve(step.Run)
	step.Uses = resolve(step.Uses)
	step.Command = resolve(step.Command)
	step.Task = resolve(step.Task)
	step.If = resolve(step.If)
	step.When = resolve(step.When)
	step.Shell = resolve(step.Shell)
	step.WorkingDir = resolve(step.WorkingDir)
	for index := range step.Script {
		step.Script[index] = resolve(step.Script[index])
	}
	for index := range step.Arguments {
		step.Arguments[index] = resolve(step.Arguments[index])
	}
	for key, value := range step.Env {
		step.Env[key] = resolve(value)
	}
	for key, value := range step.With {
		step.With[key] = resolve(value)
	}
	for key, value := range step.Parameters {
		step.Parameters[key] = resolve(value)
	}
	for key, value := range step.Inputs {
		step.Inputs[key] = resolve(value)
	}
	if err != nil {
		return types.Step{}, err
	}
	return step, nil
}

func resolveCompositeString(value string, inputs map[string]string) (string, error) {
	missing := ""
	resolved := compositeInputExpression.ReplaceAllStringFunc(value, func(expression string) string {
		matches := compositeInputExpression.FindStringSubmatch(expression)
		if len(matches) != 2 {
			return expression
		}
		input, exists := inputs[matches[1]]
		if !exists {
			missing = matches[1]
			return expression
		}
		return input
	})
	if missing != "" {
		return "", fmt.Errorf("references undeclared input %q", missing)
	}
	if strings.Contains(resolved, "${{ inputs[") {
		return "", fmt.Errorf("bracket input expressions are not supported")
	}
	return resolved, nil
}

func mergeCompositeEnvironment(parent, child map[string]string) map[string]string {
	result := make(map[string]string, len(parent)+len(child)+1)
	for key, value := range parent {
		result[key] = value
	}
	for key, value := range child {
		result[key] = value
	}
	return result
}

func combineCompositeConditions(parent, child string) string {
	parent = strings.TrimSpace(parent)
	child = strings.TrimSpace(child)
	if parent == "" {
		return child
	}
	if child == "" {
		return parent
	}
	return fmt.Sprintf("${{ (%s) && (%s) }}", unwrapCompositeExpression(parent), unwrapCompositeExpression(child))
}

func unwrapCompositeExpression(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${{") && strings.HasSuffix(value, "}}") {
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "${{"), "}}"))
	}
	return value
}

func compositeStepLabel(step types.Step, index int) string {
	if strings.TrimSpace(step.Name) != "" {
		return step.Name
	}
	return fmt.Sprintf("STEP %02d", index+1)
}
