package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectLifecycleIsReversibleAndDisablesAutomationAtomically(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "gci.db")
	database, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	checkout := filepath.Join(t.TempDir(), "checkout")
	project, err := database.CreateProject(ctx, CreateProjectParams{
		Slug: "lifecycle", Name: "Lifecycle", SourceType: "local",
		CanonicalPath: &checkout, DefaultBranch: "main", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflows (id, project_id, workflow_key, name, definition_json, environment_json, revision, active, created_at, updated_at) VALUES (?, ?, ?, ?, '{}', '{}', 1, 1, ?, ?)`, []any{"workflow-lifecycle", project.ID, "ci", "CI", now, now}},
		{`INSERT INTO project_commit_triggers (project_id, ref, enabled, created_at, updated_at) VALUES (?, 'main', 1, ?, ?)`, []any{project.ID, now, now}},
		{`INSERT INTO schedules (id, project_id, expression, active, next_run_at, created_at, updated_at, workflow_id, ref, timezone) VALUES (?, ?, '*/5 * * * *', 1, ?, ?, ?, ?, 'main', 'UTC')`, []any{"schedule-lifecycle", project.ID, now, now, now, "workflow-lifecycle"}},
		{`INSERT INTO schedule_claims (schedule_id, due_at, claimed_at) VALUES (?, ?, ?)`, []any{"schedule-lifecycle", now, now}},
		{`INSERT INTO webhook_endpoints (id, project_id, name, provider, token_hash, metadata_json, enabled, created_at, updated_at) VALUES (?, ?, 'push', 'github', ?, '{}', 1, ?, ?)`, []any{"webhook-lifecycle", project.ID, []byte("token-hash"), now, now}},
		{`INSERT INTO secrets (id, project_id, name, provider, key_reference, version, created_at, updated_at) VALUES (?, ?, 'TOKEN', 'local', 'key', '1', ?, ?)`, []any{"secret-lifecycle", project.ID, now, now}},
		{`INSERT INTO runs (id, project_id, trigger_type, status, created_at, updated_at) VALUES (?, ?, 'manual', 'succeeded', ?, ?)`, []any{"run-complete", project.ID, now, now}},
		{`INSERT INTO runs (id, project_id, trigger_type, status, created_at, updated_at) VALUES (?, ?, 'manual', 'queued', ?, ?)`, []any{"run-active", project.ID, now, now}},
	}
	for _, statement := range statements {
		if _, err := database.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed lifecycle state: %v", err)
		}
	}

	if _, err := database.DeactivateProject(ctx, project.ID, "wrong-slug"); err == nil {
		t.Fatal("deactivation accepted the wrong confirmation slug")
	}
	_, err = database.DeactivateProject(ctx, project.ID, project.Slug)
	var conflict *ErrConflict
	if !errors.As(err, &conflict) || conflict.Field != "activeRuns" {
		t.Fatalf("active run deactivation error = %v", err)
	}
	if _, err := database.db.ExecContext(ctx, `DELETE FROM runs WHERE id = 'run-active'`); err != nil {
		t.Fatal(err)
	}

	inactive, err := database.DeactivateProject(ctx, project.ID, project.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if inactive.Active {
		t.Fatal("deactivated project is still active")
	}
	if _, err := database.DeactivateProject(ctx, project.ID, project.Slug); err != nil {
		t.Fatalf("idempotent deactivation: %v", err)
	}
	activeProjects, err := database.ListActiveProjects(ctx)
	if err != nil || len(activeProjects) != 0 {
		t.Fatalf("active projects = %#v, err=%v", activeProjects, err)
	}
	inactiveProjects, err := database.ListInactiveProjects(ctx)
	if err != nil || len(inactiveProjects) != 1 || inactiveProjects[0].ID != project.ID {
		t.Fatalf("inactive projects = %#v, err=%v", inactiveProjects, err)
	}

	for name, query := range map[string]string{
		"workflow":       `SELECT active FROM workflows WHERE id = 'workflow-lifecycle'`,
		"commit trigger": `SELECT enabled FROM project_commit_triggers WHERE project_id = '` + project.ID + `'`,
		"schedule":       `SELECT active FROM schedules WHERE id = 'schedule-lifecycle'`,
		"webhook":        `SELECT enabled FROM webhook_endpoints WHERE id = 'webhook-lifecycle'`,
	} {
		var enabled int
		if err := database.db.QueryRowContext(ctx, query).Scan(&enabled); err != nil || enabled != 0 {
			t.Fatalf("%s enabled=%d err=%v", name, enabled, err)
		}
	}
	for name, query := range map[string]string{
		"schedule claim": `SELECT COUNT(*) FROM schedule_claims WHERE schedule_id = 'schedule-lifecycle'`,
		"next run":       `SELECT COUNT(*) FROM schedules WHERE id = 'schedule-lifecycle' AND next_run_at IS NOT NULL`,
	} {
		var count int
		if err := database.db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", name, count, err)
		}
	}
	for name, query := range map[string]string{
		"completed run": `SELECT COUNT(*) FROM runs WHERE id = 'run-complete'`,
		"secret":        `SELECT COUNT(*) FROM secrets WHERE id = 'secret-lifecycle'`,
	} {
		var count int
		if err := database.db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", name, count, err)
		}
	}

	reactivated, err := database.CreateProject(ctx, CreateProjectParams{
		Slug: "replacement-slug", Name: "Lifecycle reactivated", SourceType: "local",
		CanonicalPath: &checkout, DefaultBranch: "trunk", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.ID != project.ID || reactivated.Slug != project.Slug || !reactivated.Active || reactivated.DefaultBranch != "trunk" {
		t.Fatalf("reactivated project = %#v", reactivated)
	}
	var workflowActive, triggerEnabled, scheduleActive, webhookEnabled int
	if err := database.db.QueryRowContext(ctx, `SELECT active FROM workflows WHERE id = 'workflow-lifecycle'`).Scan(&workflowActive); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT enabled FROM project_commit_triggers WHERE project_id = ?`, project.ID).Scan(&triggerEnabled); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT active FROM schedules WHERE id = 'schedule-lifecycle'`).Scan(&scheduleActive); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT enabled FROM webhook_endpoints WHERE id = 'webhook-lifecycle'`).Scan(&webhookEnabled); err != nil {
		t.Fatal(err)
	}
	if workflowActive != 1 || triggerEnabled != 0 || scheduleActive != 0 || webhookEnabled != 0 {
		t.Fatalf("reactivation state workflow=%d trigger=%d schedule=%d webhook=%d", workflowActive, triggerEnabled, scheduleActive, webhookEnabled)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetProject(ctx, project.ID)
	if err != nil || !persisted.Active || persisted.ID != project.ID {
		t.Fatalf("persisted project = %#v, err=%v", persisted, err)
	}
}
