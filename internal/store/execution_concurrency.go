package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ExecutionConcurrencyScope string

const (
	ExecutionConcurrencyWorkflow ExecutionConcurrencyScope = "workflow"
	ExecutionConcurrencyJob      ExecutionConcurrencyScope = "job"
)

type ExecutionConcurrencyLease struct {
	Scope       ExecutionConcurrencyScope `json:"scope"`
	Group       string                    `json:"group"`
	RunID       string                    `json:"runId"`
	HolderID    string                    `json:"holderId"`
	OwnerID     string                    `json:"ownerId"`
	AcquiredAt  time.Time                 `json:"acquiredAt"`
	HeartbeatAt time.Time                 `json:"heartbeatAt"`
	ExpiresAt   time.Time                 `json:"expiresAt"`
}

type AcquireExecutionConcurrencyParams struct {
	Scope    ExecutionConcurrencyScope
	Group    string
	RunID    string
	HolderID string
	OwnerID  string
	TTL      time.Duration
	Now      time.Time
}

type AcquireExecutionConcurrencyResult struct {
	Lease    ExecutionConcurrencyLease `json:"lease"`
	Acquired bool                      `json:"acquired"`
}

const executionConcurrencyColumns = `scope, group_key, run_id, holder_id, owner_id, acquired_at, heartbeat_at, expires_at`

func (s *Store) AcquireExecutionConcurrency(ctx context.Context, params AcquireExecutionConcurrencyParams) (AcquireExecutionConcurrencyResult, error) {
	if err := requireContext(ctx); err != nil {
		return AcquireExecutionConcurrencyResult{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return AcquireExecutionConcurrencyResult{}, err
	}
	params, err = normalizeExecutionConcurrencyParams(params)
	if err != nil {
		return AcquireExecutionConcurrencyResult{}, err
	}
	expiresAt := params.Now.Add(params.TTL)
	_, err = db.ExecContext(ctx, `
		INSERT INTO execution_concurrency_leases (
			scope, group_key, run_id, holder_id, owner_id, acquired_at, heartbeat_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, group_key) DO UPDATE SET
			run_id = excluded.run_id,
			holder_id = excluded.holder_id,
			owner_id = excluded.owner_id,
			acquired_at = CASE
				WHEN execution_concurrency_leases.holder_id = excluded.holder_id
				 AND execution_concurrency_leases.owner_id = excluded.owner_id
				THEN execution_concurrency_leases.acquired_at ELSE excluded.acquired_at END,
			heartbeat_at = excluded.heartbeat_at,
			expires_at = excluded.expires_at
		WHERE execution_concurrency_leases.expires_at <= excluded.heartbeat_at
		   OR (execution_concurrency_leases.holder_id = excluded.holder_id
		       AND execution_concurrency_leases.owner_id = excluded.owner_id)
	`, params.Scope, params.Group, params.RunID, params.HolderID, params.OwnerID,
		params.Now.UnixMilli(), params.Now.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return AcquireExecutionConcurrencyResult{}, fmt.Errorf("store: acquire execution concurrency: %w", err)
	}
	lease, err := scanExecutionConcurrencyLease(db.QueryRowContext(ctx, `
		SELECT `+executionConcurrencyColumns+` FROM execution_concurrency_leases
		WHERE scope = ? AND group_key = ?
	`, params.Scope, params.Group))
	if err != nil {
		return AcquireExecutionConcurrencyResult{}, fmt.Errorf("store: read execution concurrency: %w", err)
	}
	return AcquireExecutionConcurrencyResult{
		Lease: lease, Acquired: lease.HolderID == params.HolderID && lease.OwnerID == params.OwnerID,
	}, nil
}

func (s *Store) ReleaseExecutionConcurrency(ctx context.Context, scope ExecutionConcurrencyScope, group, holderID, ownerID string) (bool, error) {
	if err := requireContext(ctx); err != nil {
		return false, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return false, err
	}
	if !validExecutionConcurrencyScope(scope) {
		return false, invalidInput("execution concurrency scope", "must be workflow or job")
	}
	group = strings.TrimSpace(strings.ToLower(group))
	holderID = strings.TrimSpace(holderID)
	ownerID = strings.TrimSpace(ownerID)
	if group == "" || holderID == "" || ownerID == "" {
		return false, invalidInput("execution concurrency release", "group, holder ID, and owner ID are required")
	}
	result, err := db.ExecContext(ctx, `
		DELETE FROM execution_concurrency_leases
		WHERE scope = ? AND group_key = ? AND holder_id = ? AND owner_id = ?
	`, scope, group, holderID, ownerID)
	if err != nil {
		return false, fmt.Errorf("store: release execution concurrency: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: count released execution concurrency: %w", err)
	}
	return deleted == 1, nil
}

func (s *Store) GetExecutionConcurrency(ctx context.Context, scope ExecutionConcurrencyScope, group string) (*ExecutionConcurrencyLease, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if !validExecutionConcurrencyScope(scope) {
		return nil, invalidInput("execution concurrency scope", "must be workflow or job")
	}
	group = strings.TrimSpace(strings.ToLower(group))
	if group == "" {
		return nil, invalidInput("execution concurrency group", "must not be empty")
	}
	lease, err := scanExecutionConcurrencyLease(s.db.QueryRowContext(ctx, `
		SELECT `+executionConcurrencyColumns+` FROM execution_concurrency_leases
		WHERE scope = ? AND group_key = ?
	`, scope, group))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get execution concurrency: %w", err)
	}
	return &lease, nil
}

