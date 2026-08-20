package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
)

func (a *API) handleProjectCommitTriggerWeb(writer http.ResponseWriter, request *http.Request) {
	enabled := strings.EqualFold(strings.TrimSpace(request.FormValue("enabled")), "true")
	policy, err := a.commitTriggers.Configure(request.Context(), request.PathValue("project"), request.FormValue("ref"), enabled)
	if err != nil {
		a.renderAppSection(writer, request, "projects", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "commit_trigger.updated", "project", policy.ProjectID)
	notice := "COMMIT WATCH DISABLED"
	if policy.Enabled {
		notice = "COMMIT WATCH ENABLED / BASELINE RECORDED"
	}
	a.renderAppSectionState(writer, request, "projects", "", notice, http.StatusOK)
}

func (a *API) handleProjectCommitTrigger(writer http.ResponseWriter, request *http.Request) {
	projectID := request.PathValue("project")
	if request.Method == http.MethodGet {
		policy, err := a.store.GetProjectCommitTrigger(request.Context(), projectID)
		if err != nil {
			var notFound *store.ErrNotFound
			if errors.As(err, &notFound) {
				writeError(writer, http.StatusNotFound, "commit_trigger_not_found", "commit trigger is not configured")
				return
			}
			writeError(writer, http.StatusInternalServerError, "store_failed", "failed to load commit trigger")
			return
		}
		writeJSON(writer, http.StatusOK, policy)
		return
	}
	var payload struct {
		Ref     string `json:"ref"`
		Enabled bool   `json:"enabled"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	policy, err := a.commitTriggers.Configure(request.Context(), projectID, payload.Ref, payload.Enabled)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_commit_trigger", err.Error())
		return
	}
	a.recordExecutionAudit(request, "commit_trigger.updated", "project", policy.ProjectID)
	writeJSON(writer, http.StatusOK, policy)
}
