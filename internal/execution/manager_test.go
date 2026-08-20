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

func TestManagerSyncProjectAndEnqueueWorkflowSnapshotsDefinition(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	workflowPath := filepath.Join(root, ".github", "workflows", "build.yml")
	writeManagerWorkflow(t, workflowPath, githubWorkflow("Build v1", `printf 'first-definition\n'`))

	first := syncManagerWorkflow(t, ctx, manager, project.ID)
	firstRun, err := manager.EnqueueWorkflow(ctx, first.ID, "", "first-sha")
	if err != nil {
		t.Fatalf("enqueue first workflow: %v", err)
	}

	writeManagerWorkflow(t, workflowPath, githubWorkflow("Build v2", `printf 'second-definition\n'`))
	second := syncManagerWorkflow(t, ctx, manager, project.ID)
	if second.ID != first.ID {
		t.Fatalf("workflow ID changed after sync: got %q, want %q", second.ID, first.ID)
	}
	if second.Revision != first.Revision+1 {
		t.Fatalf("workflow revision = %d, want %d", second.Revision, first.Revision+1)
	}

	firstGraph, err := database.GetRunGraph(ctx, firstRun.ID)
	if err != nil {
		t.Fatalf("read first immutable graph: %v", err)
	}
	if firstGraph.Run.WorkflowRevision == nil || *firstGraph.Run.WorkflowRevision != first.Revision {
		t.Fatalf("first run workflow revision = %v, want %d", firstGraph.Run.WorkflowRevision, first.Revision)
	}
	if got := graphStepCommand(t, firstGraph, "build", 0); got != "printf 'first-definition\\n'" {
		t.Fatalf("first run command = %q, want original definition", got)
	}

	secondRun, err := manager.EnqueueWorkflow(ctx, second.ID, "refs/heads/release", "second-sha")
	if err != nil {
		t.Fatalf("enqueue second workflow: %v", err)
	}
	secondGraph, err := database.GetRunGraph(ctx, secondRun.ID)
	if err != nil {
		t.Fatalf("read second immutable graph: %v", err)
	}
	if secondGraph.Run.WorkflowRevision == nil || *secondGraph.Run.WorkflowRevision != second.Revision {
		t.Fatalf("second run workflow revision = %v, want %d", secondGraph.Run.WorkflowRevision, second.Revision)
	}
	if got := graphStepCommand(t, secondGraph, "build", 0); got != "printf 'second-definition\\n'" {
		t.Fatalf("second run command = %q, want updated definition", got)
	}
}

func TestManagerExecutesDependenciesAndPersistsStdoutAndStderr(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	orderPath := filepath.Join(root, "execution-order")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "pipeline.yml"), strings.Join([]string{
		"name: Ordered pipeline",
		"on: workflow_dispatch",
		"jobs:",
		"  prepare:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - name: Prepare",
		"        run: |",
		"          printf 'prepare' > execution-order",
		"          printf 'stdout-line\\n'",
		"          printf 'stderr-line\\n' >&2",
		"  verify:",
		"    needs: prepare",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - name: Verify dependency",
		"        run: |",
		"          test \"$(cat execution-order)\" = prepare",
		"          printf 'verify' >> execution-order",
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read completed graph: %v", err)
	}
	if graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", graph.Run.Status)
	}
	if got := jobStatus(t, graph, "prepare"); got != store.StatusSucceeded {
		t.Errorf("prepare status = %q, want succeeded", got)
	}
	if got := jobStatus(t, graph, "verify"); got != store.StatusSucceeded {
		t.Errorf("verify status = %q, want succeeded", got)
	}
	contents, err := os.ReadFile(orderPath)
	if err != nil {
		t.Fatalf("read dependency-order marker: %v", err)
	}
	if got := string(contents); got != "prepareverify" {
		t.Fatalf("execution order = %q, want prepareverify", got)
	}

	prepare := jobGraph(t, graph, "prepare")
	lines, err := database.ListLogLines(ctx, prepare.Steps[0].ID)
	if err != nil {
		t.Fatalf("list durable step logs: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("durable log lines = %#v, want stdout and stderr lines", lines)
	}
	seen := map[store.LogStream]string{}
	for index, line := range lines {
		if line.Sequence != int64(index+1) {
			t.Errorf("line %d sequence = %d, want %d", index, line.Sequence, index+1)
		}
		seen[line.Stream] = line.Message
	}
	if got := seen[store.LogStreamStdout]; got != "stdout-line" {
		t.Errorf("stdout durable log = %q, want stdout-line", got)
	}
	if got := seen[store.LogStreamStderr]; got != "stderr-line" {
		t.Errorf("stderr durable log = %q, want stderr-line", got)
	}
}

