package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestChildPipelineAPIAndWebGraphContract(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	if err := os.WriteFile(filepath.Join(fixture.projectPath, ".gitlab-ci.yml"), []byte("bridge:\n  trigger:\n    include: child.yml\n    strategy: mirror\nafter:\n  needs: [bridge]\n  script: [\"printf parent\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.projectPath, "child.yml"), []byte("verify:\n  script: [\"printf child\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.commitProject(t)
	created := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{"slug": "child-api", "name": "Child API", "path": fixture.projectPath}, fixture.token, nil, "", nil)
	var project store.Project
	decodeResponse(t, created, &project)
	workflowsResponse := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/workflows", nil, fixture.token, nil, "", nil)
	var workflows struct {
		Items []store.Workflow `json:"items"`
	}
	decodeResponse(t, workflowsResponse, &workflows)
	queued := fixture.request(t, http.MethodPost, "/api/v1/workflows/"+workflows.Items[0].ID+"/runs", map[string]any{"ref": "refs/heads/main", "commitSha": fixture.projectHead(t)}, fixture.token, nil, "", nil)
	var parent store.Run
	decodeResponse(t, queued, &parent)
	for range 3 {
		processed, err := fixture.execution.ProcessNext(t.Context())
		if err != nil || !processed {
			t.Fatalf("process = %v, %v", processed, err)
		}
	}
	graphResponse := fixture.request(t, http.MethodGet, "/api/v1/runs/"+parent.ID, nil, fixture.token, nil, "", nil)
	var graph store.RunGraph
	decodeResponse(t, graphResponse, &graph)
	if graph.Run.Status != store.StatusSucceeded || len(graph.ChildPipelines) != 1 {
		t.Fatalf("graph = %#v", graph)
	}
	childResponse := fixture.request(t, http.MethodGet, "/api/v1/runs/"+graph.ChildPipelines[0].ChildRunID, nil, fixture.token, nil, "", nil)
	var child store.RunGraph
	decodeResponse(t, childResponse, &child)
	if child.ParentPipeline == nil || child.ParentPipeline.ParentRunID != parent.ID || child.Run.CommitSHA == nil || *child.Run.CommitSHA != *graph.Run.CommitSHA {
		t.Fatalf("child = %#v", child)
	}
	runsResponse := fixture.request(t, http.MethodGet, "/api/v1/projects/"+project.ID+"/runs", nil, fixture.token, nil, "", nil)
	if !strings.Contains(runsResponse.Body.String(), parent.ID) || strings.Contains(runsResponse.Body.String(), child.Run.ID) {
		t.Fatalf("runs = %s", runsResponse.Body.String())
	}
	cookie, _ := fixture.login(t)
	page := fixture.request(t, http.MethodGet, "/app/runs/"+parent.ID, nil, "", cookie, "", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "DOWNSTREAM") || !strings.Contains(page.Body.String(), "child.yml") || !strings.Contains(page.Body.String(), child.Run.ID) {
		t.Fatalf("page = %s", page.Body.String())
	}
}
