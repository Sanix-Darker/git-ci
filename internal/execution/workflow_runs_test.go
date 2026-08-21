package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestWorkflowRunDispatchesFailureConclusionAfterManagerRestart(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	failureMarker := filepath.Join(t.TempDir(), "failure-cd")
	successMarker := filepath.Join(t.TempDir(), "success-cd")
	writeManagerWorkflow(t, filepath.Join(root, ".github/workflows/ci.yml"), `name: CI Gate
on: [workflow_dispatch]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: exit 23
`)
	writeManagerWorkflow(t, filepath.Join(root, ".github/workflows/cd.yml"), "name: Failure Delivery\non:\n  workflow_run:\n    workflows: [CI Gate]\n    types: [completed]\n    branches: [main]\njobs:\n  success-path:\n    if: github.event.workflow_run.conclusion == 'success'\n    runs-on: ubuntu-latest\n    steps:\n      - run: printf success > "+successMarker+"\n  failure-path:\n    if: github.event.workflow_run.conclusion == 'failure'\n    runs-on: ubuntu-latest\n    steps:\n      - run: printf failure > "+failureMarker+"\n")
	commitManagerRepository(t, root)
	workflows, err := manager.SyncProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("sync workflows: %v", err)
	}
	sourceWorkflow := workflowByName(t, workflows, "CI Gate")
	source, err := manager.EnqueueWorkflow(ctx, sourceWorkflow.ID, "main", "")
	if err != nil {
		t.Fatalf("enqueue source: %v", err)
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process source = (%t, %v)", processed, err)
	}
	sourceGraph, err := database.GetRunGraph(ctx, source.ID)
	if err != nil || sourceGraph.Run.Status != store.StatusFailed {
		t.Fatalf("source graph = %#v, %v", sourceGraph.Run, err)
	}
	restarted, err := NewManager(database, WithWorkspaceRoot(filepath.Join(t.TempDir(), "restart-workspaces")))
	if err != nil {
		t.Fatalf("restart manager: %v", err)
	}
	if processed, err := restarted.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process downstream after restart = (%t, %v)", processed, err)
	}
	runs, err := database.ListRuns(ctx, project.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %#v, %v", runs, err)
	}
	var downstream store.Run
	for _, run := range runs {
		if run.TriggerType == "workflow_run" {
			downstream = run
		}
	}
	if downstream.ID == "" || downstream.Status != store.StatusSucceeded || downstream.Ref == nil || source.Ref == nil || *downstream.Ref != *source.Ref || downstream.CommitSHA == nil || source.CommitSHA == nil || *downstream.CommitSHA != *source.CommitSHA {
		t.Fatalf("downstream = %#v, source = %#v", downstream, source)
	}
	graph, err := database.GetRunGraph(ctx, downstream.ID)
	if err != nil || graph.WorkflowRun == nil || graph.WorkflowRun.SourceRunID != source.ID || graph.WorkflowRun.SourceConclusion != store.WorkflowRunFailure || graph.WorkflowRun.Depth != 1 {
		t.Fatalf("downstream workflow_run = %#v, %v", graph.WorkflowRun, err)
	}
	if _, err := os.Stat(failureMarker); err != nil {
		t.Fatalf("failure conclusion job did not run: %v", err)
	}
	if _, err := os.Stat(successMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("success conclusion job unexpectedly ran: %v", err)
	}
	if processed, err := restarted.ProcessNext(ctx); err != nil || processed {
		t.Fatalf("drain downstream completion = (%t, %v), want no run", processed, err)
	}
}

