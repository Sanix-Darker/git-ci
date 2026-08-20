package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenFreshDatabaseMigratesAndConfiguresSQLite(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	requiredTables := map[string]bool{
		"schema_migrations":  false,
		"projects":           false,
		"audit_events":       false,
		"runs":               false,
		"jobs":               false,
		"steps":              false,
		"deliveries":         false,
		"schedules":          false,
		"secrets":            false,
		"deployment_targets": false,
	}
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan table name: %v", err)
		}
		if _, ok := requiredTables[name]; ok {
			requiredTables[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate table names: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close table rows: %v", err)
	}
	for table, found := range requiredTables {
		if !found {
			t.Errorf("fresh database is missing %q", table)
		}
	}

	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys pragma: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var journalMode string
	if err := store.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode pragma: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := store.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout pragma: %v", err)
	}
	if busyTimeout != busyTimeoutMillis {
		t.Errorf("busy_timeout = %d, want %d", busyTimeout, busyTimeoutMillis)
	}

	var migrations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 9 {
		t.Errorf("migration count = %d, want 9", migrations)
	}
}

func TestProjectConstraints(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	project, err := store.CreateProject(ctx, testProjectParams("alpha"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if project.ID == "" {
		t.Fatal("created project has no ID")
	}

	duplicate := testProjectParams("alpha")
	duplicate.Name = "Different project"
	_, err = store.CreateProject(ctx, duplicate)
	var conflict *ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate slug error = %v, want *ErrConflict", err)
	}
	if conflict.Resource != "project" || conflict.Field != "slug" || conflict.Value != "alpha" {
		t.Errorf("unexpected conflict: %#v", conflict)
	}

	now := nowUTC().UnixMilli()
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO runs (
			id, project_id, trigger_type, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, "run-with-missing-project", "missing-project", "manual", "queued", now, now)
	if err == nil {
		t.Fatal("run with unknown project unexpectedly satisfied foreign key")
	}
}

func TestProjectCRUDAndOrdering(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	zetaParams := testProjectParams("zeta")
	zetaParams.Active = false
	zeta, err := store.CreateProject(ctx, zetaParams)
	if err != nil {
		t.Fatalf("create zeta: %v", err)
	}
	alpha, err := store.CreateProject(ctx, testProjectParams("alpha"))
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	_, err = store.CreateProject(ctx, testProjectParams("beta"))
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}

	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("listed %d projects, want 3", len(projects))
	}
	for index, slug := range []string{"alpha", "beta", "zeta"} {
		if projects[index].Slug != slug {
			t.Errorf("projects[%d].Slug = %q, want %q", index, projects[index].Slug, slug)
		}
	}

	bySlug, err := store.GetProject(ctx, "alpha")
	if err != nil {
		t.Fatalf("get project by slug: %v", err)
	}
	if bySlug.ID != alpha.ID || bySlug.Name != alpha.Name {
		t.Errorf("project by slug = %#v, want created alpha %#v", bySlug, alpha)
	}
	if bySlug.CanonicalPath == nil || alpha.CanonicalPath == nil || *bySlug.CanonicalPath != *alpha.CanonicalPath {
		t.Errorf("canonical path = %v, want %v", bySlug.CanonicalPath, alpha.CanonicalPath)
	}
	if bySlug.RepositoryURL == nil || alpha.RepositoryURL == nil || *bySlug.RepositoryURL != *alpha.RepositoryURL {
		t.Errorf("repository URL = %v, want %v", bySlug.RepositoryURL, alpha.RepositoryURL)
	}

	byID, err := store.GetProject(ctx, zeta.ID)
	if err != nil {
		t.Fatalf("get project by ID: %v", err)
	}
	if byID.Slug != zeta.Slug || byID.Active {
		t.Errorf("project by ID = %#v, want inactive zeta", byID)
	}
	if !byID.CreatedAt.Equal(byID.UpdatedAt) {
		t.Errorf("created_at = %s, updated_at = %s, want equal creation timestamps", byID.CreatedAt, byID.UpdatedAt)
	}

	_, err = store.GetProject(ctx, "does-not-exist")
	var notFound *ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("missing project error = %v, want *ErrNotFound", err)
	}
	if notFound.Resource != "project" || notFound.Key != "does-not-exist" {
		t.Errorf("unexpected not-found error: %#v", notFound)
	}
}

func TestProjectPersistsAfterRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "store.db")

	first, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	project, err := first.CreateProject(ctx, testProjectParams("restartable"))
	if err != nil {
		first.Close()
		t.Fatalf("create project: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second store: %v", err)
		}
	})

	got, err := second.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("get persisted project: %v", err)
	}
	if got.ID != project.ID || got.Slug != project.Slug || got.Name != project.Name {
		t.Errorf("persisted project = %#v, want %#v", got, project)
	}
}

