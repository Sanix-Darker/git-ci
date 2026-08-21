// Package execution discovers normalized CI definitions from registered local
// projects. It intentionally accepts store project records instead of arbitrary
// filesystem roots so callers cannot expand the service's discovery boundary.
package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/internal/executionsemantics"
	"github.com/sanix-darker/git-ci/internal/parsers"
	"github.com/sanix-darker/git-ci/internal/runnerinventory"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
	"github.com/sanix-darker/git-ci/pkg/types"
	yaml "gopkg.in/yaml.v3"
)

// Provider identifies the CI provider that produced a Definition.
type Provider string

const (
	// ProviderGitHubActions is the normalized provider identifier for GitHub
	// Actions workflow files.
	ProviderGitHubActions Provider = "github"
	// ProviderGitLabCI is the normalized provider identifier for GitLab CI
	// configuration files.
	ProviderGitLabCI Provider = "gitlab"
)

// Definition is a provider-neutral workflow definition ready to be stored by
// a control plane. File is always a slash-separated path relative to
// ProjectPath, while Key is stable for a registered project and source file.
type Definition struct {
	Key              string                 `json:"key"`
	ProjectID        string                 `json:"projectId,omitempty"`
	ProjectSlug      string                 `json:"projectSlug,omitempty"`
	ProjectPath      string                 `json:"projectPath"`
	Provider         Provider               `json:"provider"`
	File             string                 `json:"file"`
	Name             string                 `json:"name"`
	Environment      map[string]string      `json:"environment"`
	Stages           []string               `json:"stages"`
	Triggers         []string               `json:"triggers"`
	TriggerPolicies  []triggerpolicy.Policy `json:"triggerPolicies,omitempty"`
	Concurrency      *ConcurrencyDefinition `json:"concurrency,omitempty"`
	Jobs             []JobDefinition        `json:"jobs"`
	TopologicalOrder []string               `json:"topologicalOrder"`
}

// ChildPipelineDefinition is frozen into its trigger job. Definition is the
// complete normalized downstream DAG, so execution never reparses a mutable
// checkout after the parent run has been dispatched.
type ChildPipelineDefinition struct {
	SourceFile               string            `json:"sourceFile"`
	Strategy                 string            `json:"strategy"`
	Depth                    int               `json:"depth"`
	InheritVariables         bool              `json:"inheritVariables"`
	ForwardYAMLVariables     bool              `json:"forwardYAMLVariables"`
	ForwardPipelineVariables bool              `json:"forwardPipelineVariables"`
	Variables                map[string]string `json:"variables,omitempty"`
	Definition               *Definition       `json:"definition"`
}

// JobDefinition is a provider-neutral, persistence-ready job. Key is the
// source job identifier; Needs and Requires therefore reference keys rather
// than presentation names.
type JobDefinition struct {
	Retry                *types.RetryPolicy
	Key                  string                               `json:"key"`
	SourceKey            string                               `json:"sourceKey,omitempty"`
	Name                 string                               `json:"name"`
	Environment          map[string]string                    `json:"environment"`
	EnvironmentName      string                               `json:"environmentName,omitempty"`
	DeploymentTier       string                               `json:"deploymentTier,omitempty"`
	Needs                []string                             `json:"needs"`
	Requires             []string                             `json:"requires"`
	ArtifactDependencies []string                             `json:"artifactDependencies,omitempty"`
	DependenciesDefined  bool                                 `json:"dependenciesDefined,omitempty"`
	NeedsArtifacts       map[string]bool                      `json:"needsArtifacts,omitempty"`
	Stage                string                               `json:"stage,omitempty"`
	RunnerHint           string                               `json:"runnerHint,omitempty"`
	RunnerRequirements   []string                             `json:"runnerRequirements,omitempty"`
	RunnerGroup          string                               `json:"runnerGroup,omitempty"`
	RunnerMatch          runnerinventory.Match                `json:"runnerMatch"`
	AllowFailure         bool                                 `json:"allowFailure"`
	TimeoutMinutes       int                                  `json:"timeoutMinutes,omitempty"`
	RollbackCommand      string                               `json:"rollbackCommand,omitempty"`
	VerifyCommand        string                               `json:"verifyCommand,omitempty"`
	Matrix               map[string]string                    `json:"matrix,omitempty"`
	MatrixIndex          int                                  `json:"matrixIndex,omitempty"`
	MatrixTotal          int                                  `json:"matrixTotal,omitempty"`
	MatrixLabel          string                               `json:"matrixLabel,omitempty"`
	Condition            executionsemantics.ConditionContract `json:"condition"`
	Rules                []RuleDefinition                     `json:"rules,omitempty"`
	Only                 *OnlyExceptDefinition                `json:"only,omitempty"`
	Except               *OnlyExceptDefinition                `json:"except,omitempty"`
	When                 string                               `json:"when,omitempty"`
	ManualConfirmation   string                               `json:"manualConfirmation,omitempty"`
	Concurrency          *ConcurrencyDefinition               `json:"concurrency,omitempty"`
	Interruptible        bool                                 `json:"interruptible,omitempty"`
	FailFast             bool                                 `json:"failFast,omitempty"`
	MaxParallel          int                                  `json:"maxParallel,omitempty"`
	WorkflowCall         *types.WorkflowCall                  `json:"workflowCall,omitempty"`
	ChildPipeline        *ChildPipelineDefinition             `json:"childPipeline,omitempty"`
	Container            *types.Container                     `json:"container,omitempty"`
	Services             map[string]*types.Service            `json:"services,omitempty"`
	Artifacts            *types.ArtifactConfig                `json:"artifacts,omitempty"`
	Cache                *types.CacheConfig                   `json:"cache,omitempty"`
	Outputs              map[string]string                    `json:"outputs,omitempty"`
	Steps                []StepDefinition                     `json:"steps"`
}

type ConcurrencyDefinition struct {
	Group            string `json:"group"`
	CancelInProgress bool   `json:"cancelInProgress,omitempty"`
	Limit            int    `json:"limit,omitempty"`
}

type RuleDefinition struct {
	Condition    executionsemantics.ConditionContract `json:"condition"`
	When         string                               `json:"when,omitempty"`
	Changes      []string                             `json:"changes,omitempty"`
	Exists       []string                             `json:"exists,omitempty"`
	Variables    map[string]string                    `json:"variables,omitempty"`
	AllowFailure bool                                 `json:"allowFailure,omitempty"`
}

type OnlyExceptDefinition struct {
	Refs      []string `json:"refs,omitempty"`
	Changes   []string `json:"changes,omitempty"`
	Variables []string `json:"variables,omitempty"`
}

