package parsers

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	yaml "gopkg.in/yaml.v3"
)

type GitlabParser struct {
	baseDir       string
	includeCache  map[string]*GitlabCI
}

// NewGitlabParser creates a new GitLab CI parser
func NewGitlabParser() *GitlabParser {
	return &GitlabParser{
		includeCache: make(map[string]*GitlabCI),
	}
}

// GitLab CI structures with full feature support
type GitlabCI struct {
	// Global configuration
	Image        interface{}               `yaml:"image,omitempty"`
	Services     []interface{}             `yaml:"services,omitempty"`
	Stages       []string                  `yaml:"stages,omitempty"`
	Variables    map[string]interface{}    `yaml:"variables,omitempty"`
	Cache        interface{}               `yaml:"cache,omitempty"`
	BeforeScript []interface{}             `yaml:"before_script,omitempty"`
	AfterScript  []interface{}             `yaml:"after_script,omitempty"`

	// Workflow rules
	Workflow     *GitlabWorkflow           `yaml:"workflow,omitempty"`

	// Includes for modular pipelines
	Include      interface{}               `yaml:"include,omitempty"`

	// Default job configuration
	Default      *GitlabDefault            `yaml:"default,omitempty"`

	// Jobs - everything else that's not a keyword
	Jobs         map[string]*GitlabJob     `yaml:",inline"`
}

type GitlabWorkflow struct {
	Rules        []GitlabRule              `yaml:"rules,omitempty"`
}

type GitlabDefault struct {
	Image        interface{}               `yaml:"image,omitempty"`
	Services     []interface{}             `yaml:"services,omitempty"`
	BeforeScript []interface{}             `yaml:"before_script,omitempty"`
	AfterScript  []interface{}             `yaml:"after_script,omitempty"`
	Tags         []string                  `yaml:"tags,omitempty"`
	Cache        interface{}               `yaml:"cache,omitempty"`
	Artifacts    *GitlabArtifacts          `yaml:"artifacts,omitempty"`
	Retry        interface{}               `yaml:"retry,omitempty"`
	Timeout      string                    `yaml:"timeout,omitempty"`
	Interruptible bool                     `yaml:"interruptible,omitempty"`
}

type GitlabJob struct {
	// Basic configuration
	Stage        string                    `yaml:"stage,omitempty"`
	Image        interface{}               `yaml:"image,omitempty"`
	Services     []interface{}             `yaml:"services,omitempty"`
	Script       []interface{}             `yaml:"script"`

	// Extended configuration
	Extends      interface{}               `yaml:"extends,omitempty"`
	Rules        []GitlabRule              `yaml:"rules,omitempty"`
	Only         *GitlabOnlyExcept         `yaml:"only,omitempty"`
	Except       *GitlabOnlyExcept         `yaml:"except,omitempty"`

	// Job behavior
	When         string                    `yaml:"when,omitempty"`
	Manual       bool                      `yaml:"manual,omitempty"`
	AllowFailure interface{}               `yaml:"allow_failure,omitempty"`
	Retry        interface{}               `yaml:"retry,omitempty"`
	Timeout      string                    `yaml:"timeout,omitempty"`

	// Scripts
	BeforeScript []interface{}             `yaml:"before_script,omitempty"`
	AfterScript  []interface{}             `yaml:"after_script,omitempty"`

	// Variables and secrets
	Variables    map[string]interface{}    `yaml:"variables,omitempty"`
	Secrets      map[string]interface{}    `yaml:"secrets,omitempty"`
	InheritVariables *bool                 `yaml:"inherit,omitempty"`

	// Dependencies
	Needs        interface{}               `yaml:"needs,omitempty"`
	Dependencies []string                  `yaml:"dependencies,omitempty"`

	// Artifacts and cache
	Artifacts    *GitlabArtifacts          `yaml:"artifacts,omitempty"`
	Cache        interface{}               `yaml:"cache,omitempty"`

	// Runner selection
	Tags         []string                  `yaml:"tags,omitempty"`

	// Parallel execution
	Parallel     interface{}               `yaml:"parallel,omitempty"`

	// Environment
	Environment  interface{}               `yaml:"environment,omitempty"`

	// Coverage
	Coverage     string                    `yaml:"coverage,omitempty"`

	// Release
	Release      *GitlabRelease            `yaml:"release,omitempty"`

	// Pages
	Pages        interface{}               `yaml:"pages,omitempty"`

	// Downstream pipelines
	Trigger      interface{}               `yaml:"trigger,omitempty"`

	// Resource group
	ResourceGroup string                   `yaml:"resource_group,omitempty"`

	// Interruptible
	Interruptible *bool                    `yaml:"interruptible,omitempty"`
}

type GitlabRule struct {
	If           string                    `yaml:"if,omitempty"`
	Changes      interface{}               `yaml:"changes,omitempty"`
	Exists       []string                  `yaml:"exists,omitempty"`
	When         string                    `yaml:"when,omitempty"`
	StartIn      string                    `yaml:"start_in,omitempty"`
	AllowFailure interface{}               `yaml:"allow_failure,omitempty"`
	Variables    map[string]interface{}    `yaml:"variables,omitempty"`
}

type GitlabOnlyExcept struct {
	Refs         []string                  `yaml:"refs,omitempty"`
	Variables    []string                  `yaml:"variables,omitempty"`
	Changes      []string                  `yaml:"changes,omitempty"`
	Kubernetes   string                    `yaml:"kubernetes,omitempty"`
}

