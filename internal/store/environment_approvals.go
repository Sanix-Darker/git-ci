package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type EnvironmentApprovalStatus string

const (
	EnvironmentApprovalPending   EnvironmentApprovalStatus = "pending"
	EnvironmentApprovalApproved  EnvironmentApprovalStatus = "approved"
	EnvironmentApprovalRejected  EnvironmentApprovalStatus = "rejected"
	EnvironmentApprovalCancelled EnvironmentApprovalStatus = "cancelled"
)

type EnvironmentApprovalRequest struct {
	ID                string                    `json:"id"`
	EnvironmentID     string                    `json:"environmentId"`
	RunID             string                    `json:"runId"`
	JobID             string                    `json:"jobId"`
	Status            EnvironmentApprovalStatus `json:"status"`
	RequiredApprovals int                       `json:"requiredApprovals"`
	RequestedBy       string                    `json:"requestedBy"`
	RequestedAt       time.Time                 `json:"requestedAt"`
	DecidedAt         *time.Time                `json:"decidedAt,omitempty"`
}

type EnvironmentApprovalDecision struct {
	ID        string                    `json:"id"`
	RequestID string                    `json:"requestId"`
	Decision  EnvironmentApprovalStatus `json:"decision"`
	Actor     string                    `json:"actor"`
	Reason    *string                   `json:"reason,omitempty"`
	CreatedAt time.Time                 `json:"createdAt"`
}

type ListEnvironmentApprovalsParams struct {
	ProjectID string
	Status    EnvironmentApprovalStatus
}

type EnvironmentApprovalSummary struct {
	EnvironmentApprovalRequest
	ProjectID       string         `json:"projectId"`
	ProjectName     string         `json:"projectName"`
	EnvironmentName string         `json:"environmentName"`
	DeploymentTier  DeploymentTier `json:"deploymentTier"`
	JobName         string         `json:"jobName"`
	Ref             *string        `json:"ref,omitempty"`
	CommitSHA       *string        `json:"commitSha,omitempty"`
}

type RequestEnvironmentApprovalParams struct {
	JobID       string
	RequestedBy string
}

type DecideEnvironmentApprovalParams struct {
	RequestID string
	Decision  EnvironmentApprovalStatus
	Actor     string
	Reason    string
}

const approvalRequestColumns = `id, environment_id, run_id, job_id, status, required_approvals, requested_by, requested_at, decided_at`

