package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	secretColumns          = `id, project_id, name, provider, key_reference, version, encryption_algorithm, created_at, updated_at`
	webhookEndpointColumns = `id, project_id, name, provider, token_hash, metadata_json, enabled, created_at, updated_at`
	webhookDeliveryColumns = `id, endpoint_id, provider_delivery_id, event_type, payload_sha256, status, error_message, received_at, processed_at, created_at, updated_at`
	deploymentColumns      = `id, project_id, run_id, environment, status, created_at, updated_at, finished_at`
)

// Secret is non-sensitive secret metadata. It intentionally cannot expose an
// encrypted envelope through JSON serialization.
type Secret struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"projectId"`
	Name                string    `json:"name"`
	Provider            *string   `json:"provider,omitempty"`
	KeyReference        *string   `json:"keyReference,omitempty"`
	Version             *string   `json:"version,omitempty"`
	EncryptionAlgorithm string    `json:"encryptionAlgorithm"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// SecretEnvelope contains opaque encrypted secret bytes. Nonce and Ciphertext
// have explicit JSON exclusion to prevent accidental API plaintext exposure.
type SecretEnvelope struct {
	Secret
	Nonce      []byte `json:"-"`
	Ciphertext []byte `json:"-"`
}

type UpsertSecretParams struct {
	ProjectID           string
	Name                string
	Provider            *string
	KeyReference        *string
	Version             *string
	EncryptionAlgorithm string
	Nonce               []byte
	Ciphertext          []byte
}

// ListSecrets returns metadata only, ordered by name then opaque ID.
func (s *Store) ListSecrets(ctx context.Context, projectID string) ([]Secret, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("project ID", projectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+secretColumns+` FROM secrets WHERE project_id = ? ORDER BY name ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list secrets: %w", err)
	}
	defer rows.Close()
	secrets := make([]Secret, 0)
	for rows.Next() {
		secret, err := scanSecret(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan secret: %w", err)
		}
		secrets = append(secrets, secret)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate secrets: %w", err)
	}
	return secrets, nil
}

// GetSecret returns metadata only. Use GetSecretEnvelope only in trusted code
// that is responsible for decrypting the value.
func (s *Store) GetSecret(ctx context.Context, secretID string) (Secret, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Secret{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Secret{}, err
	}
	secretID, err = normalizeRequiredText("secret ID", secretID)
	if err != nil {
		return Secret{}, err
	}
	secret, err := scanSecret(db.QueryRowContext(ctx, `SELECT `+secretColumns+` FROM secrets WHERE id = ?`, secretID))
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, &ErrNotFound{Resource: "secret", Key: secretID}
	}
	if err != nil {
		return Secret{}, fmt.Errorf("store: get secret: %w", err)
	}
	return secret, nil
}

func (s *Store) GetSecretEnvelope(ctx context.Context, secretID string) (SecretEnvelope, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return SecretEnvelope{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return SecretEnvelope{}, err
	}
	secretID, err = normalizeRequiredText("secret ID", secretID)
	if err != nil {
		return SecretEnvelope{}, err
	}
	var envelope SecretEnvelope
	secret, err := scanSecretWithEnvelope(db.QueryRowContext(ctx, `SELECT `+secretColumns+`, nonce, ciphertext FROM secrets WHERE id = ?`, secretID), &envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return SecretEnvelope{}, &ErrNotFound{Resource: "secret", Key: secretID}
	}
	if err != nil {
		return SecretEnvelope{}, fmt.Errorf("store: get secret envelope: %w", err)
	}
	envelope.Secret = secret
	if len(envelope.Nonce) == 0 || len(envelope.Ciphertext) == 0 {
		return SecretEnvelope{}, fmt.Errorf("store: get secret envelope: %w", invalidInput("secret envelope", "is unavailable for a legacy metadata-only record"))
	}
	return envelope, nil
}

func (s *Store) UpsertSecret(ctx context.Context, params UpsertSecretParams) (Secret, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Secret{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Secret{}, err
	}
	params, err = normalizeUpsertSecretParams(params)
	if err != nil {
		return Secret{}, err
	}
	now := nowUTC()
	id, err := randomOpaqueID()
	if err != nil {
		return Secret{}, fmt.Errorf("store: generate secret ID: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO secrets (id, project_id, name, provider, key_reference, version, encryption_algorithm, nonce, ciphertext, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET
			provider = excluded.provider, key_reference = excluded.key_reference, version = excluded.version,
			encryption_algorithm = excluded.encryption_algorithm, nonce = excluded.nonce, ciphertext = excluded.ciphertext, updated_at = excluded.updated_at`,
		id, params.ProjectID, params.Name, nullableString(params.Provider), nullableString(params.KeyReference), nullableString(params.Version), params.EncryptionAlgorithm, params.Nonce, params.Ciphertext, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Secret{}, fmt.Errorf("store: upsert secret: %w", err)
	}
	return s.getSecretByProjectName(ctx, db, params.ProjectID, params.Name)
}

