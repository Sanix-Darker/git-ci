package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/auth"
	execdomain "github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
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
	if err := request.ParseForm(); err != nil {
		a.renderAppSection(writer, request, "workflows", "invalid dispatch form", http.StatusBadRequest)
		return
	}
	inputs := make(map[string]string)
	for name, values := range request.PostForm {
		if !strings.HasPrefix(name, "input.") || len(values) == 0 {
			continue
		}
		inputs[strings.TrimPrefix(name, "input.")] = values[len(values)-1]
	}
	run, err := a.execution.EnqueueWorkflowWithInputs(request.Context(), request.PathValue("workflow"), request.FormValue("ref"), request.FormValue("commitSha"), inputs)
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

func (a *API) handleStepLogsWeb(writer http.ResponseWriter, request *http.Request) {
	runID, stepID := request.PathValue("run"), request.PathValue("step")
	graph, err := a.store.GetRunGraph(request.Context(), runID)
	if err != nil {
		var notFound *store.ErrNotFound
		if errors.As(err, &notFound) {
			http.NotFound(writer, request)
			return
		}
		http.Error(writer, "failed to load run logs", http.StatusInternalServerError)
		return
	}
	stepName, found := "", false
	for _, item := range graph.Jobs {
		for _, step := range item.Steps {
			if step.ID == stepID {
				stepName, found = step.Name, true
				break
			}
		}
	}
	if !found {
		http.NotFound(writer, request)
		return
	}
	lines, err := a.store.ListLogLines(request.Context(), stepID)
	if err != nil {
		http.Error(writer, "failed to load step logs", http.StatusInternalServerError)
		return
	}
	data := webui.StepLogView{RunID: runID, StepID: stepID, StepName: stepName, Terminal: terminalStatus(graph.Run.Status)}
	for _, line := range lines {
		data.Logs = append(data.Logs, webui.LogView{Sequence: int(line.Sequence), Stream: strings.ToUpper(string(line.Stream)), Message: line.Message})
	}
	a.web.RenderStepLogs(writer, http.StatusOK, data)
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
		Notice: strings.TrimSpace(request.URL.Query().Get("notice")),
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
		path := stringValue(project.CanonicalPath)
		health, healthDetail, dot := projectCheckoutHealth(path)
		projectView := webui.ProjectView{
			ID: project.ID, Name: project.Name, Slug: project.Slug, CanonicalPath: path,
			Health: health, HealthDetail: healthDetail, Dot: dot,
			CommitTrigger: webui.CommitTriggerView{Ref: project.DefaultBranch, Status: "OFF", Dot: "dot-blue"},
		}
		policy, policyErr := a.store.GetProjectCommitTrigger(ctx, project.ID)
		if policyErr == nil {
			projectView.CommitTrigger = commitTriggerView(policy)
		} else {
			var notFound *store.ErrNotFound
			if !errors.As(policyErr, &notFound) {
				return policyErr
			}
		}
		workflows, err := a.store.ListWorkflows(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, workflow := range workflows {
			var definition execdomain.Definition
			_ = json.Unmarshal(workflow.Definition, &definition)
			workflowNames[workflow.ID] = workflow.Name
			view := webui.WorkflowView{
				ID: workflow.ID, ProjectID: project.ID, ProjectName: project.Name,
				Name: workflow.Name, Key: workflow.Key, Provider: strings.ToUpper(string(definition.Provider)),
				File: definition.File, Revision: int(workflow.Revision), JobCount: len(definition.Jobs),
				DefaultRef: project.DefaultBranch,
			}
			populateWorkflowDefinitionView(&view, workflow.Definition)
			data.Workflows = append(data.Workflows, view)
			projectView.Workflows = append(projectView.Workflows, view)
		}
		projectView.WorkflowCount = len(projectView.Workflows)
		data.ProjectViews = append(data.ProjectViews, projectView)
		runs, err := a.store.ListRuns(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			data.Runs = append(data.Runs, runView(run, project.Name, workflowNames))
		}
	}
	sort.Slice(data.Runs, func(i, j int) bool { return data.Runs[i].CreatedAt > data.Runs[j].CreatedAt })
	if data.Page == "overview" || data.Page == "runs" {
		data.RunFilter = normalizeRunFilter(data.RunFilter)
		allRuns := data.Runs
		data.Runs = filterRunViews(allRuns, data.RunFilter, time.Now().UTC())
		data.Telemetry = buildRunTelemetry(allRuns, data.RunFilter, time.Now().UTC())
	}
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
		detail, err := a.runDetail(ctx, selectedRunID, projectNames, workflowNames, data.CSRFToken)
		if err != nil {
			return err
		}
		data.SelectedRun = &detail
	}
	return nil
}

