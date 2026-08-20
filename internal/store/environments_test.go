package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestEnvironmentProtectionApprovalAndLeaseLifecycle(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "protected")
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "push",
		Ref: "refs/heads/main", CommitSHA: "abc123", SourcePath: "/srv/projects/protected",
		Jobs: []EnqueueJob{{
			Key: "deploy", Name: "Deploy", EnvironmentName: "production", DeploymentTier: "production",
			DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "ship", Name: "Ship", Command: "./deploy"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := database.ListDeploymentTargets(ctx, run.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets = %#v, %v", targets, err)
	}

	environment, err := database.UpsertEnvironment(ctx, UpsertEnvironmentParams{
		ProjectID: project.ID, Name: "production", DeploymentTier: DeploymentTierProduction,
		Protected: true, RequiredApprovals: 1, WaitTimerSeconds: 30,
		AllowedRefs:     []string{"refs/tags/v*", "refs/heads/main", "refs/heads/main"},
		ConcurrencyMode: EnvironmentConcurrencyQueue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !EnvironmentAllowsRef(environment, "refs/heads/main") || !EnvironmentAllowsRef(environment, "refs/tags/v1.2.3") || EnvironmentAllowsRef(environment, "refs/heads/feature") {
		t.Fatalf("allowed ref policy = %#v", environment.AllowedRefs)
	}
	if len(environment.AllowedRefs) != 2 || environment.RequiredApprovals != 1 || !environment.Protected {
		t.Fatalf("environment = %#v", environment)
	}
	if _, err := database.UpsertEnvironment(ctx, UpsertEnvironmentParams{
		ProjectID: project.ID, Name: "invalid", RequiredApprovals: 1,
	}); err == nil {
		t.Fatal("unprotected environment accepted approval policy")
	}

	request, err := database.RequestEnvironmentApproval(ctx, RequestEnvironmentApprovalParams{JobID: targets[0].JobID, RequestedBy: "operator"})
	if err != nil || request.Status != EnvironmentApprovalPending {
		t.Fatalf("approval request = %#v, %v", request, err)
	}
	access, err := database.EvaluateEnvironmentAccess(ctx, targets[0].JobID, request.RequestedAt.Add(time.Minute))
	if err != nil || access.Ready || access.Reason != "approval_required" {
		t.Fatalf("pending environment access = %#v, %v", access, err)
	}
	duplicate, err := database.RequestEnvironmentApproval(ctx, RequestEnvironmentApprovalParams{JobID: targets[0].JobID, RequestedBy: "another-operator"})
	if err != nil || duplicate.ID != request.ID || duplicate.RequestedBy != "operator" {
		t.Fatalf("duplicate approval request = %#v, %v", duplicate, err)
	}
	request, err = database.DecideEnvironmentApproval(ctx, DecideEnvironmentApprovalParams{
		RequestID: request.ID, Decision: EnvironmentApprovalApproved, Actor: "operator", Reason: "release window",
	})
	if err != nil || request.Status != EnvironmentApprovalApproved || request.DecidedAt == nil {
		t.Fatalf("approval decision = %#v, %v", request, err)
	}
	access, err = database.EvaluateEnvironmentAccess(ctx, targets[0].JobID, request.RequestedAt.Add(31*time.Second))
	if err != nil || !access.Ready || access.ApprovalStatus == nil || *access.ApprovalStatus != EnvironmentApprovalApproved {
		t.Fatalf("approved environment access = %#v, %v", access, err)
	}
	decisions, err := database.ListEnvironmentApprovalDecisions(ctx, request.ID)
	if err != nil || len(decisions) != 1 || decisions[0].Actor != "operator" || decisions[0].Reason == nil || *decisions[0].Reason != "release window" {
		t.Fatalf("approval decisions = %#v, %v", decisions, err)
	}
	if _, err := database.DecideEnvironmentApproval(ctx, DecideEnvironmentApprovalParams{
		RequestID: request.ID, Decision: EnvironmentApprovalRejected, Actor: "operator",
	}); err == nil {
		t.Fatal("conflicting second approval decision succeeded")
	}

	now := request.RequestedAt.Add(31 * time.Second)
	first, err := database.AcquireEnvironmentLease(ctx, AcquireEnvironmentLeaseParams{JobID: targets[0].JobID, OwnerID: "worker-a", TTL: time.Minute, Now: now})
	if err != nil || !first.Acquired {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	contended, err := database.AcquireEnvironmentLease(ctx, AcquireEnvironmentLeaseParams{JobID: targets[0].JobID, OwnerID: "worker-b", TTL: time.Minute, Now: now.Add(30 * time.Second)})
	if err != nil || contended.Acquired || contended.Lease.OwnerID != "worker-a" {
		t.Fatalf("contended lease = %#v, %v", contended, err)
	}
	takenOver, err := database.AcquireEnvironmentLease(ctx, AcquireEnvironmentLeaseParams{JobID: targets[0].JobID, OwnerID: "worker-b", TTL: time.Minute, Now: now.Add(61 * time.Second)})
	if err != nil || !takenOver.Acquired || takenOver.Lease.OwnerID != "worker-b" {
		t.Fatalf("expired lease takeover = %#v, %v", takenOver, err)
	}
	released, err := database.ReleaseEnvironmentLease(ctx, environment.ID, targets[0].JobID, "worker-a")
	if err != nil || released {
		t.Fatalf("stale lease release = %v, %v", released, err)
	}
	released, err = database.ReleaseEnvironmentLease(ctx, environment.ID, targets[0].JobID, "worker-b")
	if err != nil || !released {
		t.Fatalf("lease release = %v, %v", released, err)
	}
}
