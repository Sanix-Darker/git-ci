package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/store"
)

func (a *API) handleManualJobPlay(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		IdempotencyKey string            `json:"idempotencyKey"`
		Variables      map[string]string `json:"variables"`
		Confirmed      bool              `json:"confirmed"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	if strings.TrimSpace(payload.IdempotencyKey) == "" {
		payload.IdempotencyKey = strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	}
	if strings.TrimSpace(payload.IdempotencyKey) == "" {
		writeError(writer, http.StatusUnprocessableEntity, "idempotency_key_required", "idempotencyKey or Idempotency-Key is required")
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	result, err := a.execution.PlayManualJob(request.Context(), store.PlayManualJobParams{
		RunID: request.PathValue("run"), JobID: request.PathValue("job"), Actor: principal.Subject,
		IdempotencyKey: payload.IdempotencyKey, Variables: payload.Variables, Confirmed: payload.Confirmed,
	})
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "job.played", "job", result.Job.ID)
	writeJSON(writer, http.StatusAccepted, result)
}

func (a *API) handleManualJobPlayWeb(writer http.ResponseWriter, request *http.Request) {
	variables, err := parseManualVariableLines(request.FormValue("variables"))
	if err != nil {
		a.renderRun(writer, request, request.FormValue("sourceRunId"), err.Error(), false)
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	result, err := a.execution.PlayManualJob(request.Context(), store.PlayManualJobParams{
		RunID: request.FormValue("sourceRunId"), JobID: request.PathValue("job"), Actor: principal.Subject,
		IdempotencyKey: request.FormValue("idempotencyKey"), Variables: variables,
		Confirmed: request.FormValue("confirmed") == "true",
	})
	if err != nil {
		a.renderRun(writer, request, request.FormValue("sourceRunId"), err.Error(), false)
		return
	}
	a.recordExecutionAudit(request, "job.played", "job", result.Job.ID)
	a.redirectWeb(writer, request, "/app/runs/"+result.Run.ID+"?notice=MANUAL+JOB+QUEUED")
}

func parseManualVariableLines(value string) (map[string]string, error) {
	variables := make(map[string]string)
	for index, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, variableValue, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, errors.New("manual variable line " + string(rune(index+1+'0')) + " must use KEY=VALUE")
		}
		if _, exists := variables[key]; exists {
			return nil, errors.New("manual variables must not contain duplicate keys")
		}
		variables[key] = variableValue
	}
	return variables, nil
}

func newManualPlayIdempotencyKey() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "manual-play-" + base64.RawURLEncoding.EncodeToString(value), nil
}
