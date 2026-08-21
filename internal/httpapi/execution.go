package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/sanix-darker/git-ci/internal/auth"
	execdomain "github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/store"
)

func (a *API) handleProjectWorkflows(writer http.ResponseWriter, request *http.Request) {
	items, err := a.store.ListWorkflows(request.Context(), request.PathValue("project"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to list workflows")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleSyncProjectWorkflows(writer http.ResponseWriter, request *http.Request) {
	items, err := a.execution.SyncProject(request.Context(), request.PathValue("project"))
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "workflow.synced", "project", request.PathValue("project"))
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleWorkflow(writer http.ResponseWriter, request *http.Request) {
	workflow, err := a.store.GetWorkflow(request.Context(), request.PathValue("workflow"))
	if err != nil {
		a.writeStoreError(writer, err, "workflow not found")
		return
	}
	writeJSON(writer, http.StatusOK, workflow)
}

func (a *API) handleEnqueueWorkflowRun(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Ref       string            `json:"ref"`
		CommitSHA string            `json:"commitSha"`
		Inputs    map[string]string `json:"inputs"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	run, err := a.execution.EnqueueWorkflowWithInputs(request.Context(), request.PathValue("workflow"), payload.Ref, payload.CommitSHA, payload.Inputs)
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.recordExecutionAudit(request, "run.queued", "run", run.ID)
	writeJSON(writer, http.StatusAccepted, run)
}

func (a *API) handleProjectRuns(writer http.ResponseWriter, request *http.Request) {
	items, err := a.store.ListRuns(request.Context(), request.PathValue("project"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to list runs")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleRun(writer http.ResponseWriter, request *http.Request) {
	graph, err := a.store.GetRunGraph(request.Context(), request.PathValue("run"))
	if err != nil {
		a.writeStoreError(writer, err, "run not found")
		return
	}
	writeJSON(writer, http.StatusOK, graph)
}

func (a *API) handleCancelRun(writer http.ResponseWriter, request *http.Request) {
	cancellation, err := a.store.RequestRunCancellation(request.Context(), request.PathValue("run"))
	if err != nil {
		a.writeStoreError(writer, err, err.Error())
		return
	}
	a.execution.Notify()
	a.recordExecutionAudit(request, "run.cancel_requested", "run", request.PathValue("run"))
	writeJSON(writer, http.StatusAccepted, cancellation)
}

func (a *API) handleRunLogs(writer http.ResponseWriter, request *http.Request) {
	graph, err := a.store.GetRunGraph(request.Context(), request.PathValue("run"))
	if err != nil {
		a.writeStoreError(writer, err, "run not found")
		return
	}
	lines := make([]store.LogLine, 0)
	sections := make([]store.StepLogSection, 0)
	for _, job := range graph.Jobs {
		for _, step := range job.Steps {
			stepLines, err := a.store.ListLogLines(request.Context(), step.ID)
			if err != nil {
				a.writeStoreError(writer, err, "failed to list logs")
				return
			}
			lines = append(lines, stepLines...)
			stepSections, err := a.store.ListStepLogSections(request.Context(), step.ID)
			if err != nil {
				a.writeStoreError(writer, err, "failed to list log sections")
				return
			}
			sections = append(sections, stepSections...)
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Sequence < lines[j].Sequence })
	sort.Slice(sections, func(i, j int) bool { return sections[i].StartSequence < sections[j].StartSequence })
	writeJSON(writer, http.StatusOK, map[string]any{"items": lines, "count": len(lines), "sections": sections, "sectionCount": len(sections)})
}

func (a *API) writeStoreError(writer http.ResponseWriter, err error, message string) {
	var notFound *store.ErrNotFound
	var conflict *store.ErrConflict
	var rollback *store.ErrRollbackEligibility
	var replay *store.ErrReplayEligibility
	var manualPlay *store.ErrManualJobPlay
	var releaseTransition *store.ErrReleaseTransition
	var runnerUnavailable *execdomain.ErrRunnerUnavailable
	switch {
	case errors.As(err, &notFound):
		writeError(writer, http.StatusNotFound, "not_found", message)
	case errors.As(err, &conflict):
		writeError(writer, http.StatusConflict, "conflict", message)
	case errors.As(err, &rollback):
		writeError(writer, http.StatusUnprocessableEntity, rollback.Code, rollback.Message)
	case errors.As(err, &replay):
		writeError(writer, http.StatusUnprocessableEntity, replay.Code, replay.Message)
	case errors.As(err, &manualPlay):
		writeError(writer, http.StatusUnprocessableEntity, manualPlay.Code, manualPlay.Message)
	case errors.As(err, &releaseTransition):
		writeError(writer, http.StatusUnprocessableEntity, releaseTransition.Code, releaseTransition.Message)
	case errors.As(err, &runnerUnavailable):
		writeError(writer, http.StatusConflict, "runner_unavailable", runnerUnavailable.Error())
	default:
		writeError(writer, http.StatusUnprocessableEntity, "execution_failed", message)
	}
}

func (a *API) recordExecutionAudit(request *http.Request, action, resourceType, resourceID string) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	_, _ = a.store.RecordAudit(request.Context(), store.AuditEvent{
		Action:       action,
		Actor:        principal.Subject,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     json.RawMessage(`{}`),
	})
}
