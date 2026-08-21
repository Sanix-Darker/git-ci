package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webui"
)

type pageDefinition struct {
	title       string
	kicker      string
	description string
}

var appPages = map[string]pageDefinition{
	"overview": {
		title:       "Dashboard",
		kicker:      "Control plane",
		description: "Projects, service health, and the latest execution surface.",
	},
	"projects": {
		title:       "Projects",
		kicker:      "Source registry",
		description: "Select Git checkouts from approved VPS roots and register them with git-ci.",
	},
	"workflows": {
		title:       "Workflows",
		kicker:      "Pipeline definitions",
		description: "Parsed GitHub Actions and GitLab CI workflow definitions.",
	},
	"runs": {
		title:       "Runs",
		kicker:      "Execution history",
		description: "Queued, active, completed, and cancelled pipeline runs.",
	},
	"jobs": {
		title:       "Jobs",
		kicker:      "Execution units",
		description: "Stage dependencies, step status, timing, logs, and retry controls.",
	},
	"runners": {
		title:       "Runners",
		kicker:      "Execution fleet",
		description: "Local execution capacity, scheduler mode, labels, runtime support, and availability.",
	},
	"secrets": {
		title:       "Secrets",
		kicker:      "Protected values",
		description: "Encrypted values, project scopes, environment scopes, and access audit.",
	},
	"schedules": {
		title:       "Schedules",
		kicker:      "Cron triggers",
		description: "Recurring workflow runs with explicit timezone and branch policy.",
	},
	"deployments": {
		title:       "Deployments",
		kicker:      "Delivery gates",
		description: "Environment history, approvals, protected targets, and rollback context.",
	},
	"settings": {
		title:       "Settings",
		kicker:      "Service policy",
		description: "Notification previews, event webhooks, retention, and control-plane configuration.",
	},
}

func (a *API) handleLoginPage(writer http.ResponseWriter, request *http.Request) {
	if principal, err := a.auth.Authenticate(request); err == nil && principal.Method == auth.AuthMethodSession {
		http.Redirect(writer, request, "/app", http.StatusSeeOther)
		return
	}
	a.web.RenderLogin(writer, http.StatusOK, webui.PageData{Version: a.version})
}

func (a *API) handleLoginForm(writer http.ResponseWriter, request *http.Request) {
	if !a.parseWebForm(writer, request) {
		return
	}
	principal, err := a.auth.AuthenticateBearer(strings.TrimSpace(request.FormValue("token")))
	if err != nil {
		a.renderLoginError(writer, request, "The admin token is not valid.")
		return
	}
	if _, err := a.store.RecordAudit(request.Context(), store.AuditEvent{
		Action:       "session.login",
		Actor:        principal.Subject,
		ResourceType: "session",
	}); err != nil {
		a.renderLoginError(writer, request, "The login could not be audited.")
		return
	}
	if _, err := a.auth.IssueSession(writer, request); err != nil {
		a.renderLoginError(writer, request, "The browser session could not be created.")
		return
	}
	a.redirectWeb(writer, request, "/app")
}

func (a *API) handleLogoutWeb(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	if _, err := a.store.RecordAudit(request.Context(), store.AuditEvent{
		Action:       "session.logout",
		Actor:        principal.Subject,
		ResourceType: "session",
	}); err != nil {
		http.Error(writer, "logout audit failed", http.StatusInternalServerError)
		return
	}
	a.auth.ClearSession(writer, request)
	a.redirectWeb(writer, request, "/login")
}

func (a *API) handleAppPage(writer http.ResponseWriter, request *http.Request) {
	section := strings.TrimSpace(request.PathValue("section"))
	if section == "" {
		section = "overview"
	}
	if _, ok := appPages[section]; !ok {
		http.NotFound(writer, request)
		return
	}
	a.renderAppSection(writer, request, section, "", http.StatusOK)
}

func (a *API) handleProjectPageWeb(writer http.ResponseWriter, request *http.Request) {
	a.renderProjectWorkspace(writer, request, request.PathValue("project"), "", strings.TrimSpace(request.URL.Query().Get("notice")), http.StatusOK)
}

