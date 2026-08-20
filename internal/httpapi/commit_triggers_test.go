package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestCommitTriggerVersionedAPI(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	created := fixture.request(t, http.MethodPost, "/api/v1/projects", map[string]any{
		"slug": "watched-project", "path": fixture.projectPath,
	}, fixture.token, nil, "", nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	projects, err := fixture.store.ListProjects(t.Context())
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects=%d err=%v", len(projects), err)
	}
	updated := fixture.request(t, http.MethodPut, "/api/v1/projects/"+projects[0].ID+"/commit-trigger", map[string]any{
		"ref": "main", "enabled": true,
	}, fixture.token, nil, "", nil)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"enabled":true`) || !strings.Contains(updated.Body.String(), `"lastCommitSha"`) {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	loaded := fixture.request(t, http.MethodGet, "/api/v1/projects/"+projects[0].ID+"/commit-trigger", nil, fixture.token, nil, "", nil)
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), `"ref":"main"`) {
		t.Fatalf("get status=%d body=%s", loaded.Code, loaded.Body.String())
	}
}
