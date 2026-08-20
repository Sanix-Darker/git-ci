package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestEnqueueDeploymentRollbackClonesClosureAndLineage(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "rollback")
	targetRun, targetDeployment := rollbackDeploymentFixture(t, ctx, database, project.ID, workflow.ID, "target-sha", StatusSucceeded, "production", true)
	sourceRun, sourceDeployment := rollbackDeploymentFixture(t, ctx, database, project.ID, workflow.ID, "source-sha", StatusFailed, "production", true)
	if _, err := database.db.ExecContext(ctx, `
		INSERT INTO run_lineage (run_id, kind, source_run_id, source_deployment_id, target_deployment_id, actor, idempotency_key, created_at)
		VALUES (?, 'rollback', ?, ?, ?, 'forged', 'forged-source', 1)
	`, sourceRun.ID, targetRun.ID, sourceDeployment.ID, targetDeployment.ID); err == nil {
		t.Fatal("forged rollback source run lineage was accepted")
	}

	eligibility, err := database.EvaluateDeploymentRollback(ctx, sourceDeployment.ID)
	if err != nil || !eligibility.Eligible || len(eligibility.Targets) != 1 || eligibility.Targets[0].DeploymentID != targetDeployment.ID {
		t.Fatalf("rollback eligibility = %#v, %v", eligibility, err)
	}
	params := EnqueueRollbackParams{SourceDeploymentID: sourceDeployment.ID, TargetDeploymentID: targetDeployment.ID, Actor: "operator", IdempotencyKey: "rollback-001"}
	run, err := database.EnqueueDeploymentRollback(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if run.TriggerType != "rollback" || run.CommitSHA == nil || *run.CommitSHA != *targetRun.CommitSHA {
		t.Fatalf("rollback run = %#v", run)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || len(graph.Jobs) != 2 {
		t.Fatalf("rollback graph = %#v, %v", graph, err)
	}
	deploy := graph.Jobs[1]
	if len(deploy.Steps) != 2 || deploy.Steps[0].Command == nil || *deploy.Steps[0].Command != "printf rollback" || deploy.Steps[1].Command == nil || *deploy.Steps[1].Command != "printf verify" {
		t.Fatalf("rollback steps = %#v", deploy.Steps)
	}
	lineage, err := database.GetRunLineageByIdempotency(ctx, "operator", "rollback-001")
	if err != nil || lineage.RunID != run.ID || pointerText(lineage.TargetDeploymentID) != targetDeployment.ID {
		t.Fatalf("rollback lineage = %#v, %v", lineage, err)
	}
	retried, err := database.EnqueueDeploymentRollback(ctx, params)
	if err != nil || retried.ID != run.ID {
		t.Fatalf("idempotent rollback = %#v, %v", retried, err)
	}
	if _, err := database.EnqueueDeploymentRollback(ctx, EnqueueRollbackParams{SourceDeploymentID: sourceDeployment.ID, TargetDeploymentID: targetDeployment.ID, Actor: "operator", IdempotencyKey: "rollback-002"}); err == nil {
		t.Fatal("second active rollback was accepted")
	} else {
		var conflict *ErrConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("active rollback error = %T %v", err, err)
		}
	}
	rollbackDeployment, err := database.EnsureDeploymentForJob(ctx, deploy.Job.ID)
	if err != nil || pointerText(rollbackDeployment.SourceDeploymentID) != sourceDeployment.ID || pointerText(rollbackDeployment.TargetDeploymentID) != targetDeployment.ID {
		t.Fatalf("rollback deployment = %#v, %v", rollbackDeployment, err)
	}
}

func TestRollbackRejectsCrossEnvironmentAndDeploymentAncestor(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "rollback-reject")
	_, target := rollbackDeploymentFixture(t, ctx, database, project.ID, workflow.ID, "target", StatusSucceeded, "staging", true)
	_, source := rollbackDeploymentFixture(t, ctx, database, project.ID, workflow.ID, "source", StatusFailed, "production", true)
	_, err := database.EnqueueDeploymentRollback(ctx, EnqueueRollbackParams{SourceDeploymentID: source.ID, TargetDeploymentID: target.ID, Actor: "operator", IdempotencyKey: "cross-env"})
	var ineligible *ErrRollbackEligibility
	if !errors.As(err, &ineligible) || ineligible.Code != "target_mismatch" {
		t.Fatalf("cross environment error = %T %v", err, err)
	}
}

func rollbackDeploymentFixture(t *testing.T, ctx context.Context, database *Store, projectID, workflowID, commit string, status Status, environment string, rollback bool) (Run, Deployment) {
	t.Helper()
	rollbackCommand, verifyCommand := "", ""
	if rollback {
		rollbackCommand, verifyCommand = "printf rollback", "printf verify"
	}
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: projectID, WorkflowID: workflowID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: commit, SourcePath: "/srv/rollback", Jobs: []EnqueueJob{
		{Key: "build", Name: "Build", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "build", Name: "Build", Command: "printf build"}}},
		{Key: "deploy", Name: "Deploy", EnvironmentName: environment, DeploymentTier: "production", DependencyKeys: json.RawMessage(`["build"]`), RollbackCommand: rollbackCommand, VerifyCommand: verifyCommand, Steps: []EnqueueStep{{Key: "deploy", Name: "Deploy", Command: "printf deploy"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	graph, _ := database.GetRunGraph(ctx, run.ID)
	deployment, err := database.EnsureDeploymentForJob(ctx, graph.Jobs[1].Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = database.TransitionDeployment(ctx, deployment.ID, StatusRunning, nil)
	if err == nil {
		deployment, err = database.TransitionDeployment(ctx, deployment.ID, status, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return run, deployment
}
