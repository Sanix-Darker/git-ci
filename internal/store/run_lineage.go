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
		if lineage.SourceDeploymentID == "" || lineage.TargetDeploymentID == "" {
			return lineage, invalidInput("rollback lineage", "requires source and target deployments")
		}
	case RunLineageJobReplay, RunLineageStepReplay:
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
