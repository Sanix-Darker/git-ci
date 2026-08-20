package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/store"
)

func (a *API) handleDeploymentRollbackOptions(writer http.ResponseWriter, request *http.Request) {
	result, err := a.store.EvaluateDeploymentRollback(request.Context(), request.PathValue("deployment"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to evaluate rollback")
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (a *API) handleDeploymentRollback(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		TargetDeploymentID string `json:"targetDeploymentId"`
		IdempotencyKey     string `json:"idempotencyKey"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	if payload.IdempotencyKey == "" {
		payload.IdempotencyKey = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if strings.TrimSpace(payload.TargetDeploymentID) == "" {
		writeError(writer, http.StatusUnprocessableEntity, "target_deployment_required", "targetDeploymentId is required")
		return
	}
	if strings.TrimSpace(payload.IdempotencyKey) == "" {
		writeError(writer, http.StatusUnprocessableEntity, "idempotency_key_required", "idempotencyKey or Idempotency-Key is required")
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	run, err := a.execution.EnqueueDeploymentRollback(request.Context(), store.EnqueueRollbackParams{SourceDeploymentID: request.PathValue("deployment"), TargetDeploymentID: payload.TargetDeploymentID, Actor: principal.Subject, IdempotencyKey: payload.IdempotencyKey})
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "deployment.rollback_queued", "run", run.ID)
	writeJSON(writer, http.StatusAccepted, run)
}

func (a *API) handleDeploymentRollbackWeb(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	run, err := a.execution.EnqueueDeploymentRollback(request.Context(), store.EnqueueRollbackParams{SourceDeploymentID: request.PathValue("deployment"), TargetDeploymentID: request.FormValue("targetDeploymentId"), Actor: principal.Subject, IdempotencyKey: request.FormValue("idempotencyKey")})
	if err != nil {
		a.renderAppSection(writer, request, "deployments", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "deployment.rollback_queued", "run", run.ID)
	a.redirectWeb(writer, request, "/app/runs/"+run.ID)
}

func newRollbackIdempotencyKey() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "rollback-" + base64.RawURLEncoding.EncodeToString(value), nil
}
