package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerExecutesMirroredChildPipelineAndResumesParent(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "child-marker")
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), "prepare:\n  script: [\"printf prepare\"]\nbridge:\n  needs: [prepare]\n  trigger:\n    include: .gci/child.yml\n    strategy: mirror\nfinish:\n  needs: [bridge]\n  script: [\"test -f "+shellTestPath(marker)+"\"]\n")
	writeManagerWorkflow(t, filepath.Join(root, ".gci/child.yml"), "verify:\n  script: [\"printf child > "+shellTestPath(marker)+"\"]\n")
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	parent, err := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	processManagerOnce(t, ctx, manager)
	graph, _ := database.GetRunGraph(ctx, parent.ID)
	if graph.Run.Status != store.StatusWaiting || len(graph.ChildPipelines) != 1 {
		t.Fatalf("waiting graph = %#v", graph)
	}
	childID := graph.ChildPipelines[0].ChildRunID
	processManagerOnce(t, ctx, manager)
	processManagerOnce(t, ctx, manager)
	graph, _ = database.GetRunGraph(ctx, parent.ID)
	child, _ := database.GetRunGraph(ctx, childID)
	if graph.Run.Status != store.StatusSucceeded || child.Run.Status != store.StatusSucceeded || jobStatus(t, graph, "bridge") != store.StatusSucceeded || jobStatus(t, graph, "finish") != store.StatusSucceeded {
		t.Fatalf("parent=%#v child=%#v", graph, child)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	runs, _ := database.ListRuns(ctx, project.ID)
	if len(runs) != 1 || runs[0].ID != parent.ID {
		t.Fatalf("root runs = %#v", runs)
	}
}

func TestManagerMirrorsChildFailureIntoParentDAG(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), "bridge:\n  trigger:\n    include: child.yml\n    strategy: depend\nafter:\n  needs: [bridge]\n  script: [\"printf should-not-run\"]\n")
	writeManagerWorkflow(t, filepath.Join(root, "child.yml"), "fail:\n  script: [\"exit 23\"]\n")
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	parent, _ := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	processManagerOnce(t, ctx, manager)
	processManagerOnce(t, ctx, manager)
	processManagerOnce(t, ctx, manager)
	graph, _ := database.GetRunGraph(ctx, parent.ID)
	if graph.Run.Status != store.StatusFailed || jobStatus(t, graph, "bridge") != store.StatusFailed || jobStatus(t, graph, "after") != store.StatusSkipped {
		t.Fatalf("graph = %#v", graph)
	}
}

func TestManagerAsyncChildDoesNotBlockParent(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), "bridge:\n  trigger:\n    include: child.yml\nafter:\n  needs: [bridge]\n  script: [\"printf parent\"]\n")
	writeManagerWorkflow(t, filepath.Join(root, "child.yml"), "child:\n  script: [\"printf child\"]\n")
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	parent, _ := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	processManagerOnce(t, ctx, manager)
	graph, _ := database.GetRunGraph(ctx, parent.ID)
	if graph.Run.Status != store.StatusSucceeded || len(graph.ChildPipelines) != 1 || graph.ChildPipelines[0].ChildStatus != store.StatusQueued {
		t.Fatalf("async parent = %#v", graph)
	}
	processManagerOnce(t, ctx, manager)
	child, _ := database.GetRunGraph(ctx, graph.ChildPipelines[0].ChildRunID)
	if child.Run.Status != store.StatusSucceeded {
		t.Fatalf("async child = %#v", child)
	}
}

func processManagerOnce(t *testing.T, ctx context.Context, manager *Manager) {
	t.Helper()
	processed, err := manager.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
}
