package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestWorkflowRunDispatchSurvivesRestartAndLinkIsImmutableAndIdempotent(t *testing.T) {
	ctx := context.Background()
	database, path := newTestStore(t)
	project, err := database.CreateProject(ctx, testProjectParams("workflow-run"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	definition := json.RawMessage(`{"provider":"github","jobs":[{"key":"build"}]}`)
	sourceWorkflow, err := database.UpsertWorkflow(ctx, UpsertWorkflowParams{ProjectID: project.ID, Key: "github:ci", Name: "CI", Definition: definition, Environment: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("create source workflow: %v", err)
	}
	targetWorkflow, err := database.UpsertWorkflow(ctx, UpsertWorkflowParams{ProjectID: project.ID, Key: "github:cd", Name: "CD", Definition: definition, Environment: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("create target workflow: %v", err)
	}
	job := EnqueueJob{Key: "build", Name: "Build", Environment: json.RawMessage(`{}`), DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "step", Name: "Step", Command: "true", Environment: json.RawMessage(`{}`)}}}
	source, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: sourceWorkflow.ID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: "0123456789abcdef", SourcePath: "/work/workflow-run", Environment: json.RawMessage(`{}`), Jobs: []EnqueueJob{job}})
	if err != nil {
		t.Fatalf("enqueue source: %v", err)
	}
	if _, err := database.TransitionRun(ctx, source.ID, StatusRunning); err != nil {
		t.Fatalf("start source: %v", err)
	}
	if _, err := database.TransitionRun(ctx, source.ID, StatusFailed); err != nil {
		t.Fatalf("finish source: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	restarted, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	dispatches, err := restarted.ListPendingWorkflowRunDispatches(ctx, 16)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("pending dispatches = %#v, %v", dispatches, err)
	}
	dispatch := dispatches[0]
	if dispatch.SourceRunID != source.ID || dispatch.SourceWorkflowName != "CI" || dispatch.SourceWorkflowRevision != sourceWorkflow.Revision || dispatch.Conclusion != WorkflowRunFailure {
		t.Fatalf("dispatch = %#v", dispatch)
	}
	link := EnqueueWorkflowRunLink{SourceRunID: source.ID, SourceWorkflowName: dispatch.SourceWorkflowName, SourceWorkflowRevision: dispatch.SourceWorkflowRevision, SourceConclusion: dispatch.Conclusion, TargetWorkflowID: targetWorkflow.ID, TargetWorkflowRevision: targetWorkflow.Revision, Depth: 1, IdempotencyKey: "workflow_run:" + source.ID + ":" + targetWorkflow.ID}
	downstream, err := restarted.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: targetWorkflow.ID, TriggerType: "workflow_run", Ref: "refs/heads/main", CommitSHA: "0123456789abcdef", SourcePath: "/work/workflow-run", Environment: json.RawMessage(`{}`), Jobs: []EnqueueJob{job}, WorkflowRun: &link})
	if err != nil {
		t.Fatalf("enqueue downstream: %v", err)
	}
	graph, err := restarted.GetRunGraph(ctx, downstream.ID)
	if err != nil || graph.WorkflowRun == nil || graph.WorkflowRun.SourceRunID != source.ID || graph.WorkflowRun.Depth != 1 {
		t.Fatalf("downstream graph workflow_run = %#v, %v", graph.WorkflowRun, err)
	}
	byKey, err := restarted.GetWorkflowRunLinkByIdempotency(ctx, link.IdempotencyKey)
	if err != nil || byKey.RunID != downstream.ID {
		t.Fatalf("idempotency lookup = %#v, %v", byKey, err)
	}
	if _, err := restarted.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: targetWorkflow.ID, TriggerType: "workflow_run", Ref: "refs/heads/main", CommitSHA: "0123456789abcdef", SourcePath: "/work/workflow-run", Environment: json.RawMessage(`{}`), Jobs: []EnqueueJob{job}, WorkflowRun: &link}); err == nil {
		t.Fatal("duplicate workflow_run target was accepted")
	}
	if err := restarted.MarkWorkflowRunDispatched(ctx, source.ID); err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	dispatches, err = restarted.ListPendingWorkflowRunDispatches(ctx, 16)
	if err != nil || len(dispatches) != 0 {
		t.Fatalf("dispatches after acknowledgement = %#v, %v", dispatches, err)
	}
	if _, err := restarted.db.ExecContext(ctx, `UPDATE workflow_run_links SET depth = 2 WHERE run_id = ?`, downstream.ID); err == nil {
		t.Fatal("immutable workflow_run link was updated")
	}
	if _, err := restarted.GetWorkflowRunLink(ctx, "missing"); err == nil {
		t.Fatal("missing workflow_run link returned no error")
	} else {
		var notFound *ErrNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("missing workflow_run link error = %T %v", err, err)
		}
	}
}

