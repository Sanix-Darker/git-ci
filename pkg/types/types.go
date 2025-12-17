package types

import (
	"encoding/json"
	"time"
)

// Parser interface for different CI providers
type Parser interface {
	Parse(filePath string) (*Pipeline, error)
	ParseDirectory(dir string) ([]*Pipeline, error)
	Validate(pipeline *Pipeline) error
	GetProviderName() string
}

// Runner interface for different execution backends
type Runner interface {
	RunJob(job *Job, workdir string) error
	RunStep(step *Step, env map[string]string, workdir string) error
	Cleanup() error
	GetRunnerType() RunnerType
}

// Pipeline represents a CI/CD pipeline (universal across all providers)
type Pipeline struct {
	// Core fields (supported by all)
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Jobs        map[string]*Job   `yaml:"jobs" json:"jobs"`
	Environment map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// Provider-specific mapping
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"` // github, gitlab, jenkins, circleci

	// GitHub Actions: on, GitLab: only/except, Jenkins: triggers
	Triggers []string `yaml:"triggers,omitempty" json:"triggers,omitempty"`

	// GitLab specific
	Stages []string `yaml:"stages,omitempty" json:"stages,omitempty"`

	// Advanced features
	Variables   map[string]*Variable `yaml:"variables,omitempty" json:"variables,omitempty"`
	Defaults    *Defaults            `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Concurrency *Concurrency         `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`

	// Workflow control
	Rules []Rule         `yaml:"rules,omitempty" json:"rules,omitempty"`
	When  *WhenCondition `yaml:"when,omitempty" json:"when,omitempty"`

	// Metadata
	Version  string            `yaml:"version,omitempty" json:"version,omitempty"`
	Metadata map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// Job represents a single job in the pipeline (universal)
type Job struct {
	// Core fields (supported by all)
	Name        string            `yaml:"name" json:"name"`
	Steps       []Step            `yaml:"steps" json:"steps"`
	Environment map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// Runner specification
	// GitHub: runs-on, GitLab: tags/image, Jenkins: agent, CircleCI: executor
	RunsOn   string   `yaml:"runs-on,omitempty" json:"runs-on,omitempty"`
	Tags     []string `yaml:"tags,omitempty" json:"tags,omitempty"`         // GitLab
	Image    string   `yaml:"image,omitempty" json:"image,omitempty"`       // GitLab/CircleCI
	Agent    *Agent   `yaml:"agent,omitempty" json:"agent,omitempty"`       // Jenkins
	Executor string   `yaml:"executor,omitempty" json:"executor,omitempty"` // CircleCI

	// Container/Docker support (GitHub/GitLab/CircleCI)
	Container *Container          `yaml:"container,omitempty" json:"container,omitempty"`
	Services  map[string]*Service `yaml:"services,omitempty" json:"services,omitempty"`

	// Dependencies and ordering
	Needs        []string `yaml:"needs,omitempty" json:"needs,omitempty"`               // GitHub/GitLab
	Dependencies []string `yaml:"dependencies,omitempty" json:"dependencies,omitempty"` // GitLab
	Stage        string   `yaml:"stage,omitempty" json:"stage,omitempty"`               // GitLab
	Requires     []string `yaml:"requires,omitempty" json:"requires,omitempty"`         // CircleCI

	// Conditionals
	If     string      `yaml:"if,omitempty" json:"if,omitempty"`         // GitHub
	Only   *OnlyExcept `yaml:"only,omitempty" json:"only,omitempty"`     // GitLab
	Except *OnlyExcept `yaml:"except,omitempty" json:"except,omitempty"` // GitLab
	Rules  []Rule      `yaml:"rules,omitempty" json:"rules,omitempty"`   // GitLab
	When   string      `yaml:"when,omitempty" json:"when,omitempty"`     // GitLab/CircleCI

	// Execution control
	TimeoutMin    int          `yaml:"timeout-minutes,omitempty" json:"timeout-minutes,omitempty"`
	Timeout       string       `yaml:"timeout,omitempty" json:"timeout,omitempty"` // GitLab format
	ContinueOnErr bool         `yaml:"continue-on-error,omitempty" json:"continue-on-error,omitempty"`
	AllowFailure  bool         `yaml:"allow_failure,omitempty" json:"allow_failure,omitempty"` // GitLab
	Retry         *RetryPolicy `yaml:"retry,omitempty" json:"retry,omitempty"`
	MaxRetries    int          `yaml:"max_retries,omitempty" json:"max_retries,omitempty"` // Jenkins

	// Parallelism and strategy
	Strategy *Strategy                `yaml:"strategy,omitempty" json:"strategy,omitempty"` // GitHub
	Parallel *Parallel                `yaml:"parallel,omitempty" json:"parallel,omitempty"` // GitLab
	Matrix   map[string][]interface{} `yaml:"matrix,omitempty" json:"matrix,omitempty"`     // Jenkins/CircleCI

	// Scripts (GitLab style)
	Script       []string `yaml:"script,omitempty" json:"script,omitempty"`
	BeforeScript []string `yaml:"before_script,omitempty" json:"before_script,omitempty"`
	AfterScript  []string `yaml:"after_script,omitempty" json:"after_script,omitempty"`

	// Artifacts and caching
	Artifacts *ArtifactConfig `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Cache     *CacheConfig    `yaml:"cache,omitempty" json:"cache,omitempty"`

	// Advanced features
	Secrets       map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Outputs       map[string]string `yaml:"outputs,omitempty" json:"outputs,omitempty"`
	ResourceClass string            `yaml:"resource_class,omitempty" json:"resource_class,omitempty"` // CircleCI

	// Workflow integration
	WorkflowCall *WorkflowCall  `yaml:"workflow_call,omitempty" json:"workflow_call,omitempty"` // Reusable workflows
	Trigger      *TriggerConfig `yaml:"trigger,omitempty" json:"trigger,omitempty"`             // GitLab downstream

	// Environment and deployment
	EnvironmentName string `yaml:"environment,omitempty" json:"environment,omitempty"`
	DeploymentTier  string `yaml:"deployment_tier,omitempty" json:"deployment_tier,omitempty"`
}

