// Package webui renders the git-ci operator interface as server-owned HTML.
package webui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/compatibility"
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
)

//go:embed templates/*.html assets/*
var embedded embed.FS

type PageData struct {
	Page                string
	Title               string
	Kicker              string
	Description         string
	Actor               string
	CSRFToken           string
	Version             string
	Error               string
	Notice              string
	Projects            []store.Project
	ProjectViews        []ProjectView
	SelectedProject     *ProjectView
	Candidates          []projects.Project
	Workflows           []WorkflowView
	Runs                []RunView
	Jobs                []JobView
	SelectedRun         *RunDetailView
	Secrets             []SecretView
	Schedules           []ScheduleView
	Deployments         []DeploymentView
	Releases            []ReleaseView
	ReleaseCandidates   []ReleaseRunView
	SelectedRelease     *ReleaseDetailView
	ReleaseFilter       ReleaseFilterView
	Environments        []EnvironmentView
	EnvironmentSecrets  []EnvironmentSecretView
	Approvals           []ApprovalView
	ActiveDeployments   bool
	Webhooks            []WebhookView
	Runners             []RunnerView
	RunFilter           RunFilterView
	Telemetry           RunTelemetryView
	Compatibility       compatibility.Report
	CompatibilityFilter compatibility.Filter
	Audit               AuditView
	AuditFilter         AuditFilterView
}

type ProjectView struct {
	ID, Name, Slug, CanonicalPath, Health, HealthDetail, Dot, CSRFToken string
	WorkflowCount                                                       int
	Workflows                                                           []WorkflowView
	CommitTrigger                                                       CommitTriggerView
	Workspace                                                           bool
}

type CommitTriggerView struct {
	Ref, Status, Dot, LastCommitSHA, LastCheckedAt, LastTriggeredAt, LastError string
	Enabled                                                                    bool
}

type WorkflowView struct {
	ID, ProjectID, ProjectName, Name, Key, Provider, File string
	Revision, JobCount, EdgeCount                         int
	DefaultRef                                            string
	Triggers                                              []string
	TriggerPolicies                                       []WorkflowTriggerPolicyView
	ManualInputs                                          []WorkflowInputView
	Stages                                                []string
	Jobs                                                  []WorkflowJobView
	GraphRows                                             []RunGraphRowView
	Badges                                                []SemanticBadgeView
	RunnerReady                                           bool
	RunnerBlocked                                         int
}

type SemanticBadgeView struct {
	Label, Tone, Hint string
}

type WorkflowTriggerPolicyView struct {
	Event, Condition                                               string
	Branches, BranchesIgnore, Tags, TagsIgnore, Paths, PathsIgnore []string
	Actions, Schedules                                             []string
	Evaluable                                                      bool
}

type WorkflowInputView struct {
	Name, Description, Type, Default string
	Required                         bool
	Options                          []string
}

type WorkflowJobView struct {
	Key, SourceKey, Name, Stage, Runner, Dependencies, OptionalDependencies string
	AllowFailure                                                            bool
	Steps                                                                   []WorkflowStepView
	Badges                                                                  []SemanticBadgeView
}

type RunnerView struct {
	ID, Name, Status, Dot, Mode, OS, Architecture, Group string
	Labels, Tags                                         []string
	DockerAvailable, RunUntagged                         bool
	MaxParallel                                          int
}

type WorkflowStepView struct {
	Name, Command, Action string
	Badges                []SemanticBadgeView
}

type RunView struct {
	ID, ProjectID, ProjectName, WorkflowName, WorkflowKey, Status, Dot, Trigger, Ref, CommitSHA, CreatedAt string
	CanCancel                                                                                              bool
	CreatedUnix, DurationSeconds                                                                           int64
	DurationLabel                                                                                          string
}

type JobView struct {
	ID, RunID, ProjectName, WorkflowName, Key, Name, Status, Dot string
	StepCount                                                    int
}