func (s *Store) RequestEnvironmentApproval(ctx context.Context, params RequestEnvironmentApprovalParams) (EnvironmentApprovalRequest, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	params.JobID, err = normalizeRequiredText("approval job ID", params.JobID)
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	params.RequestedBy, err = normalizeRequiredText("approval requester", params.RequestedBy)
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}

	var environmentID, runID string
	var requiredApprovals int
	var protected int
	err = db.QueryRowContext(ctx, `
		SELECT environment.id, target.run_id, environment.required_approvals, environment.protected
		FROM deployment_targets AS target
		JOIN runs AS run ON run.id = target.run_id
		JOIN environments AS environment
		  ON environment.project_id = run.project_id AND environment.name = target.environment
		WHERE target.job_id = ?
	`, params.JobID).Scan(&environmentID, &runID, &requiredApprovals, &protected)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentApprovalRequest{}, &ErrNotFound{Resource: "protected deployment target", Key: params.JobID}
	}
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: resolve approval target: %w", err)
	}
	if protected == 0 || requiredApprovals == 0 {
		return EnvironmentApprovalRequest{}, invalidInput("approval request", "target environment does not require approval")
	}

	id, err := randomOpaqueID()
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: generate approval request ID: %w", err)
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO environment_approval_requests (
			id, environment_id, run_id, job_id, status, required_approvals, requested_by, requested_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO NOTHING
	`, id, environmentID, runID, params.JobID, EnvironmentApprovalPending, requiredApprovals, params.RequestedBy, now.UnixMilli())
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: request environment approval: %w", err)
	}
	return s.getEnvironmentApprovalByJob(ctx, db, params.JobID)
}

func (s *Store) GetEnvironmentApprovalRequest(ctx context.Context, requestID string) (EnvironmentApprovalRequest, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	requestID, err = normalizeRequiredText("approval request ID", requestID)
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	request, err := scanEnvironmentApprovalRequest(db.QueryRowContext(ctx, `SELECT `+approvalRequestColumns+` FROM environment_approval_requests WHERE id = ?`, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentApprovalRequest{}, &ErrNotFound{Resource: "environment approval request", Key: requestID}
	}
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: get environment approval request: %w", err)
	}
	return request, nil
}

func (s *Store) ListEnvironmentApprovalRequests(ctx context.Context, params ListEnvironmentApprovalsParams) ([]EnvironmentApprovalSummary, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	params.ProjectID = strings.TrimSpace(params.ProjectID)
	if params.Status != "" && params.Status != EnvironmentApprovalPending && params.Status != EnvironmentApprovalApproved && params.Status != EnvironmentApprovalRejected && params.Status != EnvironmentApprovalCancelled {
		return nil, invalidInput("approval status", "must be pending, approved, rejected, or cancelled")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT request.id, request.environment_id, request.run_id, request.job_id, request.status,
			request.required_approvals, request.requested_by, request.requested_at, request.decided_at,
			environment.project_id, project.name, environment.name, environment.deployment_tier,
			job.name, run.ref, run.commit_sha
		FROM environment_approval_requests AS request
		JOIN environments AS environment ON environment.id = request.environment_id
		JOIN projects AS project ON project.id = environment.project_id
		JOIN runs AS run ON run.id = request.run_id
		JOIN jobs AS job ON job.id = request.job_id
		WHERE (? = '' OR environment.project_id = ?)
			AND (? = '' OR request.status = ?)
		ORDER BY CASE request.status WHEN 'pending' THEN 0 ELSE 1 END,
			request.requested_at DESC, request.id DESC
	`, params.ProjectID, params.ProjectID, params.Status, params.Status)
	if err != nil {
		return nil, fmt.Errorf("store: list environment approval requests: %w", err)
	}
	defer rows.Close()
	items := make([]EnvironmentApprovalSummary, 0)
	for rows.Next() {
		item, err := scanEnvironmentApprovalSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan environment approval request: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate environment approval requests: %w", err)
	}
	return items, nil
}

func (s *Store) DecideEnvironmentApproval(ctx context.Context, params DecideEnvironmentApprovalParams) (EnvironmentApprovalRequest, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	params.RequestID, err = normalizeRequiredText("approval request ID", params.RequestID)
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	params.Actor, err = normalizeRequiredText("approval actor", params.Actor)
	if err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	if params.Decision != EnvironmentApprovalApproved && params.Decision != EnvironmentApprovalRejected {
		return EnvironmentApprovalRequest{}, invalidInput("approval decision", "must be approved or rejected")
	}
	params.Reason = strings.TrimSpace(params.Reason)

	transaction, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: begin approval decision: %w", err)
	}
	defer transaction.Rollback()
	request, err := scanEnvironmentApprovalRequest(transaction.QueryRowContext(ctx, `SELECT `+approvalRequestColumns+` FROM environment_approval_requests WHERE id = ?`, params.RequestID))
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentApprovalRequest{}, &ErrNotFound{Resource: "environment approval request", Key: params.RequestID}
	}
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: load approval decision: %w", err)
	}
	if request.Status != EnvironmentApprovalPending {
		if request.Status == params.Decision {
			return request, nil
		}
		return EnvironmentApprovalRequest{}, invalidInput("approval decision", "request has already been decided")
	}

	now := nowUTC()
	result, err := transaction.ExecContext(ctx, `
		UPDATE environment_approval_requests
		SET status = ?, decided_at = ?
		WHERE id = ? AND status = ?
	`, params.Decision, now.UnixMilli(), params.RequestID, EnvironmentApprovalPending)
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: update approval request: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: approval request changed concurrently")
	}
	decisionID, err := randomOpaqueID()
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: generate approval decision ID: %w", err)
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO environment_approval_decisions (id, request_id, decision, actor, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, decisionID, params.RequestID, params.Decision, params.Actor, nullableText(params.Reason), now.UnixMilli())
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: insert approval decision: %w", err)
	}
	request, err = scanEnvironmentApprovalRequest(transaction.QueryRowContext(ctx, `SELECT `+approvalRequestColumns+` FROM environment_approval_requests WHERE id = ?`, params.RequestID))
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: reload approval request: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: commit approval decision: %w", err)
	}
	return request, nil
}