// Step represents a single step in a job (universal)
type Step struct {
	// Core fields
	ID   string `yaml:"id,omitempty" json:"id,omitempty"`
	Name string `yaml:"name" json:"name"`

	// Execution (one of these)
	Run     string   `yaml:"run,omitempty" json:"run,omitempty"`         // Shell command
	Uses    string   `yaml:"uses,omitempty" json:"uses,omitempty"`       // Action/Orb
	Script  []string `yaml:"script,omitempty" json:"script,omitempty"`   // GitLab style
	Command string   `yaml:"command,omitempty" json:"command,omitempty"` // CircleCI
	Task    string   `yaml:"task,omitempty" json:"task,omitempty"`       // Ansible/Other

	// Parameters and configuration
	With       map[string]string `yaml:"with,omitempty" json:"with,omitempty"`             // GitHub
	Parameters map[string]string `yaml:"parameters,omitempty" json:"parameters,omitempty"` // CircleCI
	Inputs     map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`         // Generic
	Arguments  []string          `yaml:"args,omitempty" json:"args,omitempty"`             // Docker/Shell

	// Environment
	Env       map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Variables map[string]string `yaml:"variables,omitempty" json:"variables,omitempty"` // GitLab

	// Conditionals and control
	If            string `yaml:"if,omitempty" json:"if,omitempty"`
	When          string `yaml:"when,omitempty" json:"when,omitempty"` // GitLab/CircleCI
	ContinueOnErr bool   `yaml:"continue-on-error,omitempty" json:"continue-on-error,omitempty"`
	AllowFailure  bool   `yaml:"allow_failure,omitempty" json:"allow_failure,omitempty"`

	// Execution context
	Shell      string `yaml:"shell,omitempty" json:"shell,omitempty"`
	WorkingDir string `yaml:"working-directory,omitempty" json:"working-directory,omitempty"`
	User       string `yaml:"user,omitempty" json:"user,omitempty"`

	// Timeouts and retries
	TimeoutMin  int          `yaml:"timeout-minutes,omitempty" json:"timeout-minutes,omitempty"`
	Timeout     string       `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	RetryPolicy *RetryPolicy `yaml:"retry,omitempty" json:"retry,omitempty"`

	// Caching and artifacts
	Cache     *CacheConfig    `yaml:"cache,omitempty" json:"cache,omitempty"`
	Artifacts *ArtifactConfig `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`

	// Step type hints (for parser/runner routing)
	Type StepType `yaml:"type,omitempty" json:"type,omitempty"`

	// Background and services
	Background bool `yaml:"background,omitempty" json:"background,omitempty"`
	Detach     bool `yaml:"detach,omitempty" json:"detach,omitempty"`
}