func TestWorkflowRunDispatchesSuccessConclusion(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	successMarker := filepath.Join(t.TempDir(), "success-cd")
	failureMarker := filepath.Join(t.TempDir(), "failure-cd")
	writeManagerWorkflow(t, filepath.Join(root, ".github/workflows/ci.yml"), `name: CI Gate
on: [workflow_dispatch]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: true
`)
	writeManagerWorkflow(t, filepath.Join(root, ".github/workflows/cd.yml"), "name: Success Delivery\non:\n  workflow_run:\n    workflows: [CI Gate]\n    types: [completed]\njobs:\n  success-path:\n    if: github.event.workflow_run.conclusion == 'success'\n    runs-on: ubuntu-latest\n    steps:\n      - run: printf success > "+successMarker+"\n  failure-path:\n    if: github.event.workflow_run.conclusion == 'failure'\n    runs-on: ubuntu-latest\n    steps:\n      - run: printf failure > "+failureMarker+"\n")
	commitManagerRepository(t, root)
	workflows, err := manager.SyncProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("sync workflows: %v", err)
	}
	source, err := manager.EnqueueWorkflow(ctx, workflowByName(t, workflows, "CI Gate").ID, "main", "")
	if err != nil {
		t.Fatalf("enqueue source: %v", err)
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process source = (%t, %v)", processed, err)
	}
	sourceGraph, err := database.GetRunGraph(ctx, source.ID)
	if err != nil || sourceGraph.Run.Status != store.StatusSucceeded {
		t.Fatalf("source graph = %#v, %v", sourceGraph.Run, err)
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process downstream = (%t, %v)", processed, err)
	}
	runs, err := database.ListRuns(ctx, project.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %#v, %v", runs, err)
	}
	var downstream store.Run
	for _, run := range runs {
		if run.TriggerType == "workflow_run" {
			downstream = run
		}
	}
	if downstream.ID == "" || downstream.Status != store.StatusSucceeded || downstream.Ref == nil || source.Ref == nil || *downstream.Ref != *source.Ref || downstream.CommitSHA == nil || source.CommitSHA == nil || *downstream.CommitSHA != *source.CommitSHA {
		t.Fatalf("downstream = %#v, source = %#v", downstream, source)
	}
	graph, err := database.GetRunGraph(ctx, downstream.ID)
	if err != nil || graph.WorkflowRun == nil || graph.WorkflowRun.SourceRunID != source.ID || graph.WorkflowRun.SourceConclusion != store.WorkflowRunSuccess || graph.WorkflowRun.Depth != 1 {
		t.Fatalf("downstream workflow_run = %#v, %v", graph.WorkflowRun, err)
	}
	if _, err := os.Stat(successMarker); err != nil {
		t.Fatalf("success conclusion job did not run: %v", err)
	}
	if _, err := os.Stat(failureMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure conclusion job unexpectedly ran: %v", err)
	}
}

func TestWorkflowRunChainStopsAfterThreeDownstreamLevels(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	names := []string{"A", "B", "C", "D", "E"}
	for index, name := range names {
		trigger := "on: [workflow_dispatch]"
		if index > 0 {
			trigger = "on:\n  workflow_run:\n    workflows: [" + names[index-1] + "]\n    types: [completed]"
		}
		contents := "name: " + name + "\n" + trigger + "\njobs:\n  run:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n"
		writeManagerWorkflow(t, filepath.Join(root, ".github/workflows/"+strings.ToLower(name)+".yml"), contents)
	}
	commitManagerRepository(t, root)
	workflows, err := manager.SyncProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("sync chain: %v", err)
	}
	source, err := manager.EnqueueWorkflow(ctx, workflowByName(t, workflows, "A").ID, "main", "")
	if err != nil {
		t.Fatalf("enqueue A: %v", err)
	}
	for index := 0; index < 4; index++ {
		if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
			t.Fatalf("process chain level %d = (%t, %v)", index, processed, err)
		}
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || processed {
		t.Fatalf("process beyond depth cap = (%t, %v)", processed, err)
	}
	runs, err := database.ListRuns(ctx, project.ID)
	if err != nil || len(runs) != 4 {
		t.Fatalf("depth-capped runs = %#v, %v", runs, err)
	}
	seenDepth := map[int]bool{0: false, 1: false, 2: false, 3: false}
	for _, run := range runs {
		graph, err := database.GetRunGraph(ctx, run.ID)
		if err != nil {
			t.Fatalf("load chain run %s: %v", run.ID, err)
		}
		depth := 0
		if graph.WorkflowRun != nil {
			depth = graph.WorkflowRun.Depth
		}
		seenDepth[depth] = true
	}
	for depth := 0; depth <= 3; depth++ {
		if !seenDepth[depth] {
			t.Errorf("missing workflow_run depth %d; source = %s", depth, source.ID)
		}
	}
}

func workflowByName(t *testing.T, workflows []store.Workflow, name string) store.Workflow {
	t.Helper()
	for _, workflow := range workflows {
		if workflow.Name == name {
			return workflow
		}
	}
	t.Fatalf("workflow %q not found in %#v", name, workflows)
	return store.Workflow{}
}
