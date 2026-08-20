package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) EnsureEnvironmentForJob(ctx context.Context, jobID string) (Environment, error) {
	jobID, err := normalizeRequiredText("deployment target job ID", jobID)
	if err != nil {
		return Environment{}, err
	}
	var projectID, name string
	var tier DeploymentTier
	err = s.db.QueryRowContext(ctx, `
		SELECT run.project_id, target.environment, target.deployment_tier
		FROM deployment_targets AS target JOIN runs AS run ON run.id = target.run_id
		WHERE target.job_id = ?
	`, jobID).Scan(&projectID, &name, &tier)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, &ErrNotFound{Resource: "deployment target", Key: jobID}
	}
	if err != nil {
		return Environment{}, fmt.Errorf("store: resolve target environment: %w", err)
	}
	environment, err := s.GetEnvironment(ctx, projectID, name)
	if err == nil {
		return environment, nil
	}
	var notFound *ErrNotFound
	if !errors.As(err, &notFound) {
		return Environment{}, err
	}
	id, err := randomOpaqueID()
	if err != nil {
		return Environment{}, fmt.Errorf("store: generate default environment ID: %w", err)
	}
	now := nowUTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO environments (
			id, project_id, name, deployment_tier, protected, required_approvals,
			wait_timer_seconds, allowed_refs_json, concurrency_mode, created_at, updated_at
		) VALUES (?, ?, ?, ?, 0, 0, 0, '[]', ?, ?, ?)
		ON CONFLICT(project_id, name) DO NOTHING
	`, id, projectID, name, tier, EnvironmentConcurrencyQueue, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Environment{}, fmt.Errorf("store: ensure target environment: %w", err)
	}
	return s.GetEnvironment(ctx, projectID, name)
}

func (s *Store) EnsureDeploymentForJob(ctx context.Context, jobID string) (Deployment, error) {
	jobID, err := normalizeRequiredText("deployment job ID", jobID)
	if err != nil {
		return Deployment{}, err
	}
	var existingID string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM deployments WHERE job_id = ?`, jobID).Scan(&existingID)
	if err == nil {
		return s.GetDeployment(ctx, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, fmt.Errorf("store: find job deployment: %w", err)
	}
	canonicalEnvironment, err := s.EnsureEnvironmentForJob(ctx, jobID)
	if err != nil {
		return Deployment{}, fmt.Errorf("store: ensure deployment environment: %w", err)
	}
	var projectID, runID, environment string
	err = s.db.QueryRowContext(ctx, `
		SELECT run.project_id, target.run_id, target.environment
		FROM deployment_targets AS target JOIN runs AS run ON run.id = target.run_id
		WHERE target.job_id = ?
	`, jobID).Scan(&projectID, &runID, &environment)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, &ErrNotFound{Resource: "deployment target", Key: jobID}
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("store: resolve job deployment: %w", err)
	}
	deploymentID, err := randomOpaqueID()
	if err != nil {
		return Deployment{}, fmt.Errorf("store: generate deployment ID: %w", err)
	}
	eventID, err := randomOpaqueID()
	if err != nil {
		return Deployment{}, fmt.Errorf("store: generate deployment event ID: %w", err)
	}
	now := nowUTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, fmt.Errorf("store: begin ensure deployment: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO deployments (id, project_id, run_id, environment, status, created_at, updated_at, job_id, deployment_tier)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, deploymentID, projectID, runID, environment, StatusQueued, now.UnixMilli(), now.UnixMilli(), jobID, canonicalEnvironment.DeploymentTier)
	if err != nil {
		return Deployment{}, fmt.Errorf("store: ensure deployment: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return Deployment{}, fmt.Errorf("store: count ensured deployment: %w", err)
	}
	if created == 1 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deployment_events (id, deployment_id, status, reason, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, eventID, deploymentID, StatusQueued, "workflow job targeted environment", now.UnixMilli()); err != nil {
			return Deployment{}, fmt.Errorf("store: record ensured deployment: %w", err)
		}
	} else if err := tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE job_id = ?`, jobID).Scan(&deploymentID); err != nil {
		return Deployment{}, fmt.Errorf("store: read concurrent deployment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, fmt.Errorf("store: commit ensured deployment: %w", err)
	}
	return s.GetDeployment(ctx, deploymentID)
}
