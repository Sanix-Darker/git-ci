package parsers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	"gopkg.in/yaml.v3"
)

type GithubParser struct {
	// Cache for reusable workflows
	workflowCache map[string]*GithubWorkflow
	// Base directory for resolving relative paths
	baseDir string
}

// NewGithubParser creates a new GitHub Actions parser
func NewGithubParser() *GithubParser {
	return &GithubParser{
		workflowCache: make(map[string]*GithubWorkflow),
	}
}

// GitHub Actions workflow structures with full feature support
type GithubWorkflow struct {
	Name        string                    `yaml:"name"`
	On          interface{}               `yaml:"on"`
	Env         map[string]string         `yaml:"env,omitempty"`
	Defaults    *GithubDefaults           `yaml:"defaults,omitempty"`
	Jobs        map[string]*GithubJob     `yaml:"jobs"`
	Permissions interface{}               `yaml:"permissions,omitempty"`
	Concurrency *GithubConcurrency        `yaml:"concurrency,omitempty"`
}

type GithubDefaults struct {
	Run *GithubRunDefaults `yaml:"run,omitempty"`
}

type GithubRunDefaults struct {
	Shell            string `yaml:"shell,omitempty"`
	WorkingDirectory string `yaml:"working-directory,omitempty"`
}

type GithubConcurrency struct {
	Group            string `yaml:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress,omitempty"`
}

type GithubJob struct {
	Name             string                      `yaml:"name,omitempty"`
	RunsOn           interface{}                 `yaml:"runs-on"`
	Needs            interface{}                 `yaml:"needs,omitempty"`
	If               string                      `yaml:"if,omitempty"`
	Steps            []GithubStep                `yaml:"steps"`
	Env              map[string]string           `yaml:"env,omitempty"`
	Defaults         *GithubDefaults             `yaml:"defaults,omitempty"`
	TimeoutMinutes   int                         `yaml:"timeout-minutes,omitempty"`
	Strategy         *GithubStrategy             `yaml:"strategy,omitempty"`
	ContinueOnError  interface{}                 `yaml:"continue-on-error,omitempty"`
	Container        interface{}                 `yaml:"container,omitempty"`
	Services         map[string]*GithubService   `yaml:"services,omitempty"`
	Uses             string                      `yaml:"uses,omitempty"`
	With             map[string]interface{}      `yaml:"with,omitempty"`
	Secrets          interface{}                 `yaml:"secrets,omitempty"`
	Outputs          map[string]string           `yaml:"outputs,omitempty"`
	Environment      interface{}                 `yaml:"environment,omitempty"`
	Concurrency      *GithubConcurrency          `yaml:"concurrency,omitempty"`
	Permissions      interface{}                 `yaml:"permissions,omitempty"`
}

type GithubStrategy struct {
	Matrix       interface{} `yaml:"matrix,omitempty"`
	FailFast     *bool       `yaml:"fail-fast,omitempty"`
	MaxParallel  int         `yaml:"max-parallel,omitempty"`
}

type GithubMatrix struct {
	Include []map[string]interface{} `yaml:"include,omitempty"`
	Exclude []map[string]interface{} `yaml:"exclude,omitempty"`
}

type GithubService struct {
	Image        string                 `yaml:"image"`
	Env          map[string]string      `yaml:"env,omitempty"`
	Ports        []interface{}          `yaml:"ports,omitempty"`
	Volumes      []string               `yaml:"volumes,omitempty"`
	Options      string                 `yaml:"options,omitempty"`
	Credentials  map[string]string      `yaml:"credentials,omitempty"`
}

type GithubContainer struct {
	Image        string                 `yaml:"image"`
	Env          map[string]string      `yaml:"env,omitempty"`
	Ports        []interface{}          `yaml:"ports,omitempty"`
	Volumes      []string               `yaml:"volumes,omitempty"`
	Options      string                 `yaml:"options,omitempty"`
	Credentials  map[string]string      `yaml:"credentials,omitempty"`
}

