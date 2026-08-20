package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	workflowColumns = `
		id,
		project_id,
		workflow_key,
		name,
		definition_json,
		environment_json,
		revision,
		active,
		created_at,
		updated_at`

	runColumns = `
		id,
		project_id,
		workflow_id,
		workflow_key,
		workflow_revision,
		trigger_type,
		status,
		ref,
		commit_sha,
		environment_json,
		cancellation_requested,
		cancellation_requested_at,
		worker_id,
		claimed_at,
		failure_reason,
		source_path,
		started_at,
		finished_at,
		created_at,
		updated_at`

	jobColumns = `
		id,
		run_id,
		job_key,
		name,
		status,
		runner,
		position,
		environment_json,
		dependency_keys_json,
		allow_failure,
		timeout_minutes,
		started_at,
		finished_at,
		created_at,
		updated_at`

	stepColumns = `
		id,
		job_id,
		step_key,
		step_index,
		name,
		command,
		status,
		environment_json,
		action,
		working_directory,
		timeout_minutes,
		shell,
		allow_failure,
		started_at,
		finished_at,
		created_at,
		updated_at`

	logLineColumns = `
		id,
		run_id,
		job_id,
		step_id,
		sequence,
		stream,
		message,
		created_at`
)

// Status is the lifecycle state shared by a run, job, and step.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusSkipped   Status = "skipped"
)

// LogStream identifies the source of a durable log line.
type LogStream string

const (
	LogStreamStdout LogStream = "stdout"
	LogStreamStderr LogStream = "stderr"
	LogStreamSystem LogStream = "system"
)

// ErrInvalidStatusTransition reports an attempted lifecycle transition that
// cannot be performed from the resource's current state.
type ErrInvalidStatusTransition struct {
	Resource string
	ID       string
	From     Status
	To       Status
}

func (e *ErrInvalidStatusTransition) Error() string {
	if e == nil {
		return "store: invalid status transition"
	}
	return fmt.Sprintf("store: invalid %s status transition for %s: %s -> %s", e.Resource, e.ID, e.From, e.To)
}

// Is makes status-transition errors comparable by category with errors.Is.
func (e *ErrInvalidStatusTransition) Is(target error) bool {
	_, ok := target.(*ErrInvalidStatusTransition)
	return ok
}

// Workflow is a mutable workflow definition. Runs record the workflow's key
// and revision at enqueue time, while their job and step snapshots stay fixed.
type Workflow struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"projectId"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Definition  json.RawMessage `json:"definition"`
	Environment json.RawMessage `json:"environment"`
	Revision    int64           `json:"revision"`
	Active      bool            `json:"active"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// UpsertWorkflowParams contains the complete current definition for a
// workflow. Definition and Environment must be JSON objects.
type UpsertWorkflowParams struct {
	ProjectID   string
	Key         string
	Name        string
	Definition  json.RawMessage
	Environment json.RawMessage
}

// Run is the durable lifecycle record for one immutable workflow execution.
type Run struct {
	ID                      string          `json:"id"`
	ProjectID               string          `json:"projectId"`
	WorkflowID              *string         `json:"workflowId,omitempty"`
	WorkflowKey             *string         `json:"workflowKey,omitempty"`
	WorkflowRevision        *int64          `json:"workflowRevision,omitempty"`
	TriggerType             string          `json:"triggerType"`
	Status                  Status          `json:"status"`
	Ref                     *string         `json:"ref,omitempty"`
	CommitSHA               *string         `json:"commitSha,omitempty"`
	Environment             json.RawMessage `json:"environment"`
	CancellationRequested   bool            `json:"cancellationRequested"`
	CancellationRequestedAt *time.Time      `json:"cancellationRequestedAt,omitempty"`
	WorkerID                *string         `json:"workerId,omitempty"`
	ClaimedAt               *time.Time      `json:"claimedAt,omitempty"`
	FailureReason           *string         `json:"failureReason,omitempty"`
	SourcePath              string          `json:"sourcePath"`
	StartedAt               *time.Time      `json:"startedAt,omitempty"`
	FinishedAt              *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt               time.Time       `json:"createdAt"`
	UpdatedAt               time.Time       `json:"updatedAt"`
}

// EnqueueRunParams contains all immutable execution input. Jobs and steps are
// copied into the run transaction; later workflow upserts cannot alter them.
type EnqueueRunParams struct {
	ProjectID   string
	WorkflowID  string
	TriggerType string
	Ref         string
	CommitSHA   string
	SourcePath  string
	Environment json.RawMessage
	Jobs        []EnqueueJob
}

// EnqueueJob is an immutable job snapshot. DependencyKeys is a JSON array of
// job keys, and Environment is a JSON object. The slice order assigns the
// durable job position.
type EnqueueJob struct {
	Key             string
	Name            string
	Runner          string
	EnvironmentName string
	DeploymentTier  string
	Environment     json.RawMessage
	DependencyKeys  json.RawMessage
	AllowFailure    bool
	TimeoutMinutes  int
	Steps           []EnqueueStep
}

// EnqueueStep is an immutable step snapshot. Environment is a JSON object.
// The slice order assigns the durable step index.
type EnqueueStep struct {
	Key              string
	Name             string
	Command          string
	Action           string
	WorkingDirectory string
	TimeoutMinutes   int
	Shell            string
	AllowFailure     bool
	Environment      json.RawMessage
}

// Job is a durable job snapshot and its mutable lifecycle fields.
type Job struct {
	ID             string          `json:"id"`
	RunID          string          `json:"runId"`
	Key            *string         `json:"key,omitempty"`
	Name           string          `json:"name"`
	Status         Status          `json:"status"`
	Runner         *string         `json:"runner,omitempty"`
	Position       int             `json:"position"`
	Environment    json.RawMessage `json:"environment"`
	DependencyKeys json.RawMessage `json:"dependencyKeys"`
	AllowFailure   bool            `json:"allowFailure"`
	TimeoutMinutes int             `json:"timeoutMinutes"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// Step is a durable step snapshot and its mutable lifecycle fields.