type deploymentExtension struct {
	Rollback string `yaml:"rollback"`
	Verify   string `yaml:"verify"`
}

// StepDefinition is a provider-neutral, persistence-ready execution step.
// Command holds the shell/script command when available; Action preserves a
// provider action reference such as actions/checkout@v4.
type StepDefinition struct {
	Key              string                               `json:"key"`
	Name             string                               `json:"name"`
	Command          string                               `json:"command,omitempty"`
	Action           string                               `json:"action,omitempty"`
	Environment      map[string]string                    `json:"environment"`
	WorkingDirectory string                               `json:"workingDirectory,omitempty"`
	TimeoutMinutes   int                                  `json:"timeoutMinutes,omitempty"`
	Shell            string                               `json:"shell,omitempty"`
	AllowFailure     bool                                 `json:"allowFailure"`
	Condition        executionsemantics.ConditionContract `json:"condition"`
	Inputs           map[string]string                    `json:"inputs,omitempty"`
	Artifacts        *types.ArtifactConfig                `json:"artifacts,omitempty"`
	Cache            *types.CacheConfig                   `json:"cache,omitempty"`
}

type registeredProject struct {
	project store.Project
	path    string
}

type workflowFile struct {
	provider Provider
	relative string
	absolute string
}

// Discover discovers GitHub Actions and GitLab CI definitions from the
// canonical local paths stored on registered projects. Projects without a
// CanonicalPath are intentionally skipped: remote-only records are not a
// filesystem discovery target.
//
// Results are sorted by canonical project path, source file, and provider.
// Workflow files are checked before parsing, so symlinked paths and paths
// outside a registered project never reach a provider parser.
func Discover(projects []store.Project) ([]Definition, error) {
	registered := make([]registeredProject, 0, len(projects))
	for _, project := range projects {
		path, found, err := canonicalRegisteredPath(project)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		registered = append(registered, registeredProject{
			project: project,
			path:    path,
		})
	}

	sort.Slice(registered, func(i, j int) bool {
		left, right := registered[i], registered[j]
		if left.path != right.path {
			return left.path < right.path
		}
		if left.project.ID != right.project.ID {
			return left.project.ID < right.project.ID
		}
		if left.project.Slug != right.project.Slug {
			return left.project.Slug < right.project.Slug
		}
		return left.project.Name < right.project.Name
	})

	definitions := make([]Definition, 0)
	for _, project := range registered {
		discovered, err := discoverCanonicalProject(project.project, project.path)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, discovered...)
	}

	sort.Slice(definitions, func(i, j int) bool {
		left, right := definitions[i], definitions[j]
		if left.ProjectPath != right.ProjectPath {
			return left.ProjectPath < right.ProjectPath
		}
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Provider != right.Provider {
			return left.Provider < right.Provider
		}
		return left.Key < right.Key
	})
	return definitions, nil
}

// DiscoverProject is the single-project form of Discover. It has the same
// canonical-path and workflow-path safety guarantees.
func DiscoverProject(project store.Project) ([]Definition, error) {
	path, found, err := canonicalRegisteredPath(project)
	if err != nil {
		return nil, err
	}
	if !found {
		return []Definition{}, nil
	}
	return discoverCanonicalProject(project, path)
}

func canonicalRegisteredPath(project store.Project) (string, bool, error) {
	if project.CanonicalPath == nil {
		return "", false, nil
	}

	storedPath := *project.CanonicalPath
	if storedPath == "" {
		return "", false, fmt.Errorf("registered project %q has an empty canonical path", projectLabel(project))
	}
	if strings.TrimSpace(storedPath) != storedPath {
		return "", false, fmt.Errorf("registered project %q has a non-canonical path", projectLabel(project))
	}
	if !filepath.IsAbs(storedPath) {
		return "", false, fmt.Errorf("registered project %q canonical path must be absolute", projectLabel(project))
	}
	if filepath.Clean(storedPath) != storedPath {
		return "", false, fmt.Errorf("registered project %q canonical path must be clean", projectLabel(project))
	}

	resolvedPath, err := filepath.EvalSymlinks(storedPath)
	if err != nil {
		return "", false, fmt.Errorf("resolve registered project %q canonical path: %w", projectLabel(project), err)
	}
	resolvedPath = filepath.Clean(resolvedPath)
	if resolvedPath != storedPath {
		return "", false, fmt.Errorf("registered project %q path is not canonical", projectLabel(project))
	}

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", false, fmt.Errorf("stat registered project %q canonical path: %w", projectLabel(project), err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("registered project %q canonical path is not a directory", projectLabel(project))
	}
	return resolvedPath, true, nil
}

func projectLabel(project store.Project) string {
	switch {
	case project.ID != "":
		return project.ID
	case project.Slug != "":
		return project.Slug
	case project.Name != "":
		return project.Name
	default:
		return "<unknown>"
	}
}

