package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/store"
)

type replayRunResponse struct {
	Run     store.Run        `json:"run"`
	Lineage store.RunLineage `json:"lineage"`
}

func (a *API) handleRunReplayOptions(writer http.ResponseWriter, request *http.Request) {
	result, err := a.store.EvaluateRunReplays(request.Context(), request.PathValue("run"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to evaluate run replay")
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (a *API) handleRunJobReplay(writer http.ResponseWriter, request *http.Request) {
	a.handleReplay(writer, request, store.EnqueueReplayParams{
		Kind: store.RunLineageJobReplay, SourceRunID: request.PathValue("run"), SourceJobID: request.PathValue("job"),
	})
}

func (a *API) handleRunStepReplay(writer http.ResponseWriter, request *http.Request) {
	a.handleReplay(writer, request, store.EnqueueReplayParams{
		Kind: store.RunLineageStepReplay, SourceRunID: request.PathValue("run"),
		SourceJobID: request.PathValue("job"), SourceStepID: request.PathValue("step"),
	})
}

func (a *API) handleReplay(writer http.ResponseWriter, request *http.Request, params store.EnqueueReplayParams) {
	var payload struct {
		IdempotencyKey    string `json:"idempotencyKey"`
		ConfirmSuccessful bool   `json:"confirmSuccessful"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	if payload.IdempotencyKey == "" {
		payload.IdempotencyKey = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if strings.TrimSpace(payload.IdempotencyKey) == "" {
		writeError(writer, http.StatusUnprocessableEntity, "idempotency_key_required", "idempotencyKey or Idempotency-Key is required")
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	params.Actor, params.IdempotencyKey, params.ConfirmSuccessful = principal.Subject, payload.IdempotencyKey, payload.ConfirmSuccessful
	run, err := a.execution.EnqueueRunReplay(request.Context(), params)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	lineage, err := a.store.GetRunLineage(request.Context(), run.ID)
	if err != nil {
		a.writeStoreError(writer, err, "failed to load replay provenance")
		return
	}
	a.recordExecutionAudit(request, string(params.Kind)+".queued", "run", run.ID)
	writeJSON(writer, http.StatusAccepted, replayRunResponse{Run: run, Lineage: lineage})
}

func (a *API) handleJobReplayWeb(writer http.ResponseWriter, request *http.Request) {
	a.handleReplayWeb(writer, request, store.EnqueueReplayParams{Kind: store.RunLineageJobReplay, SourceJobID: request.PathValue("job")})
}

func (a *API) handleStepReplayWeb(writer http.ResponseWriter, request *http.Request) {
	a.handleReplayWeb(writer, request, store.EnqueueReplayParams{Kind: store.RunLineageStepReplay, SourceStepID: request.PathValue("step")})
}

func (a *API) handleReplayWeb(writer http.ResponseWriter, request *http.Request, params store.EnqueueReplayParams) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	params.SourceRunID, params.SourceJobID = request.FormValue("sourceRunId"), request.FormValue("sourceJobId")
	params.Actor, params.IdempotencyKey = principal.Subject, request.FormValue("idempotencyKey")
	params.ConfirmSuccessful = request.FormValue("confirmSuccessful") == "true"
	run, err := a.execution.EnqueueRunReplay(request.Context(), params)
	if err != nil {
		a.renderRun(writer, request, request.FormValue("sourceRunId"), err.Error(), false)
		return
	}
	a.recordExecutionAudit(request, string(params.Kind)+".queued", "run", run.ID)
	a.redirectWeb(writer, request, "/app/runs/"+run.ID+"?notice=REPLAY+QUEUED")
}

func newReplayIdempotencyKey(kind store.RunLineageKind) (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return string(kind) + "-" + base64.RawURLEncoding.EncodeToString(value), nil
}