func commitTriggerView(policy store.ProjectCommitTrigger) webui.CommitTriggerView {
	view := webui.CommitTriggerView{Ref: policy.Ref, Enabled: policy.Enabled, Status: "OFF", Dot: "dot-blue"}
	if policy.Enabled {
		view.Status, view.Dot = "WATCHING", "dot-green"
	}
	if policy.LastCommitSHA != nil {
		view.LastCommitSHA = *policy.LastCommitSHA
	}
	if policy.LastCheckedAt != nil {
		view.LastCheckedAt = policy.LastCheckedAt.UTC().Format("2006-01-02 15:04:05Z")
	}
	if policy.LastTriggeredAt != nil {
		view.LastTriggeredAt = policy.LastTriggeredAt.UTC().Format("2006-01-02 15:04:05Z")
	}
	if policy.LastError != nil {
		view.LastError, view.Status, view.Dot = *policy.LastError, "ERROR", "dot-red"
	}
	return view
}

type workflowDefinitionDocument struct {
	Triggers         []string               `json:"triggers"`
	TriggerPolicies  []triggerpolicy.Policy `json:"triggerPolicies"`
	Stages           []string               `json:"stages"`
	TopologicalOrder []string               `json:"topologicalOrder"`
	Jobs             json.RawMessage        `json:"jobs"`
}

type workflowDefinitionJobDocument struct {
	Key          string                           `json:"key"`
	Name         string                           `json:"name"`
	Stage        string                           `json:"stage"`
	RunnerHint   string                           `json:"runnerHint"`
	Needs        []string                         `json:"needs"`
	Requires     []string                         `json:"requires"`
	AllowFailure bool                             `json:"allowFailure"`
	Steps        []workflowDefinitionStepDocument `json:"steps"`
}

type workflowDefinitionStepDocument struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Action  string `json:"action"`
}

func populateWorkflowDefinitionView(view *webui.WorkflowView, raw []byte) {
	var definition workflowDefinitionDocument
	if json.Unmarshal(raw, &definition) != nil {
		return
	}
	view.Triggers = definition.Triggers
	view.Stages = definition.Stages
	manualInputs := make(map[string]struct{})
	for _, policy := range definition.TriggerPolicies {
		policyView := webui.WorkflowTriggerPolicyView{
			Event: policy.Event, Branches: policy.Branches, BranchesIgnore: policy.BranchesIgnore,
			Tags: policy.Tags, TagsIgnore: policy.TagsIgnore, Paths: policy.Paths,
			PathsIgnore: policy.PathsIgnore, Actions: policy.Actions, Schedules: policy.Schedules,
			Condition: policy.Condition, Evaluable: policy.Evaluable,
		}
		view.TriggerPolicies = append(view.TriggerPolicies, policyView)
		if policy.Event != "workflow_dispatch" && policy.Event != "manual" {
			continue
		}
		for _, input := range policy.Inputs {
			if _, exists := manualInputs[input.Name]; exists {
				continue
			}
			manualInputs[input.Name] = struct{}{}
			view.ManualInputs = append(view.ManualInputs, webui.WorkflowInputView{
				Name: input.Name, Description: input.Description, Type: input.Type,
				Required: input.Required, Default: input.Default, Options: input.Options,
			})
		}
	}

	jobsByKey := make(map[string]workflowDefinitionJobDocument)
	var jobs []workflowDefinitionJobDocument
	if err := json.Unmarshal(definition.Jobs, &jobs); err == nil {
		for _, job := range jobs {
			jobsByKey[job.Key] = job
		}
	} else {
		_ = json.Unmarshal(definition.Jobs, &jobsByKey)
	}
	order := append([]string(nil), definition.TopologicalOrder...)
	if len(order) == 0 {
		for key := range jobsByKey {
			order = append(order, key)
		}
		sort.Strings(order)
	}
	runJobs := make([]webui.RunJobView, 0, len(order))
	for _, key := range order {
		job, ok := jobsByKey[key]
		if !ok {
			continue
		}
		dependencies := append([]string(nil), job.Needs...)
		dependencies = append(dependencies, job.Requires...)
		workflowJob := webui.WorkflowJobView{
			Key: key, Name: job.Name, Stage: job.Stage, Runner: job.RunnerHint,
			Dependencies: strings.Join(dependencies, ", "), AllowFailure: job.AllowFailure,
		}
		if workflowJob.Name == "" {
			workflowJob.Name = key
		}
		for _, step := range job.Steps {
			workflowJob.Steps = append(workflowJob.Steps, webui.WorkflowStepView{
				Name: step.Name, Command: step.Command, Action: step.Action,
			})
		}
		view.Jobs = append(view.Jobs, workflowJob)
		runJobs = append(runJobs, webui.RunJobView{
			Key: key, Name: workflowJob.Name, Status: "READY", Dot: "dot-green",
			Runner: workflowJob.Runner, Dependencies: workflowJob.Dependencies,
			DependencyKeys: dependencies, AllowFailure: workflowJob.AllowFailure,
		})
	}
	view.GraphRows = buildGraphRows(runJobs)
}

