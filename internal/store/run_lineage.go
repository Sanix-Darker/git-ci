package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RunLineageKind string

const (
	RunLineageRollback   RunLineageKind = "rollback"
	RunLineageJobReplay  RunLineageKind = "job_replay"
	RunLineageStepReplay RunLineageKind = "step_replay"
)

type RunLineage struct {
	RunID, SourceRunID, Actor, IdempotencyKey string
	Kind                                      RunLineageKind
	SourceJobID, SourceStepID                 *string
	SourceDeploymentID, TargetDeploymentID    *string
	CreatedAt                                 time.Time
}

type EnqueueRunLineage struct {
	Kind                                   RunLineageKind
	SourceRunID, SourceJobID, SourceStepID string
	SourceDeploymentID, TargetDeploymentID string
	Actor, IdempotencyKey                  string
}

type RollbackTarget struct {
	DeploymentID, RunID, Ref, CommitSHA, JobName, CreatedAt string `json:"-"`
}

type RollbackTargetView struct {
	DeploymentID string    `json:"deploymentId"`
	RunID        string    `json:"runId"`
	Ref          string    `json:"ref,omitempty"`
	CommitSHA    string    `json:"commitSha"`
	JobName      string    `json:"jobName"`
	CreatedAt    time.Time `json:"createdAt"`
}

type RollbackEligibility struct {
	SourceDeploymentID string               `json:"sourceDeploymentId"`
	Eligible           bool                 `json:"eligible"`
	Code               string               `json:"code"`
	Message            string               `json:"message"`
	Targets            []RollbackTargetView `json:"targets"`
}

type EnqueueRollbackParams struct {
	SourceDeploymentID, TargetDeploymentID, Actor, IdempotencyKey string
}

type ErrRollbackEligibility struct{ Code, Message string }

func (err *ErrRollbackEligibility) Error() string {
	if err == nil || err.Message == "" {
		return "store: deployment is not eligible for rollback"
	}
	return "store: " + err.Message
}

type ReplayEligibility struct {
	Kind         RunLineageKind `json:"kind"`
	SourceRunID  string         `json:"sourceRunId"`
	SourceJobID  string         `json:"sourceJobId"`
	SourceStepID string         `json:"sourceStepId,omitempty"`
	Eligible     bool           `json:"eligible"`
	Code         string         `json:"code"`
	Message      string         `json:"message"`
}

type EnqueueReplayParams struct {
	Kind                      RunLineageKind
	SourceJobID, SourceStepID string
	Actor, IdempotencyKey     string
}

type ErrReplayEligibility struct{ Code, Message string }

func (err *ErrReplayEligibility) Error() string {
	if err == nil || err.Message == "" {
		return "store: source is not eligible for replay"
	}
	return "store: " + err.Message
}

const runLineageColumns = `run_id, kind, source_run_id, source_job_id, source_step_id, source_deployment_id, target_deployment_id, actor, idempotency_key, created_at`

func (s *Store) EvaluateDeploymentRollback(ctx context.Context, sourceDeploymentID string) (RollbackEligibility, error) {
	source, err := s.GetDeployment(ctx, sourceDeploymentID)
	if err != nil {
		return RollbackEligibility{}, err
	}
	result := RollbackEligibility{SourceDeploymentID: source.ID, Code: "no_rollback_targets", Message: "no previous successful deployment has an explicit rollback command", Targets: []RollbackTargetView{}}
	if !rollbackSourceStatus(source.Status) {
		result.Code, result.Message = "deployment_not_terminal", "deployment must be succeeded, failed, or cancelled before rollback"
		return result, nil
	}
	deployments, err := s.ListDeployments(ctx, source.ProjectID)
	if err != nil {
		return RollbackEligibility{}, err
	}
	for _, target := range deployments {
		if target.ID == source.ID || target.Environment != source.Environment || target.Status != StatusSucceeded || target.JobID == nil || target.CreatedAt.After(source.CreatedAt) {
			continue
		}
		graph, jobs, err := s.rollbackGraph(ctx, target)
		if err != nil {
			var ineligible *ErrRollbackEligibility
			if errors.As(err, &ineligible) {
				continue
			}
			return RollbackEligibility{}, err
		}
		var targetJobName string
		for _, item := range jobs {
			if item.Key == pointerText(graph.targetJob.Key) {
				targetJobName = item.Name
			}
		}
		result.Targets = append(result.Targets, RollbackTargetView{
			DeploymentID: target.ID, RunID: target.RunID, Ref: pointerText(graph.graph.Run.Ref),
			CommitSHA: pointerText(graph.graph.Run.CommitSHA), JobName: targetJobName, CreatedAt: target.CreatedAt,
		})
	}
	if len(result.Targets) > 0 {
		result.Eligible, result.Code, result.Message = true, "eligible", "rollback can be enqueued"
	}
	return result, nil
}