type GitlabArtifacts struct {
	Name         string                    `yaml:"name,omitempty"`
	Paths        []string                  `yaml:"paths,omitempty"`
	Exclude      []string                  `yaml:"exclude,omitempty"`
	ExpireIn     string                    `yaml:"expire_in,omitempty"`
	ExposeAs     string                    `yaml:"expose_as,omitempty"`
	Public       *bool                     `yaml:"public,omitempty"`
	Reports      map[string]interface{}    `yaml:"reports,omitempty"`
	Untracked    bool                      `yaml:"untracked,omitempty"`
	When         string                    `yaml:"when,omitempty"`
}

type GitlabCache struct {
	Key          interface{}               `yaml:"key,omitempty"`
	Paths        []string                  `yaml:"paths,omitempty"`
	Policy       string                    `yaml:"policy,omitempty"`
	Untracked    bool                      `yaml:"untracked,omitempty"`
	When         string                    `yaml:"when,omitempty"`
	Fallback     interface{}               `yaml:"fallback_keys,omitempty"`
}

type GitlabEnvironment struct {
	Name         string                    `yaml:"name"`
	URL          string                    `yaml:"url,omitempty"`
	OnStop       string                    `yaml:"on_stop,omitempty"`
	Action       string                    `yaml:"action,omitempty"`
	AutoStopIn   string                    `yaml:"auto_stop_in,omitempty"`
	Kubernetes   map[string]interface{}    `yaml:"kubernetes,omitempty"`
	Deployment   string                    `yaml:"deployment_tier,omitempty"`
}

type GitlabRelease struct {
	TagName      string                    `yaml:"tag_name"`
	Description  string                    `yaml:"description,omitempty"`
	Name         string                    `yaml:"name,omitempty"`
	Ref          string                    `yaml:"ref,omitempty"`
	Milestones   []string                  `yaml:"milestones,omitempty"`
	ReleasedAt   string                    `yaml:"released_at,omitempty"`
	Assets       *GitlabReleaseAssets      `yaml:"assets,omitempty"`
}

type GitlabReleaseAssets struct {
	Links        []GitlabAssetLink         `yaml:"links,omitempty"`
}

type GitlabAssetLink struct {
	Name         string                    `yaml:"name"`
	URL          string                    `yaml:"url"`
	LinkType     string                    `yaml:"link_type,omitempty"`
	Filepath     string                    `yaml:"filepath,omitempty"`
}

// Parse parses a GitLab CI configuration file
func (p *GitlabParser) Parse(ciFilePath string) (*types.Pipeline, error) {
	p.baseDir = filepath.Dir(ciFilePath)

	// Check if file exists
	if _, err := os.Stat(ciFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("GitLab CI file not found: %s", ciFilePath)
	}

	// Read file content
	data, err := os.ReadFile(ciFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read GitLab CI file: %w", err)
	}

	// Handle empty files
	if len(data) == 0 {
		return nil, fmt.Errorf("GitLab CI file is empty: %s", ciFilePath)
	}

	// Parse YAML into raw map first
	var rawData map[string]interface{}
	if err := yaml.Unmarshal(data, &rawData); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Extract GitLab CI structure
	gitlabCI := p.parseRawData(rawData)

	// Process includes if any
	if err := p.processIncludes(gitlabCI); err != nil {
		return nil, fmt.Errorf("failed to process includes: %w", err)
	}

	// Convert to generic Pipeline
	pipeline := p.convertToPipeline(gitlabCI)

	// Validate the pipeline
	if err := p.Validate(pipeline); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return pipeline, nil
}

// parseRawData converts raw YAML data to GitlabCI structure
func (p *GitlabParser) parseRawData(rawData map[string]interface{}) *GitlabCI {
	ci := &GitlabCI{
		Jobs: make(map[string]*GitlabJob),
	}

	// Reserved keywords that are not jobs
	reservedKeywords := map[string]bool{
		"image": true, "services": true, "stages": true,
		"variables": true, "cache": true, "before_script": true,
		"after_script": true, "workflow": true, "include": true,
		"default": true,
	}

	// Process global configuration
	if stages, ok := rawData["stages"].([]interface{}); ok {
		ci.Stages = p.parseStringArray(stages)
	}

	if variables, ok := rawData["variables"].(map[string]interface{}); ok {
		ci.Variables = variables
	}

	if image := rawData["image"]; image != nil {
		ci.Image = image
	}

	if services := rawData["services"]; services != nil {
		ci.Services = p.parseServices(services)
	}

	if cache := rawData["cache"]; cache != nil {
		ci.Cache = cache
	}

	if beforeScript := rawData["before_script"]; beforeScript != nil {
		ci.BeforeScript = p.parseScriptArray(beforeScript)
	}

	if afterScript := rawData["after_script"]; afterScript != nil {
		ci.AfterScript = p.parseScriptArray(afterScript)
	}

	if include := rawData["include"]; include != nil {
		ci.Include = include
	}

	// Process workflow rules
	if workflow, ok := rawData["workflow"].(map[string]interface{}); ok {
		ci.Workflow = p.parseWorkflow(workflow)
	}

	// Process default configuration
	if defaultConfig, ok := rawData["default"].(map[string]interface{}); ok {
		ci.Default = p.parseDefault(defaultConfig)
	}

	// Process jobs (everything that's not a reserved keyword)
	for name, jobData := range rawData {
		// Skip reserved keywords and hidden jobs (starting with .)
		if reservedKeywords[name] || strings.HasPrefix(name, ".") {
			continue
		}

		if jobMap, ok := jobData.(map[string]interface{}); ok {
			job := p.parseJob(jobMap)
			if job != nil {
				ci.Jobs[name] = job
			}
		}
	}

	return ci
}

