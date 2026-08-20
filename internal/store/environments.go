package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

type EnvironmentConcurrencyMode string

const (
	EnvironmentConcurrencyQueue            EnvironmentConcurrencyMode = "queue"
	EnvironmentConcurrencyCancelInProgress EnvironmentConcurrencyMode = "cancel_in_progress"
)

type Environment struct {
	ID                string                     `json:"id"`
	ProjectID         string                     `json:"projectId"`
	Name              string                     `json:"name"`
	DeploymentTier    DeploymentTier             `json:"deploymentTier"`
	Protected         bool                       `json:"protected"`
	RequiredApprovals int                        `json:"requiredApprovals"`
	WaitTimerSeconds  int                        `json:"waitTimerSeconds"`
	AllowedRefs       []string                   `json:"allowedRefs"`
	ConcurrencyMode   EnvironmentConcurrencyMode `json:"concurrencyMode"`
	CreatedAt         time.Time                  `json:"createdAt"`
	UpdatedAt         time.Time                  `json:"updatedAt"`
}

type EnvironmentAccess struct {
	Environment    Environment                `json:"environment"`
	ProjectID      string                     `json:"projectId"`
	RunID          string                     `json:"runId"`
	JobID          string                     `json:"jobId"`
	Ref            string                     `json:"ref"`
	ApprovalStatus *EnvironmentApprovalStatus `json:"approvalStatus,omitempty"`
	WaitUntil      *time.Time                 `json:"waitUntil,omitempty"`
	Ready          bool                       `json:"ready"`
	Reason         string                     `json:"reason,omitempty"`
}

type UpsertEnvironmentParams struct {
	ProjectID         string
	Name              string
	DeploymentTier    DeploymentTier
	Protected         bool
	RequiredApprovals int
	WaitTimerSeconds  int
	AllowedRefs       []string
	ConcurrencyMode   EnvironmentConcurrencyMode
}

const environmentColumns = `id, project_id, name, deployment_tier, protected, required_approvals, wait_timer_seconds, allowed_refs_json, concurrency_mode, created_at, updated_at`

