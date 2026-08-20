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
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
)

const DefaultMaxBodyBytes int64 = 1 << 20

type Config struct {
	Auth         *auth.Manager
	Store        *store.Store
	Projects     *projects.Registry
	StaticDir    string
	Version      string
	MaxBodyBytes int64
}

type API struct {
	auth         *auth.Manager
	store        *store.Store
	projects     *projects.Registry
	staticDir    string
	version      string
	maxBodyBytes int64
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
	}
	return api.routes(), nil
}

func (a *API) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("POST /api/v1/session/login", a.handleLogin)
	mux.Handle("GET /api/v1", a.requireAuth(http.HandlerFunc(a.handleAPIRoot)))
	mux.Handle("GET /api/v1/session", a.requireAuth(http.HandlerFunc(a.handleSession)))
	mux.Handle("DELETE /api/v1/session", a.requireAuth(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /api/v1/project-candidates", a.requireAuth(http.HandlerFunc(a.handleProjectCandidates)))
	mux.Handle("GET /api/v1/projects", a.requireAuth(http.HandlerFunc(a.handleProjects)))
	mux.Handle("POST /api/v1/projects", a.requireAuth(http.HandlerFunc(a.handleProjects)))
	mux.Handle("GET /api/v1/projects/{project}", a.requireAuth(http.HandlerFunc(a.handleProject)))
	mux.HandleFunc("/api/", a.handleAPINotFound)
	mux.HandleFunc("/api", a.handleAPINotFound)
	mux.HandleFunc("/app", a.handleAppUnavailable)
	mux.HandleFunc("/app/", a.handleAppUnavailable)

	if a.staticDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(a.staticDir)))
	} else {
		mux.HandleFunc("/", http.NotFound)
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
		"capabilities": []string{"auth", "local-projects", "audit"},
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
	writeJSON(writer, http.StatusOK, map[string]any{
		"actor": principal.Subject,
		"auth":  principal.Method,
	})
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

func (a *API) handleAppUnavailable(writer http.ResponseWriter, _ *http.Request) {
	writeError(writer, http.StatusNotFound, "console_unavailable", "operator console is not enabled")
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
