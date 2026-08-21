package store

import (
	"errors"
	"strings"
	"testing"
)

func TestStepAnnotationsPersistHydrateAndEnforceBounds(t *testing.T) {
	ctx := t.Context()
	databasePath := t.TempDir() + "/annotations.db"
	database, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "annotations")
	run, err := database.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "annotations"))
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	stepID := graph.Jobs[0].Steps[0].ID
	line, column := 12, 4
	created, err := database.AppendStepAnnotation(ctx, AppendStepAnnotationParams{
		StepID: stepID, Level: AnnotationWarning, Message: "compile warning",
		Title: "Compiler", File: "src/app.go", StartLine: &line, StartColumn: &column,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < MaxStepAnnotations; index++ {
		if _, err := database.AppendStepAnnotation(ctx, AppendStepAnnotationParams{StepID: stepID, Level: AnnotationNotice, Message: "bounded notice"}); err != nil {
			t.Fatalf("append annotation %d: %v", index, err)
		}
	}
	if _, err := database.AppendStepAnnotation(ctx, AppendStepAnnotationParams{StepID: stepID, Level: AnnotationError, Message: "over limit"}); err == nil {
		t.Fatal("annotation over the per-step limit unexpectedly accepted")
	}
	if _, err := database.AppendStepAnnotation(ctx, AppendStepAnnotationParams{StepID: stepID, Level: AnnotationError, Message: strings.Repeat("x", MaxAnnotationMessageSize+1)}); err == nil {
		t.Fatal("oversized annotation unexpectedly accepted")
	}
	if _, err := database.AppendStepAnnotation(ctx, AppendStepAnnotationParams{StepID: "missing", Level: AnnotationError, Message: "missing"}); err == nil {
		t.Fatal("missing step unexpectedly accepted")
	} else {
		var notFound *ErrNotFound
		if !errors.As(err, &notFound) {
			t.Fatalf("missing step error = %T %v", err, err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	persisted, err := reopened.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	annotations := persisted.Jobs[0].Steps[0].Annotations
	found := false
	for _, item := range annotations {
		if item.ID == created.ID && item.StartLine != nil && *item.StartLine == line {
			found = true
		}
	}
	if len(annotations) != MaxStepAnnotations || !found {
		t.Fatalf("persisted annotations = %#v", annotations)
	}
}