// Container configuration (GitHub/GitLab/CircleCI compatible)
type Container struct {
	Image       string            `yaml:"image" json:"image"`
	Name        string            `yaml:"name,omitempty" json:"name,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Ports       []string          `yaml:"ports,omitempty" json:"ports,omitempty"`
	Options     string            `yaml:"options,omitempty" json:"options,omitempty"`
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	Network     string            `yaml:"network,omitempty" json:"network,omitempty"`
	NetworkMode string            `yaml:"network_mode,omitempty" json:"network_mode,omitempty"`
	Credentials map[string]string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	Auth        *ContainerAuth    `yaml:"auth,omitempty" json:"auth,omitempty"`
	HealthCheck *HealthCheck      `yaml:"health-check,omitempty" json:"health-check,omitempty"`
	User        string            `yaml:"user,omitempty" json:"user,omitempty"`
	Privileged  bool              `yaml:"privileged,omitempty" json:"privileged,omitempty"`
	CapAdd      []string          `yaml:"cap_add,omitempty" json:"cap_add,omitempty"`
	CapDrop     []string          `yaml:"cap_drop,omitempty" json:"cap_drop,omitempty"`
	SecurityOpt []string          `yaml:"security_opt,omitempty" json:"security_opt,omitempty"`
	// Resource limits
	CPUs   string `yaml:"cpus,omitempty" json:"cpus,omitempty"`     // CPU limit (e.g., "2" or "0.5")
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"` // Memory limit (e.g., "2g" or "512m")
}

// Service container definition (GitHub/GitLab/docker-compose compatible)
type Service struct {
	Image       string            `yaml:"image" json:"image"`
	Name        string            `yaml:"name,omitempty" json:"name,omitempty"`
	Alias       string            `yaml:"alias,omitempty" json:"alias,omitempty"` // GitLab
	Command     []string          `yaml:"command,omitempty" json:"command,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Ports       []string          `yaml:"ports,omitempty" json:"ports,omitempty"`
	Volumes     []string          `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Options     string            `yaml:"options,omitempty" json:"options,omitempty"`
	HealthCheck *HealthCheck      `yaml:"health-check,omitempty" json:"health-check,omitempty"`
	Networks    []string          `yaml:"networks,omitempty" json:"networks,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
}

// Strategy for matrix builds (GitHub style, but universal)
type Strategy struct {
	Matrix      map[string][]interface{} `yaml:"matrix,omitempty" json:"matrix,omitempty"`
	Include     []map[string]interface{} `yaml:"include,omitempty" json:"include,omitempty"`
	Exclude     []map[string]interface{} `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	FailFast    bool                     `yaml:"fail-fast,omitempty" json:"fail-fast,omitempty"`
	MaxParallel int                      `yaml:"max-parallel,omitempty" json:"max-parallel,omitempty"`
}