// parseJob parses a GitLab job definition
func (p *GitlabParser) parseJob(jobData map[string]interface{}) *GitlabJob {
	job := &GitlabJob{}

	// Parse basic fields
	if stage, ok := jobData["stage"].(string); ok {
		job.Stage = stage
	}

	if image := jobData["image"]; image != nil {
		job.Image = image
	}

	if services := jobData["services"]; services != nil {
		job.Services = p.parseServices(services)
	}

	// Parse scripts (required for valid job)
	if script := jobData["script"]; script != nil {
		job.Script = p.parseScriptArray(script)
	} else {
		// Job without script is not valid (unless it's a trigger or pages job)
		if jobData["trigger"] == nil && jobData["pages"] == nil {
			return nil
		}
	}

	if beforeScript := jobData["before_script"]; beforeScript != nil {
		job.BeforeScript = p.parseScriptArray(beforeScript)
	}

	if afterScript := jobData["after_script"]; afterScript != nil {
		job.AfterScript = p.parseScriptArray(afterScript)
	}

	// Parse variables
	if variables, ok := jobData["variables"].(map[string]interface{}); ok {
		job.Variables = variables
	}

	// Parse control flow
	if when, ok := jobData["when"].(string); ok {
		job.When = when
	}

	if manual, ok := jobData["manual"].(bool); ok {
		job.Manual = manual
	}

	job.AllowFailure = jobData["allow_failure"]

	// Parse dependencies
	if needs := jobData["needs"]; needs != nil {
		job.Needs = needs
	}

	if dependencies, ok := jobData["dependencies"].([]interface{}); ok {
		job.Dependencies = p.parseStringArray(dependencies)
	}

	// Parse rules
	if rules, ok := jobData["rules"].([]interface{}); ok {
		job.Rules = p.parseRules(rules)
	}

	// Parse only/except (deprecated but still supported)
	if only, ok := jobData["only"].(map[string]interface{}); ok {
		job.Only = p.parseOnlyExcept(only)
	}

	if except, ok := jobData["except"].(map[string]interface{}); ok {
		job.Except = p.parseOnlyExcept(except)
	}

	// Parse artifacts
	if artifacts, ok := jobData["artifacts"].(map[string]interface{}); ok {
		job.Artifacts = p.parseArtifacts(artifacts)
	}

	// Parse cache
	job.Cache = jobData["cache"]

	// Parse tags
	if tags, ok := jobData["tags"].([]interface{}); ok {
		job.Tags = p.parseStringArray(tags)
	}

	// Parse timeout
	if timeout, ok := jobData["timeout"].(string); ok {
		job.Timeout = timeout
	}

	// Parse retry
	job.Retry = jobData["retry"]

	// Parse parallel
	job.Parallel = jobData["parallel"]

	// Parse environment
	job.Environment = jobData["environment"]

	// Parse coverage
	if coverage, ok := jobData["coverage"].(string); ok {
		job.Coverage = coverage
	}

	// Parse trigger
	job.Trigger = jobData["trigger"]

	// Parse resource_group
	if resourceGroup, ok := jobData["resource_group"].(string); ok {
		job.ResourceGroup = resourceGroup
	}

	// Parse interruptible
	if interruptible, ok := jobData["interruptible"].(bool); ok {
		job.Interruptible = &interruptible
	}

	// Parse extends
	job.Extends = jobData["extends"]

	// Parse secrets
	if secrets, ok := jobData["secrets"].(map[string]interface{}); ok {
		job.Secrets = secrets
	}

	// Parse release
	if release, ok := jobData["release"].(map[string]interface{}); ok {
		job.Release = p.parseRelease(release)
	}

	// Parse pages
	job.Pages = jobData["pages"]

	return job
}

// convertToPipeline converts GitLab CI to generic Pipeline
func (p *GitlabParser) convertToPipeline(ci *GitlabCI) *types.Pipeline {
	pipeline := &types.Pipeline{
		Name:        "GitLab CI Pipeline",
		Provider:    "gitlab",
		Jobs:        make(map[string]*types.Job),
		Stages:      ci.Stages,
		Environment: p.convertVariables(ci.Variables),
	}

	// Extract pipeline name from workflow if available
	if ci.Workflow != nil && len(ci.Workflow.Rules) > 0 {
		pipeline.Description = "GitLab CI Workflow"
	}

	// Set global defaults
	var globalImage string
	var globalBeforeScript []string
	var globalAfterScript []string

	if ci.Image != nil {
		globalImage = p.parseImage(ci.Image)
	}

	if ci.BeforeScript != nil {
		globalBeforeScript = p.convertScriptToStrings(ci.BeforeScript)
	}

	if ci.AfterScript != nil {
		globalAfterScript = p.convertScriptToStrings(ci.AfterScript)
	}

	// Apply defaults if specified
	if ci.Default != nil {
		if ci.Default.Image != nil {
			globalImage = p.parseImage(ci.Default.Image)
		}
		if ci.Default.BeforeScript != nil {
			globalBeforeScript = p.convertScriptToStrings(ci.Default.BeforeScript)
		}
		if ci.Default.AfterScript != nil {
			globalAfterScript = p.convertScriptToStrings(ci.Default.AfterScript)
		}
	}

	// Process jobs
	for jobName, glJob := range ci.Jobs {
		job := p.convertJob(jobName, glJob, globalImage, globalBeforeScript, globalAfterScript)
		pipeline.Jobs[jobName] = job
	}

	// If no stages defined, create them from jobs
	if len(pipeline.Stages) == 0 {
		pipeline.Stages = p.extractStages(ci.Jobs)
	}

	return pipeline
}