func (s *Store) ListEnvironmentApprovalDecisions(ctx context.Context, requestID string) ([]EnvironmentApprovalDecision, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	requestID, err = normalizeRequiredText("approval request ID", requestID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, request_id, decision, actor, reason, created_at
		FROM environment_approval_decisions WHERE request_id = ? ORDER BY created_at ASC, id ASC
	`, requestID)
	if err != nil {
		return nil, fmt.Errorf("store: list approval decisions: %w", err)
	}
	defer rows.Close()
	decisions := make([]EnvironmentApprovalDecision, 0)
	for rows.Next() {
		var decision EnvironmentApprovalDecision
		var reason sql.NullString
		var createdAt int64
		if err := rows.Scan(&decision.ID, &decision.RequestID, &decision.Decision, &decision.Actor, &reason, &createdAt); err != nil {
			return nil, fmt.Errorf("store: scan approval decision: %w", err)
		}
		decision.Reason = nullStringPointer(reason)
		decision.CreatedAt = timeFromMillis(createdAt)
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate approval decisions: %w", err)
	}
	return decisions, nil
}

func (s *Store) getEnvironmentApprovalByJob(ctx context.Context, db *sql.DB, jobID string) (EnvironmentApprovalRequest, error) {
	request, err := scanEnvironmentApprovalRequest(db.QueryRowContext(ctx, `SELECT `+approvalRequestColumns+` FROM environment_approval_requests WHERE job_id = ?`, jobID))
	if err != nil {
		return EnvironmentApprovalRequest{}, fmt.Errorf("store: get approval request by job: %w", err)
	}
	return request, nil
}

func scanEnvironmentApprovalRequest(scanner configurationScanner) (EnvironmentApprovalRequest, error) {
	var request EnvironmentApprovalRequest
	var requestedAt int64
	var decidedAt sql.NullInt64
	if err := scanner.Scan(&request.ID, &request.EnvironmentID, &request.RunID, &request.JobID, &request.Status,
		&request.RequiredApprovals, &request.RequestedBy, &requestedAt, &decidedAt); err != nil {
		return EnvironmentApprovalRequest{}, err
	}
	request.RequestedAt = timeFromMillis(requestedAt)
	request.DecidedAt = nullTimePointer(decidedAt)
	return request, nil
}

func scanEnvironmentApprovalSummary(scanner configurationScanner) (EnvironmentApprovalSummary, error) {
	var item EnvironmentApprovalSummary
	var requestedAt int64
	var decidedAt sql.NullInt64
	var ref, commitSHA sql.NullString
	if err := scanner.Scan(&item.ID, &item.EnvironmentID, &item.RunID, &item.JobID, &item.Status,
		&item.RequiredApprovals, &item.RequestedBy, &requestedAt, &decidedAt,
		&item.ProjectID, &item.ProjectName, &item.EnvironmentName, &item.DeploymentTier,
		&item.JobName, &ref, &commitSHA); err != nil {
		return EnvironmentApprovalSummary{}, err
	}
	item.RequestedAt = timeFromMillis(requestedAt)
	item.DecidedAt = nullTimePointer(decidedAt)
	item.Ref = nullStringPointer(ref)
	item.CommitSHA = nullStringPointer(commitSHA)
	return item, nil
}