type RunDetailView struct {
	Run         RunView
	Jobs        []RunJobView
	GraphRows   []RunGraphRowView
	Lineage     *RunLineageView
	Terminal    bool
	EdgeCount   int
	Artifacts   []ArtifactView
	TestReports []TestReportView
	Upstream    *ChildPipelineView
}

type ArtifactView struct {
	ID, Name, SHA256, Size, Download string
	FileCount                        int
}

type TestReportView struct {
	Name, Duration                   string
	Tests, Failures, Errors, Skipped int
}

type RunLineageView struct {
	Kind, SourceRunID, SourceJobID, SourceStepID, Actor, CreatedAt string
}

type RunJobView struct {
	ID, Key, SourceKey, Name, Status, Dot, Runner, Dependencies, OptionalDependencies string
	ManualPlayedBy, ManualPlayedAt                                                    string
	AllowFailure, AllowedFailure                                                      bool
	AllowFailureExitCodes                                                             []int
	DependencyKeys                                                                    []string
	Steps                                                                             []RunStepView
	Replay                                                                            ReplayControlView
	Manual                                                                            ManualPlayControlView
	Badges                                                                            []SemanticBadgeView
	Attempts                                                                          []JobAttemptView
	ChildPipeline                                                                     *ChildPipelineView
}

type ChildPipelineView struct {
	ParentRunID, ParentJobID, ChildRunID, SourceFile, Strategy, Status, Dot string
	Depth                                                                   int
}

type JobAttemptView struct {
	Number             int
	Status, Tone, Hint string
}

type RunStepView struct {
	ID, RunID, Name, Status, Dot, Command, Summary string
	Terminal                                       bool
	Replay                                         ReplayControlView
	Badges                                         []SemanticBadgeView
	Annotations                                    []RunStepAnnotationView
}

type RunStepAnnotationView struct {
	Level, Dot, Title, Message, Location string
}

type ReplayControlView struct {
	Action, CSRFToken, IdempotencyKey, SourceRunID, SourceJobID string
	Label, Hint, Mode, Consequence, CommitSHA                   string
	Enabled, RequiresConfirmation                               bool
}

type ManualPlayControlView struct {
	Action, CSRFToken, IdempotencyKey, SourceRunID   string
	Label, Hint, Mode, Confirmation                  string
	Present, Enabled, Blocking, RequiresConfirmation bool
}

type LogView struct {
	Sequence int
	Stream   string
	Message  string
}

type LogEntryView struct {
	Line  *LogView
	Group *LogGroupView
}

type LogGroupView struct {
	ID, Provider, Name, Dot string
	LineCount               int
	Open                    bool
	Entries                 []LogEntryView
}

type RunGraphRowView struct {
	Level int
	Jobs  []RunJobView
}

type RunFilterView struct {
	Range, Status, Project string
}

type HistogramBarView struct {
	Label string
	Count int
	Level int
}

type RunTelemetryView struct {
	Window, PassRate                 string
	Total, Succeeded, Failed, Active int
	Volume, Duration                 []HistogramBarView
}

type AuditFilterView struct {
	Range, Query, Project, Actor, Action, ResourceType string
}

type AuditView struct {
	Window, ActorsLabel string
	Total, Count        int
	Items               []AuditEventView
	Buckets             []HistogramBarView
	Actors              []string
	Actions             []string
	ResourceTypes       []string
}

type AuditEventView struct {
	ID, ProjectID, Action, Actor, ResourceType, ResourceID, Metadata, CreatedAt string
}

type StepLogView struct {
	RunID, StepID, StepName string
	Terminal                bool
	Logs                    []LogView
	Entries                 []LogEntryView
}