func normalizeExecutionConcurrencyParams(params AcquireExecutionConcurrencyParams) (AcquireExecutionConcurrencyParams, error) {
	if !validExecutionConcurrencyScope(params.Scope) {
		return AcquireExecutionConcurrencyParams{}, invalidInput("execution concurrency scope", "must be workflow or job")
	}
	params.Group = strings.TrimSpace(strings.ToLower(params.Group))
	if params.Group == "" || len(params.Group) > 512 {
		return AcquireExecutionConcurrencyParams{}, invalidInput("execution concurrency group", "must contain between 1 and 512 bytes")
	}
	var err error
	if params.RunID, err = normalizeRequiredText("execution concurrency run ID", params.RunID); err != nil {
		return AcquireExecutionConcurrencyParams{}, err
	}
	if params.HolderID, err = normalizeRequiredText("execution concurrency holder ID", params.HolderID); err != nil {
		return AcquireExecutionConcurrencyParams{}, err
	}
	if params.OwnerID, err = normalizeRequiredText("execution concurrency owner ID", params.OwnerID); err != nil {
		return AcquireExecutionConcurrencyParams{}, err
	}
	if params.TTL < time.Second || params.TTL > 24*time.Hour {
		return AcquireExecutionConcurrencyParams{}, invalidInput("execution concurrency TTL", "must be between one second and 24 hours")
	}
	if params.Now.IsZero() {
		params.Now = nowUTC()
	} else {
		params.Now = params.Now.UTC()
	}
	return params, nil
}

func validExecutionConcurrencyScope(scope ExecutionConcurrencyScope) bool {
	return scope == ExecutionConcurrencyWorkflow || scope == ExecutionConcurrencyJob
}

func scanExecutionConcurrencyLease(scanner executionScanner) (ExecutionConcurrencyLease, error) {
	var lease ExecutionConcurrencyLease
	var acquiredAt, heartbeatAt, expiresAt int64
	if err := scanner.Scan(&lease.Scope, &lease.Group, &lease.RunID, &lease.HolderID, &lease.OwnerID, &acquiredAt, &heartbeatAt, &expiresAt); err != nil {
		return ExecutionConcurrencyLease{}, err
	}
	lease.AcquiredAt = timeFromMillis(acquiredAt)
	lease.HeartbeatAt = timeFromMillis(heartbeatAt)
	lease.ExpiresAt = timeFromMillis(expiresAt)
	return lease, nil
}
