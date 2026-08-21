package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestManualJobPausePlayIdempotencyAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "manual.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "manual")
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", SourcePath: "/tmp/manual", Jobs: []EnqueueJob{{Key: "deploy", Name: "Deploy", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "ship", Name: "Ship", Command: "printf ship"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextQueuedRun(ctx, "manual-worker")
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	graph, _ := database.GetRunGraph(ctx, run.ID)
	jobID := graph.Jobs[0].Job.ID
	state, err := database.PauseManualJob(ctx, PauseManualJobParams{JobID: jobID, Blocking: true, Confirmation: "Ship now?"})
	if err != nil || !state.Blocking || state.Confirmation != "Ship now?" {
		t.Fatalf("manual state = %#v, %v", state, err)
	}
	graph, _ = database.GetRunGraph(ctx, run.ID)
	if graph.Run.Status != StatusWaiting || graph.Jobs[0].Job.Status != StatusManual || graph.Jobs[0].Job.ManualState == nil {
		t.Fatalf("paused graph = %#v", graph)
	}
	_, err = database.PlayManualJob(ctx, PlayManualJobParams{RunID: run.ID, JobID: jobID, Actor: "operator", IdempotencyKey: "play-1"})
	var playErr *ErrManualJobPlay
	if !errors.As(err, &playErr) || playErr.Code != "manual_confirmation_required" {
		t.Fatalf("unconfirmed play error = %v", err)
	}
	played, err := database.PlayManualJob(ctx, PlayManualJobParams{RunID: run.ID, JobID: jobID, Actor: "operator", IdempotencyKey: "play-1", Confirmed: true, Variables: map[string]string{"TARGET": "production"}})
	if err != nil || played.Run.Status != StatusQueued || played.Job.Status != StatusQueued || played.Play.Variables["TARGET"] != "production" {
		t.Fatalf("played result = %#v, %v", played, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	graph, err = database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Jobs[0].Job.ManualPlay == nil || graph.Jobs[0].Job.ManualPlay.Actor != "operator" {
		t.Fatalf("restarted graph = %#v, %v", graph, err)
	}
	repeated, err := database.PlayManualJob(ctx, PlayManualJobParams{RunID: run.ID, JobID: jobID, Actor: "operator", IdempotencyKey: "play-1", Confirmed: true})
	if err != nil || !repeated.Idempotent || repeated.Play.ID != played.Play.ID {
		t.Fatalf("idempotent play = %#v, %v", repeated, err)
	}
	_, err = database.PlayManualJob(ctx, PlayManualJobParams{RunID: run.ID, JobID: jobID, Actor: "operator", IdempotencyKey: "play-2", Confirmed: true, Variables: map[string]string{"bad-key": "x"}})
	if !errors.As(err, &playErr) || playErr.Code != "manual_play_variables_invalid" {
		t.Fatalf("invalid variables error = %v", err)
	}
}
