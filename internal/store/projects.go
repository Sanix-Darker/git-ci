package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const projectColumns = `
	id,
	slug,
	name,
	source_type,
	canonical_path,
	repository_url,
	default_branch,
	active,
	created_at,
	updated_at`

// Project is a configured source repository or local checkout.
type Project struct {
	ID            string    `json:"id"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	SourceType    string    `json:"sourceType"`
	CanonicalPath *string   `json:"canonicalPath,omitempty"`
	RepositoryURL *string   `json:"repositoryUrl,omitempty"`
	DefaultBranch string    `json:"defaultBranch"`
	Active        bool      `json:"active"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// CreateProjectParams contains the mutable input needed to create a project.
// IDs and timestamps are assigned by Store.
type CreateProjectParams struct {
	Slug          string
	Name          string
	SourceType    string
	CanonicalPath *string
	RepositoryURL *string
	DefaultBranch string
	Active        bool
}

// CreateProject validates and persists a project. Slugs are unique.
func (s *Store) CreateProject(ctx context.Context, params CreateProjectParams) (Project, error) {
	if err := requireContext(ctx); err != nil {
		return Project{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Project{}, err
	}

	params, err = normalizeCreateProjectParams(params)
	if err != nil {
		return Project{}, err
	}
	if params.CanonicalPath != nil {
		existing, lookupErr := scanProject(db.QueryRowContext(ctx, `
			SELECT `+projectColumns+`
			FROM projects
			WHERE canonical_path = ?
			ORDER BY active DESC, updated_at DESC, id ASC
			LIMIT 1
		`, *params.CanonicalPath))
		switch {
		case lookupErr == nil && !existing.Active && params.Active:
			return reactivateProject(ctx, db, existing.ID, params)
		case lookupErr == nil && existing.Slug == params.Slug:
			return Project{}, &ErrConflict{Resource: "project", Field: "slug", Value: params.Slug}
		case lookupErr == nil:
			return Project{}, &ErrConflict{Resource: "project", Field: "canonicalPath", Value: *params.CanonicalPath}
		case !errors.Is(lookupErr, sql.ErrNoRows):
			return Project{}, fmt.Errorf("store: find project by canonical path: %w", lookupErr)
		}
	}
	id, err := randomOpaqueID()
	if err != nil {
		return Project{}, fmt.Errorf("store: generate project ID: %w", err)
	}

	createdAt := nowUTC()
	project := Project{
		ID:            id,
		Slug:          params.Slug,
		Name:          params.Name,
		SourceType:    params.SourceType,
		CanonicalPath: params.CanonicalPath,
		RepositoryURL: params.RepositoryURL,
		DefaultBranch: params.DefaultBranch,
		Active:        params.Active,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO projects (
			id, slug, name, source_type, canonical_path, repository_url,
			default_branch, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		project.ID,
		project.Slug,
		project.Name,
		project.SourceType,
		nullableString(project.CanonicalPath),
		nullableString(project.RepositoryURL),
		project.DefaultBranch,
		boolToInteger(project.Active),
		project.CreatedAt.UnixMilli(),
		project.UpdatedAt.UnixMilli(),
	)
	if err != nil {
		exists, lookupErr := projectSlugExists(ctx, db, project.Slug)
		if lookupErr == nil && exists {
			return Project{}, &ErrConflict{
				Resource: "project",
				Field:    "slug",
				Value:    project.Slug,
			}
		}
		return Project{}, fmt.Errorf("store: create project: %w", err)
	}

	return project, nil
}

// ListProjects returns every project, including inactive history, in stable order.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	return s.listProjects(ctx, nil)
}

// ListActiveProjects returns projects available to operational surfaces.
func (s *Store) ListActiveProjects(ctx context.Context) ([]Project, error) {
	active := true
	return s.listProjects(ctx, &active)
}

// ListInactiveProjects returns unregistered projects whose history is retained.
func (s *Store) ListInactiveProjects(ctx context.Context) ([]Project, error) {
	active := false
	return s.listProjects(ctx, &active)
}

func (s *Store) listProjects(ctx context.Context, active *bool) ([]Project, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}

	query := `SELECT ` + projectColumns + ` FROM projects`
	arguments := make([]any, 0, 1)
	if active != nil {
		query += ` WHERE active = ?`
		arguments = append(arguments, boolToInteger(*active))
	}
	query += ` ORDER BY slug ASC, id ASC`
	rows, err := db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate projects: %w", err)
	}
	return projects, nil
}