func projectCheckoutHealth(path string) (string, string, string) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "PATH MISSING", "The registered checkout is not available on this host.", "dot-red"
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return "NOT A GIT CHECKOUT", "Discovery can read files, but commit-pinned execution requires Git metadata.", "dot-red"
	}
	return "READY", "Git checkout available for discovery and commit-pinned runs.", "dot-green"
}

func (a *API) runDetail(ctx context.Context, runID string, projectNames, workflowNames map[string]string, csrfToken string) (webui.RunDetailView, error) {
	graph, err := a.store.GetRunGraph(ctx, runID)
	if err != nil {
		return webui.RunDetailView{}, err
	}
	detail := webui.RunDetailView{
		Run:      runView(graph.Run, projectNames[graph.Run.ProjectID], workflowNames),
		Terminal: terminalStatus(graph.Run.Status),
	}
	lineage, lineageErr := a.store.GetRunLineage(ctx, runID)
	if lineageErr == nil {
		detail.Lineage = &webui.RunLineageView{
			Kind:        strings.ToUpper(strings.ReplaceAll(string(lineage.Kind), "_", " ")),
			SourceRunID: lineage.SourceRunID, SourceJobID: stringValue(lineage.SourceJobID),
			SourceStepID: stringValue(lineage.SourceStepID), Actor: lineage.Actor,
			CreatedAt: lineage.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"),
		}
	} else {
		var notFound *store.ErrNotFound
		if !errors.As(lineageErr, &notFound) {
			return webui.RunDetailView{}, lineageErr
		}
	}
	for _, item := range graph.Jobs {
		jobEligibility, err := a.store.EvaluateJobReplay(ctx, item.Job.ID)
		if err != nil {
			return webui.RunDetailView{}, err
		}
		jobReplay, err := replayControl(store.RunLineageJobReplay, item.Job.ID, runID, csrfToken, jobEligibility)
		if err != nil {
			return webui.RunDetailView{}, err
		}
		job := webui.RunJobView{
			ID: item.Job.ID, Key: stringValue(item.Job.Key), Name: item.Job.Name,
			Status: strings.ToUpper(string(item.Job.Status)), Dot: statusDot(item.Job.Status),
			Runner: stringValue(item.Job.Runner), Dependencies: strings.Join(decodeDependencies(item.Job.DependencyKeys), ", "),
			AllowFailure: item.Job.AllowFailure, Replay: jobReplay,
		}
		for _, step := range item.Steps {
			stepEligibility, err := a.store.EvaluateStepReplay(ctx, step.ID)
			if err != nil {
				return webui.RunDetailView{}, err
			}
			stepReplay, err := replayControl(store.RunLineageStepReplay, step.ID, runID, csrfToken, stepEligibility)
			if err != nil {
				return webui.RunDetailView{}, err
			}
			view := webui.RunStepView{
				ID: step.ID, RunID: runID, Name: step.Name, Status: strings.ToUpper(string(step.Status)),
				Dot: statusDot(step.Status), Command: stringValue(step.Command), Terminal: detail.Terminal, Replay: stepReplay,
			}
			job.Steps = append(job.Steps, view)
		}
		job.DependencyKeys = decodeDependencies(item.Job.DependencyKeys)
		detail.Jobs = append(detail.Jobs, job)
	}
	detail.GraphRows = buildGraphRows(detail.Jobs)
	return detail, nil
}