func (s *Store) UpsertEnvironment(ctx context.Context, params UpsertEnvironmentParams) (Environment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Environment{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Environment{}, err
	}
	params, allowedRefs, err := normalizeEnvironmentParams(params)
	if err != nil {
		return Environment{}, err
	}
	id, err := randomOpaqueID()
	if err != nil {
		return Environment{}, fmt.Errorf("store: generate environment ID: %w", err)
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO environments (
			id, project_id, name, deployment_tier, protected, required_approvals,
			wait_timer_seconds, allowed_refs_json, concurrency_mode, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET
			deployment_tier = excluded.deployment_tier,
			protected = excluded.protected,
			required_approvals = excluded.required_approvals,
			wait_timer_seconds = excluded.wait_timer_seconds,
			allowed_refs_json = excluded.allowed_refs_json,
			concurrency_mode = excluded.concurrency_mode,
			updated_at = excluded.updated_at
	`, id, params.ProjectID, params.Name, params.DeploymentTier, params.Protected, params.RequiredApprovals,
		params.WaitTimerSeconds, allowedRefs, params.ConcurrencyMode, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Environment{}, fmt.Errorf("store: upsert environment: %w", err)
	}
	return s.GetEnvironment(ctx, params.ProjectID, params.Name)
}

func (s *Store) GetEnvironment(ctx context.Context, projectID, name string) (Environment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Environment{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Environment{}, err
	}
	projectID, err = normalizeRequiredText("environment project ID", projectID)
	if err != nil {
		return Environment{}, err
	}
	name, err = normalizeRequiredText("environment name", name)
	if err != nil {
		return Environment{}, err
	}
	environment, err := scanEnvironment(db.QueryRowContext(ctx, `SELECT `+environmentColumns+` FROM environments WHERE project_id = ? AND name = ?`, projectID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, &ErrNotFound{Resource: "environment", Key: projectID + "/" + name}
	}
	if err != nil {
		return Environment{}, fmt.Errorf("store: get environment: %w", err)
	}
	return environment, nil
}

func (s *Store) GetEnvironmentByID(ctx context.Context, environmentID string) (Environment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Environment{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Environment{}, err
	}
	environmentID, err = normalizeRequiredText("environment ID", environmentID)
	if err != nil {
		return Environment{}, err
	}
	environment, err := scanEnvironment(db.QueryRowContext(ctx, `SELECT `+environmentColumns+` FROM environments WHERE id = ?`, environmentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, &ErrNotFound{Resource: "environment", Key: environmentID}
	}
	if err != nil {
		return Environment{}, fmt.Errorf("store: get environment by ID: %w", err)
	}
	return environment, nil
}

func (s *Store) ListEnvironments(ctx context.Context, projectID string) ([]Environment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("environment project ID", projectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+environmentColumns+` FROM environments WHERE project_id = ? ORDER BY name ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list environments: %w", err)
	}
	defer rows.Close()
	environments := make([]Environment, 0)
	for rows.Next() {
		environment, scanErr := scanEnvironment(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan environment: %w", scanErr)
		}
		environments = append(environments, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate environments: %w", err)
	}
	return environments, nil
}

func EnvironmentAllowsRef(environment Environment, ref string) bool {
	if len(environment.AllowedRefs) == 0 {
		return true
	}
	ref = strings.TrimSpace(ref)
	for _, pattern := range environment.AllowedRefs {
		if pattern == ref {
			return true
		}
		matched, err := path.Match(pattern, ref)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func (s *Store) EvaluateEnvironmentAccess(ctx context.Context, jobID string, now time.Time) (EnvironmentAccess, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return EnvironmentAccess{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return EnvironmentAccess{}, err
	}
	jobID, err = normalizeRequiredText("environment access job ID", jobID)
	if err != nil {
		return EnvironmentAccess{}, err
	}
	if now.IsZero() {
		now = nowUTC()
	} else {
		now = now.UTC()
	}

	var access EnvironmentAccess
	var environmentName string
	var ref sql.NullString
	var runCreatedAt int64
	err = db.QueryRowContext(ctx, `
		SELECT target.run_id, target.job_id, run.project_id, target.environment, run.ref, run.created_at
		FROM deployment_targets AS target
		JOIN runs AS run ON run.id = target.run_id
		WHERE target.job_id = ?
	`, jobID).Scan(&access.RunID, &access.JobID, &access.ProjectID, &environmentName, &ref, &runCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentAccess{}, &ErrNotFound{Resource: "environment deployment target", Key: jobID}
	}
	if err != nil {
		return EnvironmentAccess{}, fmt.Errorf("store: resolve environment access target: %w", err)
	}
	access.Ref = ref.String
	access.Environment, err = s.GetEnvironment(ctx, access.ProjectID, environmentName)
	if err != nil {
		return EnvironmentAccess{}, err
	}
	if !access.Environment.Protected {
		access.Ready = true
		return access, nil
	}
	if !EnvironmentAllowsRef(access.Environment, access.Ref) {
		access.Reason = "ref_not_allowed"
		return access, nil
	}

	waitAnchor := timeFromMillis(runCreatedAt)
	if access.Environment.RequiredApprovals > 0 {
		var status EnvironmentApprovalStatus
		var requestedAt int64
		err = db.QueryRowContext(ctx, `
			SELECT status, requested_at FROM environment_approval_requests WHERE job_id = ?
		`, jobID).Scan(&status, &requestedAt)
		if errors.Is(err, sql.ErrNoRows) {
			access.Reason = "approval_required"
			return access, nil
		}
		if err != nil {
			return EnvironmentAccess{}, fmt.Errorf("store: evaluate environment approval: %w", err)
		}
		access.ApprovalStatus = &status
		waitAnchor = timeFromMillis(requestedAt)
		switch status {
		case EnvironmentApprovalApproved:
		case EnvironmentApprovalRejected:
			access.Reason = "approval_rejected"
			return access, nil
		case EnvironmentApprovalCancelled:
			access.Reason = "approval_cancelled"
			return access, nil
		default:
			access.Reason = "approval_required"
			return access, nil
		}
	}
	if access.Environment.WaitTimerSeconds > 0 {
		waitUntil := waitAnchor.Add(time.Duration(access.Environment.WaitTimerSeconds) * time.Second)
		access.WaitUntil = &waitUntil
		if now.Before(waitUntil) {
			access.Reason = "wait_timer"
			return access, nil
		}
	}
	access.Ready = true
	return access, nil
}

func normalizeEnvironmentParams(params UpsertEnvironmentParams) (UpsertEnvironmentParams, string, error) {
	var err error
	params.ProjectID, err = normalizeRequiredText("environment project ID", params.ProjectID)
	if err != nil {
		return UpsertEnvironmentParams{}, "", err
	}
	params.Name, err = normalizeRequiredText("environment name", params.Name)
	if err != nil {
		return UpsertEnvironmentParams{}, "", err
	}
	if params.DeploymentTier == "" {
		params.DeploymentTier = DeploymentTierOther
	}
	switch params.DeploymentTier {
	case DeploymentTierProduction, DeploymentTierStaging, DeploymentTierTesting, DeploymentTierDevelopment, DeploymentTierOther:
	default:
		return UpsertEnvironmentParams{}, "", invalidInput("environment deployment tier", "must be production, staging, testing, development, or other")
	}
	if params.RequiredApprovals < 0 || params.RequiredApprovals > 1 {
		return UpsertEnvironmentParams{}, "", invalidInput("environment required approvals", "must be zero or one")
	}
	if !params.Protected && (params.RequiredApprovals != 0 || params.WaitTimerSeconds != 0 || len(params.AllowedRefs) != 0) {
		return UpsertEnvironmentParams{}, "", invalidInput("environment protection", "must be enabled when protection rules are configured")
	}
	if params.WaitTimerSeconds < 0 || params.WaitTimerSeconds > 86400 {
		return UpsertEnvironmentParams{}, "", invalidInput("environment wait timer", "must be between zero and 86400 seconds")
	}
	if params.ConcurrencyMode == "" {
		params.ConcurrencyMode = EnvironmentConcurrencyQueue
	}
	if params.ConcurrencyMode != EnvironmentConcurrencyQueue && params.ConcurrencyMode != EnvironmentConcurrencyCancelInProgress {
		return UpsertEnvironmentParams{}, "", invalidInput("environment concurrency mode", "must be queue or cancel_in_progress")
	}
	refs := make([]string, 0, len(params.AllowedRefs))
	seen := make(map[string]struct{}, len(params.AllowedRefs))
	for _, ref := range params.AllowedRefs {
		ref, err = normalizeRequiredText("environment allowed ref", ref)
		if err != nil {
			return UpsertEnvironmentParams{}, "", err
		}
		if _, matchErr := path.Match(ref, ""); matchErr != nil {
			return UpsertEnvironmentParams{}, "", invalidInput("environment allowed ref", "must be a valid path pattern")
		}
		if _, exists := seen[ref]; !exists {
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	params.AllowedRefs = refs
	encoded, err := json.Marshal(refs)
	if err != nil {
		return UpsertEnvironmentParams{}, "", fmt.Errorf("store: encode environment refs: %w", err)
	}
	return params, string(encoded), nil
}

type environmentScanner interface {
	Scan(dest ...any) error
}

func scanEnvironment(scanner environmentScanner) (Environment, error) {
	var environment Environment
	var protected int
	var allowedRefs string
	var createdAt, updatedAt int64
	if err := scanner.Scan(&environment.ID, &environment.ProjectID, &environment.Name, &environment.DeploymentTier,
		&protected, &environment.RequiredApprovals, &environment.WaitTimerSeconds, &allowedRefs,
		&environment.ConcurrencyMode, &createdAt, &updatedAt); err != nil {
		return Environment{}, err
	}
	if err := json.Unmarshal([]byte(allowedRefs), &environment.AllowedRefs); err != nil {
		return Environment{}, fmt.Errorf("decode allowed refs: %w", err)
	}
	environment.Protected = protected != 0
	environment.CreatedAt = timeFromMillis(createdAt)
	environment.UpdatedAt = timeFromMillis(updatedAt)
	return environment, nil
}
