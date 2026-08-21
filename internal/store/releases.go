package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReleaseState string

const (
	ReleaseDraft     ReleaseState = "draft"
	ReleasePublished ReleaseState = "published"
)

type Release struct {
	ID              string       `json:"id"`
	ProjectID       string       `json:"projectId"`
	ProjectName     string       `json:"projectName"`
	RunID           string       `json:"runId"`
	TagName         string       `json:"tagName"`
	TargetCommitSHA string       `json:"targetCommitSha"`
	Name            string       `json:"name"`
	Notes           string       `json:"notes"`
	State           ReleaseState `json:"state"`
	Prerelease      bool         `json:"prerelease"`
	CreatedBy       string       `json:"createdBy"`
	PublishedBy     *string      `json:"publishedBy,omitempty"`
	CreatedAt       time.Time    `json:"createdAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	PublishedAt     *time.Time   `json:"publishedAt,omitempty"`
}

type CreateReleaseParams struct {
	ProjectID, RunID, TagName, TargetCommitSHA, Name, Notes, Actor string
	Prerelease                                                     bool
}

type UpdateReleaseParams struct {
	ReleaseID, Name, Notes string
	Prerelease             bool
}

type ReleaseFilter struct {
	ProjectID string
	State     ReleaseState
	Query     string
	Limit     int
}

const releaseColumns = `
	r.id, r.project_id, p.name, r.run_id, r.tag_name, r.target_commit_sha,
	r.name, r.notes, r.state, r.prerelease, r.created_by, r.published_by,
	r.created_at, r.updated_at, r.published_at`

type releaseScanner interface{ Scan(...any) error }

func scanRelease(row releaseScanner) (Release, error) {
	var item Release
	var prerelease int
	var publishedBy sql.NullString
	var createdAt, updatedAt int64
	var publishedAt sql.NullInt64
	if err := row.Scan(
		&item.ID, &item.ProjectID, &item.ProjectName, &item.RunID, &item.TagName, &item.TargetCommitSHA,
		&item.Name, &item.Notes, &item.State, &prerelease, &item.CreatedBy, &publishedBy,
		&createdAt, &updatedAt, &publishedAt,
	); err != nil {
		return Release{}, err
	}
	item.Prerelease = prerelease == 1
	item.CreatedAt = timeFromMillis(createdAt)
	item.UpdatedAt = timeFromMillis(updatedAt)
	if publishedBy.Valid {
		item.PublishedBy = &publishedBy.String
	}
	if publishedAt.Valid {
		value := timeFromMillis(publishedAt.Int64)
		item.PublishedAt = &value
	}
	return item, nil
}

func (s *Store) CreateRelease(ctx context.Context, params CreateReleaseParams) (Release, error) {
	if err := requireContext(ctx); err != nil {
		return Release{}, err
	}
	var err error
	if params.ProjectID, err = normalizeRequiredText("release project ID", params.ProjectID); err != nil {
		return Release{}, err
	}
	if params.RunID, err = normalizeRequiredText("release run ID", params.RunID); err != nil {
		return Release{}, err
	}
	if params.TagName, err = normalizeReleaseText("release tag", params.TagName, 255, true); err != nil {
		return Release{}, err
	}
	if params.TargetCommitSHA, err = normalizeReleaseObjectID(params.TargetCommitSHA); err != nil {
		return Release{}, err
	}
	if params.Name == "" {
		params.Name = params.TagName
	}
	if params.Name, err = normalizeReleaseText("release name", params.Name, 256, true); err != nil {
		return Release{}, err
	}
	if params.Notes, err = normalizeReleaseText("release notes", params.Notes, 65536, false); err != nil {
		return Release{}, err
	}
	if params.Actor, err = normalizeReleaseText("release actor", params.Actor, 256, true); err != nil {
		return Release{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Release{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Release{}, fmt.Errorf("store: begin create release: %w", err)
	}
	defer tx.Rollback()

	var active int
	if err := tx.QueryRowContext(ctx, `SELECT active FROM projects WHERE id = ?`, params.ProjectID).Scan(&active); errors.Is(err, sql.ErrNoRows) {
		return Release{}, &ErrNotFound{Resource: "project", Key: params.ProjectID}
	} else if err != nil {
		return Release{}, fmt.Errorf("store: inspect release project: %w", err)
	}
	if active != 1 {
		return Release{}, &ErrReleaseTransition{Code: "project_inactive", Message: "release project must be actively registered"}
	}
	var runProjectID, runCommit string
	var runStatus Status
	if err := tx.QueryRowContext(ctx, `SELECT project_id, status, commit_sha FROM runs WHERE id = ?`, params.RunID).Scan(&runProjectID, &runStatus, &runCommit); errors.Is(err, sql.ErrNoRows) {
		return Release{}, &ErrNotFound{Resource: "run", Key: params.RunID}
	} else if err != nil {
		return Release{}, fmt.Errorf("store: inspect release run: %w", err)
	}
	if runProjectID != params.ProjectID {
		return Release{}, &ErrReleaseTransition{Code: "source_ownership_mismatch", Message: "release source run does not belong to the project"}
	}
	if runStatus != StatusSucceeded {
		return Release{}, &ErrReleaseTransition{Code: "source_run_not_successful", Message: "release source run must have succeeded"}
	}
	if !strings.EqualFold(runCommit, params.TargetCommitSHA) {
		return Release{}, &ErrReleaseTransition{Code: "tag_commit_mismatch", Message: "release tag must resolve to the source run commit"}
	}
	id, err := randomOpaqueID()
	if err != nil {
		return Release{}, fmt.Errorf("store: generate release ID: %w", err)
	}
	now := nowUTC()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO releases (
			id, project_id, run_id, tag_name, target_commit_sha, name, notes,
			state, prerelease, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?, ?)
	`, id, params.ProjectID, params.RunID, params.TagName, params.TargetCommitSHA, params.Name, params.Notes, boolToInteger(params.Prerelease), params.Actor, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: releases.project_id, releases.tag_name") {
			return Release{}, &ErrConflict{Resource: "release", Field: "tagName", Value: params.TagName}
		}
		return Release{}, fmt.Errorf("store: create release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Release{}, fmt.Errorf("store: commit create release: %w", err)
	}
	return s.GetRelease(ctx, id)
}