// GetProject returns a project by either its opaque ID or its slug.
func (s *Store) GetProject(ctx context.Context, key string) (Project, error) {
	if err := requireContext(ctx); err != nil {
		return Project{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Project{}, err
	}

	key, err = normalizeRequiredText("project key", key)
	if err != nil {
		return Project{}, err
	}
	project, err := scanProject(db.QueryRowContext(ctx, `
		SELECT `+projectColumns+`
		FROM projects
		WHERE id = ? OR slug = ?
		ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END
		LIMIT 1
	`, key, key, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, &ErrNotFound{Resource: "project", Key: key}
	}
	if err != nil {
		return Project{}, fmt.Errorf("store: get project: %w", err)
	}
	return project, nil
}

// DeactivateProject unregisters a project without deleting its checkout or history.
// Every asynchronous trigger source is disabled in the same SQLite transaction.
func (s *Store) DeactivateProject(ctx context.Context, key, confirmSlug string) (Project, error) {
	if err := requireContext(ctx); err != nil {
		return Project{}, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return Project{}, err
	}
	key, err = normalizeRequiredText("project key", key)
	if err != nil {
		return Project{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("store: begin project deactivation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	project, err := scanProject(tx.QueryRowContext(ctx, `
		SELECT `+projectColumns+`
		FROM projects
		WHERE id = ? OR slug = ?
		ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END
		LIMIT 1
	`, key, key, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, &ErrNotFound{Resource: "project", Key: key}
	}
	if err != nil {
		return Project{}, fmt.Errorf("store: load project for deactivation: %w", err)
	}
	if confirmSlug != project.Slug {
		return Project{}, invalidInput("project confirmation", "must exactly match project slug")
	}
	if !project.Active {
		return project, nil
	}

	var activeRuns int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM runs
		WHERE project_id = ? AND status IN ('queued', 'running')
	`, project.ID).Scan(&activeRuns); err != nil {
		return Project{}, fmt.Errorf("store: count active project runs: %w", err)
	}
	if activeRuns > 0 {
		return Project{}, &ErrConflict{Resource: "project", Field: "activeRuns", Value: strconv.Itoa(activeRuns)}
	}

	updatedAt := nowUTC()
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE projects SET active = 0, updated_at = ? WHERE id = ?`, []any{updatedAt.UnixMilli(), project.ID}},
		{`UPDATE workflows SET active = 0, updated_at = ? WHERE project_id = ?`, []any{updatedAt.UnixMilli(), project.ID}},
		{`UPDATE project_commit_triggers SET enabled = 0, updated_at = ? WHERE project_id = ?`, []any{updatedAt.UnixMilli(), project.ID}},
		{`DELETE FROM schedule_claims WHERE schedule_id IN (SELECT id FROM schedules WHERE project_id = ?)`, []any{project.ID}},
		{`UPDATE schedules SET active = 0, next_run_at = NULL, updated_at = ? WHERE project_id = ?`, []any{updatedAt.UnixMilli(), project.ID}},
		{`UPDATE webhook_endpoints SET enabled = 0, updated_at = ? WHERE project_id = ?`, []any{updatedAt.UnixMilli(), project.ID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return Project{}, fmt.Errorf("store: deactivate project resources: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("store: commit project deactivation: %w", err)
	}
	project.Active = false
	project.UpdatedAt = updatedAt
	return project, nil
}

func reactivateProject(ctx context.Context, db *sql.DB, projectID string, params CreateProjectParams) (Project, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("store: begin project reactivation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	project, err := scanProject(tx.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = ?`, projectID))
	if err != nil {
		return Project{}, fmt.Errorf("store: load project for reactivation: %w", err)
	}
	if project.Active {
		return Project{}, &ErrConflict{Resource: "project", Field: "canonicalPath", Value: *params.CanonicalPath}
	}
	updatedAt := nowUTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET name = ?, source_type = ?, canonical_path = ?, repository_url = ?,
			default_branch = ?, active = 1, updated_at = ?
		WHERE id = ?
	`, params.Name, params.SourceType, nullableString(params.CanonicalPath), nullableString(params.RepositoryURL), params.DefaultBranch, updatedAt.UnixMilli(), project.ID); err != nil {
		return Project{}, fmt.Errorf("store: reactivate project: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET active = 1, updated_at = ? WHERE project_id = ?`, updatedAt.UnixMilli(), project.ID); err != nil {
		return Project{}, fmt.Errorf("store: reactivate project workflows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("store: commit project reactivation: %w", err)
	}

	project.Name = params.Name
	project.SourceType = params.SourceType
	project.CanonicalPath = params.CanonicalPath
	project.RepositoryURL = params.RepositoryURL
	project.DefaultBranch = params.DefaultBranch
	project.Active = true
	project.UpdatedAt = updatedAt
	return project, nil
}

func normalizeCreateProjectParams(params CreateProjectParams) (CreateProjectParams, error) {
	var err error
	if params.Slug, err = normalizeRequiredText("project slug", params.Slug); err != nil {
		return CreateProjectParams{}, err
	}
	if params.Name, err = normalizeRequiredText("project name", params.Name); err != nil {
		return CreateProjectParams{}, err
	}
	if params.SourceType, err = normalizeRequiredText("project source type", params.SourceType); err != nil {
		return CreateProjectParams{}, err
	}
	if params.DefaultBranch, err = normalizeRequiredText("project default branch", params.DefaultBranch); err != nil {
		return CreateProjectParams{}, err
	}
	if params.CanonicalPath, err = normalizeOptionalString("project canonical path", params.CanonicalPath); err != nil {
		return CreateProjectParams{}, err
	}
	if params.RepositoryURL, err = normalizeOptionalString("project repository URL", params.RepositoryURL); err != nil {
		return CreateProjectParams{}, err
	}
	return params, nil
}

func normalizeRequiredText(field, value string) (string, error) {
	normalized, err := normalizeOptionalText(field, value)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", invalidInput(field, "must not be empty")
	}
	return normalized, nil
}

func normalizeOptionalText(field, value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if strings.IndexByte(normalized, 0) >= 0 {
		return "", invalidInput(field, "must not contain a NUL byte")
	}
	return normalized, nil
}

func normalizeOptionalString(field string, value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized, err := normalizeRequiredText(field, *value)
	if err != nil {
		return nil, err
	}
	return &normalized, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func projectSlugExists(ctx context.Context, db *sql.DB, slug string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE slug = ? LIMIT 1`, slug).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func projectIDExists(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ? LIMIT 1`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type projectScanner interface {
	Scan(dest ...any) error
}

func scanProject(scanner projectScanner) (Project, error) {
	var (
		project       Project
		canonicalPath sql.NullString
		repositoryURL sql.NullString
		active        int64
		createdAt     int64
		updatedAt     int64
	)
	if err := scanner.Scan(
		&project.ID,
		&project.Slug,
		&project.Name,
		&project.SourceType,
		&canonicalPath,
		&repositoryURL,
		&project.DefaultBranch,
		&active,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Project{}, err
	}

	if canonicalPath.Valid {
		value := canonicalPath.String
		project.CanonicalPath = &value
	}
	if repositoryURL.Valid {
		value := repositoryURL.String
		project.RepositoryURL = &value
	}
	project.Active = active != 0
	project.CreatedAt = timeFromMillis(createdAt)
	project.UpdatedAt = timeFromMillis(updatedAt)
	return project, nil
}