func TestRecordAudit(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	project, err := store.CreateProject(ctx, testProjectParams("audited"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	recorded, err := store.RecordAudit(ctx, AuditEvent{
		ProjectID:    project.ID,
		Action:       "project.created",
		Actor:        "cli",
		ResourceType: "project",
		ResourceID:   project.ID,
		Metadata:     json.RawMessage(`{"source":"test"}`),
	})
	if err != nil {
		t.Fatalf("record audit event: %v", err)
	}
	if recorded.ID == "" {
		t.Fatal("recorded audit event has no ID")
	}
	if recorded.CreatedAt.IsZero() {
		t.Fatal("recorded audit event has no creation timestamp")
	}

	var (
		storedProjectID string
		storedAction    string
		storedMetadata  string
		storedCreatedAt int64
	)
	if err := store.db.QueryRowContext(ctx, `
		SELECT project_id, action, metadata_json, created_at
		FROM audit_events
		WHERE id = ?
	`, recorded.ID).Scan(&storedProjectID, &storedAction, &storedMetadata, &storedCreatedAt); err != nil {
		t.Fatalf("read audit event: %v", err)
	}
	if storedProjectID != project.ID || storedAction != "project.created" {
		t.Errorf("stored audit fields = (%q, %q), want (%q, %q)", storedProjectID, storedAction, project.ID, "project.created")
	}
	if storedMetadata != `{"source":"test"}` {
		t.Errorf("stored metadata = %q", storedMetadata)
	}
	if storedCreatedAt != recorded.CreatedAt.UnixMilli() {
		t.Errorf("stored created_at = %d, want %d", storedCreatedAt, recorded.CreatedAt.UnixMilli())
	}

	_, err = store.RecordAudit(ctx, AuditEvent{
		ProjectID: "missing-project",
		Action:    "project.checked",
	})
	var notFound *ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("missing audit project error = %v, want *ErrNotFound", err)
	}
}

func TestConcurrentReadersAndWriters(t *testing.T) {
	store, _ := newTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const (
		writers = 24
		readers = 6
		reads   = 20
	)

	start := make(chan struct{})
	errs := make(chan error, writers+readers)
	var waitGroup sync.WaitGroup

	for writer := 0; writer < writers; writer++ {
		writer := writer
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := store.CreateProject(ctx, testProjectParams(fmt.Sprintf("concurrent-%02d", writer)))
			if err != nil {
				errs <- fmt.Errorf("writer %d: %w", writer, err)
			}
		}()
	}
	for reader := 0; reader < readers; reader++ {
		reader := reader
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			for read := 0; read < reads; read++ {
				if _, err := store.ListProjects(ctx); err != nil {
					errs <- fmt.Errorf("reader %d, read %d: %w", reader, read, err)
					return
				}
			}
		}()
	}

	close(start)
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list concurrent projects: %v", err)
	}
	if len(projects) != writers {
		t.Errorf("project count = %d, want %d", len(projects), writers)
	}
}

func TestInvalidInputs(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, ""); err == nil {
		t.Fatal("Open accepted an empty database path")
	}
	if _, err := Open(nil, filepath.Join(t.TempDir(), "store.db")); err == nil {
		t.Fatal("Open accepted a nil context")
	}

	store, _ := newTestStore(t)
	invalidProjects := []CreateProjectParams{
		{Name: "name", SourceType: "local", DefaultBranch: "main"},
		{Slug: "slug", SourceType: "local", DefaultBranch: "main"},
		{Slug: "slug", Name: "name", DefaultBranch: "main"},
		{Slug: "slug", Name: "name", SourceType: "local"},
	}
	for index, params := range invalidProjects {
		if _, err := store.CreateProject(ctx, params); err == nil {
			t.Errorf("invalid project %d was accepted: %#v", index, params)
		}
	}
	blankPath := " "
	params := testProjectParams("blank-path")
	params.CanonicalPath = &blankPath
	if _, err := store.CreateProject(ctx, params); err == nil {
		t.Error("project with blank canonical path was accepted")
	}

	if _, err := store.GetProject(ctx, " "); err == nil {
		t.Error("GetProject accepted an empty key")
	}
	invalidEvents := []AuditEvent{
		{},
		{Action: "event", Metadata: json.RawMessage(`{`)},
		{ID: "caller-provided", Action: "event"},
		{Action: "event", CreatedAt: nowUTC()},
	}
	for index, event := range invalidEvents {
		if _, err := store.RecordAudit(ctx, event); err == nil {
			t.Errorf("invalid audit event %d was accepted: %#v", index, event)
		}
	}

	var projects int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projects != 0 {
		t.Errorf("invalid input created %d projects, want 0", projects)
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	return store, databasePath
}

func testProjectParams(slug string) CreateProjectParams {
	canonicalPath := "/work/" + slug
	repositoryURL := "https://example.invalid/" + slug + ".git"
	return CreateProjectParams{
		Slug:          slug,
		Name:          "Project " + slug,
		SourceType:    "git",
		CanonicalPath: &canonicalPath,
		RepositoryURL: &repositoryURL,
		DefaultBranch: "main",
		Active:        true,
	}
}
