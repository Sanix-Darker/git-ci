package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestExecutionMigrationPreservesInitialData(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open(sqliteDriver, databasePath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}

	initial, err := migrationFiles.ReadFile("migrations/0001_initial.sql")
	if err != nil {
		legacy.Close()
		t.Fatalf("read initial migration: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, string(initial)); err != nil {
		legacy.Close()
		t.Fatalf("apply initial migration: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			version TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		legacy.Close()
		t.Fatalf("create legacy migration table: %v", err)
	}
	sum := sha256.Sum256(initial)
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, checksum, applied_at)
		VALUES (?, ?, ?)
	`, "0001_initial", hex.EncodeToString(sum[:]), nowUTC().UnixMilli()); err != nil {
		legacy.Close()
		t.Fatalf("record initial migration: %v", err)
	}
	now := nowUTC().UnixMilli()
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO projects (
			id, slug, name, source_type, default_branch, active, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, "legacy-project", "legacy", "Legacy project", "git", "main", 1, now, now); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy project: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO runs (id, project_id, trigger_type, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "legacy-run", "legacy-project", "manual", StatusQueued, now, now); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy run: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO jobs (id, run_id, name, status, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "legacy-job", "legacy-run", "Legacy job", StatusQueued, 0, now, now); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy job: %v", err)
	}
	if _, err := legacy.ExecContext(ctx, `
		INSERT INTO steps (id, job_id, step_index, name, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "legacy-step", "legacy-job", 0, "Legacy step", StatusQueued, now, now); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy step: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close migrated database: %v", err)
		}
	})

	graph, err := store.GetRunGraph(ctx, "legacy-run")
	if err != nil {
		t.Fatalf("get preserved graph: %v", err)
	}
	if graph.Run.ID != "legacy-run" || graph.Run.WorkflowID != nil || len(graph.Jobs) != 1 || len(graph.Jobs[0].Steps) != 1 {
		t.Fatalf("preserved graph = %#v", graph)
	}
	if graph.Jobs[0].Job.Key != nil || graph.Jobs[0].Steps[0].Key != nil {
		t.Fatalf("legacy snapshot keys should remain absent: %#v", graph)
	}
	var migrations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("count applied migrations: %v", err)
	}
	if migrations != 18 {
		t.Errorf("migration count = %d, want 18", migrations)
	}
}

func TestWorkflowUpsertListGetAndRestart(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "execution.db")
	first, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	project, err := first.CreateProject(ctx, testProjectParams("workflows"))
	if err != nil {
		first.Close()
		t.Fatalf("create project: %v", err)
	}
	created, err := first.UpsertWorkflow(ctx, UpsertWorkflowParams{
		ProjectID:   project.ID,
		Key:         "build",
		Name:        "Build and test",
		Definition:  json.RawMessage(`{"on":["push"],"jobs":{"build":{}}}`),
		Environment: json.RawMessage(`{"GOFLAGS":"-mod=mod"}`),
	})
	if err != nil {
		first.Close()
		t.Fatalf("create workflow: %v", err)
	}
	updated, err := first.UpsertWorkflow(ctx, UpsertWorkflowParams{
		ProjectID:   project.ID,
		Key:         "build",
		Name:        "Build, test, and lint",
		Definition:  json.RawMessage(`{"on":["push","pull_request"],"jobs":{"build":{}}}`),
		Environment: json.RawMessage(`{"GOFLAGS":"-mod=readonly"}`),
	})
	if err != nil {
		first.Close()
		t.Fatalf("update workflow: %v", err)
	}
	if updated.ID != created.ID || updated.Revision != created.Revision+1 || updated.CreatedAt != created.CreatedAt {
		first.Close()
		t.Fatalf("updated workflow = %#v, created = %#v", updated, created)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close second store: %v", err)
		}
	})

	got, err := second.GetWorkflow(ctx, updated.ID)
	if err != nil {
		t.Fatalf("get persisted workflow: %v", err)
	}
	if got.Name != updated.Name || got.Revision != 2 || !sameJSON(got.Environment, json.RawMessage(`{"GOFLAGS":"-mod=readonly"}`)) {
		t.Fatalf("persisted workflow = %#v", got)
	}
	other, err := second.UpsertWorkflow(ctx, UpsertWorkflowParams{
		ProjectID:  project.ID,
		Key:        "deploy",
		Name:       "Deploy",
		Definition: json.RawMessage(`{"on":["workflow_dispatch"]}`),
	})
	if err != nil {
		t.Fatalf("create second workflow: %v", err)
	}
	workflows, err := second.ListWorkflows(ctx, project.ID)
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(workflows) != 2 || workflows[0].ID != updated.ID || workflows[1].ID != other.ID {
		t.Fatalf("workflows = %#v", workflows)
	}
}