func TestManagerSkipsDependentJobsAfterFailure(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	dependentMarker := filepath.Join(root, "dependent-ran")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "failure.yml"), strings.Join([]string{
		"name: Failed dependency",
		"on: workflow_dispatch",
		"jobs:",
		"  fail:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: exit 17",
		"  dependent:",
		"    needs: fail",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: printf should-not-run > dependent-ran",
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read completed graph: %v", err)
	}
	if graph.Run.Status != store.StatusFailed {
		t.Fatalf("run status = %q, want failed", graph.Run.Status)
	}
	if got := jobStatus(t, graph, "fail"); got != store.StatusFailed {
		t.Errorf("fail status = %q, want failed", got)
	}
	dependent := jobGraph(t, graph, "dependent")
	if dependent.Job.Status != store.StatusSkipped || dependent.Steps[0].Status != store.StatusSkipped {
		t.Errorf("dependent graph = %#v, want skipped job and step", dependent)
	}
	if _, err := os.Stat(dependentMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dependent command ran; stat error = %v", err)
	}
}

func TestManagerAllowsFailedDependencyWhenConfigured(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(root, "dependent-ran")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "allowed-failure.yml"), strings.Join([]string{
		"name: Allowed failure",
		"on: workflow_dispatch",
		"jobs:",
		"  unstable:",
		"    continue-on-error: true",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: exit 23",
		"  dependent:",
		"    needs: unstable",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: printf continued > dependent-ran",
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read completed graph: %v", err)
	}
	if graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", graph.Run.Status)
	}
	unstable := jobGraph(t, graph, "unstable")
	if unstable.Job.Status != store.StatusFailed || !unstable.Job.AllowFailure {
		t.Errorf("unstable job = %#v, want failed allowed-failure job", unstable.Job)
	}
	if got := jobStatus(t, graph, "dependent"); got != store.StatusSucceeded {
		t.Errorf("dependent status = %q, want succeeded", got)
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read allowed-failure marker: %v", err)
	}
	if got := string(contents); got != "continued" {
		t.Errorf("allowed-failure marker = %q, want continued", got)
	}
}