// convertJob converts GitLab job to generic Job
func (p *GitlabParser) convertJob(
	jobName string,
	glJob *GitlabJob,
	globalImage string,
	globalBeforeScript []string,
	globalAfterScript []string,
) *types.Job {
	job := &types.Job{
		Name:        jobName,
		Stage:       glJob.Stage,
		Environment: p.convertVariables(glJob.Variables),
		Tags:        glJob.Tags,
		When:        glJob.When,
	}

	// Set image/runs-on
	if glJob.Image != nil {
		job.Image = p.parseImage(glJob.Image)
		job.RunsOn = job.Image
	} else if globalImage != "" {
		job.Image = globalImage
		job.RunsOn = globalImage
	} else if len(glJob.Tags) > 0 {
		job.RunsOn = glJob.Tags[0]
	} else {
		job.RunsOn = "gitlab-runner"
	}

	// Parse container configuration
	if glJob.Image != nil || glJob.Services != nil {
		job.Container = &types.Container{
			Image: job.Image,
		}

		// Add services
		if glJob.Services != nil {
			job.Services = p.convertServices(glJob.Services)
		}
	}

	// Handle allow_failure
	switch v := glJob.AllowFailure.(type) {
	case bool:
		job.AllowFailure = v
		job.ContinueOnErr = v
	case map[string]interface{}:
		// Complex allow_failure with exit_codes
		job.AllowFailure = true
		job.ContinueOnErr = true
	}

	// Parse timeout
	if glJob.Timeout != "" {
		if minutes := p.parseTimeout(glJob.Timeout); minutes > 0 {
			job.TimeoutMin = minutes
		}
	}

	// Parse retry
	if glJob.Retry != nil {
		job.Retry = p.parseRetry(glJob.Retry)
	}

	// Parse needs
	job.Needs = p.parseNeeds(glJob.Needs)
	if len(job.Needs) == 0 && len(glJob.Dependencies) > 0 {
		job.Needs = glJob.Dependencies
	}

	// Parse parallel
	if glJob.Parallel != nil {
		job.Parallel = p.parseParallel(glJob.Parallel)
	}

	// Parse artifacts
	if glJob.Artifacts != nil {
		job.Artifacts = p.convertArtifacts(glJob.Artifacts)
	}

	// Parse cache
	if glJob.Cache != nil {
		job.Cache = p.parseCache(glJob.Cache)
	}

	// Parse environment
	if glJob.Environment != nil {
		job.EnvironmentName = p.parseEnvironment(glJob.Environment)
	}

	// Convert scripts to steps
	job.Steps = p.convertScriptsToSteps(
		glJob,
		globalBeforeScript,
		globalAfterScript,
	)

	// Parse rules for conditional execution
	if len(glJob.Rules) > 0 {
		job.Rules = p.convertRules(glJob.Rules)
		// Set If condition from first rule if available
		if len(glJob.Rules) > 0 && glJob.Rules[0].If != "" {
			job.If = glJob.Rules[0].If
		}
	}

	// Parse only/except (deprecated but still supported)
	if glJob.Only != nil {
		job.Only = p.convertOnlyExcept(glJob.Only)
	}
	if glJob.Except != nil {
		job.Except = p.convertOnlyExcept(glJob.Except)
	}

	// Handle trigger
	if glJob.Trigger != nil {
		job.Trigger = p.parseTrigger(glJob.Trigger)
	}

	// Set interruptible
	if glJob.Interruptible != nil {
		// Copy the value
		job.ContinueOnErr = !*glJob.Interruptible
	}

	return job
}

// convertScriptsToSteps converts GitLab scripts to generic Steps
func (p *GitlabParser) convertScriptsToSteps(
	job *GitlabJob,
	globalBeforeScript []string,
	globalAfterScript []string,
) []types.Step {
	var steps []types.Step
	stepCounter := 1

	// Add before_script as steps
	beforeScript := p.convertScriptToStrings(job.BeforeScript)
	if len(beforeScript) == 0 && len(globalBeforeScript) > 0 {
		beforeScript = globalBeforeScript
	}

	if len(beforeScript) > 0 {
		steps = append(steps, types.Step{
			Name:   "Before Script",
			Run:    strings.Join(beforeScript, "\n"),
			Script: beforeScript,
		})
		stepCounter++
	}

	// Add main script as steps
	mainScript := p.convertScriptToStrings(job.Script)
	if len(mainScript) > 0 {
		// If script has many commands, group them
		if len(mainScript) > 5 {
			steps = append(steps, types.Step{
				Name:   "Main Script",
				Run:    strings.Join(mainScript, "\n"),
				Script: mainScript,
			})
		} else {
			// Create individual steps for fewer commands
			for _, cmd := range mainScript {
				stepName := p.generateStepName(cmd, stepCounter)
				steps = append(steps, types.Step{
					Name:   stepName,
					Run:    cmd,
					Script: []string{cmd},
				})
				stepCounter++
			}
		}
	}

	// Add after_script as steps
	afterScript := p.convertScriptToStrings(job.AfterScript)
	if len(afterScript) == 0 && len(globalAfterScript) > 0 {
		afterScript = globalAfterScript
	}

	if len(afterScript) > 0 {
		steps = append(steps, types.Step{
			Name:          "After Script",
			Run:           strings.Join(afterScript, "\n"),
			Script:        afterScript,
			ContinueOnErr: true, // after_script typically runs regardless
		})
	}

	return steps
}