// Parallel configuration (GitLab style)
type Parallel struct {
	Total  int                      `yaml:"total,omitempty" json:"total,omitempty"`
	Matrix []map[string]interface{} `yaml:"matrix,omitempty" json:"matrix,omitempty"`
}

// Agent configuration (Jenkins style)
type Agent struct {
	Label      string           `yaml:"label,omitempty" json:"label,omitempty"`
	Docker     *Container       `yaml:"docker,omitempty" json:"docker,omitempty"`
	Kubernetes *KubernetesAgent `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`
	Any        bool             `yaml:"any,omitempty" json:"any,omitempty"`
	None       bool             `yaml:"none,omitempty" json:"none,omitempty"`
}

// KubernetesAgent for Jenkins/Tekton style
type KubernetesAgent struct {
	Label      string      `yaml:"label,omitempty" json:"label,omitempty"`
	Namespace  string      `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	Cloud      string      `yaml:"cloud,omitempty" json:"cloud,omitempty"`
	Containers []Container `yaml:"containers,omitempty" json:"containers,omitempty"`
}

// Variable with different value types
type Variable struct {
	Value       interface{} `yaml:"value,omitempty" json:"value,omitempty"`
	Description string      `yaml:"description,omitempty" json:"description,omitempty"`
	Type        string      `yaml:"type,omitempty" json:"type,omitempty"`
	Default     interface{} `yaml:"default,omitempty" json:"default,omitempty"`
	Required    bool        `yaml:"required,omitempty" json:"required,omitempty"`
	Options     []string    `yaml:"options,omitempty" json:"options,omitempty"`
	Pattern     string      `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Secret      bool        `yaml:"secret,omitempty" json:"secret,omitempty"`
	Expand      bool        `yaml:"expand,omitempty" json:"expand,omitempty"`
}

// Rule for conditional execution (GitLab style, but universal)
type Rule struct {
	If           string            `yaml:"if,omitempty" json:"if,omitempty"`
	When         string            `yaml:"when,omitempty" json:"when,omitempty"`
	Changes      []string          `yaml:"changes,omitempty" json:"changes,omitempty"`
	Exists       []string          `yaml:"exists,omitempty" json:"exists,omitempty"`
	Variables    map[string]string `yaml:"variables,omitempty" json:"variables,omitempty"`
	AllowFailure bool              `yaml:"allow_failure,omitempty" json:"allow_failure,omitempty"`
}

// OnlyExcept for GitLab style conditions
type OnlyExcept struct {
	Refs       []string `yaml:"refs,omitempty" json:"refs,omitempty"`
	Changes    []string `yaml:"changes,omitempty" json:"changes,omitempty"`
	Kubernetes string   `yaml:"kubernetes,omitempty" json:"kubernetes,omitempty"`
	Variables  []string `yaml:"variables,omitempty" json:"variables,omitempty"`
}

// WhenCondition for execution control
type WhenCondition struct {
	OnSuccess bool           `yaml:"on_success,omitempty" json:"on_success,omitempty"`
	OnFailure bool           `yaml:"on_failure,omitempty" json:"on_failure,omitempty"`
	Always    bool           `yaml:"always,omitempty" json:"always,omitempty"`
	Never     bool           `yaml:"never,omitempty" json:"never,omitempty"`
	Manual    bool           `yaml:"manual,omitempty" json:"manual,omitempty"`
	Delayed   *time.Duration `yaml:"delayed,omitempty" json:"delayed,omitempty"`
}

// RetryPolicy for resilient execution
type RetryPolicy struct {
	MaxAttempts int      `yaml:"max,omitempty" json:"max,omitempty"`
	When        []string `yaml:"when,omitempty" json:"when,omitempty"` // GitLab style
	Delay       string   `yaml:"delay,omitempty" json:"delay,omitempty"`
	Backoff     string   `yaml:"backoff,omitempty" json:"backoff,omitempty"`
	ExitCodes   []int    `yaml:"exit_codes,omitempty" json:"exit_codes,omitempty"`
}

