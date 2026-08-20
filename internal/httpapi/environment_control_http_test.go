package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestProtectedDeliveryAPIContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	cookie, csrf := fixture.login(t)
	createdProject := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{"slug": "delivery", "name": "Delivery", "path": fixture.projectPath}, "", cookie, csrf, nil)
	var project store.Project
	decodeResponse(t, createdProject, &project)
	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/environments", map[string]any{"name": "production"}, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")
	created := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/environments", map[string]any{
		"name": "production", "deploymentTier": "production", "protected": true,
		"requiredApprovals": 1, "allowedRefs": []string{"refs/heads/main"}, "concurrencyMode": "queue",
	}, "", cookie, csrf, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create environment = %d, %s", created.Code, created.Body.String())
	}
	var environment store.Environment
	decodeResponse(t, created, &environment)
	invalid := fixture.request(t, http.MethodPatch, "/api/v1/environments/"+environment.ID, map[string]any{"requiredApprovals": 2}, "", cookie, csrf, nil)
	assertAPIError(t, invalid, http.StatusUnprocessableEntity, "execution_failed")
	secretValue := "environment-only-value"
	secretResponse := fixture.request(t, http.MethodPost, "/api/v1/environments/"+environment.ID+"/secrets", map[string]any{"name": "DEPLOY_TOKEN", "value": secretValue}, "", cookie, csrf, nil)
	if secretResponse.Code != http.StatusCreated || strings.Contains(secretResponse.Body.String(), secretValue) || strings.Contains(secretResponse.Body.String(), "ciphertext") {
		t.Fatalf("environment secret response = %d, %s", secretResponse.Code, secretResponse.Body.String())
	}
	workflow, err := fixture.store.UpsertWorkflow(t.Context(), store.UpsertWorkflowParams{ProjectID: project.ID, Key: "deploy", Name: "Deploy", Definition: json.RawMessage(`{"jobs":{}}`), Environment: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.EnqueueRun(t.Context(), store.EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", SourcePath: fixture.projectPath,
		Jobs: []store.EnqueueJob{{Key: "deploy", Name: "Deploy", EnvironmentName: "production", DeploymentTier: "production", DependencyKeys: json.RawMessage(`[]`), Steps: []store.EnqueueStep{{Key: "ship", Name: "Ship", Command: "true"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.store.GetRunGraph(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	request, err := fixture.store.RequestEnvironmentApproval(t.Context(), store.RequestEnvironmentApprovalParams{JobID: graph.Jobs[0].Job.ID, RequestedBy: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	listed := fixture.request(t, http.MethodGet, "/api/v1/approvals?projectId="+project.ID+"&status=pending", nil, "", cookie, "", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), request.ID) || !strings.Contains(listed.Body.String(), "production") {
		t.Fatalf("approvals = %d, %s", listed.Code, listed.Body.String())
	}
	missingDecisionCSRF := fixture.request(t, http.MethodPost, "/api/v1/approvals/"+request.ID+"/decision", map[string]any{"decision": "approved"}, "", cookie, "", nil)
	assertAPIError(t, missingDecisionCSRF, http.StatusForbidden, "csrf_failed")
	decided := fixture.request(t, http.MethodPost, "/api/v1/approvals/"+request.ID+"/decision", map[string]any{"decision": "approved", "reason": "release window"}, "", cookie, csrf, nil)
	if decided.Code != http.StatusOK || !strings.Contains(decided.Body.String(), "approved") {
		t.Fatalf("decision = %d, %s", decided.Code, decided.Body.String())
	}
	detail := fixture.request(t, http.MethodGet, "/api/v1/approvals/"+request.ID, nil, "", cookie, "", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "release window") {
		t.Fatalf("approval detail = %d, %s", detail.Code, detail.Body.String())
	}
	deployment, err := fixture.store.EnsureDeploymentForJob(t.Context(), graph.Jobs[0].Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	deploymentDetail := fixture.request(t, http.MethodGet, "/api/v1/deployments/"+deployment.ID, nil, "", cookie, "", nil)
	if deploymentDetail.Code != http.StatusOK || !strings.Contains(deploymentDetail.Body.String(), graph.Jobs[0].Job.ID) || !strings.Contains(deploymentDetail.Body.String(), "production") {
		t.Fatalf("deployment detail = %d, %s", deploymentDetail.Code, deploymentDetail.Body.String())
	}
}