type SecretView struct{ ID, ProjectName, Name, UpdatedAt string }
type ScheduleView struct {
	ID, ProjectName, WorkflowName, Cron, Ref, Timezone, NextRunAt string
	Enabled                                                       bool
}
type DeploymentView struct {
	ID, RunID, JobID, JobName, ProjectName, Environment, DeploymentTier, Status, Dot, UpdatedAt string
	CSRFToken, RollbackKey, RollbackHint                                                        string
	Terminal, CanRollback                                                                       bool
	RollbackTargets                                                                             []RollbackTargetView
}
type ReleaseFilterView struct{ Project, State, Query string }
type ReleaseView struct {
	ID, ProjectID, ProjectName, RunID, TagName, TargetCommitSHA, Name, Notes string
	State, Dot, CreatedBy, CreatedAt, PublishedAt                            string
	Prerelease                                                               bool
}
type ReleaseRunView struct {
	ID, ProjectID, ProjectName, WorkflowName, Ref, CommitSHA, CreatedAt string
}
type ReleaseArtifactView struct {
	ID, Name, SHA256, Size, Download string
	FileCount                        int
}
type ReleaseDeploymentView struct{ ID, RunID, Environment, Tier, Status, UpdatedAt string }
type ReleaseDetailView struct {
	Release     ReleaseView
	SourceRun   ReleaseRunView
	Artifacts   []ReleaseArtifactView
	Deployments []ReleaseDeploymentView
}
type RollbackTargetView struct{ ID, Ref, CommitSHA, CreatedAt string }
type EnvironmentView struct {
	ID, ProjectID, ProjectName, Name, DeploymentTier, AllowedRefs, ConcurrencyMode, UpdatedAt string
	Protected                                                                                 bool
	RequiredApprovals, WaitTimerSeconds, SecretCount                                          int
}
type EnvironmentSecretView struct{ ID, ProjectName, EnvironmentName, Name, UpdatedAt string }
type ApprovalView struct {
	ID, RunID, JobID, ProjectName, EnvironmentName, DeploymentTier, JobName, Ref, CommitSHA, RequestedAt, CSRFToken string
}
type WebhookView struct {
	ID, ProjectName, Name, Provider, URL string
	Enabled                              bool
}

type Renderer struct {
	templates *template.Template
	assets    http.Handler
}

func New() (*Renderer, error) {
	functions := template.FuncMap{
		"base":  filepath.Base,
		"pad2":  func(value int) string { return fmt.Sprintf("%02d", value) },
		"upper": strings.ToUpper,
		"itoa":  strconv.Itoa,
		"short": func(value string) string {
			if len(value) <= 10 {
				return value
			}
			return value[:10]
		},
	}
	parsed, err := template.New("git-ci").Funcs(functions).ParseFS(embedded, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("webui: parse templates: %w", err)
	}
	assetFS, err := fs.Sub(embedded, "assets")
	if err != nil {
		return nil, fmt.Errorf("webui: asset filesystem: %w", err)
	}
	return &Renderer{
		templates: parsed,
		assets:    immutableAssets(http.FileServer(http.FS(assetFS))),
	}, nil
}

func (r *Renderer) Assets() http.Handler {
	return r.assets
}

func (r *Renderer) RenderLogin(writer http.ResponseWriter, status int, data PageData) {
	r.render(writer, status, "login", data)
}

func (r *Renderer) RenderLoginFeedback(writer http.ResponseWriter, status int, message string) {
	r.render(writer, status, "login_feedback", PageData{Error: message})
}

func (r *Renderer) RenderApp(writer http.ResponseWriter, status int, data PageData, fragment bool) {
	name := "app"
	if fragment {
		name = "app_frame"
	}
	r.render(writer, status, name, data)
}

func (r *Renderer) RenderRunPanel(writer http.ResponseWriter, status int, data PageData) {
	r.render(writer, status, "run_detail_panel", data)
}

func (r *Renderer) RenderStepLogs(writer http.ResponseWriter, status int, data StepLogView) {
	r.render(writer, status, "step_logs", data)
}

func (r *Renderer) render(writer http.ResponseWriter, status int, name string, data any) {
	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(writer, "template rendering failed", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(output.Bytes())
}

func immutableAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(writer, request)
	})
}
