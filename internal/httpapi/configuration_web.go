package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

func (a *API) handleUpsertSecretWeb(writer http.ResponseWriter, request *http.Request) {
	item, err := a.secrets.Upsert(request.Context(), request.FormValue("projectId"), request.FormValue("name"), request.FormValue("value"))
	if err != nil {
		a.renderAppSection(writer, request, "secrets", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "secret.upserted", "secret", item.ID)
	a.renderAppSectionState(writer, request, "secrets", "", "SECRET STORED WITH AES-256-GCM", http.StatusOK)
}

func (a *API) handleDeleteSecretWeb(writer http.ResponseWriter, request *http.Request) {
	if err := a.secrets.Delete(request.Context(), request.PathValue("secret")); err != nil {
		a.renderAppSection(writer, request, "secrets", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "secret.deleted", "secret", request.PathValue("secret"))
	a.renderAppSectionState(writer, request, "secrets", "", "SECRET DELETED", http.StatusOK)
}

func (a *API) handleUpsertEnvironmentWeb(writer http.ResponseWriter, request *http.Request) {
	requiredApprovals, approvalsErr := strconv.Atoi(request.FormValue("requiredApprovals"))
	waitSeconds, waitErr := strconv.Atoi(request.FormValue("waitTimerSeconds"))
	if approvalsErr != nil || waitErr != nil {
		a.renderAppSection(writer, request, "deployments", "Approval and wait values must be integers.", http.StatusUnprocessableEntity)
		return
	}
	protected := request.FormValue("protected") == "on"
	refs := strings.FieldsFunc(request.FormValue("allowedRefs"), func(value rune) bool { return value == ',' || value == '\n' || value == '\r' })
	if !protected {
		requiredApprovals, waitSeconds, refs = 0, 0, nil
	}
	item, err := a.store.UpsertEnvironment(request.Context(), store.UpsertEnvironmentParams{
		ProjectID: request.FormValue("projectId"), Name: request.FormValue("name"),
		DeploymentTier: store.DeploymentTier(request.FormValue("deploymentTier")), Protected: protected,
		RequiredApprovals: requiredApprovals, WaitTimerSeconds: waitSeconds, AllowedRefs: refs,
		ConcurrencyMode: store.EnvironmentConcurrencyMode(request.FormValue("concurrencyMode")),
	})
	if err != nil {
		a.renderAppSection(writer, request, "deployments", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "environment.upserted", "environment", item.ID)
	a.renderAppSectionState(writer, request, "deployments", "", "ENVIRONMENT POLICY STORED", http.StatusOK)
}

func (a *API) handleUpsertEnvironmentSecretWeb(writer http.ResponseWriter, request *http.Request) {
	item, err := a.secrets.UpsertEnvironment(request.Context(), request.FormValue("environmentId"), request.FormValue("name"), request.FormValue("value"))
	if err != nil {
		a.renderAppSection(writer, request, "deployments", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "environment_secret.upserted", "environment_secret", item.ID)
	a.renderAppSectionState(writer, request, "deployments", "", "ENVIRONMENT SECRET STORED WITH AES-256-GCM", http.StatusOK)
}

func (a *API) handleDeleteEnvironmentSecretWeb(writer http.ResponseWriter, request *http.Request) {
	if err := a.secrets.DeleteEnvironment(request.Context(), request.PathValue("secret")); err != nil {
		a.renderAppSection(writer, request, "deployments", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "environment_secret.deleted", "environment_secret", request.PathValue("secret"))
	a.renderAppSectionState(writer, request, "deployments", "", "ENVIRONMENT SECRET DELETED", http.StatusOK)
}

func (a *API) handleApprovalDecisionWeb(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	decision := store.EnvironmentApprovalStatus(request.FormValue("decision"))
	item, err := a.store.DecideEnvironmentApproval(request.Context(), store.DecideEnvironmentApprovalParams{
		RequestID: request.PathValue("approval"), Decision: decision,
		Actor: principal.Subject, Reason: request.FormValue("reason"),
	})
	if err != nil {
		a.renderAppSection(writer, request, "deployments", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.execution.Notify()
	a.recordExecutionAudit(request, "approval."+string(decision), "environment_approval", item.ID)
	a.renderAppSectionState(writer, request, "deployments", "", "DEPLOYMENT "+strings.ToUpper(string(decision)), http.StatusOK)
}

func (a *API) handleCreateScheduleWeb(writer http.ResponseWriter, request *http.Request) {
	workflow, err := a.store.GetWorkflow(request.Context(), request.FormValue("workflowId"))
	if err != nil {
		a.renderAutomationWebState(writer, request, projectWorkspaceReturn(request), "schedules", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	if !a.requireAutomationProject(writer, request, workflow.ProjectID) {
		return
	}
	item, err := a.scheduler.Create(request.Context(), workflow.ProjectID, workflow.ID, request.FormValue("cron"), request.FormValue("ref"), request.FormValue("timezone"), true)
	if err != nil {
		a.renderAutomationWebState(writer, request, workflow.ProjectID, "schedules", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "schedule.created", "schedule", item.ID)
	a.renderAutomationWebState(writer, request, workflow.ProjectID, "schedules", "", "SCHEDULE ARMED", http.StatusOK)
}

func (a *API) handleToggleScheduleWeb(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetWorkflowSchedule(request.Context(), request.PathValue("schedule"))
	if err != nil {
		a.renderAutomationWebState(writer, request, projectWorkspaceReturn(request), "schedules", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	if !a.requireAutomationProject(writer, request, item.ProjectID) {
		return
	}
	ref := ""
	if item.Ref != nil {
		ref = *item.Ref
	}
	updated, err := a.scheduler.Update(request.Context(), item.ID, item.Cron, ref, item.Timezone, !item.Enabled)
	if err != nil {
		a.renderAutomationWebState(writer, request, item.ProjectID, "schedules", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "schedule.toggled", "schedule", updated.ID)
	a.renderAutomationWebState(writer, request, item.ProjectID, "schedules", "", "SCHEDULE UPDATED", http.StatusOK)
}

func (a *API) handleDeleteScheduleWeb(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetWorkflowSchedule(request.Context(), request.PathValue("schedule"))
	if err != nil {
		a.renderAutomationWebState(writer, request, projectWorkspaceReturn(request), "schedules", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	if !a.requireAutomationProject(writer, request, item.ProjectID) {
		return
	}
	if err := a.scheduler.Delete(request.Context(), item.ID); err != nil {
		a.renderAutomationWebState(writer, request, item.ProjectID, "schedules", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "schedule.deleted", "schedule", item.ID)
	a.renderAutomationWebState(writer, request, item.ProjectID, "schedules", "", "SCHEDULE DELETED", http.StatusOK)
}

func (a *API) handleCreateWebhookWeb(writer http.ResponseWriter, request *http.Request) {
	workflow, err := a.store.GetWorkflow(request.Context(), request.FormValue("workflowId"))
	if err != nil {
		a.renderAutomationWebState(writer, request, projectWorkspaceReturn(request), "settings", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	if !a.requireAutomationProject(writer, request, workflow.ProjectID) {
		return
	}
	created, err := a.webhooks.Create(request.Context(), workflow.ProjectID, request.FormValue("name"), request.FormValue("provider"), workflow.ID, request.FormValue("ref"))
	if err != nil {
		a.renderAutomationWebState(writer, request, workflow.ProjectID, "settings", err.Error(), "", http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "webhook.created", "webhook", created.Endpoint.ID)
	a.renderAutomationWebState(writer, request, workflow.ProjectID, "settings", "", "WEBHOOK TOKEN (SHOWN ONCE): "+created.Token, http.StatusOK)
}

func (a *API) requireAutomationProject(writer http.ResponseWriter, request *http.Request, actualProjectID string) bool {
	requestedProjectID := projectWorkspaceReturn(request)
	if requestedProjectID == "" || requestedProjectID == actualProjectID {
		return true
	}
	a.renderAutomationWebState(writer, request, requestedProjectID, "projects", "The selected workflow does not belong to this project.", "", http.StatusUnprocessableEntity)
	return false
}

func (a *API) renderAutomationWebState(writer http.ResponseWriter, request *http.Request, projectID, section, message, notice string, status int) {
	if projectID != "" && projectWorkspaceReturn(request) == projectID {
		a.renderProjectWorkspace(writer, request, projectID, message, notice, status)
		return
	}
	a.renderAppSectionState(writer, request, section, message, notice, status)
}

func workflowNamesByID(workflows []webui.WorkflowView) map[string]string {
	result := make(map[string]string, len(workflows))
	for _, workflow := range workflows {
		result[workflow.ID] = workflow.Name
	}
	return result
}

func (a *API) populateProjectAutomationPage(ctx context.Context, data *webui.PageData, project store.Project, workflowNames map[string]string) error {
	schedules, err := a.store.ListWorkflowSchedules(ctx, project.ID)
	if err != nil {
		return err
	}
	for _, item := range schedules {
		ref, next := "", "DISABLED"
		if item.Ref != nil {
			ref = *item.Ref
		}
		if item.NextRunAt != nil {
			next = item.NextRunAt.UTC().Format("2006-01-02 15:04:05Z")
		}
		workflowName := workflowNames[item.WorkflowID]
		if workflowName == "" {
			workflowName = item.WorkflowID
		}
		data.Schedules = append(data.Schedules, webui.ScheduleView{ID: item.ID, ProjectName: project.Name, WorkflowName: workflowName, Cron: item.Cron, Ref: ref, Timezone: item.Timezone, NextRunAt: next, Enabled: item.Enabled})
	}
	endpoints, err := a.store.ListWebhookEndpoints(ctx, project.ID)
	if err != nil {
		return err
	}
	for _, item := range endpoints {
		data.Webhooks = append(data.Webhooks, webui.WebhookView{ID: item.ID, ProjectName: project.Name, Name: item.Name, Provider: strings.ToUpper(item.Provider), URL: "/hooks/" + item.ID, Enabled: item.Enabled})
	}
	return nil
}

func (a *API) populateConfigurationPage(ctx context.Context, data *webui.PageData) error {
	workflowNames := workflowNamesByID(data.Workflows)
	for _, project := range data.Projects {
		secrets, err := a.store.ListSecrets(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, item := range secrets {
			data.Secrets = append(data.Secrets, webui.SecretView{ID: item.ID, ProjectName: project.Name, Name: item.Name, UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z")})
		}
		environments, err := a.store.ListEnvironments(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, item := range environments {
			environmentSecrets, err := a.store.ListEnvironmentSecrets(ctx, item.ID)
			if err != nil {
				return err
			}
			data.Environments = append(data.Environments, webui.EnvironmentView{
				ID: item.ID, ProjectID: project.ID, ProjectName: project.Name, Name: item.Name,
				DeploymentTier: strings.ToUpper(string(item.DeploymentTier)), Protected: item.Protected,
				RequiredApprovals: item.RequiredApprovals, WaitTimerSeconds: item.WaitTimerSeconds,
				AllowedRefs: strings.Join(item.AllowedRefs, ", "), ConcurrencyMode: strings.ToUpper(string(item.ConcurrencyMode)),
				SecretCount: len(environmentSecrets), UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z"),
			})
			for _, secret := range environmentSecrets {
				data.EnvironmentSecrets = append(data.EnvironmentSecrets, webui.EnvironmentSecretView{
					ID: secret.ID, ProjectName: project.Name, EnvironmentName: item.Name,
					Name: secret.Name, UpdatedAt: secret.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z"),
				})
			}
		}
		if err := a.populateProjectAutomationPage(ctx, data, project, workflowNames); err != nil {
			return err
		}
		deployments, err := a.store.ListDeployments(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, item := range deployments {
			jobID, jobName := "", "LEGACY / MANUAL"
			if item.JobID != nil {
				jobID = *item.JobID
				for _, job := range data.Jobs {
					if job.ID == jobID {
						jobName = job.Name
						break
					}
				}
			}
			terminal := terminalStatus(item.Status)
			view := webui.DeploymentView{ID: item.ID, RunID: item.RunID, JobID: jobID, JobName: jobName, ProjectName: project.Name, Environment: item.Environment, DeploymentTier: strings.ToUpper(string(item.DeploymentTier)), Status: strings.ToUpper(string(item.Status)), Dot: statusDot(item.Status), UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z"), Terminal: terminal, CSRFToken: data.CSRFToken}
			eligibility, err := a.store.EvaluateDeploymentRollback(ctx, item.ID)
			if err != nil {
				return err
			}
			view.RollbackHint = eligibility.Message
			if eligibility.Eligible {
				view.RollbackKey, err = newRollbackIdempotencyKey()
				if err != nil {
					return err
				}
				view.CanRollback = true
				for _, target := range eligibility.Targets {
					view.RollbackTargets = append(view.RollbackTargets, webui.RollbackTargetView{ID: target.DeploymentID, Ref: target.Ref, CommitSHA: target.CommitSHA, CreatedAt: target.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")})
				}
			}
			data.Deployments = append(data.Deployments, view)
			if !terminal {
				data.ActiveDeployments = true
			}
		}
	}
	approvals, err := a.store.ListEnvironmentApprovalRequests(ctx, store.ListEnvironmentApprovalsParams{Status: store.EnvironmentApprovalPending})
	if err != nil {
		return err
	}
	for _, item := range approvals {
		data.Approvals = append(data.Approvals, webui.ApprovalView{
			ID: item.ID, RunID: item.RunID, JobID: item.JobID, ProjectName: item.ProjectName,
			EnvironmentName: item.EnvironmentName, DeploymentTier: strings.ToUpper(string(item.DeploymentTier)),
			JobName: item.JobName, Ref: stringValue(item.Ref), CommitSHA: stringValue(item.CommitSHA),
			RequestedAt: item.RequestedAt.UTC().Format("2006-01-02 15:04:05Z"), CSRFToken: data.CSRFToken,
		})
	}
	return nil
}
