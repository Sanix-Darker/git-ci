package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWebLoginNavigationProjectRegistrationAndLogout(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)

	login := webRequest(fixture, http.MethodPost, "/login", url.Values{"token": {"wrong"}}, nil, true)
	if login.Code != http.StatusOK || !strings.Contains(login.Body.String(), "not valid") {
		t.Fatalf("invalid login = %d %s", login.Code, login.Body.String())
	}

	login = webRequest(fixture, http.MethodPost, "/login", url.Values{"token": {fixture.token}}, nil, true)
	if login.Code != http.StatusNoContent || login.Header().Get("HX-Redirect") != "/app" {
		t.Fatalf("login = %d, redirect=%q, body=%s", login.Code, login.Header().Get("HX-Redirect"), login.Body.String())
	}
	cookie := findCookie(t, login.Result(), "gitci_session")

	app := webRequest(fixture, http.MethodGet, "/app", nil, cookie, false)
	if app.Code != http.StatusOK || !strings.Contains(app.Body.String(), "Dashboard") || !strings.Contains(app.Body.String(), "Workflows") {
		t.Fatalf("app = %d %s", app.Code, app.Body.String())
	}
	session := fixture.request(t, http.MethodGet, "/api/v1/session", nil, "", cookie, "", nil)
	var sessionPayload map[string]any
	decodeResponse(t, session, &sessionPayload)
	csrf, _ := sessionPayload["csrfToken"].(string)
	if csrf == "" {
		t.Fatalf("session CSRF token missing: %s", session.Body.String())
	}

	projectsPage := webRequest(fixture, http.MethodGet, "/app/projects", nil, cookie, true)
	if projectsPage.Code != http.StatusOK || !strings.Contains(projectsPage.Body.String(), fixture.projectPath) {
		t.Fatalf("projects = %d %s", projectsPage.Code, projectsPage.Body.String())
	}

	create := webRequest(fixture, http.MethodPost, "/app/projects", url.Values{
		"_csrf": {csrf},
		"path":  {fixture.projectPath},
		"slug":  {"web-project"},
	}, cookie, true)
	if create.Code != http.StatusOK || !strings.Contains(create.Body.String(), "web-project") {
		t.Fatalf("create = %d %s", create.Code, create.Body.String())
	}

	noCSRF := webRequest(fixture, http.MethodPost, "/app/projects", url.Values{
		"path": {fixture.projectPath},
		"slug": {"forbidden"},
	}, cookie, true)
	if noCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d, body=%s", noCSRF.Code, noCSRF.Body.String())
	}

	logout := webRequest(fixture, http.MethodPost, "/logout", url.Values{"_csrf": {csrf}}, cookie, true)
	if logout.Code != http.StatusNoContent || logout.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("logout = %d, redirect=%q", logout.Code, logout.Header().Get("HX-Redirect"))
	}
}

func TestWebUnknownSectionAndEmbeddedAssets(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	cookie, _ := fixture.login(t)

	runners := webRequest(fixture, http.MethodGet, "/app/runners", nil, cookie, true)
	for _, expected := range []string{"Runners", "LOCAL RUNNER", "data-runner-card", `href="/app/runners"`} {
		if runners.Code != http.StatusOK || !strings.Contains(runners.Body.String(), expected) {
			t.Fatalf("runners page missing %q: status=%d body=%s", expected, runners.Code, runners.Body.String())
		}
	}

	missing := webRequest(fixture, http.MethodGet, "/app/unknown", nil, cookie, true)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown section status = %d", missing.Code)
	}
	asset := webRequest(fixture, http.MethodGet, "/ui/assets/htmx.min.js", nil, nil, false)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d, cache=%q", asset.Code, asset.Header().Get("Cache-Control"))
	}
}

func webRequest(fixture *apiFixture, method, path string, values url.Values, cookie *http.Cookie, htmx bool) *httptest.ResponseRecorder {
	var body string
	if values != nil {
		body = values.Encode()
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		request.Header.Set("HX-Request", "true")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	return recorder
}
