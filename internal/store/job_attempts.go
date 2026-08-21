package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type JobAttempt struct {
	ID            string     `json:"id"`
	JobID         string     `json:"jobId"`
	RunID         string     `json:"runId"`
	AttemptNumber int        `json:"attemptNumber"`
	Status        Status     `json:"status"`
	FailureKind   string     `json:"failureKind,omitempty"`
	ExitCode      *int       `json:"exitCode,omitempty"`
	Message       string     `json:"message,omitempty"`
	WillRetry     bool       `json:"willRetry"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

type FinishJobAttemptParams struct {
	AttemptID, FailureKind, Message string
	Status                          Status
	ExitCode                        *int
	WillRetry                       bool
}

func (s *Store) StartJobAttempt(ctx context.Context, jobID string) (JobAttempt, error) {
	var lastErr error
	for retry := 0; retry < 50; retry++ {
		attempt, err := s.startJobAttempt(ctx, jobID)
		if err == nil || !isSQLiteBusy(err) {
			return attempt, err
		}
		lastErr = err
		if err := waitForJobAttemptRetry(ctx, retry); err != nil {
			return JobAttempt{}, err
		}
	}
	return JobAttempt{}, lastErr
}

func (s *Store) startJobAttempt(ctx context.Context, jobID string) (JobAttempt, error) {
	if err := requireContext(ctx); err != nil {
		return JobAttempt{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return JobAttempt{}, err
	}
	jobID, err = normalizeRequiredText("job attempt job ID", jobID)
	if err != nil {
		return JobAttempt{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return JobAttempt{}, fmt.Errorf("store: begin job attempt: %w", err)
	}
	defer tx.Rollback()
	var runID string
	var status Status
	if err := tx.QueryRowContext(ctx, `SELECT run_id, status FROM jobs WHERE id = ?`, jobID).Scan(&runID, &status); errors.Is(err, sql.ErrNoRows) {
		return JobAttempt{}, &ErrNotFound{Resource: "job", Key: jobID}
	} else if err != nil {
		return JobAttempt{}, fmt.Errorf("store: load attempted job: %w", err)
	}
	if status != StatusQueued {
		return JobAttempt{}, &ErrConflict{Resource: "job", Field: "status", Value: jobID}
	}
	var number int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM job_attempts WHERE job_id = ?`, jobID).Scan(&number); err != nil {
		return JobAttempt{}, fmt.Errorf("store: number job attempt: %w", err)
	}
	id, err := randomOpaqueID()
	if err != nil {
		return JobAttempt{}, fmt.Errorf("store: generate job attempt ID: %w", err)
	}
	now := nowUTC()
	attempt := JobAttempt{ID: id, JobID: jobID, RunID: runID, AttemptNumber: number, Status: StatusRunning, StartedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO job_attempts (id, job_id, run_id, attempt_number, status, started_at) VALUES (?, ?, ?, ?, ?, ?)`, attempt.ID, attempt.JobID, attempt.RunID, attempt.AttemptNumber, attempt.Status, now.UnixMilli()); err != nil {
		return JobAttempt{}, fmt.Errorf("store: insert job attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return JobAttempt{}, fmt.Errorf("store: commit job attempt: %w", err)
	}
	return attempt, nil
}

func (s *Store) FinishJobAttempt(ctx context.Context, params FinishJobAttemptParams) (JobAttempt, error) {
	var lastErr error
	for retry := 0; retry < 50; retry++ {
		attempt, err := s.finishJobAttempt(ctx, params)
		if err == nil || !isSQLiteBusy(err) {
			return attempt, err
		}
		lastErr = err
		if err := waitForJobAttemptRetry(ctx, retry); err != nil {
			return JobAttempt{}, err
		}
	}
	return JobAttempt{}, lastErr
}

func (s *Store) finishJobAttempt(ctx context.Context, params FinishJobAttemptParams) (JobAttempt, error) {
	if err := requireContext(ctx); err != nil {
		return JobAttempt{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return JobAttempt{}, err
	}
	params.AttemptID, err = normalizeRequiredText("job attempt ID", params.AttemptID)
	if err != nil {
		return JobAttempt{}, err
	}
	if params.Status != StatusSucceeded && params.Status != StatusFailed && params.Status != StatusCancelled && params.Status != StatusSkipped {
		return JobAttempt{}, invalidInput("job attempt status", "must be terminal")
	}
	params.FailureKind = strings.TrimSpace(params.FailureKind)
	params.Message = strings.TrimSpace(params.Message)
	now := nowUTC()
	attempt, err := scanJobAttempt(db.QueryRowContext(ctx, `
		UPDATE job_attempts
		SET status = ?, failure_kind = ?, exit_code = ?, message = ?, will_retry = ?, finished_at = ?
		WHERE id = ? AND status = ?
		RETURNING id, job_id, run_id, attempt_number, status, failure_kind, exit_code, message, will_retry, started_at, finished_at
	`, params.Status, nullableText(params.FailureKind), nullableAttemptExitCode(params.ExitCode), nullableText(params.Message), params.WillRetry, now.UnixMilli(), params.AttemptID, StatusRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return JobAttempt{}, &ErrConflict{Resource: "job attempt", Field: "status", Value: params.AttemptID}
	}
	if err != nil {
		return JobAttempt{}, fmt.Errorf("store: finish job attempt: %w", err)
	}
	return attempt, nil
}

func (s *Store) ResetJobForRetry(ctx context.Context, jobID, attemptID string) error {
	var lastErr error
	for retry := 0; retry < 50; retry++ {
		err := s.resetJobForRetry(ctx, jobID, attemptID)
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		lastErr = err
		if err := waitForJobAttemptRetry(ctx, retry); err != nil {
			return err
		}
	}
	return lastErr
}

func (s *Store) resetJobForRetry(ctx context.Context, jobID, attemptID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin retry reset: %w", err)
	}
	defer tx.Rollback()
	var eligible int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_attempts WHERE id = ? AND job_id = ? AND status = ? AND will_retry = 1`, attemptID, jobID, StatusFailed).Scan(&eligible); err != nil {
		return fmt.Errorf("store: inspect retry attempt: %w", err)
	}
	if eligible != 1 {
		return &ErrConflict{Resource: "job attempt", Field: "retry", Value: attemptID}
	}
	now := nowUTC()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = NULL, finished_at = NULL, updated_at = ? WHERE id = ? AND status = ?`, StatusQueued, now.UnixMilli(), jobID, StatusFailed)
	if err != nil {
		return fmt.Errorf("store: queue retry job: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return &ErrConflict{Resource: "job", Field: "status", Value: jobID}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, started_at = NULL, finished_at = NULL, updated_at = ? WHERE job_id = ?`, StatusQueued, now.UnixMilli(), jobID); err != nil {
		return fmt.Errorf("store: reset retry steps: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit retry reset: %w", err)
	}
	return nil
}

