package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/auth"
	"github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/scheduler"
	"github.com/sanix-darker/git-ci/internal/secrets"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/webhooks"
)

type apiFixture struct {
	handler     http.Handler
	store       *store.Store
	root        string
	projectPath string
	token       string
}

func TestPublicAndAuthenticationContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)

	health := fixture.request(t, http.MethodGet, "/healthz", nil, "", nil, "", nil)
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, body=%s", health.Code, health.Body.String())
	}
	if health.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing security headers: %#v", health.Header())
	}
	if health.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing content security policy")
	}

	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/projects", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")

	badLogin := fixture.request(t, http.MethodPost, "/api/v1/session/login", map[string]any{"token": "wrong"}, "", nil, "", nil)
	assertAPIError(t, badLogin, http.StatusUnauthorized, "invalid_credentials")

	secureHeaders := http.Header{"X-Forwarded-Proto": []string{"https"}}
	login := fixture.request(t, http.MethodPost, "/api/v1/session/login", map[string]any{"token": fixture.token}, "", nil, "", secureHeaders)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", login.Code, login.Body.String())
	}
	cookie := findCookie(t, login.Result(), "gitci_session")
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe login cookie: %#v", cookie)
	}
	var loginPayload struct {
		Actor     string `json:"actor"`
		CSRFToken string `json:"csrfToken"`
	}
	decodeResponse(t, login, &loginPayload)
	if loginPayload.Actor != auth.AdminSubject || loginPayload.CSRFToken == "" {
		t.Fatalf("login response = %#v", loginPayload)
	}

	session := fixture.request(t, http.MethodGet, "/api/v1/session", nil, "", cookie, "", nil)
	if session.Code != http.StatusOK {
		t.Fatalf("session status = %d, body=%s", session.Code, session.Body.String())
	}
	if !strings.Contains(session.Body.String(), `"csrfToken":"`+loginPayload.CSRFToken+`"`) {
		t.Fatalf("session did not restore CSRF state: %s", session.Body.String())
	}
}

func TestCookieCSRFAndProjectLifecycle(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	cookie, csrf := fixture.login(t)
	payload := map[string]any{
		"slug":          "local-app",
		"name":          "Local app",
		"path":          fixture.projectPath,
		"defaultBranch": "master",
	}

	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects", payload, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")

	created := fixture.request(t, http.MethodPost, "/api/v1/projects", payload, "", cookie, csrf, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", created.Code, created.Body.String())
	}
	var project store.Project
	decodeResponse(t, created, &project)
	if project.Slug != "local-app" || project.CanonicalPath == nil || *project.CanonicalPath != fixture.projectPath {
		t.Fatalf("created project = %#v", project)
	}

	listed := fixture.request(t, http.MethodGet, "/api/v1/projects", nil, "", cookie, "", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"count":1`) {
		t.Fatalf("list status = %d, body=%s", listed.Code, listed.Body.String())
	}
	detail := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID, nil, "", cookie, "", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"slug":"local-app"`) {
		t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
	}

	conflict := fixture.request(t, http.MethodPost, "/api/v1/projects", payload, "", cookie, csrf, nil)
	assertAPIError(t, conflict, http.StatusConflict, "project_conflict")

	logout := fixture.request(t, http.MethodDelete, "/api/v1/session", nil, "", cookie, csrf, nil)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body=%s", logout.Code, logout.Body.String())
	}
}

