package parsers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	yaml "gopkg.in/yaml.v3"
)

// DroneParser parses Drone CI configuration files (.drone.yml)
type DroneParser struct {
	baseDir string
}

// NewDroneParser creates a new Drone CI parser
func NewDroneParser() *DroneParser {
	return &DroneParser{}
}

// Drone CI top-level structures
type DronePipeline struct {
	Kind        string            `yaml:"kind"`
	Type        string            `yaml:"type,omitempty"`
	Name        string            `yaml:"name,omitempty"`
	Platform    *DronePlatform    `yaml:"platform,omitempty"`
	Node        map[string]string `yaml:"node,omitempty"`
	Trigger     *DroneTrigger     `yaml:"trigger,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Services    []DroneService    `yaml:"services,omitempty"`
	Steps       []DroneStep       `yaml:"steps"`
	Volumes     []DroneVolume     `yaml:"volumes,omitempty"`
	Workspace   *DroneWorkspace   `yaml:"workspace,omitempty"`
	Concurrency *DroneConcurrency `yaml:"concurrency,omitempty"`
	Clone       *DroneClone       `yaml:"clone,omitempty"`
}

type DronePlatform struct {
	OS      string `yaml:"os,omitempty"`
	Arch    string `yaml:"arch,omitempty"`
	Variant string `yaml:"variant,omitempty"`
	Version string `yaml:"version,omitempty"`
}

type DroneTrigger struct {
	Branch     *DroneStringSlice `yaml:"branch,omitempty"`
	Event      *DroneStringSlice `yaml:"event,omitempty"`
	Ref        *DroneStringSlice `yaml:"ref,omitempty"`
	Repo       *DroneStringSlice `yaml:"repo,omitempty"`
	Target     *DroneStringSlice `yaml:"target,omitempty"`
	Status     *DroneStringSlice `yaml:"status,omitempty"`
	Cron       string            `yaml:"cron,omitempty"`
	Action     string            `yaml:"action,omitempty"`
	PromoteTo  string            `yaml:"promote_to,omitempty"`
	RollbackTo string            `yaml:"rollback_to,omitempty"`
}

type DroneStringSlice []string

// UnmarshalYAML handles both single string and list of strings
func (d *DroneStringSlice) UnmarshalYAML(value *yaml.Node) error {
	var single string
	if err := value.Decode(&single); err == nil {
		*d = []string{single}
		return nil
	}

	var multi []string
	if err := value.Decode(&multi); err == nil {
		*d = multi
		return nil
	}

	var objs []map[string]interface{}
	if err := value.Decode(&objs); err == nil {
		for _, obj := range objs {
			if exclude, ok := obj["exclude"]; ok {
				if str, ok := exclude.(string); ok {
					*d = append(*d, "!"+str)
				}
			}
		}
		return nil
	}

	return nil
}

type DroneStep struct {
	Name        string                 `yaml:"name"`
	Image       string                 `yaml:"image,omitempty"`
	Commands    []string               `yaml:"commands,omitempty"`
	Environment map[string]string      `yaml:"environment,omitempty"`
	Settings    map[string]interface{} `yaml:"settings,omitempty"`
	DependsOn   []string               `yaml:"depends_on,omitempty"`
	When        *DroneTrigger          `yaml:"when,omitempty"`
	Volumes     []string               `yaml:"volumes,omitempty"`
	Detach      bool                   `yaml:"detach,omitempty"`
	Privileged  bool                   `yaml:"privileged,omitempty"`
	Entrypoint  []string               `yaml:"entrypoint,omitempty"`
	Command     []string               `yaml:"command,omitempty"`
	Failure     string                 `yaml:"failure,omitempty"`
	Pull        string                 `yaml:"pull,omitempty"`
}

type DroneService struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
	Privileged  bool              `yaml:"privileged,omitempty"`
}

type DroneVolume struct {
	Name string        `yaml:"name,omitempty"`
	Host *DroneHostVol `yaml:"host,omitempty"`
	Temp *DroneHostVol `yaml:"temp,omitempty"`
}

type DroneHostVol struct {
	Path string `yaml:"path,omitempty"`
}

type DroneWorkspace struct {
	Path string `yaml:"path,omitempty"`
	Base string `yaml:"base,omitempty"`
}

type DroneClone struct {
	Disable bool `yaml:"disable,omitempty"`
	Depth   int  `yaml:"depth,omitempty"`
}

type DroneConcurrency struct {
	Limit int `yaml:"limit,omitempty"`
}

// Parse parses a Drone CI configuration file
func (p *DroneParser) Parse(ciFilePath string) (*types.Pipeline, error) {
	p.baseDir = filepath.Dir(ciFilePath)

	if _, err := os.Stat(ciFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("drone CI file not found: %s", ciFilePath)
	}

	data, err := os.ReadFile(ciFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Drone CI file: %w", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("drone CI file is empty: %s", ciFilePath)
	}

	var pipeline DronePipeline
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(false)
	if err := decoder.Decode(&pipeline); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	genericPipeline := p.convertToPipeline(&pipeline)
	if err := p.Validate(genericPipeline); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return genericPipeline, nil
}

// convertToPipeline converts Drone pipeline to generic Pipeline
func (p *DroneParser) convertToPipeline(drone *DronePipeline) *types.Pipeline {
	pipeline := &types.Pipeline{
		Name:        drone.Name,
		Description: fmt.Sprintf("Drone CI pipeline: %s", drone.Name),
		Provider:    "drone",
		Jobs:        make(map[string]*types.Job),
		Environment: drone.Environment,
	}

	// Drone has a single pipeline with steps as "jobs"
	// In Drone, steps run sequentially in a shared workspace
	if len(drone.Name) == 0 {
		pipeline.Name = "Drone Pipeline"
	}

	// Convert services
	var services map[string]*types.Service
	if len(drone.Services) > 0 {
		services = make(map[string]*types.Service)
		for _, svc := range drone.Services {
			svcName := svc.Name
			if svcName == "" {
				svcName = fmt.Sprintf("service-%d", len(services)+1)
			}
			services[svcName] = &types.Service{
				Image: svc.Image,
				Env:   svc.Environment,
				Name:  svcName,
			}
		}
	}

	// Build merged environment: pipeline-level env + step-level env (step wins)
	mergedEnv := make(map[string]string)
	for k, v := range drone.Environment {
		mergedEnv[k] = v
	}

	// Convert each step to a job (Drone pipeline steps map to jobs in gci)
	// Drone steps are sequential by default, with depends_on for parallel
	depMap := make(map[string][]string)
	for _, step := range drone.Steps {
		// Step-level env overrides pipeline-level env
		jobEnv := make(map[string]string)
		for k, v := range mergedEnv {
			jobEnv[k] = v
		}
		for k, v := range step.Environment {
			jobEnv[k] = v
		}

		// Resolve ${VAR} templates in image name from merged env
		image := p.resolveEnvVars(step.Image, jobEnv)

		job := &types.Job{
			Name:        step.Name,
			RunsOn:      image,
			Image:       image,
			Environment: jobEnv,
			Steps:       p.convertStep(step),
			When:        p.parseWhen(step.When),
			Services:    services,
		}

		// Map depends_on to needs
		if len(step.DependsOn) > 0 {
			job.Needs = step.DependsOn
		}

		// Generate step-level trigger conditions
		if step.When != nil {
			triggers := p.extractTriggerEvents(step.When)
			if len(triggers) > 0 {
				job.If = strings.Join(triggers, " && ")
			}
		}

		pipeline.Jobs[step.Name] = job
		depMap[step.Name] = step.DependsOn
	}

	// If no depends_on, set default sequential ordering
	for i, step := range drone.Steps {
		job := pipeline.Jobs[step.Name]
		if job != nil && len(job.Needs) == 0 && i > 0 {
			// Default: depends on previous step
			if prevName := drone.Steps[i-1].Name; prevName != "" {
				if _, exists := pipeline.Jobs[prevName]; exists {
					job.Needs = append(job.Needs, prevName)
				}
			}
		}
	}

	return pipeline
}

// convertStep converts a Drone step to generic Steps
func (p *DroneParser) convertStep(droneStep DroneStep) []types.Step {
	if len(droneStep.Commands) > 0 {
		return []types.Step{
			{
				Name:   droneStep.Name,
				Run:    strings.Join(droneStep.Commands, "\n"),
				Script: droneStep.Commands,
			},
		}
	}

	// Plugin step (using settings instead of commands)
	if len(droneStep.Settings) > 0 {
		with := make(map[string]string)
		for k, v := range droneStep.Settings {
			with[k] = fmt.Sprintf("%v", v)
		}

		return []types.Step{
			{
				Name: droneStep.Name,
				Uses: fmt.Sprintf("plugins/%s", droneStep.Image),
				With: with,
			},
		}
	}

	return []types.Step{
		{
			Name: droneStep.Name,
			Run:  fmt.Sprintf("echo \"Running %s\"", droneStep.Name),
		},
	}
}

// parseWhen converts Drone trigger conditions to a when string
func (p *DroneParser) parseWhen(trigger *DroneTrigger) string {
	if trigger == nil {
		return ""
	}

	var conditions []string

	if trigger.Branch != nil && len(*trigger.Branch) > 0 {
		conditions = append(conditions, fmt.Sprintf("branch in [%s]", strings.Join(*trigger.Branch, ", ")))
	}

	if trigger.Event != nil && len(*trigger.Event) > 0 {
		conditions = append(conditions, fmt.Sprintf("event in [%s]", strings.Join(*trigger.Event, ", ")))
	}

	return strings.Join(conditions, " && ")
}

// extractTriggerEvents extracts event conditions from trigger
func (p *DroneParser) extractTriggerEvents(trigger *DroneTrigger) []string {
	var events []string
	if trigger == nil {
		return events
	}

	if trigger.Event != nil {
		for _, e := range *trigger.Event {
			events = append(events, fmt.Sprintf("github.event_name == '%s'", e))
		}
	}

	return events
}

// resolveEnvVars resolves ${VAR} template variables in a string using the env map.
func (p *DroneParser) resolveEnvVars(s string, env map[string]string) string {
	result := s
	for k, v := range env {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
	}
	return result
}

// Validate validates the parsed pipeline
func (p *DroneParser) Validate(pipeline *types.Pipeline) error {
	if pipeline == nil {
		return fmt.Errorf("pipeline is nil")
	}

	var errors []string
	if len(pipeline.Jobs) == 0 {
		errors = append(errors, "no steps defined in pipeline")
	}

	jobIDs := make(map[string]bool)
	for jobID := range pipeline.Jobs {
		jobIDs[jobID] = true
	}

	for jobID, job := range pipeline.Jobs {
		if len(job.Steps) == 0 {
			errors = append(errors, fmt.Sprintf("step '%s' has no commands or settings", jobID))
		}

		for _, need := range job.Needs {
			if !jobIDs[need] {
				errors = append(errors, fmt.Sprintf("step '%s' depends on non-existent step '%s'", jobID, need))
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("validation errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// GetProviderName returns the name of this parser
func (p *DroneParser) GetProviderName() string {
	return "drone"
}

// ParseDirectory parses all Drone CI files in a directory
func (p *DroneParser) ParseDirectory(dir string) ([]*types.Pipeline, error) {
	var pipelines []*types.Pipeline

	// Check for .drone.yml
	mainFile := filepath.Join(dir, ".drone.yml")
	if _, err := os.Stat(mainFile); err == nil {
		pipeline, err := p.Parse(mainFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", mainFile, err)
		}
		pipelines = append(pipelines, pipeline)
	}

	// Check for .drone.yaml
	altFile := filepath.Join(dir, ".drone.yaml")
	if _, err := os.Stat(altFile); err == nil {
		pipeline, err := p.Parse(altFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", altFile, err)
		}
		pipelines = append(pipelines, pipeline)
	}

	if len(pipelines) == 0 {
		return nil, fmt.Errorf("no Drone CI files found in %s", dir)
	}

	return pipelines, nil
}
