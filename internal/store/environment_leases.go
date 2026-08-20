package store

import (
	"context"
	"fmt"
	"time"
)

type EnvironmentLease struct {
	EnvironmentID string    `json:"environmentId"`
	RunID         string    `json:"runId"`
	JobID         string    `json:"jobId"`
	OwnerID       string    `json:"ownerId"`
	AcquiredAt    time.Time `json:"acquiredAt"`
	HeartbeatAt   time.Time `json:"heartbeatAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type AcquireEnvironmentLeaseParams struct {
	JobID   string
	OwnerID string
	TTL     time.Duration
	Now     time.Time
}

type AcquireEnvironmentLeaseResult struct {
	Lease    EnvironmentLease `json:"lease"`
	Acquired bool             `json:"acquired"`
}

const environmentLeaseColumns = `environment_id, run_id, job_id, owner_id, acquired_at, heartbeat_at, expires_at`

func (s *Store) AcquireEnvironmentLease(ctx context.Context, params AcquireEnvironmentLeaseParams) (AcquireEnvironmentLeaseResult, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return AcquireEnvironmentLeaseResult{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return AcquireEnvironmentLeaseResult{}, err
	}
	params.JobID, err = normalizeRequiredText("environment lease job ID", params.JobID)
	if err != nil {
		return AcquireEnvironmentLeaseResult{}, err
	}
	params.OwnerID, err = normalizeRequiredText("environment lease owner ID", params.OwnerID)
	if err != nil {
		return AcquireEnvironmentLeaseResult{}, err
	}
	if params.TTL < time.Second || params.TTL > 24*time.Hour {
		return AcquireEnvironmentLeaseResult{}, invalidInput("environment lease TTL", "must be between one second and 24 hours")
	}
	if params.Now.IsZero() {
		params.Now = nowUTC()
	} else {
		params.Now = params.Now.UTC()
	}
	access, err := s.EvaluateEnvironmentAccess(ctx, params.JobID, params.Now)
	if err != nil {
		return AcquireEnvironmentLeaseResult{}, err
	}
	if !access.Ready {
		return AcquireEnvironmentLeaseResult{}, invalidInput("environment lease", "protection is not satisfied: "+access.Reason)
	}

	environmentID, runID := access.Environment.ID, access.RunID
	expiresAt := params.Now.Add(params.TTL)
	_, err = db.ExecContext(ctx, `
		INSERT INTO environment_leases (
			environment_id, run_id, job_id, owner_id, acquired_at, heartbeat_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(environment_id) DO UPDATE SET
			run_id = excluded.run_id,
			job_id = excluded.job_id,
			owner_id = excluded.owner_id,
			acquired_at = CASE
				WHEN environment_leases.job_id = excluded.job_id AND environment_leases.owner_id = excluded.owner_id
				THEN environment_leases.acquired_at ELSE excluded.acquired_at END,
			heartbeat_at = excluded.heartbeat_at,
			expires_at = excluded.expires_at
		WHERE environment_leases.expires_at <= excluded.heartbeat_at
		   OR (environment_leases.job_id = excluded.job_id AND environment_leases.owner_id = excluded.owner_id)
	`, environmentID, runID, params.JobID, params.OwnerID, params.Now.UnixMilli(), params.Now.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return AcquireEnvironmentLeaseResult{}, fmt.Errorf("store: acquire environment lease: %w", err)
	}
	lease, err := scanEnvironmentLease(db.QueryRowContext(ctx, `SELECT `+environmentLeaseColumns+` FROM environment_leases WHERE environment_id = ?`, environmentID))
	if err != nil {
		return AcquireEnvironmentLeaseResult{}, fmt.Errorf("store: read environment lease: %w", err)
	}
	return AcquireEnvironmentLeaseResult{
		Lease:    lease,
		Acquired: lease.JobID == params.JobID && lease.OwnerID == params.OwnerID,
	}, nil
}

func (s *Store) ReleaseEnvironmentLease(ctx context.Context, environmentID, jobID, ownerID string) (bool, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return false, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return false, err
	}
	environmentID, err = normalizeRequiredText("environment lease environment ID", environmentID)
	if err != nil {
		return false, err
	}
	jobID, err = normalizeRequiredText("environment lease job ID", jobID)
	if err != nil {
		return false, err
	}
	ownerID, err = normalizeRequiredText("environment lease owner ID", ownerID)
	if err != nil {
		return false, err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM environment_leases WHERE environment_id = ? AND job_id = ? AND owner_id = ?`, environmentID, jobID, ownerID)
	if err != nil {
		return false, fmt.Errorf("store: release environment lease: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: count released environment lease: %w", err)
	}
	return deleted == 1, nil
}

func scanEnvironmentLease(scanner configurationScanner) (EnvironmentLease, error) {
	var lease EnvironmentLease
	var acquiredAt, heartbeatAt, expiresAt int64
	if err := scanner.Scan(&lease.EnvironmentID, &lease.RunID, &lease.JobID, &lease.OwnerID, &acquiredAt, &heartbeatAt, &expiresAt); err != nil {
		return EnvironmentLease{}, err
	}
	lease.AcquiredAt = timeFromMillis(acquiredAt)
	lease.HeartbeatAt = timeFromMillis(heartbeatAt)
	lease.ExpiresAt = timeFromMillis(expiresAt)
	return lease, nil
}
