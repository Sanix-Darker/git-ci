package parsers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	yaml "gopkg.in/yaml.v3"
)

// CircleCIParser parses CircleCI configuration files (.circleci/config.yml)
type CircleCIParser struct {
	baseDir string
}

// NewCircleCIParser creates a new CircleCI parser
func NewCircleCIParser() *CircleCIParser {
	return &CircleCIParser{}
}

// CircleCI config top-level structure
type CircleCIConfig struct {
	Version   interface{}                 `yaml:"version"`
	Orbs      map[string]string           `yaml:"orbs,omitempty"`
	Executors map[string]CircleCIExecutor `yaml:"executors,omitempty"`
	Jobs      map[string]CircleCIJob      `yaml:"jobs,omitempty"`
	Workflows map[string]interface{}      `yaml:"workflows,omitempty"`
}

type CircleCIExecutor struct {
	Docker        []CircleCIDockerImage `yaml:"docker,omitempty"`
	Machine       interface{}           `yaml:"machine,omitempty"`
	MacOS         interface{}           `yaml:"macos,omitempty"`
	ResourceClass string                `yaml:"resource_class,omitempty"`
	Shell         string                `yaml:"shell,omitempty"`
	Environment   map[string]string     `yaml:"environment,omitempty"`
}

type CircleCIDockerImage struct {
	Image       string            `yaml:"image"`
	Auth        map[string]string `yaml:"auth,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Entrypoint  interface{}       `yaml:"entrypoint,omitempty"`
	Command     interface{}       `yaml:"command,omitempty"`
	User        string            `yaml:"user,omitempty"`
}

type CircleCIJob struct {
	Docker        []CircleCIDockerImage  `yaml:"docker,omitempty"`
	Machine       interface{}            `yaml:"machine,omitempty"`
	MacOS         interface{}            `yaml:"macos,omitempty"`
	Executor      string                 `yaml:"executor,omitempty"`
	ResourceClass string                 `yaml:"resource_class,omitempty"`
	Shell         string                 `yaml:"shell,omitempty"`
	Environment   map[string]string      `yaml:"environment,omitempty"`
	Parameters    map[string]interface{} `yaml:"parameters,omitempty"`
	Steps         []interface{}          `yaml:"steps"`
	Parallelism   int                    `yaml:"parallelism,omitempty"`
}

// Parse parses a CircleCI configuration file
func (p *CircleCIParser) Parse(ciFilePath string) (*types.Pipeline, error) {
	p.baseDir = filepath.Dir(ciFilePath)

	if _, err := os.Stat(ciFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("CircleCI file not found: %s", ciFilePath)
	}

	data, err := os.ReadFile(ciFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read CircleCI file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("CircleCI file is empty: %s", ciFilePath)
	}

	var config CircleCIConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(false)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	pipeline := p.convertToPipeline(&config)
	if err := p.Validate(pipeline); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return pipeline, nil
}

// convertToPipeline converts CircleCI config to generic Pipeline
func (p *CircleCIParser) convertToPipeline(config *CircleCIConfig) *types.Pipeline {
	pipeline := &types.Pipeline{
		Name:     "CircleCI Pipeline",
		Provider: "circleci",
		Jobs:     make(map[string]*types.Job),
	}

	// Build executor lookup map
	executorMap := make(map[string]*CircleCIExecutor)
	for name, exec := range config.Executors {
		exec := exec
		executorMap[name] = &exec
	}

	// Track which job definitions have been accounted for in workflows
	jobsInWorkflow := make(map[string]bool)

	// Iterate workflow entries directly. Each entry creates a pipeline job.
	// This correctly handles multiple entries referencing the same job
	// definition (e.g., 'test' invoked with different name: and parameters).
	for _, workflowVal := range config.Workflows {
		workflowMap, ok := workflowVal.(map[string]interface{})
		if !ok {
			continue
		}

		jobsRaw, ok := workflowMap["jobs"]
		if !ok {
			continue
		}

		jobsList, ok := jobsRaw.([]interface{})
		if !ok {
			continue
		}

		for _, jobEntry := range jobsList {
			var wfName, jobRef string
			var deps []string

			switch v := jobEntry.(type) {
			case string:
				wfName, jobRef = v, v
			case map[string]interface{}:
				for ref, details := range v {
					jobRef = ref
					wfName = ref
					if dm, ok := details.(map[string]interface{}); ok {
						// CircleCI 'name:' field overrides the job reference for requires:
						if alias, ok := dm["name"].(string); ok && alias != "" {
							wfName = alias
						}
						if requires, ok := dm["requires"].([]interface{}); ok {
							for _, req := range requires {
								if str, ok := req.(string); ok {
									deps = append(deps, str)
								}
							}
						}
					}
				}
			}

			if ciJob, ok := config.Jobs[jobRef]; ok {
				jobsInWorkflow[jobRef] = true
				job := p.convertJob(jobRef, ciJob, executorMap)
				job.Needs = deps
				job.Requires = deps
				pipeline.Jobs[wfName] = job
			}
		}
	}

	// Register any job definitions not referenced in a workflow
	// (e.g., when no workflows section exists)
	for jobName, ciJob := range config.Jobs {
		if !jobsInWorkflow[jobName] {
			if _, exists := pipeline.Jobs[jobName]; !exists {
				pipeline.Jobs[jobName] = p.convertJob(jobName, ciJob, executorMap)
			}
		}
	}

	return pipeline
}

// convertJob converts a CircleCI job to generic Job
func (p *CircleCIParser) convertJob(jobName string, ciJob CircleCIJob, executorMap map[string]*CircleCIExecutor) *types.Job {
	job := &types.Job{
		Name:          jobName,
		RunsOn:        p.getRunsOn(jobName, ciJob, executorMap),
		ResourceClass: ciJob.ResourceClass,
		Environment:   ciJob.Environment,
		Steps:         p.convertSteps(ciJob.Steps),
	}

	// Handle parallelism (similar to matrix)
	if ciJob.Parallelism > 0 {
		job.Parallel = &types.Parallel{
			Total: ciJob.Parallelism,
		}
	}

	// Get image from executor or inline docker
	image := p.getImage(jobName, ciJob, executorMap)
	if image != "" {
		job.Image = image
		job.Container = &types.Container{
			Image: image,
		}
	}

	return job
}

// getRunsOn determines the runner label from executor or inline docker
func (p *CircleCIParser) getRunsOn(jobName string, ciJob CircleCIJob, executorMap map[string]*CircleCIExecutor) string {
	if ciJob.ResourceClass != "" {
		return ciJob.ResourceClass
	}

	if len(ciJob.Docker) > 0 {
		return ciJob.Docker[0].Image
	}

	if ciJob.Executor != "" {
		if exec, ok := executorMap[ciJob.Executor]; ok && len(exec.Docker) > 0 {
			return exec.Docker[0].Image
		}
	}

	return "ubuntu-latest"
}

// getImage extracts the primary Docker image from executor or inline config
func (p *CircleCIParser) getImage(jobName string, ciJob CircleCIJob, executorMap map[string]*CircleCIExecutor) string {
	if len(ciJob.Docker) > 0 {
		return ciJob.Docker[0].Image
	}

	if ciJob.Executor != "" {
		if exec, ok := executorMap[ciJob.Executor]; ok && len(exec.Docker) > 0 {
			return exec.Docker[0].Image
		}
	}

	return ""
}

// convertSteps converts CircleCI steps to generic Steps
func (p *CircleCIParser) convertSteps(ciSteps []interface{}) []types.Step {
	var steps []types.Step

	for _, rawStep := range ciSteps {
		step := types.Step{}

		switch v := rawStep.(type) {
		case string:
			// Simple step like "checkout"
			if v == "checkout" {
				step.Name = "Checkout"
				step.Uses = "actions/checkout"
			} else {
				step.Name = fmt.Sprintf("Run %s", v)
			}

		case map[string]interface{}:
			for key, val := range v {
				switch key {
				case "run":
					runMap, ok := val.(map[string]interface{})
					if !ok {
						// Simple string command
						step.Name = "Run"
						step.Run = fmt.Sprintf("%v", val)
						break
					}

					if name, ok := runMap["name"].(string); ok {
						step.Name = name
					} else {
						step.Name = "Run"
					}

					if command, ok := runMap["command"].(string); ok {
						step.Run = command
					}

					if shell, ok := runMap["shell"].(string); ok {
						step.Shell = shell
					}

					if background, ok := runMap["background"].(bool); ok {
						step.Background = background
					}

					if workingDir, ok := runMap["working_directory"].(string); ok {
						step.WorkingDir = workingDir
					}

					if when, ok := runMap["when"].(string); ok {
						step.When = when
					}

				case "checkout":
					step.Name = "Checkout"
					step.Uses = "actions/checkout"

				case "setup_remote_docker":
					step.Name = "Setup remote Docker"
					step.Run = "setup_remote_docker"

				case "persist_to_workspace", "attach_workspace":
					step.Name = p.camelToTitle(key)
					step.Command = key
					step.Run = fmt.Sprintf("%s %v", key, val)

				case "store_artifacts":
					step.Name = "Store artifacts"
					step.Command = "store_artifacts"

				case "store_test_results":
					step.Name = "Store test results"
					step.Command = "store_test_results"

				default:
					// Orb command (e.g., "go/mod-download", "go/test", "node/install-packages")
					step.Name = p.orbStepName(key)
					step.Uses = key
					// Store options as parameters
					if paramsMap, ok := val.(map[string]interface{}); ok {
						params := make(map[string]string)
						for k, pv := range paramsMap {
							params[k] = fmt.Sprintf("%v", pv)
						}
						step.Parameters = params
					}
				}
			}
		}

		if step.Name == "" {
			step.Name = fmt.Sprintf("Step %d", len(steps)+1)
		}

		steps = append(steps, step)
	}

	return steps
}

// orbStepName generates a human-readable name from an orb command
func (p *CircleCIParser) orbStepName(orbRef string) string {
	caser := cases.Title(language.English)
	parts := strings.Split(orbRef, "/")
	if len(parts) == 2 {
		orbName := strings.ReplaceAll(parts[0], "-", " ")
		cmdName := strings.ReplaceAll(parts[1], "-", " ")
		return fmt.Sprintf("%s: %s", caser.String(orbName), caser.String(cmdName))
	}
	return strings.ReplaceAll(orbRef, "-", " ")
}

// camelToTitle converts camelCase or kebab-case to title case
func (p *CircleCIParser) camelToTitle(s string) string {
	caser := cases.Title(language.English)
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	return caser.String(s)
}

// Validate validates the parsed pipeline
func (p *CircleCIParser) Validate(pipeline *types.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	var errors []string
	if len(pipeline.Jobs) == 0 {
		errors = append(errors, "no jobs defined in pipeline")
	}

	jobIDs := make(map[string]bool)
	for jobID := range pipeline.Jobs {
		jobIDs[jobID] = true
	}

	for jobID, job := range pipeline.Jobs {
		if len(job.Steps) == 0 {
			errors = append(errors, fmt.Sprintf("job '%s' has no steps", jobID))
		}

		for _, need := range job.Needs {
			if !jobIDs[need] {
				errors = append(errors, fmt.Sprintf("job '%s' depends on non-existent job '%s'", jobID, need))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// GetProviderName returns the name of this parser
func (p *CircleCIParser) GetProviderName() string {
	return "circleci"
}

// ParseDirectory parses all CircleCI config files in a directory
func (p *CircleCIParser) ParseDirectory(dir string) ([]*types.Pipeline, error) {
	mainFile := filepath.Join(dir, ".circleci", "config.yml")
	if _, err := os.Stat(mainFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("CircleCI config not found in %s", dir)
	}

	pipeline, err := p.Parse(mainFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CircleCI config: %w", err)
	}

	return []*types.Pipeline{pipeline}, nil
}