// CacheConfig for build caching (universal)
type CacheConfig struct {
	Key       string   `yaml:"key,omitempty" json:"key,omitempty"`
	Paths     []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	Policy    string   `yaml:"policy,omitempty" json:"policy,omitempty"`       // pull/push/pull-push
	Untracked bool     `yaml:"untracked,omitempty" json:"untracked,omitempty"` // GitLab
	When      string   `yaml:"when,omitempty" json:"when,omitempty"`
	Fallback  []string `yaml:"fallback_keys,omitempty" json:"fallback_keys,omitempty"`
}

// ArtifactConfig for artifact handling (universal)
type ArtifactConfig struct {
	Name      string            `yaml:"name,omitempty" json:"name,omitempty"`
	Paths     []string          `yaml:"paths" json:"paths"`
	When      string            `yaml:"when,omitempty" json:"when,omitempty"`
	ExpireIn  string            `yaml:"expire_in,omitempty" json:"expire_in,omitempty"`
	Reports   map[string]string `yaml:"reports,omitempty" json:"reports,omitempty"` // GitLab
	Format    string            `yaml:"format,omitempty" json:"format,omitempty"`
	Untracked bool              `yaml:"untracked,omitempty" json:"untracked,omitempty"`
	Public    bool              `yaml:"public,omitempty" json:"public,omitempty"` // GitLab
	Exclude   []string          `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

// Defaults for job/step configuration
type Defaults struct {
	Run           *RunDefaults    `yaml:"run,omitempty" json:"run,omitempty"`
	Timeout       string          `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retry         *RetryPolicy    `yaml:"retry,omitempty" json:"retry,omitempty"`
	Artifacts     *ArtifactConfig `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Cache         *CacheConfig    `yaml:"cache,omitempty" json:"cache,omitempty"`
	BeforeScript  []string        `yaml:"before_script,omitempty" json:"before_script,omitempty"`
	AfterScript   []string        `yaml:"after_script,omitempty" json:"after_script,omitempty"`
	Image         string          `yaml:"image,omitempty" json:"image,omitempty"`
	Services      []string        `yaml:"services,omitempty" json:"services,omitempty"`
	Tags          []string        `yaml:"tags,omitempty" json:"tags,omitempty"`
	Interruptible bool            `yaml:"interruptible,omitempty" json:"interruptible,omitempty"`
}

// RunDefaults for shell execution
type RunDefaults struct {
	Shell            string `yaml:"shell,omitempty" json:"shell,omitempty"`
	WorkingDirectory string `yaml:"working-directory,omitempty" json:"working-directory,omitempty"`
}

// Concurrency control
type Concurrency struct {
	Group            string `yaml:"group" json:"group"`
	CancelInProgress bool   `yaml:"cancel-in-progress,omitempty" json:"cancel-in-progress,omitempty"`
	Limit            int    `yaml:"limit,omitempty" json:"limit,omitempty"`
}

// WorkflowCall for reusable workflows
type WorkflowCall struct {
	Uses    string                 `yaml:"uses" json:"uses"`
	With    map[string]interface{} `yaml:"with,omitempty" json:"with,omitempty"`
	Secrets map[string]string      `yaml:"secrets,omitempty" json:"secrets,omitempty"`
}

// TriggerConfig for downstream pipelines (GitLab)
type TriggerConfig struct {
	Project  string            `yaml:"project,omitempty" json:"project,omitempty"`
	Branch   string            `yaml:"branch,omitempty" json:"branch,omitempty"`
	Strategy string            `yaml:"strategy,omitempty" json:"strategy,omitempty"`
	Forward  map[string]string `yaml:"forward,omitempty" json:"forward,omitempty"`
}

// HealthCheck configuration
type HealthCheck struct {
	Test        []string      `yaml:"test,omitempty" json:"test,omitempty"`
	Interval    time.Duration `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout     time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries     int           `yaml:"retries,omitempty" json:"retries,omitempty"`
	StartPeriod time.Duration `yaml:"start_period,omitempty" json:"start_period,omitempty"`
	Disable     bool          `yaml:"disable,omitempty" json:"disable,omitempty"`
}