func TestBearerProjectPolicyAndRequestLimits(t *testing.T) {
	fixture := newAPIFixture(t, 256)
	outside := t.TempDir()
	escape := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"slug": "escape",
		"path": outside,
	}, fixture.token, nil, "", nil)
	assertAPIError(t, escape, http.StatusBadRequest, "invalid_project_path")

	created := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"path": fixture.projectPath,
	}, fixture.token, nil, "", nil)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"slug":"project"`) {
		t.Fatalf("bearer create status = %d, body=%s", created.Code, created.Body.String())
	}

	unknown := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"path":    fixture.projectPath,
		"unknown": true,
	}, fixture.token, nil, "", nil)
	assertAPIError(t, unknown, http.StatusBadRequest, "invalid_json")

	largeBody := bytes.NewBufferString(`{"token":"` + strings.Repeat("x", 300) + `"}`)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session/login", largeBody)
	request.Header.Set("Content-Type", "application/json")
	fixture.handler.ServeHTTP(recorder, request)
	assertAPIError(t, recorder, http.StatusRequestEntityTooLarge, "body_too_large")
}

func TestDiscoveryAndRouteSeparation(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	candidates := fixture.request(t, http.MethodGet, "/api/v1/project-candidates", nil, fixture.token, nil, "", nil)
	if candidates.Code != http.StatusOK || !strings.Contains(candidates.Body.String(), fixture.projectPath) {
		t.Fatalf("candidates status = %d, body=%s", candidates.Code, candidates.Body.String())
	}

	home := fixture.request(t, http.MethodGet, "/", nil, "", nil, "", nil)
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "public-home") {
		t.Fatalf("home status = %d, body=%s", home.Code, home.Body.String())
	}
	app := fixture.request(t, http.MethodGet, "/app", nil, "", nil, "", nil)
	if app.Code != http.StatusSeeOther || app.Header().Get("Location") != "/login" {
		t.Fatalf("app status = %d, location=%q", app.Code, app.Header().Get("Location"))
	}
	login := fixture.request(t, http.MethodGet, "/login", nil, "", nil, "", nil)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), "OPERATOR GATE") {
		t.Fatalf("login status = %d, body=%s", login.Code, login.Body.String())
	}
	legacy := fixture.request(t, http.MethodGet, "/api/v0/projects", nil, "", nil, "", nil)
	assertAPIError(t, legacy, http.StatusNotFound, "route_not_found")
	missing := fixture.request(t, http.MethodGet, "/api/v1/projects/missing", nil, fixture.token, nil, "", nil)
	assertAPIError(t, missing, http.StatusNotFound, "project_not_found")
}

func newAPIFixture(t *testing.T, maxBodyBytes int64) *apiFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	projectPath := filepath.Join(root, "project")
	staticDir := filepath.Join(base, "site")
	for _, directory := range []string{filepath.Join(projectPath, ".git"), staticDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("public-home"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	registry, err := projects.NewRegistry([]string{root})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	database, err := store.Open(context.Background(), filepath.Join(base, "gci.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	manager, token, err := auth.NewManager(filepath.Join(base, "admin.token"), filepath.Join(base, "session.key"))
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	secretManager, err := secrets.NewManager(database, filepath.Join(base, "secret.key"))
	if err != nil {
		t.Fatalf("new secret manager: %v", err)
	}
	executionManager, err := execution.NewManager(database, execution.WithSecretResolver(secretManager))
	if err != nil {
		t.Fatalf("new execution manager: %v", err)
	}
	scheduleManager, err := scheduler.NewManager(database, executionManager)
	if err != nil {
		t.Fatalf("new scheduler manager: %v", err)
	}
	webhookManager, err := webhooks.NewManager(database, executionManager)
	if err != nil {
		t.Fatalf("new webhook manager: %v", err)
	}
	handler, err := New(Config{
		Auth: manager, Store: database, Projects: registry, StaticDir: staticDir,
		Version: "test", MaxBodyBytes: maxBodyBytes, Execution: executionManager,
		Secrets: secretManager, Scheduler: scheduleManager,
		Webhooks: webhookManager,
	})
	if err != nil {
		t.Fatalf("new API: %v", err)
	}
	return &apiFixture{handler: handler, store: database, root: root, projectPath: projectPath, token: token}
}

func (f *apiFixture) login(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	response := f.request(t, http.MethodPost, "/api/v1/session/login", map[string]any{"token": f.token}, "", nil, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		CSRFToken string `json:"csrfToken"`
	}
	decodeResponse(t, response, &payload)
	return findCookie(t, response.Result(), "gitci_session"), payload.CSRFToken
}

func (f *apiFixture) request(t *testing.T, method, path string, payload any, bearer string, cookie *http.Cookie, csrf string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	return recorder
}

func findCookie(t *testing.T, response *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("cookie %q not found", name)
	return nil
}

func assertAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), `"code":"`+code+`"`) {
		t.Fatalf("response status = %d, body=%s; want status %d code %s", response.Code, response.Body.String(), status, code)
	}
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), destination); err != nil {
		t.Fatalf("decode response %s: %v", response.Body.String(), err)
	}
}
