package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestChildPipelineLineagePausesReconcilesAndHidesChildRun(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "children")
	parent, bridge := enqueueChildPipelineParent(t, ctx, database, project.ID, workflow.ID, ChildPipelineMirror)
	if _, err := database.TransitionRun(ctx, parent.ID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	child, err := database.EnqueueRun(ctx, childPipelineRunParams(project.ID, workflow.ID, parent, bridge, ChildPipelineMirror))
	if err != nil {
		t.Fatal(err)
	}
	parentGraph, _ := database.GetRunGraph(ctx, parent.ID)
	if parentGraph.Run.Status != StatusWaiting || parentGraph.Jobs[0].Job.Status != StatusWaiting || len(parentGraph.ChildPipelines) != 1 || parentGraph.ChildPipelines[0].ChildRunID != child.ID {
		t.Fatalf("paused parent = %#v", parentGraph)
	}
	childGraph, _ := database.GetRunGraph(ctx, child.ID)
	if childGraph.ParentPipeline == nil || childGraph.ParentPipeline.ParentRunID != parent.ID {
		t.Fatalf("child graph = %#v", childGraph)
	}
	runs, _ := database.ListRuns(ctx, project.ID)
	if len(runs) != 1 || runs[0].ID != parent.ID {
		t.Fatalf("root runs = %#v", runs)
	}
	if _, err := database.TransitionRun(ctx, child.ID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := database.TransitionRun(ctx, child.ID, StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	if settled, err := database.ReconcileCompletedChildPipelines(ctx); err != nil || settled != 1 {
		t.Fatalf("settled=%d err=%v", settled, err)
	}
	parentGraph, _ = database.GetRunGraph(ctx, parent.ID)
	if parentGraph.Run.Status != StatusQueued || parentGraph.Jobs[0].Job.Status != StatusSucceeded {
		t.Fatalf("resumed parent = %#v", parentGraph)
	}
}

func TestChildPipelineCancellationCascadesAndLineageIsImmutable(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "child-cancel")
	parent, bridge := enqueueChildPipelineParent(t, ctx, database, project.ID, workflow.ID, ChildPipelineMirror)
	_, _ = database.TransitionRun(ctx, parent.ID, StatusRunning)
	child, err := database.EnqueueRun(ctx, childPipelineRunParams(project.ID, workflow.ID, parent, bridge, ChildPipelineMirror))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.RequestRunCancellation(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	childGraph, _ := database.GetRunGraph(ctx, child.ID)
	if childGraph.Run.Status != StatusCancelled || !childGraph.Run.CancellationRequested {
		t.Fatalf("cancelled child = %#v", childGraph.Run)
	}
	if _, err := database.db.ExecContext(ctx, `UPDATE child_pipeline_links SET source_file = 'changed.yml' WHERE child_run_id = ?`, child.ID); err == nil {
		t.Fatal("mutable child pipeline lineage unexpectedly accepted")
	}
}

func enqueueChildPipelineParent(t *testing.T, ctx context.Context, database *Store, projectID, workflowID string, strategy ChildPipelineStrategy) (Run, Job) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"sourceFile": ".gci/child.yml", "strategy": strategy, "depth": 1})
	parent, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: projectID, WorkflowID: workflowID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: "same-sha", SourcePath: "/srv/children", Jobs: []EnqueueJob{{Key: "bridge", Name: "Bridge", Runner: "gci-control-plane", Environment: json.RawMessage(`{}`), DependencyKeys: json.RawMessage(`[]`), ChildPipeline: payload}}})
	if err != nil {
		t.Fatal(err)
	}
	graph, _ := database.GetRunGraph(ctx, parent.ID)
	return parent, graph.Jobs[0].Job
}

func childPipelineRunParams(projectID, workflowID string, parent Run, bridge Job, strategy ChildPipelineStrategy) EnqueueRunParams {
	return EnqueueRunParams{ProjectID: projectID, WorkflowID: workflowID, TriggerType: "parent_pipeline", Ref: *parent.Ref, CommitSHA: *parent.CommitSHA, SourcePath: parent.SourcePath, Jobs: []EnqueueJob{{Key: "child", Name: "Child", Runner: "local", Environment: json.RawMessage(`{}`), DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "run", Name: "Run", Command: "true", Environment: json.RawMessage(`{}`)}}}}, ChildPipeline: &EnqueueChildPipelineLink{ParentRunID: parent.ID, ParentJobID: bridge.ID, SourceFile: ".gci/child.yml", Strategy: strategy, Depth: 1}}
}