func (s *Store) DeleteSecret(ctx context.Context, secretID string) error {
	if err := validateConfigurationContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	secretID, err = normalizeRequiredText("secret ID", secretID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, secretID)
	if err != nil {
		return fmt.Errorf("store: delete secret: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete secret rows: %w", err)
	}
	if changed == 0 {
		return &ErrNotFound{Resource: "secret", Key: secretID}
	}
	return nil
}

type WorkflowSchedule struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"projectId"`
	WorkflowID string     `json:"workflowId"`
	Cron       string     `json:"cron"`
	Ref        *string    `json:"ref,omitempty"`
	Timezone   string     `json:"timezone"`
	Enabled    bool       `json:"enabled"`
	NextRunAt  *time.Time `json:"nextRunAt,omitempty"`
	LastRunAt  *time.Time `json:"lastRunAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

type CreateWorkflowScheduleParams struct {
	ProjectID, WorkflowID, Cron string
	Ref                         *string
	Timezone                    string
	Enabled                     bool
	NextRunAt                   *time.Time
}
type UpdateWorkflowScheduleParams struct {
	Cron      string
	Ref       *string
	Timezone  string
	Enabled   bool
	NextRunAt *time.Time
	LastRunAt *time.Time
}
type ScheduleClaim struct {
	Schedule  WorkflowSchedule `json:"schedule"`
	DueAt     time.Time        `json:"dueAt"`
	ClaimedAt time.Time        `json:"claimedAt"`
}

func (s *Store) CreateWorkflowSchedule(ctx context.Context, params CreateWorkflowScheduleParams) (WorkflowSchedule, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return WorkflowSchedule{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WorkflowSchedule{}, err
	}
	params, err = normalizeCreateWorkflowScheduleParams(params)
	if err != nil {
		return WorkflowSchedule{}, err
	}
	id, err := randomOpaqueID()
	if err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: generate schedule ID: %w", err)
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `INSERT INTO schedules (id, project_id, expression, branch, active, next_run_at, last_run_at, created_at, updated_at, workflow_id, ref, timezone) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?)`, id, params.ProjectID, params.Cron, nullableString(params.Ref), boolToInteger(params.Enabled), nullableConfigurationTime(params.NextRunAt), now.UnixMilli(), now.UnixMilli(), params.WorkflowID, nullableString(params.Ref), params.Timezone)
	if err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: create workflow schedule: %w", err)
	}
	return s.GetWorkflowSchedule(ctx, id)
}

func (s *Store) ListWorkflowSchedules(ctx context.Context, projectID string) ([]WorkflowSchedule, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("project ID", projectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, project_id, workflow_id, expression, ref, timezone, active, next_run_at, last_run_at, created_at, updated_at FROM schedules WHERE project_id = ? AND workflow_id IS NOT NULL ORDER BY next_run_at ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list workflow schedules: %w", err)
	}
	defer rows.Close()
	items := make([]WorkflowSchedule, 0)
	for rows.Next() {
		item, err := scanWorkflowSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan workflow schedule: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate workflow schedules: %w", err)
	}
	return items, nil
}

func (s *Store) GetWorkflowSchedule(ctx context.Context, scheduleID string) (WorkflowSchedule, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return WorkflowSchedule{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WorkflowSchedule{}, err
	}
	scheduleID, err = normalizeRequiredText("schedule ID", scheduleID)
	if err != nil {
		return WorkflowSchedule{}, err
	}
	item, err := scanWorkflowSchedule(db.QueryRowContext(ctx, `SELECT id, project_id, workflow_id, expression, ref, timezone, active, next_run_at, last_run_at, created_at, updated_at FROM schedules WHERE id = ? AND workflow_id IS NOT NULL`, scheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowSchedule{}, &ErrNotFound{Resource: "workflow schedule", Key: scheduleID}
	}
	if err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: get workflow schedule: %w", err)
	}
	return item, nil
}

func (s *Store) UpdateWorkflowSchedule(ctx context.Context, scheduleID string, params UpdateWorkflowScheduleParams) (WorkflowSchedule, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return WorkflowSchedule{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WorkflowSchedule{}, err
	}
	scheduleID, err = normalizeRequiredText("schedule ID", scheduleID)
	if err != nil {
		return WorkflowSchedule{}, err
	}
	params, err = normalizeUpdateWorkflowScheduleParams(params)
	if err != nil {
		return WorkflowSchedule{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: begin update workflow schedule: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE schedules SET expression = ?, branch = ?, ref = ?, timezone = ?, active = ?, next_run_at = ?, last_run_at = ?, updated_at = ? WHERE id = ? AND workflow_id IS NOT NULL`, params.Cron, nullableString(params.Ref), nullableString(params.Ref), params.Timezone, boolToInteger(params.Enabled), nullableConfigurationTime(params.NextRunAt), nullableConfigurationTime(params.LastRunAt), nowUTC().UnixMilli(), scheduleID)
	if err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: update workflow schedule: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: update workflow schedule rows: %w", err)
	}
	if changed == 0 {
		return WorkflowSchedule{}, &ErrNotFound{Resource: "workflow schedule", Key: scheduleID}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schedule_claims WHERE schedule_id = ?`, scheduleID); err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: release workflow schedule claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return WorkflowSchedule{}, fmt.Errorf("store: commit update workflow schedule: %w", err)
	}
	return s.GetWorkflowSchedule(ctx, scheduleID)
}

