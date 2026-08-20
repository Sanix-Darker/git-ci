package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DeploymentTier string

const (
	DeploymentTierProduction  DeploymentTier = "production"
	DeploymentTierStaging     DeploymentTier = "staging"
	DeploymentTierTesting     DeploymentTier = "testing"
	DeploymentTierDevelopment DeploymentTier = "development"
	DeploymentTierOther       DeploymentTier = "other"
)

type DeploymentTarget struct {
	JobID          string         `json:"jobId"`
	RunID          string         `json:"runId"`
	JobKey         string         `json:"jobKey"`
	Environment    string         `json:"environment"`
	DeploymentTier DeploymentTier `json:"deploymentTier"`
	CreatedAt      time.Time      `json:"createdAt"`
}

func (s *Store) GetDeploymentTargetForJob(ctx context.Context, jobID string) (DeploymentTarget, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return DeploymentTarget{}, invalidInput("deployment target job ID", "must not be empty")
	}
	target, err := scanDeploymentTarget(s.db.QueryRowContext(ctx, `
		SELECT job_id, run_id, job_key, environment, deployment_tier, created_at
		FROM deployment_targets
		WHERE job_id = ?
	`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return DeploymentTarget{}, &ErrNotFound{Resource: "deployment target", Key: jobID}
	}
	if err != nil {
		return DeploymentTarget{}, fmt.Errorf("store: get deployment target: %w", err)
	}
	return target, nil
}

func (s *Store) ListDeploymentTargets(ctx context.Context, runID string) ([]DeploymentTarget, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, invalidInput("deployment target run ID", "must not be empty")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, run_id, job_key, environment, deployment_tier, created_at
		FROM deployment_targets
		WHERE run_id = ?
		ORDER BY job_key ASC, job_id ASC
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list deployment targets: %w", err)
	}
	defer rows.Close()

	targets := make([]DeploymentTarget, 0)
	for rows.Next() {
		target, scanErr := scanDeploymentTarget(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan deployment target: %w", scanErr)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate deployment targets: %w", err)
	}
	return targets, nil
}

func normalizeEnqueueJobDeployment(job *EnqueueJob) error {
	var err error
	job.EnvironmentName, err = normalizeOptionalText("run job deployment environment", job.EnvironmentName)
	if err != nil {
		return err
	}
	if job.EnvironmentName == "" {
		if strings.TrimSpace(job.DeploymentTier) != "" {
			return invalidInput("run job deployment tier", "requires a deployment environment")
		}
		return nil
	}

	tier := DeploymentTier(strings.ToLower(strings.TrimSpace(job.DeploymentTier)))
	if tier == "" {
		tier = DeploymentTierOther
	}
	switch tier {
	case DeploymentTierProduction, DeploymentTierStaging, DeploymentTierTesting, DeploymentTierDevelopment, DeploymentTierOther:
		job.DeploymentTier = string(tier)
		return nil
	default:
		return invalidInput("run job deployment tier", "must be production, staging, testing, development, or other")
	}
}

func insertDeploymentTarget(ctx context.Context, transaction *sql.Tx, runID, jobID string, job EnqueueJob, now time.Time) error {
	if job.EnvironmentName == "" {
		return nil
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO deployment_targets (
			job_id, run_id, job_key, environment, deployment_tier, created_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, jobID, runID, job.Key, job.EnvironmentName, job.DeploymentTier, now.UnixMilli()); err != nil {
		return fmt.Errorf("store: insert deployment target snapshot: %w", err)
	}
	return nil
}

type deploymentTargetScanner interface {
	Scan(dest ...any) error
}

func scanDeploymentTarget(scanner deploymentTargetScanner) (DeploymentTarget, error) {
	var target DeploymentTarget
	var createdAt int64
	if err := scanner.Scan(&target.JobID, &target.RunID, &target.JobKey, &target.Environment, &target.DeploymentTier, &createdAt); err != nil {
		return DeploymentTarget{}, err
	}
	target.CreatedAt = timeFromMillis(createdAt)
	return target, nil
}
