package store

import (
	"context"
	"testing"
	"time"
)

func TestExecutionConcurrencyLeaseContentionExpiryAndScopes(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "concurrency")
	firstRun, err := database.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "first"))
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := database.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "second"))
	if err != nil {
		t.Fatal(err)
	}
	secondGraph, err := database.GetRunGraph(ctx, secondRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	first, err := database.AcquireExecutionConcurrency(ctx, AcquireExecutionConcurrencyParams{
		Scope: ExecutionConcurrencyWorkflow, Group: "PROJECT:Deploy", RunID: firstRun.ID,
		HolderID: firstRun.ID, OwnerID: "worker-a", TTL: time.Minute, Now: now,
	})
	if err != nil || !first.Acquired || first.Lease.Group != "project:deploy" {
		t.Fatalf("first workflow lease = %#v, %v", first, err)
	}
	contended, err := database.AcquireExecutionConcurrency(ctx, AcquireExecutionConcurrencyParams{
		Scope: ExecutionConcurrencyWorkflow, Group: "project:deploy", RunID: secondRun.ID,
		HolderID: secondRun.ID, OwnerID: "worker-b", TTL: time.Minute, Now: now.Add(30 * time.Second),
	})
	if err != nil || contended.Acquired || contended.Lease.RunID != firstRun.ID {
		t.Fatalf("contended workflow lease = %#v, %v", contended, err)
	}
	jobLease, err := database.AcquireExecutionConcurrency(ctx, AcquireExecutionConcurrencyParams{
		Scope: ExecutionConcurrencyJob, Group: "project:deploy", RunID: secondRun.ID,
		HolderID: secondGraph.Jobs[0].Job.ID, OwnerID: "worker-b", TTL: time.Minute, Now: now.Add(30 * time.Second),
	})
	if err != nil || !jobLease.Acquired {
		t.Fatalf("separate job scope lease = %#v, %v", jobLease, err)
	}
	takenOver, err := database.AcquireExecutionConcurrency(ctx, AcquireExecutionConcurrencyParams{
		Scope: ExecutionConcurrencyWorkflow, Group: "project:deploy", RunID: secondRun.ID,
		HolderID: secondRun.ID, OwnerID: "worker-b", TTL: time.Minute, Now: now.Add(61 * time.Second),
	})
	if err != nil || !takenOver.Acquired || takenOver.Lease.RunID != secondRun.ID {
		t.Fatalf("expired workflow takeover = %#v, %v", takenOver, err)
	}
	released, err := database.ReleaseExecutionConcurrency(ctx, ExecutionConcurrencyWorkflow, "project:deploy", firstRun.ID, "worker-a")
	if err != nil || released {
		t.Fatalf("stale release = %t, %v", released, err)
	}
	released, err = database.ReleaseExecutionConcurrency(ctx, ExecutionConcurrencyWorkflow, "project:deploy", secondRun.ID, "worker-b")
	if err != nil || !released {
		t.Fatalf("owner release = %t, %v", released, err)
	}
}
