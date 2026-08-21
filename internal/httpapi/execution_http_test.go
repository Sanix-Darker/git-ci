package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestVersionedExecutionAPIQueuedRunContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	workflowPath := filepath.Join(fixture.projectPath, ".github", "workflows", "verify.yml")
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	if err := os.WriteFile(workflowPath, []byte(`name: Verify
on: push
env:
  MODE: integration
jobs:
  prepare:
    runs-on: ubuntu-latest
    steps:
      - name: Prepare
        run: printf prepare
  verify:
    needs: prepare
    runs-on: ubuntu-latest
    steps:
      - name: Verify
        run: printf verify
`), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	fixture.commitProject(t)

	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/projects/missing/workflows", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")

	created := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"slug": "execution-project", "name": "Execution project", "path": fixture.projectPath, "defaultBranch": "main",
	}, fixture.token, nil, "", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body=%s", created.Code, created.Body.String())
	}
	var project store.Project
	decodeResponse(t, created, &project)

	cookie, csrf := fixture.login(t)
	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/workflows/sync", nil, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")

	synced := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/workflows/sync", nil, "", cookie, csrf, nil)
	if synced.Code != http.StatusOK {
		t.Fatalf("sync workflows status = %d, body=%s", synced.Code, synced.Body.String())
	}
	var syncedPayload struct {
		Items []store.Workflow `json:"items"`
		Count int              `json:"count"`
	}
	decodeResponse(t, synced, &syncedPayload)
	if syncedPayload.Count != 1 || len(syncedPayload.Items) != 1 {
		t.Fatalf("synced workflows = %#v, want one workflow", syncedPayload)
	}
	workflow := syncedPayload.Items[0]
	if workflow.Name != "Verify" || workflow.ProjectID != project.ID || !workflow.Active {
		t.Fatalf("synced workflow = %#v", workflow)
	}

	listed := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/workflows", nil, "", cookie, "", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list workflows status = %d, body=%s", listed.Code, listed.Body.String())
	}
	var listedPayload struct {
		Items []store.Workflow `json:"items"`
		Count int              `json:"count"`
	}
	decodeResponse(t, listed, &listedPayload)
	if listedPayload.Count != 1 || listedPayload.Items[0].ID != workflow.ID {
		t.Fatalf("listed workflows = %#v, want synced workflow %q", listedPayload, workflow.ID)
	}

	detail := fixture.request(t, http.MethodGet, "/api/v1/workflows/"+workflow.ID, nil, "", cookie, "", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("workflow detail status = %d, body=%s", detail.Code, detail.Body.String())
	}
	var detailedWorkflow store.Workflow
	decodeResponse(t, detail, &detailedWorkflow)
	if detailedWorkflow.ID != workflow.ID || detailedWorkflow.Revision != workflow.Revision {
		t.Fatalf("workflow detail = %#v, want %#v", detailedWorkflow, workflow)
	}

	missingWorkflow := fixture.request(t, http.MethodGet, "/api/v1/workflows/missing", nil, "", cookie, "", nil)
	assertAPIError(t, missingWorkflow, http.StatusNotFound, "not_found")

	enqueueMissingCSRF := fixture.request(t, http.MethodPost, "/api/v1/workflows/"+workflow.ID+"/runs", map[string]any{"ref": "refs/heads/main"}, "", cookie, "", nil)
	assertAPIError(t, enqueueMissingCSRF, http.StatusForbidden, "csrf_failed")

	enqueued := fixture.request(t, http.MethodPost, "/api/v1/workflows/"+workflow.ID+"/runs", map[string]any{
		"ref": "refs/heads/main", "commitSha": fixture.projectHead(t),
	}, "", cookie, csrf, nil)
	if enqueued.Code != http.StatusAccepted {
		t.Fatalf("enqueue status = %d, body=%s", enqueued.Code, enqueued.Body.String())
	}
	var run store.Run
	decodeResponse(t, enqueued, &run)
	if run.Status != store.StatusQueued || run.WorkflowID == nil || *run.WorkflowID != workflow.ID {
		t.Fatalf("enqueued run = %#v, want queued snapshot for %q", run, workflow.ID)
	}

	runs := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/runs", nil, "", cookie, "", nil)
	if runs.Code != http.StatusOK {
		t.Fatalf("list runs status = %d, body=%s", runs.Code, runs.Body.String())
	}
	var runsPayload struct {
		Items []store.Run `json:"items"`
		Count int         `json:"count"`
	}
	decodeResponse(t, runs, &runsPayload)
	if runsPayload.Count != 1 || runsPayload.Items[0].ID != run.ID {
		t.Fatalf("listed runs = %#v, want queued run %q", runsPayload, run.ID)
	}

	graphResponse := fixture.request(t, http.MethodGet, "/api/v1/runs/"+run.ID, nil, "", cookie, "", nil)
	if graphResponse.Code != http.StatusOK {
		t.Fatalf("run graph status = %d, body=%s", graphResponse.Code, graphResponse.Body.String())
	}
	var graph store.RunGraph
	decodeResponse(t, graphResponse, &graph)
	if graph.Run.ID != run.ID || graph.Run.Status != store.StatusQueued || len(graph.Jobs) != 2 {
		t.Fatalf("run graph = %#v, want queued two-job graph", graph)
	}
	if graph.Jobs[0].Job.Status != store.StatusQueued || graph.Jobs[0].Job.Key == nil || *graph.Jobs[0].Job.Key != "prepare" || len(graph.Jobs[0].Steps) != 1 {
		t.Fatalf("first graph job = %#v, want queued prepare snapshot", graph.Jobs[0])
	}
	if graph.Jobs[1].Job.Status != store.StatusQueued || string(graph.Jobs[1].Job.DependencyKeys) != `["prepare"]` || len(graph.Jobs[1].Steps) != 1 {
		t.Fatalf("second graph job = %#v, want queued verify dependency snapshot", graph.Jobs[1])
	}
	summary := "# API summary\n\n<script>alert(\"no\")</script>"
	if _, err := fixture.store.SetStepSummary(t.Context(), graph.Jobs[0].Steps[0].ID, summary); err != nil {
		t.Fatalf("set step summary: %v", err)
	}
	summaryResponse := fixture.request(t, http.MethodGet, "/api/v1/runs/"+run.ID, nil, "", cookie, "", nil)
	if summaryResponse.Code != http.StatusOK {
		t.Fatalf("summary graph status = %d, body=%s", summaryResponse.Code, summaryResponse.Body.String())
	}
	var summarizedGraph store.RunGraph
	decodeResponse(t, summaryResponse, &summarizedGraph)
	if summarizedGraph.Jobs[0].Steps[0].Summary != summary {
		t.Fatalf("step summary = %q, want %q", summarizedGraph.Jobs[0].Steps[0].Summary, summary)
	}
	runPage := fixture.request(t, http.MethodGet, "/app/runs/"+run.ID, nil, "", cookie, "", nil)
	if runPage.Code != http.StatusOK || !strings.Contains(runPage.Body.String(), "STEP SUMMARY") || !strings.Contains(runPage.Body.String(), "&lt;script&gt;") || strings.Contains(runPage.Body.String(), "<script>") {
		t.Fatalf("run summary page status = %d, body=%s", runPage.Code, runPage.Body.String())
	}

	if _, err := fixture.store.AppendLogLine(t.Context(), store.AppendLogLineParams{StepID: graph.Jobs[0].Steps[0].ID, Stream: store.LogStreamStdout, Message: "prepare output"}); err != nil {
		t.Fatalf("append stdout log: %v", err)
	}
	if _, err := fixture.store.AppendLogLine(t.Context(), store.AppendLogLineParams{StepID: graph.Jobs[1].Steps[0].ID, Stream: store.LogStreamStderr, Message: "verify warning"}); err != nil {
		t.Fatalf("append stderr log: %v", err)
	}
	logs := fixture.request(t, http.MethodGet, "/api/v1/runs/"+run.ID+"/logs", nil, "", cookie, "", nil)
	if logs.Code != http.StatusOK {
		t.Fatalf("run logs status = %d, body=%s", logs.Code, logs.Body.String())
	}
	var logsPayload struct {
		Items []store.LogLine `json:"items"`
		Count int             `json:"count"`
	}
	decodeResponse(t, logs, &logsPayload)
	if logsPayload.Count != 2 || len(logsPayload.Items) != 2 || logsPayload.Items[0].Sequence != 1 || logsPayload.Items[0].Message != "prepare output" || logsPayload.Items[1].Sequence != 2 || logsPayload.Items[1].Stream != store.LogStreamStderr {
		t.Fatalf("run logs = %#v, want ordered durable logs", logsPayload)
	}

	cancelMissingCSRF := fixture.request(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", nil, "", cookie, "", nil)
	assertAPIError(t, cancelMissingCSRF, http.StatusForbidden, "csrf_failed")
	cancelled := fixture.request(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", nil, "", cookie, csrf, nil)
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, body=%s", cancelled.Code, cancelled.Body.String())
	}
	var cancellation store.RunCancellation
	decodeResponse(t, cancelled, &cancellation)
	if cancellation.RunID != run.ID || !cancellation.Requested || cancellation.RequestedAt == nil {
		t.Fatalf("cancellation = %#v, want durable queued cancellation", cancellation)
	}

	cancelledGraph := fixture.request(t, http.MethodGet, "/api/v1/runs/"+run.ID, nil, "", cookie, "", nil)
	if cancelledGraph.Code != http.StatusOK {
		t.Fatalf("cancelled run graph status = %d, body=%s", cancelledGraph.Code, cancelledGraph.Body.String())
	}
	decodeResponse(t, cancelledGraph, &graph)
	if graph.Run.Status != store.StatusCancelled || !graph.Run.CancellationRequested || graph.Run.FinishedAt == nil {
		t.Fatalf("cancelled queued graph = %#v, want cancelled run with durable cancellation signal", graph.Run)
	}

	missingRun := fixture.request(t, http.MethodGet, "/api/v1/runs/missing", nil, "", cookie, "", nil)
	assertAPIError(t, missingRun, http.StatusNotFound, "not_found")
	missingRunLogs := fixture.request(t, http.MethodGet, "/api/v1/runs/missing/logs", nil, "", cookie, "", nil)
	assertAPIError(t, missingRunLogs, http.StatusNotFound, "not_found")
}
