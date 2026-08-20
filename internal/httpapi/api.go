// Package httpapi exposes the versioned git-ci service control API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/scheduler"
	"github.com/sanix-darker/git-ci/internal/secrets"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webhooks"
	"github.com/sanix-darker/git-ci/internal/webui"
	"github.com/sanix-darker/git-ci/site"
)

const DefaultMaxBodyBytes int64 = 1 << 20

type Config struct {
	Auth         *auth.Manager
	Store        *store.Store
	Projects     *projects.Registry
	StaticDir    string
	Version      string
	MaxBodyBytes int64
	Execution    *execution.Manager
	Secrets      *secrets.Manager
	Scheduler    *scheduler.Manager
	Webhooks     *webhooks.Manager
}

type API struct {
	auth         *auth.Manager
	store        *store.Store
	projects     *projects.Registry
	staticDir    string
	version      string
	maxBodyBytes int64
	web          *webui.Renderer
	execution    *execution.Manager
	secrets      *secrets.Manager
	scheduler    *scheduler.Manager
	webhooks     *webhooks.Manager
}

type principalContextKey struct{}

func New(config Config) (http.Handler, error) {
	if config.Auth == nil {
		return nil, errors.New("httpapi: auth manager is required")
	}
	if config.Store == nil {
		return nil, errors.New("httpapi: store is required")
	}
	if config.Projects == nil {
		return nil, errors.New("httpapi: project registry is required")
	}
	if config.Execution == nil {
		return nil, errors.New("httpapi: execution manager is required")
	}
	if config.Secrets == nil || config.Scheduler == nil || config.Webhooks == nil {
		return nil, errors.New("httpapi: secrets, scheduler, and webhook managers are required")
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if strings.TrimSpace(config.Version) == "" {
		config.Version = "dev"
	}
	if config.StaticDir != "" {
		info, err := os.Stat(config.StaticDir)
		if err != nil {
			return nil, fmt.Errorf("httpapi: static directory: %w", err)
		}
		if !info.IsDir() {
			return nil, errors.New("httpapi: static path is not a directory")
		}
	}

	api := &API{
		auth:         config.Auth,
		store:        config.Store,
		projects:     config.Projects,
		staticDir:    config.StaticDir,
		version:      config.Version,
		maxBodyBytes: config.MaxBodyBytes,
		execution:    config.Execution,
		secrets:      config.Secrets,
		scheduler:    config.Scheduler,
		webhooks:     config.Webhooks,
	}
	renderer, err := webui.New()
	if err != nil {
		return nil, fmt.Errorf("httpapi: web renderer: %w", err)
	}
	api.web = renderer
	return api.routes(), nil
}

func (a *API) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.Handle("GET /ui/assets/", http.StripPrefix("/ui/assets/", a.web.Assets()))
	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("POST /login", a.handleLoginForm)
	mux.Handle("POST /logout", a.requireWebAuth(http.HandlerFunc(a.handleLogoutWeb)))
	mux.Handle("GET /app", a.requireWebAuth(http.HandlerFunc(a.handleAppPage)))
	mux.Handle("GET /app/{section}", a.requireWebAuth(http.HandlerFunc(a.handleAppPage)))
	mux.Handle("POST /app/projects", a.requireWebAuth(http.HandlerFunc(a.handleCreateProjectWeb)))
	mux.Handle("POST /app/projects/{project}/workflows/sync", a.requireWebAuth(http.HandlerFunc(a.handleSyncWorkflowsWeb)))
	mux.Handle("POST /app/workflows/{workflow}/runs", a.requireWebAuth(http.HandlerFunc(a.handleEnqueueRunWeb)))
	mux.Handle("GET /app/runs/{run}", a.requireWebAuth(http.HandlerFunc(a.handleRunPageWeb)))
	mux.Handle("GET /app/runs/{run}/panel", a.requireWebAuth(http.HandlerFunc(a.handleRunPanelWeb)))
	mux.Handle("POST /app/runs/{run}/cancel", a.requireWebAuth(http.HandlerFunc(a.handleCancelRunWeb)))
	mux.Handle("POST /app/secrets", a.requireWebAuth(http.HandlerFunc(a.handleUpsertSecretWeb)))
	mux.Handle("POST /app/secrets/{secret}/delete", a.requireWebAuth(http.HandlerFunc(a.handleDeleteSecretWeb)))
	mux.Handle("POST /app/schedules", a.requireWebAuth(http.HandlerFunc(a.handleCreateScheduleWeb)))
	mux.Handle("POST /app/schedules/{schedule}/toggle", a.requireWebAuth(http.HandlerFunc(a.handleToggleScheduleWeb)))
	mux.Handle("POST /app/schedules/{schedule}/delete", a.requireWebAuth(http.HandlerFunc(a.handleDeleteScheduleWeb)))
	mux.Handle("POST /app/settings/webhooks", a.requireWebAuth(http.HandlerFunc(a.handleCreateWebhookWeb)))
	mux.HandleFunc("POST /api/v1/session/login", a.handleLogin)
	mux.Handle("GET /api/v1", a.requireAuth(http.HandlerFunc(a.handleAPIRoot)))
	mux.Handle("GET /api/v1/session", a.requireAuth(http.HandlerFunc(a.handleSession)))
	mux.Handle("DELETE /api/v1/session", a.requireAuth(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /api/v1/project-candidates", a.requireAuth(http.HandlerFunc(a.handleProjectCandidates)))
	mux.Handle("GET /api/v1/projects", a.requireAuth(http.HandlerFunc(a.handleProjects)))
	mux.Handle("POST /api/v1/projects", a.requireAuth(http.HandlerFunc(a.handleProjects)))
	mux.Handle("GET /api/v1/projects/{project}", a.requireAuth(http.HandlerFunc(a.handleProject)))
	mux.Handle("GET /api/v1/projects/{project}/workflows", a.requireAuth(http.HandlerFunc(a.handleProjectWorkflows)))
	mux.Handle("POST /api/v1/projects/{project}/workflows/sync", a.requireAuth(http.HandlerFunc(a.handleSyncProjectWorkflows)))
	mux.Handle("GET /api/v1/workflows/{workflow}", a.requireAuth(http.HandlerFunc(a.handleWorkflow)))
	mux.Handle("POST /api/v1/workflows/{workflow}/runs", a.requireAuth(http.HandlerFunc(a.handleEnqueueWorkflowRun)))
	mux.Handle("GET /api/v1/projects/{project}/runs", a.requireAuth(http.HandlerFunc(a.handleProjectRuns)))
	mux.Handle("GET /api/v1/runs/{run}", a.requireAuth(http.HandlerFunc(a.handleRun)))
	mux.Handle("POST /api/v1/runs/{run}/cancel", a.requireAuth(http.HandlerFunc(a.handleCancelRun)))
	mux.Handle("GET /api/v1/runs/{run}/logs", a.requireAuth(http.HandlerFunc(a.handleRunLogs)))
	mux.Handle("GET /api/v1/projects/{project}/secrets", a.requireAuth(http.HandlerFunc(a.handleProjectSecrets)))
	mux.Handle("POST /api/v1/projects/{project}/secrets", a.requireAuth(http.HandlerFunc(a.handleProjectSecrets)))
	mux.Handle("DELETE /api/v1/secrets/{secret}", a.requireAuth(http.HandlerFunc(a.handleSecret)))
	mux.Handle("GET /api/v1/projects/{project}/schedules", a.requireAuth(http.HandlerFunc(a.handleProjectSchedules)))
	mux.Handle("POST /api/v1/projects/{project}/schedules", a.requireAuth(http.HandlerFunc(a.handleProjectSchedules)))
	mux.Handle("PATCH /api/v1/schedules/{schedule}", a.requireAuth(http.HandlerFunc(a.handleSchedule)))
	mux.Handle("DELETE /api/v1/schedules/{schedule}", a.requireAuth(http.HandlerFunc(a.handleSchedule)))
	mux.Handle("GET /api/v1/projects/{project}/webhooks", a.requireAuth(http.HandlerFunc(a.handleProjectWebhooks)))
	mux.Handle("POST /api/v1/projects/{project}/webhooks", a.requireAuth(http.HandlerFunc(a.handleProjectWebhooks)))
	mux.Handle("GET /api/v1/projects/{project}/deployments", a.requireAuth(http.HandlerFunc(a.handleProjectDeployments)))
	mux.Handle("POST /api/v1/projects/{project}/deployments", a.requireAuth(http.HandlerFunc(a.handleProjectDeployments)))
	mux.Handle("PATCH /api/v1/deployments/{deployment}", a.requireAuth(http.HandlerFunc(a.handleDeployment)))
	mux.HandleFunc("POST /hooks/{endpoint}", a.handleWebhookDelivery)
	mux.HandleFunc("/api/", a.handleAPINotFound)
	mux.HandleFunc("/api", a.handleAPINotFound)

	if a.staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(a.staticDir)))
	} else {
		mux.Handle("/", site.Handler())
	}

	return a.securityHeaders(mux)
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'")
		writer.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func (a *API) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, err := a.auth.Authenticate(request)
		if err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, auth.ErrCSRF) {
				status = http.StatusForbidden
			}
			writeError(writer, status, authErrorCode(err), err.Error())
			return
		}
		ctx := context.WithValue(request.Context(), principalContextKey{}, principal)
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