func discoverCanonicalProject(project store.Project, root string) ([]Definition, error) {
	files, err := discoverWorkflowFiles(root)
	if err != nil {
		return nil, fmt.Errorf("discover workflows for project %q: %w", projectLabel(project), err)
	}

	definitions := make([]Definition, 0, len(files))
	for _, file := range files {
		pipeline, err := parseWorkflow(root, file)
		if err != nil {
			return nil, fmt.Errorf(
				"parse %s workflow %q for project %q: %w",
				file.provider,
				file.relative,
				projectLabel(project),
				err,
			)
		}
		extensions, err := readDeploymentExtensions(file)
		if err != nil {
			return nil, fmt.Errorf("parse git-ci extensions in %q: %w", file.relative, err)
		}

		definition, err := normalizeDefinition(project, root, file, pipeline, extensions)
		if err != nil {
			return nil, fmt.Errorf(
				"normalize %s workflow %q for project %q: %w",
				file.provider,
				file.relative,
				projectLabel(project),
				err,
			)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func discoverWorkflowFiles(root string) ([]workflowFile, error) {
	files := make([]workflowFile, 0)

	workflowDirectory, exists, err := safeExistingPath(root, filepath.Join(".github", "workflows"), true)
	if err != nil {
		return nil, err
	}
	if exists {
		entries, err := os.ReadDir(workflowDirectory)
		if err != nil {
			return nil, fmt.Errorf("read GitHub workflow directory: %w", err)
		}
		for _, entry := range entries {
			name := entry.Name()
			extension := filepath.Ext(name)
			if extension != ".yml" && extension != ".yaml" {
				continue
			}

			relative := filepath.Join(".github", "workflows", name)
			absolute, safe, err := safeExistingPath(root, relative, false)
			if err != nil {
				return nil, err
			}
			if !safe {
				continue
			}
			files = append(files, workflowFile{
				provider: ProviderGitHubActions,
				relative: filepath.ToSlash(relative),
				absolute: absolute,
			})
		}
	}

	for _, name := range []string{".gitlab-ci.yml", ".gitlab-ci.yaml"} {
		absolute, safe, err := safeExistingPath(root, name, false)
		if err != nil {
			return nil, err
		}
		if !safe {
			continue
		}
		files = append(files, workflowFile{
			provider: ProviderGitLabCI,
			relative: name,
			absolute: absolute,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].relative != files[j].relative {
			return files[i].relative < files[j].relative
		}
		return files[i].provider < files[j].provider
	})
	return files, nil
}

func safeExistingPath(root, relative string, directory bool) (string, bool, error) {
	cleanRelative := filepath.Clean(relative)
	if cleanRelative == "." || filepath.IsAbs(cleanRelative) || !isSafeRelativePath(cleanRelative) {
		return "", false, fmt.Errorf("unsafe workflow path %q", relative)
	}

	candidate := filepath.Join(root, cleanRelative)
	if !pathWithin(root, candidate) {
		return "", false, fmt.Errorf("workflow path %q escapes its project", relative)
	}

	current := root
	components := strings.Split(cleanRelative, string(filepath.Separator))
	var info os.FileInfo
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return "", false, fmt.Errorf("unsafe workflow path %q", relative)
		}
		current = filepath.Join(current, component)
		var err error
		info, err = os.Lstat(current)
		if os.IsNotExist(err) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("lstat workflow path %q: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, nil
		}
		if index < len(components)-1 && !info.IsDir() {
			return "", false, nil
		}
	}

	if directory {
		if !info.IsDir() {
			return "", false, nil
		}
	} else if !info.Mode().IsRegular() {
		return "", false, nil
	}

	resolvedPath, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", false, fmt.Errorf("resolve workflow path %q: %w", relative, err)
	}
	if !pathWithin(root, resolvedPath) {
		return "", false, nil
	}
	return current, true, nil
}

func safeAbsoluteFile(root, candidate string) (string, bool, error) {
	if !filepath.IsAbs(candidate) {
		return "", false, nil
	}
	cleanCandidate := filepath.Clean(candidate)
	if !pathWithin(root, cleanCandidate) {
		return "", false, nil
	}
	relative, err := filepath.Rel(root, cleanCandidate)
	if err != nil {
		return "", false, fmt.Errorf("make workflow path relative: %w", err)
	}
	return safeExistingPath(root, relative, false)
}

func isSafeRelativePath(value string) bool {
	if value == ".." {
		return false
	}
	return !strings.HasPrefix(value, ".."+string(filepath.Separator))
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || isSafeRelativePath(relative)
}

func parseWorkflow(root string, file workflowFile) (*types.Pipeline, error) {
	switch file.provider {
	case ProviderGitHubActions:
		return parsers.NewGithubParser().Parse(file.absolute)
	case ProviderGitLabCI:
		if err := validateGitLabIncludes(root, file.absolute); err != nil {
			return nil, err
		}
		return parsers.NewGitlabParser().Parse(file.absolute)
	default:
		return nil, fmt.Errorf("unsupported workflow provider %q", file.provider)
	}
}

// validateGitLabIncludes prevents the existing GitLab parser's local include
// support from opening a file outside the registered project. It permits only
// local include forms that resolve to a regular, non-symlinked file beneath
// root. Remote and template includes are rejected because discovery is local,
// deterministic, and non-networked.
func validateGitLabIncludes(root, workflowPath string) error {
	content, err := os.ReadFile(workflowPath)
	if err != nil {
		return fmt.Errorf("read GitLab workflow for include validation: %w", err)
	}

	var document map[string]interface{}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return fmt.Errorf("parse GitLab include declarations: %w", err)
	}
	include, found := document["include"]
	if !found {
		return nil
	}
	return validateGitLabInclude(root, filepath.Dir(workflowPath), include)
}

func validateGitLabInclude(root, workflowDirectory string, include interface{}) error {
	switch value := include.(type) {
	case string:
		return validateGitLabAbsoluteInclude(root, value)
	case []interface{}:
		for _, item := range value {
			if err := validateGitLabInclude(root, workflowDirectory, item); err != nil {
				return err
			}
		}
		return nil
	case map[string]interface{}:
		if local, found := value["local"]; found {
			path, ok := local.(string)
			if !ok || path == "" {
				return fmt.Errorf("GitLab local include must be a non-empty string")
			}
			if filepath.IsAbs(path) {
				return fmt.Errorf("GitLab local include %q must be relative", path)
			}
			candidate := filepath.Join(workflowDirectory, path)
			if _, safe, err := safeAbsoluteFile(root, candidate); err != nil {
				return err
			} else if !safe {
				return fmt.Errorf("GitLab local include %q is not a regular file inside the project", path)
			}
			return nil
		}
		if file, found := value["file"]; found {
			path, ok := file.(string)
			if !ok || path == "" {
				return fmt.Errorf("GitLab file include must be a non-empty string")
			}
			return validateGitLabAbsoluteInclude(root, path)
		}
		return fmt.Errorf("unsupported GitLab include; only local project files are allowed")
	default:
		return fmt.Errorf("unsupported GitLab include type %T", include)
	}
}

func validateGitLabAbsoluteInclude(root, includePath string) error {
	if !filepath.IsAbs(includePath) {
		return fmt.Errorf("GitLab include %q must be an absolute project path", includePath)
	}
	if _, safe, err := safeAbsoluteFile(root, includePath); err != nil {
		return err
	} else if !safe {
		return fmt.Errorf("GitLab include %q is not a regular file inside the project", includePath)
	}
	return nil
}

func normalizeDefinition(
	project store.Project,
	root string,
	file workflowFile,
	pipeline *types.Pipeline,
	extensions map[string]deploymentExtension,
) (Definition, error) {
	state := &childPipelineExpansionState{stack: []string{file.absolute}}
	return normalizeDefinitionRecursive(project, root, file, pipeline, extensions, 0, state)
}

type childPipelineExpansionState struct {
	stack []string
	count int
}