// ContainerAuth for registry authentication
type ContainerAuth struct {
	Username      string `yaml:"username,omitempty" json:"username,omitempty"`
	Password      string `yaml:"password,omitempty" json:"password,omitempty"`
	Email         string `yaml:"email,omitempty" json:"email,omitempty"`
	ServerAddress string `yaml:"server_address,omitempty" json:"server_address,omitempty"`
	IdentityToken string `yaml:"identity_token,omitempty" json:"identity_token,omitempty"`
	RegistryToken string `yaml:"registry_token,omitempty" json:"registry_token,omitempty"`
}

// StepType for routing execution
type StepType string

const (
	StepTypeCommand   StepType = "command"
	StepTypeAction    StepType = "action"
	StepTypeScript    StepType = "script"
	StepTypeContainer StepType = "container"
	StepTypeOrb       StepType = "orb"      // CircleCI
	StepTypeTask      StepType = "task"     // Ansible/Tekton
	StepTypeTemplate  StepType = "template" // Argo
)

// RunnerType represents the type of runner
type RunnerType string

const (
	RunnerTypeBash       RunnerType = "bash"
	RunnerTypeDocker     RunnerType = "docker"
	RunnerTypePodman     RunnerType = "podman"
	RunnerTypeKubernetes RunnerType = "kubernetes"
	RunnerTypeSSH        RunnerType = "ssh"
	RunnerTypeWinRM      RunnerType = "winrm"
	RunnerTypeVagrant    RunnerType = "vagrant"
)

// PipelineStatus for execution tracking
type PipelineStatus string

const (
	StatusPending   PipelineStatus = "pending"
	StatusQueued    PipelineStatus = "queued"
	StatusRunning   PipelineStatus = "running"
	StatusSuccess   PipelineStatus = "success"
	StatusFailed    PipelineStatus = "failed"
	StatusCancelled PipelineStatus = "cancelled"
	StatusSkipped   PipelineStatus = "skipped"
	StatusManual    PipelineStatus = "manual"
	StatusScheduled PipelineStatus = "scheduled"
)

// ExecutionResult for tracking results
type ExecutionResult struct {
	Success   bool               `json:"success"`
	Status    PipelineStatus     `json:"status"`
	ExitCode  int                `json:"exit_code"`
	Output    string             `json:"output,omitempty"`
	Error     string             `json:"error,omitempty"`
	Duration  time.Duration      `json:"duration"`
	StartTime time.Time          `json:"start_time"`
	EndTime   time.Time          `json:"end_time"`
	Artifacts []string           `json:"artifacts,omitempty"`
	Logs      []LogEntry         `json:"logs,omitempty"`
	Metrics   map[string]float64 `json:"metrics,omitempty"`
	Metadata  map[string]string  `json:"metadata,omitempty"`
}

// LogEntry for structured logging
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// JobStatus for tracking job execution
type JobStatus struct {
	Name      string         `json:"name"`
	Status    PipelineStatus `json:"status"`
	StartTime *time.Time     `json:"start_time,omitempty"`
	EndTime   *time.Time     `json:"end_time,omitempty"`
	Duration  *time.Duration `json:"duration,omitempty"`
	ExitCode  int            `json:"exit_code,omitempty"`
	Message   string         `json:"message,omitempty"`
	Steps     []StepStatus   `json:"steps,omitempty"`
	Attempts  int            `json:"attempts,omitempty"`
}

// StepStatus for tracking step execution
type StepStatus struct {
	Name      string         `json:"name"`
	Status    PipelineStatus `json:"status"`
	StartTime *time.Time     `json:"start_time,omitempty"`
	EndTime   *time.Time     `json:"end_time,omitempty"`
	Duration  *time.Duration `json:"duration,omitempty"`
	ExitCode  int            `json:"exit_code,omitempty"`
	Output    string         `json:"output,omitempty"`
	Error     string         `json:"error,omitempty"`
	Skipped   bool           `json:"skipped,omitempty"`
	Retries   int            `json:"retries,omitempty"`
}