func TestWorkflowRunMigrationDoesNotBackfillTerminalRuns(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy-workflow-run.db")
	legacy, err := sql.Open(sqliteDriver, databasePath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if err := configureDatabase(ctx, legacy); err != nil {
		legacy.Close()
		t.Fatalf("configure legacy database: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			version TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		legacy.Close()
		t.Fatalf("create migration table: %v", err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		legacy.Close()
		t.Fatalf("load migrations: %v", err)
	}
	for _, migration := range migrations {
		if migration.version >= "0009_workflow_runs" {
			break
		}
		if _, err := legacy.ExecContext(ctx, migration.sql); err != nil {
			legacy.Close()
			t.Fatalf("apply migration %s: %v", migration.version, err)
		}
		if _, err := legacy.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, checksum, applied_at)
			VALUES (?, ?, ?)
		`, migration.version, migration.checksum, nowUTC().UnixMilli()); err != nil {
			legacy.Close()
			t.Fatalf("record migration %s: %v", migration.version, err)
		}
	}
	now := nowUTC().UnixMilli()
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO projects (
			id, slug, name, source_type, canonical_path, repository_url, default_branch, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "legacy-project", "legacy-workflow-run", "Legacy workflow_run", "git", "/work/legacy-workflow-run", "https://example.invalid/legacy.git", "main", 1, now, now); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy project: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO workflows (
			id, project_id, workflow_key, name, definition_json, environment_json, revision, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "legacy-workflow", "legacy-project", "github:ci", "Legacy CI", `{"provider":"github","jobs":[{"key":"build"}]}`, `{}`, 1, 1, now, now); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy workflow: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO runs (
			id, project_id, workflow_id, workflow_key, workflow_revision, trigger_type, status, ref, commit_sha, started_at, finished_at, created_at, updated_at, environment_json, source_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "legacy-terminal-run", "legacy-project", "legacy-workflow", "github:ci", 1, "manual", StatusSucceeded, "main", "0123456789abcdef", now, now, now, now, `{}`, "/work/legacy-workflow-run"); err != nil {
		legacy.Close()
		t.Fatalf("insert terminal run before workflow_run migration: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	migrated, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	dispatches, err := migrated.ListPendingWorkflowRunDispatches(ctx, 16)
	if err != nil || len(dispatches) != 0 {
		t.Fatalf("pre-migration terminal dispatches = %#v, %v", dispatches, err)
	}
	if _, err := migrated.db.ExecContext(ctx, `
		INSERT INTO runs (
			id, project_id, workflow_id, workflow_key, workflow_revision, trigger_type, status, ref, commit_sha, started_at, created_at, updated_at, environment_json, source_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "post-migration-run", "legacy-project", "legacy-workflow", "github:ci", 1, "manual", StatusRunning, "main", "fedcba9876543210", now, now, now, `{}`, "/work/legacy-workflow-run"); err != nil {
		t.Fatalf("insert post-migration run: %v", err)
	}
	if _, err := migrated.db.ExecContext(ctx, `
		UPDATE runs SET status = ?, finished_at = ?, updated_at = ?
		WHERE id = ?
	`, StatusSucceeded, now, now, "post-migration-run"); err != nil {
		t.Fatalf("finish post-migration run: %v", err)
	}
	dispatches, err = migrated.ListPendingWorkflowRunDispatches(ctx, 16)
	if err != nil || len(dispatches) != 1 || dispatches[0].SourceRunID != "post-migration-run" || dispatches[0].Conclusion != WorkflowRunSuccess {
		t.Fatalf("post-migration dispatches = %#v, %v", dispatches, err)
	}
}
