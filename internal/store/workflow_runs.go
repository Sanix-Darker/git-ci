package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxWorkflowRunDepth = 3

type WorkflowRunConclusion string

const (
	WorkflowRunSuccess   WorkflowRunConclusion = "success"
	WorkflowRunFailure   WorkflowRunConclusion = "failure"
	WorkflowRunCancelled WorkflowRunConclusion = "cancelled"
	WorkflowRunSkipped   WorkflowRunConclusion = "skipped"
)

type WorkflowRunDispatch struct {
	SourceRunID            string                `json:"sourceRunId"`
	SourceWorkflowName     string                `json:"sourceWorkflowName"`
	SourceWorkflowRevision int64                 `json:"sourceWorkflowRevision"`
	Conclusion             WorkflowRunConclusion `json:"conclusion"`
	CreatedAt              time.Time             `json:"createdAt"`
}

type WorkflowRunLink struct {
	RunID                  string                `json:"runId"`
	SourceRunID            string                `json:"sourceRunId"`
	SourceWorkflowName     string                `json:"sourceWorkflowName"`
	SourceWorkflowRevision int64                 `json:"sourceWorkflowRevision"`
	SourceConclusion       WorkflowRunConclusion `json:"sourceConclusion"`
	TargetWorkflowID       string                `json:"targetWorkflowId"`
	TargetWorkflowRevision int64                 `json:"targetWorkflowRevision"`
	Depth                  int                   `json:"depth"`
	IdempotencyKey         string                `json:"idempotencyKey"`
	CreatedAt              time.Time             `json:"createdAt"`
}

type EnqueueWorkflowRunLink struct {
	SourceRunID, SourceWorkflowName, TargetWorkflowID, IdempotencyKey string
	SourceWorkflowRevision, TargetWorkflowRevision                    int64
	SourceConclusion                                                  WorkflowRunConclusion
	Depth                                                             int
}

const workflowRunLinkColumns = `run_id, source_run_id, source_workflow_name, source_workflow_revision, source_conclusion, target_workflow_id, target_workflow_revision, depth, idempotency_key, created_at`

func (s *Store) ListPendingWorkflowRunDispatches(ctx context.Context, limit int) ([]WorkflowRunDispatch, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 256 {
		return nil, invalidInput("workflow_run dispatch limit", "must be between 1 and 256")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT source_run_id, source_workflow_name, source_workflow_revision, conclusion, created_at
		FROM workflow_run_dispatches
		WHERE dispatched_at IS NULL
		ORDER BY created_at ASC, source_run_id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list workflow_run dispatches: %w", err)
	}
	defer rows.Close()
	items := make([]WorkflowRunDispatch, 0)
	for rows.Next() {
		var item WorkflowRunDispatch
		var created int64
		if err := rows.Scan(&item.SourceRunID, &item.SourceWorkflowName, &item.SourceWorkflowRevision, &item.Conclusion, &created); err != nil {
			return nil, fmt.Errorf("store: scan workflow_run dispatch: %w", err)
		}
		item.CreatedAt = timeFromMillis(created)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate workflow_run dispatches: %w", err)
	}
	return items, nil
}

func (s *Store) MarkWorkflowRunDispatched(ctx context.Context, sourceRunID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	sourceRunID, err = normalizeRequiredText("workflow_run source run ID", sourceRunID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE workflow_run_dispatches SET dispatched_at = COALESCE(dispatched_at, ?) WHERE source_run_id = ?`, nowUTC().UnixMilli(), sourceRunID)
	if err != nil {
		return fmt.Errorf("store: mark workflow_run dispatched: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: inspect workflow_run dispatch update: %w", err)
	}
	if changed == 0 {
		return &ErrNotFound{Resource: "workflow_run dispatch", Key: sourceRunID}
	}
	return nil
}

func (s *Store) GetWorkflowRunLink(ctx context.Context, runID string) (WorkflowRunLink, error) {
	if err := requireContext(ctx); err != nil {
		return WorkflowRunLink{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WorkflowRunLink{}, err
	}
	runID, err = normalizeRequiredText("workflow_run run ID", runID)
	if err != nil {
		return WorkflowRunLink{}, err
	}
	item, err := scanWorkflowRunLink(db.QueryRowContext(ctx, `SELECT `+workflowRunLinkColumns+` FROM workflow_run_links WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowRunLink{}, &ErrNotFound{Resource: "workflow_run link", Key: runID}
	}
	if err != nil {
		return WorkflowRunLink{}, fmt.Errorf("store: get workflow_run link: %w", err)
	}
	return item, nil
}