func normalizeDefinitionRecursive(
	project store.Project,
	root string,
	file workflowFile,
	pipeline *types.Pipeline,
	extensions map[string]deploymentExtension,
	depth int,
	state *childPipelineExpansionState,
) (Definition, error) {
	if pipeline == nil {
		return Definition{}, fmt.Errorf("parser returned no pipeline")
	}
	if file.provider == ProviderGitHubActions {
		if err := expandGitHubLocalCalls(root, file.absolute, pipeline); err != nil {
			return Definition{}, fmt.Errorf("expand reusable workflows: %w", err)
		}
	}

	sourceKeys := make([]string, 0, len(pipeline.Jobs))
	for key := range pipeline.Jobs {
		sourceKeys = append(sourceKeys, key)
	}
	sort.Strings(sourceKeys)

	jobs := make(map[string]JobDefinition, len(sourceKeys))
	variantKeys := make(map[string][]string, len(sourceKeys))
	jobKeys := make([]string, 0, len(sourceKeys))
	for _, sourceKey := range sourceKeys {
		job := pipeline.Jobs[sourceKey]
		if job == nil {
			return Definition{}, fmt.Errorf("job %q is nil", sourceKey)
		}
		variants, err := executionsemantics.ExpandMatrix(job)
		if err != nil {
			return Definition{}, fmt.Errorf("job %q: %w", sourceKey, err)
		}
		if len(jobKeys)+len(variants) > 256 {
			return Definition{}, fmt.Errorf("workflow expands beyond 256 jobs")
		}
		for _, variant := range variants {
			key := executionsemantics.MatrixJobKey(sourceKey, variant)
			normalized, err := normalizeJob(key, job, extensions[sourceKey])
			if err != nil {
				return Definition{}, fmt.Errorf("job %q: %w", sourceKey, err)
			}
			normalized.SourceKey = sourceKey
			normalized.SourceKey = sourceKey
			normalized.RunnerRequirements, normalized.RunnerGroup = runnerRequirements(file.provider, job)
			if err := applyMatrixVariant(&normalized, variant, string(file.provider)); err != nil {
				return Definition{}, fmt.Errorf("job %q: %w", sourceKey, err)
			}
			jobs[key] = normalized
			variantKeys[sourceKey] = append(variantKeys[sourceKey], key)
			jobKeys = append(jobKeys, key)
		}
	}

	dependencies := make(map[string][]string, len(jobKeys))
	for _, sourceKey := range sourceKeys {
		job := pipeline.Jobs[sourceKey]
		for _, key := range variantKeys[sourceKey] {
			normalized := jobs[key]
			normalized.Needs = expandDependencyKeys(job.Needs, variantKeys)
			normalized.Requires = expandDependencyKeys(job.Requires, variantKeys)
			normalized.ArtifactDependencies = expandDependencyKeys(job.Dependencies, variantKeys)
			normalized.NeedsArtifacts = expandArtifactNeeds(job.NeedsArtifacts, variantKeys)
			jobs[key] = normalized
			dependencies[key] = sortedUnique(append(
				append([]string{}, normalized.Needs...),
				normalized.Requires...,
			))
		}
	}

	order, err := deterministicTopologicalOrder(jobKeys, dependencies)
	if err != nil {
		return Definition{}, err
	}

	projectKey := project.ID
	if projectKey == "" {
		projectKey = root
	}
	triggerPolicies, err := triggerpolicy.ParseFile(string(file.provider), file.absolute, pipeline.Triggers)
	if err != nil {
		return Definition{}, fmt.Errorf("normalize trigger policy: %w", err)
	}
	definition := Definition{
		Key:              projectKey + ":" + string(file.provider) + ":" + file.relative,
		ProjectID:        project.ID,
		ProjectSlug:      project.Slug,
		ProjectPath:      root,
		Provider:         file.provider,
		File:             file.relative,
		Name:             pipeline.Name,
		Environment:      copyStringMap(pipeline.Environment),
		Stages:           copyStringSlice(pipeline.Stages),
		Triggers:         sortedUnique(pipeline.Triggers),
		TriggerPolicies:  triggerPolicies,
		Concurrency:      normalizeConcurrency(pipeline.Concurrency),
		Jobs:             make([]JobDefinition, 0, len(order)),
		TopologicalOrder: copyStringSlice(order),
	}
	for _, key := range order {
		definition.Jobs = append(definition.Jobs, jobs[key])
	}
	if file.provider == ProviderGitLabCI {
		if err := attachLocalChildPipelines(project, root, pipeline, &definition, depth, state); err != nil {
			return Definition{}, err
		}
	}
	applyDefinitionRunnerInventory(&definition, runnerinventory.Local(runnerinventory.Config{}))
	return definition, nil
}

const (
	maxChildPipelineDepth = 2
	maxChildPipelines     = 50
)

func attachLocalChildPipelines(project store.Project, root string, pipeline *types.Pipeline, definition *Definition, depth int, state *childPipelineExpansionState) error {
	for index := range definition.Jobs {
		job := &definition.Jobs[index]
		source := pipeline.Jobs[job.SourceKey]
		if source == nil || source.Trigger == nil {
			continue
		}
		if job.MatrixTotal > 1 {
			return fmt.Errorf("job %q: matrix child pipeline triggers are not supported", job.SourceKey)
		}
		child, err := normalizeLocalChildPipeline(project, root, job.SourceKey, source, depth, state)
		if err != nil {
			return err
		}
		job.ChildPipeline = child
		job.RunnerHint = "gci-control-plane"
		job.RunnerRequirements = nil
		job.RunnerGroup = ""
	}
	return nil
}

