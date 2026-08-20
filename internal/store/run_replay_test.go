package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRunReplayClonesPinnedJobClosureAndIsolatedStep(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "replay")
	source, err := database.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", Ref: "refs/heads/main",
		CommitSHA: "source-sha", SourcePath: "/srv/replay", Environment: json.RawMessage(`{"GLOBAL":"one"}`),
		Jobs: []EnqueueJob{
			{Key: "prepare", Name: "Prepare", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "prepare", Name: "Prepare", Command: "printf prepare"}}},
			{Key: "test", Name: "Test", DependencyKeys: json.RawMessage(`["prepare"]`), Environment: json.RawMessage(`{"JOB":"two"}`), Steps: []EnqueueStep{{Key: "test", Name: "Test", Command: "printf test", WorkingDirectory: "src"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeReplaySource(t, ctx, database, source.ID)
	graph, _ := database.GetRunGraph(ctx, source.ID)
	targetJob, targetStep := graph.Jobs[1].Job, graph.Jobs[1].Steps[0]

	jobOptions, err := database.EvaluateJobReplay(ctx, targetJob.ID)
	if err != nil || !jobOptions.Eligible || jobOptions.SourceRunID != source.ID {
		t.Fatalf("job replay eligibility = %#v, %v", jobOptions, err)
	}
	jobParams := EnqueueReplayParams{Kind: RunLineageJobReplay, SourceJobID: targetJob.ID, Actor: "operator", IdempotencyKey: "job-001"}
	jobRun, err := database.EnqueueRunReplay(ctx, jobParams)
	if err != nil {
		t.Fatal(err)
	}
	jobGraph, _ := database.GetRunGraph(ctx, jobRun.ID)
	if jobRun.TriggerType != "job_replay" || jobRun.CommitSHA == nil || *jobRun.CommitSHA != "source-sha" || len(jobGraph.Jobs) != 2 {
		t.Fatalf("job replay graph = %#v / %#v", jobRun, jobGraph)
	}
	retried, err := database.EnqueueRunReplay(ctx, jobParams)
	if err != nil || retried.ID != jobRun.ID {
		t.Fatalf("idempotent job replay = %#v, %v", retried, err)
	}
	if _, err := database.EnqueueRunReplay(ctx, EnqueueReplayParams{Kind: RunLineageJobReplay, SourceJobID: targetJob.ID, Actor: "operator", IdempotencyKey: "job-002"}); err == nil {
		t.Fatal("concurrent duplicate job replay was accepted")
	} else {
		var conflict *ErrReplayEligibility
		if !errors.As(err, &conflict) || conflict.Code != "active_replay_exists" {
			t.Fatalf("duplicate job replay error = %T %v", err, err)
		}
	}

	stepOptions, err := database.EvaluateStepReplay(ctx, targetStep.ID)
	if err != nil || !stepOptions.Eligible || stepOptions.SourceStepID != targetStep.ID {
		t.Fatalf("step replay eligibility = %#v, %v", stepOptions, err)
	}
	stepRun, err := database.EnqueueRunReplay(ctx, EnqueueReplayParams{Kind: RunLineageStepReplay, SourceStepID: targetStep.ID, Actor: "operator", IdempotencyKey: "step-001"})
	if err != nil {
		t.Fatal(err)
	}
	stepGraph, _ := database.GetRunGraph(ctx, stepRun.ID)
	if len(stepGraph.Jobs) != 1 || len(stepGraph.Jobs[0].Steps) != 1 || string(stepGraph.Jobs[0].Job.DependencyKeys) != "[]" || pointerText(stepGraph.Jobs[0].Steps[0].Command) != "printf test" {
		t.Fatalf("isolated step replay = %#v", stepGraph)
	}
	lineage, err := database.GetRunLineageByIdempotency(ctx, "operator", "step-001")
	if err != nil || lineage.RunID != stepRun.ID || pointerText(lineage.SourceJobID) != targetJob.ID || pointerText(lineage.SourceStepID) != targetStep.ID {
		t.Fatalf("step replay lineage = %#v, %v", lineage, err)
	}

	forged, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: "forged-sha", SourcePath: "/srv/replay", Jobs: []EnqueueJob{{Key: "test", Name: "Test", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "test", Name: "Test", Command: "printf test"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, `INSERT INTO run_lineage (run_id, kind, source_run_id, source_job_id, actor, idempotency_key, created_at) VALUES (?, 'job_replay', ?, ?, 'forged', 'forged', 1)`, forged.ID, source.ID, targetJob.ID); err == nil {
		t.Fatal("forged replay provenance was accepted")
	}
}

func completeReplaySource(t *testing.T, ctx context.Context, database *Store, runID string) {
	t.Helper()
	claimed, err := database.ClaimNextQueuedRun(ctx, "replay-test-worker")
	if err != nil || claimed == nil || claimed.ID != runID {
		t.Fatalf("claim replay source = %#v, %v", claimed, err)
	}
	graph, err := database.GetRunGraph(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range graph.Jobs {
		if _, err := database.TransitionJob(ctx, item.Job.ID, StatusRunning); err != nil {
			t.Fatal(err)
		}
		for _, step := range item.Steps {
			if _, err := database.TransitionStep(ctx, step.ID, StatusRunning); err != nil {
				t.Fatal(err)
			}
			if _, err := database.TransitionStep(ctx, step.ID, StatusSucceeded); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.TransitionJob(ctx, item.Job.ID, StatusSucceeded); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.TransitionRun(ctx, runID, StatusSucceeded); err != nil {
		t.Fatal(err)
	}
}