func (s *Store) GetWorkflowRunLinkByIdempotency(ctx context.Context, key string) (WorkflowRunLink, error) {
	if err := requireContext(ctx); err != nil {
		return WorkflowRunLink{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WorkflowRunLink{}, err
	}
	key, err = normalizeRequiredText("workflow_run idempotency key", key)
	if err != nil {
		return WorkflowRunLink{}, err
	}
	item, err := scanWorkflowRunLink(db.QueryRowContext(ctx, `SELECT `+workflowRunLinkColumns+` FROM workflow_run_links WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowRunLink{}, &ErrNotFound{Resource: "workflow_run link", Key: key}
	}
	if err != nil {
		return WorkflowRunLink{}, fmt.Errorf("store: get workflow_run link by idempotency: %w", err)
	}
	return item, nil
}

func normalizeEnqueueWorkflowRunLink(link EnqueueWorkflowRunLink) (EnqueueWorkflowRunLink, error) {
	var err error
	if link.SourceRunID, err = normalizeRequiredText("workflow_run source run ID", link.SourceRunID); err != nil {
		return link, err
	}
	if link.SourceWorkflowName, err = normalizeRequiredText("workflow_run source workflow name", link.SourceWorkflowName); err != nil {
		return link, err
	}
	if link.TargetWorkflowID, err = normalizeRequiredText("workflow_run target workflow ID", link.TargetWorkflowID); err != nil {
		return link, err
	}
	if link.IdempotencyKey, err = normalizeRequiredText("workflow_run idempotency key", link.IdempotencyKey); err != nil {
		return link, err
	}
	if link.SourceWorkflowRevision < 0 || link.TargetWorkflowRevision <= 0 {
		return link, invalidInput("workflow_run revision", "source must be non-negative and target must be positive")
	}
	if !validWorkflowRunConclusion(link.SourceConclusion) {
		return link, invalidInput("workflow_run conclusion", "must be success, failure, cancelled, or skipped")
	}
	if link.Depth < 1 || link.Depth > maxWorkflowRunDepth {
		return link, invalidInput("workflow_run depth", "must be between 1 and 3")
	}
	return link, nil
}

func insertWorkflowRunLink(ctx context.Context, tx *sql.Tx, run Run, link EnqueueWorkflowRunLink, now time.Time) error {
	if run.WorkflowID == nil || run.WorkflowRevision == nil || *run.WorkflowID != link.TargetWorkflowID || *run.WorkflowRevision != link.TargetWorkflowRevision {
		return invalidInput("workflow_run target", "must match the generated run workflow snapshot")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_run_links (`+workflowRunLinkColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, run.ID, link.SourceRunID, link.SourceWorkflowName, link.SourceWorkflowRevision, link.SourceConclusion, link.TargetWorkflowID, link.TargetWorkflowRevision, link.Depth, link.IdempotencyKey, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("store: insert workflow_run link: %w", err)
	}
	return nil
}

func attachWorkflowRunLink(ctx context.Context, db *sql.DB, graph *RunGraph) error {
	item, err := scanWorkflowRunLink(db.QueryRowContext(ctx, `SELECT `+workflowRunLinkColumns+` FROM workflow_run_links WHERE run_id = ?`, graph.Run.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: attach workflow_run link: %w", err)
	}
	graph.WorkflowRun = &item
	return nil
}

func scanWorkflowRunLink(scanner interface{ Scan(...any) error }) (WorkflowRunLink, error) {
	var item WorkflowRunLink
	var created int64
	if err := scanner.Scan(&item.RunID, &item.SourceRunID, &item.SourceWorkflowName, &item.SourceWorkflowRevision, &item.SourceConclusion, &item.TargetWorkflowID, &item.TargetWorkflowRevision, &item.Depth, &item.IdempotencyKey, &created); err != nil {
		return WorkflowRunLink{}, err
	}
	item.CreatedAt = timeFromMillis(created)
	return item, nil
}

func validWorkflowRunConclusion(value WorkflowRunConclusion) bool {
	switch WorkflowRunConclusion(strings.TrimSpace(string(value))) {
	case WorkflowRunSuccess, WorkflowRunFailure, WorkflowRunCancelled, WorkflowRunSkipped:
		return true
	default:
		return false
	}
}
