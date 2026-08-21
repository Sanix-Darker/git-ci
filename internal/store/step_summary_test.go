package store

import (
	"errors"
	"strings"
	"testing"
)

func TestStepSummaryPersistsAcrossRestart(t *testing.T) {
	ctx := t.Context()
	databasePath := t.TempDir() + "/summary.db"
	database, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "summary")
	run, err := database.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "summary"))
	if err != nil {
		t.Fatalf("enqueue run: %v", err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	const summary = "# Build\n\n- tests: 42\n"
	updated, err := database.SetStepSummary(ctx, graph.Jobs[0].Steps[0].ID, summary)
	if err != nil || updated.Summary != summary {
		t.Fatalf("set summary = %#v, %v", updated, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.GetRunGraph(ctx, run.ID)
	if err != nil || persisted.Jobs[0].Steps[0].Summary != summary {
		t.Fatalf("persisted graph = %#v, %v", persisted, err)
	}
}

func TestStepSummaryRejectsUnsafeOrOversizedText(t *testing.T) {
	ctx := t.Context()
	database, err := Open(ctx, t.TempDir()+"/summary-guards.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "summary-guards")
	run, err := database.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "summary-guards"))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stepID := graph.Jobs[0].Steps[0].ID
	for name, value := range map[string]string{
		"null":     "bad\x00summary",
		"utf8":     string([]byte{0xff, 0xfe}),
		"oversize": strings.Repeat("x", MaxStepSummaryBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := database.SetStepSummary(ctx, stepID, value); err == nil {
				t.Fatal("unsafe summary unexpectedly accepted")
			}
		})
	}
	if _, err := database.SetStepSummary(ctx, "missing", "summary"); err == nil {
		t.Fatal("missing step summary unexpectedly accepted")
	} else {
		var notFound *ErrNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("missing step error = %T %v", err, err)
		}
	}
}
