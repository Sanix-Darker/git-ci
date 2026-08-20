package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// ListProjects returns every project in stable ascending slug order.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	db, err := s.dbHandle()
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT `+projectColumns+`
		FROM projects
		ORDER BY slug ASC, id ASC
	`)
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
