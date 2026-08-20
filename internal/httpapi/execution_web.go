package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	execdomain "github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func (a *API) handleSyncWorkflowsWeb(writer http.ResponseWriter, request *http.Request) {
	_, err := a.execution.SyncProject(request.Context(), request.PathValue("project"))
	if err != nil {
		a.renderAppSection(writer, request, "workflows", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "workflow.synced", "project", request.PathValue("project"))
	writer.Header().Set("HX-Trigger", "workflowsSynced")
	a.renderAppSection(writer, request, "workflows", "", http.StatusOK)
}

func (a *API) handleEnqueueRunWeb(writer http.ResponseWriter, request *http.Request) {
	run, err := a.execution.EnqueueWorkflow(request.Context(), request.PathValue("workflow"), request.FormValue("ref"), request.FormValue("commitSha"))
	if err != nil {
		a.renderAppSection(writer, request, "workflows", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "run.queued", "run", run.ID)
	a.redirectWeb(writer, request, "/app/runs/"+run.ID)
}

func (a *API) handleCancelRunWeb(writer http.ResponseWriter, request *http.Request) {
	if _, err := a.store.RequestRunCancellation(request.Context(), request.PathValue("run")); err != nil {
		a.renderRun(writer, request, request.PathValue("run"), err.Error(), false)
		return
	}
	a.execution.Notify()
	a.recordExecutionAudit(request, "run.cancel_requested", "run", request.PathValue("run"))
	if isHTMX(request) {
		a.renderRun(writer, request, request.PathValue("run"), "", true)
		return
	}
	http.Redirect(writer, request, "/app/runs/"+request.PathValue("run"), http.StatusSeeOther)
}

func (a *API) handleRunPageWeb(writer http.ResponseWriter, request *http.Request) {
	a.renderRun(writer, request, request.PathValue("run"), "", false)
}

func (a *API) handleRunPanelWeb(writer http.ResponseWriter, request *http.Request) {
	a.renderRun(writer, request, request.PathValue("run"), "", true)
}

func (a *API) renderRun(writer http.ResponseWriter, request *http.Request, runID, message string, panel bool) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	session, err := a.auth.CurrentSession(request)
	if err != nil {
		a.webUnauthorized(writer, request)
		return
	}
	definition := appPages["runs"]
	data := webui.PageData{
		Page: "runs", Title: definition.title, Kicker: definition.kicker,
		Description: definition.description, Actor: principal.Subject,
		CSRFToken: session.CSRFToken, Version: a.version, Error: message,
	}
	projects, err := a.store.ListProjects(request.Context())
	if err != nil {
		http.Error(writer, "failed to list projects", http.StatusInternalServerError)
		return
	}
	data.Projects = projects
	if err := a.populateExecutionPage(request.Context(), &data, runID); err != nil {
		var notFound *store.ErrNotFound
		if errors.As(err, &notFound) {
			http.NotFound(writer, request)
			return
		}
		http.Error(writer, "failed to load run", http.StatusInternalServerError)
		return
	}
	if panel {
		a.web.RenderRunPanel(writer, http.StatusOK, data)
		return
	}
	a.web.RenderApp(writer, http.StatusOK, data, false)
}

