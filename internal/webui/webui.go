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

	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
)

//go:embed templates/*.html assets/*
var embedded embed.FS

type PageData struct {
	Page               string
	Title              string
	Kicker             string
	Description        string
	Actor              string
	CSRFToken          string
	Version            string
	Error              string
	Notice             string
	Projects           []store.Project
	Candidates         []projects.Project
	Workflows          []WorkflowView
	Runs               []RunView
	Jobs               []JobView
	SelectedRun        *RunDetailView
	Secrets            []SecretView
	Schedules          []ScheduleView
	Deployments        []DeploymentView
	Environments       []EnvironmentView
	EnvironmentSecrets []EnvironmentSecretView
	Approvals          []ApprovalView
	ActiveDeployments  bool
	Webhooks           []WebhookView
	RunFilter          RunFilterView
	Telemetry          RunTelemetryView
}

type WorkflowView struct {
	ID, ProjectID, ProjectName, Name, Key, Provider, File string
	Revision, JobCount                                    int
}

type RunView struct {
	ID, ProjectName, WorkflowName, WorkflowKey, Status, Dot, Ref, CommitSHA, CreatedAt string
	CanCancel                                                                          bool
	CreatedUnix, DurationSeconds                                                       int64
	DurationLabel                                                                      string
}

type JobView struct {
	ID, RunID, ProjectName, WorkflowName, Key, Name, Status, Dot string
	StepCount                                                    int
}

type RunDetailView struct {
	Run       RunView
	Jobs      []RunJobView
	GraphRows []RunGraphRowView
	Lineage   *RunLineageView
	Terminal  bool
}

type RunLineageView struct {
	Kind, SourceRunID, SourceJobID, SourceStepID, Actor, CreatedAt string
}

type RunJobView struct {
	ID, Key, Name, Status, Dot, Runner, Dependencies string
	AllowFailure                                     bool
	DependencyKeys                                   []string
	Steps                                            []RunStepView
	Replay                                           ReplayControlView
}

type RunStepView struct {
	ID, RunID, Name, Status, Dot, Command string
	Terminal                              bool
	Replay                                ReplayControlView
}

type ReplayControlView struct {
	Action, CSRFToken, IdempotencyKey, SourceRunID, SourceJobID string
	Label, Hint, Mode, Consequence, CommitSHA                   string
	Enabled, RequiresConfirmation                               bool
}

type LogView struct {
	Sequence int
	Stream   string
	Message  string
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

type StepLogView struct {
	RunID, StepID, StepName string
	Terminal                bool
	Logs                    []LogView
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