func (a *API) renderProjectWorkspace(writer http.ResponseWriter, request *http.Request, projectID, message, notice string, status int) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	session, err := a.auth.CurrentSession(request)
	if err != nil {
		a.webUnauthorized(writer, request)
		return
	}
	project, err := a.store.GetProject(request.Context(), projectID)
	if err != nil {
		var notFound *store.ErrNotFound
		if errors.As(err, &notFound) {
			http.NotFound(writer, request)
			return
		}
		http.Error(writer, "failed to load project", http.StatusInternalServerError)
		return
	}
	allProjects, err := a.store.ListProjects(request.Context())
	if err != nil {
		http.Error(writer, "failed to list projects", http.StatusInternalServerError)
		return
	}
	data := webui.PageData{
		Page: "projects", Title: project.Name, Kicker: "Project workspace",
		Description: "Workflow graphs, dispatch, commit watch, cron, webhooks, and recent runs for one checkout.",
		Actor:       principal.Subject, CSRFToken: session.CSRFToken, Version: a.version,
		Error: message, Notice: notice, Projects: []store.Project{project},
		Runners: runnerInventoryViews(a.execution.RunnerInventory()),
	}
	if err := a.populateExecutionPage(request.Context(), &data, ""); err != nil {
		http.Error(writer, "failed to load project execution state", http.StatusInternalServerError)
		return
	}
	if err := a.populateProjectAutomationPage(request.Context(), &data, project, workflowNamesByID(data.Workflows)); err != nil {
		http.Error(writer, "failed to load project automation state", http.StatusInternalServerError)
		return
	}
	if len(data.ProjectViews) != 1 {
		http.Error(writer, "project execution state is incomplete", http.StatusInternalServerError)
		return
	}
	selected := data.ProjectViews[0]
	selected.Workspace = true
	data.SelectedProject = &selected
	data.ProjectViews = []webui.ProjectView{selected}
	data.Projects = allProjects
	data.RunFilter = webui.RunFilterView{Range: "all", Project: project.ID}
	data.Telemetry = buildRunTelemetry(data.Runs, data.RunFilter, time.Now().UTC())
	if isHTMX(request) && status >= 400 {
		status = http.StatusOK
	}
	a.web.RenderApp(writer, status, data, isHTMX(request))
}