func (a *API) populateExecutionPage(ctx context.Context, data *webui.PageData, selectedRunID string) error {
	projectNames := make(map[string]string, len(data.Projects))
	workflowNames := make(map[string]string)
	for _, project := range data.Projects {
		projectNames[project.ID] = project.Name
		workflows, err := a.store.ListWorkflows(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, workflow := range workflows {
			var definition execdomain.Definition
			_ = json.Unmarshal(workflow.Definition, &definition)
			workflowNames[workflow.ID] = workflow.Name
			data.Workflows = append(data.Workflows, webui.WorkflowView{
				ID: workflow.ID, ProjectID: project.ID, ProjectName: project.Name,
				Name: workflow.Name, Key: workflow.Key, Provider: strings.ToUpper(string(definition.Provider)),
				File: definition.File, Revision: int(workflow.Revision), JobCount: len(definition.Jobs),
			})
		}
		runs, err := a.store.ListRuns(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			data.Runs = append(data.Runs, runView(run, project.Name, workflowNames))
		}
	}
	sort.Slice(data.Runs, func(i, j int) bool { return data.Runs[i].CreatedAt > data.Runs[j].CreatedAt })
	for _, run := range data.Runs {
		graph, err := a.store.GetRunGraph(ctx, run.ID)
		if err != nil {
			return err
		}
		for _, item := range graph.Jobs {
			data.Jobs = append(data.Jobs, webui.JobView{
				ID: item.Job.ID, RunID: run.ID, ProjectName: run.ProjectName,
				WorkflowName: run.WorkflowName, Key: stringValue(item.Job.Key), Name: item.Job.Name,
				Status: strings.ToUpper(string(item.Job.Status)), Dot: statusDot(item.Job.Status), StepCount: len(item.Steps),
			})
		}
	}
	if selectedRunID != "" {
		detail, err := a.runDetail(ctx, selectedRunID, projectNames, workflowNames)
		if err != nil {
			return err
		}
		data.SelectedRun = &detail
	}
	return nil
}

func (a *API) runDetail(ctx context.Context, runID string, projectNames, workflowNames map[string]string) (webui.RunDetailView, error) {
	graph, err := a.store.GetRunGraph(ctx, runID)
	if err != nil {
		return webui.RunDetailView{}, err
	}
	detail := webui.RunDetailView{
		Run:      runView(graph.Run, projectNames[graph.Run.ProjectID], workflowNames),
		Terminal: terminalStatus(graph.Run.Status),
	}
	for _, item := range graph.Jobs {
		job := webui.RunJobView{
			ID: item.Job.ID, Key: stringValue(item.Job.Key), Name: item.Job.Name,
			Status: strings.ToUpper(string(item.Job.Status)), Dot: statusDot(item.Job.Status),
			Runner: stringValue(item.Job.Runner), Dependencies: strings.Join(decodeDependencies(item.Job.DependencyKeys), ", "),
			AllowFailure: item.Job.AllowFailure,
		}
		for _, step := range item.Steps {
			view := webui.RunStepView{
				ID: step.ID, Name: step.Name, Status: strings.ToUpper(string(step.Status)),
				Dot: statusDot(step.Status), Command: stringValue(step.Command),
			}
			lines, err := a.store.ListLogLines(ctx, step.ID)
			if err != nil {
				return webui.RunDetailView{}, err
			}
			for _, line := range lines {
				view.Logs = append(view.Logs, webui.LogView{Sequence: int(line.Sequence), Stream: strings.ToUpper(string(line.Stream)), Message: line.Message})
			}
			job.Steps = append(job.Steps, view)
		}
		detail.Jobs = append(detail.Jobs, job)
	}
	return detail, nil
}

func runView(run store.Run, projectName string, workflowNames map[string]string) webui.RunView {
	workflowName := "Workflow"
	if run.WorkflowID != nil && workflowNames[*run.WorkflowID] != "" {
		workflowName = workflowNames[*run.WorkflowID]
	} else if run.WorkflowKey != nil {
		workflowName = *run.WorkflowKey
	}
	ref := strings.TrimPrefix(stringValue(run.Ref), "refs/heads/")
	return webui.RunView{
		ID: run.ID, ProjectName: projectName, WorkflowName: workflowName,
		WorkflowKey: stringValue(run.WorkflowKey), Status: strings.ToUpper(string(run.Status)), Dot: statusDot(run.Status),
		Ref: ref, CommitSHA: stringValue(run.CommitSHA), CreatedAt: run.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"),
		CanCancel: run.Status == store.StatusQueued || run.Status == store.StatusRunning,
	}
}

func statusDot(status store.Status) string {
	switch status {
	case store.StatusSucceeded:
		return "dot-green"
	case store.StatusFailed, store.StatusCancelled:
		return "dot-red"
	case store.StatusQueued, store.StatusRunning:
		return "dot-blue"
	default:
		return ""
	}
}

func terminalStatus(status store.Status) bool {
	return status == store.StatusSucceeded || status == store.StatusFailed || status == store.StatusCancelled || status == store.StatusSkipped
}

func decodeDependencies(value json.RawMessage) []string {
	var result []string
	_ = json.Unmarshal(value, &result)
	return result
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