func replayControl(kind store.RunLineageKind, sourceID, runID, csrfToken string, eligibility store.ReplayEligibility) (webui.ReplayControlView, error) {
	control := webui.ReplayControlView{
		CSRFToken: csrfToken, SourceRunID: runID, SourceJobID: eligibility.SourceJobID,
		Enabled: eligibility.Eligible, Hint: eligibility.Message, CommitSHA: eligibility.CommitSHA,
		RequiresConfirmation: eligibility.RequiresConfirmation,
	}
	if kind == store.RunLineageJobReplay {
		control.Action, control.Label, control.Mode = "/app/jobs/"+sourceID+"/replay", "REPLAY JOB", "CLEAN COMMIT WORKSPACE"
		control.Consequence = fmt.Sprintf("%d PREREQUISITES REPLAYED", eligibility.DependencyCount)
	} else {
		control.Action, control.Label, control.Mode = "/app/steps/"+sourceID+"/replay", "REPLAY STEP", "CLEAN WORKSPACE / ONE STEP"
		control.Consequence = "ONLY THIS SHELL STEP RUNS"
	}
	if eligibility.DeploymentGate {
		control.Consequence += " / DEPLOYMENT APPROVAL REQUIRED"
	} else {
		control.Consequence += " / NO DEPLOYMENT GATE"
	}
	if eligibility.Eligible {
		control.Hint = control.Mode + " / " + control.Consequence
	}
	if !eligibility.Eligible {
		return control, nil
	}
	key, err := newReplayIdempotencyKey(kind)
	if err != nil {
		return webui.ReplayControlView{}, err
	}
	control.IdempotencyKey = key
	return control, nil
}

func runView(run store.Run, projectName string, workflowNames map[string]string) webui.RunView {
	workflowName := "Workflow"
	if run.WorkflowID != nil && workflowNames[*run.WorkflowID] != "" {
		workflowName = workflowNames[*run.WorkflowID]
	} else if run.WorkflowKey != nil {
		workflowName = *run.WorkflowKey
	}
	ref := strings.TrimPrefix(stringValue(run.Ref), "refs/heads/")
	durationSeconds := int64(0)
	if run.StartedAt != nil {
		end := run.UpdatedAt
		if run.FinishedAt != nil {
			end = *run.FinishedAt
		}
		if end.After(*run.StartedAt) {
			durationSeconds = int64(end.Sub(*run.StartedAt).Seconds())
		}
	}
	return webui.RunView{
		ID: run.ID, ProjectName: projectName, WorkflowName: workflowName,
		WorkflowKey: stringValue(run.WorkflowKey), Status: strings.ToUpper(string(run.Status)), Dot: statusDot(run.Status),
		Ref: ref, CommitSHA: stringValue(run.CommitSHA), CreatedAt: run.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"),
		CanCancel:   run.Status == store.StatusQueued || run.Status == store.StatusRunning,
		CreatedUnix: run.CreatedAt.Unix(), DurationSeconds: durationSeconds, DurationLabel: formatRunDuration(durationSeconds),
	}
}

func runFilterFromRequest(request *http.Request) webui.RunFilterView {
	return normalizeRunFilter(webui.RunFilterView{
		Range:   strings.ToLower(strings.TrimSpace(request.URL.Query().Get("range"))),
		Status:  strings.ToLower(strings.TrimSpace(request.URL.Query().Get("status"))),
		Project: strings.TrimSpace(request.URL.Query().Get("project")),
	})
}

func normalizeRunFilter(filter webui.RunFilterView) webui.RunFilterView {
	switch filter.Range {
	case "1h", "6h", "24h", "7d", "30d", "all":
	default:
		filter.Range = "24h"
	}
	switch filter.Status {
	case "", "queued", "running", "succeeded", "failed", "cancelled", "skipped":
	default:
		filter.Status = ""
	}
	return filter
}

func filterRunViews(runs []webui.RunView, filter webui.RunFilterView, now time.Time) []webui.RunView {
	filter = normalizeRunFilter(filter)
	cutoff := time.Time{}
	switch filter.Range {
	case "1h":
		cutoff = now.Add(-time.Hour)
	case "6h":
		cutoff = now.Add(-6 * time.Hour)
	case "24h":
		cutoff = now.Add(-24 * time.Hour)
	case "7d":
		cutoff = now.Add(-7 * 24 * time.Hour)
	case "30d":
		cutoff = now.Add(-30 * 24 * time.Hour)
	}
	filtered := make([]webui.RunView, 0, len(runs))
	for _, run := range runs {
		if !cutoff.IsZero() && time.Unix(run.CreatedUnix, 0).Before(cutoff) {
			continue
		}
		if filter.Status != "" && !strings.EqualFold(run.Status, filter.Status) {
			continue
		}
		if filter.Project != "" && run.ProjectName != filter.Project && run.ID != filter.Project {
			continue
		}
		filtered = append(filtered, run)
	}
	return filtered
}

