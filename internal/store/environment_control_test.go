package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestEnvironmentControlQueriesExposeApprovalAndDeploymentContext(t *testing.T) {
	database, _ := newTestStore(t)
	ctx := context.Background()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "control")
	environment, err := database.UpsertEnvironment(ctx, UpsertEnvironmentParams{
		ProjectID: project.ID, Name: "production", DeploymentTier: DeploymentTierProduction,
		Protected: true, RequiredApprovals: 1, AllowedRefs: []string{"refs/heads/main"},
		ConcurrencyMode: EnvironmentConcurrencyQueue,
	})
	if err != nil {
		t.Fatal(err)
	}
	byID, err := database.GetEnvironmentByID(ctx, environment.ID)
	if err != nil || byID.Name != "production" {
		t.Fatalf("environment by ID = %#v, %v", byID, err)
	}
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", SourcePath: "/srv/control",
		Jobs: []EnqueueJob{{Key: "deploy", Name: "Deploy production", EnvironmentName: "production", DeploymentTier: "production", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "ship", Name: "Ship", Command: "true"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	jobID := graph.Jobs[0].Job.ID
	request, err := database.RequestEnvironmentApproval(ctx, RequestEnvironmentApprovalParams{JobID: jobID, RequestedBy: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := database.ListEnvironmentApprovalRequests(ctx, ListEnvironmentApprovalsParams{ProjectID: project.ID, Status: EnvironmentApprovalPending})
	if err != nil || len(items) != 1 || items[0].ID != request.ID || items[0].EnvironmentName != "production" || items[0].JobName != "Deploy production" {
		t.Fatalf("approval summaries = %#v, %v", items, err)
	}
	if _, err := database.DecideEnvironmentApproval(ctx, DecideEnvironmentApprovalParams{RequestID: request.ID, Decision: EnvironmentApprovalApproved, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	pending, err := database.ListEnvironmentApprovalRequests(ctx, ListEnvironmentApprovalsParams{Status: EnvironmentApprovalPending})
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending approvals = %#v, %v", pending, err)
	}
	deployment, err := database.EnsureDeploymentForJob(ctx, jobID)
	if err != nil || deployment.JobID == nil || *deployment.JobID != jobID || deployment.DeploymentTier != DeploymentTierProduction {
		t.Fatalf("deployment target context = %#v, %v", deployment, err)
	}
}