func (s *Store) EnqueueDeploymentRollback(ctx context.Context, params EnqueueRollbackParams) (Run, error) {
	var err error
	if params.SourceDeploymentID, err = normalizeRequiredText("rollback source deployment ID", params.SourceDeploymentID); err != nil {
		return Run{}, err
	}
	if params.TargetDeploymentID, err = normalizeRequiredText("rollback target deployment ID", params.TargetDeploymentID); err != nil {
		return Run{}, err
	}
	if params.Actor, err = normalizeRequiredText("rollback actor", params.Actor); err != nil {
		return Run{}, err
	}
	if params.IdempotencyKey, err = normalizeRequiredText("rollback idempotency key", params.IdempotencyKey); err != nil {
		return Run{}, err
	}
	if existing, err := s.GetRunLineageByIdempotency(ctx, params.Actor, params.IdempotencyKey); err == nil {
		return s.existingRollbackRun(ctx, existing, params)
	} else {
		var notFound *ErrNotFound
		if !errors.As(err, &notFound) {
			return Run{}, err
		}
	}

	source, err := s.GetDeployment(ctx, params.SourceDeploymentID)
	if err != nil {
		return Run{}, err
	}
	target, err := s.GetDeployment(ctx, params.TargetDeploymentID)
	if err != nil {
		return Run{}, err
	}
	if err := validateRollbackPair(source, target); err != nil {
		return Run{}, err
	}
	graph, jobs, err := s.rollbackGraph(ctx, target)
	if err != nil {
		return Run{}, err
	}
	var active string
	err = s.db.QueryRowContext(ctx, `SELECT lineage.run_id FROM run_lineage AS lineage JOIN runs AS run ON run.id = lineage.run_id WHERE lineage.kind = 'rollback' AND lineage.source_deployment_id = ? AND lineage.target_deployment_id = ? AND run.status IN ('queued','waiting','running') LIMIT 1`, source.ID, target.ID).Scan(&active)
	if err == nil {
		return Run{}, &ErrConflict{Resource: "rollback", Field: "activeRun", Value: active}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("store: check active rollback: %w", err)
	}

	environment := map[string]string{}
	if err := json.Unmarshal(graph.graph.Run.Environment, &environment); err != nil {
		return Run{}, fmt.Errorf("store: decode rollback environment: %w", err)
	}
	environment["GCI_ROLLBACK_SOURCE_DEPLOYMENT_ID"] = source.ID
	environment["GCI_ROLLBACK_TARGET_DEPLOYMENT_ID"] = target.ID
	environment["GCI_ROLLBACK_TARGET_SHA"] = pointerText(graph.graph.Run.CommitSHA)
	environmentJSON, err := json.Marshal(environment)
	if err != nil {
		return Run{}, err
	}
	if graph.graph.Run.WorkflowID == nil || graph.graph.Run.CommitSHA == nil {
		return Run{}, rollbackError("target_snapshot_missing", "rollback target has no immutable workflow snapshot")
	}
	run, err := s.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: source.ProjectID, WorkflowID: *graph.graph.Run.WorkflowID, TriggerType: "rollback",
		Ref: pointerText(graph.graph.Run.Ref), CommitSHA: *graph.graph.Run.CommitSHA, SourcePath: graph.graph.Run.SourcePath,
		Environment: environmentJSON, Jobs: jobs,
		Lineage: &EnqueueRunLineage{Kind: RunLineageRollback, SourceRunID: source.RunID, SourceDeploymentID: source.ID, TargetDeploymentID: target.ID, Actor: params.Actor, IdempotencyKey: params.IdempotencyKey},
	})
	if err == nil {
		return run, nil
	}
	if existing, lookupErr := s.GetRunLineageByIdempotency(ctx, params.Actor, params.IdempotencyKey); lookupErr == nil {
		return s.existingRollbackRun(ctx, existing, params)
	}
	if strings.Contains(err.Error(), "active rollback already exists") {
		return Run{}, &ErrConflict{Resource: "rollback", Field: "active", Value: source.ID}
	}
	return Run{}, err
}