func normalizeLocalChildPipeline(project store.Project, root, jobKey string, job *types.Job, depth int, state *childPipelineExpansionState) (*ChildPipelineDefinition, error) {
	trigger := job.Trigger
	if trigger.Project != "" || trigger.Branch != "" {
		return nil, fmt.Errorf("job %q: multi-project child pipelines are not supported", jobKey)
	}
	if trigger.IncludeCount != 1 {
		return nil, fmt.Errorf("job %q: child pipeline trigger must contain exactly one local include", jobKey)
	}
	if trigger.IncludeKind != "local" {
		return nil, fmt.Errorf("job %q: child pipeline include kind %q is not supported; use include:local", jobKey, trigger.IncludeKind)
	}
	if depth >= maxChildPipelineDepth {
		return nil, fmt.Errorf("job %q: child pipeline nesting exceeds %d levels", jobKey, maxChildPipelineDepth)
	}
	strategy := strings.ToLower(strings.TrimSpace(trigger.Strategy))
	if strategy == "" {
		strategy = "async"
	}
	if strategy != "async" && strategy != "mirror" && strategy != "depend" {
		return nil, fmt.Errorf("job %q: child pipeline strategy %q is not supported", jobKey, trigger.Strategy)
	}
	reference := strings.TrimSpace(trigger.Include)
	if reference == "" || filepath.IsAbs(reference) || strings.Contains(reference, "\\") {
		return nil, fmt.Errorf("job %q: child pipeline local include must be a non-empty repository-relative path", jobKey)
	}
	clean := filepath.Clean(filepath.FromSlash(reference))
	if clean == "." || !isSafeRelativePath(clean) {
		return nil, fmt.Errorf("job %q: child pipeline include %q leaves the registered project", jobKey, reference)
	}
	candidate, safe, err := safeAbsoluteFile(root, filepath.Join(root, clean))
	if err != nil {
		return nil, fmt.Errorf("job %q: resolve child pipeline %q: %w", jobKey, reference, err)
	}
	if !safe {
		return nil, fmt.Errorf("job %q: child pipeline include %q is not a regular non-symlinked file inside the project", jobKey, reference)
	}
	for _, ancestor := range state.stack {
		if ancestor == candidate {
			return nil, fmt.Errorf("job %q: child pipeline cycle reaches %s", jobKey, filepath.ToSlash(clean))
		}
	}
	state.count++
	if state.count > maxChildPipelines {
		return nil, fmt.Errorf("workflow references more than %d child pipelines", maxChildPipelines)
	}
	if err := validateGitLabIncludes(root, candidate); err != nil {
		return nil, fmt.Errorf("job %q: validate child pipeline includes: %w", jobKey, err)
	}
	childPipeline, err := parsers.NewGitlabParser().Parse(candidate)
	if err != nil {
		return nil, fmt.Errorf("job %q: parse child pipeline %q: %w", jobKey, reference, err)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return nil, fmt.Errorf("job %q: resolve child pipeline source: %w", jobKey, err)
	}
	childFile := workflowFile{provider: ProviderGitLabCI, relative: filepath.ToSlash(relative), absolute: candidate}
	extensions, err := readDeploymentExtensions(childFile)
	if err != nil {
		return nil, fmt.Errorf("job %q: parse child pipeline extensions: %w", jobKey, err)
	}
	state.stack = append(state.stack, candidate)
	childDefinition, err := normalizeDefinitionRecursive(project, root, childFile, childPipeline, extensions, depth+1, state)
	state.stack = state.stack[:len(state.stack)-1]
	if err != nil {
		return nil, fmt.Errorf("job %q: normalize child pipeline %q: %w", jobKey, reference, err)
	}
	inheritVariables := true
	if trigger.InheritVariables != nil {
		inheritVariables = *trigger.InheritVariables
	}
	forwardYAMLVariables := true
	forwardPipelineVariables := false
	if trigger.Forward != nil {
		if trigger.Forward.YAMLVariables != nil {
			forwardYAMLVariables = *trigger.Forward.YAMLVariables
		}
		if trigger.Forward.PipelineVariables != nil {
			forwardPipelineVariables = *trigger.Forward.PipelineVariables
		}
	}
	return &ChildPipelineDefinition{
		SourceFile: filepath.ToSlash(relative), Strategy: strategy, Depth: depth + 1,
		InheritVariables: inheritVariables, ForwardYAMLVariables: forwardYAMLVariables,
		ForwardPipelineVariables: forwardPipelineVariables, Variables: copyStringMap(job.Environment),
		Definition: &childDefinition,
	}, nil
}

func normalizeJob(key string, job *types.Job, extension deploymentExtension) (JobDefinition, error) {
	rollback, err := normalizeDeploymentExtensionCommand("rollback", extension.Rollback)
	if err != nil {
		return JobDefinition{}, err
	}
	verify, err := normalizeDeploymentExtensionCommand("verify", extension.Verify)
	if err != nil {
		return JobDefinition{}, err
	}
	if verify != "" && rollback == "" {
		return JobDefinition{}, fmt.Errorf("x-gci verify requires rollback")
	}
	if rollback != "" && strings.TrimSpace(job.EnvironmentName) == "" {
		return JobDefinition{}, fmt.Errorf("x-gci rollback requires a deployment environment")
	}
	normalized := JobDefinition{
		Retry:                job.Retry,
		Key:                  key,
		Name:                 job.Name,
		Environment:          copyStringMap(job.Environment),
		EnvironmentName:      job.EnvironmentName,
		DeploymentTier:       job.DeploymentTier,
		Needs:                sortedUnique(job.Needs),
		Requires:             sortedUnique(job.Requires),
		ArtifactDependencies: sortedUnique(job.Dependencies),
		DependenciesDefined:  job.DependenciesDefined,
		NeedsArtifacts:       copyBoolMap(job.NeedsArtifacts),
		Stage:                job.Stage,
		RunnerHint:           runnerHint(job),
		AllowFailure:         job.AllowFailure || job.ContinueOnErr,
		TimeoutMinutes:       job.TimeoutMin,
		RollbackCommand:      rollback,
		VerifyCommand:        verify,
		Condition:            executionsemantics.CompileCondition(job.If),
		Rules:                normalizeRules(job.Rules),
		Only:                 normalizeOnlyExcept(job.Only),
		Except:               normalizeOnlyExcept(job.Except),
		When:                 job.When,
		ManualConfirmation:   strings.TrimSpace(job.ManualConfirmation),
		Concurrency:          normalizeConcurrency(job.Concurrency),
		Interruptible:        job.Interruptible,
		WorkflowCall:         copyWorkflowCall(job.WorkflowCall),
		Container:            copyContainer(job.Container),
		Services:             copyServices(job.Services),
		Artifacts:            copyArtifactConfig(job.Artifacts),
		Cache:                copyCacheConfig(job.Cache),
		Outputs:              copyStringMap(job.Outputs),
		Steps:                normalizeSteps(key, job.Steps),
	}
	if job.Strategy != nil {
		normalized.FailFast = job.Strategy.FailFast
		normalized.MaxParallel = job.Strategy.MaxParallel
	}
	return normalized, nil
}