func (s *Store) GetRelease(ctx context.Context, releaseID string) (Release, error) {
	if err := requireContext(ctx); err != nil {
		return Release{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Release{}, err
	}
	releaseID, err = normalizeRequiredText("release ID", releaseID)
	if err != nil {
		return Release{}, err
	}
	item, err := scanRelease(db.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases r JOIN projects p ON p.id = r.project_id WHERE r.id = ?`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, &ErrNotFound{Resource: "release", Key: releaseID}
	}
	if err != nil {
		return Release{}, fmt.Errorf("store: get release: %w", err)
	}
	return item, nil
}

func (s *Store) ListReleases(ctx context.Context, filter ReleaseFilter) ([]Release, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}
	if filter.ProjectID, err = normalizeOptionalText("release project filter", filter.ProjectID); err != nil {
		return nil, err
	}
	if filter.State != "" && filter.State != ReleaseDraft && filter.State != ReleasePublished {
		return nil, invalidInput("release state filter", "must be draft or published")
	}
	if filter.Query, err = normalizeReleaseText("release query", filter.Query, 256, false); err != nil {
		return nil, err
	}
	if filter.ProjectID != "" {
		exists, existsErr := projectIDExists(ctx, db, filter.ProjectID)
		if existsErr != nil {
			return nil, fmt.Errorf("store: inspect release filter project: %w", existsErr)
		}
		if !exists {
			return nil, &ErrNotFound{Resource: "project", Key: filter.ProjectID}
		}
	}
	if filter.Limit <= 0 {
		filter.Limit = 200
	}
	if filter.Limit > 500 {
		return nil, invalidInput("release limit", "must not exceed 500")
	}
	clauses := []string{"1 = 1"}
	arguments := make([]any, 0, 6)
	if filter.ProjectID != "" {
		clauses = append(clauses, "r.project_id = ?")
		arguments = append(arguments, filter.ProjectID)
	}
	if filter.State != "" {
		clauses = append(clauses, "r.state = ?")
		arguments = append(arguments, filter.State)
	}
	if filter.Query != "" {
		pattern := "%" + strings.ToLower(filter.Query) + "%"
		clauses = append(clauses, "(lower(r.tag_name) LIKE ? OR lower(r.name) LIKE ? OR lower(r.notes) LIKE ? OR lower(p.name) LIKE ?)")
		arguments = append(arguments, pattern, pattern, pattern, pattern)
	}
	arguments = append(arguments, filter.Limit)
	rows, err := db.QueryContext(ctx, `SELECT `+releaseColumns+` FROM releases r JOIN projects p ON p.id = r.project_id WHERE `+strings.Join(clauses, " AND ")+` ORDER BY COALESCE(r.published_at, r.created_at) DESC, r.id DESC LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("store: list releases: %w", err)
	}
	defer rows.Close()
	items := make([]Release, 0)
	for rows.Next() {
		item, scanErr := scanRelease(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan release: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate releases: %w", err)
	}
	return items, nil
}

func (s *Store) UpdateRelease(ctx context.Context, params UpdateReleaseParams) (Release, error) {
	if err := requireContext(ctx); err != nil {
		return Release{}, err
	}
	var err error
	if params.ReleaseID, err = normalizeRequiredText("release ID", params.ReleaseID); err != nil {
		return Release{}, err
	}
	if params.Name, err = normalizeReleaseText("release name", params.Name, 256, true); err != nil {
		return Release{}, err
	}
	if params.Notes, err = normalizeReleaseText("release notes", params.Notes, 65536, false); err != nil {
		return Release{}, err
	}
	current, err := s.GetRelease(ctx, params.ReleaseID)
	if err != nil {
		return Release{}, err
	}
	if current.State != ReleaseDraft {
		return Release{}, &ErrReleaseTransition{Code: "release_published", Message: "published releases cannot be edited"}
	}
	db, err := s.dbHandle()
	if err != nil {
		return Release{}, err
	}
	result, err := db.ExecContext(ctx, `UPDATE releases SET name = ?, notes = ?, prerelease = ?, updated_at = ? WHERE id = ? AND state = 'draft'`, params.Name, params.Notes, boolToInteger(params.Prerelease), nowUTC().UnixMilli(), params.ReleaseID)
	if err != nil {
		return Release{}, fmt.Errorf("store: update release: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Release{}, &ErrReleaseTransition{Code: "release_published", Message: "published releases cannot be edited"}
	}
	return s.GetRelease(ctx, params.ReleaseID)
}

func (s *Store) PublishRelease(ctx context.Context, releaseID, actor string) (Release, error) {
	if err := requireContext(ctx); err != nil {
		return Release{}, err
	}
	var err error
	if releaseID, err = normalizeRequiredText("release ID", releaseID); err != nil {
		return Release{}, err
	}
	if actor, err = normalizeReleaseText("release publisher", actor, 256, true); err != nil {
		return Release{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Release{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Release{}, fmt.Errorf("store: begin publish release: %w", err)
	}
	defer tx.Rollback()
	var state ReleaseState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM releases WHERE id = ?`, releaseID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return Release{}, &ErrNotFound{Resource: "release", Key: releaseID}
	} else if err != nil {
		return Release{}, fmt.Errorf("store: inspect release state: %w", err)
	}
	if state == ReleasePublished {
		if err := tx.Commit(); err != nil {
			return Release{}, fmt.Errorf("store: commit idempotent release publication: %w", err)
		}
		return s.GetRelease(ctx, releaseID)
	}
	now := nowUTC()
	result, err := tx.ExecContext(ctx, `UPDATE releases SET state = 'published', published_by = ?, published_at = ?, updated_at = ? WHERE id = ? AND state = 'draft'`, actor, now.UnixMilli(), now.UnixMilli(), releaseID)
	if err != nil {
		return Release{}, fmt.Errorf("store: publish release: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Release{}, &ErrReleaseTransition{Code: "release_transition_conflict", Message: "release publication raced with another transition"}
	}
	if err := tx.Commit(); err != nil {
		return Release{}, fmt.Errorf("store: commit publish release: %w", err)
	}
	return s.GetRelease(ctx, releaseID)
}

func (s *Store) DeleteDraftRelease(ctx context.Context, releaseID string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	current, err := s.GetRelease(ctx, releaseID)
	if err != nil {
		return err
	}
	if current.State != ReleaseDraft {
		return &ErrReleaseTransition{Code: "release_published", Message: "published releases cannot be deleted"}
	}
	db, err := s.dbHandle()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM releases WHERE id = ? AND state = 'draft'`, current.ID)
	if err != nil {
		return fmt.Errorf("store: delete draft release: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return &ErrReleaseTransition{Code: "release_published", Message: "published releases cannot be deleted"}
	}
	return nil
}

func (s *Store) GetLatestRelease(ctx context.Context, projectID string) (Release, error) {
	if err := requireContext(ctx); err != nil {
		return Release{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Release{}, err
	}
	projectID, err = normalizeRequiredText("release project ID", projectID)
	if err != nil {
		return Release{}, err
	}
	exists, err := projectIDExists(ctx, db, projectID)
	if err != nil {
		return Release{}, fmt.Errorf("store: inspect latest release project: %w", err)
	}
	if !exists {
		return Release{}, &ErrNotFound{Resource: "project", Key: projectID}
	}
	item, err := scanRelease(db.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases r JOIN projects p ON p.id = r.project_id WHERE r.project_id = ? AND r.state = 'published' AND r.prerelease = 0 ORDER BY r.published_at DESC, r.id DESC LIMIT 1`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, &ErrNotFound{Resource: "release", Key: "latest:" + projectID}
	}
	if err != nil {
		return Release{}, fmt.Errorf("store: get latest release: %w", err)
	}
	return item, nil
}

func normalizeReleaseText(field, value string, maximum int, required bool) (string, error) {
	value, err := normalizeOptionalText(field, value)
	if err != nil {
		return "", err
	}
	if required && value == "" {
		return "", invalidInput(field, "must not be empty")
	}
	if len(value) > maximum {
		return "", invalidInput(field, fmt.Sprintf("must not exceed %d bytes", maximum))
	}
	return value, nil
}

func normalizeReleaseObjectID(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 40 && len(value) != 64 {
		return "", invalidInput("release target commit", "must be a full object ID")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", invalidInput("release target commit", "must be a hexadecimal object ID")
	}
	return value, nil
}