// Helper functions

func (p *GitlabParser) parseStringArray(data []interface{}) []string {
	var result []string
	for _, item := range data {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

func (p *GitlabParser) parseScriptArray(data interface{}) []interface{} {
	switch v := data.(type) {
	case []interface{}:
		return v
	case string:
		return []interface{}{v}
	case []string:
		result := make([]interface{}, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result
	}
	return nil
}

func (p *GitlabParser) convertScriptToStrings(data []interface{}) []string {
	var result []string
	for _, item := range data {
		if str, ok := item.(string); ok {
			result = append(result, str)
		}
	}
	return result
}

func (p *GitlabParser) parseServices(data interface{}) []interface{} {
	switch v := data.(type) {
	case []interface{}:
		return v
	case string:
		return []interface{}{v}
	}
	return nil
}

func (p *GitlabParser) convertServices(services []interface{}) map[string]*types.Service {
	result := make(map[string]*types.Service)

	for i, service := range services {
		serviceName := fmt.Sprintf("service-%d", i+1)

		switch v := service.(type) {
		case string:
			result[serviceName] = &types.Service{
				Image: v,
			}
		case map[string]interface{}:
			svc := &types.Service{}
			if name, ok := v["name"].(string); ok {
				serviceName = name
			}
			if image, ok := v["image"].(string); ok {
				svc.Image = image
			}
			if alias, ok := v["alias"].(string); ok {
				svc.Alias = alias
			}
			if command, ok := v["command"].([]interface{}); ok {
				svc.Command = p.parseStringArray(command)
			}
			if entrypoint, ok := v["entrypoint"].([]interface{}); ok {
				svc.Entrypoint = p.parseStringArray(entrypoint)
			}
			result[serviceName] = svc
		}
	}

	return result
}

func (p *GitlabParser) parseImage(data interface{}) string {
	switch v := data.(type) {
	case string:
		return v
	case map[string]interface{}:
		if name, ok := v["name"].(string); ok {
			return name
		}
	}
	return "alpine:latest"
}

func (p *GitlabParser) parseTimeout(timeout string) int {
	// Parse GitLab timeout format (e.g., "30 minutes", "1h 30m", "30m")
	timeout = strings.ToLower(timeout)

	// Simple parsing for common formats
	if strings.Contains(timeout, "hour") || strings.Contains(timeout, "h") {
		// Extract hours
		re := regexp.MustCompile(`(\d+)\s*(hours?|h)`)
		if matches := re.FindStringSubmatch(timeout); len(matches) > 1 {
			if hours, err := strconv.Atoi(matches[1]); err == nil {
				return hours * 60
			}
		}
	}

	if strings.Contains(timeout, "minute") || strings.Contains(timeout, "m") {
		// Extract minutes
		re := regexp.MustCompile(`(\d+)\s*(minutes?|m)`)
		if matches := re.FindStringSubmatch(timeout); len(matches) > 1 {
			if minutes, err := strconv.Atoi(matches[1]); err == nil {
				return minutes
			}
		}
	}

	// Try to parse as simple number (assumes minutes)
	if minutes, err := strconv.Atoi(timeout); err == nil {
		return minutes
	}

	return 0
}

func (p *GitlabParser) parseRetry(retry interface{}) *types.RetryPolicy {
	switch v := retry.(type) {
	case int:
		return &types.RetryPolicy{
			MaxAttempts: v,
		}
	case map[string]interface{}:
		policy := &types.RetryPolicy{}
		if max, ok := v["max"].(int); ok {
			policy.MaxAttempts = max
		}
		if when, ok := v["when"].([]interface{}); ok {
			policy.When = p.parseStringArray(when)
		}
		return policy
	}
	return nil
}

func (p *GitlabParser) parseNeeds(needs interface{}) []string {
	var result []string

	switch v := needs.(type) {
	case string:
		result = append(result, v)
	case []interface{}:
		for _, need := range v {
			switch n := need.(type) {
			case string:
				result = append(result, n)
			case map[string]interface{}:
				// Handle complex needs with job/project/ref
				if job, ok := n["job"].(string); ok {
					result = append(result, job)
				}
			}
		}
	}

	return result
}

func (p *GitlabParser) parseParallel(parallel interface{}) *types.Parallel {
	switch v := parallel.(type) {
	case int:
		return &types.Parallel{
			Total: v,
		}
	case map[string]interface{}:
		p := &types.Parallel{}
		if total, ok := v["total"].(int); ok {
			p.Total = total
		}
		if matrix, ok := v["matrix"].([]interface{}); ok {
			p.Matrix = make([]map[string]interface{}, len(matrix))
			for i, m := range matrix {
				if mMap, ok := m.(map[string]interface{}); ok {
					p.Matrix[i] = mMap
				}
			}
		}
		return p
	}
	return nil
}

func (p *GitlabParser) parseCache(cache interface{}) *types.CacheConfig {
	switch v := cache.(type) {
	case map[string]interface{}:
		c := &types.CacheConfig{}

		if key := v["key"]; key != nil {
			c.Key = fmt.Sprintf("%v", key)
		}

		if paths, ok := v["paths"].([]interface{}); ok {
			c.Paths = p.parseStringArray(paths)
		}

		if policy, ok := v["policy"].(string); ok {
			c.Policy = policy
		}

		if untracked, ok := v["untracked"].(bool); ok {
			c.Untracked = untracked
		}

		if when, ok := v["when"].(string); ok {
			c.When = when
		}

		return c
	case []interface{}:
		// Multiple caches - return first one for simplicity
		if len(v) > 0 {
			return p.parseCache(v[0])
		}
	}
	return nil
}

func (p *GitlabParser) parseEnvironment(env interface{}) string {
	switch v := env.(type) {
	case string:
		return v
	case map[string]interface{}:
		if name, ok := v["name"].(string); ok {
			return name
		}
	}
	return ""
}

func (p *GitlabParser) parseTrigger(trigger interface{}) *types.TriggerConfig {
	switch v := trigger.(type) {
	case string:
		return &types.TriggerConfig{
			Project: v,
		}
	case map[string]interface{}:
		t := &types.TriggerConfig{}
		if project, ok := v["project"].(string); ok {
			t.Project = project
		}
		if branch, ok := v["branch"].(string); ok {
			t.Branch = branch
		}
		if strategy, ok := v["strategy"].(string); ok {
			t.Strategy = strategy
		}
		return t
	}
	return nil
}

func (p *GitlabParser) convertVariables(vars map[string]interface{}) map[string]string {
	result := make(map[string]string)
	for k, v := range vars {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

func (p *GitlabParser) convertArtifacts(artifacts *GitlabArtifacts) *types.ArtifactConfig {
	return &types.ArtifactConfig{
		Name:      artifacts.Name,
		Paths:     artifacts.Paths,
		Exclude:   artifacts.Exclude,
		ExpireIn:  artifacts.ExpireIn,
		When:      artifacts.When,
		Untracked: artifacts.Untracked,
		Public:    artifacts.Public != nil && *artifacts.Public,
	}
}

func (p *GitlabParser) convertRules(rules []GitlabRule) []types.Rule {
	var result []types.Rule
	for _, r := range rules {
		rule := types.Rule{
			If:           r.If,
			When:         r.When,
			Variables:    p.convertVariables(r.Variables),
		}

		// Parse changes
		switch v := r.Changes.(type) {
		case []interface{}:
			rule.Changes = p.parseStringArray(v)
		case string:
			rule.Changes = []string{v}
		}

		rule.Exists = r.Exists

		// Parse allow_failure
		switch af := r.AllowFailure.(type) {
		case bool:
			rule.AllowFailure = af
		}

		result = append(result, rule)
	}
	return result
}

func (p *GitlabParser) convertOnlyExcept(oe *GitlabOnlyExcept) *types.OnlyExcept {
	return &types.OnlyExcept{
		Refs:      oe.Refs,
		Changes:   oe.Changes,
		Variables: oe.Variables,
		Kubernetes: oe.Kubernetes,
	}
}

func (p *GitlabParser) parseWorkflow(workflow map[string]interface{}) *GitlabWorkflow {
	w := &GitlabWorkflow{}

	if rules, ok := workflow["rules"].([]interface{}); ok {
		w.Rules = p.parseRules(rules)
	}

	return w
}

func (p *GitlabParser) parseDefault(defaultConfig map[string]interface{}) *GitlabDefault {
	d := &GitlabDefault{}

	if image := defaultConfig["image"]; image != nil {
		d.Image = image
	}

	if services := defaultConfig["services"]; services != nil {
		d.Services = p.parseServices(services)
	}

	if beforeScript := defaultConfig["before_script"]; beforeScript != nil {
		d.BeforeScript = p.parseScriptArray(beforeScript)
	}

	if afterScript := defaultConfig["after_script"]; afterScript != nil {
		d.AfterScript = p.parseScriptArray(afterScript)
	}

	if tags, ok := defaultConfig["tags"].([]interface{}); ok {
		d.Tags = p.parseStringArray(tags)
	}

	if timeout, ok := defaultConfig["timeout"].(string); ok {
		d.Timeout = timeout
	}

	if interruptible, ok := defaultConfig["interruptible"].(bool); ok {
		d.Interruptible = interruptible
	}

	return d
}

func (p *GitlabParser) parseRules(rules []interface{}) []GitlabRule {
	var result []GitlabRule

	for _, r := range rules {
		if ruleMap, ok := r.(map[string]interface{}); ok {
			rule := GitlabRule{}

			if ifCond, ok := ruleMap["if"].(string); ok {
				rule.If = ifCond
			}

			rule.Changes = ruleMap["changes"]

			if exists, ok := ruleMap["exists"].([]interface{}); ok {
				rule.Exists = p.parseStringArray(exists)
			}

			if when, ok := ruleMap["when"].(string); ok {
				rule.When = when
			}

			if startIn, ok := ruleMap["start_in"].(string); ok {
				rule.StartIn = startIn
			}

			rule.AllowFailure = ruleMap["allow_failure"]

			if variables, ok := ruleMap["variables"].(map[string]interface{}); ok {
				rule.Variables = variables
			}

			result = append(result, rule)
		}
	}

	return result
}

func (p *GitlabParser) parseOnlyExcept(data map[string]interface{}) *GitlabOnlyExcept {
	oe := &GitlabOnlyExcept{}

	if refs, ok := data["refs"].([]interface{}); ok {
		oe.Refs = p.parseStringArray(refs)
	}

	if variables, ok := data["variables"].([]interface{}); ok {
		oe.Variables = p.parseStringArray(variables)
	}

	if changes, ok := data["changes"].([]interface{}); ok {
		oe.Changes = p.parseStringArray(changes)
	}

	if kubernetes, ok := data["kubernetes"].(string); ok {
		oe.Kubernetes = kubernetes
	}

	return oe
}

func (p *GitlabParser) parseArtifacts(artifacts map[string]interface{}) *GitlabArtifacts {
	a := &GitlabArtifacts{}

	if name, ok := artifacts["name"].(string); ok {
		a.Name = name
	}

	if paths, ok := artifacts["paths"].([]interface{}); ok {
		a.Paths = p.parseStringArray(paths)
	}

	if exclude, ok := artifacts["exclude"].([]interface{}); ok {
		a.Exclude = p.parseStringArray(exclude)
	}

	if expireIn, ok := artifacts["expire_in"].(string); ok {
		a.ExpireIn = expireIn
	}

	if exposeAs, ok := artifacts["expose_as"].(string); ok {
		a.ExposeAs = exposeAs
	}

	if public, ok := artifacts["public"].(bool); ok {
		a.Public = &public
	}

	if reports, ok := artifacts["reports"].(map[string]interface{}); ok {
		a.Reports = reports
	}

	if untracked, ok := artifacts["untracked"].(bool); ok {
		a.Untracked = untracked
	}

	if when, ok := artifacts["when"].(string); ok {
		a.When = when
	}

	return a
}

func (p *GitlabParser) parseRelease(release map[string]interface{}) *GitlabRelease {
	r := &GitlabRelease{}

	if tagName, ok := release["tag_name"].(string); ok {
		r.TagName = tagName
	}

	if description, ok := release["description"].(string); ok {
		r.Description = description
	}

	if name, ok := release["name"].(string); ok {
		r.Name = name
	}

	if ref, ok := release["ref"].(string); ok {
		r.Ref = ref
	}

	if milestones, ok := release["milestones"].([]interface{}); ok {
		r.Milestones = p.parseStringArray(milestones)
	}

	if releasedAt, ok := release["released_at"].(string); ok {
		r.ReleasedAt = releasedAt
	}

	// Parse assets
	if assets, ok := release["assets"].(map[string]interface{}); ok {
		r.Assets = &GitlabReleaseAssets{}

		if links, ok := assets["links"].([]interface{}); ok {
			for _, link := range links {
				if linkMap, ok := link.(map[string]interface{}); ok {
					assetLink := GitlabAssetLink{}

					if name, ok := linkMap["name"].(string); ok {
						assetLink.Name = name
					}

					if url, ok := linkMap["url"].(string); ok {
						assetLink.URL = url
					}

					if linkType, ok := linkMap["link_type"].(string); ok {
						assetLink.LinkType = linkType
					}

					if filepath, ok := linkMap["filepath"].(string); ok {
						assetLink.Filepath = filepath
					}

					r.Assets.Links = append(r.Assets.Links, assetLink)
				}
			}
		}
	}

	return r
}

func (p *GitlabParser) generateStepName(cmd string, index int) string {
	// Clean and truncate command for step name
	cmd = strings.TrimSpace(cmd)

	// Remove common prefixes
	prefixes := []string{"echo ", "npm ", "yarn ", "make ", "docker ", "git "}
	for _, prefix := range prefixes {
		if strings.HasPrefix(cmd, prefix) {
			cmd = strings.TrimPrefix(cmd, prefix)
			break
		}
	}

	// Truncate if too long
	if len(cmd) > 50 {
		cmd = cmd[:47] + "..."
	}

	if cmd == "" {
		return fmt.Sprintf("Step %d", index)
	}

	return cmd
}

func (p *GitlabParser) extractStages(jobs map[string]*GitlabJob) []string {
	stageMap := make(map[string]bool)
	var stages []string

	// Collect unique stages
	for _, job := range jobs {
		if job.Stage != "" && !stageMap[job.Stage] {
			stageMap[job.Stage] = true
			stages = append(stages, job.Stage)
		}
	}

	// If no stages defined, use default
	if len(stages) == 0 {
		stages = []string{"test", "build", "deploy"}
	}

	return stages
}

func (p *GitlabParser) processIncludes(ci *GitlabCI) error {
	// Process include directives
	if ci.Include == nil {
		return nil
	}

	// Handle different include formats
	switch v := ci.Include.(type) {
	case string:
		return p.includeFile(v, ci)
	case []interface{}:
		for _, include := range v {
			if err := p.processInclude(include, ci); err != nil {
				return err
			}
		}
	case map[string]interface{}:
		return p.processInclude(v, ci)
	}

	return nil
}

func (p *GitlabParser) processInclude(include interface{}, ci *GitlabCI) error {
	switch v := include.(type) {
	case string:
		return p.includeFile(v, ci)
	case map[string]interface{}:
		// Handle different include types
		if local, ok := v["local"].(string); ok {
			return p.includeFile(filepath.Join(p.baseDir, local), ci)
		}
		if file, ok := v["file"].(string); ok {
			// Handle project file includes
			return p.includeFile(file, ci)
		}
		if template, ok := v["template"].(string); ok {
			// Handle template includes (would need template resolution)
			fmt.Printf("Template include not yet supported: %s\n", template)
		}
		if remote, ok := v["remote"].(string); ok {
			// Handle remote includes (would need HTTP fetch)
			fmt.Printf("Remote include not yet supported: %s\n", remote)
		}
	}
	return nil
}

func (p *GitlabParser) includeFile(path string, ci *GitlabCI) error {
	// Check cache first
	if cached, ok := p.includeCache[path]; ok {
		p.mergeCI(ci, cached)
		return nil
	}

	// Read and parse included file
	data, err := os.ReadFile(path)
	if err != nil {
		// Non-fatal: included file might not exist
		return nil
	}

	var rawData map[string]interface{}
	if err := yaml.Unmarshal(data, &rawData); err != nil {
		return fmt.Errorf("failed to parse included file %s: %w", path, err)
	}

	includedCI := p.parseRawData(rawData)

	// Cache for future use
	p.includeCache[path] = includedCI

	// Merge into main CI
	p.mergeCI(ci, includedCI)

	return nil
}

func (p *GitlabParser) mergeCI(target, source *GitlabCI) {
	// Merge jobs
	for name, job := range source.Jobs {
		if target.Jobs == nil {
			target.Jobs = make(map[string]*GitlabJob)
		}
		// Don't override existing jobs
		if _, exists := target.Jobs[name]; !exists {
			target.Jobs[name] = job
		}
	}

	// Merge variables
	if target.Variables == nil && source.Variables != nil {
		target.Variables = source.Variables
	}

	// Merge stages
	if len(target.Stages) == 0 && len(source.Stages) > 0 {
		target.Stages = source.Stages
	}

	// Merge defaults
	if target.Default == nil && source.Default != nil {
		target.Default = source.Default
	}
}

// Validate validates the parsed pipeline
func (p *GitlabParser) Validate(pipeline *types.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	var errors []string

	// Validate jobs
	if len(pipeline.Jobs) == 0 {
		errors = append(errors, "no jobs defined in pipeline")
	}

	// Validate job stages
	stageMap := make(map[string]bool)
	for _, stage := range pipeline.Stages {
		stageMap[stage] = true
	}

	for jobName, job := range pipeline.Jobs {
		// Validate job has steps or is a trigger
		if len(job.Steps) == 0 && job.Trigger == nil {
			errors = append(errors, fmt.Sprintf("job '%s' has no steps or trigger", jobName))
		}

		// Validate stage exists if specified
		if job.Stage != "" && len(stageMap) > 0 && !stageMap[job.Stage] {
			errors = append(errors, fmt.Sprintf("job '%s' references undefined stage '%s'", jobName, job.Stage))
		}

		// Validate job dependencies exist
		for _, need := range job.Needs {
			if _, exists := pipeline.Jobs[need]; !exists {
				errors = append(errors, fmt.Sprintf("job '%s' depends on non-existent job '%s'", jobName, need))
			}
		}

		// Check for circular dependencies
		if err := p.checkCircularDependencies(jobName, job, pipeline.Jobs, []string{}); err != nil {
			errors = append(errors, err.Error())
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

func (p *GitlabParser) checkCircularDependencies(jobName string, job *types.Job, allJobs map[string]*types.Job, visited []string) error {
	// Check if we've already visited this job (circular dependency)
	for _, v := range visited {
		if v == jobName {
			return fmt.Errorf("circular dependency detected: %s", strings.Join(append(visited, jobName), " -> "))
		}
	}

	visited = append(visited, jobName)

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

// ParseDirectory parses all GitLab CI files in a directory
func (p *GitlabParser) ParseDirectory(dir string) ([]*types.Pipeline, error) {
	var pipelines []*types.Pipeline

	// Check for .gitlab-ci.yml in root
	mainFile := filepath.Join(dir, ".gitlab-ci.yml")
	if _, err := os.Stat(mainFile); err == nil {
		pipeline, err := p.Parse(mainFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", mainFile, err)
		}
		pipelines = append(pipelines, pipeline)
	}

	// Also check for .gitlab-ci.yaml
	altFile := filepath.Join(dir, ".gitlab-ci.yaml")
	if _, err := os.Stat(altFile); err == nil {
		pipeline, err := p.Parse(altFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", altFile, err)
		}
		pipelines = append(pipelines, pipeline)
	}

	if len(pipelines) == 0 {
		return nil, fmt.Errorf("no GitLab CI files found in %s", dir)
	}

	return pipelines, nil
}

// GetProviderName returns the name of this parser
func (p *GitlabParser) GetProviderName() string {
	return "gitlab"
}