func TestManagerCancelsRunningRun(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	readyPath := filepath.Join(root, "worker-ready")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "cancel.yml"), strings.Join([]string{
		"name: Cancellable",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - timeout-minutes: 1",
		"        run: printf ready > worker-ready; exec sleep 30",
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run, err := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	if err != nil {
		t.Fatalf("enqueue cancellable workflow: %v", err)
	}

	type processResult struct {
		processed bool
		err       error
	}
	result := make(chan processResult, 1)
	go func() {
		processed, processErr := manager.ProcessNext(ctx)
		result <- processResult{processed: processed, err: processErr}
	}()
	waitForManagerFile(t, readyPath)
	if _, err := database.RequestRunCancellation(ctx, run.ID); err != nil {
		t.Fatalf("request cancellation: %v", err)
	}
	select {
	case outcome := <-result:
		if outcome.err != nil || !outcome.processed {
			t.Fatalf("ProcessNext() = (%t, %v), want (true, nil)", outcome.processed, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessNext did not stop after durable cancellation request")
	}

	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read cancelled graph: %v", err)
	}
	if graph.Run.Status != store.StatusCancelled {
		t.Fatalf("run status = %q, want cancelled", graph.Run.Status)
	}
	if got := jobStatus(t, graph, "build"); got != store.StatusCancelled {
		t.Errorf("job status = %q, want cancelled", got)
	}
	if got := jobGraph(t, graph, "build").Steps[0].Status; got != store.StatusCancelled {
		t.Errorf("step status = %q, want cancelled", got)
	}
}

func TestManagerRejectsEscapingWorkingDirectory(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	escapedPath := filepath.Join(filepath.Dir(root), "escaped")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "containment.yml"), strings.Join([]string{
		"name: Containment",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - working-directory: ../outside",
		"        run: printf escaped > " + escapedPath,
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read containment graph: %v", err)
	}
	if graph.Run.Status != store.StatusFailed || jobStatus(t, graph, "build") != store.StatusFailed {
		t.Errorf("containment graph = %#v, want failed run and job", graph)
	}
	if _, err := os.Stat(escapedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("escaping command created %q; stat error = %v", escapedPath, err)
	}
	step := jobGraph(t, graph, "build").Steps[0]
	lines, err := database.ListLogLines(ctx, step.ID)
	if err != nil {
		t.Fatalf("list containment logs: %v", err)
	}
	if len(lines) != 1 || lines[0].Stream != store.LogStreamSystem || !strings.Contains(lines[0].Message, "escapes registered project") {
		t.Errorf("containment logs = %#v, want one system rejection", lines)
	}
}

func TestManagerFailsUnsupportedActionWithoutExecutingFollowingStep(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(root, "following-step-ran")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "action.yml"), strings.Join([]string{
		"name: Unsupported action",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - uses: actions/setup-go@v5",
		"      - run: printf unsafe > following-step-ran",
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("read unsupported-action graph: %v", err)
	}
	if graph.Run.Status != store.StatusFailed || jobStatus(t, graph, "build") != store.StatusFailed {
		t.Errorf("unsupported-action graph = %#v, want failed run and job", graph)
	}
	build := jobGraph(t, graph, "build")
	if build.Steps[0].Status != store.StatusFailed || build.Steps[1].Status != store.StatusSkipped {
		t.Errorf("unsupported-action steps = %#v, want failed then skipped", build.Steps)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("step after unsupported action ran; stat error = %v", err)
	}
	lines, err := database.ListLogLines(ctx, build.Steps[0].ID)
	if err != nil {
		t.Fatalf("list unsupported-action logs: %v", err)
	}
	if len(lines) != 1 || lines[0].Stream != store.LogStreamSystem || !strings.Contains(lines[0].Message, `unsupported action "actions/setup-go@v5"`) {
		t.Errorf("unsupported-action logs = %#v, want one system failure", lines)
	}
}

func newManagerTestFixture(t *testing.T) (context.Context, *store.Store, *Manager, store.Project, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "git-ci.db"))
	if err != nil {
		t.Fatalf("open temporary SQLite store: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close temporary SQLite store: %v", err)
		}
	})
	projectPath := root
	project, err := database.CreateProject(ctx, store.CreateProjectParams{
		Slug:          "fixture",
		Name:          "Fixture project",
		SourceType:    "git",
		CanonicalPath: &projectPath,
		DefaultBranch: "main",
		Active:        true,
	})
	if err != nil {
		t.Fatalf("create registered project: %v", err)
	}
	manager, err := NewManager(database)
	if err != nil {
		t.Fatalf("create execution manager: %v", err)
	}
	return ctx, database, manager, project, root
}

func writeManagerWorkflow(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write workflow fixture: %v", err)
	}
}

func githubWorkflow(name, command string) string {
	return strings.Join([]string{
		"name: " + name,
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: " + command,
	}, "\n")
}

func syncManagerWorkflow(t *testing.T, ctx context.Context, manager *Manager, projectID string) store.Workflow {
	t.Helper()
	workflows, err := manager.SyncProject(ctx, projectID)
	if err != nil {
		t.Fatalf("sync project workflows: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("synced workflows = %#v, want exactly one", workflows)
	}
	return workflows[0]
}

func enqueueAndProcess(t *testing.T, ctx context.Context, manager *Manager, workflowID string) store.Run {
	t.Helper()
	run, err := manager.EnqueueWorkflow(ctx, workflowID, "", "fixture-sha")
	if err != nil {
		t.Fatalf("enqueue workflow: %v", err)
	}
	processed, err := manager.ProcessNext(ctx)
	if err != nil {
		t.Fatalf("process queued workflow: %v", err)
	}
	if !processed {
		t.Fatal("ProcessNext() = false, want one claimed run")
	}
	return run
}

func jobGraph(t *testing.T, graph store.RunGraph, key string) store.JobGraph {
	t.Helper()
	for _, item := range graph.Jobs {
		if item.Job.Key != nil && *item.Job.Key == key {
			return item
		}
	}
	t.Fatalf("run graph has no job %q: %#v", key, graph.Jobs)
	return store.JobGraph{}
}

func jobStatus(t *testing.T, graph store.RunGraph, key string) store.Status {
	t.Helper()
	return jobGraph(t, graph, key).Job.Status
}

func graphStepCommand(t *testing.T, graph store.RunGraph, jobKey string, index int) string {
	t.Helper()
	steps := jobGraph(t, graph, jobKey).Steps
	if index >= len(steps) || steps[index].Command == nil {
		t.Fatalf("job %q step %d has no command: %#v", jobKey, index, steps)
	}
	return *steps[index].Command
}

func waitForManagerFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat cancellation readiness file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for cancellation readiness file %q", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
