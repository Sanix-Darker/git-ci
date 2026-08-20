package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestVersionedDeploymentRollbackContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	ctx := t.Context()
	projectPath := fixture.projectPath
	project, err := fixture.store.CreateProject(ctx, store.CreateProjectParams{
		Slug: "rollback-api", Name: "Rollback API", SourceType: "git",
		CanonicalPath: &projectPath, DefaultBranch: "main", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := fixture.store.UpsertWorkflow(ctx, store.UpsertWorkflowParams{
		ProjectID: project.ID, Key: "rollback-api:github:deploy", Name: "Deploy",
		Definition: json.RawMessage(`{"name":"Deploy"}`), Environment: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.projectHead(t)
	_, target := apiRollbackDeploymentFixture(t, fixture, project.ID, workflow.ID, commit, store.StatusSucceeded)
	_, source := apiRollbackDeploymentFixture(t, fixture, project.ID, workflow.ID, commit, store.StatusFailed)

	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/deployments/"+source.ID+"/rollback-options", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")
	cookie, csrf := fixture.login(t)
	options := fixture.request(t, http.MethodGet, "/api/v1/deployments/"+source.ID+"/rollback-options", nil, "", cookie, "", nil)
	if options.Code != http.StatusOK || !strings.Contains(options.Body.String(), target.ID) || strings.Contains(options.Body.String(), "printf rollback") {
		t.Fatalf("rollback options = %d, %s", options.Code, options.Body.String())
	}
	missingTarget := fixture.request(t, http.MethodPost, "/api/v1/deployments/"+source.ID+"/rollback", map[string]any{"idempotencyKey": "api-rollback"}, "", cookie, csrf, nil)
	assertAPIError(t, missingTarget, http.StatusUnprocessableEntity, "target_deployment_required")
	missingKey := fixture.request(t, http.MethodPost, "/api/v1/deployments/"+source.ID+"/rollback", map[string]any{"targetDeploymentId": target.ID}, "", cookie, csrf, nil)
	assertAPIError(t, missingKey, http.StatusUnprocessableEntity, "idempotency_key_required")
	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/deployments/"+source.ID+"/rollback", map[string]any{"targetDeploymentId": target.ID, "idempotencyKey": "api-rollback"}, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")

	queued := fixture.request(t, http.MethodPost, "/api/v1/deployments/"+source.ID+"/rollback", map[string]any{"targetDeploymentId": target.ID, "idempotencyKey": "api-rollback"}, "", cookie, csrf, nil)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("queue rollback = %d, %s", queued.Code, queued.Body.String())
	}
	var run store.Run
	decodeResponse(t, queued, &run)
	if run.TriggerType != "rollback" || run.Status != store.StatusQueued || run.CommitSHA == nil || *run.CommitSHA != commit {
		t.Fatalf("rollback run = %#v", run)
	}
	retried := fixture.request(t, http.MethodPost, "/api/v1/deployments/"+source.ID+"/rollback", map[string]any{"targetDeploymentId": target.ID, "idempotencyKey": "api-rollback"}, "", cookie, csrf, nil)
	var retriedRun store.Run
	decodeResponse(t, retried, &retriedRun)
	if retried.Code != http.StatusAccepted || retriedRun.ID != run.ID {
		t.Fatalf("idempotent retry = %d, %#v", retried.Code, retriedRun)
	}
}

func apiRollbackDeploymentFixture(t *testing.T, fixture *apiFixture, projectID, workflowID, commit string, status store.Status) (store.Run, store.Deployment) {
	t.Helper()
	run, err := fixture.store.EnqueueRun(t.Context(), store.EnqueueRunParams{
		ProjectID: projectID, WorkflowID: workflowID, TriggerType: "manual",
		Ref: "refs/heads/main", CommitSHA: commit, SourcePath: fixture.projectPath,
		Jobs: []store.EnqueueJob{{
			Key: "deploy", Name: "Deploy", EnvironmentName: "production", DeploymentTier: "production",
			DependencyKeys: json.RawMessage(`[]`), RollbackCommand: "printf rollback", VerifyCommand: "printf verify",
			Steps: []store.EnqueueStep{{Key: "deploy", Name: "Deploy", Command: "printf deploy"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.store.GetRunGraph(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := fixture.store.EnsureDeploymentForJob(t.Context(), graph.Jobs[0].Job.ID)
	if err == nil {
		deployment, err = fixture.store.TransitionDeployment(t.Context(), deployment.ID, store.StatusRunning, nil)
	}
	if err == nil {
		deployment, err = fixture.store.TransitionDeployment(t.Context(), deployment.ID, status, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return run, deployment
}