type replaySource struct {
	graph RunGraph
	job   JobGraph
	step  *Step
}

func (s *Store) EvaluateJobReplay(ctx context.Context, sourceJobID string) (ReplayEligibility, error) {
	result, _, err := s.evaluateReplay(ctx, RunLineageJobReplay, sourceJobID, "")
	return result, err
}

func (s *Store) EvaluateStepReplay(ctx context.Context, sourceStepID string) (ReplayEligibility, error) {
	result, _, err := s.evaluateReplay(ctx, RunLineageStepReplay, "", sourceStepID)
	return result, err
}

func (s *Store) GetReplaySourceRun(ctx context.Context, params EnqueueReplayParams) (Run, error) {
	source, err := s.loadReplaySource(ctx, params.Kind, params.SourceJobID, params.SourceStepID)
	if err != nil {
		return Run{}, err
	}
	return source.graph.Run, nil
}

func (s *Store) EnqueueRunReplay(ctx context.Context, params EnqueueReplayParams) (Run, error) {
	var err error
	if params.Actor, err = normalizeRequiredText("replay actor", params.Actor); err != nil {
		return Run{}, err
	}
	if params.IdempotencyKey, err = normalizeRequiredText("replay idempotency key", params.IdempotencyKey); err != nil {
		return Run{}, err
	}
	switch params.Kind {
	case RunLineageJobReplay:
		if params.SourceJobID, err = normalizeRequiredText("replay source job ID", params.SourceJobID); err != nil {
			return Run{}, err
		}
		if strings.TrimSpace(params.SourceStepID) != "" {
			return Run{}, invalidInput("job replay", "must not include a source step")
		}
	case RunLineageStepReplay:
		if params.SourceStepID, err = normalizeRequiredText("replay source step ID", params.SourceStepID); err != nil {
			return Run{}, err
		}
	default:
		return Run{}, invalidInput("replay kind", "must be job_replay or step_replay")
	}
	if existing, lookupErr := s.GetRunLineageByIdempotency(ctx, params.Actor, params.IdempotencyKey); lookupErr == nil {
		return s.existingReplayRun(ctx, existing, params)
	} else {
		var notFound *ErrNotFound
		if !errors.As(lookupErr, &notFound) {
			return Run{}, lookupErr
		}
	}

	eligibility, source, err := s.evaluateReplay(ctx, params.Kind, params.SourceJobID, params.SourceStepID)
	if err != nil {
		return Run{}, err
	}
	if !eligibility.Eligible {
		return Run{}, &ErrReplayEligibility{Code: eligibility.Code, Message: eligibility.Message}
	}
	jobs, err := s.cloneReplayJobs(ctx, source)
	if err != nil {
		return Run{}, err
	}
	if source.graph.Run.WorkflowID == nil || source.graph.Run.CommitSHA == nil {
		return Run{}, replayError("source_snapshot_missing", "source run has no immutable workflow snapshot")
	}
	lineage := EnqueueRunLineage{
		Kind: params.Kind, SourceRunID: source.graph.Run.ID, SourceJobID: source.job.Job.ID,
		Actor: params.Actor, IdempotencyKey: params.IdempotencyKey,
	}
	if source.step != nil {
		lineage.SourceStepID = source.step.ID
	}
	run, err := s.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: source.graph.Run.ProjectID, WorkflowID: *source.graph.Run.WorkflowID,
		TriggerType: string(params.Kind), Ref: pointerText(source.graph.Run.Ref),
		CommitSHA: *source.graph.Run.CommitSHA, SourcePath: source.graph.Run.SourcePath,
		Environment: cloneJSON(source.graph.Run.Environment), Jobs: jobs, Lineage: &lineage,
	})
	if err == nil {
		return run, nil
	}
	if existing, lookupErr := s.GetRunLineageByIdempotency(ctx, params.Actor, params.IdempotencyKey); lookupErr == nil {
		return s.existingReplayRun(ctx, existing, params)
	}
	if strings.Contains(err.Error(), "active job replay already exists") || strings.Contains(err.Error(), "active step replay already exists") {
		return Run{}, &ErrConflict{Resource: "replay", Field: "active", Value: eligibility.SourceJobID}
	}
	return Run{}, err
}