func attachJobAttempts(ctx context.Context, db *sql.DB, graph *RunGraph) error {
	index := make(map[string]int, len(graph.Jobs))
	for i := range graph.Jobs {
		index[graph.Jobs[i].Job.ID] = i
	}
	rows, err := db.QueryContext(ctx, `SELECT id, job_id, run_id, attempt_number, status, failure_kind, exit_code, message, will_retry, started_at, finished_at FROM job_attempts WHERE run_id = ? ORDER BY job_id ASC, attempt_number ASC`, graph.Run.ID)
	if err != nil {
		return fmt.Errorf("store: list job attempts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		attempt, scanErr := scanJobAttempt(rows)
		if scanErr != nil {
			return fmt.Errorf("store: scan job attempt: %w", scanErr)
		}
		if i, ok := index[attempt.JobID]; ok {
			graph.Jobs[i].Job.Attempts = append(graph.Jobs[i].Job.Attempts, attempt)
		}
	}
	return rows.Err()
}

func scanJobAttempt(scanner executionScanner) (JobAttempt, error) {
	var item JobAttempt
	var failureKind, message sql.NullString
	var exitCode, finishedAt sql.NullInt64
	var willRetry int64
	var startedAt int64
	if err := scanner.Scan(&item.ID, &item.JobID, &item.RunID, &item.AttemptNumber, &item.Status, &failureKind, &exitCode, &message, &willRetry, &startedAt, &finishedAt); err != nil {
		return JobAttempt{}, err
	}
	item.FailureKind = failureKind.String
	item.Message = message.String
	item.WillRetry = willRetry != 0
	item.StartedAt = timeFromMillis(startedAt)
	if exitCode.Valid {
		value := int(exitCode.Int64)
		item.ExitCode = &value
	}
	if finishedAt.Valid {
		value := timeFromMillis(finishedAt.Int64)
		item.FinishedAt = &value
	}
	return item, nil
}

func nullableAttemptExitCode(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func waitForJobAttemptRetry(ctx context.Context, retry int) error {
	timer := time.NewTimer(time.Duration(retry+1) * 2 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