func TestEnqueueRunStoresImmutableGraphAndDependencies(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, store, "snapshot")
	run, err := store.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID:   project.ID,
		WorkflowID:  workflow.ID,
		TriggerType: "push",
		Ref:         "refs/heads/main",
		CommitSHA:   "abc123",
		SourcePath:  "/srv/projects/snapshot",
		Environment: json.RawMessage(`{"CI":"true"}`),
		Jobs: []EnqueueJob{
			{
				Key:            "build",
				Name:           "Build",
				Runner:         "docker",
				Environment:    json.RawMessage(`{"GOOS":"linux"}`),
				DependencyKeys: json.RawMessage(`[]`),
				Steps: []EnqueueStep{
					{Key: "checkout", Name: "Checkout", Command: "git checkout ."},
					{Key: "compile", Name: "Compile", Command: "go build ./...", Environment: json.RawMessage(`{"CGO_ENABLED":"0"}`)},
				},
			},
			{
				Key:             "deploy",
				Name:            "Deploy",
				EnvironmentName: "production",
				DependencyKeys:  json.RawMessage(`["build"]`),
				Steps: []EnqueueStep{
					{Key: "ship", Name: "Ship", Command: "./deploy"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("enqueue run: %v", err)
	}
	if run.Status != StatusQueued || run.WorkflowID == nil || *run.WorkflowID != workflow.ID || run.WorkflowRevision == nil || *run.WorkflowRevision != workflow.Revision {
		t.Fatalf("queued run = %#v", run)
	}

	graph, err := store.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if len(graph.Jobs) != 2 || len(graph.Jobs[0].Steps) != 2 || len(graph.Jobs[1].Steps) != 1 {
		t.Fatalf("graph shape = %#v", graph)
	}
	if graph.Jobs[0].Job.Key == nil || *graph.Jobs[0].Job.Key != "build" || !sameJSON(graph.Jobs[1].Job.DependencyKeys, json.RawMessage(`["build"]`)) {
		t.Fatalf("graph dependencies = %#v", graph)
	}
	if !sameJSON(graph.Jobs[0].Steps[1].Environment, json.RawMessage(`{"CGO_ENABLED":"0"}`)) {
		t.Fatalf("step environment = %s", graph.Jobs[0].Steps[1].Environment)
	}
	targets, err := store.ListDeploymentTargets(ctx, run.ID)
	if err != nil {
		t.Fatalf("list deployment targets: %v", err)
	}
	if len(targets) != 1 || targets[0].JobKey != "deploy" || targets[0].Environment != "production" || targets[0].DeploymentTier != DeploymentTierOther {
		t.Fatalf("deployment targets = %#v", targets)
	}
	target, err := store.GetDeploymentTargetForJob(ctx, targets[0].JobID)
	if err != nil || target != targets[0] {
		t.Fatalf("get deployment target = %#v, %v", target, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployment_targets SET environment = ? WHERE job_id = ?`, "staging", target.JobID); err == nil {
		t.Fatal("deployment target snapshot update unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO deployment_targets (job_id, run_id, job_key, environment, deployment_tier, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, graph.Jobs[0].Job.ID, run.ID, "deploy", "production", DeploymentTierProduction, nowUTC().UnixMilli()); err == nil {
		t.Fatal("deployment target with mismatched job key unexpectedly succeeded")
	}
	if _, err := store.UpsertWorkflow(ctx, UpsertWorkflowParams{
		ProjectID:  project.ID,
		Key:        workflow.Key,
		Name:       "Changed definition",
		Definition: json.RawMessage(`{"on":["schedule"]}`),
	}); err != nil {
		t.Fatalf("change source workflow: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE jobs SET name = ? WHERE id = ?`, "Mutated", graph.Jobs[0].Job.ID); err == nil {
		t.Fatal("job snapshot update unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE steps SET command = ? WHERE id = ?`, "false", graph.Jobs[0].Steps[0].ID); err == nil {
		t.Fatal("step snapshot update unexpectedly succeeded")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE runs SET environment_json = ? WHERE id = ?`, `{}`, run.ID); err == nil {
		t.Fatal("run snapshot update unexpectedly succeeded")
	}

	runs, err := store.ListRuns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID || !sameJSON(runs[0].Environment, json.RawMessage(`{"CI":"true"}`)) {
		t.Fatalf("runs = %#v", runs)
	}
}

func TestEnqueueRunRejectsInvalidDependencyGraphs(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, store, "invalid-graph")
	base := EnqueueRunParams{
		ProjectID:   project.ID,
		WorkflowID:  workflow.ID,
		TriggerType: "manual",
		Jobs: []EnqueueJob{
			{Key: "one", Name: "One", DependencyKeys: json.RawMessage(`["missing"]`), Steps: []EnqueueStep{{Key: "one", Name: "One"}}},
		},
	}
	if _, err := store.EnqueueRun(ctx, base); err == nil {
		t.Fatal("unknown dependency was accepted")
	}
	base.Jobs = []EnqueueJob{
		{Key: "one", Name: "One", DependencyKeys: json.RawMessage(`["two"]`), Steps: []EnqueueStep{{Key: "one", Name: "One"}}},
		{Key: "two", Name: "Two", DependencyKeys: json.RawMessage(`["one"]`), Steps: []EnqueueStep{{Key: "two", Name: "Two"}}},
	}
	if _, err := store.EnqueueRun(ctx, base); err == nil {
		t.Fatal("cyclic dependency graph was accepted")
	}
}

func TestClaimNextQueuedRunIsAtomic(t *testing.T) {
	store, _ := newTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, store, "claim")

	const workers = 12
	for index := 0; index < workers; index++ {
		if _, err := store.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, fmt.Sprintf("queue-%02d", index))); err != nil {
			t.Fatalf("enqueue run %d: %v", index, err)
		}
	}

	start := make(chan struct{})
	claimed := make(chan *Run, workers)
	errs := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			run, err := store.ClaimNextQueuedRun(ctx, fmt.Sprintf("worker-%02d", index))
			if err != nil {
				errs <- err
				return
			}
			claimed <- run
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errs)
	close(claimed)
	for err := range errs {
		t.Error(err)
	}

	seen := make(map[string]struct{}, workers)
	for run := range claimed {
		if run == nil {
			t.Error("worker did not claim an available run")
			continue
		}
		if run.Status != StatusRunning || run.WorkerID == nil || run.StartedAt == nil || run.ClaimedAt == nil {
			t.Errorf("claimed run = %#v", run)
		}
		if _, exists := seen[run.ID]; exists {
			t.Errorf("run %q was claimed more than once", run.ID)
		}
		seen[run.ID] = struct{}{}
	}
	if len(seen) != workers {
		t.Errorf("claimed %d runs, want %d", len(seen), workers)
	}
}

func TestStatusTransitionsAndCancellation(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, store, "transitions")
	run, err := store.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "transition"))
	if err != nil {
		t.Fatalf("enqueue run: %v", err)
	}
	if _, err := store.TransitionRun(ctx, run.ID, StatusSucceeded); err == nil {
		t.Fatal("queued run transitioned directly to succeeded")
	} else {
		var transition *ErrInvalidStatusTransition
		if !errors.As(err, &transition) {
			t.Fatalf("invalid transition error = %v", err)
		}
	}
	claimed, err := store.ClaimNextQueuedRun(ctx, "worker")
	if err != nil || claimed == nil {
		t.Fatalf("claim run = %#v, %v", claimed, err)
	}
	graph, err := store.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	jobID := graph.Jobs[0].Job.ID
	stepID := graph.Jobs[0].Steps[0].ID
	job, err := store.TransitionJob(ctx, jobID, StatusRunning)
	if err != nil || job.StartedAt == nil {
		t.Fatalf("start job = %#v, %v", job, err)
	}
	step, err := store.TransitionStep(ctx, stepID, StatusRunning)
	if err != nil || step.StartedAt == nil {
		t.Fatalf("start step = %#v, %v", step, err)
	}
	step, err = store.TransitionStep(ctx, stepID, StatusSucceeded)
	if err != nil || step.FinishedAt == nil {
		t.Fatalf("finish step = %#v, %v", step, err)
	}
	job, err = store.TransitionJob(ctx, jobID, StatusSucceeded)
	if err != nil || job.FinishedAt == nil {
		t.Fatalf("finish job = %#v, %v", job, err)
	}
	cancellation, err := store.RequestRunCancellation(ctx, run.ID)
	if err != nil || !cancellation.Requested || cancellation.RequestedAt == nil {
		t.Fatalf("request cancellation = %#v, %v", cancellation, err)
	}
	readCancellation, err := store.GetRunCancellation(ctx, run.ID)
	if err != nil || !readCancellation.Requested || readCancellation.RequestedAt == nil {
		t.Fatalf("read cancellation = %#v, %v", readCancellation, err)
	}
	finished, err := store.TransitionRun(ctx, run.ID, StatusCancelled)
	if err != nil || finished.FinishedAt == nil || finished.Status != StatusCancelled {
		t.Fatalf("cancel run = %#v, %v", finished, err)
	}
	if _, err := store.TransitionRun(ctx, run.ID, StatusFailed); err == nil {
		t.Fatal("terminal run transition unexpectedly succeeded")
	}

	queued, err := store.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "queued-cancel"))
	if err != nil {
		t.Fatalf("enqueue queued cancellation run: %v", err)
	}
	if _, err := store.RequestRunCancellation(ctx, queued.ID); err != nil {
		t.Fatalf("cancel queued run: %v", err)
	}
	queuedGraph, err := store.GetRunGraph(ctx, queued.ID)
	if err != nil || queuedGraph.Run.Status != StatusCancelled || queuedGraph.Run.FinishedAt == nil {
		t.Fatalf("queued cancellation graph = %#v, %v", queuedGraph, err)
	}
}

func TestLogLinesAreOrderedAcrossConcurrentAppendsAndRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "logs.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, store, "logs")
	run, err := store.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "logs"))
	if err != nil {
		store.Close()
		t.Fatalf("enqueue run: %v", err)
	}
	graph, err := store.GetRunGraph(ctx, run.ID)
	if err != nil {
		store.Close()
		t.Fatalf("get graph: %v", err)
	}
	stepID := graph.Jobs[0].Steps[0].ID

	const linesToAppend = 20
	start := make(chan struct{})
	errs := make(chan error, linesToAppend)
	var waitGroup sync.WaitGroup
	for index := 0; index < linesToAppend; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			_, err := store.AppendLogLine(ctx, AppendLogLineParams{
				StepID:  stepID,
				Stream:  LogStreamStdout,
				Message: fmt.Sprintf("line-%02d", index),
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened store: %v", err)
		}
	})
	lines, err := reopened.ListLogLines(ctx, stepID)
	if err != nil {
		t.Fatalf("list log lines: %v", err)
	}
	if len(lines) != linesToAppend {
		t.Fatalf("log line count = %d, want %d", len(lines), linesToAppend)
	}
	for index, line := range lines {
		if line.Sequence != int64(index+1) || line.StepID != stepID || line.Message == "" {
			t.Errorf("line %d = %#v", index, line)
		}
	}
}

func TestMarkInterruptedRunningRunsFailedAtStartup(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "recovery.db")
	first, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, first, "recovery")
	firstRun, err := first.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "first"))
	if err != nil {
		first.Close()
		t.Fatalf("enqueue first run: %v", err)
	}
	secondRun, err := first.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "second"))
	if err != nil {
		first.Close()
		t.Fatalf("enqueue second run: %v", err)
	}
	queuedRun, err := first.EnqueueRun(ctx, testEnqueueRunParams(project.ID, workflow.ID, "queued"))
	if err != nil {
		first.Close()
		t.Fatalf("enqueue queued run: %v", err)
	}
	for _, worker := range []string{"worker-one", "worker-two"} {
		if _, err := first.ClaimNextQueuedRun(ctx, worker); err != nil {
			first.Close()
			t.Fatalf("claim interrupted run: %v", err)
		}
	}
	firstGraph, err := first.GetRunGraph(ctx, firstRun.ID)
	if err != nil {
		first.Close()
		t.Fatalf("get first graph: %v", err)
	}
	if _, err := first.TransitionJob(ctx, firstGraph.Jobs[0].Job.ID, StatusRunning); err != nil {
		first.Close()
		t.Fatalf("start first job: %v", err)
	}
	if _, err := first.TransitionStep(ctx, firstGraph.Jobs[0].Steps[0].ID, StatusRunning); err != nil {
		first.Close()
		t.Fatalf("start first step: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open recovered store: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close recovered store: %v", err)
		}
	})
	count, err := second.MarkInterruptedRunningRunsFailed(ctx)
	if err != nil {
		t.Fatalf("recover interrupted runs: %v", err)
	}
	if count != 2 {
		t.Fatalf("recovered %d runs, want 2", count)
	}
	recoveredFirst, err := second.GetRunGraph(ctx, firstRun.ID)
	if err != nil {
		t.Fatalf("get recovered first graph: %v", err)
	}
	if recoveredFirst.Run.Status != StatusFailed || recoveredFirst.Run.FinishedAt == nil || recoveredFirst.Jobs[0].Job.Status != StatusFailed || recoveredFirst.Jobs[0].Steps[0].Status != StatusFailed {
		t.Fatalf("recovered first graph = %#v", recoveredFirst)
	}
	recoveredSecond, err := second.GetRunGraph(ctx, secondRun.ID)
	if err != nil {
		t.Fatalf("get recovered second graph: %v", err)
	}
	if recoveredSecond.Run.Status != StatusFailed || recoveredSecond.Jobs[0].Job.Status != StatusSkipped {
		t.Fatalf("recovered second graph = %#v", recoveredSecond)
	}
	stillQueued, err := second.GetRunGraph(ctx, queuedRun.ID)
	if err != nil || stillQueued.Run.Status != StatusQueued {
		t.Fatalf("queued graph = %#v, %v", stillQueued, err)
	}
}

func createExecutionProjectAndWorkflow(t *testing.T, ctx context.Context, store *Store, slug string) (Project, Workflow) {
	t.Helper()
	project, err := store.CreateProject(ctx, testProjectParams(slug))
	if err != nil {
		t.Fatalf("create execution project: %v", err)
	}
	workflow, err := store.UpsertWorkflow(ctx, UpsertWorkflowParams{
		ProjectID:   project.ID,
		Key:         "pipeline",
		Name:        "Pipeline",
		Definition:  json.RawMessage(`{"on":["push"],"jobs":{"build":{}}}`),
		Environment: json.RawMessage(`{"CI":"true"}`),
	})
	if err != nil {
		t.Fatalf("create execution workflow: %v", err)
	}
	return project, workflow
}

func testEnqueueRunParams(projectID, workflowID, suffix string) EnqueueRunParams {
	return EnqueueRunParams{
		ProjectID:   projectID,
		WorkflowID:  workflowID,
		TriggerType: "manual",
		Ref:         "refs/heads/main",
		CommitSHA:   "sha-" + suffix,
		SourcePath:  "/srv/projects/" + suffix,
		Jobs: []EnqueueJob{
			{
				Key:            "build",
				Name:           "Build",
				Runner:         "local",
				DependencyKeys: json.RawMessage(`[]`),
				Steps: []EnqueueStep{
					{Key: "test", Name: "Test", Command: "go test ./..."},
				},
			},
		},
	}
}

func sameJSON(left, right json.RawMessage) bool {
	var leftValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	var rightValue any
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return fmt.Sprintf("%#v", leftValue) == fmt.Sprintf("%#v", rightValue)
}