// PipelineRun represents a complete pipeline execution
type PipelineRun struct {
	ID          string                `json:"id"`
	PipelineID  string                `json:"pipeline_id"`
	Status      PipelineStatus        `json:"status"`
	Trigger     string                `json:"trigger"`
	Branch      string                `json:"branch,omitempty"`
	Commit      string                `json:"commit,omitempty"`
	Author      string                `json:"author,omitempty"`
	StartTime   time.Time             `json:"start_time"`
	EndTime     *time.Time            `json:"end_time,omitempty"`
	Duration    *time.Duration        `json:"duration,omitempty"`
	Jobs        map[string]*JobStatus `json:"jobs"`
	Environment map[string]string     `json:"environment,omitempty"`
	Metadata    map[string]string     `json:"metadata,omitempty"`
}

// Notification for pipeline events
type Notification struct {
	Type       string            `json:"type"` // email, slack, webhook, teams
	When       []string          `json:"when"` // success, failure, always, change
	Recipients []string          `json:"recipients,omitempty"`
	Template   string            `json:"template,omitempty"`
	Config     map[string]string `json:"config,omitempty"`
	Enabled    bool              `json:"enabled"`
}

// Secret for secure values
type Secret struct {
	Name        string        `json:"name"`
	Value       string        `json:"value,omitempty"`
	ValueFrom   *SecretSource `json:"value_from,omitempty"`
	Type        string        `json:"type,omitempty"`
	Required    bool          `json:"required,omitempty"`
	Description string        `json:"description,omitempty"`
}

// SecretSource for external secret stores
type SecretSource struct {
	Provider string            `json:"provider"` // vault, aws-secrets, azure-keyvault
	Path     string            `json:"path,omitempty"`
	Key      string            `json:"key,omitempty"`
	Version  string            `json:"version,omitempty"`
	Config   map[string]string `json:"config,omitempty"`
}

// Environment for deployment targets
type Environment struct {
	Name           string            `json:"name"`
	URL            string            `json:"url,omitempty"`
	Production     bool              `json:"production,omitempty"`
	Variables      map[string]string `json:"variables,omitempty"`
	Secrets        []string          `json:"secrets,omitempty"`
	OnStop         string            `json:"on_stop,omitempty"`
	AutoStopAt     *time.Time        `json:"auto_stop_at,omitempty"`
	ReviewApps     bool              `json:"review_apps,omitempty"`
	DeploymentTier string            `json:"deployment_tier,omitempty"`
}

// Compatibility check functions

// IsGitHubCompatible checks if the pipeline can run on GitHub Actions
func (p *Pipeline) IsGitHubCompatible() bool {
	// Check for GitHub-specific features
	for _, job := range p.Jobs {
		if job.RunsOn == "" && job.Container == nil {
			return false
		}
	}
	return true
}

// IsGitLabCompatible checks if the pipeline can run on GitLab CI
func (p *Pipeline) IsGitLabCompatible() bool {
	// GitLab requires either stages or basic job definitions
	return len(p.Jobs) > 0
}

// IsJenkinsCompatible checks if the pipeline can run on Jenkins
func (p *Pipeline) IsJenkinsCompatible() bool {
	// Jenkins requires agent definitions
	for _, job := range p.Jobs {
		if job.Agent == nil && job.RunsOn == "" {
			return false
		}
	}
	return true
}

// MarshalJSON implements custom JSON marshaling
func (p *Pipeline) MarshalJSON() ([]byte, error) {
	// Custom marshaling to handle provider-specific fields
	type Alias Pipeline
	return json.Marshal(&struct {
		*Alias
		Version string `json:"version"`
	}{
		Alias:   (*Alias)(p),
		Version: "1.0",
	})
}
