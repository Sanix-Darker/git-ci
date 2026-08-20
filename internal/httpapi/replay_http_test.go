package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestVersionedJobAndStepReplayContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	ctx := t.Context()
	path := fixture.projectPath
	project, err := fixture.store.CreateProject(ctx, store.CreateProjectParams{Slug: "replay-api", Name: "Replay API", SourceType: "git", CanonicalPath: &path, DefaultBranch: "main", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := fixture.store.UpsertWorkflow(ctx, store.UpsertWorkflowParams{ProjectID: project.ID, Key: "replay-api:github:test", Name: "Replay", Definition: json.RawMessage(`{"name":"Replay"}`), Environment: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	source, err := fixture.store.EnqueueRun(ctx, store.EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: fixture.projectHead(t), SourcePath: fixture.projectPath, Jobs: []store.EnqueueJob{{Key: "test", Name: "Test", DependencyKeys: json.RawMessage(`[]`), Steps: []store.EnqueueStep{{Key: "test", Name: "Test", Command: "printf replay-api"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	completeAPIReplaySource(t, fixture, source.ID)
	graph, _ := fixture.store.GetRunGraph(ctx, source.ID)
	jobID, stepID := graph.Jobs[0].Job.ID, graph.Jobs[0].Steps[0].ID

	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/jobs/"+jobID+"/replay-options", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")
	cookie, csrf := fixture.login(t)
	options := fixture.request(t, http.MethodGet, "/api/v1/jobs/"+jobID+"/replay-options", nil, "", cookie, "", nil)
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), `"eligible":true`) || strings.Contains(options.Body.String(), "printf replay-api") {
		t.Fatalf("job replay options = %d, %s", options.Code, options.Body.String())
	}
	stepOptions := fixture.request(t, http.MethodGet, "/api/v1/steps/"+stepID+"/replay-options", nil, "", cookie, "", nil)
	if stepOptions.Code != http.StatusOK || !strings.Contains(stepOptions.Body.String(), `"eligible":true`) || strings.Contains(stepOptions.Body.String(), "printf replay-api") {
		t.Fatalf("step replay options = %d, %s", stepOptions.Code, stepOptions.Body.String())
	}
	page := fixture.request(t, http.MethodGet, "/app/runs/"+source.ID, nil, "", cookie, "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/app/jobs/"+jobID+"/replay") || !strings.Contains(page.Body.String(), "PLAY STEP") {
		t.Fatalf("replay web controls = %d, %s", page.Code, page.Body.String())
	}
	missingKey := fixture.request(t, http.MethodPost, "/api/v1/jobs/"+jobID+"/replay", map[string]any{}, "", cookie, csrf, nil)
	assertAPIError(t, missingKey, http.StatusUnprocessableEntity, "idempotency_key_required")
	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/jobs/"+jobID+"/replay", map[string]any{"idempotencyKey": "api-job"}, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")

	queued := fixture.request(t, http.MethodPost, "/api/v1/jobs/"+jobID+"/replay", map[string]any{"idempotencyKey": "api-job"}, "", cookie, csrf, nil)
	var jobRun store.Run
	decodeResponse(t, queued, &jobRun)
	if queued.Code != http.StatusAccepted || jobRun.TriggerType != "job_replay" {
		t.Fatalf("job replay = %d, %#v", queued.Code, jobRun)
	}
	retried := fixture.request(t, http.MethodPost, "/api/v1/jobs/"+jobID+"/replay", map[string]any{"idempotencyKey": "api-job"}, "", cookie, csrf, nil)
	var retriedRun store.Run
	decodeResponse(t, retried, &retriedRun)
	if retried.Code != http.StatusAccepted || retriedRun.ID != jobRun.ID {
		t.Fatalf("idempotent job replay = %d, %#v", retried.Code, retriedRun)
	}
	duplicate := fixture.request(t, http.MethodPost, "/api/v1/jobs/"+jobID+"/replay", map[string]any{"idempotencyKey": "api-job-duplicate"}, "", cookie, csrf, nil)
	assertAPIError(t, duplicate, http.StatusUnprocessableEntity, "active_replay_exists")
	stepQueued := fixture.request(t, http.MethodPost, "/api/v1/steps/"+stepID+"/replay", map[string]any{"idempotencyKey": "api-step"}, "", cookie, csrf, nil)
	var stepRun store.Run
	decodeResponse(t, stepQueued, &stepRun)
	if stepQueued.Code != http.StatusAccepted || stepRun.TriggerType != "step_replay" {
		t.Fatalf("step replay = %d, %#v", stepQueued.Code, stepRun)
	}
}

func completeAPIReplaySource(t *testing.T, fixture *apiFixture, runID string) {
	t.Helper()
	ctx := t.Context()
	claimed, err := fixture.store.ClaimNextQueuedRun(ctx, "api-replay-worker")
	if err != nil || claimed == nil || claimed.ID != runID {
		t.Fatalf("claim API replay source = %#v, %v", claimed, err)
	}
	graph, _ := fixture.store.GetRunGraph(ctx, runID)
	for _, item := range graph.Jobs {
		if _, err := fixture.store.TransitionJob(ctx, item.Job.ID, store.StatusRunning); err != nil {
			t.Fatal(err)
		}
		for _, step := range item.Steps {
			if _, err := fixture.store.TransitionStep(ctx, step.ID, store.StatusRunning); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.store.TransitionStep(ctx, step.ID, store.StatusSucceeded); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fixture.store.TransitionJob(ctx, item.Job.ID, store.StatusSucceeded); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.store.TransitionRun(ctx, runID, store.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
}
