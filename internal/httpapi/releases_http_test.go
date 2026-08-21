package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestReleaseAPIAndWebLifecycle(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	workflowPath := filepath.Join(fixture.projectPath, ".github", "workflows", "release.yml")
	if err := os.MkdirAll(filepath.Dir(workflowPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, []byte("name: Release CI\non: workflow_dispatch\njobs:\n  package:\n    runs-on: self-hosted\n    steps:\n      - run: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.commitProject(t)
	head := fixture.projectHead(t)
	if output, err := exec.Command("git", "-C", fixture.projectPath, "tag", "v2.0.0").CombinedOutput(); err != nil {
		t.Fatalf("create release tag: %v: %s", err, output)
	}
	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/projects/missing/releases", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")
	cookie, csrf := fixture.login(t)
	createdProject := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{"slug": "release-api", "name": "Release API", "path": fixture.projectPath}, "", cookie, csrf, nil)
	var project store.Project
	decodeResponse(t, createdProject, &project)
	synced := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/workflows/sync", nil, "", cookie, csrf, nil)
	var workflows struct {
		Items []store.Workflow `json:"items"`
	}
	decodeResponse(t, synced, &workflows)
	if len(workflows.Items) != 1 {
		t.Fatalf("synced workflows = %s", synced.Body.String())
	}
	run, err := fixture.store.EnqueueRun(t.Context(), store.EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflows.Items[0].ID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: head, SourcePath: fixture.projectPath, Jobs: []store.EnqueueJob{{Key: "package", Name: "Package", DependencyKeys: json.RawMessage(`[]`), Steps: []store.EnqueueStep{{Key: "package", Name: "Package", Command: "true"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := fixture.store.ClaimNextQueuedRun(t.Context(), "release-api-worker")
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim source = %#v, %v", claimed, err)
	}
	if _, err := fixture.store.TransitionRun(t.Context(), run.ID, store.StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"runId": run.ID, "tagName": "v2.0.0", "name": "Version 2", "notes": "API release"}
	missingCSRF := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/releases", payload, "", cookie, "", nil)
	assertAPIError(t, missingCSRF, http.StatusForbidden, "csrf_failed")
	created := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/releases", payload, "", cookie, csrf, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create release status=%d body=%s", created.Code, created.Body.String())
	}
	var release store.Release
	decodeResponse(t, created, &release)
	if release.State != store.ReleaseDraft || release.TargetCommitSHA != head {
		t.Fatalf("created release = %#v", release)
	}
	duplicate := fixture.request(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/releases", payload, "", cookie, csrf, nil)
	assertAPIError(t, duplicate, http.StatusConflict, "conflict")
	updated := fixture.request(t, http.MethodPatch, "/api/v1/releases/"+release.ID, map[string]any{"name": "Version 2 stable", "notes": "Updated API release"}, "", cookie, csrf, nil)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), "Updated API release") {
		t.Fatalf("update release status=%d body=%s", updated.Code, updated.Body.String())
	}
	detail := fixture.request(t, http.MethodGet, "/api/v1/releases/"+release.ID, nil, "", cookie, "", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"artifacts":[]`) || !strings.Contains(detail.Body.String(), run.ID) {
		t.Fatalf("release detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	published := fixture.request(t, http.MethodPost, "/api/v1/releases/"+release.ID+"/publish", nil, "", cookie, csrf, nil)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"state":"published"`) {
		t.Fatalf("publish release status=%d body=%s", published.Code, published.Body.String())
	}
	latest := fixture.request(t, http.MethodGet, "/api/v1/releases/latest?project="+project.ID, nil, "", cookie, "", nil)
	if latest.Code != http.StatusOK || !strings.Contains(latest.Body.String(), release.ID) {
		t.Fatalf("latest release status=%d body=%s", latest.Code, latest.Body.String())
	}
	deletePublished := fixture.request(t, http.MethodDelete, "/api/v1/releases/"+release.ID, nil, "", cookie, csrf, nil)
	assertAPIError(t, deletePublished, http.StatusUnprocessableEntity, "release_published")
	page := fixture.request(t, http.MethodGet, "/app/releases/"+release.ID, nil, "", cookie, "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Version 2 stable") || !strings.Contains(page.Body.String(), "SOURCE RUN") {
		t.Fatalf("release page status=%d body=%s", page.Code, page.Body.String())
	}
	audit := fixture.request(t, http.MethodGet, "/api/v1/audit?q=release.published", nil, "", cookie, "", nil)
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), "release.published") || !strings.Contains(audit.Body.String(), release.ID) {
		t.Fatalf("release audit status=%d body=%s", audit.Code, audit.Body.String())
	}
}