func applyMatrixVariant(job *JobDefinition, variant executionsemantics.MatrixVariant, provider string) error {
	job.Matrix = variant.Values
	job.MatrixIndex = variant.Index
	job.MatrixTotal = variant.Total
	job.MatrixLabel = variant.Label
	job.Name = executionsemantics.MatrixJobName(job.Name, variant)
	environment, err := executionsemantics.MatrixEnvironment(variant, provider)
	if err != nil {
		return err
	}
	if job.Environment == nil {
		job.Environment = make(map[string]string)
	}
	for key, value := range environment {
		job.Environment[key] = value
	}
	fields := []*string{&job.Name, &job.EnvironmentName, &job.DeploymentTier, &job.RunnerHint, &job.RunnerGroup, &job.RollbackCommand, &job.VerifyCommand}
	for _, field := range fields {
		resolved, err := executionsemantics.ResolveMatrixTemplate(*field, variant.Values)
		if err != nil {
			return err
		}
		*field = resolved
	}
	for key, value := range job.Environment {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value, variant.Values)
		if err != nil {
			return err
		}
		job.Environment[key] = resolved
	}
	for index, requirement := range job.RunnerRequirements {
		resolved, err := executionsemantics.ResolveMatrixTemplate(requirement, variant.Values)
		if err != nil {
			return err
		}
		job.RunnerRequirements[index] = resolved
	}
	if job.Concurrency != nil {
		job.Concurrency.Group, err = executionsemantics.ResolveMatrixTemplate(job.Concurrency.Group, variant.Values)
		if err != nil {
			return err
		}
	}
	if err := resolveMatrixArtifactConfig(job.Artifacts, variant.Values); err != nil {
		return err
	}
	if err := resolveMatrixCacheConfig(job.Cache, variant.Values); err != nil {
		return err
	}
	for key, value := range job.Outputs {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value, variant.Values)
		if err != nil {
			return err
		}
		job.Outputs[key] = resolved
	}
	if err := resolveMatrixContainer(job.Container, variant.Values); err != nil {
		return err
	}
	if err := resolveMatrixWorkflowCall(job.WorkflowCall, variant.Values); err != nil {
		return err
	}
	serviceKeys := make([]string, 0, len(job.Services))
	for key := range job.Services {
		serviceKeys = append(serviceKeys, key)
	}
	sort.Strings(serviceKeys)
	for _, key := range serviceKeys {
		if err := resolveMatrixService(job.Services[key], variant.Values); err != nil {
			return fmt.Errorf("service %q: %w", key, err)
		}
	}
	for index := range job.Steps {
		step := &job.Steps[index]
		stepFields := []*string{&step.Name, &step.Command, &step.Action, &step.WorkingDirectory, &step.Shell}
		for _, field := range stepFields {
			resolved, err := executionsemantics.ResolveMatrixTemplate(*field, variant.Values)
			if err != nil {
				return err
			}
			*field = resolved
		}
		for key, value := range step.Environment {
			resolved, err := executionsemantics.ResolveMatrixTemplate(value, variant.Values)
			if err != nil {
				return err
			}
			step.Environment[key] = resolved
		}
		for key, value := range step.Inputs {
			resolved, err := executionsemantics.ResolveMatrixTemplate(value, variant.Values)
			if err != nil {
				return err
			}
			step.Inputs[key] = resolved
		}
		if err := resolveMatrixArtifactConfig(step.Artifacts, variant.Values); err != nil {
			return err
		}
		if err := resolveMatrixCacheConfig(step.Cache, variant.Values); err != nil {
			return err
		}
	}
	return freezeJobSemantics(job, provider)
}

func expandDependencyKeys(keys []string, variants map[string][]string) []string {
	var expanded []string
	for _, key := range keys {
		if matches := variants[key]; len(matches) > 0 {
			expanded = append(expanded, matches...)
		} else {
			expanded = append(expanded, key)
		}
	}
	return sortedUnique(expanded)
}

func expandArtifactNeeds(values map[string]bool, variants map[string][]string) map[string]bool {
	result := make(map[string]bool)
	for key, artifacts := range values {
		matches := variants[key]
		if len(matches) == 0 {
			result[key] = artifacts
			continue
		}
		for _, match := range matches {
			result[match] = artifacts
		}
	}
	return result
}

func normalizeConcurrency(value *types.Concurrency) *ConcurrencyDefinition {
	if value == nil || strings.TrimSpace(value.Group) == "" {
		return nil
	}
	return &ConcurrencyDefinition{Group: value.Group, CancelInProgress: value.CancelInProgress, Limit: value.Limit}
}

func normalizeRules(rules []types.Rule) []RuleDefinition {
	result := make([]RuleDefinition, 0, len(rules))
	for _, rule := range rules {
		result = append(result, RuleDefinition{
			Condition: executionsemantics.CompileCondition(rule.If), When: rule.When,
			Changes: copyStringSlice(rule.Changes), Exists: copyStringSlice(rule.Exists),
			Variables: copyStringMap(rule.Variables), AllowFailure: rule.AllowFailure,
		})
	}
	return result
}

func normalizeOnlyExcept(value *types.OnlyExcept) *OnlyExceptDefinition {
	if value == nil {
		return nil
	}
	return &OnlyExceptDefinition{
		Refs: copyStringSlice(value.Refs), Changes: copyStringSlice(value.Changes), Variables: copyStringSlice(value.Variables),
	}
}

func freezeJobSemantics(job *JobDefinition, provider string) error {
	metadata := map[string]interface{}{
		"provider": provider, "sourceKey": job.SourceKey, "stage": job.Stage,
		"artifactDependencies": job.ArtifactDependencies, "dependenciesDefined": job.DependenciesDefined,
		"needsArtifacts": job.NeedsArtifacts, "matrix": job.Matrix, "matrixIndex": job.MatrixIndex,
		"matrixTotal": job.MatrixTotal, "matrixLabel": job.MatrixLabel, "condition": job.Condition,
		"rules": job.Rules, "only": job.Only, "except": job.Except, "when": job.When,
		"manualConfirmation": job.ManualConfirmation,
		"concurrency":        job.Concurrency, "interruptible": job.Interruptible,
		"failFast": job.FailFast, "maxParallel": job.MaxParallel,
		"workflowCall": job.WorkflowCall, "container": job.Container, "services": job.Services,
		"artifacts": job.Artifacts, "cache": job.Cache, "outputs": job.Outputs, "retry": job.Retry,
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode execution semantics: %w", err)
	}
	job.Environment["GCI_JOB_SEMANTICS_JSON"] = string(encoded)
	for index := range job.Steps {
		if job.Steps[index].Environment == nil {
			job.Steps[index].Environment = make(map[string]string)
		}
		encoded, err := json.Marshal(job.Steps[index].Condition)
		if err != nil {
			return fmt.Errorf("encode step condition: %w", err)
		}
		job.Steps[index].Environment["GCI_STEP_CONDITION_JSON"] = string(encoded)
		if len(job.Steps[index].Inputs) > 0 {
			encoded, err = json.Marshal(job.Steps[index].Inputs)
			if err != nil {
				return fmt.Errorf("encode action inputs: %w", err)
			}
			job.Steps[index].Environment["GCI_ACTION_INPUTS_JSON"] = string(encoded)
		}
	}
	return nil
}