type GithubStep struct {
	Id               string                 `yaml:"id,omitempty"`
	If               string                 `yaml:"if,omitempty"`
	Name             string                 `yaml:"name,omitempty"`
	Uses             string                 `yaml:"uses,omitempty"`
	Run              string                 `yaml:"run,omitempty"`
	Shell            string                 `yaml:"shell,omitempty"`
	With             map[string]interface{} `yaml:"with,omitempty"`
	Env              map[string]string      `yaml:"env,omitempty"`
	ContinueOnError  interface{}            `yaml:"continue-on-error,omitempty"`
	TimeoutMinutes   int                    `yaml:"timeout-minutes,omitempty"`
	WorkingDirectory string                 `yaml:"working-directory,omitempty"`
}

// Parse parses a GitHub Actions workflow file
func (p *GithubParser) Parse(ciFilePath string) (*types.Pipeline, error) {
	// Store base directory for relative path resolution
	p.baseDir = filepath.Dir(ciFilePath)

	// Check if file exists
	if _, err := os.Stat(ciFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("workflow file not found: %s", ciFilePath)
	}

	// Read file content
	data, err := os.ReadFile(ciFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file: %w", err)
	}

	// Handle empty files
	if len(data) == 0 {
		return nil, fmt.Errorf("workflow file is empty: %s", ciFilePath)
	}

	// Parse YAML with strict mode for better error reporting
	var workflow GithubWorkflow
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(false) // Allow unknown fields for forward compatibility

	if err := decoder.Decode(&workflow); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Convert to generic Pipeline
	pipeline, err := p.convertToPipeline(&workflow)
	if err != nil {
		return nil, fmt.Errorf("failed to convert workflow: %w", err)
	}

	// Validate the pipeline
	if err := p.Validate(pipeline); err != nil {
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	return pipeline, nil
}

// convertToPipeline converts GitHub workflow to generic Pipeline
func (p *GithubParser) convertToPipeline(workflow *GithubWorkflow) (*types.Pipeline, error) {
	pipeline := &types.Pipeline{
		Name:        workflow.Name,
		Description: fmt.Sprintf("GitHub Actions workflow: %s", workflow.Name),
		Jobs:        make(map[string]*types.Job),
		Environment: workflow.Env,
		Triggers:    p.parseTriggers(workflow.On),
	}

	// Process each job
	for jobID, ghJob := range workflow.Jobs {
		// Handle reusable workflows
		if ghJob.Uses != "" {
			job, err := p.parseReusableWorkflow(jobID, ghJob)
			if err != nil {
				return nil, fmt.Errorf("failed to parse reusable workflow in job %s: %w", jobID, err)
			}
			pipeline.Jobs[jobID] = job
			continue
		}

		job, err := p.convertJob(jobID, ghJob, workflow.Defaults)
		if err != nil {
			return nil, fmt.Errorf("failed to convert job %s: %w", jobID, err)
		}
		pipeline.Jobs[jobID] = job
	}

	return pipeline, nil
}

// convertJob converts GitHub job to generic Job
func (p *GithubParser) convertJob(jobID string, ghJob *GithubJob, globalDefaults *GithubDefaults) (*types.Job, error) {
	job := &types.Job{
		Name:          p.getJobName(jobID, ghJob),
		RunsOn:        p.parseRunsOn(ghJob.RunsOn),
		Environment:   ghJob.Env,
		If:            ghJob.If,
		TimeoutMin:    ghJob.TimeoutMinutes,
		ContinueOnErr: p.parseContinueOnError(ghJob.ContinueOnError),
		Needs:         p.parseNeeds(ghJob.Needs),
	}

	// Set default timeout if not specified
	if job.TimeoutMin == 0 {
		job.TimeoutMin = 360 // GitHub's default is 6 hours
	}

	// Parse container configuration
	if ghJob.Container != nil {
		container, err := p.parseContainer(ghJob.Container)
		if err != nil {
			return nil, fmt.Errorf("failed to parse container: %w", err)
		}
		job.Container = container
	}

	// Parse services
	if len(ghJob.Services) > 0 {
		job.Services = p.parseServices(ghJob.Services)
	}

	// Parse strategy for matrix builds
	if ghJob.Strategy != nil {
		job.Strategy = p.parseStrategy(ghJob.Strategy)
	}

	// Determine default shell and working directory
	defaultShell := "bash"
	defaultWorkDir := ""

	if globalDefaults != nil && globalDefaults.Run != nil {
		if globalDefaults.Run.Shell != "" {
			defaultShell = globalDefaults.Run.Shell
		}
		if globalDefaults.Run.WorkingDirectory != "" {
			defaultWorkDir = globalDefaults.Run.WorkingDirectory
		}
	}

	if ghJob.Defaults != nil && ghJob.Defaults.Run != nil {
		if ghJob.Defaults.Run.Shell != "" {
			defaultShell = ghJob.Defaults.Run.Shell
		}
		if ghJob.Defaults.Run.WorkingDirectory != "" {
			defaultWorkDir = ghJob.Defaults.Run.WorkingDirectory
		}
	}

	// Convert steps
	job.Steps = p.convertSteps(ghJob.Steps, defaultShell, defaultWorkDir)

	return job, nil
}

// convertSteps converts GitHub steps to generic Steps
func (p *GithubParser) convertSteps(ghSteps []GithubStep, defaultShell, defaultWorkDir string) []types.Step {
	steps := make([]types.Step, 0, len(ghSteps))

	for i, ghStep := range ghSteps {
		step := types.Step{
			ID:            ghStep.Id,
			Name:          p.getStepName(ghStep, i),
			Run:           ghStep.Run,
			Uses:          ghStep.Uses,
			With:          p.convertWith(ghStep.With),
			Env:           ghStep.Env,
			If:            ghStep.If,
			ContinueOnErr: p.parseContinueOnError(ghStep.ContinueOnError),
			TimeoutMin:    ghStep.TimeoutMinutes,
			Shell:         p.getStepShell(ghStep.Shell, defaultShell),
			WorkingDir:    p.getStepWorkDir(ghStep.WorkingDirectory, defaultWorkDir),
		}

		steps = append(steps, step)
	}

	return steps
}

// some Helper functions (privates to the gitlab parser)

func (p *GithubParser) getJobName(jobID string, job *GithubJob) string {
	if job.Name != "" {
		return job.Name
	}
	// Convert job ID to readable name
	return strings.ReplaceAll(strings.Title(strings.ReplaceAll(jobID, "-", " ")), "_", " ")
}

func (p *GithubParser) getStepName(step GithubStep, index int) string {
	if step.Name != "" {
		return step.Name
	}

	if step.Uses != "" {
		// Extract action name from uses
		parts := strings.Split(step.Uses, "@")
		if len(parts) > 0 {
			actionParts := strings.Split(parts[0], "/")
			if len(actionParts) >= 2 {
				return fmt.Sprintf("Run %s", actionParts[len(actionParts)-1])
			}
		}
		return fmt.Sprintf("Action: %s", step.Uses)
	}

	if step.Run != "" {
		// Use first line of run command as name
		lines := strings.Split(step.Run, "\n")
		if len(lines) > 0 && len(lines[0]) > 0 {
			firstLine := strings.TrimSpace(lines[0])
			// Remove common prefixes
			firstLine = strings.TrimPrefix(firstLine, "echo ")
			firstLine = strings.TrimPrefix(firstLine, "npm ")
			firstLine = strings.TrimPrefix(firstLine, "yarn ")
			firstLine = strings.TrimPrefix(firstLine, "make ")

			if len(firstLine) > 50 {
				firstLine = firstLine[:47] + "..."
			}
			return firstLine
		}
	}

	return fmt.Sprintf("Step %d", index+1)
}

func (p *GithubParser) getStepShell(stepShell, defaultShell string) string {
	if stepShell != "" {
		return stepShell
	}
	if defaultShell != "" {
		return defaultShell
	}
	return "bash"
}

func (p *GithubParser) getStepWorkDir(stepWorkDir, defaultWorkDir string) string {
	if stepWorkDir != "" {
		return stepWorkDir
	}
	return defaultWorkDir
}

func (p *GithubParser) parseTriggers(on interface{}) []string {
	var triggers []string

	switch v := on.(type) {
	case string:
		triggers = append(triggers, v)
	case []interface{}:
		for _, trigger := range v {
			if str, ok := trigger.(string); ok {
				triggers = append(triggers, str)
			}
		}
	case map[string]interface{}:
		for trigger := range v {
			triggers = append(triggers, trigger)
		}
	}

	return triggers
}

func (p *GithubParser) parseRunsOn(runsOn interface{}) string {
	switch v := runsOn.(type) {
	case string:
		return v
	case []interface{}:
		// For matrix builds, return the first runner
		if len(v) > 0 {
			if str, ok := v[0].(string); ok {
				return str
			}
		}
	case map[string]interface{}:
		// Handle complex runs-on with labels
		if labels, ok := v["labels"].([]interface{}); ok && len(labels) > 0 {
			if str, ok := labels[0].(string); ok {
				return str
			}
		}
	}
	return "ubuntu-latest"
}

func (p *GithubParser) parseNeeds(needs interface{}) []string {
	var result []string

	switch v := needs.(type) {
	case string:
		result = append(result, v)
	case []interface{}:
		for _, need := range v {
			if str, ok := need.(string); ok {
				result = append(result, str)
			}
		}
	case map[string]interface{}:
		// Handle complex needs with conditions
		for needID := range v {
			result = append(result, needID)
		}
	}

	return result
}

func (p *GithubParser) parseContinueOnError(continueOnError interface{}) bool {
	switch v := continueOnError.(type) {
	case bool:
		return v
	case string:
		// Handle expressions like "${{ failure() }}"
		return strings.Contains(v, "true") || strings.Contains(v, "failure()")
	}
	return false
}

func (p *GithubParser) parseContainer(container interface{}) (*types.Container, error) {
	switch v := container.(type) {
	case string:
		// Simple image string
		return &types.Container{
			Image: v,
		}, nil
	case map[string]interface{}:
		// Full container configuration
		c := &types.Container{
			Env:     make(map[string]string),
			Volumes: []string{},
		}

		if image, ok := v["image"].(string); ok {
			c.Image = image
		}

		if options, ok := v["options"].(string); ok {
			c.Options = options
		}

		if env, ok := v["env"].(map[string]interface{}); ok {
			for k, val := range env {
				if str, ok := val.(string); ok {
					c.Env[k] = str
				}
			}
		}

		if volumes, ok := v["volumes"].([]interface{}); ok {
			for _, vol := range volumes {
				if str, ok := vol.(string); ok {
					c.Volumes = append(c.Volumes, str)
				}
			}
		}

		if ports, ok := v["ports"].([]interface{}); ok {
			for _, port := range ports {
				c.Ports = append(c.Ports, fmt.Sprintf("%v", port))
			}
		}

		return c, nil
	}

	return nil, fmt.Errorf("invalid container configuration type: %T", container)
}

func (p *GithubParser) parseServices(services map[string]*GithubService) map[string]*types.Service {
	result := make(map[string]*types.Service)

	for name, ghService := range services {
		service := &types.Service{
			Image:   ghService.Image,
			Env:     ghService.Env,
			Options: ghService.Options,
			Volumes: ghService.Volumes,
		}

		// Convert ports
		for _, port := range ghService.Ports {
			service.Ports = append(service.Ports, fmt.Sprintf("%v", port))
		}

		result[name] = service
	}

	return result
}

func (p *GithubParser) parseStrategy(strategy *GithubStrategy) *types.Strategy {
	s := &types.Strategy{
		MaxParallel: strategy.MaxParallel,
	}

	if strategy.FailFast != nil {
		s.FailFast = *strategy.FailFast
	} else {
		s.FailFast = true // GitHub default
	}

	// Parse matrix
	if strategy.Matrix != nil {
		s.Matrix = p.parseMatrix(strategy.Matrix)
	}

	return s
}

func (p *GithubParser) parseMatrix(matrix interface{}) map[string][]interface{} {
	result := make(map[string][]interface{})

	switch v := matrix.(type) {
	case map[string]interface{}:
		for key, value := range v {
			// Skip include/exclude as they're special keys
			if key == "include" || key == "exclude" {
				continue
			}

			switch val := value.(type) {
			case []interface{}:
				result[key] = val
			case interface{}:
				result[key] = []interface{}{val}
			}
		}
	}

	return result
}

func (p *GithubParser) convertWith(with map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for k, v := range with {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

func (p *GithubParser) parseReusableWorkflow(jobID string, ghJob *GithubJob) (*types.Job, error) {
	// Parse reusable workflow reference
	// Format: owner/repo/.github/workflows/workflow.yml@ref
	// or: ./.github/workflows/workflow.yml

	job := &types.Job{
		Name:   p.getJobName(jobID, ghJob),
		RunsOn: "ubuntu-latest", // Default for reusable workflows
		Steps: []types.Step{
			{
				Name: fmt.Sprintf("Call reusable workflow: %s", ghJob.Uses),
				Uses: ghJob.Uses,
				With: p.convertWith(ghJob.With),
			},
		},
	}

	// Handle secrets
	if ghJob.Secrets != nil {
		switch v := ghJob.Secrets.(type) {
		case string:
			if v == "inherit" {
				job.Environment = map[string]string{
					"SECRETS": "inherited",
				}
			}
		case map[string]interface{}:
			// Map individual secrets
			for k, val := range v {
				if job.Environment == nil {
					job.Environment = make(map[string]string)
				}
				job.Environment[fmt.Sprintf("SECRET_%s", strings.ToUpper(k))] = fmt.Sprintf("%v", val)
			}
		}
	}

	return job, nil
}

// Validate validates the parsed pipeline
func (p *GithubParser) Validate(pipeline *types.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	var errors []string

	// Validate pipeline name
	if pipeline.Name == "" {
		errors = append(errors, "workflow has no name")
	}

	// Validate jobs
	if len(pipeline.Jobs) == 0 {
		errors = append(errors, "no jobs defined in workflow")
	}

	// Track job IDs for dependency validation
	jobIDs := make(map[string]bool)
	for jobID := range pipeline.Jobs {
		jobIDs[jobID] = true
	}

	for jobID, job := range pipeline.Jobs {
		// Validate runs-on
		if job.RunsOn == "" {
			errors = append(errors, fmt.Sprintf("job '%s' has no 'runs-on' specified", jobID))
		}

		// Validate steps
		if len(job.Steps) == 0 {
			errors = append(errors, fmt.Sprintf("job '%s' has no steps", jobID))
		}

		// Validate job dependencies
		for _, need := range job.Needs {
			if !jobIDs[need] {
				errors = append(errors, fmt.Sprintf("job '%s' depends on non-existent job '%s'", jobID, need))
			}
		}

		// Check for circular dependencies
		if err := p.checkCircularDependencies(jobID, job, pipeline.Jobs, []string{}); err != nil {
			errors = append(errors, err.Error())
		}

		// Validate each step
		for i, step := range job.Steps {
			if step.Run == "" && step.Uses == "" {
				errors = append(errors, fmt.Sprintf("step %d in job '%s' has neither 'run' nor 'uses'", i+1, jobID))
			}

			if step.Run != "" && step.Uses != "" {
				errors = append(errors, fmt.Sprintf("step %d in job '%s' has both 'run' and 'uses' (only one allowed)", i+1, jobID))
			}

			// Validate action references
			if step.Uses != "" {
				if err := p.validateActionReference(step.Uses); err != nil {
					errors = append(errors, fmt.Sprintf("step %d in job '%s': %v", i+1, jobID, err))
				}
			}

			// Validate shell
			if step.Shell != "" {
				validShells := map[string]bool{
					"bash": true, "pwsh": true, "python": true,
					"sh": true, "cmd": true, "powershell": true,
				}
				if !validShells[step.Shell] {
					errors = append(errors, fmt.Sprintf("step %d in job '%s' has invalid shell: %s", i+1, jobID, step.Shell))
				}
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

func (p *GithubParser) checkCircularDependencies(jobID string, job *types.Job, allJobs map[string]*types.Job, visited []string) error {
	// Check if we've already visited this job (circular dependency)
	for _, v := range visited {
		if v == jobID {
			return fmt.Errorf("circular dependency detected: %s", strings.Join(append(visited, jobID), " -> "))
		}
	}

	visited = append(visited, jobID)

	// Check dependencies recursively
	for _, need := range job.Needs {
		if dependentJob, exists := allJobs[need]; exists {
			if err := p.checkCircularDependencies(need, dependentJob, allJobs, visited); err != nil {
				return err
			}
		}
	}

	return nil
}

func (p *GithubParser) validateActionReference(uses string) error {
	// Validate action reference format
	// Valid formats:
	// - owner/repo@ref
	// - owner/repo/path@ref
	// - docker://image:tag
	// - ./path/to/action

	if strings.HasPrefix(uses, "docker://") {
		// Docker action
		return nil
	}

	if strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "../") {
		// Local action
		return nil
	}

	// GitHub action
	pattern := `^[a-zA-Z0-9\-_]+/[a-zA-Z0-9\-_]+(/[a-zA-Z0-9\-_/]+)?@.+$`
	matched, err := regexp.MatchString(pattern, uses)
	if err != nil {
		return fmt.Errorf("failed to validate action reference: %w", err)
	}

	if !matched {
		return fmt.Errorf("invalid action reference format: %s", uses)
	}

	return nil
}

// GetWorkflowInputs extracts workflow inputs from workflow_dispatch events
func (p *GithubParser) GetWorkflowInputs(workflow *GithubWorkflow) map[string]interface{} {
	inputs := make(map[string]interface{})

	// Parse the 'on' field for workflow_dispatch
	switch on := workflow.On.(type) {
	case map[string]interface{}:
		if dispatch, ok := on["workflow_dispatch"].(map[string]interface{}); ok {
			if dispatchInputs, ok := dispatch["inputs"].(map[string]interface{}); ok {
				return dispatchInputs
			}
		}
	}

	return inputs
}

// GetWorkflowOutputs extracts workflow outputs from job outputs
func (p *GithubParser) GetWorkflowOutputs(workflow *GithubWorkflow) map[string]string {
	outputs := make(map[string]string)

	for jobID, job := range workflow.Jobs {
		for outputName, outputValue := range job.Outputs {
			// Prefix with job ID to avoid conflicts
			outputs[fmt.Sprintf("%s.%s", jobID, outputName)] = outputValue
		}
	}

	return outputs
}

// GetProviderName returns the name of this parser
func (p *GithubParser) GetProviderName() string {
    return "github"
}

// ParseDirectory parses all workflow files in a directory
func (p *GithubParser) ParseDirectory(dir string) ([]*types.Pipeline, error) {
    workflowDir := filepath.Join(dir, ".github", "workflows")

    if _, err := os.Stat(workflowDir); os.IsNotExist(err) {
        return nil, fmt.Errorf("workflows directory not found: %s", workflowDir)
    }

    entries, err := os.ReadDir(workflowDir)
    if err != nil {
        return nil, fmt.Errorf("failed to read workflows directory: %w", err)
    }

    var pipelines []*types.Pipeline
    for _, entry := range entries {
        if entry.IsDir() {
            continue
        }

        name := entry.Name()
        if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
            continue
        }

        filePath := filepath.Join(workflowDir, name)
        pipeline, err := p.Parse(filePath)
        if err != nil {
            fmt.Printf("Warning: Failed to parse %s: %v\n", name, err)
            continue
        }

        pipelines = append(pipelines, pipeline)
    }

    if len(pipelines) == 0 {
        return nil, fmt.Errorf("no valid workflow files found in %s", workflowDir)
    }

    return pipelines, nil
}
