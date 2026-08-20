package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
)

func (a *API) handleProjectSecrets(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project")
	if request.Method == http.MethodGet {
		items, err := a.secrets.List(request.Context(), projectID)
		if err != nil {
			a.writeStoreError(writer, err, "failed to list secrets")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	var payload struct{ Name, Value string }
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.secrets.Upsert(request.Context(), projectID, payload.Name, payload.Value)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "secret.upserted", "secret", item.ID)
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleSecret(writer http.ResponseWriter, request *http.Request) {
	if err := a.secrets.Delete(request.Context(), request.PathValue("secret")); err != nil {
		a.writeStoreError(writer, err, "secret not found")
		return
	}
	a.recordExecutionAudit(request, "secret.deleted", "secret", request.PathValue("secret"))
	writer.WriteHeader(http.StatusNoContent)
}

func (a *API) handleProjectSchedules(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project")
	if request.Method == http.MethodGet {
		items, err := a.store.ListWorkflowSchedules(request.Context(), projectID)
		if err != nil {
			a.writeStoreError(writer, err, "failed to list schedules")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	var payload struct {
		WorkflowID string `json:"workflowId"`
		Cron       string `json:"cron"`
		Ref        string `json:"ref"`
		Timezone   string `json:"timezone"`
		Enabled    bool   `json:"enabled"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.scheduler.Create(request.Context(), projectID, payload.WorkflowID, payload.Cron, payload.Ref, payload.Timezone, payload.Enabled)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "schedule.created", "schedule", item.ID)
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleSchedule(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("schedule")
	if request.Method == http.MethodDelete {
		if err := a.scheduler.Delete(request.Context(), id); err != nil {
			a.writeStoreError(writer, err, "schedule not found")
			return
		}
		a.recordExecutionAudit(request, "schedule.deleted", "schedule", id)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	var payload struct {
		Cron     string `json:"cron"`
		Ref      string `json:"ref"`
		Timezone string `json:"timezone"`
		Enabled  bool   `json:"enabled"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.scheduler.Update(request.Context(), id, payload.Cron, payload.Ref, payload.Timezone, payload.Enabled)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "schedule.updated", "schedule", id)
	writeJSON(writer, http.StatusOK, item)
}

func (a *API) handleProjectWebhooks(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project")
	if request.Method == http.MethodGet {
		items, err := a.store.ListWebhookEndpoints(request.Context(), projectID)
		if err != nil {
			a.writeStoreError(writer, err, "failed to list webhooks")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	var payload struct{ Name, Provider, WorkflowID, Ref string }
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.webhooks.Create(request.Context(), projectID, payload.Name, payload.Provider, payload.WorkflowID, payload.Ref)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "webhook.created", "webhook", item.Endpoint.ID)
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleWebhookDelivery(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, a.maxBodyBytes)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "failed to read webhook payload")
		return
	}
	token := strings.TrimSpace(request.Header.Get("X-Git-CI-Token"))
	if token == "" {
		token = strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	}
	deliveryID := firstHeader(request, "X-Git-CI-Delivery", "X-GitHub-Delivery", "X-Gitlab-Event-UUID")
	eventType := firstHeader(request, "X-Git-CI-Event", "X-GitHub-Event", "X-Gitlab-Event")
	result, run, err := a.webhooks.Deliver(request.Context(), request.PathValue("endpoint"), token, deliveryID, eventType, payload)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if strings.Contains(err.Error(), "token") {
			status = http.StatusUnauthorized
		}
		writeError(writer, status, "webhook_rejected", err.Error())
		return
	}
	response := map[string]any{"delivery": result.Delivery, "duplicate": !result.Created}
	if run != nil {
		response["run"] = run
	}
	writeJSON(writer, http.StatusAccepted, response)
}

func (a *API) handleProjectDeployments(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project")
	if request.Method == http.MethodGet {
		items, err := a.store.ListDeployments(request.Context(), projectID)
		if err != nil {
			a.writeStoreError(writer, err, "failed to list deployments")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	var payload struct{ RunID, Environment string }
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.store.CreateDeployment(request.Context(), store.CreateDeploymentParams{ProjectID: projectID, RunID: payload.RunID, Environment: payload.Environment, Status: store.StatusQueued})
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "deployment.created", "deployment", item.ID)
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleDeployment(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Status store.Status `json:"status"`
		Reason *string      `json:"reason"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.store.TransitionDeployment(request.Context(), request.PathValue("deployment"), payload.Status, payload.Reason)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "deployment.transitioned", "deployment", item.ID)
	writeJSON(writer, http.StatusOK, item)
}

func firstHeader(request *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(request.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}