func (s *Store) evaluateReplay(ctx context.Context, kind RunLineageKind, sourceJobID, sourceStepID string) (ReplayEligibility, replaySource, error) {
	source, err := s.loadReplaySource(ctx, kind, sourceJobID, sourceStepID)
	if err != nil {
		return ReplayEligibility{}, replaySource{}, err
	}
	result := ReplayEligibility{
		Kind: kind, SourceRunID: source.graph.Run.ID, SourceJobID: source.job.Job.ID,
		Code: "eligible", Message: "replay can be enqueued",
	}
	if source.step != nil {
		result.SourceStepID = source.step.ID
	}
	switch {
	case !replayTerminalStatus(source.graph.Run.Status):
		result.Code, result.Message = "source_run_not_terminal", "source run must be terminal before replay"
	case !replayTerminalStatus(source.job.Job.Status):
		result.Code, result.Message = "source_job_not_terminal", "source job must be terminal before replay"
	case source.graph.Run.WorkflowID == nil || source.graph.Run.CommitSHA == nil || strings.TrimSpace(source.graph.Run.SourcePath) == "":
		result.Code, result.Message = "source_snapshot_missing", "source run has no immutable workflow snapshot"
	case source.step != nil && !replayTerminalStatus(source.step.Status):
		result.Code, result.Message = "source_step_not_terminal", "source step must be terminal before replay"
	case source.step != nil && (source.step.Command == nil || strings.TrimSpace(*source.step.Command) == ""):
		result.Code, result.Message = "source_step_not_runnable", "only persisted shell command steps can be replayed"
	default:
		active, activeErr := s.activeReplayRun(ctx, kind, source.job.Job.ID, result.SourceStepID)
		if activeErr != nil {
			return ReplayEligibility{}, replaySource{}, activeErr
		}
		if active != "" {
			result.Code, result.Message = "active_replay_exists", "an active replay already exists for this source"
		} else {
			result.Eligible = true
		}
	}
	return result, source, nil
}

func (s *Store) loadReplaySource(ctx context.Context, kind RunLineageKind, sourceJobID, sourceStepID string) (replaySource, error) {
	var runID, actualJobID string
	switch kind {
	case RunLineageJobReplay:
		sourceJobID = strings.TrimSpace(sourceJobID)
		if err := s.db.QueryRowContext(ctx, `SELECT run_id FROM jobs WHERE id = ?`, sourceJobID).Scan(&runID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return replaySource{}, &ErrNotFound{Resource: "job", Key: sourceJobID}
			}
			return replaySource{}, err
		}
		actualJobID = sourceJobID
	case RunLineageStepReplay:
		sourceStepID = strings.TrimSpace(sourceStepID)
		if err := s.db.QueryRowContext(ctx, `SELECT jobs.run_id, steps.job_id FROM steps JOIN jobs ON jobs.id = steps.job_id WHERE steps.id = ?`, sourceStepID).Scan(&runID, &actualJobID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return replaySource{}, &ErrNotFound{Resource: "step", Key: sourceStepID}
			}
			return replaySource{}, err
		}
		if strings.TrimSpace(sourceJobID) != "" && strings.TrimSpace(sourceJobID) != actualJobID {
			return replaySource{}, invalidInput("replay source job", "does not own source step")
		}
	default:
		return replaySource{}, invalidInput("replay kind", "must be job_replay or step_replay")
	}
	graph, err := s.GetRunGraph(ctx, runID)
	if err != nil {
		return replaySource{}, err
	}
	result := replaySource{graph: graph}
	for _, item := range graph.Jobs {
		if item.Job.ID != actualJobID {
			continue
		}
		result.job = item
		if kind == RunLineageStepReplay {
			for _, step := range item.Steps {
				if step.ID == sourceStepID {
					selected := step
					result.step = &selected
					break
				}
			}
		}
		break
	}
	if result.job.Job.ID == "" || (kind == RunLineageStepReplay && result.step == nil) {
		return replaySource{}, &ErrNotFound{Resource: "replay source", Key: actualJobID}
	}
	return result, nil
}