func (s *Store) DeleteWorkflowSchedule(ctx context.Context, scheduleID string) error {
	if err := validateConfigurationContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	scheduleID, err = normalizeRequiredText("schedule ID", scheduleID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ? AND workflow_id IS NOT NULL`, scheduleID)
	if err != nil {
		return fmt.Errorf("store: delete workflow schedule: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete workflow schedule rows: %w", err)
	}
	if changed == 0 {
		return &ErrNotFound{Resource: "workflow schedule", Key: scheduleID}
	}
	return nil
}

// ClaimDueWorkflowSchedules atomically reserves due schedules. A claim remains
// durable across a restart until UpdateWorkflowSchedule records the next run.
func (s *Store) ClaimDueWorkflowSchedules(ctx context.Context, dueBefore time.Time, limit int) ([]ScheduleClaim, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, invalidInput("schedule claim limit", "must be between 1 and 1000")
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	if dueBefore.IsZero() {
		return nil, invalidInput("schedule due time", "must not be zero")
	}
	now := nowUTC()
	rows, err := db.QueryContext(ctx, `
		INSERT OR IGNORE INTO schedule_claims (schedule_id, due_at, claimed_at)
		SELECT id, next_run_at, ?
		FROM schedules
		WHERE workflow_id IS NOT NULL
			AND active = 1
			AND next_run_at IS NOT NULL
			AND next_run_at <= ?
			AND NOT EXISTS (SELECT 1 FROM schedule_claims WHERE schedule_id = schedules.id)
		ORDER BY next_run_at ASC, id ASC
		LIMIT ?
		RETURNING schedule_id, due_at, claimed_at`, now.UnixMilli(), dueBefore.UTC().UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: reserve due workflow schedules: %w", err)
	}
	defer rows.Close()
	claims := make([]ScheduleClaim, 0)
	for rows.Next() {
		var scheduleID string
		var dueAtMillis, claimedAtMillis int64
		if err := rows.Scan(&scheduleID, &dueAtMillis, &claimedAtMillis); err != nil {
			return nil, fmt.Errorf("store: scan reserved workflow schedule: %w", err)
		}
		item, err := s.GetWorkflowSchedule(ctx, scheduleID)
		if err != nil {
			return nil, fmt.Errorf("store: read reserved workflow schedule: %w", err)
		}
		claims = append(claims, ScheduleClaim{Schedule: item, DueAt: timeFromMillis(dueAtMillis), ClaimedAt: timeFromMillis(claimedAtMillis)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate reserved workflow schedules: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("store: close reserved workflow schedules: %w", err)
	}
	return claims, nil
}

type WebhookEndpoint struct {
	ID        string          `json:"id"`
	ProjectID string          `json:"projectId"`
	Name      string          `json:"name"`
	Provider  string          `json:"provider"`
	TokenHash []byte          `json:"-"`
	Metadata  json.RawMessage `json:"metadata"`
	Enabled   bool            `json:"enabled"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}
type CreateWebhookEndpointParams struct {
	ProjectID, Name, Provider string
	TokenHash                 []byte
	Metadata                  json.RawMessage
	Enabled                   bool
}
type UpdateWebhookEndpointParams struct {
	Provider  string
	TokenHash []byte
	Metadata  json.RawMessage
	Enabled   bool
}
type WebhookDeliveryStatus string

const (
	WebhookDeliveryReceived WebhookDeliveryStatus = "received"
	WebhookDeliveryAccepted WebhookDeliveryStatus = "accepted"
	WebhookDeliveryRejected WebhookDeliveryStatus = "rejected"
	WebhookDeliveryFailed   WebhookDeliveryStatus = "failed"
)

type WebhookDelivery struct {
	ID                 string                `json:"id"`
	EndpointID         string                `json:"endpointId"`
	ProviderDeliveryID string                `json:"providerDeliveryId"`
	EventType          string                `json:"eventType"`
	PayloadSHA256      string                `json:"payloadSha256"`
	Status             WebhookDeliveryStatus `json:"status"`
	ErrorMessage       *string               `json:"errorMessage,omitempty"`
	ReceivedAt         time.Time             `json:"receivedAt"`
	ProcessedAt        *time.Time            `json:"processedAt,omitempty"`
	CreatedAt          time.Time             `json:"createdAt"`
	UpdatedAt          time.Time             `json:"updatedAt"`
}
type RecordWebhookDeliveryParams struct {
	EndpointID, ProviderDeliveryID, EventType, PayloadSHA256 string
	Status                                                   WebhookDeliveryStatus
	ErrorMessage                                             *string
	ProcessedAt                                              *time.Time
}
type RecordedWebhookDelivery struct {
	Delivery WebhookDelivery `json:"delivery"`
	Created  bool            `json:"created"`
}

func (s *Store) CreateWebhookEndpoint(ctx context.Context, params CreateWebhookEndpointParams) (WebhookEndpoint, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return WebhookEndpoint{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WebhookEndpoint{}, err
	}
	params, err = normalizeCreateWebhookEndpointParams(params)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	id, err := randomOpaqueID()
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: generate webhook endpoint ID: %w", err)
	}
	now := nowUTC()
	_, err = db.ExecContext(ctx, `INSERT INTO webhook_endpoints (id, project_id, name, provider, token_hash, metadata_json, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, params.ProjectID, params.Name, params.Provider, params.TokenHash, params.Metadata, boolToInteger(params.Enabled), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		if endpointName, lookupErr := webhookEndpointNameExists(ctx, db, params.ProjectID, params.Name); lookupErr == nil && endpointName {
			return WebhookEndpoint{}, &ErrConflict{Resource: "webhook endpoint", Field: "name", Value: params.Name}
		}
		return WebhookEndpoint{}, fmt.Errorf("store: create webhook endpoint: %w", err)
	}
	return s.GetWebhookEndpoint(ctx, id)
}

func (s *Store) ListWebhookEndpoints(ctx context.Context, projectID string) ([]WebhookEndpoint, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("project ID", projectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE project_id = ? ORDER BY name ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list webhook endpoints: %w", err)
	}
	defer rows.Close()
	items := make([]WebhookEndpoint, 0)
	for rows.Next() {
		item, err := scanWebhookEndpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan webhook endpoint: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate webhook endpoints: %w", err)
	}
	return items, nil
}

func (s *Store) GetWebhookEndpoint(ctx context.Context, endpointID string) (WebhookEndpoint, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return WebhookEndpoint{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WebhookEndpoint{}, err
	}
	endpointID, err = normalizeRequiredText("webhook endpoint ID", endpointID)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	item, err := scanWebhookEndpoint(db.QueryRowContext(ctx, `SELECT `+webhookEndpointColumns+` FROM webhook_endpoints WHERE id = ?`, endpointID))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookEndpoint{}, &ErrNotFound{Resource: "webhook endpoint", Key: endpointID}
	}
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: get webhook endpoint: %w", err)
	}
	return item, nil
}

func (s *Store) UpdateWebhookEndpoint(ctx context.Context, endpointID string, params UpdateWebhookEndpointParams) (WebhookEndpoint, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return WebhookEndpoint{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return WebhookEndpoint{}, err
	}
	endpointID, err = normalizeRequiredText("webhook endpoint ID", endpointID)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	params, err = normalizeUpdateWebhookEndpointParams(params)
	if err != nil {
		return WebhookEndpoint{}, err
	}
	result, err := db.ExecContext(ctx, `UPDATE webhook_endpoints SET provider = ?, token_hash = ?, metadata_json = ?, enabled = ?, updated_at = ? WHERE id = ?`, params.Provider, params.TokenHash, params.Metadata, boolToInteger(params.Enabled), nowUTC().UnixMilli(), endpointID)
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: update webhook endpoint: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return WebhookEndpoint{}, fmt.Errorf("store: update webhook endpoint rows: %w", err)
	}
	if changed == 0 {
		return WebhookEndpoint{}, &ErrNotFound{Resource: "webhook endpoint", Key: endpointID}
	}
	return s.GetWebhookEndpoint(ctx, endpointID)
}

func (s *Store) DeleteWebhookEndpoint(ctx context.Context, endpointID string) error {
	if err := validateConfigurationContext(ctx); err != nil {
		return err
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	endpointID, err = normalizeRequiredText("webhook endpoint ID", endpointID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE id = ?`, endpointID)
	if err != nil {
		return fmt.Errorf("store: delete webhook endpoint: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete webhook endpoint rows: %w", err)
	}
	if changed == 0 {
		return &ErrNotFound{Resource: "webhook endpoint", Key: endpointID}
	}
	return nil
}

