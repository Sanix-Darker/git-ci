package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestVersionedConfigurationAPIContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	workflowPath := filepath.Join(fixture.projectPath, ".github", "workflows", "deploy.yml")
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
		t.Fatalf("create workflow directory: %v", err)
	}
	if err := os.WriteFile(workflowPath, []byte(`name: Deploy
on: push
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: printf deploy
`), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/projects/missing/secrets", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")

	cookie, csrf := fixture.login(t)
	createdProject := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"slug": "configuration-project", "name": "Configuration project", "path": fixture.projectPath, "defaultBranch": "main",
	}, "", cookie, csrf, nil)
	if createdProject.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, body=%s", createdProject.Code, createdProject.Body.String())
	}
	var project store.Project
	decodeResponse(t, createdProject, &project)

	synced := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/workflows/sync", nil, "", cookie, csrf, nil)
	if synced.Code != http.StatusOK {
		t.Fatalf("sync workflows status = %d, body=%s", synced.Code, synced.Body.String())
	}
	var workflows struct {
		Items []store.Workflow `json:"items"`
		Count int              `json:"count"`
	}
	decodeResponse(t, synced, &workflows)
	if workflows.Count != 1 || len(workflows.Items) != 1 {
		t.Fatalf("synced workflows = %#v, want one workflow", workflows)
	}
	workflow := workflows.Items[0]

	missingSecretCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/secrets", map[string]any{"name": "DEPLOY_TOKEN", "value": "plain-secret"}, "", cookie, "", nil)
	assertAPIError(t, missingSecretCSRF, http.StatusForbidden, "csrf_failed")
	createdSecret := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/secrets", map[string]any{"name": "DEPLOY_TOKEN", "value": "plain-secret"}, "", cookie, csrf, nil)
	if createdSecret.Code != http.StatusCreated {
		t.Fatalf("create secret status = %d, body=%s", createdSecret.Code, createdSecret.Body.String())
	}
	if strings.Contains(createdSecret.Body.String(), "plain-secret") || strings.Contains(createdSecret.Body.String(), "ciphertext") || strings.Contains(createdSecret.Body.String(), "nonce") {
		t.Fatalf("secret create exposed sensitive material: %s", createdSecret.Body.String())
	}
	var secret store.Secret
	decodeResponse(t, createdSecret, &secret)
	envelope, err := fixture.store.GetSecretEnvelope(t.Context(), secret.ID)
	if err != nil || len(envelope.Ciphertext) == 0 || string(envelope.Ciphertext) == "plain-secret" {
		t.Fatalf("secret was not stored as an encrypted envelope: %#v, err=%v", envelope, err)
	}
	listedSecrets := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/secrets", nil, "", cookie, "", nil)
	if listedSecrets.Code != http.StatusOK || strings.Contains(listedSecrets.Body.String(), "plain-secret") || strings.Contains(listedSecrets.Body.String(), "ciphertext") || strings.Contains(listedSecrets.Body.String(), "nonce") {
		t.Fatalf("list secrets status = %d, body=%s", listedSecrets.Code, listedSecrets.Body.String())
	}
	invalidSecret := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/secrets", map[string]any{"name": "lowercase", "value": "value"}, "", cookie, csrf, nil)
	assertAPIError(t, invalidSecret, http.StatusUnprocessableEntity, "execution_failed")
	deletedSecret := fixture.request(t, http.MethodDelete, "/api/v1/secrets/"+secret.ID, nil, "", cookie, csrf, nil)
	if deletedSecret.Code != http.StatusNoContent {
		t.Fatalf("delete secret status = %d, body=%s", deletedSecret.Code, deletedSecret.Body.String())
	}

	missingScheduleCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/schedules", map[string]any{"workflowId": workflow.ID, "cron": "0 * * * *", "timezone": "UTC", "enabled": true}, "", cookie, "", nil)
	assertAPIError(t, missingScheduleCSRF, http.StatusForbidden, "csrf_failed")
	invalidSchedule := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/schedules", map[string]any{"workflowId": workflow.ID, "cron": "not cron", "timezone": "UTC", "enabled": true}, "", cookie, csrf, nil)
	assertAPIError(t, invalidSchedule, http.StatusUnprocessableEntity, "execution_failed")
	createdSchedule := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/schedules", map[string]any{"workflowId": workflow.ID, "cron": "0 * * * *", "ref": "refs/heads/main", "timezone": "UTC", "enabled": true}, "", cookie, csrf, nil)
	if createdSchedule.Code != http.StatusCreated {
		t.Fatalf("create schedule status = %d, body=%s", createdSchedule.Code, createdSchedule.Body.String())
	}
	var schedule store.WorkflowSchedule
	decodeResponse(t, createdSchedule, &schedule)
	listedSchedules := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/schedules", nil, "", cookie, "", nil)
	if listedSchedules.Code != http.StatusOK || !strings.Contains(listedSchedules.Body.String(), schedule.ID) {
		t.Fatalf("list schedules status = %d, body=%s", listedSchedules.Code, listedSchedules.Body.String())
	}
	updatedSchedule := fixture.request(t, http.MethodPatch, "/api/v1/schedules/"+schedule.ID, map[string]any{"cron": "*/2 * * * *", "ref": "refs/heads/release", "timezone": "UTC", "enabled": false}, "", cookie, csrf, nil)
	if updatedSchedule.Code != http.StatusOK {
		t.Fatalf("update schedule status = %d, body=%s", updatedSchedule.Code, updatedSchedule.Body.String())
	}
	decodeResponse(t, updatedSchedule, &schedule)
	if schedule.Cron != "*/2 * * * *" || schedule.Enabled || schedule.Ref == nil || *schedule.Ref != "refs/heads/release" {
		t.Fatalf("updated schedule = %#v", schedule)
	}
	deletedSchedule := fixture.request(t, http.MethodDelete, "/api/v1/schedules/"+schedule.ID, nil, "", cookie, csrf, nil)
	if deletedSchedule.Code != http.StatusNoContent {
		t.Fatalf("delete schedule status = %d, body=%s", deletedSchedule.Code, deletedSchedule.Body.String())
	}

	invalidWebhook := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/webhooks", map[string]any{"name": "invalid", "provider": "unknown", "workflowId": workflow.ID}, "", cookie, csrf, nil)
	assertAPIError(t, invalidWebhook, http.StatusUnprocessableEntity, "execution_failed")
	createdWebhook := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/webhooks", map[string]any{"name": "github push", "provider": "github", "workflowId": workflow.ID, "ref": "refs/heads/main"}, "", cookie, csrf, nil)
	if createdWebhook.Code != http.StatusCreated {
		t.Fatalf("create webhook status = %d, body=%s", createdWebhook.Code, createdWebhook.Body.String())
	}
	if strings.Contains(createdWebhook.Body.String(), "tokenHash") {
		t.Fatalf("webhook create exposed token hash: %s", createdWebhook.Body.String())
	}
	var createdEndpoint struct {
		Endpoint store.WebhookEndpoint `json:"endpoint"`
		Token    string                `json:"token"`
	}
	decodeResponse(t, createdWebhook, &createdEndpoint)
	if createdEndpoint.Token == "" {
		t.Fatal("webhook create did not return the one-time token")
	}
	listedWebhooks := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/webhooks", nil, "", cookie, "", nil)
	if listedWebhooks.Code != http.StatusOK || strings.Contains(listedWebhooks.Body.String(), createdEndpoint.Token) || strings.Contains(listedWebhooks.Body.String(), "tokenHash") {
		t.Fatalf("webhook list status = %d, body=%s", listedWebhooks.Code, listedWebhooks.Body.String())
	}

	deliveryHeaders := http.Header{
		"X-Git-CI-Token":    []string{createdEndpoint.Token},
		"X-Git-CI-Delivery": []string{"delivery-001"},
		"X-Git-CI-Event":    []string{"push"},
	}
	firstDelivery := fixture.request(t, http.MethodPost, "/hooks/"+createdEndpoint.Endpoint.ID, map[string]any{"ref": "refs/heads/main", "after": "abc123"}, "", nil, "", deliveryHeaders)
	if firstDelivery.Code != http.StatusAccepted || strings.Contains(firstDelivery.Body.String(), `"duplicate":true`) {
		t.Fatalf("first webhook delivery status = %d, body=%s", firstDelivery.Code, firstDelivery.Body.String())
	}
	var firstPayload struct {
		Run *store.Run `json:"run"`
	}
	decodeResponse(t, firstDelivery, &firstPayload)
	if firstPayload.Run == nil || firstPayload.Run.Status != store.StatusQueued {
		t.Fatalf("first webhook delivery did not enqueue a queued run: %s", firstDelivery.Body.String())
	}
	duplicateDelivery := fixture.request(t, http.MethodPost, "/hooks/"+createdEndpoint.Endpoint.ID, map[string]any{"ref": "refs/heads/main", "after": "abc123"}, "", nil, "", deliveryHeaders)
	if duplicateDelivery.Code != http.StatusAccepted || !strings.Contains(duplicateDelivery.Body.String(), `"duplicate":true`) || strings.Contains(duplicateDelivery.Body.String(), `"run":`) {
		t.Fatalf("duplicate webhook delivery status = %d, body=%s", duplicateDelivery.Code, duplicateDelivery.Body.String())
	}
	runs := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/runs", nil, "", cookie, "", nil)
	var runList struct {
		Items []store.Run `json:"items"`
		Count int         `json:"count"`
	}
	decodeResponse(t, runs, &runList)
	if runs.Code != http.StatusOK || runList.Count != 1 || runList.Items[0].ID != firstPayload.Run.ID {
		t.Fatalf("webhook runs status = %d, payload=%#v", runs.Code, runList)
	}

	missingDeploymentCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/deployments", map[string]any{"runId": firstPayload.Run.ID, "environment": "production"}, "", cookie, "", nil)
	assertAPIError(t, missingDeploymentCSRF, http.StatusForbidden, "csrf_failed")
	createdDeployment := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/deployments", map[string]any{"runId": firstPayload.Run.ID, "environment": "production"}, "", cookie, csrf, nil)
	if createdDeployment.Code != http.StatusCreated {
		t.Fatalf("create deployment status = %d, body=%s", createdDeployment.Code, createdDeployment.Body.String())
	}
	var deployment store.Deployment
	decodeResponse(t, createdDeployment, &deployment)
	invalidTransition := fixture.request(t, http.MethodPatch, "/api/v1/deployments/"+deployment.ID, map[string]any{"status": "invalid"}, "", cookie, csrf, nil)
	assertAPIError(t, invalidTransition, http.StatusUnprocessableEntity, "execution_failed")
	runningDeployment := fixture.request(t, http.MethodPatch, "/api/v1/deployments/"+deployment.ID, map[string]any{"status": store.StatusRunning}, "", cookie, csrf, nil)
	if runningDeployment.Code != http.StatusOK {
		t.Fatalf("start deployment status = %d, body=%s", runningDeployment.Code, runningDeployment.Body.String())
	}
	completedDeployment := fixture.request(t, http.MethodPatch, "/api/v1/deployments/"+deployment.ID, map[string]any{"status": store.StatusSucceeded, "reason": "published"}, "", cookie, csrf, nil)
	if completedDeployment.Code != http.StatusOK {
		t.Fatalf("complete deployment status = %d, body=%s", completedDeployment.Code, completedDeployment.Body.String())
	}
	decodeResponse(t, completedDeployment, &deployment)
	if deployment.Status != store.StatusSucceeded || deployment.FinishedAt == nil || len(deployment.History) != 3 || deployment.History[2].Reason == nil || *deployment.History[2].Reason != "published" {
		t.Fatalf("completed deployment = %#v", deployment)
	}
	listedDeployments := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/deployments", nil, "", cookie, "", nil)
	if listedDeployments.Code != http.StatusOK || !strings.Contains(listedDeployments.Body.String(), deployment.ID) {
		t.Fatalf("list deployments status = %d, body=%s", listedDeployments.Code, listedDeployments.Body.String())
	}
}
