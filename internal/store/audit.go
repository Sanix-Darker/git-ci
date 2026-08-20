package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AuditEvent is an immutable record of a meaningful store action. IDs and
// creation timestamps are assigned by Store when the event is recorded.
type AuditEvent struct {
	ID           string
	ProjectID    string
	Action       string
	Actor        string
	ResourceType string
	ResourceID   string
	Metadata     json.RawMessage
	CreatedAt    time.Time
}

// RecordAudit validates and appends an audit event. ProjectID is optional; if
// provided, it must identify an existing project.
func (s *Store) RecordAudit(ctx context.Context, event AuditEvent) (AuditEvent, error) {
	if err := requireContext(ctx); err != nil {
		return AuditEvent{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return AuditEvent{}, err
	}

	event, err = normalizeAuditEvent(event)
	if err != nil {
		return AuditEvent{}, err
	}
	if event.ProjectID != "" {
		exists, err := projectIDExists(ctx, db, event.ProjectID)
		if err != nil {
			return AuditEvent{}, fmt.Errorf("store: check audit project: %w", err)
		}
		if !exists {
			return AuditEvent{}, &ErrNotFound{Resource: "project", Key: event.ProjectID}
		}
	}

	event.ID, err = randomOpaqueID()
	if err != nil {
		return AuditEvent{}, fmt.Errorf("store: generate audit event ID: %w", err)
	}
	event.CreatedAt = nowUTC()

	_, err = db.ExecContext(ctx, `
		INSERT INTO audit_events (
			id, project_id, action, actor, resource_type, resource_id,
			metadata_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.ID,
		nullableText(event.ProjectID),
		event.Action,
		nullableText(event.Actor),
		nullableText(event.ResourceType),
		nullableText(event.ResourceID),
		string(event.Metadata),
		event.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return AuditEvent{}, fmt.Errorf("store: record audit event: %w", err)
	}
	return event, nil
}

func normalizeAuditEvent(event AuditEvent) (AuditEvent, error) {
	if event.ID != "" {
		return AuditEvent{}, invalidInput("audit event ID", "is assigned by the store")
	}
	if !event.CreatedAt.IsZero() {
		return AuditEvent{}, invalidInput("audit event created time", "is assigned by the store")
	}

	var err error
	if event.ProjectID, err = normalizeOptionalText("audit project ID", event.ProjectID); err != nil {
		return AuditEvent{}, err
	}
	if event.Action, err = normalizeRequiredText("audit action", event.Action); err != nil {
		return AuditEvent{}, err
	}
	if event.Actor, err = normalizeOptionalText("audit actor", event.Actor); err != nil {
		return AuditEvent{}, err
	}
	if event.ResourceType, err = normalizeOptionalText("audit resource type", event.ResourceType); err != nil {
		return AuditEvent{}, err
	}
	if event.ResourceID, err = normalizeOptionalText("audit resource ID", event.ResourceID); err != nil {
		return AuditEvent{}, err
	}

	metadata := bytes.TrimSpace(event.Metadata)
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	if !json.Valid(metadata) {
		return AuditEvent{}, invalidInput("audit metadata", "must be valid JSON")
	}
	event.Metadata = append(json.RawMessage(nil), metadata...)
	return event, nil
}
