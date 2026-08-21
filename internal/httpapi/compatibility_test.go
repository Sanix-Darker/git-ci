package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/compatibility"
	"github.com/sanix-darker/git-ci/internal/store"
)

func TestCompatibilityAPIWebAndRegistrationDiscovery(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/compatibility", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")
	reportResponse := fixture.request(t, http.MethodGet, "/api/v1/compatibility?provider=github&state=partial&q=actions", nil, fixture.token, nil, "", nil)
	if reportResponse.Code != http.StatusOK {
		t.Fatalf("compatibility status = %d, body=%s", reportResponse.Code, reportResponse.Body.String())
	}
	var report compatibility.Report
	decodeResponse(t, reportResponse, &report)
	if report.Count == 0 || report.Counts.Partial != report.Count {
		t.Fatalf("compatibility report = %#v", report)
	}
	invalid := fixture.request(t, http.MethodGet, "/api/v1/compatibility?provider=circleci", nil, fixture.token, nil, "", nil)
	assertAPIError(t, invalid, http.StatusBadRequest, "invalid_compatibility_filter")
	root := fixture.request(t, http.MethodGet, "/api/v1", nil, fixture.token, nil, "", nil)
	if !strings.Contains(root.Body.String(), "compatibility-report") {
		t.Fatalf("API root does not advertise compatibility report: %s", root.Body.String())
	}
	cookie, _ := fixture.login(t)
	page := fixture.request(t, http.MethodGet, "/app/compatibility", nil, "", cookie, "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "SUPPORT CONTRACT") || !strings.Contains(page.Body.String(), "GITHUB_TOKEN permissions") {
		t.Fatalf("compatibility page = %d, %s", page.Code, page.Body.String())
	}

	workflowDir := filepath.Join(fixture.projectPath, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "ci.yml"), []byte("name: Auto discovery\non: workflow_dispatch\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo discovered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{"slug": "auto-discovery", "path": fixture.projectPath}, fixture.token, nil, "", nil)
	if created.Code != http.StatusCreated || created.Header().Get("X-GCI-Workflow-Discovery") != "succeeded" || created.Header().Get("X-GCI-Workflow-Count") != "1" {
		t.Fatalf("registration discovery = %d, headers=%v, body=%s", created.Code, created.Header(), created.Body.String())
	}
	var project store.Project
	decodeResponse(t, created, &project)
	workflows := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/workflows", nil, fixture.token, nil, "", nil)
	if workflows.Code != http.StatusOK || !strings.Contains(workflows.Body.String(), `"count":1`) || !strings.Contains(workflows.Body.String(), "Auto discovery") {
		t.Fatalf("auto-discovered workflows = %d, %s", workflows.Code, workflows.Body.String())
	}

	broken := newAPIFixture(t, DefaultMaxBodyBytes)
	brokenDir := filepath.Join(broken.projectPath, ".github", "workflows")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "broken.yml"), []byte("jobs: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenRegistration := broken.request(t, http.MethodPost, "/api/v1/projects", map[string]any{"slug": "broken-discovery", "path": broken.projectPath}, broken.token, nil, "", nil)
	if brokenRegistration.Code != http.StatusCreated || brokenRegistration.Header().Get("X-GCI-Workflow-Discovery") != "failed" {
		t.Fatalf("broken registration = %d, headers=%v, body=%s", brokenRegistration.Code, brokenRegistration.Header(), brokenRegistration.Body.String())
	}
}
