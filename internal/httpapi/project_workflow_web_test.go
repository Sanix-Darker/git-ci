package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebProjectRegistrationDiscoversAndRendersWorkflowGraph(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	workflowDir := filepath.Join(fixture.projectPath, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	definition := `name: Release pipeline
on: [push, workflow_dispatch]
concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: true
jobs:
  build:
    if: ${{ matrix.os != 'blocked' }}
    strategy:
      max-parallel: 2
      matrix:
        os: [linux, windows]
    runs-on: ubuntu-latest
    steps:
      - name: Compile
        run: go build ./...
  deploy:
    needs: [build]
    runs-on: ubuntu-latest
    steps:
      - name: Deploy
        run: ./deploy.sh
`
	if err := os.WriteFile(filepath.Join(workflowDir, "release.yml"), []byte(definition), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	fixture.commitProject(t)
	cookie, csrf := fixture.login(t)

	form := url.Values{"_csrf": {csrf}, "path": {fixture.projectPath}, "slug": {"release-project"}}
	request := httptest.NewRequest(http.MethodPost, "/app/projects", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "WORKFLOWS%20SYNCED") {
		t.Fatalf("register status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	projects, err := fixture.store.ListProjects(context.Background())
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects=%d err=%v", len(projects), err)
	}
	workflows, err := fixture.store.ListWorkflows(context.Background(), projects[0].ID)
	if err != nil || len(workflows) != 1 {
		t.Fatalf("workflows=%d err=%v", len(workflows), err)
	}

	request = httptest.NewRequest(http.MethodGet, "/app/workflows", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, expected := range []string{"Release pipeline", "Pipeline dependency graph", "Compile", "Deploy", "AFTER BUILD", "MATRIX 01/02", "OS=linux", "IF matrix.os != &#39;blocked&#39;", "LOCK release-${{ github.ref }} / CANCEL OLD", "name=\"ref\"", "name=\"commitSha\""} {
		if !strings.Contains(body, expected) {
			t.Fatalf("workflow page missing %q", expected)
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/app/projects", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	for _, expected := range []string{"LOCAL COMMIT WATCH", "SAVE COMMIT WATCH"} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("project page missing %q", expected)
		}
	}
}