func (s *Store) activeReplayRun(ctx context.Context, kind RunLineageKind, sourceJobID, sourceStepID string) (string, error) {
	query, arguments := `SELECT lineage.run_id FROM run_lineage AS lineage JOIN runs AS run ON run.id = lineage.run_id WHERE lineage.kind = ? AND lineage.source_job_id = ? AND run.status IN ('queued','waiting','running')`, []any{kind, sourceJobID}
	if kind == RunLineageStepReplay {
		query += ` AND lineage.source_step_id = ?`
		arguments = append(arguments, sourceStepID)
	}
	query += ` LIMIT 1`
	var runID string
	if err := s.db.QueryRowContext(ctx, query, arguments...).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return runID, nil
}

func (s *Store) cloneReplayJobs(ctx context.Context, source replaySource) ([]EnqueueJob, error) {
	if source.step != nil {
		job, err := s.cloneReplayJob(ctx, source.job)
		if err != nil {
			return nil, err
		}
		job.DependencyKeys = json.RawMessage(`[]`)
		job.Steps = []EnqueueStep{cloneReplayStep(*source.step)}
		return []EnqueueJob{job}, nil
	}
	byKey := make(map[string]JobGraph, len(source.graph.Jobs))
	for _, item := range source.graph.Jobs {
		byKey[pointerText(item.Job.Key)] = item
	}
	included := map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if included[key] {
			return nil
		}
		item, ok := byKey[key]
		if !ok {
			return replayError("source_graph_invalid", "source dependency graph is incomplete")
		}
		included[key] = true
		var dependencies []string
		if err := json.Unmarshal(item.Job.DependencyKeys, &dependencies); err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(pointerText(source.job.Job.Key)); err != nil {
		return nil, err
	}
	jobs := make([]EnqueueJob, 0, len(included))
	for _, item := range source.graph.Jobs {
		if !included[pointerText(item.Job.Key)] {
			continue
		}
		job, err := s.cloneReplayJob(ctx, item)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) cloneReplayJob(ctx context.Context, item JobGraph) (EnqueueJob, error) {
	job := EnqueueJob{
		Key: pointerText(item.Job.Key), Name: item.Job.Name, Runner: pointerText(item.Job.Runner),
		Environment: cloneJSON(item.Job.Environment), DependencyKeys: cloneJSON(item.Job.DependencyKeys),
		AllowFailure: item.Job.AllowFailure, TimeoutMinutes: item.Job.TimeoutMinutes,
		RollbackCommand: pointerText(item.Job.RollbackCommand), VerifyCommand: pointerText(item.Job.VerifyCommand),
	}
	target, err := s.GetDeploymentTargetForJob(ctx, item.Job.ID)
	if err == nil {
		job.EnvironmentName, job.DeploymentTier = target.Environment, string(target.DeploymentTier)
	} else {
		var notFound *ErrNotFound
		if !errors.As(err, &notFound) {
			return EnqueueJob{}, err
		}
	}
	for _, step := range item.Steps {
		job.Steps = append(job.Steps, cloneReplayStep(step))
	}
	return job, nil
}

func cloneReplayStep(step Step) EnqueueStep {
	return EnqueueStep{
		Key: pointerText(step.Key), Name: step.Name, Command: pointerText(step.Command), Action: pointerText(step.Action),
		WorkingDirectory: pointerText(step.WorkingDirectory), TimeoutMinutes: step.TimeoutMinutes,
		Shell: pointerText(step.Shell), AllowFailure: step.AllowFailure, Environment: cloneJSON(step.Environment),
	}
}

func replayTerminalStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled || status == StatusSkipped
}

func replayError(code, message string) error {
	return &ErrReplayEligibility{Code: code, Message: message}
}

type rollbackGraph struct {
	graph     RunGraph
	targetJob Job
}

