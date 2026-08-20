package httpapi

import (
	"context"
	"net/http"
	"strings"

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

func (a *API) handleCreateScheduleWeb(writer http.ResponseWriter, request *http.Request) {
	workflow, err := a.store.GetWorkflow(request.Context(), request.FormValue("workflowId"))
	if err != nil {
		a.renderAppSection(writer, request, "schedules", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	item, err := a.scheduler.Create(request.Context(), workflow.ProjectID, workflow.ID, request.FormValue("cron"), request.FormValue("ref"), request.FormValue("timezone"), true)
	if err != nil {
		a.renderAppSection(writer, request, "schedules", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "schedule.created", "schedule", item.ID)
	a.renderAppSectionState(writer, request, "schedules", "", "SCHEDULE ARMED", http.StatusOK)
}

func (a *API) handleToggleScheduleWeb(writer http.ResponseWriter, request *http.Request) {
	item, err := a.store.GetWorkflowSchedule(request.Context(), request.PathValue("schedule"))
	if err != nil {
		a.renderAppSection(writer, request, "schedules", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	ref := ""
	if item.Ref != nil {
		ref = *item.Ref
	}
	updated, err := a.scheduler.Update(request.Context(), item.ID, item.Cron, ref, item.Timezone, !item.Enabled)
	if err != nil {
		a.renderAppSection(writer, request, "schedules", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "schedule.toggled", "schedule", updated.ID)
	a.renderAppSectionState(writer, request, "schedules", "", "SCHEDULE UPDATED", http.StatusOK)
}

func (a *API) handleDeleteScheduleWeb(writer http.ResponseWriter, request *http.Request) {
	if err := a.scheduler.Delete(request.Context(), request.PathValue("schedule")); err != nil {
		a.renderAppSection(writer, request, "schedules", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "schedule.deleted", "schedule", request.PathValue("schedule"))
	a.renderAppSectionState(writer, request, "schedules", "", "SCHEDULE DELETED", http.StatusOK)
}

func (a *API) handleCreateWebhookWeb(writer http.ResponseWriter, request *http.Request) {
	workflow, err := a.store.GetWorkflow(request.Context(), request.FormValue("workflowId"))
	if err != nil {
		a.renderAppSection(writer, request, "settings", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	created, err := a.webhooks.Create(request.Context(), workflow.ProjectID, request.FormValue("name"), request.FormValue("provider"), workflow.ID, request.FormValue("ref"))
	if err != nil {
		a.renderAppSection(writer, request, "settings", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	a.recordExecutionAudit(request, "webhook.created", "webhook", created.Endpoint.ID)
	a.renderAppSectionState(writer, request, "settings", "", "WEBHOOK TOKEN (SHOWN ONCE): "+created.Token, http.StatusOK)
}

func (a *API) populateConfigurationPage(ctx context.Context, data *webui.PageData) error {
	workflowNames := make(map[string]string, len(data.Workflows))
	for _, workflow := range data.Workflows {
		workflowNames[workflow.ID] = workflow.Name
	}
	for _, project := range data.Projects {
		secrets, err := a.store.ListSecrets(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, item := range secrets {
			data.Secrets = append(data.Secrets, webui.SecretView{ID: item.ID, ProjectName: project.Name, Name: item.Name, UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z")})
		}
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
			data.Schedules = append(data.Schedules, webui.ScheduleView{ID: item.ID, ProjectName: project.Name, WorkflowName: workflowNames[item.WorkflowID], Cron: item.Cron, Ref: ref, Timezone: item.Timezone, NextRunAt: next, Enabled: item.Enabled})
		}
		deployments, err := a.store.ListDeployments(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, item := range deployments {
			data.Deployments = append(data.Deployments, webui.DeploymentView{ID: item.ID, RunID: item.RunID, ProjectName: project.Name, Environment: item.Environment, Status: strings.ToUpper(string(item.Status)), Dot: statusDot(item.Status), UpdatedAt: item.UpdatedAt.UTC().Format("2006-01-02 15:04:05Z")})
		}
		endpoints, err := a.store.ListWebhookEndpoints(ctx, project.ID)
		if err != nil {
			return err
		}
		for _, item := range endpoints {
			data.Webhooks = append(data.Webhooks, webui.WebhookView{ID: item.ID, ProjectName: project.Name, Name: item.Name, Provider: strings.ToUpper(item.Provider), URL: "/hooks/" + item.ID, Enabled: item.Enabled})
		}
	}
	return nil
}