type Step struct {
	ID               string          `json:"id"`
	JobID            string          `json:"jobId"`
	Key              *string         `json:"key,omitempty"`
	Index            int             `json:"index"`
	Name             string          `json:"name"`
	Command          *string         `json:"command,omitempty"`
	Status           Status          `json:"status"`
	Environment      json.RawMessage `json:"environment"`
	Action           *string         `json:"action,omitempty"`
	WorkingDirectory *string         `json:"workingDirectory,omitempty"`
	TimeoutMinutes   int             `json:"timeoutMinutes"`
	Shell            *string         `json:"shell,omitempty"`
	AllowFailure     bool            `json:"allowFailure"`
	StartedAt        *time.Time      `json:"startedAt,omitempty"`
	FinishedAt       *time.Time      `json:"finishedAt,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

// JobGraph is a job and its steps in the order a worker should process them.
type JobGraph struct {
	Job   Job    `json:"job"`
	Steps []Step `json:"steps"`
}

// RunGraph is the durable graph view for one run. Dependency edges are the
// JSON dependency keys attached to each Job.
type RunGraph struct {
	Run  Run        `json:"run"`
	Jobs []JobGraph `json:"jobs"`
}

// LogLine is one immutable, line-oriented worker output record.
type LogLine struct {
	ID        string    `json:"id"`
	RunID     string    `json:"runId"`
	JobID     string    `json:"jobId"`
	StepID    string    `json:"stepId"`
	Sequence  int64     `json:"sequence"`
	Stream    LogStream `json:"stream"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// AppendLogLineParams is the input for one immutable log line. Message must
// represent a single line, but it may be empty.
type AppendLogLineParams struct {
	StepID  string
	Stream  LogStream
	Message string
}

// RunCancellation is the durable cancellation signal read by the worker.
type RunCancellation struct {
	RunID       string     `json:"runId"`
	Requested   bool       `json:"requested"`
	RequestedAt *time.Time `json:"requestedAt,omitempty"`
}

// UpsertWorkflow creates a workflow or replaces its current definition. An
// existing workflow keeps its ID and receives a monotonically increasing
// revision number.
func (s *Store) UpsertWorkflow(ctx context.Context, params UpsertWorkflowParams) (Workflow, error) {
	if err := requireContext(ctx); err != nil {
		return Workflow{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Workflow{}, err
	}
	params, err = normalizeUpsertWorkflowParams(params)
	if err != nil {
		return Workflow{}, err
	}

	exists, err := projectIDExists(ctx, db, params.ProjectID)
	if err != nil {
		return Workflow{}, fmt.Errorf("store: check workflow project: %w", err)
	}
	if !exists {
		return Workflow{}, &ErrNotFound{Resource: "project", Key: params.ProjectID}
	}

	id, err := randomOpaqueID()
	if err != nil {
		return Workflow{}, fmt.Errorf("store: generate workflow ID: %w", err)
	}
	now := nowUTC()
	workflow, err := scanWorkflow(db.QueryRowContext(ctx, `
		INSERT INTO workflows (
			id, project_id, workflow_key, name, definition_json,
			environment_json, revision, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
		ON CONFLICT(project_id, workflow_key) DO UPDATE SET
			name = excluded.name,
			definition_json = excluded.definition_json,
			environment_json = excluded.environment_json,
			revision = workflows.revision + 1,
			active = 1,
			updated_at = excluded.updated_at
		RETURNING `+workflowColumns,
		id,
		params.ProjectID,
		params.Key,
		params.Name,
		string(params.Definition),
		string(params.Environment),
		now.UnixMilli(),
		now.UnixMilli(),
	))
	if err != nil {
		return Workflow{}, fmt.Errorf("store: upsert workflow: %w", err)
	}
	return workflow, nil
}

// ListWorkflows returns all workflows for a project in stable key order.
func (s *Store) ListWorkflows(ctx context.Context, projectID string) ([]Workflow, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("workflow project ID", projectID)
	if err != nil {
		return nil, err
	}
	exists, err := projectIDExists(ctx, db, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: check workflow project: %w", err)
	}
	if !exists {
		return nil, &ErrNotFound{Resource: "project", Key: projectID}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT `+workflowColumns+`
		FROM workflows
		WHERE project_id = ? AND active = 1
		ORDER BY workflow_key ASC, id ASC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list workflows: %w", err)
	}
	defer rows.Close()

	workflows := make([]Workflow, 0)
	for rows.Next() {
		workflow, err := scanWorkflow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan workflow: %w", err)
		}
		workflows = append(workflows, workflow)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate workflows: %w", err)
	}
	return workflows, nil
}

// SetProjectWorkflowSet marks exactly the supplied workflow keys active for a
// project. Historical workflow rows remain addressable by ID for immutable
// run history after their source files are removed.
func (s *Store) SetProjectWorkflowSet(ctx context.Context, projectID string, keys []string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID, err = normalizeRequiredText("workflow project ID", projectID)
	if err != nil {
		return err
	}
	args := make([]any, 0, len(keys)+1)
	args = append(args, projectID)
	query := `UPDATE workflows SET active = 0, updated_at = ? WHERE project_id = ?`
	now := nowUTC().UnixMilli()
	args = append([]any{now}, args...)
	if len(keys) > 0 {
		placeholders := make([]string, 0, len(keys))
		for _, key := range keys {
			normalized, normalizeErr := normalizeRequiredText("workflow key", key)
			if normalizeErr != nil {
				return normalizeErr
			}
			placeholders = append(placeholders, "?")
			args = append(args, normalized)
		}
		query += ` AND workflow_key NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("store: set project workflow set: %w", err)
	}
	return nil
}

// GetWorkflow returns a workflow by its opaque ID.
func (s *Store) GetWorkflow(ctx context.Context, workflowID string) (Workflow, error) {
	if err := requireContext(ctx); err != nil {
		return Workflow{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Workflow{}, err
	}
	workflowID, err = normalizeRequiredText("workflow ID", workflowID)
	if err != nil {
		return Workflow{}, err
	}

	workflow, err := scanWorkflow(db.QueryRowContext(ctx, `
		SELECT `+workflowColumns+`
		FROM workflows
		WHERE id = ?
	`, workflowID))
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, &ErrNotFound{Resource: "workflow", Key: workflowID}
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("store: get workflow: %w", err)
	}
	return workflow, nil
}

// EnqueueRun atomically stores a queued run and immutable snapshots of every
// job, step, environment, and dependency edge the worker will need.
func (s *Store) EnqueueRun(ctx context.Context, params EnqueueRunParams) (Run, error) {
	if err := requireContext(ctx); err != nil {
		return Run{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Run{}, err
	}
	params, err = normalizeEnqueueRunParams(params)
	if err != nil {
		return Run{}, err
	}

	exists, err := projectIDExists(ctx, db, params.ProjectID)
	if err != nil {
		return Run{}, fmt.Errorf("store: check run project: %w", err)
	}
	if !exists {
		return Run{}, &ErrNotFound{Resource: "project", Key: params.ProjectID}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("store: begin enqueue run: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, `
		SELECT `+workflowColumns+`
		FROM workflows
		WHERE id = ? AND project_id = ?
	`, params.WorkflowID, params.ProjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, &ErrNotFound{Resource: "workflow", Key: params.WorkflowID}
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: get run workflow: %w", err)
	}
	if len(params.Environment) == 0 {
		params.Environment = cloneJSON(workflow.Environment)
	}

	runID, err := randomOpaqueID()
	if err != nil {
		return Run{}, fmt.Errorf("store: generate run ID: %w", err)
	}
	now := nowUTC()
	run := Run{
		ID:               runID,
		ProjectID:        params.ProjectID,
		WorkflowID:       stringPointer(workflow.ID),
		WorkflowKey:      stringPointer(workflow.Key),
		WorkflowRevision: int64Pointer(workflow.Revision),
		TriggerType:      params.TriggerType,
		Status:           StatusQueued,
		Ref:              optionalTextPointer(params.Ref),
		CommitSHA:        optionalTextPointer(params.CommitSHA),
		Environment:      cloneJSON(params.Environment),
		SourcePath:       params.SourcePath,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runs (
			id, project_id, workflow_id, workflow_key, workflow_revision,
			trigger_type, status, ref, commit_sha, environment_json,
			source_path, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		run.ID,
		run.ProjectID,
		workflow.ID,
		workflow.Key,
		workflow.Revision,
		run.TriggerType,
		run.Status,
		nullableString(run.Ref),
		nullableString(run.CommitSHA),
		string(run.Environment),
		run.SourcePath,
		run.CreatedAt.UnixMilli(),
		run.UpdatedAt.UnixMilli(),
	); err != nil {
		return Run{}, fmt.Errorf("store: insert run: %w", err)
	}

	for jobPosition, job := range params.Jobs {
		jobID, err := randomOpaqueID()
		if err != nil {
			return Run{}, fmt.Errorf("store: generate job ID: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO jobs (
				id, run_id, job_key, name, status, runner, position,
			environment_json, dependency_keys_json, allow_failure,
			timeout_minutes, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			jobID,
			run.ID,
			job.Key,
			job.Name,
			StatusQueued,
			nullableText(job.Runner),
			jobPosition,
			string(job.Environment),
			string(job.DependencyKeys),
			job.AllowFailure,
			job.TimeoutMinutes,
			now.UnixMilli(),
			now.UnixMilli(),
		); err != nil {
			return Run{}, fmt.Errorf("store: insert job snapshot: %w", err)
		}
		if err := insertDeploymentTarget(ctx, tx, runID, jobID, job, now); err != nil {
			return Run{}, err
		}

		for stepIndex, step := range job.Steps {
			stepID, err := randomOpaqueID()
			if err != nil {
				return Run{}, fmt.Errorf("store: generate step ID: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO steps (
					id, job_id, step_key, step_index, name, command, status,
					environment_json, action, working_directory, timeout_minutes,
					shell, allow_failure, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`,
				stepID,
				jobID,
				step.Key,
				stepIndex,
				step.Name,
				nullableText(step.Command),
				StatusQueued,
				string(step.Environment),
				nullableText(step.Action),
				nullableText(step.WorkingDirectory),
				step.TimeoutMinutes,
				nullableText(step.Shell),
				step.AllowFailure,
				now.UnixMilli(),
				now.UnixMilli(),
			); err != nil {
				return Run{}, fmt.Errorf("store: insert step snapshot: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("store: commit enqueue run: %w", err)
	}
	return run, nil
}

// ListRuns returns every run for a project, newest first.
func (s *Store) ListRuns(ctx context.Context, projectID string) ([]Run, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("run project ID", projectID)
	if err != nil {
		return nil, err
	}
	exists, err := projectIDExists(ctx, db, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: check run project: %w", err)
	}
	if !exists {
		return nil, &ErrNotFound{Resource: "project", Key: projectID}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT `+runColumns+`
		FROM runs
		WHERE project_id = ?
		ORDER BY created_at DESC, id DESC
	`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()

	runs := make([]Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan run: %w", err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate runs: %w", err)
	}
	return runs, nil
}

// GetRunGraph returns the run together with jobs and ordered steps. The job
// dependency graph is represented by each Job's immutable DependencyKeys.
func (s *Store) GetRunGraph(ctx context.Context, runID string) (RunGraph, error) {
	if err := requireContext(ctx); err != nil {
		return RunGraph{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return RunGraph{}, err
	}
	runID, err = normalizeRequiredText("run ID", runID)
	if err != nil {
		return RunGraph{}, err
	}

	run, err := scanRun(db.QueryRowContext(ctx, `
		SELECT `+runColumns+`
		FROM runs
		WHERE id = ?
	`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return RunGraph{}, &ErrNotFound{Resource: "run", Key: runID}
	}
	if err != nil {
		return RunGraph{}, fmt.Errorf("store: get run: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT `+jobColumns+`
		FROM jobs
		WHERE run_id = ?
		ORDER BY position ASC, id ASC
	`, runID)
	if err != nil {
		return RunGraph{}, fmt.Errorf("store: list run jobs: %w", err)
	}

	graph := RunGraph{Run: run, Jobs: make([]JobGraph, 0)}
	jobIndex := make(map[string]int)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			_ = rows.Close()
			return RunGraph{}, fmt.Errorf("store: scan run job: %w", err)
		}
		jobIndex[job.ID] = len(graph.Jobs)
		graph.Jobs = append(graph.Jobs, JobGraph{Job: job, Steps: make([]Step, 0)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return RunGraph{}, fmt.Errorf("store: iterate run jobs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return RunGraph{}, fmt.Errorf("store: close run jobs: %w", err)
	}

	stepRows, err := db.QueryContext(ctx, `
		SELECT `+stepColumns+`
		FROM steps
		WHERE job_id IN (SELECT id FROM jobs WHERE run_id = ?)
		ORDER BY job_id ASC, step_index ASC, id ASC
	`, runID)
	if err != nil {
		return RunGraph{}, fmt.Errorf("store: list run steps: %w", err)
	}
	defer stepRows.Close()

	for stepRows.Next() {
		step, err := scanStep(stepRows)
		if err != nil {
			return RunGraph{}, fmt.Errorf("store: scan run step: %w", err)
		}
		index, ok := jobIndex[step.JobID]
		if !ok {
			return RunGraph{}, fmt.Errorf("store: step %q belongs to a job outside run %q", step.ID, runID)
		}
		graph.Jobs[index].Steps = append(graph.Jobs[index].Steps, step)
	}
	if err := stepRows.Err(); err != nil {
		return RunGraph{}, fmt.Errorf("store: iterate run steps: %w", err)
	}
	return graph, nil
}

// ClaimNextQueuedRun atomically claims the oldest non-cancelled queued run.
// A nil run and nil error mean that no work is currently available.
func (s *Store) ClaimNextQueuedRun(ctx context.Context, workerID string) (*Run, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	workerID, err = normalizeRequiredText("worker ID", workerID)
	if err != nil {
		return nil, err
	}
	now := nowUTC()

	run, err := scanRun(db.QueryRowContext(ctx, `
		UPDATE runs
		SET
			status = ?,
			worker_id = ?,
			claimed_at = ?,
			started_at = COALESCE(started_at, ?),
			updated_at = ?
		WHERE id = (
			SELECT id
			FROM runs
			WHERE status = ? AND cancellation_requested = 0
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		) AND status = ? AND cancellation_requested = 0
		RETURNING `+runColumns,
		StatusRunning,
		workerID,
		now.UnixMilli(),
		now.UnixMilli(),
		now.UnixMilli(),
		StatusQueued,
		StatusQueued,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim queued run: %w", err)
	}
	return &run, nil
}

// TransitionRun changes one run status only when the lifecycle transition is
// valid. It maintains started, finished, and updated timestamps atomically.
func (s *Store) TransitionRun(ctx context.Context, runID string, next Status) (Run, error) {
	if err := requireContext(ctx); err != nil {
		return Run{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Run{}, err
	}
	runID, err = normalizeRequiredText("run ID", runID)
	if err != nil {
		return Run{}, err
	}
	if err := validateStatus(next); err != nil {
		return Run{}, err
	}

	current, err := scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, &ErrNotFound{Resource: "run", Key: runID}
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: get run for transition: %w", err)
	}
	if !canTransition(current.Status, next) {
		return Run{}, &ErrInvalidStatusTransition{Resource: "run", ID: runID, From: current.Status, To: next}
	}

	now := nowUTC()
	startedAt := current.StartedAt
	if next == StatusRunning && startedAt == nil {
		startedAt = &now
	}
	finishedAt := current.FinishedAt
	if isTerminalStatus(next) {
		finishedAt = &now
	}
	updated, err := scanRun(db.QueryRowContext(ctx, `
		UPDATE runs
		SET status = ?, started_at = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?
		RETURNING `+runColumns,
		next,
		nullableTime(startedAt),
		nullableTime(finishedAt),
		now.UnixMilli(),
		runID,
		current.Status,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, &ErrConflict{Resource: "run", Field: "status", Value: runID}
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: transition run: %w", err)
	}
	return updated, nil
}

// TransitionJob changes one job status only when the lifecycle transition is
// valid. It maintains started, finished, and updated timestamps atomically.
func (s *Store) TransitionJob(ctx context.Context, jobID string, next Status) (Job, error) {
	if err := requireContext(ctx); err != nil {
		return Job{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Job{}, err
	}
	jobID, err = normalizeRequiredText("job ID", jobID)
	if err != nil {
		return Job{}, err
	}
	if err := validateStatus(next); err != nil {
		return Job{}, err
	}

	current, err := scanJob(db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, &ErrNotFound{Resource: "job", Key: jobID}
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: get job for transition: %w", err)
	}
	if !canTransition(current.Status, next) {
		return Job{}, &ErrInvalidStatusTransition{Resource: "job", ID: jobID, From: current.Status, To: next}
	}

	now := nowUTC()
	startedAt := current.StartedAt
	if next == StatusRunning && startedAt == nil {
		startedAt = &now
	}
	finishedAt := current.FinishedAt
	if isTerminalStatus(next) {
		finishedAt = &now
	}
	updated, err := scanJob(db.QueryRowContext(ctx, `
		UPDATE jobs
		SET status = ?, started_at = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?
		RETURNING `+jobColumns,
		next,
		nullableTime(startedAt),
		nullableTime(finishedAt),
		now.UnixMilli(),
		jobID,
		current.Status,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, &ErrConflict{Resource: "job", Field: "status", Value: jobID}
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: transition job: %w", err)
	}
	return updated, nil
}

// TransitionStep changes one step status only when the lifecycle transition is
// valid. It maintains started, finished, and updated timestamps atomically.
func (s *Store) TransitionStep(ctx context.Context, stepID string, next Status) (Step, error) {
	if err := requireContext(ctx); err != nil {
		return Step{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Step{}, err
	}
	stepID, err = normalizeRequiredText("step ID", stepID)
	if err != nil {
		return Step{}, err
	}
	if err := validateStatus(next); err != nil {
		return Step{}, err
	}

	current, err := scanStep(db.QueryRowContext(ctx, `SELECT `+stepColumns+` FROM steps WHERE id = ?`, stepID))
	if errors.Is(err, sql.ErrNoRows) {
		return Step{}, &ErrNotFound{Resource: "step", Key: stepID}
	}
	if err != nil {
		return Step{}, fmt.Errorf("store: get step for transition: %w", err)
	}
	if !canTransition(current.Status, next) {
		return Step{}, &ErrInvalidStatusTransition{Resource: "step", ID: stepID, From: current.Status, To: next}
	}

	now := nowUTC()
	startedAt := current.StartedAt
	if next == StatusRunning && startedAt == nil {
		startedAt = &now
	}
	finishedAt := current.FinishedAt
	if isTerminalStatus(next) {
		finishedAt = &now
	}
	updated, err := scanStep(db.QueryRowContext(ctx, `
		UPDATE steps
		SET status = ?, started_at = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?
		RETURNING `+stepColumns,
		next,
		nullableTime(startedAt),
		nullableTime(finishedAt),
		now.UnixMilli(),
		stepID,
		current.Status,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Step{}, &ErrConflict{Resource: "step", Field: "status", Value: stepID}
	}
	if err != nil {
		return Step{}, fmt.Errorf("store: transition step: %w", err)
	}
	return updated, nil
}

// AppendLogLine appends one line with an atomically allocated per-run sequence
// number. It verifies that the requested step belongs to a durable run first.
func (s *Store) AppendLogLine(ctx context.Context, params AppendLogLineParams) (LogLine, error) {
	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		line, err := s.appendLogLine(ctx, params)
		if err == nil || !isSQLiteBusy(err) {
			return line, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(attempt+1) * 2 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return LogLine{}, ctx.Err()
		case <-timer.C:
		}
	}
	return LogLine{}, lastErr
}

func (s *Store) appendLogLine(ctx context.Context, params AppendLogLineParams) (LogLine, error) {
	if err := requireContext(ctx); err != nil {
		return LogLine{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return LogLine{}, err
	}
	params, err = normalizeAppendLogLineParams(params)
	if err != nil {
		return LogLine{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return LogLine{}, fmt.Errorf("store: begin append log line: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var runID, jobID string
	err = tx.QueryRowContext(ctx, `
		SELECT jobs.run_id, steps.job_id
		FROM steps
		JOIN jobs ON jobs.id = steps.job_id
		WHERE steps.id = ?
	`, params.StepID).Scan(&runID, &jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return LogLine{}, &ErrNotFound{Resource: "step", Key: params.StepID}
	}
	if err != nil {
		return LogLine{}, fmt.Errorf("store: resolve log step: %w", err)
	}

	var sequence int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO run_log_counters (run_id, next_sequence)
		VALUES (?, 2)
		ON CONFLICT(run_id) DO UPDATE SET next_sequence = run_log_counters.next_sequence + 1
		RETURNING next_sequence - 1
	`, runID).Scan(&sequence)
	if err != nil {
		return LogLine{}, fmt.Errorf("store: allocate log sequence: %w", err)
	}

	id, err := randomOpaqueID()
	if err != nil {
		return LogLine{}, fmt.Errorf("store: generate log line ID: %w", err)
	}
	now := nowUTC()
	line := LogLine{
		ID:        id,
		RunID:     runID,
		JobID:     jobID,
		StepID:    params.StepID,
		Sequence:  sequence,
		Stream:    params.Stream,
		Message:   params.Message,
		CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_log_lines (
			id, run_id, job_id, step_id, sequence, stream, message, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		line.ID,
		line.RunID,
		line.JobID,
		line.StepID,
		line.Sequence,
		line.Stream,
		line.Message,
		line.CreatedAt.UnixMilli(),
	); err != nil {
		return LogLine{}, fmt.Errorf("store: insert log line: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return LogLine{}, fmt.Errorf("store: commit log line: %w", err)
	}
	return line, nil
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy")
}

// ListLogLines returns every line for a step in its durable append order.
func (s *Store) ListLogLines(ctx context.Context, stepID string) ([]LogLine, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	stepID, err = normalizeRequiredText("log step ID", stepID)
	if err != nil {
		return nil, err
	}

	var exists int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM steps WHERE id = ?`, stepID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ErrNotFound{Resource: "step", Key: stepID}
	}
	if err != nil {
		return nil, fmt.Errorf("store: check log step: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT `+logLineColumns+`
		FROM run_log_lines
		WHERE step_id = ?
		ORDER BY sequence ASC, id ASC
	`, stepID)
	if err != nil {
		return nil, fmt.Errorf("store: list log lines: %w", err)
	}
	defer rows.Close()

	lines := make([]LogLine, 0)
	for rows.Next() {
		line, err := scanLogLine(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan log line: %w", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate log lines: %w", err)
	}
	return lines, nil
}

// RequestRunCancellation records a durable cancellation signal. Queued runs
// become cancelled immediately; running workers read the signal and perform
// their own orderly cancellation.
func (s *Store) RequestRunCancellation(ctx context.Context, runID string) (RunCancellation, error) {
	if err := requireContext(ctx); err != nil {
		return RunCancellation{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return RunCancellation{}, err
	}
	runID, err = normalizeRequiredText("run ID", runID)
	if err != nil {
		return RunCancellation{}, err
	}
	now := nowUTC()

	run, err := scanRun(db.QueryRowContext(ctx, `
		UPDATE runs
		SET
			cancellation_requested = 1,
			cancellation_requested_at = ?,
			status = CASE WHEN status = ? THEN ? ELSE status END,
			finished_at = CASE WHEN status = ? THEN ? ELSE finished_at END,
			updated_at = ?
		WHERE id = ?
			AND status IN (?, ?)
			AND cancellation_requested = 0
		RETURNING `+runColumns,
		now.UnixMilli(),
		StatusQueued,
		StatusCancelled,
		StatusQueued,
		now.UnixMilli(),
		now.UnixMilli(),
		runID,
		StatusQueued,
		StatusRunning,
	))
	if err == nil {
		return cancellationFromRun(run), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RunCancellation{}, fmt.Errorf("store: request run cancellation: %w", err)
	}
	return s.GetRunCancellation(ctx, runID)
}

// GetRunCancellation reads the durable cancellation signal for a run.
func (s *Store) GetRunCancellation(ctx context.Context, runID string) (RunCancellation, error) {
	if err := requireContext(ctx); err != nil {
		return RunCancellation{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return RunCancellation{}, err
	}
	runID, err = normalizeRequiredText("run ID", runID)
	if err != nil {
		return RunCancellation{}, err
	}

	run, err := scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return RunCancellation{}, &ErrNotFound{Resource: "run", Key: runID}
	}
	if err != nil {
		return RunCancellation{}, fmt.Errorf("store: get run cancellation: %w", err)
	}
	return cancellationFromRun(run), nil
}

// MarkInterruptedRunningRunsFailed finalizes work left running by an abrupt
// service stop. Running jobs and steps fail; queued descendants are skipped.
// It returns the number of interrupted runs finalized by this startup action.
func (s *Store) MarkInterruptedRunningRunsFailed(ctx context.Context) (int, error) {
	if err := requireContext(ctx); err != nil {
		return 0, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin interrupt recovery: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps
		SET
			status = CASE WHEN status = ? THEN ? ELSE ? END,
			finished_at = ?,
			updated_at = ?
		WHERE status IN (?, ?)
			AND job_id IN (
				SELECT jobs.id
				FROM jobs
				JOIN runs ON runs.id = jobs.run_id
				WHERE runs.status = ?
			)
	`,
		StatusRunning,
		StatusFailed,
		StatusSkipped,
		now.UnixMilli(),
		now.UnixMilli(),
		StatusQueued,
		StatusRunning,
		StatusRunning,
	); err != nil {
		return 0, fmt.Errorf("store: recover interrupted steps: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs
		SET
			status = CASE WHEN status = ? THEN ? ELSE ? END,
			finished_at = ?,
			updated_at = ?
		WHERE status IN (?, ?)
			AND run_id IN (SELECT id FROM runs WHERE status = ?)
	`,
		StatusRunning,
		StatusFailed,
		StatusSkipped,
		now.UnixMilli(),
		now.UnixMilli(),
		StatusQueued,
		StatusRunning,
		StatusRunning,
	); err != nil {
		return 0, fmt.Errorf("store: recover interrupted jobs: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE runs
		SET status = ?, finished_at = ?, failure_reason = ?, updated_at = ?
		WHERE status = ?
	`,
		StatusFailed,
		now.UnixMilli(),
		"service interrupted before run completed",
		now.UnixMilli(),
		StatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("store: recover interrupted runs: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: count interrupted runs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit interrupt recovery: %w", err)
	}
	return int(count), nil
}

func normalizeUpsertWorkflowParams(params UpsertWorkflowParams) (UpsertWorkflowParams, error) {
	var err error
	if params.ProjectID, err = normalizeRequiredText("workflow project ID", params.ProjectID); err != nil {
		return UpsertWorkflowParams{}, err
	}
	if params.Key, err = normalizeRequiredText("workflow key", params.Key); err != nil {
		return UpsertWorkflowParams{}, err
	}
	if params.Name, err = normalizeRequiredText("workflow name", params.Name); err != nil {
		return UpsertWorkflowParams{}, err
	}
	if params.Definition, err = normalizeJSONObject("workflow definition", params.Definition, true); err != nil {
		return UpsertWorkflowParams{}, err
	}
	if params.Environment, err = normalizeJSONObject("workflow environment", params.Environment, false); err != nil {
		return UpsertWorkflowParams{}, err
	}
	return params, nil
}

func normalizeEnqueueRunParams(params EnqueueRunParams) (EnqueueRunParams, error) {
	var err error
	if params.ProjectID, err = normalizeRequiredText("run project ID", params.ProjectID); err != nil {
		return EnqueueRunParams{}, err
	}
	if params.WorkflowID, err = normalizeRequiredText("run workflow ID", params.WorkflowID); err != nil {
		return EnqueueRunParams{}, err
	}
	if params.TriggerType, err = normalizeRequiredText("run trigger type", params.TriggerType); err != nil {
		return EnqueueRunParams{}, err
	}
	if params.Ref, err = normalizeOptionalText("run ref", params.Ref); err != nil {
		return EnqueueRunParams{}, err
	}
	if params.CommitSHA, err = normalizeOptionalText("run commit SHA", params.CommitSHA); err != nil {
		return EnqueueRunParams{}, err
	}
	if params.SourcePath, err = normalizeRequiredText("run source path", params.SourcePath); err != nil {
		return EnqueueRunParams{}, err
	}
	if len(bytes.TrimSpace(params.Environment)) > 0 {
		if params.Environment, err = normalizeJSONObject("run environment", params.Environment, false); err != nil {
			return EnqueueRunParams{}, err
		}
	} else {
		params.Environment = nil
	}
	if len(params.Jobs) == 0 {
		return EnqueueRunParams{}, invalidInput("run jobs", "must contain at least one job")
	}

	jobKeys := make(map[string]struct{}, len(params.Jobs))
	for jobIndex := range params.Jobs {
		job := &params.Jobs[jobIndex]
		if job.Key, err = normalizeRequiredText("run job key", job.Key); err != nil {
			return EnqueueRunParams{}, err
		}
		if _, exists := jobKeys[job.Key]; exists {
			return EnqueueRunParams{}, invalidInput("run job key", "must be unique")
		}
		jobKeys[job.Key] = struct{}{}
		if job.Name, err = normalizeRequiredText("run job name", job.Name); err != nil {
			return EnqueueRunParams{}, err
		}
		if job.Runner, err = normalizeOptionalText("run job runner", job.Runner); err != nil {
			return EnqueueRunParams{}, err
		}
		if err := normalizeEnqueueJobDeployment(job); err != nil {
			return EnqueueRunParams{}, err
		}
		if job.Environment, err = normalizeJSONObject("run job environment", job.Environment, false); err != nil {
			return EnqueueRunParams{}, err
		}
		if job.DependencyKeys, err = normalizeDependencyKeys(job.DependencyKeys); err != nil {
			return EnqueueRunParams{}, err
		}
		if job.TimeoutMinutes < 0 {
			return EnqueueRunParams{}, invalidInput("run job timeout", "must not be negative")
		}
		if len(job.Steps) == 0 {
			return EnqueueRunParams{}, invalidInput("run job steps", "must contain at least one step")
		}

		stepKeys := make(map[string]struct{}, len(job.Steps))
		for stepIndex := range job.Steps {
			step := &job.Steps[stepIndex]
			if step.Key, err = normalizeRequiredText("run step key", step.Key); err != nil {
				return EnqueueRunParams{}, err
			}
			if _, exists := stepKeys[step.Key]; exists {
				return EnqueueRunParams{}, invalidInput("run step key", "must be unique within a job")
			}
			stepKeys[step.Key] = struct{}{}
			if step.Name, err = normalizeRequiredText("run step name", step.Name); err != nil {
				return EnqueueRunParams{}, err
			}
			if step.Command, err = normalizeOptionalText("run step command", step.Command); err != nil {
				return EnqueueRunParams{}, err
			}
			if step.Action, err = normalizeOptionalText("run step action", step.Action); err != nil {
				return EnqueueRunParams{}, err
			}
			if step.Command == "" && step.Action == "" {
				return EnqueueRunParams{}, invalidInput("run step", "must contain a command or action")
			}
			if step.WorkingDirectory, err = normalizeOptionalText("run step working directory", step.WorkingDirectory); err != nil {
				return EnqueueRunParams{}, err
			}
			if step.Shell, err = normalizeOptionalText("run step shell", step.Shell); err != nil {
				return EnqueueRunParams{}, err
			}
			if step.TimeoutMinutes < 0 {
				return EnqueueRunParams{}, invalidInput("run step timeout", "must not be negative")
			}
			if step.Environment, err = normalizeJSONObject("run step environment", step.Environment, false); err != nil {
				return EnqueueRunParams{}, err
			}
		}
	}

	for _, job := range params.Jobs {
		var dependencies []string
		if err := json.Unmarshal(job.DependencyKeys, &dependencies); err != nil {
			return EnqueueRunParams{}, fmt.Errorf("store: decode normalized job dependencies: %w", err)
		}
		for _, dependency := range dependencies {
			if dependency == job.Key {
				return EnqueueRunParams{}, invalidInput("run job dependencies", "must not depend on itself")
			}
			if _, exists := jobKeys[dependency]; !exists {
				return EnqueueRunParams{}, invalidInput("run job dependencies", "must reference an enqueued job key")
			}
		}
	}
	if err := validateAcyclicDependencies(params.Jobs); err != nil {
		return EnqueueRunParams{}, err
	}
	return params, nil
}

func normalizeAppendLogLineParams(params AppendLogLineParams) (AppendLogLineParams, error) {
	var err error
	if params.StepID, err = normalizeRequiredText("log step ID", params.StepID); err != nil {
		return AppendLogLineParams{}, err
	}
	if !validLogStream(params.Stream) {
		return AppendLogLineParams{}, invalidInput("log stream", "must be stdout, stderr, or system")
	}
	if strings.IndexByte(params.Message, 0) >= 0 {
		return AppendLogLineParams{}, invalidInput("log message", "must not contain a NUL byte")
	}
	if strings.ContainsAny(params.Message, "\r\n") {
		return AppendLogLineParams{}, invalidInput("log message", "must contain one line")
	}
	return params, nil
}

func normalizeJSONObject(field string, value json.RawMessage, required bool) (json.RawMessage, error) {
	normalized := bytes.TrimSpace(value)
	if len(normalized) == 0 {
		if required {
			return nil, invalidInput(field, "must be a JSON object")
		}
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(normalized) {
		return nil, invalidInput(field, "must be valid JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &object); err != nil || object == nil {
		return nil, invalidInput(field, "must be a JSON object")
	}
	return cloneJSON(normalized), nil
}

func normalizeDependencyKeys(value json.RawMessage) (json.RawMessage, error) {
	normalized := bytes.TrimSpace(value)
	if len(normalized) == 0 {
		normalized = []byte("[]")
	}
	if !json.Valid(normalized) || normalized[0] != '[' {
		return nil, invalidInput("run job dependencies", "must be a JSON array of job keys")
	}
	var keys []string
	if err := json.Unmarshal(normalized, &keys); err != nil {
		return nil, invalidInput("run job dependencies", "must be a JSON array of job keys")
	}
	seen := make(map[string]struct{}, len(keys))
	for index, key := range keys {
		var err error
		key, err = normalizeRequiredText("run job dependency key", key)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			return nil, invalidInput("run job dependencies", "must not contain duplicate keys")
		}
		seen[key] = struct{}{}
		keys[index] = key
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("store: encode job dependencies: %w", err)
	}
	return json.RawMessage(encoded), nil
}

func validateAcyclicDependencies(jobs []EnqueueJob) error {
	dependencies := make(map[string][]string, len(jobs))
	for _, job := range jobs {
		var keys []string
		if err := json.Unmarshal(job.DependencyKeys, &keys); err != nil {
			return fmt.Errorf("store: decode normalized job dependencies: %w", err)
		}
		dependencies[job.Key] = keys
	}

	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int, len(dependencies))
	var visit func(string) error
	visit = func(key string) error {
		switch states[key] {
		case visiting:
			return invalidInput("run job dependencies", "must not contain a cycle")
		case visited:
			return nil
		}
		states[key] = visiting
		for _, dependency := range dependencies[key] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		states[key] = visited
		return nil
	}
	for key := range dependencies {
		if states[key] == unvisited {
			if err := visit(key); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateStatus(status Status) error {
	if !validStatus(status) {
		return invalidInput("status", "must be queued, running, succeeded, failed, cancelled, or skipped")
	}
	return nil
}

func validStatus(status Status) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed, StatusCancelled, StatusSkipped:
		return true
	default:
		return false
	}
}

func validLogStream(stream LogStream) bool {
	switch stream {
	case LogStreamStdout, LogStreamStderr, LogStreamSystem:
		return true
	default:
		return false
	}
}

func canTransition(current, next Status) bool {
	switch current {
	case StatusQueued:
		return next == StatusRunning || next == StatusFailed || next == StatusCancelled || next == StatusSkipped
	case StatusRunning:
		return next == StatusSucceeded || next == StatusFailed || next == StatusCancelled || next == StatusSkipped
	default:
		return false
	}
}

func isTerminalStatus(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusSkipped:
		return true
	default:
		return false
	}
}

func cancellationFromRun(run Run) RunCancellation {
	return RunCancellation{
		RunID:       run.ID,
		Requested:   run.CancellationRequested,
		RequestedAt: copyTimePointer(run.CancellationRequestedAt),
	}
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

func int64Pointer(value int64) *int64 {
	copy := value
	return &copy
}

func optionalTextPointer(value string) *string {
	if value == "" {
		return nil
	}
	return stringPointer(value)
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

type executionScanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(scanner executionScanner) (Workflow, error) {
	var (
		workflow    Workflow
		definition  string
		environment string
		createdAt   int64
		updatedAt   int64
		active      int64
	)
	if err := scanner.Scan(
		&workflow.ID,
		&workflow.ProjectID,
		&workflow.Key,
		&workflow.Name,
		&definition,
		&environment,
		&workflow.Revision,
		&active,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Workflow{}, err
	}
	workflow.Definition = cloneJSON(json.RawMessage(definition))
	workflow.Environment = cloneJSON(json.RawMessage(environment))
	workflow.Active = active != 0
	workflow.CreatedAt = timeFromMillis(createdAt)
	workflow.UpdatedAt = timeFromMillis(updatedAt)
	return workflow, nil
}

func scanRun(scanner executionScanner) (Run, error) {
	var (
		run                     Run
		workflowID              sql.NullString
		workflowKey             sql.NullString
		workflowRevision        sql.NullInt64
		ref                     sql.NullString
		commitSHA               sql.NullString
		environment             string
		cancellationRequested   int64
		cancellationRequestedAt sql.NullInt64
		workerID                sql.NullString
		claimedAt               sql.NullInt64
		failureReason           sql.NullString
		sourcePath              string
		startedAt               sql.NullInt64
		finishedAt              sql.NullInt64
		createdAt               int64
		updatedAt               int64
	)
	if err := scanner.Scan(
		&run.ID,
		&run.ProjectID,
		&workflowID,
		&workflowKey,
		&workflowRevision,
		&run.TriggerType,
		&run.Status,
		&ref,
		&commitSHA,
		&environment,
		&cancellationRequested,
		&cancellationRequestedAt,
		&workerID,
		&claimedAt,
		&failureReason,
		&sourcePath,
		&startedAt,
		&finishedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Run{}, err
	}
	run.WorkflowID = nullStringPointer(workflowID)
	run.WorkflowKey = nullStringPointer(workflowKey)
	run.WorkflowRevision = nullInt64Pointer(workflowRevision)
	run.Ref = nullStringPointer(ref)
	run.CommitSHA = nullStringPointer(commitSHA)
	run.Environment = cloneJSON(json.RawMessage(environment))
	run.CancellationRequested = cancellationRequested != 0
	run.CancellationRequestedAt = nullTimePointer(cancellationRequestedAt)
	run.WorkerID = nullStringPointer(workerID)
	run.ClaimedAt = nullTimePointer(claimedAt)
	run.FailureReason = nullStringPointer(failureReason)
	run.SourcePath = sourcePath
	run.StartedAt = nullTimePointer(startedAt)
	run.FinishedAt = nullTimePointer(finishedAt)
	run.CreatedAt = timeFromMillis(createdAt)
	run.UpdatedAt = timeFromMillis(updatedAt)
	return run, nil
}

func scanJob(scanner executionScanner) (Job, error) {
	var (
		job            Job
		key            sql.NullString
		runner         sql.NullString
		environment    string
		dependencyKeys string
		allowFailure   int64
		timeoutMinutes int
		startedAt      sql.NullInt64
		finishedAt     sql.NullInt64
		createdAt      int64
		updatedAt      int64
	)
	if err := scanner.Scan(
		&job.ID,
		&job.RunID,
		&key,
		&job.Name,
		&job.Status,
		&runner,
		&job.Position,
		&environment,
		&dependencyKeys,
		&allowFailure,
		&timeoutMinutes,
		&startedAt,
		&finishedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Job{}, err
	}
	job.Key = nullStringPointer(key)
	job.Runner = nullStringPointer(runner)
	job.Environment = cloneJSON(json.RawMessage(environment))
	job.DependencyKeys = cloneJSON(json.RawMessage(dependencyKeys))
	job.AllowFailure = allowFailure != 0
	job.TimeoutMinutes = timeoutMinutes
	job.StartedAt = nullTimePointer(startedAt)
	job.FinishedAt = nullTimePointer(finishedAt)
	job.CreatedAt = timeFromMillis(createdAt)
	job.UpdatedAt = timeFromMillis(updatedAt)
	return job, nil
}

func scanStep(scanner executionScanner) (Step, error) {
	var (
		step             Step
		key              sql.NullString
		command          sql.NullString
		action           sql.NullString
		workingDirectory sql.NullString
		timeoutMinutes   int
		shell            sql.NullString
		allowFailure     int64
		environment      string
		startedAt        sql.NullInt64
		finishedAt       sql.NullInt64
		createdAt        int64
		updatedAt        int64
	)
	if err := scanner.Scan(
		&step.ID,
		&step.JobID,
		&key,
		&step.Index,
		&step.Name,
		&command,
		&step.Status,
		&environment,
		&action,
		&workingDirectory,
		&timeoutMinutes,
		&shell,
		&allowFailure,
		&startedAt,
		&finishedAt,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Step{}, err
	}
	step.Key = nullStringPointer(key)
	step.Command = nullStringPointer(command)
	step.Environment = cloneJSON(json.RawMessage(environment))
	step.Action = nullStringPointer(action)
	step.WorkingDirectory = nullStringPointer(workingDirectory)
	step.TimeoutMinutes = timeoutMinutes
	step.Shell = nullStringPointer(shell)
	step.AllowFailure = allowFailure != 0
	step.StartedAt = nullTimePointer(startedAt)
	step.FinishedAt = nullTimePointer(finishedAt)
	step.CreatedAt = timeFromMillis(createdAt)
	step.UpdatedAt = timeFromMillis(updatedAt)
	return step, nil
}

func scanLogLine(scanner executionScanner) (LogLine, error) {
	var (
		line      LogLine
		createdAt int64
	)
	if err := scanner.Scan(
		&line.ID,
		&line.RunID,
		&line.JobID,
		&line.StepID,
		&line.Sequence,
		&line.Stream,
		&line.Message,
		&createdAt,
	); err != nil {
		return LogLine{}, err
	}
	line.CreatedAt = timeFromMillis(createdAt)
	return line, nil
}

func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return stringPointer(value.String)
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return int64Pointer(value.Int64)
}

func nullTimePointer(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	converted := timeFromMillis(value.Int64)
	return &converted
}