func (s *Store) RecordWebhookDelivery(ctx context.Context, params RecordWebhookDeliveryParams) (RecordedWebhookDelivery, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return RecordedWebhookDelivery{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return RecordedWebhookDelivery{}, err
	}
	params, err = normalizeRecordWebhookDeliveryParams(params)
	if err != nil {
		return RecordedWebhookDelivery{}, err
	}
	id, err := randomOpaqueID()
	if err != nil {
		return RecordedWebhookDelivery{}, fmt.Errorf("store: generate webhook delivery ID: %w", err)
	}
	now := nowUTC()
	result, err := db.ExecContext(ctx, `INSERT INTO webhook_deliveries (id, endpoint_id, provider_delivery_id, event_type, payload_sha256, status, error_message, received_at, processed_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(endpoint_id, provider_delivery_id) DO NOTHING`, id, params.EndpointID, params.ProviderDeliveryID, params.EventType, params.PayloadSHA256, params.Status, nullableString(params.ErrorMessage), now.UnixMilli(), nullableConfigurationTime(params.ProcessedAt), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return RecordedWebhookDelivery{}, fmt.Errorf("store: record webhook delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RecordedWebhookDelivery{}, fmt.Errorf("store: record webhook delivery rows: %w", err)
	}
	item, err := s.getWebhookDelivery(ctx, db, params.EndpointID, params.ProviderDeliveryID)
	if err != nil {
		return RecordedWebhookDelivery{}, err
	}
	return RecordedWebhookDelivery{Delivery: item, Created: changed == 1}, nil
}

func (s *Store) ListWebhookDeliveries(ctx context.Context, endpointID string) ([]WebhookDelivery, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	endpointID, err = normalizeRequiredText("webhook endpoint ID", endpointID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries WHERE endpoint_id = ? ORDER BY received_at DESC, id DESC`, endpointID)
	if err != nil {
		return nil, fmt.Errorf("store: list webhook deliveries: %w", err)
	}
	defer rows.Close()
	items := make([]WebhookDelivery, 0)
	for rows.Next() {
		item, err := scanWebhookDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan webhook delivery: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate webhook deliveries: %w", err)
	}
	return items, nil
}

type Deployment struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"projectId"`
	RunID       string            `json:"runId"`
	Environment string            `json:"environment"`
	Status      Status            `json:"status"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	FinishedAt  *time.Time        `json:"finishedAt,omitempty"`
	History     []DeploymentEvent `json:"history,omitempty"`
}
type DeploymentEvent struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Status       Status    `json:"status"`
	Reason       *string   `json:"reason,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}
type CreateDeploymentParams struct {
	ProjectID, RunID, Environment string
	Status                        Status
	Reason                        *string
}

func (s *Store) CreateDeployment(ctx context.Context, params CreateDeploymentParams) (Deployment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Deployment{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Deployment{}, err
	}
	params, err = normalizeCreateDeploymentParams(params)
	if err != nil {
		return Deployment{}, err
	}
	id, err := randomOpaqueID()
	if err != nil {
		return Deployment{}, fmt.Errorf("store: generate deployment ID: %w", err)
	}
	eventID, err := randomOpaqueID()
	if err != nil {
		return Deployment{}, fmt.Errorf("store: generate deployment event ID: %w", err)
	}
	now := nowUTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, fmt.Errorf("store: begin create deployment: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO deployments (id, project_id, run_id, environment, status, created_at, updated_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, params.ProjectID, params.RunID, params.Environment, params.Status, now.UnixMilli(), now.UnixMilli(), nullableFinishedAt(params.Status, now))
	if err != nil {
		return Deployment{}, fmt.Errorf("store: create deployment: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deployment_events (id, deployment_id, status, reason, created_at) VALUES (?, ?, ?, ?, ?)`, eventID, id, params.Status, nullableString(params.Reason), now.UnixMilli())
	if err != nil {
		return Deployment{}, fmt.Errorf("store: create deployment event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, fmt.Errorf("store: commit create deployment: %w", err)
	}
	return s.GetDeployment(ctx, id)
}

func (s *Store) ListDeployments(ctx context.Context, projectID string) ([]Deployment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	projectID, err = normalizeRequiredText("project ID", projectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE project_id = ? ORDER BY created_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list deployments: %w", err)
	}
	defer rows.Close()
	items := make([]Deployment, 0)
	for rows.Next() {
		item, err := scanDeployment(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan deployment: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate deployments: %w", err)
	}
	return items, nil
}

func (s *Store) GetDeployment(ctx context.Context, deploymentID string) (Deployment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Deployment{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Deployment{}, err
	}
	deploymentID, err = normalizeRequiredText("deployment ID", deploymentID)
	if err != nil {
		return Deployment{}, err
	}
	item, err := scanDeployment(db.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, &ErrNotFound{Resource: "deployment", Key: deploymentID}
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("store: get deployment: %w", err)
	}
	history, err := listDeploymentEvents(ctx, db, deploymentID)
	if err != nil {
		return Deployment{}, err
	}
	item.History = history
	return item, nil
}

func (s *Store) TransitionDeployment(ctx context.Context, deploymentID string, next Status, reason *string) (Deployment, error) {
	if err := validateConfigurationContext(ctx); err != nil {
		return Deployment{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Deployment{}, err
	}
	deploymentID, err = normalizeRequiredText("deployment ID", deploymentID)
	if err != nil {
		return Deployment{}, err
	}
	if !validDeploymentStatus(next) {
		return Deployment{}, invalidInput("deployment status", "must be a known lifecycle state")
	}
	reason, err = normalizeOptionalString("deployment reason", reason)
	if err != nil {
		return Deployment{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, fmt.Errorf("store: begin deployment transition: %w", err)
	}
	defer tx.Rollback()
	current, err := scanDeployment(tx.QueryRowContext(ctx, `SELECT `+deploymentColumns+` FROM deployments WHERE id = ?`, deploymentID))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, &ErrNotFound{Resource: "deployment", Key: deploymentID}
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("store: get deployment transition: %w", err)
	}
	if !validDeploymentTransition(current.Status, next) {
		return Deployment{}, &ErrInvalidStatusTransition{Resource: "deployment", ID: deploymentID, From: current.Status, To: next}
	}
	eventID, err := randomOpaqueID()
	if err != nil {
		return Deployment{}, fmt.Errorf("store: generate deployment event ID: %w", err)
	}
	now := nowUTC()
	_, err = tx.ExecContext(ctx, `UPDATE deployments SET status = ?, updated_at = ?, finished_at = ? WHERE id = ?`, next, now.UnixMilli(), nullableFinishedAt(next, now), deploymentID)
	if err != nil {
		return Deployment{}, fmt.Errorf("store: transition deployment: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deployment_events (id, deployment_id, status, reason, created_at) VALUES (?, ?, ?, ?, ?)`, eventID, deploymentID, next, nullableString(reason), now.UnixMilli())
	if err != nil {
		return Deployment{}, fmt.Errorf("store: record deployment transition: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, fmt.Errorf("store: commit deployment transition: %w", err)
	}
	return s.GetDeployment(ctx, deploymentID)
}

func validateConfigurationContext(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	return ctx.Err()
}
func normalizeUpsertSecretParams(params UpsertSecretParams) (UpsertSecretParams, error) {
	var err error
	if params.ProjectID, err = normalizeRequiredText("secret project ID", params.ProjectID); err != nil {
		return params, err
	}
	if params.Name, err = normalizeRequiredText("secret name", params.Name); err != nil {
		return params, err
	}
	if params.Provider, err = normalizeOptionalString("secret provider", params.Provider); err != nil {
		return params, err
	}
	if params.KeyReference, err = normalizeOptionalString("secret key reference", params.KeyReference); err != nil {
		return params, err
	}
	if params.Version, err = normalizeOptionalString("secret version", params.Version); err != nil {
		return params, err
	}
	if params.EncryptionAlgorithm, err = normalizeRequiredText("secret encryption algorithm", params.EncryptionAlgorithm); err != nil {
		return params, err
	}
	if len(params.Nonce) == 0 || len(params.Ciphertext) == 0 {
		return params, invalidInput("secret envelope", "nonce and ciphertext must not be empty")
	}
	return params, nil
}
func normalizeCreateWorkflowScheduleParams(params CreateWorkflowScheduleParams) (CreateWorkflowScheduleParams, error) {
	var err error
	if params.ProjectID, err = normalizeRequiredText("schedule project ID", params.ProjectID); err != nil {
		return params, err
	}
	if params.WorkflowID, err = normalizeRequiredText("schedule workflow ID", params.WorkflowID); err != nil {
		return params, err
	}
	if params.Cron, err = normalizeCron(params.Cron); err != nil {
		return params, err
	}
	if params.Ref, err = normalizeOptionalString("schedule ref", params.Ref); err != nil {
		return params, err
	}
	if params.Timezone, err = normalizeTimezone(params.Timezone); err != nil {
		return params, err
	}
	if params.NextRunAt == nil || params.NextRunAt.IsZero() {
		return params, invalidInput("schedule next run", "must not be empty")
	}
	value := params.NextRunAt.UTC()
	params.NextRunAt = &value
	return params, nil
}
func normalizeUpdateWorkflowScheduleParams(params UpdateWorkflowScheduleParams) (UpdateWorkflowScheduleParams, error) {
	var err error
	if params.Cron, err = normalizeCron(params.Cron); err != nil {
		return params, err
	}
	if params.Ref, err = normalizeOptionalString("schedule ref", params.Ref); err != nil {
		return params, err
	}
	if params.Timezone, err = normalizeTimezone(params.Timezone); err != nil {
		return params, err
	}
	if params.NextRunAt != nil {
		if params.NextRunAt.IsZero() {
			return params, invalidInput("schedule next run", "must not be zero")
		}
		value := params.NextRunAt.UTC()
		params.NextRunAt = &value
	}
	if params.LastRunAt != nil {
		if params.LastRunAt.IsZero() {
			return params, invalidInput("schedule last run", "must not be zero")
		}
		value := params.LastRunAt.UTC()
		params.LastRunAt = &value
	}
	return params, nil
}
func normalizeCron(value string) (string, error) {
	value, err := normalizeRequiredText("schedule cron", value)
	if err != nil {
		return "", err
	}
	if len(value) > 256 || len(strings.Fields(value)) != 5 {
		return "", invalidInput("schedule cron", "must contain exactly five fields")
	}
	return value, nil
}
func normalizeTimezone(value string) (string, error) {
	value, err := normalizeRequiredText("schedule timezone", value)
	if err != nil {
		return "", err
	}
	if _, err := time.LoadLocation(value); err != nil {
		return "", invalidInput("schedule timezone", "must be an IANA timezone")
	}
	return value, nil
}
func normalizeCreateWebhookEndpointParams(params CreateWebhookEndpointParams) (CreateWebhookEndpointParams, error) {
	var err error
	if params.ProjectID, err = normalizeRequiredText("webhook project ID", params.ProjectID); err != nil {
		return params, err
	}
	if params.Name, err = normalizeRequiredText("webhook endpoint name", params.Name); err != nil {
		return params, err
	}
	if params.Provider, err = normalizeRequiredText("webhook provider", params.Provider); err != nil {
		return params, err
	}
	if len(params.TokenHash) == 0 {
		return params, invalidInput("webhook token hash", "must not be empty")
	}
	if params.Metadata, err = normalizeJSONObject("webhook metadata", params.Metadata, false); err != nil {
		return params, err
	}
	return params, nil
}
func normalizeUpdateWebhookEndpointParams(params UpdateWebhookEndpointParams) (UpdateWebhookEndpointParams, error) {
	normalized, err := normalizeCreateWebhookEndpointParams(CreateWebhookEndpointParams{ProjectID: "update", Name: "update", Provider: params.Provider, TokenHash: params.TokenHash, Metadata: params.Metadata, Enabled: params.Enabled})
	if err != nil {
		return params, err
	}
	params.Provider, params.TokenHash, params.Metadata = normalized.Provider, normalized.TokenHash, normalized.Metadata
	return params, nil
}
func normalizeRecordWebhookDeliveryParams(params RecordWebhookDeliveryParams) (RecordWebhookDeliveryParams, error) {
	var err error
	if params.EndpointID, err = normalizeRequiredText("webhook endpoint ID", params.EndpointID); err != nil {
		return params, err
	}
	if params.ProviderDeliveryID, err = normalizeRequiredText("provider delivery ID", params.ProviderDeliveryID); err != nil {
		return params, err
	}
	if params.EventType, err = normalizeRequiredText("webhook event type", params.EventType); err != nil {
		return params, err
	}
	params.PayloadSHA256 = strings.ToLower(strings.TrimSpace(params.PayloadSHA256))
	if len(params.PayloadSHA256) != 64 {
		return params, invalidInput("webhook payload SHA-256", "must be a 64-character hex digest")
	}
	if _, err := hex.DecodeString(params.PayloadSHA256); err != nil {
		return params, invalidInput("webhook payload SHA-256", "must be hexadecimal")
	}
	if !validWebhookDeliveryStatus(params.Status) {
		return params, invalidInput("webhook delivery status", "must be a known delivery state")
	}
	if params.ErrorMessage, err = normalizeOptionalString("webhook delivery error", params.ErrorMessage); err != nil {
		return params, err
	}
	if params.ProcessedAt != nil {
		if params.ProcessedAt.IsZero() {
			return params, invalidInput("webhook processed time", "must not be zero")
		}
		value := params.ProcessedAt.UTC()
		params.ProcessedAt = &value
	}
	return params, nil
}
func normalizeCreateDeploymentParams(params CreateDeploymentParams) (CreateDeploymentParams, error) {
	var err error
	if params.ProjectID, err = normalizeRequiredText("deployment project ID", params.ProjectID); err != nil {
		return params, err
	}
	if params.RunID, err = normalizeRequiredText("deployment run ID", params.RunID); err != nil {
		return params, err
	}
	if params.Environment, err = normalizeRequiredText("deployment environment", params.Environment); err != nil {
		return params, err
	}
	if !validDeploymentStatus(params.Status) {
		return params, invalidInput("deployment status", "must be a known lifecycle state")
	}
	if params.Reason, err = normalizeOptionalString("deployment reason", params.Reason); err != nil {
		return params, err
	}
	return params, nil
}
func validWebhookDeliveryStatus(status WebhookDeliveryStatus) bool {
	return status == WebhookDeliveryReceived || status == WebhookDeliveryAccepted || status == WebhookDeliveryRejected || status == WebhookDeliveryFailed
}
func validDeploymentStatus(status Status) bool {
	return status == StatusQueued || status == StatusRunning || status == StatusSucceeded || status == StatusFailed || status == StatusCancelled || status == StatusSkipped
}
func validDeploymentTransition(from, to Status) bool {
	if from == to {
		return false
	}
	switch from {
	case StatusQueued:
		return to == StatusRunning || to == StatusFailed || to == StatusCancelled || to == StatusSkipped
	case StatusRunning:
		return to == StatusSucceeded || to == StatusFailed || to == StatusCancelled
	default:
		return false
	}
}
func nullableConfigurationTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMilli()
}
func nullableFinishedAt(status Status, now time.Time) any {
	if status == StatusSucceeded || status == StatusFailed || status == StatusCancelled || status == StatusSkipped {
		return now.UnixMilli()
	}
	return nil
}

type configurationScanner interface{ Scan(dest ...any) error }

func scanSecret(scanner configurationScanner) (Secret, error) {
	var item Secret
	var provider, reference, version sql.NullString
	var created, updated int64
	if err := scanner.Scan(&item.ID, &item.ProjectID, &item.Name, &provider, &reference, &version, &item.EncryptionAlgorithm, &created, &updated); err != nil {
		return Secret{}, err
	}
	if provider.Valid {
		value := provider.String
		item.Provider = &value
	}
	if reference.Valid {
		value := reference.String
		item.KeyReference = &value
	}
	if version.Valid {
		value := version.String
		item.Version = &value
	}
	item.CreatedAt, item.UpdatedAt = timeFromMillis(created), timeFromMillis(updated)
	return item, nil
}
func scanSecretWithEnvelope(scanner configurationScanner, envelope *SecretEnvelope) (Secret, error) {
	var item Secret
	var provider, reference, version sql.NullString
	var created, updated int64
	if err := scanner.Scan(&item.ID, &item.ProjectID, &item.Name, &provider, &reference, &version, &item.EncryptionAlgorithm, &created, &updated, &envelope.Nonce, &envelope.Ciphertext); err != nil {
		return Secret{}, err
	}
	if provider.Valid {
		value := provider.String
		item.Provider = &value
	}
	if reference.Valid {
		value := reference.String
		item.KeyReference = &value
	}
	if version.Valid {
		value := version.String
		item.Version = &value
	}
	item.CreatedAt, item.UpdatedAt = timeFromMillis(created), timeFromMillis(updated)
	return item, nil
}
func scanWorkflowSchedule(scanner configurationScanner) (WorkflowSchedule, error) {
	var item WorkflowSchedule
	var ref sql.NullString
	var active int64
	var next, last sql.NullInt64
	var created, updated int64
	if err := scanner.Scan(&item.ID, &item.ProjectID, &item.WorkflowID, &item.Cron, &ref, &item.Timezone, &active, &next, &last, &created, &updated); err != nil {
		return WorkflowSchedule{}, err
	}
	if ref.Valid {
		value := ref.String
		item.Ref = &value
	}
	item.Enabled = active != 0
	if next.Valid {
		value := timeFromMillis(next.Int64)
		item.NextRunAt = &value
	}
	if last.Valid {
		value := timeFromMillis(last.Int64)
		item.LastRunAt = &value
	}
	item.CreatedAt, item.UpdatedAt = timeFromMillis(created), timeFromMillis(updated)
	return item, nil
}
func scanWebhookEndpoint(scanner configurationScanner) (WebhookEndpoint, error) {
	var item WebhookEndpoint
	var enabled int64
	var created, updated int64
	if err := scanner.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Provider, &item.TokenHash, &item.Metadata, &enabled, &created, &updated); err != nil {
		return WebhookEndpoint{}, err
	}
	item.Enabled = enabled != 0
	item.CreatedAt, item.UpdatedAt = timeFromMillis(created), timeFromMillis(updated)
	return item, nil
}
func scanWebhookDelivery(scanner configurationScanner) (WebhookDelivery, error) {
	var item WebhookDelivery
	var message sql.NullString
	var processed sql.NullInt64
	var received, created, updated int64
	if err := scanner.Scan(&item.ID, &item.EndpointID, &item.ProviderDeliveryID, &item.EventType, &item.PayloadSHA256, &item.Status, &message, &received, &processed, &created, &updated); err != nil {
		return WebhookDelivery{}, err
	}
	if message.Valid {
		value := message.String
		item.ErrorMessage = &value
	}
	if processed.Valid {
		value := timeFromMillis(processed.Int64)
		item.ProcessedAt = &value
	}
	item.ReceivedAt, item.CreatedAt, item.UpdatedAt = timeFromMillis(received), timeFromMillis(created), timeFromMillis(updated)
	return item, nil
}
func scanDeployment(scanner configurationScanner) (Deployment, error) {
	var item Deployment
	var finished sql.NullInt64
	var created, updated int64
	if err := scanner.Scan(&item.ID, &item.ProjectID, &item.RunID, &item.Environment, &item.Status, &created, &updated, &finished); err != nil {
		return Deployment{}, err
	}
	item.CreatedAt, item.UpdatedAt = timeFromMillis(created), timeFromMillis(updated)
	if finished.Valid {
		value := timeFromMillis(finished.Int64)
		item.FinishedAt = &value
	}
	return item, nil
}
func scanDeploymentEvent(scanner configurationScanner) (DeploymentEvent, error) {
	var item DeploymentEvent
	var reason sql.NullString
	var created int64
	if err := scanner.Scan(&item.ID, &item.DeploymentID, &item.Status, &reason, &created); err != nil {
		return DeploymentEvent{}, err
	}
	if reason.Valid {
		value := reason.String
		item.Reason = &value
	}
	item.CreatedAt = timeFromMillis(created)
	return item, nil
}
func (s *Store) getSecretByProjectName(ctx context.Context, db *sql.DB, projectID, name string) (Secret, error) {
	item, err := scanSecret(db.QueryRowContext(ctx, `SELECT `+secretColumns+` FROM secrets WHERE project_id = ? AND name = ?`, projectID, name))
	if err != nil {
		return Secret{}, fmt.Errorf("store: read upserted secret: %w", err)
	}
	return item, nil
}
func (s *Store) getWebhookDelivery(ctx context.Context, db *sql.DB, endpointID, providerDeliveryID string) (WebhookDelivery, error) {
	item, err := scanWebhookDelivery(db.QueryRowContext(ctx, `SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries WHERE endpoint_id = ? AND provider_delivery_id = ?`, endpointID, providerDeliveryID))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookDelivery{}, &ErrNotFound{Resource: "webhook delivery", Key: providerDeliveryID}
	}
	if err != nil {
		return WebhookDelivery{}, fmt.Errorf("store: get webhook delivery: %w", err)
	}
	return item, nil
}
func listDeploymentEvents(ctx context.Context, db *sql.DB, deploymentID string) ([]DeploymentEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, deployment_id, status, reason, created_at FROM deployment_events WHERE deployment_id = ? ORDER BY created_at ASC, id ASC`, deploymentID)
	if err != nil {
		return nil, fmt.Errorf("store: list deployment history: %w", err)
	}
	defer rows.Close()
	items := make([]DeploymentEvent, 0)
	for rows.Next() {
		item, err := scanDeploymentEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan deployment history: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate deployment history: %w", err)
	}
	return items, nil
}
func webhookEndpointNameExists(ctx context.Context, db *sql.DB, projectID, name string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM webhook_endpoints WHERE project_id = ? AND name = ?`, projectID, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
