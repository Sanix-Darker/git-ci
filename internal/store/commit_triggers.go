package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const commitTriggerColumns = `
	project_id,
	ref,
	enabled,
	last_commit_sha,
	last_checked_at,
	last_triggered_at,
	last_error,
	created_at,
	updated_at`

type ProjectCommitTrigger struct {
	ProjectID       string     `json:"projectId"`
	Ref             string     `json:"ref"`
	Enabled         bool       `json:"enabled"`
	LastCommitSHA   *string    `json:"lastCommitSha,omitempty"`
	LastCheckedAt   *time.Time `json:"lastCheckedAt,omitempty"`
	LastTriggeredAt *time.Time `json:"lastTriggeredAt,omitempty"`
	LastError       *string    `json:"lastError,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type UpsertProjectCommitTriggerParams struct {
	ProjectID     string
	Ref           string
	Enabled       bool
	LastCommitSHA *string
}

func (s *Store) UpsertProjectCommitTrigger(ctx context.Context, params UpsertProjectCommitTriggerParams) (ProjectCommitTrigger, error) {
	if err := requireContext(ctx); err != nil {
		return ProjectCommitTrigger{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return ProjectCommitTrigger{}, err
	}
	if params.ProjectID, err = normalizeRequiredText("commit trigger project ID", params.ProjectID); err != nil {
		return ProjectCommitTrigger{}, err
	}
	if params.Ref, err = normalizeRequiredText("commit trigger ref", params.Ref); err != nil {
		return ProjectCommitTrigger{}, err
	}
	if params.LastCommitSHA, err = normalizeOptionalString("commit trigger commit SHA", params.LastCommitSHA); err != nil {
		return ProjectCommitTrigger{}, err
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO project_commit_triggers (
			project_id, ref, enabled, last_commit_sha, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			ref = excluded.ref,
			enabled = excluded.enabled,
			last_commit_sha = excluded.last_commit_sha,
			last_error = NULL,
			updated_at = excluded.updated_at
	`, params.ProjectID, params.Ref, boolToInteger(params.Enabled), nullableString(params.LastCommitSHA), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return ProjectCommitTrigger{}, fmt.Errorf("store: upsert project commit trigger: %w", err)
	}
	return s.GetProjectCommitTrigger(ctx, params.ProjectID)
}

func (s *Store) GetProjectCommitTrigger(ctx context.Context, projectID string) (ProjectCommitTrigger, error) {
	if err := requireContext(ctx); err != nil {
		return ProjectCommitTrigger{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return ProjectCommitTrigger{}, err
	}
	projectID, err = normalizeRequiredText("commit trigger project ID", projectID)
	if err != nil {
		return ProjectCommitTrigger{}, err
	}
	item, err := scanProjectCommitTrigger(db.QueryRowContext(ctx, `
		SELECT `+commitTriggerColumns+`
		FROM project_commit_triggers
		WHERE project_id = ?
	`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectCommitTrigger{}, &ErrNotFound{Resource: "project commit trigger", Key: projectID}
	}
	if err != nil {
		return ProjectCommitTrigger{}, fmt.Errorf("store: get project commit trigger: %w", err)
	}
	return item, nil
}

func (s *Store) ListEnabledProjectCommitTriggers(ctx context.Context) ([]ProjectCommitTrigger, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT `+commitTriggerColumns+`
		FROM project_commit_triggers
		WHERE enabled = 1
		ORDER BY updated_at ASC, project_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("store: list enabled project commit triggers: %w", err)
	}
	defer rows.Close()
	items := make([]ProjectCommitTrigger, 0)
	for rows.Next() {
		item, scanErr := scanProjectCommitTrigger(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan project commit trigger: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate project commit triggers: %w", err)
	}
	return items, nil
}

func (s *Store) RecordProjectCommitTriggerCheck(ctx context.Context, projectID string, observedSHA *string, triggered bool, message *string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	projectID, err = normalizeRequiredText("commit trigger project ID", projectID)
	if err != nil {
		return err
	}
	if observedSHA, err = normalizeOptionalString("commit trigger observed SHA", observedSHA); err != nil {
		return err
	}
	if message, err = normalizeOptionalString("commit trigger error", message); err != nil {
		return err
	}
	now := nowUTC()
	result, err := db.ExecContext(ctx, `
		UPDATE project_commit_triggers
		SET last_commit_sha = COALESCE(?, last_commit_sha),
			last_checked_at = ?,
			last_triggered_at = CASE WHEN ? = 1 THEN ? ELSE last_triggered_at END,
			last_error = ?,
			updated_at = ?
		WHERE project_id = ?
	`, nullableString(observedSHA), now.UnixMilli(), boolToInteger(triggered), now.UnixMilli(), nullableString(message), now.UnixMilli(), projectID)
	if err != nil {
		return fmt.Errorf("store: record project commit trigger check: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected == 0 {
		return &ErrNotFound{Resource: "project commit trigger", Key: projectID}
	}
	return nil
}

func (s *Store) CommitTriggeredRunExists(ctx context.Context, workflowID, commitSHA string) (bool, error) {
	if err := requireContext(ctx); err != nil {
		return false, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return false, err
	}
	if workflowID, err = normalizeRequiredText("workflow ID", workflowID); err != nil {
		return false, err
	}
	if commitSHA, err = normalizeRequiredText("commit SHA", commitSHA); err != nil {
		return false, err
	}
	var present int
	err = db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM runs
			WHERE workflow_id = ? AND commit_sha = ? AND trigger_type = 'commit'
		)
	`, workflowID, commitSHA).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("store: check commit-triggered run: %w", err)
	}
	return present == 1, nil
}

func scanProjectCommitTrigger(scanner interface{ Scan(...any) error }) (ProjectCommitTrigger, error) {
	var item ProjectCommitTrigger
	var enabled int64
	var lastSHA, lastError sql.NullString
	var lastChecked, lastTriggered sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(
		&item.ProjectID, &item.Ref, &enabled, &lastSHA, &lastChecked,
		&lastTriggered, &lastError, &createdAt, &updatedAt,
	); err != nil {
		return ProjectCommitTrigger{}, err
	}
	item.Enabled = enabled == 1
	if lastSHA.Valid {
		item.LastCommitSHA = &lastSHA.String
	}
	if lastChecked.Valid {
		value := time.UnixMilli(lastChecked.Int64).UTC()
		item.LastCheckedAt = &value
	}
	if lastTriggered.Valid {
		value := time.UnixMilli(lastTriggered.Int64).UTC()
		item.LastTriggeredAt = &value
	}
	if lastError.Valid {
		item.LastError = &lastError.String
	}
	item.CreatedAt = time.UnixMilli(createdAt).UTC()
	item.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return item, nil
}