func (a *API) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": a.version,
	})
}

func (a *API) handleAPIRoot(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"api":          "v1",
		"capabilities": []string{"auth", "local-projects", "workflow-discovery", "durable-runs", "local-worker", "audit"},
	})
}

func (a *API) handleLogin(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Token string `json:"token"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	principal, err := a.auth.AuthenticateBearer(payload.Token)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
		return
	}
	if _, err := a.store.RecordAudit(request.Context(), store.AuditEvent{
		Action:       "session.login",
		Actor:        principal.Subject,
		ResourceType: "session",
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "audit_failed", "failed to record login")
		return
	}
	issued, err := a.auth.IssueSession(writer, request)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "session_failed", "failed to create session")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"actor":     principal.Subject,
		"csrfToken": issued.CSRFToken,
		"expiresAt": issued.ExpiresAt.UTC(),
	})
}

func (a *API) handleSession(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	payload := map[string]any{
		"actor": principal.Subject,
		"auth":  principal.Method,
	}
	if principal.Method == auth.AuthMethodSession {
		session, err := a.auth.CurrentSession(request)
		if err != nil {
			writeError(writer, http.StatusUnauthorized, authErrorCode(err), err.Error())
			return
		}
		payload["csrfToken"] = session.CSRFToken
		payload["expiresAt"] = session.ExpiresAt
	}
	writeJSON(writer, http.StatusOK, payload)
}

func (a *API) handleLogout(writer http.ResponseWriter, request *http.Request) {
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	if _, err := a.store.RecordAudit(request.Context(), store.AuditEvent{
		Action:       "session.logout",
		Actor:        principal.Subject,
		ResourceType: "session",
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "audit_failed", "failed to record logout")
		return
	}
	a.auth.ClearSession(writer, request)
	writer.WriteHeader(http.StatusNoContent)
}

func (a *API) handleProjectCandidates(writer http.ResponseWriter, _ *http.Request) {
	candidates, err := a.projects.Discover()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "discovery_failed", "failed to discover projects")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items": candidates,
		"count": len(candidates),
	})
}

func (a *API) handleProjects(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items, err := a.store.ListProjects(request.Context())
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "store_failed", "failed to list projects")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
	case http.MethodPost:
		a.createProject(writer, request)
	default:
		writer.Header().Set("Allow", "GET, POST")
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (a *API) createProject(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		Path          string `json:"path"`
		DefaultBranch string `json:"defaultBranch"`
	}
	if !a.decodeJSON(writer, request, &payload) {
		return
	}
	canonicalPath, err := a.projects.ValidateLocalPath(payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_project_path", err.Error())
		return
	}
	payload.Slug = strings.TrimSpace(payload.Slug)
	if payload.Slug == "" {
		payload.Slug = projects.SuggestSlug(canonicalPath)
	}
	if err := projects.ValidateSlug(payload.Slug); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_project_slug", err.Error())
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	if payload.Name == "" {
		payload.Name = filepath.Base(canonicalPath)
	}
	payload.DefaultBranch = strings.TrimSpace(payload.DefaultBranch)
	if payload.DefaultBranch == "" {
		payload.DefaultBranch = "main"
	}
	project, err := a.store.CreateProject(request.Context(), store.CreateProjectParams{
		Slug:          payload.Slug,
		Name:          payload.Name,
		SourceType:    "local",
		CanonicalPath: &canonicalPath,
		DefaultBranch: payload.DefaultBranch,
		Active:        true,
	})
	if err != nil {
		var conflict *store.ErrConflict
		if errors.As(err, &conflict) {
			writeError(writer, http.StatusConflict, "project_conflict", conflict.Error())
			return
		}
		writeError(writer, http.StatusBadRequest, "invalid_project", err.Error())
		return
	}
	principal := request.Context().Value(principalContextKey{}).(auth.Principal)
	if _, err := a.store.RecordAudit(request.Context(), store.AuditEvent{
		ProjectID:    project.ID,
		Action:       "project.created",
		Actor:        principal.Subject,
		ResourceType: "project",
		ResourceID:   project.ID,
		Metadata:     json.RawMessage(`{"sourceType":"local"}`),
	}); err != nil {
		writeError(writer, http.StatusInternalServerError, "audit_failed", "project created but audit recording failed")
		return
	}
	writeJSON(writer, http.StatusCreated, project)
}

func (a *API) handleProject(writer http.ResponseWriter, request *http.Request) {
	project, err := a.store.GetProject(request.Context(), request.PathValue("project"))
	if err != nil {
		var notFound *store.ErrNotFound
		if errors.As(err, &notFound) {
			writeError(writer, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		writeError(writer, http.StatusInternalServerError, "store_failed", "failed to load project")
		return
	}
	writeJSON(writer, http.StatusOK, project)
}

func (a *API) handleAPINotFound(writer http.ResponseWriter, _ *http.Request) {
	writeError(writer, http.StatusNotFound, "route_not_found", "API route not found")
}

func (a *API) decodeJSON(writer http.ResponseWriter, request *http.Request, destination any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, a.maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large")
			return false
		}
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
		return false
	}
	return true
}

func authErrorCode(err error) string {
	var authError *auth.AuthError
	if errors.As(err, &authError) {
		return string(authError.Code)
	}
	return "authentication_failed"
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