func readDeploymentExtensions(file workflowFile) (map[string]deploymentExtension, error) {
	contents, err := os.ReadFile(file.absolute)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(contents, &document); err != nil {
		return nil, err
	}
	if len(document.Content) == 0 {
		return map[string]deploymentExtension{}, nil
	}
	root := document.Content[0]
	jobs := root
	if file.provider == ProviderGitHubActions {
		jobs = yamlMappingValue(root, "jobs")
	}
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return map[string]deploymentExtension{}, nil
	}
	result := make(map[string]deploymentExtension)
	for index := 0; index+1 < len(jobs.Content); index += 2 {
		jobKey, jobNode := jobs.Content[index], jobs.Content[index+1]
		extensionNode := yamlMappingValue(jobNode, "x-gci")
		if extensionNode == nil {
			continue
		}
		var extension deploymentExtension
		if err := extensionNode.Decode(&extension); err != nil {
			return nil, fmt.Errorf("job %q x-gci must be an object: %w", jobKey.Value, err)
		}
		result[jobKey.Value] = extension
	}
	return result, nil
}

func yamlMappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func normalizeDeploymentExtensionCommand(name, command string) (string, error) {
	command = strings.TrimSpace(command)
	if strings.ContainsRune(command, 0) {
		return "", fmt.Errorf("x-gci %s contains a null byte", name)
	}
	if len(command) > 1<<20 {
		return "", fmt.Errorf("x-gci %s exceeds one MiB", name)
	}
	return command, nil
}

func normalizeSteps(jobKey string, steps []types.Step) []StepDefinition {
	normalized := make([]StepDefinition, 0, len(steps))
	usedKeys := make(map[string]int, len(steps))
	for index, step := range steps {
		key := strings.TrimSpace(step.ID)
		if key == "" {
			key = fmt.Sprintf("%s:%03d", jobKey, index+1)
		}
		usedKeys[key]++
		if usedKeys[key] > 1 {
			key = fmt.Sprintf("%s#%d", key, usedKeys[key])
		}

		normalized = append(normalized, StepDefinition{
			Key:              key,
			Name:             step.Name,
			Command:          stepCommand(step),
			Action:           step.Uses,
			Environment:      copyStringMap(step.Env),
			WorkingDirectory: step.WorkingDir,
			TimeoutMinutes:   step.TimeoutMin,
			Shell:            step.Shell,
			AllowFailure:     step.AllowFailure || step.ContinueOnErr,
			Condition:        executionsemantics.CompileCondition(step.If),
			Inputs:           copyStringMap(step.With),
			Artifacts:        copyArtifactConfig(step.Artifacts),
			Cache:            copyCacheConfig(step.Cache),
		})
	}
	return normalized
}

func stepCommand(step types.Step) string {
	switch {
	case step.Run != "":
		return step.Run
	case step.Command != "":
		return step.Command
	case len(step.Script) > 0:
		return strings.Join(step.Script, "\n")
	default:
		return step.Task
	}
}

func runnerHint(job *types.Job) string {
	switch {
	case job.RunsOn != "":
		return job.RunsOn
	case job.Image != "":
		return job.Image
	case len(job.Tags) > 0:
		return job.Tags[0]
	case job.Executor != "":
		return job.Executor
	case job.Agent != nil:
		return job.Agent.Label
	default:
		return ""
	}
}

func runnerRequirements(provider Provider, job *types.Job) ([]string, string) {
	if job == nil {
		return nil, ""
	}
	switch provider {
	case ProviderGitHubActions:
		labels := copyStringSlice(job.RunnerLabels)
		if len(labels) == 0 && strings.TrimSpace(job.RunsOn) != "" {
			labels = []string{job.RunsOn}
		}
		return labels, job.RunnerGroup
	case ProviderGitLabCI:
		return copyStringSlice(job.Tags), ""
	default:
		return nil, ""
	}
}