func buildRunTelemetry(runs []webui.RunView, filter webui.RunFilterView, now time.Time) webui.RunTelemetryView {
	filtered := filterRunViews(runs, filter, now)
	telemetry := webui.RunTelemetryView{Window: strings.ToUpper(normalizeRunFilter(filter).Range), Total: len(filtered), PassRate: "--"}
	for _, run := range filtered {
		switch strings.ToLower(run.Status) {
		case "succeeded":
			telemetry.Succeeded++
		case "failed":
			telemetry.Failed++
		case "queued", "running":
			telemetry.Active++
		}
	}
	decided := telemetry.Succeeded + telemetry.Failed
	if decided > 0 {
		telemetry.PassRate = fmt.Sprintf("%d%%", telemetry.Succeeded*100/decided)
	}
	telemetry.Volume = volumeHistogram(filtered, normalizeRunFilter(filter).Range, now)
	telemetry.Duration = durationHistogram(filtered)
	return telemetry
}

func volumeHistogram(runs []webui.RunView, window string, now time.Time) []webui.HistogramBarView {
	span := 24 * time.Hour
	switch window {
	case "1h":
		span = time.Hour
	case "6h":
		span = 6 * time.Hour
	case "7d":
		span = 7 * 24 * time.Hour
	case "30d":
		span = 30 * 24 * time.Hour
	case "all":
		span = 24 * time.Hour
		for _, run := range runs {
			age := now.Sub(time.Unix(run.CreatedUnix, 0))
			if age > span {
				span = age
			}
		}
	}
	const bucketCount = 12
	width := span / bucketCount
	start := now.Add(-span)
	bars := make([]webui.HistogramBarView, bucketCount)
	for index := range bars {
		point := start.Add(time.Duration(index+1) * width)
		label := point.Format("15:04")
		if span > 48*time.Hour {
			label = point.Format("02 Jan")
		}
		bars[index].Label = label
	}
	for _, run := range runs {
		index := int(time.Unix(run.CreatedUnix, 0).Sub(start) / width)
		if index < 0 {
			continue
		}
		if index >= bucketCount {
			index = bucketCount - 1
		}
		bars[index].Count++
	}
	return scaleHistogram(bars)
}

func durationHistogram(runs []webui.RunView) []webui.HistogramBarView {
	bars := []webui.HistogramBarView{{Label: "<10S"}, {Label: "<30S"}, {Label: "<1M"}, {Label: "<5M"}, {Label: "5M+"}}
	for _, run := range runs {
		if run.DurationSeconds <= 0 {
			continue
		}
		index := 4
		switch {
		case run.DurationSeconds < 10:
			index = 0
		case run.DurationSeconds < 30:
			index = 1
		case run.DurationSeconds < 60:
			index = 2
		case run.DurationSeconds < 300:
			index = 3
		}
		bars[index].Count++
	}
	return scaleHistogram(bars)
}

func scaleHistogram(bars []webui.HistogramBarView) []webui.HistogramBarView {
	maximum := 0
	for _, bar := range bars {
		if bar.Count > maximum {
			maximum = bar.Count
		}
	}
	if maximum == 0 {
		return bars
	}
	for index := range bars {
		if bars[index].Count > 0 {
			bars[index].Level = 1 + bars[index].Count*9/maximum
		}
	}
	return bars
}

func buildGraphRows(jobs []webui.RunJobView) []webui.RunGraphRowView {
	byKey := make(map[string]webui.RunJobView, len(jobs))
	for _, job := range jobs {
		byKey[job.Key] = job
	}
	levels, visiting := make(map[string]int, len(jobs)), make(map[string]bool, len(jobs))
	var levelOf func(string) int
	levelOf = func(key string) int {
		if level, ok := levels[key]; ok {
			return level
		}
		if visiting[key] {
			return 0
		}
		visiting[key] = true
		level := 0
		for _, dependency := range byKey[key].DependencyKeys {
			if _, ok := byKey[dependency]; ok {
				candidate := levelOf(dependency) + 1
				if candidate > level {
					level = candidate
				}
			}
		}
		visiting[key] = false
		levels[key] = level
		return level
	}
	maxLevel := 0
	for _, job := range jobs {
		if level := levelOf(job.Key); level > maxLevel {
			maxLevel = level
		}
	}
	rows := make([]webui.RunGraphRowView, maxLevel+1)
	for level := range rows {
		rows[level].Level = level
	}
	for _, job := range jobs {
		level := levelOf(job.Key)
		rows[level].Jobs = append(rows[level].Jobs, job)
	}
	return rows
}

func formatRunDuration(seconds int64) string {
	if seconds <= 0 {
		return "--"
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", seconds/3600, seconds%3600/60)
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
