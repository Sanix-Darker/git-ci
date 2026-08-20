package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type JobWaitReason string

const (
	JobWaitApproval    JobWaitReason = "approval"
	JobWaitTimer       JobWaitReason = "timer"
	JobWaitConcurrency JobWaitReason = "concurrency"
)

type JobWait struct {
	JobID       string        `json:"jobId"`
	RunID       string        `json:"runId"`
	Reason      JobWaitReason `json:"reason"`
	Detail      *string       `json:"detail,omitempty"`
	AvailableAt *time.Time    `json:"availableAt,omitempty"`
	CreatedAt   time.Time     `json:"createdAt"`
	UpdatedAt   time.Time     `json:"updatedAt"`
}

type PauseJobParams struct {
	RunID       string
	JobID       string
	Reason      JobWaitReason
	Detail      string
	AvailableAt *time.Time
}

type RecoveryResult struct {
	RequeuedRuns int `json:"requeuedRuns"`
	FailedRuns   int `json:"failedRuns"`
}

const jobWaitColumns = `job_id, run_id, reason, detail, available_at, created_at, updated_at`

func (s *Store) PauseJob(ctx context.Context, params PauseJobParams) (JobWait, error) {
	if err := requireContext(ctx); err != nil {
		return JobWait{}, err
	}
	var err error
	params.RunID, err = normalizeRequiredText("job wait run ID", params.RunID)
	if err != nil {
		return JobWait{}, err
	}
	params.JobID, err = normalizeRequiredText("job wait job ID", params.JobID)
	if err != nil {
		return JobWait{}, err
	}
	switch params.Reason {
	case JobWaitApproval, JobWaitTimer, JobWaitConcurrency:
	default:
		return JobWait{}, invalidInput("job wait reason", "must be approval, timer, or concurrency")
	}
	params.Detail = strings.TrimSpace(params.Detail)
	if params.AvailableAt != nil {
		value := params.AvailableAt.UTC()
		params.AvailableAt = &value
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JobWait{}, fmt.Errorf("store: begin job wait: %w", err)
	}
	defer tx.Rollback()
	var runStatus, jobStatus Status
	err = tx.QueryRowContext(ctx, `
		SELECT runs.status, jobs.status
		FROM jobs JOIN runs ON runs.id = jobs.run_id
		WHERE jobs.id = ? AND runs.id = ?
	`, params.JobID, params.RunID).Scan(&runStatus, &jobStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return JobWait{}, &ErrNotFound{Resource: "run job", Key: params.RunID + "/" + params.JobID}
	}
	if err != nil {
		return JobWait{}, fmt.Errorf("store: read job wait state: %w", err)
	}
	if runStatus != StatusRunning || (jobStatus != StatusQueued && jobStatus != StatusWaiting) {
		return JobWait{}, &ErrInvalidStatusTransition{Resource: "job wait", ID: params.JobID, From: jobStatus, To: StatusWaiting}
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`, StatusWaiting, now.UnixMilli(), params.JobID); err != nil {
		return JobWait{}, fmt.Errorf("store: pause job: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = ?, worker_id = NULL, claimed_at = NULL, updated_at = ? WHERE id = ?
	`, StatusWaiting, now.UnixMilli(), params.RunID); err != nil {
		return JobWait{}, fmt.Errorf("store: pause run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO job_waits (job_id, run_id, reason, detail, available_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET reason = excluded.reason, detail = excluded.detail,
			available_at = excluded.available_at, updated_at = excluded.updated_at
	`, params.JobID, params.RunID, params.Reason, nullableText(params.Detail), nullableJobWaitTime(params.AvailableAt), now.UnixMilli(), now.UnixMilli()); err != nil {
		return JobWait{}, fmt.Errorf("store: record job wait: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_worker_leases WHERE run_id = ?`, params.RunID); err != nil {
		return JobWait{}, fmt.Errorf("store: release paused run lease: %w", err)
	}
	wait, err := scanJobWait(tx.QueryRowContext(ctx, `SELECT `+jobWaitColumns+` FROM job_waits WHERE job_id = ?`, params.JobID))
	if err != nil {
		return JobWait{}, fmt.Errorf("store: reload job wait: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return JobWait{}, fmt.Errorf("store: commit job wait: %w", err)
	}
	return wait, nil
}

func (s *Store) ListJobWaits(ctx context.Context) ([]JobWait, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobWaitColumns+` FROM job_waits ORDER BY updated_at ASC, job_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list job waits: %w", err)
	}
	defer rows.Close()
	waits := make([]JobWait, 0)
	for rows.Next() {
		wait, scanErr := scanJobWait(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan job wait: %w", scanErr)
		}
		waits = append(waits, wait)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate job waits: %w", err)
	}
	return waits, nil
}

func (s *Store) ResumeJob(ctx context.Context, runID, jobID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	var err error
	runID, err = normalizeRequiredText("resume run ID", runID)
	if err != nil {
		return err
	}
	jobID, err = normalizeRequiredText("resume job ID", jobID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin job resume: %w", err)
	}
	defer tx.Rollback()
	now := nowUTC()
	result, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, updated_at = ? WHERE id = ? AND run_id = ? AND status = ?`, StatusQueued, now.UnixMilli(), jobID, runID, StatusWaiting)
	if err != nil {
		return fmt.Errorf("store: resume job: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return &ErrInvalidStatusTransition{Resource: "job resume", ID: jobID, From: StatusWaiting, To: StatusQueued}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = ?, updated_at = ? WHERE id = ? AND status = ?
	`, StatusQueued, now.UnixMilli(), runID, StatusWaiting); err != nil {
		return fmt.Errorf("store: resume run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM job_waits WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("store: delete resumed job wait: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit job resume: %w", err)
	}
	return nil
}

func (s *Store) HeartbeatRunWorker(ctx context.Context, runID, workerID string, now time.Time, ttl time.Duration) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	var err error
	runID, err = normalizeRequiredText("run worker lease run ID", runID)
	if err != nil {
		return err
	}
	workerID, err = normalizeRequiredText("run worker lease worker ID", workerID)
	if err != nil {
		return err
	}
	if ttl < time.Second || ttl > 24*time.Hour {
		return invalidInput("run worker lease TTL", "must be between one second and 24 hours")
	}
	if now.IsZero() {
		now = nowUTC()
	} else {
		now = now.UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO run_worker_leases (run_id, worker_id, heartbeat_at, expires_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET worker_id = excluded.worker_id,
			heartbeat_at = excluded.heartbeat_at, expires_at = excluded.expires_at
		WHERE run_worker_leases.worker_id = excluded.worker_id
	`, runID, workerID, now.UnixMilli(), now.Add(ttl).UnixMilli())
	if err != nil {
		return fmt.Errorf("store: heartbeat run worker: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return fmt.Errorf("store: run worker lease is owned by another worker")
	}
	return nil
}

func (s *Store) ReleaseRunWorker(ctx context.Context, runID, workerID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM run_worker_leases WHERE run_id = ? AND worker_id = ?`, strings.TrimSpace(runID), strings.TrimSpace(workerID))
	if err != nil {
		return fmt.Errorf("store: release run worker: %w", err)
	}
	return nil
}

func (s *Store) RecoverExpiredRunWorkers(ctx context.Context, now, orphanBefore time.Time) (RecoveryResult, error) {
	if err := requireContext(ctx); err != nil {
		return RecoveryResult{}, err
	}
	if now.IsZero() {
		now = nowUTC()
	}
	if orphanBefore.IsZero() {
		orphanBefore = now.Add(-time.Minute)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("store: begin worker recovery: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT runs.id
		FROM runs LEFT JOIN run_worker_leases ON run_worker_leases.run_id = runs.id
		WHERE runs.status = ? AND (
			run_worker_leases.expires_at <= ? OR
			(run_worker_leases.run_id IS NULL AND runs.claimed_at <= ?)
		)
		ORDER BY runs.created_at ASC, runs.id ASC
	`, StatusRunning, now.UTC().UnixMilli(), orphanBefore.UTC().UnixMilli())
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("store: list expired workers: %w", err)
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return RecoveryResult{}, fmt.Errorf("store: scan expired worker: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Close(); err != nil {
		return RecoveryResult{}, fmt.Errorf("store: close expired workers: %w", err)
	}
	result := RecoveryResult{}
	for _, runID := range runIDs {
		var activeSteps int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM steps JOIN jobs ON jobs.id = steps.job_id
			WHERE jobs.run_id = ? AND steps.status = ?
		`, runID, StatusRunning).Scan(&activeSteps); err != nil {
			return RecoveryResult{}, fmt.Errorf("store: count active recovery steps: %w", err)
		}
		if activeSteps > 0 {
			if err := failInterruptedRun(ctx, tx, runID, now); err != nil {
				return RecoveryResult{}, err
			}
			result.FailedRuns++
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, started_at = NULL, updated_at = ? WHERE run_id = ? AND status = ?`, StatusQueued, now.UnixMilli(), runID, StatusRunning); err != nil {
				return RecoveryResult{}, fmt.Errorf("store: requeue interrupted jobs: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, worker_id = NULL, claimed_at = NULL, updated_at = ? WHERE id = ?`, StatusQueued, now.UnixMilli(), runID); err != nil {
				return RecoveryResult{}, fmt.Errorf("store: requeue interrupted run: %w", err)
			}
			result.RequeuedRuns++
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_worker_leases WHERE run_id = ?`, runID); err != nil {
			return RecoveryResult{}, fmt.Errorf("store: clear expired worker lease: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return RecoveryResult{}, fmt.Errorf("store: commit worker recovery: %w", err)
	}
	return result, nil
}

func failInterruptedRun(ctx context.Context, tx *sql.Tx, runID string, now time.Time) error {
	const failureReason = "worker lease expired during active step; command was not replayed"
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps SET status = CASE WHEN status = ? THEN ? ELSE ? END,
			finished_at = ?, updated_at = ?
		WHERE job_id IN (SELECT id FROM jobs WHERE run_id = ?) AND status IN (?, ?)
	`, StatusRunning, StatusFailed, StatusSkipped, now.UnixMilli(), now.UnixMilli(), runID, StatusRunning, StatusQueued); err != nil {
		return fmt.Errorf("store: fail interrupted steps: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = CASE WHEN status = ? THEN ? ELSE ? END,
			finished_at = ?, updated_at = ? WHERE run_id = ? AND status IN (?, ?)
	`, StatusRunning, StatusFailed, StatusSkipped, now.UnixMilli(), now.UnixMilli(), runID, StatusRunning, StatusQueued); err != nil {
		return fmt.Errorf("store: fail interrupted jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = ?, finished_at = ?, failure_reason = ?, updated_at = ? WHERE id = ?
	`, StatusFailed, now.UnixMilli(), failureReason, now.UnixMilli(), runID); err != nil {
		return fmt.Errorf("store: fail interrupted run: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM deployments WHERE run_id = ? AND status = ?`, runID, StatusRunning)
	if err != nil {
		return fmt.Errorf("store: list interrupted deployments: %w", err)
	}
	deploymentIDs := make([]string, 0)
	for rows.Next() {
		var deploymentID string
		if err := rows.Scan(&deploymentID); err != nil {
			rows.Close()
			return fmt.Errorf("store: scan interrupted deployment: %w", err)
		}
		deploymentIDs = append(deploymentIDs, deploymentID)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close interrupted deployments: %w", err)
	}
	for _, deploymentID := range deploymentIDs {
		eventID, err := randomOpaqueID()
		if err != nil {
			return fmt.Errorf("store: generate interrupted deployment event ID: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status = ?, finished_at = ?, updated_at = ? WHERE id = ?`, StatusFailed, now.UnixMilli(), now.UnixMilli(), deploymentID); err != nil {
			return fmt.Errorf("store: fail interrupted deployment: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_events (id, deployment_id, status, reason, created_at) VALUES (?, ?, ?, ?, ?)`, eventID, deploymentID, StatusFailed, failureReason, now.UnixMilli()); err != nil {
			return fmt.Errorf("store: record interrupted deployment: %w", err)
		}
	}
	return nil
}

func scanJobWait(scanner executionScanner) (JobWait, error) {
	var wait JobWait
	var detail sql.NullString
	var availableAt sql.NullInt64
	var createdAt, updatedAt int64
	if err := scanner.Scan(&wait.JobID, &wait.RunID, &wait.Reason, &detail, &availableAt, &createdAt, &updatedAt); err != nil {
		return JobWait{}, err
	}
	wait.Detail = nullStringPointer(detail)
	wait.AvailableAt = nullTimePointer(availableAt)
	wait.CreatedAt = timeFromMillis(createdAt)
	wait.UpdatedAt = timeFromMillis(updatedAt)
	return wait, nil
}

func nullableJobWaitTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMilli()
}
