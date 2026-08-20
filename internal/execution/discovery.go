// Package execution discovers normalized CI definitions from registered local
// projects. It intentionally accepts store project records instead of arbitrary
// filesystem roots so callers cannot expand the service's discovery boundary.
package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/internal/parsers"
	"github.com/sanix-darker/git-ci/internal/store"
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
	Key              string            `json:"key"`
	ProjectID        string            `json:"projectId,omitempty"`
	ProjectSlug      string            `json:"projectSlug,omitempty"`
	ProjectPath      string            `json:"projectPath"`
	Provider         Provider          `json:"provider"`
	File             string            `json:"file"`
	Name             string            `json:"name"`
	Environment      map[string]string `json:"environment"`
	Stages           []string          `json:"stages"`
	Triggers         []string          `json:"triggers"`
	Jobs             []JobDefinition   `json:"jobs"`
	TopologicalOrder []string          `json:"topologicalOrder"`
}

// JobDefinition is a provider-neutral, persistence-ready job. Key is the
// source job identifier; Needs and Requires therefore reference keys rather
// than presentation names.
type JobDefinition struct {
	Key             string            `json:"key"`
	Name            string            `json:"name"`
	Environment     map[string]string `json:"environment"`
	EnvironmentName string            `json:"environmentName,omitempty"`
	DeploymentTier  string            `json:"deploymentTier,omitempty"`
	Needs           []string          `json:"needs"`
	Requires        []string          `json:"requires"`
	Stage           string            `json:"stage,omitempty"`
	RunnerHint      string            `json:"runnerHint,omitempty"`
	AllowFailure    bool              `json:"allowFailure"`
	TimeoutMinutes  int               `json:"timeoutMinutes,omitempty"`
	Steps           []StepDefinition  `json:"steps"`
}

// StepDefinition is a provider-neutral, persistence-ready execution step.
// Command holds the shell/script command when available; Action preserves a
// provider action reference such as actions/checkout@v4.
type StepDefinition struct {
	Key              string            `json:"key"`
	Name             string            `json:"name"`
	Command          string            `json:"command,omitempty"`
	Action           string            `json:"action,omitempty"`
	Environment      map[string]string `json:"environment"`
	WorkingDirectory string            `json:"workingDirectory,omitempty"`
	TimeoutMinutes   int               `json:"timeoutMinutes,omitempty"`
	Shell            string            `json:"shell,omitempty"`
	AllowFailure     bool              `json:"allowFailure"`
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

		definition, err := normalizeDefinition(project, root, file, pipeline)
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
) (Definition, error) {
	if pipeline == nil {
		return Definition{}, fmt.Errorf("parser returned no pipeline")
	}

	jobKeys := make([]string, 0, len(pipeline.Jobs))
	for key := range pipeline.Jobs {
		jobKeys = append(jobKeys, key)
	}
	sort.Strings(jobKeys)

	jobs := make(map[string]JobDefinition, len(jobKeys))
	dependencies := make(map[string][]string, len(jobKeys))
	for _, key := range jobKeys {
		job := pipeline.Jobs[key]
		if job == nil {
			return Definition{}, fmt.Errorf("job %q is nil", key)
		}
		normalized := normalizeJob(key, job)
		jobs[key] = normalized
		dependencies[key] = sortedUnique(append(
			append([]string{}, normalized.Needs...),
			normalized.Requires...,
		))
	}

	order, err := deterministicTopologicalOrder(jobKeys, dependencies)
	if err != nil {
		return Definition{}, err
	}

	projectKey := project.ID
	if projectKey == "" {
		projectKey = root
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
		Jobs:             make([]JobDefinition, 0, len(order)),
		TopologicalOrder: copyStringSlice(order),
	}
	for _, key := range order {
		definition.Jobs = append(definition.Jobs, jobs[key])
	}
	return definition, nil
}

func normalizeJob(key string, job *types.Job) JobDefinition {
	return JobDefinition{
		Key:             key,
		Name:            job.Name,
		Environment:     copyStringMap(job.Environment),
		EnvironmentName: job.EnvironmentName,
		DeploymentTier:  job.DeploymentTier,
		Needs:           sortedUnique(job.Needs),
		Requires:        sortedUnique(job.Requires),
		Stage:           job.Stage,
		RunnerHint:      runnerHint(job),
		AllowFailure:    job.AllowFailure || job.ContinueOnErr,
		TimeoutMinutes:  job.TimeoutMin,
		Steps:           normalizeSteps(key, job.Steps),
	}
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

func copyStringMap(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
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