func (a *API) handleCreateProjectWeb(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimSpace(request.FormValue("path"))
	canonicalPath, err := a.projects.ValidateLocalPath(path)
	if err != nil {
		a.renderAppSection(writer, request, "projects", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	slug := strings.TrimSpace(request.FormValue("slug"))
	if slug == "" {
		slug = projects.SuggestSlug(canonicalPath)
	}
	if err := projects.ValidateSlug(slug); err != nil {
		a.renderAppSection(writer, request, "projects", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	name := strings.TrimSpace(request.FormValue("name"))
	if name == "" {
		name = filepath.Base(canonicalPath)
	}
	defaultBranch := strings.TrimSpace(request.FormValue("defaultBranch"))
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	project, err := a.store.CreateProject(request.Context(), store.CreateProjectParams{
		Slug:          slug,
		Name:          name,
		SourceType:    "local",
		CanonicalPath: &canonicalPath,
		DefaultBranch: defaultBranch,
		Active:        true,
	})
	if err != nil {
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			a.renderAppSection(writer, request, "projects", conflict.Error(), http.StatusConflict)
			return
		}
		a.renderAppSection(writer, request, "projects", err.Error(), http.StatusUnprocessableEntity)
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	if _, err := a.store.RecordAudit(request.Context(), store.AuditEvent{
		ProjectID:    project.ID,
		Action:       "project.created",
		Actor:        principal.Subject,
		ResourceType: "project",
		ResourceID:   project.ID,
	}); err != nil {
		a.renderAppSection(writer, request, "projects", "Project created but audit recording failed.", http.StatusInternalServerError)
		return
	}
	notice := "PROJECT REGISTERED / WORKFLOWS SYNCED"
	if _, err := a.execution.SyncProject(request.Context(), project.ID); err != nil {
		notice = "PROJECT REGISTERED / WORKFLOW SCAN FAILED"
	} else {
		a.recordExecutionAudit(request, "workflow.synced", "project", project.ID)
	}
	if !isHTMX(request) {
		http.Redirect(writer, request, "/app/projects?notice="+strings.ReplaceAll(notice, " ", "%20"), http.StatusSeeOther)
		return
	}
	writer.Header().Set("HX-Trigger", "projectRegistered")
	a.renderAppSectionState(writer, request, "projects", "", notice, http.StatusOK)
}

func (a *API) renderAppSection(writer http.ResponseWriter, request *http.Request, section, message string, status int) {
	a.renderAppSectionState(writer, request, section, message, "", status)
}

func (a *API) renderAppSectionState(writer http.ResponseWriter, request *http.Request, section, message, notice string, status int) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	session, err := a.auth.CurrentSession(request)
	if err != nil {
		a.webUnauthorized(writer, request)
		return
	}
	items, err := a.store.ListProjects(request.Context())
	if err != nil {
		http.Error(writer, "failed to list projects", http.StatusInternalServerError)
		return
	}
	candidates, err := a.projects.Discover()
	if err != nil {
		http.Error(writer, "failed to discover projects", http.StatusInternalServerError)
		return
	}
	candidates = unregisteredProjectCandidates(items, candidates)
	definition := appPages[section]
	data := webui.PageData{
		Page:        section,
		Title:       definition.title,
		Kicker:      definition.kicker,
		Description: definition.description,
		Actor:       principal.Subject,
		CSRFToken:   session.CSRFToken,
		Version:     a.version,
		Projects:    items,
		Candidates:  candidates,
		Error:       message,
		Notice:      notice,
		RunFilter:   runFilterFromRequest(request),
		Runners:     runnerInventoryViews(a.execution.RunnerInventory()),
	}
	if err := a.populateExecutionPage(request.Context(), &data, ""); err != nil {
		http.Error(writer, "failed to load execution state", http.StatusInternalServerError)
		return
	}
	if err := a.populateConfigurationPage(request.Context(), &data); err != nil {
		http.Error(writer, "failed to load configuration state", http.StatusInternalServerError)
		return
	}
	if isHTMX(request) && status >= 400 {
		status = http.StatusOK
	}
	a.web.RenderApp(writer, status, data, isHTMX(request))
}

func unregisteredProjectCandidates(items []store.Project, candidates []projects.Project) []projects.Project {
	registered := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.CanonicalPath != nil {
			registered[*item.CanonicalPath] = struct{}{}
		}
	}

	result := make([]projects.Project, 0, len(candidates))
	for _, candidate := range candidates {
		if _, exists := registered[candidate.Path]; !exists {
			result = append(result, candidate)
		}
	}
	return result
}

func (a *API) requireWebAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !webSafeMethod(request.Method) {
			if !a.parseWebForm(writer, request) {
				return
			}
			if request.Header.Get("X-CSRF-Token") == "" {
				request.Header.Set("X-CSRF-Token", request.FormValue("_csrf"))
			}
		}
		principal, err := a.auth.Authenticate(request)
		if err != nil {
			if errors.Is(err, auth.ErrCSRF) {
				http.Error(writer, "csrf validation failed", http.StatusForbidden)
				return
			}
			a.webUnauthorized(writer, request)
			return
		}
		if principal.Method != auth.AuthMethodSession {
			a.webUnauthorized(writer, request)
			return
		}
		next.ServeHTTP(writer, contextWithPrincipal(request, principal))
	})
}

func contextWithPrincipal(request *http.Request, principal auth.Principal) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), principalContextKey{}, principal))
}

func (a *API) parseWebForm(writer http.ResponseWriter, request *http.Request) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, a.maxBodyBytes)
	if err := request.ParseForm(); err != nil {
		http.Error(writer, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func (a *API) renderLoginError(writer http.ResponseWriter, request *http.Request, message string) {
	if isHTMX(request) {
		a.web.RenderLoginFeedback(writer, http.StatusOK, message)
		return
	}
	a.web.RenderLogin(writer, http.StatusUnauthorized, webui.PageData{Version: a.version, Error: message})
}

func (a *API) redirectWeb(writer http.ResponseWriter, request *http.Request, target string) {
	if isHTMX(request) {
		writer.Header().Set("HX-Redirect", target)
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(writer, request, target, http.StatusSeeOther)
}

func (a *API) webUnauthorized(writer http.ResponseWriter, request *http.Request) {
	if isHTMX(request) {
		writer.Header().Set("HX-Redirect", "/login")
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(writer, request, "/login", http.StatusSeeOther)
}

func isHTMX(request *http.Request) bool {
	return strings.EqualFold(request.Header.Get("HX-Request"), "true")
}

func projectWorkspaceReturn(request *http.Request) string {
	return strings.TrimSpace(request.FormValue("returnProject"))
}

func webSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
