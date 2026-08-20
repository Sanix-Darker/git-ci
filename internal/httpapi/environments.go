package httpapi

import (
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/store"
)

type environmentPayload struct {
	Name              string                           `json:"name"`
	DeploymentTier    store.DeploymentTier             `json:"deploymentTier"`
	Protected         bool                             `json:"protected"`
	RequiredApprovals int                              `json:"requiredApprovals"`
	WaitTimerSeconds  int                              `json:"waitTimerSeconds"`
	AllowedRefs       []string                         `json:"allowedRefs"`
	ConcurrencyMode   store.EnvironmentConcurrencyMode `json:"concurrencyMode"`
}

func (a *API) handleProjectEnvironments(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project")
	if _, err := a.store.GetProject(request.Context(), projectID); err != nil {
		a.writeStoreError(writer, err, "project not found")
		return
	}
	if request.Method == http.MethodGet {
		items, err := a.store.ListEnvironments(request.Context(), projectID)
		if err != nil {
			a.writeStoreError(writer, err, "failed to list environments")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	var payload environmentPayload
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.store.UpsertEnvironment(request.Context(), store.UpsertEnvironmentParams{
		ProjectID: projectID, Name: payload.Name, DeploymentTier: payload.DeploymentTier,
		Protected: payload.Protected, RequiredApprovals: payload.RequiredApprovals,
		WaitTimerSeconds: payload.WaitTimerSeconds, AllowedRefs: payload.AllowedRefs,
		ConcurrencyMode: payload.ConcurrencyMode,
	})
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "environment.upserted", "environment", item.ID)
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleEnvironment(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetEnvironmentByID(request.Context(), request.PathValue("environment"))
	if err != nil {
		a.writeStoreError(writer, err, "environment not found")
		return
	}
	if request.Method == http.MethodGet {
		writeJSON(writer, http.StatusOK, item)
		return
	}
	var payload struct {
		DeploymentTier    *store.DeploymentTier             `json:"deploymentTier"`
		Protected         *bool                             `json:"protected"`
		RequiredApprovals *int                              `json:"requiredApprovals"`
		WaitTimerSeconds  *int                              `json:"waitTimerSeconds"`
		AllowedRefs       *[]string                         `json:"allowedRefs"`
		ConcurrencyMode   *store.EnvironmentConcurrencyMode `json:"concurrencyMode"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	params := store.UpsertEnvironmentParams{
		ProjectID: item.ProjectID, Name: item.Name, DeploymentTier: item.DeploymentTier,
		Protected: item.Protected, RequiredApprovals: item.RequiredApprovals,
		WaitTimerSeconds: item.WaitTimerSeconds, AllowedRefs: item.AllowedRefs,
		ConcurrencyMode: item.ConcurrencyMode,
	}
	if payload.DeploymentTier != nil {
		params.DeploymentTier = *payload.DeploymentTier
	}
	if payload.Protected != nil {
		params.Protected = *payload.Protected
	}
	if payload.RequiredApprovals != nil {
		params.RequiredApprovals = *payload.RequiredApprovals
	}
	if payload.WaitTimerSeconds != nil {
		params.WaitTimerSeconds = *payload.WaitTimerSeconds
	}
	if payload.AllowedRefs != nil {
		params.AllowedRefs = *payload.AllowedRefs
	}
	if payload.ConcurrencyMode != nil {
		params.ConcurrencyMode = *payload.ConcurrencyMode
	}
	if payload.Protected != nil && !*payload.Protected {
		params.RequiredApprovals, params.WaitTimerSeconds, params.AllowedRefs = 0, 0, nil
	}
	item, err = a.store.UpsertEnvironment(request.Context(), params)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "environment.updated", "environment", item.ID)
	writeJSON(writer, http.StatusOK, item)
}

func (a *API) handleEnvironmentSecrets(writer http.ResponseWriter, request *http.Request) {
	environment, err := a.store.GetEnvironmentByID(request.Context(), request.PathValue("environment"))
	if err != nil {
		a.writeStoreError(writer, err, "environment not found")
		return
	}
	if request.Method == http.MethodGet {
		items, err := a.store.ListEnvironmentSecrets(request.Context(), environment.ID)
		if err != nil {
			a.writeStoreError(writer, err, "failed to list environment secrets")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
		return
	}
	var payload struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	item, err := a.secrets.UpsertEnvironment(request.Context(), environment.ID, payload.Name, payload.Value)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "environment_secret.upserted", "environment_secret", item.ID)
	writeJSON(writer, http.StatusCreated, item)
}

func (a *API) handleEnvironmentSecret(writer http.ResponseWriter, request *http.Request) {
	if err := a.secrets.DeleteEnvironment(request.Context(), request.PathValue("secret")); err != nil {
		a.writeStoreError(writer, err, "environment secret not found")
		return
	}
	a.recordExecutionAudit(request, "environment_secret.deleted", "environment_secret", request.PathValue("secret"))
	writer.WriteHeader(http.StatusNoContent)
}

func (a *API) handleApprovals(writer http.ResponseWriter, request *http.Request) {
	params := store.ListEnvironmentApprovalsParams{ProjectID: strings.TrimSpace(request.URL.Query().Get("projectId"))}
	if status := strings.TrimSpace(request.URL.Query().Get("status")); status != "" {
		params.Status = store.EnvironmentApprovalStatus(status)
	}
	items, err := a.store.ListEnvironmentApprovalRequests(request.Context(), params)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleApproval(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetEnvironmentApprovalRequest(request.Context(), request.PathValue("approval"))
	if err != nil {
		a.writeStoreError(writer, err, "approval not found")
		return
	}
	decisions, err := a.store.ListEnvironmentApprovalDecisions(request.Context(), item.ID)
	if err != nil {
		a.writeStoreError(writer, err, "failed to list approval decisions")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"approval": item, "decisions": decisions})
}

func (a *API) handleApprovalDecision(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Decision store.EnvironmentApprovalStatus `json:"decision"`
		Reason   string                          `json:"reason"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	item, err := a.store.DecideEnvironmentApproval(request.Context(), store.DecideEnvironmentApprovalParams{
		RequestID: request.PathValue("approval"), Decision: payload.Decision,
		Actor: principal.Subject, Reason: payload.Reason,
	})
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.execution.Notify()
	a.recordExecutionAudit(request, "approval."+string(payload.Decision), "environment_approval", item.ID)
	writeJSON(writer, http.StatusOK, item)
}

func (a *API) handleDeploymentDetail(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetDeployment(request.Context(), request.PathValue("deployment"))
	if err != nil {
		a.writeStoreError(writer, err, "deployment not found")
		return
	}
	writeJSON(writer, http.StatusOK, item)
}