func (s *Store) rollbackGraph(ctx context.Context, target Deployment) (rollbackGraph, []EnqueueJob, error) {
	if target.JobID == nil {
		return rollbackGraph{}, nil, rollbackError("target_job_missing", "rollback target is not tied to a workflow job")
	}
	graph, err := s.GetRunGraph(ctx, target.RunID)
	if err != nil {
		return rollbackGraph{}, nil, err
	}
	jobByKey := make(map[string]JobGraph, len(graph.Jobs))
	var targetItem JobGraph
	for _, item := range graph.Jobs {
		key := pointerText(item.Job.Key)
		jobByKey[key] = item
		if item.Job.ID == *target.JobID {
			targetItem = item
		}
	}
	if targetItem.Job.ID == "" || targetItem.Job.RollbackCommand == nil {
		return rollbackGraph{}, nil, rollbackError("rollback_command_missing", "rollback target has no explicit rollback command")
	}
	included := map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if included[key] {
			return nil
		}
		item, ok := jobByKey[key]
		if !ok {
			return rollbackError("target_graph_invalid", "rollback target dependency graph is incomplete")
		}
		included[key] = true
		var dependencies []string
		if err := json.Unmarshal(item.Job.DependencyKeys, &dependencies); err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(pointerText(targetItem.Job.Key)); err != nil {
		return rollbackGraph{}, nil, err
	}
	jobs := make([]EnqueueJob, 0, len(included))
	for _, item := range graph.Jobs {
		key := pointerText(item.Job.Key)
		if !included[key] {
			continue
		}
		targetMetadata, metadataErr := s.GetDeploymentTargetForJob(ctx, item.Job.ID)
		if item.Job.ID != targetItem.Job.ID && metadataErr == nil {
			return rollbackGraph{}, nil, rollbackError("deployment_dependency", "rollback dependency closure contains another deployment job")
		}
		var notFound *ErrNotFound
		if metadataErr != nil && !errors.As(metadataErr, &notFound) {
			return rollbackGraph{}, nil, metadataErr
		}
		job := EnqueueJob{Key: key, Name: item.Job.Name, Runner: pointerText(item.Job.Runner), Environment: cloneJSON(item.Job.Environment), DependencyKeys: cloneJSON(item.Job.DependencyKeys), AllowFailure: item.Job.AllowFailure, TimeoutMinutes: item.Job.TimeoutMinutes, RollbackCommand: pointerText(item.Job.RollbackCommand), VerifyCommand: pointerText(item.Job.VerifyCommand)}
		if item.Job.ID == targetItem.Job.ID {
			if metadataErr != nil {
				return rollbackGraph{}, nil, rollbackError("target_environment_missing", "rollback target has no deployment environment snapshot")
			}
			job.EnvironmentName, job.DeploymentTier = targetMetadata.Environment, string(targetMetadata.DeploymentTier)
			job.Steps = []EnqueueStep{{Key: key + ":rollback", Name: "Rollback " + targetMetadata.Environment, Command: *item.Job.RollbackCommand, Shell: "sh", Environment: json.RawMessage(`{}`)}}
			if item.Job.VerifyCommand != nil {
				job.Steps = append(job.Steps, EnqueueStep{Key: key + ":verify", Name: "Verify " + targetMetadata.Environment, Command: *item.Job.VerifyCommand, Shell: "sh", Environment: json.RawMessage(`{}`)})
			}
		} else {
			for _, step := range item.Steps {
				job.Steps = append(job.Steps, EnqueueStep{Key: pointerText(step.Key), Name: step.Name, Command: pointerText(step.Command), Action: pointerText(step.Action), WorkingDirectory: pointerText(step.WorkingDirectory), TimeoutMinutes: step.TimeoutMinutes, Shell: pointerText(step.Shell), AllowFailure: step.AllowFailure, Environment: cloneJSON(step.Environment)})
			}
		}
		jobs = append(jobs, job)
	}
	return rollbackGraph{graph: graph, targetJob: targetItem.Job}, jobs, nil
}

func validateRollbackPair(source, target Deployment) error {
	if source.ID == target.ID {
		return rollbackError("same_deployment", "source and target deployments must differ")
	}
	if !rollbackSourceStatus(source.Status) {
		return rollbackError("deployment_not_terminal", "source deployment is not terminal")
	}
	if source.ProjectID != target.ProjectID || source.Environment != target.Environment {
		return rollbackError("target_mismatch", "rollback target must belong to the same project and environment")
	}
	if target.Status != StatusSucceeded {
		return rollbackError("target_not_successful", "rollback target must be a successful deployment")
	}
	if target.CreatedAt.After(source.CreatedAt) {
		return rollbackError("target_not_previous", "rollback target must precede the source deployment")
	}
	return nil
}

func rollbackSourceStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled
}
func rollbackError(code, message string) error {
	return &ErrRollbackEligibility{Code: code, Message: message}
}
func pointerText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeEnqueueRunLineage(lineage EnqueueRunLineage) (EnqueueRunLineage, error) {
	var err error
	if lineage.SourceRunID, err = normalizeRequiredText("lineage source run ID", lineage.SourceRunID); err != nil {
		return lineage, err
	}
	if lineage.Actor, err = normalizeRequiredText("lineage actor", lineage.Actor); err != nil {
		return lineage, err
	}
	if lineage.IdempotencyKey, err = normalizeRequiredText("lineage idempotency key", lineage.IdempotencyKey); err != nil {
		return lineage, err
	}
	switch lineage.Kind {
	case RunLineageRollback:
		if lineage.SourceJobID != "" || lineage.SourceStepID != "" || lineage.SourceDeploymentID == "" || lineage.TargetDeploymentID == "" {
			return lineage, invalidInput("rollback lineage", "requires source and target deployments")
		}
	case RunLineageJobReplay:
		if lineage.SourceJobID == "" || lineage.SourceStepID != "" || lineage.SourceDeploymentID != "" || lineage.TargetDeploymentID != "" {
			return lineage, invalidInput("job replay lineage", "requires only a source job")
		}
	case RunLineageStepReplay:
		if lineage.SourceJobID == "" || lineage.SourceStepID == "" || lineage.SourceDeploymentID != "" || lineage.TargetDeploymentID != "" {
			return lineage, invalidInput("step replay lineage", "requires a source job and step")
		}
	default:
		return lineage, invalidInput("lineage kind", "must be rollback, job_replay, or step_replay")
	}
	return lineage, nil
}

func insertRunLineage(ctx context.Context, tx *sql.Tx, runID string, lineage EnqueueRunLineage, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO run_lineage (`+runLineageColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, lineage.Kind, lineage.SourceRunID, nullableText(lineage.SourceJobID), nullableText(lineage.SourceStepID), nullableText(lineage.SourceDeploymentID), nullableText(lineage.TargetDeploymentID), lineage.Actor, lineage.IdempotencyKey, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: insert run lineage: %w", err)
	}
	return nil
}

func (s *Store) GetRunLineageByIdempotency(ctx context.Context, actor, key string) (RunLineage, error) {
	lineage, err := scanRunLineage(s.db.QueryRowContext(ctx, `SELECT `+runLineageColumns+` FROM run_lineage WHERE actor = ? AND idempotency_key = ?`, strings.TrimSpace(actor), strings.TrimSpace(key)))
	if errors.Is(err, sql.ErrNoRows) {
		return RunLineage{}, &ErrNotFound{Resource: "run lineage", Key: key}
	}
	return lineage, err
}

func (s *Store) existingRollbackRun(ctx context.Context, lineage RunLineage, params EnqueueRollbackParams) (Run, error) {
	if lineage.Kind != RunLineageRollback || pointerText(lineage.SourceDeploymentID) != params.SourceDeploymentID || pointerText(lineage.TargetDeploymentID) != params.TargetDeploymentID {
		return Run{}, &ErrConflict{Resource: "idempotency key", Field: "payload", Value: params.IdempotencyKey}
	}
	graph, err := s.GetRunGraph(ctx, lineage.RunID)
	return graph.Run, err
}

func (s *Store) existingReplayRun(ctx context.Context, lineage RunLineage, params EnqueueReplayParams) (Run, error) {
	matches := lineage.Kind == params.Kind
	if params.Kind == RunLineageJobReplay {
		matches = matches && pointerText(lineage.SourceJobID) == params.SourceJobID && lineage.SourceStepID == nil
	} else {
		matches = matches && pointerText(lineage.SourceStepID) == params.SourceStepID
	}
	if !matches {
		return Run{}, &ErrConflict{Resource: "idempotency key", Field: "payload", Value: params.IdempotencyKey}
	}
	graph, err := s.GetRunGraph(ctx, lineage.RunID)
	return graph.Run, err
}

func scanRunLineage(scanner interface{ Scan(...any) error }) (RunLineage, error) {
	var item RunLineage
	var sourceJob, sourceStep, sourceDeployment, targetDeployment sql.NullString
	var created int64
	if err := scanner.Scan(&item.RunID, &item.Kind, &item.SourceRunID, &sourceJob, &sourceStep, &sourceDeployment, &targetDeployment, &item.Actor, &item.IdempotencyKey, &created); err != nil {
		return RunLineage{}, err
	}
	item.SourceJobID, item.SourceStepID = nullStringPointer(sourceJob), nullStringPointer(sourceStep)
	item.SourceDeploymentID, item.TargetDeploymentID = nullStringPointer(sourceDeployment), nullStringPointer(targetDeployment)
	item.CreatedAt = timeFromMillis(created)
	return item, nil
}