func copyStringMap(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func copyBoolMap(values map[string]bool) map[string]bool {
	copy := make(map[string]bool, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func copyStringSlice(values []string) []string {
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func copyArtifactConfig(value *types.ArtifactConfig) *types.ArtifactConfig {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Paths = copyStringSlice(value.Paths)
	copy.Exclude = copyStringSlice(value.Exclude)
	copy.Reports = copyStringMap(value.Reports)
	return &copy
}

func copyCacheConfig(value *types.CacheConfig) *types.CacheConfig {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Paths = copyStringSlice(value.Paths)
	copy.Fallback = copyStringSlice(value.Fallback)
	return &copy
}

func copyContainer(value *types.Container) *types.Container {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Env = copyStringMap(value.Env)
	clone.Volumes = copyStringSlice(value.Volumes)
	clone.Ports = copyStringSlice(value.Ports)
	clone.Command = copyStringSlice(value.Command)
	clone.Entrypoint = copyStringSlice(value.Entrypoint)
	clone.Credentials = copyStringMap(value.Credentials)
	clone.CapAdd = copyStringSlice(value.CapAdd)
	clone.CapDrop = copyStringSlice(value.CapDrop)
	clone.SecurityOpt = copyStringSlice(value.SecurityOpt)
	if value.Auth != nil {
		auth := *value.Auth
		clone.Auth = &auth
	}
	if value.HealthCheck != nil {
		health := *value.HealthCheck
		health.Test = copyStringSlice(value.HealthCheck.Test)
		clone.HealthCheck = &health
	}
	return &clone
}

func copyWorkflowCall(value *types.WorkflowCall) *types.WorkflowCall {
	if value == nil {
		return nil
	}
	clone := *value
	clone.With = make(map[string]interface{}, len(value.With))
	for key, item := range value.With {
		clone.With[key] = item
	}
	clone.Secrets = copyStringMap(value.Secrets)
	return &clone
}

func copyServices(values map[string]*types.Service) map[string]*types.Service {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]*types.Service, len(values))
	for key, value := range values {
		if value == nil {
			continue
		}
		clone := *value
		clone.Command = copyStringSlice(value.Command)
		clone.Entrypoint = copyStringSlice(value.Entrypoint)
		clone.Env = copyStringMap(value.Env)
		clone.Ports = copyStringSlice(value.Ports)
		clone.Volumes = copyStringSlice(value.Volumes)
		clone.Networks = copyStringSlice(value.Networks)
		clone.DependsOn = copyStringSlice(value.DependsOn)
		if value.HealthCheck != nil {
			health := *value.HealthCheck
			health.Test = copyStringSlice(value.HealthCheck.Test)
			clone.HealthCheck = &health
		}
		result[key] = &clone
	}
	return result
}

func resolveMatrixContainer(value *types.Container, matrix map[string]string) error {
	if value == nil {
		return nil
	}
	fields := []*string{&value.Image, &value.Name, &value.Options, &value.User, &value.CPUs, &value.Memory}
	for _, field := range fields {
		resolved, err := executionsemantics.ResolveMatrixTemplate(*field, matrix)
		if err != nil {
			return err
		}
		*field = resolved
	}
	return resolveMatrixRuntimeValues(value.Env, value.Volumes, value.Ports, value.Command, value.Entrypoint, matrix)
}

func resolveMatrixService(value *types.Service, matrix map[string]string) error {
	if value == nil {
		return nil
	}
	fields := []*string{&value.Image, &value.Name, &value.Alias, &value.Options}
	for _, field := range fields {
		resolved, err := executionsemantics.ResolveMatrixTemplate(*field, matrix)
		if err != nil {
			return err
		}
		*field = resolved
	}
	return resolveMatrixRuntimeValues(value.Env, value.Volumes, value.Ports, value.Command, value.Entrypoint, matrix)
}

func resolveMatrixRuntimeValues(environment map[string]string, volumes, ports, command, entrypoint []string, matrix map[string]string) error {
	for key, value := range environment {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value, matrix)
		if err != nil {
			return err
		}
		environment[key] = resolved
	}
	for _, values := range [][]string{volumes, ports, command, entrypoint} {
		for index := range values {
			resolved, err := executionsemantics.ResolveMatrixTemplate(values[index], matrix)
			if err != nil {
				return err
			}
			values[index] = resolved
		}
	}
	return nil
}

func resolveMatrixArtifactConfig(value *types.ArtifactConfig, matrix map[string]string) error {
	if value == nil {
		return nil
	}
	fields := []*string{&value.Name, &value.When, &value.ExpireIn, &value.Format}
	for _, field := range fields {
		resolved, err := executionsemantics.ResolveMatrixTemplate(*field, matrix)
		if err != nil {
			return err
		}
		*field = resolved
	}
	for index := range value.Paths {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value.Paths[index], matrix)
		if err != nil {
			return err
		}
		value.Paths[index] = resolved
	}
	for index := range value.Exclude {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value.Exclude[index], matrix)
		if err != nil {
			return err
		}
		value.Exclude[index] = resolved
	}
	for key, raw := range value.Reports {
		resolved, err := executionsemantics.ResolveMatrixTemplate(raw, matrix)
		if err != nil {
			return err
		}
		value.Reports[key] = resolved
	}
	return nil
}

func resolveMatrixCacheConfig(value *types.CacheConfig, matrix map[string]string) error {
	if value == nil {
		return nil
	}
	fields := []*string{&value.Key, &value.Policy, &value.When}
	for _, field := range fields {
		resolved, err := executionsemantics.ResolveMatrixTemplate(*field, matrix)
		if err != nil {
			return err
		}
		*field = resolved
	}
	for index := range value.Paths {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value.Paths[index], matrix)
		if err != nil {
			return err
		}
		value.Paths[index] = resolved
	}
	for index := range value.Fallback {
		resolved, err := executionsemantics.ResolveMatrixTemplate(value.Fallback[index], matrix)
		if err != nil {
			return err
		}
		value.Fallback[index] = resolved
	}
	return nil
}

func sortedUnique(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			unique[value] = struct{}{}
		}
	}

	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func deterministicTopologicalOrder(jobKeys []string, dependencies map[string][]string) ([]string, error) {
	keys := copyStringSlice(jobKeys)
	sort.Strings(keys)

	knownJobs := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		knownJobs[key] = struct{}{}
	}

	indegree := make(map[string]int, len(keys))
	dependents := make(map[string][]string, len(keys))
	normalizedDependencies := make(map[string][]string, len(keys))
	for _, key := range keys {
		deps := sortedUnique(dependencies[key])
		normalizedDependencies[key] = deps
		for _, dependency := range deps {
			if _, found := knownJobs[dependency]; !found {
				return nil, fmt.Errorf("job %q has missing dependency %q", key, dependency)
			}
			indegree[key]++
			dependents[dependency] = append(dependents[dependency], key)
		}
	}
	for key := range dependents {
		sort.Strings(dependents[key])
	}

	ready := make([]string, 0, len(keys))
	for _, key := range keys {
		if indegree[key] == 0 {
			ready = append(ready, key)
		}
	}

	order := make([]string, 0, len(keys))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)

		for _, dependent := range dependents[current] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = insertSorted(ready, dependent)
			}
		}
	}
	if len(order) == len(keys) {
		return order, nil
	}

	cycle := findDependencyCycle(keys, normalizedDependencies)
	if len(cycle) == 0 {
		return nil, fmt.Errorf("workflow has a dependency cycle")
	}
	return nil, fmt.Errorf("workflow has dependency cycle: %s", strings.Join(cycle, " -> "))
}

func insertSorted(values []string, value string) []string {
	index := sort.SearchStrings(values, value)
	if index < len(values) && values[index] == value {
		return values
	}
	values = append(values, "")
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}

func findDependencyCycle(jobKeys []string, dependencies map[string][]string) []string {
	const (
		notVisited = iota
		visiting
		visited
	)

	state := make(map[string]int, len(jobKeys))
	positions := make(map[string]int, len(jobKeys))
	stack := make([]string, 0, len(jobKeys))
	var visit func(string) []string
	visit = func(key string) []string {
		state[key] = visiting
		positions[key] = len(stack)
		stack = append(stack, key)

		for _, dependency := range dependencies[key] {
			switch state[dependency] {
			case visiting:
				cycle := append([]string{}, stack[positions[dependency]:]...)
				return append(cycle, dependency)
			case notVisited:
				if cycle := visit(dependency); len(cycle) > 0 {
					return cycle
				}
			}
		}

		stack = stack[:len(stack)-1]
		delete(positions, key)
		state[key] = visited
		return nil
	}

	keys := copyStringSlice(jobKeys)
	sort.Strings(keys)
	for _, key := range keys {
		if state[key] == notVisited {
			if cycle := visit(key); len(cycle) > 0 {
				return cycle
			}
		}
	}
	return nil
}
