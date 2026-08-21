package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
	auditBucketCount  = 12
)

// AuditEvent is an immutable record of a meaningful store action. IDs and
// creation timestamps are assigned by Store when the event is recorded.
type AuditEvent struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId,omitempty"`
	Action       string          `json:"action"`
	Actor        string          `json:"actor,omitempty"`
	ResourceType string          `json:"resourceType,omitempty"`
	ResourceID   string          `json:"resourceId,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"createdAt"`
}

// AuditFilter bounds an immutable audit query. Until is inclusive; a zero
// Since value means all retained history.
type AuditFilter struct {
	ProjectID    string    `json:"projectId,omitempty"`
	Actor        string    `json:"actor,omitempty"`
	Action       string    `json:"action,omitempty"`
	ResourceType string    `json:"resourceType,omitempty"`
	Search       string    `json:"q,omitempty"`
	Since        time.Time `json:"since,omitempty"`
	Until        time.Time `json:"until"`
	Limit        int       `json:"limit"`
	Offset       int       `json:"offset"`
}

type AuditFacets struct {
	Actors        []string `json:"actors"`
	Actions       []string `json:"actions"`
	ResourceTypes []string `json:"resourceTypes"`
}

type AuditBucket struct {
	Label string    `json:"label"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Count int       `json:"count"`
	Level int       `json:"level"`
}

type AuditReport struct {
	Items   []AuditEvent  `json:"items"`
	Count   int           `json:"count"`
	Total   int           `json:"total"`
	Filter  AuditFilter   `json:"filter"`
	Facets  AuditFacets   `json:"facets"`
	Buckets []AuditBucket `json:"buckets"`
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

// ListAudit returns a stable newest-first page, facets, and a fixed twelve
// bucket histogram from the same SQLite filter contract.
func (s *Store) ListAudit(ctx context.Context, filter AuditFilter) (AuditReport, error) {
	if err := requireContext(ctx); err != nil {
		return AuditReport{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return AuditReport{}, err
	}
	filter, err = normalizeAuditFilter(filter)
	if err != nil {
		return AuditReport{}, err
	}
	where, args := auditWhere(filter)

	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`+where, args...).Scan(&total); err != nil {
		return AuditReport{}, fmt.Errorf("store: count audit events: %w", err)
	}
	queryArgs := append(append([]any{}, args...), filter.Limit, filter.Offset)
	rows, err := db.QueryContext(ctx, `
		SELECT id, project_id, action, actor, resource_type, resource_id, metadata_json, created_at
		FROM audit_events`+where+`
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return AuditReport{}, fmt.Errorf("store: list audit events: %w", err)
	}
	defer rows.Close()
	items := make([]AuditEvent, 0, min(total, filter.Limit))
	for rows.Next() {
		event, scanErr := scanAuditEvent(rows)
		if scanErr != nil {
			return AuditReport{}, scanErr
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return AuditReport{}, fmt.Errorf("store: iterate audit events: %w", err)
	}

	facets, err := auditFacets(ctx, db, filter)
	if err != nil {
		return AuditReport{}, err
	}
	buckets, err := auditHistogram(ctx, db, filter, total)
	if err != nil {
		return AuditReport{}, err
	}
	return AuditReport{Items: items, Count: len(items), Total: total, Filter: filter, Facets: facets, Buckets: buckets}, nil
}

func normalizeAuditFilter(filter AuditFilter) (AuditFilter, error) {
	var err error
	if filter.ProjectID, err = normalizeOptionalText("audit project filter", filter.ProjectID); err != nil {
		return AuditFilter{}, err
	}
	if filter.Actor, err = normalizeOptionalText("audit actor filter", filter.Actor); err != nil {
		return AuditFilter{}, err
	}
	if filter.Action, err = normalizeOptionalText("audit action filter", filter.Action); err != nil {
		return AuditFilter{}, err
	}
	if filter.ResourceType, err = normalizeOptionalText("audit resource filter", filter.ResourceType); err != nil {
		return AuditFilter{}, err
	}
	if filter.Search, err = normalizeOptionalText("audit search", filter.Search); err != nil {
		return AuditFilter{}, err
	}
	if filter.Until.IsZero() {
		filter.Until = nowUTC()
	} else {
		filter.Until = filter.Until.UTC()
	}
	if !filter.Since.IsZero() {
		filter.Since = filter.Since.UTC()
		if filter.Since.After(filter.Until) {
			return AuditFilter{}, invalidInput("audit time range", "since must not be after until")
		}
	}
	if filter.Limit == 0 {
		filter.Limit = defaultAuditLimit
	}
	if filter.Limit < 1 || filter.Limit > maxAuditLimit {
		return AuditFilter{}, invalidInput("audit limit", "must be between 1 and 200")
	}
	if filter.Offset < 0 {
		return AuditFilter{}, invalidInput("audit offset", "must not be negative")
	}
	return filter, nil
}

func auditWhere(filter AuditFilter) (string, []any) {
	clauses := []string{"created_at <= ?"}
	args := []any{filter.Until.UnixMilli()}
	if !filter.Since.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, filter.Since.UnixMilli())
	}
	for _, item := range []struct {
		column string
		value  string
	}{{"project_id", filter.ProjectID}, {"actor", filter.Actor}, {"action", filter.Action}, {"resource_type", filter.ResourceType}} {
		if item.value != "" {
			clauses = append(clauses, item.column+" = ?")
			args = append(args, item.value)
		}
	}
	if filter.Search != "" {
		clauses = append(clauses, `instr(lower(action || ' ' || coalesce(actor, '') || ' ' || coalesce(resource_type, '') || ' ' || coalesce(resource_id, '') || ' ' || coalesce(project_id, '') || ' ' || metadata_json), ?) > 0`)
		args = append(args, strings.ToLower(filter.Search))
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanAuditEvent(scanner interface{ Scan(...any) error }) (AuditEvent, error) {
	var event AuditEvent
	var projectID, actor, resourceType, resourceID sql.NullString
	var metadata string
	var createdAt int64
	if err := scanner.Scan(&event.ID, &projectID, &event.Action, &actor, &resourceType, &resourceID, &metadata, &createdAt); err != nil {
		return AuditEvent{}, fmt.Errorf("store: scan audit event: %w", err)
	}
	event.ProjectID = projectID.String
	event.Actor = actor.String
	event.ResourceType = resourceType.String
	event.ResourceID = resourceID.String
	event.Metadata = json.RawMessage(metadata)
	event.CreatedAt = time.UnixMilli(createdAt).UTC()
	return event, nil
}

func auditFacets(ctx context.Context, db *sql.DB, filter AuditFilter) (AuditFacets, error) {
	base := AuditFilter{ProjectID: filter.ProjectID, Since: filter.Since, Until: filter.Until}
	actors, err := auditFacetValues(ctx, db, "actor", base)
	if err != nil {
		return AuditFacets{}, err
	}
	actions, err := auditFacetValues(ctx, db, "action", base)
	if err != nil {
		return AuditFacets{}, err
	}
	resources, err := auditFacetValues(ctx, db, "resource_type", base)
	if err != nil {
		return AuditFacets{}, err
	}
	return AuditFacets{Actors: actors, Actions: actions, ResourceTypes: resources}, nil
}

func auditFacetValues(ctx context.Context, db *sql.DB, column string, filter AuditFilter) ([]string, error) {
	where, args := auditWhere(filter)
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT `+column+` FROM audit_events`+where+` AND `+column+` IS NOT NULL AND trim(`+column+`) != '' ORDER BY lower(`+column+`), `+column+` LIMIT 100`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit %s facets: %w", column, err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("store: scan audit %s facet: %w", column, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit %s facets: %w", column, err)
	}
	return values, nil
}

