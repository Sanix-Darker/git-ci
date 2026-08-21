package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManualJobPlayAPIWebAndAuditContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	ctx := t.Context()
	path := fixture.projectPath
	project, err := fixture.store.CreateProject(ctx, store.CreateProjectParams{Slug: "manual-api", Name: "Manual API", SourceType: "git", CanonicalPath: &path, DefaultBranch: "main", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := fixture.store.UpsertWorkflow(ctx, store.UpsertWorkflowParams{ProjectID: project.ID, Key: "manual-api:gitlab:test", Name: "Manual", Definition: json.RawMessage(`{"name":"Manual"}`), Environment: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.EnqueueRun(ctx, store.EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: fixture.projectHead(t), SourcePath: fixture.projectPath, Jobs: []store.EnqueueJob{{Key: "release", Name: "Release", DependencyKeys: json.RawMessage(`[]`), Steps: []store.EnqueueStep{{Key: "ship", Name: "Ship", Command: "printf ship"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := fixture.store.ClaimNextQueuedRun(ctx, "manual-api-worker"); err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	graph, _ := fixture.store.GetRunGraph(ctx, run.ID)
	jobID := graph.Jobs[0].Job.ID
	if _, err := fixture.store.PauseManualJob(ctx, store.PauseManualJobParams{JobID: jobID, Blocking: true, Confirmation: "Ship production?"}); err != nil {
		t.Fatal(err)
	}
	pathAPI := "/api/v1/runs/" + run.ID + "/jobs/" + jobID + "/plays"
	unauthorized := fixture.request(t, http.MethodPost, pathAPI, map[string]any{"idempotencyKey": "x"}, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")
	cookie, csrf := fixture.login(t)
	page := fixture.request(t, http.MethodGet, "/app/runs/"+run.ID, nil, "", cookie, "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "/app/jobs/"+jobID+"/play") || !strings.Contains(page.Body.String(), "Ship production?") || !strings.Contains(page.Body.String(), "PLAIN / NOT SECRET") {
		t.Fatalf("manual web control = %d, %s", page.Code, page.Body.String())
	}
	missingCSRF := fixture.request(t, http.MethodPost, pathAPI, map[string]any{"idempotencyKey": "manual-api", "confirmed": true}, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")
	missingKey := fixture.request(t, http.MethodPost, pathAPI, map[string]any{}, "", cookie, csrf, nil)
	assertAPIError(t, missingKey, http.StatusUnprocessableEntity, "idempotency_key_required")
	unconfirmed := fixture.request(t, http.MethodPost, pathAPI, map[string]any{"idempotencyKey": "manual-unconfirmed"}, "", cookie, csrf, nil)
	assertAPIError(t, unconfirmed, http.StatusUnprocessableEntity, "manual_confirmation_required")
	ownership := fixture.request(t, http.MethodPost, "/api/v1/runs/wrong/jobs/"+jobID+"/plays", map[string]any{"idempotencyKey": "manual-owner", "confirmed": true}, "", cookie, csrf, nil)
	assertAPIError(t, ownership, http.StatusUnprocessableEntity, "manual_play_ownership_mismatch")
	invalid := fixture.request(t, http.MethodPost, pathAPI, map[string]any{"idempotencyKey": "manual-invalid", "confirmed": true, "variables": map[string]string{"bad-key": "x"}}, "", cookie, csrf, nil)
	assertAPIError(t, invalid, http.StatusUnprocessableEntity, "manual_play_variables_invalid")
	played := fixture.request(t, http.MethodPost, pathAPI, map[string]any{"idempotencyKey": "manual-api", "confirmed": true, "variables": map[string]string{"TARGET": "production"}}, "", cookie, csrf, nil)
	var result store.ManualJobPlayResult
	decodeResponse(t, played, &result)
	if played.Code != http.StatusAccepted || result.Run.ID != run.ID || result.Run.Status != store.StatusQueued || result.Play.Variables["TARGET"] != "production" {
		t.Fatalf("manual API result = %d, %#v", played.Code, result)
	}
	repeated := fixture.request(t, http.MethodPost, pathAPI, map[string]any{"idempotencyKey": "manual-api", "confirmed": true}, "", cookie, csrf, nil)
	var duplicate store.ManualJobPlayResult
	decodeResponse(t, repeated, &duplicate)
	if repeated.Code != http.StatusAccepted || !duplicate.Idempotent || duplicate.Play.ID != result.Play.ID {
		t.Fatalf("idempotent manual API = %d, %#v", repeated.Code, duplicate)
	}
	audit := fixture.request(t, http.MethodGet, "/api/v1/audit?range=24h&q=job.played", nil, fixture.token, nil, "", nil)
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), `"action":"job.played"`) || !strings.Contains(audit.Body.String(), jobID) {
		t.Fatalf("manual audit = %d, %s", audit.Code, audit.Body.String())
	}
}
