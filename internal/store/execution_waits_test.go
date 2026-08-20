package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestPauseAndResumeJobPersistsDurableWait(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	run, jobID, _ := enqueueProtectedExecutionTestRun(t, ctx, database, "wait")
	claimed, err := database.ClaimNextQueuedRun(ctx, "worker-a")
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if err := database.HeartbeatRunWorker(ctx, run.ID, "worker-a", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	availableAt := time.Now().UTC().Add(time.Minute)
	wait, err := database.PauseJob(ctx, PauseJobParams{
		RunID: run.ID, JobID: jobID, Reason: JobWaitTimer, Detail: "environment wait timer", AvailableAt: &availableAt,
	})
	if err != nil || wait.Reason != JobWaitTimer || wait.AvailableAt == nil {
		t.Fatalf("wait = %#v, %v", wait, err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != StatusWaiting || graph.Jobs[0].Job.Status != StatusWaiting {
		t.Fatalf("paused graph = %#v, %v", graph, err)
	}
	waits, err := database.ListJobWaits(ctx)
	if err != nil || len(waits) != 1 || waits[0].JobID != jobID {
		t.Fatalf("waits = %#v, %v", waits, err)
	}
	if err := database.ResumeJob(ctx, run.ID, jobID); err != nil {
		t.Fatal(err)
	}
	graph, err = database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != StatusQueued || graph.Jobs[0].Job.Status != StatusQueued {
		t.Fatalf("resumed graph = %#v, %v", graph, err)
	}
	waits, err = database.ListJobWaits(ctx)
	if err != nil || len(waits) != 0 {
		t.Fatalf("resumed waits = %#v, %v", waits, err)
	}
}

func TestCancellingWaitingRunTerminatesPendingGraphAndDeployment(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	run, jobID, _ := enqueueProtectedExecutionTestRun(t, ctx, database, "cancel-wait")
	if claimed, err := database.ClaimNextQueuedRun(ctx, "worker-a"); err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if _, err := database.PauseJob(ctx, PauseJobParams{RunID: run.ID, JobID: jobID, Reason: JobWaitApproval, Detail: "approval required"}); err != nil {
		t.Fatal(err)
	}
	cancellation, err := database.RequestRunCancellation(ctx, run.ID)
	if err != nil || !cancellation.Requested {
		t.Fatalf("cancellation = %#v, %v", cancellation, err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != StatusCancelled || graph.Jobs[0].Job.Status != StatusCancelled || graph.Jobs[0].Steps[0].Status != StatusSkipped {
		t.Fatalf("cancelled graph = %#v, %v", graph, err)
	}
	waits, err := database.ListJobWaits(ctx)
	if err != nil || len(waits) != 0 {
		t.Fatalf("cancelled waits = %#v, %v", waits, err)
	}
	deployment, err := database.EnsureDeploymentForJob(ctx, jobID)
	if err != nil || deployment.Status != StatusCancelled || len(deployment.History) != 2 {
		t.Fatalf("cancelled deployment = %#v, %v", deployment, err)
	}
}

func TestRecoverExpiredWorkerRequeuesBeforeStepAndFailsDuringStep(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC()

	safeRun, safeJobID, _ := enqueueProtectedExecutionTestRun(t, ctx, database, "safe-recovery")
	claimed, err := database.ClaimNextQueuedRun(ctx, "worker-safe")
	if err != nil || claimed == nil || claimed.ID != safeRun.ID {
		t.Fatalf("safe claim = %#v, %v", claimed, err)
	}
	if _, err := database.TransitionJob(ctx, safeJobID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := database.HeartbeatRunWorker(ctx, safeRun.ID, "worker-safe", base, time.Minute); err != nil {
		t.Fatal(err)
	}
	recovery, err := database.RecoverExpiredRunWorkers(ctx, base.Add(61*time.Second), base)
	if err != nil || recovery.RequeuedRuns != 1 || recovery.FailedRuns != 0 {
		t.Fatalf("safe recovery = %#v, %v", recovery, err)
	}
	safeGraph, err := database.GetRunGraph(ctx, safeRun.ID)
	if err != nil || safeGraph.Run.Status != StatusQueued || safeGraph.Jobs[0].Job.Status != StatusQueued {
		t.Fatalf("safe recovered graph = %#v, %v", safeGraph, err)
	}
	if _, err := database.TransitionRun(ctx, safeRun.ID, StatusCancelled); err != nil {
		t.Fatal(err)
	}

	unsafeRun, unsafeJobID, unsafeStepID := enqueueProtectedExecutionTestRun(t, ctx, database, "unsafe-recovery")
	claimed, err = database.ClaimNextQueuedRun(ctx, "worker-unsafe")
	if err != nil || claimed == nil || claimed.ID != unsafeRun.ID {
		t.Fatalf("unsafe claim = %#v, %v", claimed, err)
	}
	if _, err := database.TransitionJob(ctx, unsafeJobID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := database.TransitionStep(ctx, unsafeStepID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	unsafeDeployment, err := database.EnsureDeploymentForJob(ctx, unsafeJobID)
	if err != nil {
		t.Fatal(err)
	}
	unsafeDeployment, err = database.TransitionDeployment(ctx, unsafeDeployment.ID, StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.HeartbeatRunWorker(ctx, unsafeRun.ID, "worker-unsafe", base, time.Minute); err != nil {
		t.Fatal(err)
	}
	recovery, err = database.RecoverExpiredRunWorkers(ctx, base.Add(61*time.Second), base)
	if err != nil || recovery.RequeuedRuns != 0 || recovery.FailedRuns != 1 {
		t.Fatalf("unsafe recovery = %#v, %v", recovery, err)
	}
	unsafeGraph, err := database.GetRunGraph(ctx, unsafeRun.ID)
	if err != nil || unsafeGraph.Run.Status != StatusFailed || unsafeGraph.Jobs[0].Job.Status != StatusFailed || unsafeGraph.Jobs[0].Steps[0].Status != StatusFailed {
		t.Fatalf("unsafe recovered graph = %#v, %v", unsafeGraph, err)
	}
	if unsafeGraph.Run.FailureReason == nil || *unsafeGraph.Run.FailureReason == "" {
		t.Fatal("unsafe recovery did not persist failure reason")
	}
	unsafeDeployment, err = database.GetDeployment(ctx, unsafeDeployment.ID)
	if err != nil || unsafeDeployment.Status != StatusFailed || len(unsafeDeployment.History) != 3 {
		t.Fatalf("unsafe recovered deployment = %#v, %v", unsafeDeployment, err)
	}
}

func enqueueProtectedExecutionTestRun(t *testing.T, ctx context.Context, database *Store, slug string) (Run, string, string) {
	t.Helper()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, slug)
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", SourcePath: "/srv/projects/" + slug,
		Jobs: []EnqueueJob{{
			Key: "deploy", Name: "Deploy", EnvironmentName: "production", DeploymentTier: "production",
			DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "ship", Name: "Ship", Command: "true"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureEnvironmentForJob(ctx, graph.Jobs[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	deployment, err := database.EnsureDeploymentForJob(ctx, graph.Jobs[0].Job.ID)
	if err != nil || deployment.RunID != run.ID || deployment.Environment != "production" {
		t.Fatalf("deployment = %#v, %v", deployment, err)
	}
	return run, graph.Jobs[0].Job.ID, graph.Jobs[0].Steps[0].ID
}
