package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerExecutesOnlyMatchingMatrixJobAndStepConditions(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "matrix-condition")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "matrix.yml"), strings.Join([]string{
		"name: Conditional matrix",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    if: ${{ matrix.target == 'run' }}",
		"    strategy:",
		"      matrix:",
		"        target: [run, skip]",
		"    runs-on: linux",
		"    steps:",
		"      - if: ${{ matrix.target == 'run' }}",
		"        run: printf '${{ matrix.target }}' >> " + shellTestPath(marker),
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read conditional matrix graph: %v", err)
	}
	if graph.Run.Status != store.StatusSucceeded || len(graph.Jobs) != 2 {
		t.Fatalf("matrix graph = %#v, want successful two-variant run", graph)
	}
	statuses := make(map[string]store.Status)
	for _, item := range graph.Jobs {
		values := decodeEnvironmentJSON(item.Job.Environment)
		statuses[values["MATRIX_TARGET"]] = item.Job.Status
	}
	if statuses["run"] != store.StatusSucceeded || statuses["skip"] != store.StatusSkipped {
		t.Errorf("matrix statuses = %#v, want run=succeeded and skip=skipped", statuses)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "run" {
		t.Fatalf("matrix marker = %q, %v; want run", contents, err)
	}
	for _, item := range graph.Jobs {
		if decodeEnvironmentJSON(item.Job.Environment)["MATRIX_TARGET"] != "skip" {
			continue
		}
		lines, err := database.ListLogLines(ctx, item.Steps[0].ID)
		if err != nil || len(lines) != 1 || !strings.Contains(lines[0].Message, "condition evaluated to false") {
			t.Errorf("skipped matrix logs = %#v, %v; want explicit condition reason", lines, err)
		}
	}
}

func TestManagerWorkflowConcurrencyCancelsPriorRun(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	ready := filepath.Join(t.TempDir(), "concurrency-ready")
	result := filepath.Join(t.TempDir(), "concurrency-result")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "concurrency.yml"), strings.Join([]string{
		"name: Serialized deploy",
		"on: workflow_dispatch",
		"concurrency:",
		"  group: deploy-${{ github.ref }}",
		"  cancel-in-progress: true",
		"jobs:",
		"  deploy:",
		"    runs-on: linux",
		"    steps:",
		"      - run: if test -f " + shellTestPath(ready) + "; then printf second > " + shellTestPath(result) + "; else touch " + shellTestPath(ready) + "; exec sleep 30; fi",
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	first, err := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	type processResult struct {
		processed bool
		err       error
	}
	firstResult := make(chan processResult, 1)
	go func() {
		processed, processErr := manager.ProcessNext(ctx)
		firstResult <- processResult{processed: processed, err: processErr}
	}()
	waitForManagerFile(t, ready)
	second, err := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-firstResult:
		if outcome.err != nil || !outcome.processed {
			t.Fatalf("first ProcessNext() = (%t, %v)", outcome.processed, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel-in-progress did not stop prior run")
	}
	firstGraph, err := database.GetRunGraph(ctx, first.ID)
	if err != nil || firstGraph.Run.Status != store.StatusCancelled {
		t.Fatalf("first run = %#v, %v; want cancelled", firstGraph.Run, err)
	}
	secondManager, err := NewManager(database, WithWorkspaceRoot(filepath.Join(t.TempDir(), "second-workspaces")))
	if err != nil {
		t.Fatal(err)
	}
	processed, err := secondManager.ProcessNext(context.Background())
	if err != nil || !processed {
		t.Fatalf("second ProcessNext() = (%t, %v)", processed, err)
	}
	secondGraph, err := database.GetRunGraph(ctx, second.ID)
	if err != nil || secondGraph.Run.Status != store.StatusSucceeded {
		t.Fatalf("second run = %#v, %v; want succeeded", secondGraph.Run, err)
	}
	contents, err := os.ReadFile(result)
	if err != nil || string(contents) != "second" {
		t.Fatalf("serialized result = %q, %v; want second", contents, err)
	}
}

func TestManagerRunsFailureAndAlwaysConditionsAfterFailure(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	cleanupMarker := filepath.Join(t.TempDir(), "cleanup")
	defaultMarker := filepath.Join(t.TempDir(), "default")
	dependentMarker := filepath.Join(t.TempDir(), "dependent")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "status.yml"), strings.Join([]string{
		"name: Status conditions",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: linux",
		"    steps:",
		"      - id: fail",
		"        run: exit 41",
		"      - id: cleanup",
		"        if: ${{ failure() }}",
		"        run: printf cleanup > " + shellTestPath(cleanupMarker),
		"      - id: default",
		"        run: printf unsafe > " + shellTestPath(defaultMarker),
		"  report:",
		"    needs: build",
		"    if: ${{ always() }}",
		"    runs-on: linux",
		"    steps:",
		"      - run: printf report > " + shellTestPath(dependentMarker),
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read status-condition graph: %v", err)
	}
	if graph.Run.Status != store.StatusFailed || jobStatus(t, graph, "build") != store.StatusFailed || jobStatus(t, graph, "report") != store.StatusSucceeded {
		t.Errorf("status-condition graph = %#v, want failed build, successful always report, failed run", graph)
	}
	build := jobGraph(t, graph, "build")
	if got := []store.Status{build.Steps[0].Status, build.Steps[1].Status, build.Steps[2].Status}; !reflectStatuses(got, []store.Status{store.StatusFailed, store.StatusSucceeded, store.StatusSkipped}) {
		t.Errorf("build step statuses = %#v, want failed/succeeded/skipped", got)
	}
	for path, want := range map[string]string{cleanupMarker: "cleanup", dependentMarker: "report"} {
		contents, err := os.ReadFile(path)
		if err != nil || string(contents) != want {
			t.Errorf("marker %q = %q, %v; want %q", path, contents, err, want)
		}
	}
	if _, err := os.Stat(defaultMarker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("default step ran after failure; stat error = %v", err)
	}
}

func reflectStatuses(left, right []store.Status) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