func auditHistogram(ctx context.Context, db *sql.DB, filter AuditFilter, total int) ([]AuditBucket, error) {
	endMillis := filter.Until.UnixMilli()
	startMillis := filter.Since.UnixMilli()
	if filter.Since.IsZero() {
		where, args := auditWhere(filter)
		var earliest sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM audit_events`+where, args...).Scan(&earliest); err != nil {
			return nil, fmt.Errorf("store: find audit histogram start: %w", err)
		}
		if earliest.Valid {
			startMillis = earliest.Int64
		} else {
			startMillis = filter.Until.Add(-24 * time.Hour).UnixMilli()
		}
	}
	spanMillis := endMillis - startMillis
	if spanMillis < auditBucketCount {
		spanMillis = auditBucketCount
	}
	widthMillis := (spanMillis + auditBucketCount - 1) / auditBucketCount
	buckets := make([]AuditBucket, auditBucketCount)
	for index := range buckets {
		start := time.UnixMilli(startMillis + int64(index)*widthMillis).UTC()
		end := time.UnixMilli(startMillis + int64(index+1)*widthMillis).UTC()
		buckets[index] = AuditBucket{Label: auditBucketLabel(start, spanMillis), Start: start, End: end}
	}
	if total == 0 {
		return buckets, nil
	}
	where, args := auditWhere(filter)
	queryArgs := append([]any{startMillis, widthMillis}, args...)
	rows, err := db.QueryContext(ctx, `SELECT ((created_at - ?) / ?), COUNT(*) FROM audit_events`+where+` GROUP BY 1 ORDER BY 1`, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("store: build audit histogram: %w", err)
	}
	defer rows.Close()
	maximum := 0
	for rows.Next() {
		var index, count int
		if err := rows.Scan(&index, &count); err != nil {
			return nil, fmt.Errorf("store: scan audit histogram: %w", err)
		}
		if index < 0 {
			index = 0
		}
		if index >= len(buckets) {
			index = len(buckets) - 1
		}
		buckets[index].Count += count
		if buckets[index].Count > maximum {
			maximum = buckets[index].Count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit histogram: %w", err)
	}
	if maximum > 0 {
		for index := range buckets {
			if buckets[index].Count > 0 {
				buckets[index].Level = (buckets[index].Count*10 + maximum - 1) / maximum
			}
		}
	}
	return buckets, nil
}

func auditBucketLabel(value time.Time, spanMillis int64) string {
	span := time.Duration(spanMillis) * time.Millisecond
	switch {
	case span <= 6*time.Hour:
		return value.Format("15:04")
	case span <= 48*time.Hour:
		return value.Format("Mon 15")
	case span <= 45*24*time.Hour:
		return value.Format("Jan 02")
	default:
		return value.Format("2006-01")
	}
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
