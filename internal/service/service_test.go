package service

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
)

func TestServicePersistsProjectsAndCredentialsAcrossRestart(t *testing.T) {
	config, projectPath := testConfig(t)
	first, err := New(context.Background(), config)
	if err != nil {
		t.Fatalf("new first service: %v", err)
	}
	token := first.BootstrapToken()
	if token == "" {
		t.Fatal("first service did not generate bootstrap token")
	}
	create := serviceRequest(t, first.Handler(), http.MethodPost, "/api/v1/projects", map[string]any{
		"slug": "persisted", "path": projectPath,
	}, token)
	if create.Code != http.StatusCreated {
		first.Close()
		t.Fatalf("create status = %d, body=%s", create.Code, create.Body.String())
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first service: %v", err)
	}

	second, err := New(context.Background(), config)
	if err != nil {
		t.Fatalf("new second service: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if second.BootstrapToken() != "" {
		t.Fatal("restarted service exposed existing bootstrap token")
	}
	listed := serviceRequest(t, second.Handler(), http.MethodGet, "/api/v1/projects", nil, token)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"slug":"persisted"`) {
		t.Fatalf("persisted list status = %d, body=%s", listed.Code, listed.Body.String())
	}
	for _, name := range []string{"admin.token", "session.key"} {
		info, err := os.Stat(filepath.Join(config.StateDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
	stateInfo, err := os.Stat(config.StateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if stateInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %o, want 700", stateInfo.Mode().Perm())
	}
}

func TestServiceRejectsPublicListenAddresses(t *testing.T) {
	config, _ := testConfig(t)
	for _, address := range []string{":8087", "0.0.0.0:8087", "192.0.2.1:8087", "invalid"} {
		config.Listen = address
		if _, err := New(context.Background(), config); err == nil {
			t.Fatalf("New accepted public/invalid listen address %q", address)
		}
	}
}

func TestServiceGracefulShutdown(t *testing.T) {
	config, _ := testConfig(t)
	config.Listen = "127.0.0.1:0"
	control, err := New(context.Background(), config)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(func() { _ = control.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := control.Run(ctx); err != nil {
		t.Fatalf("graceful shutdown: %v", err)
	}
}

func testConfig(t *testing.T) (Config, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	projectPath := filepath.Join(root, "app")
	staticDir := filepath.Join(base, "site")
	for _, directory := range []string{filepath.Join(projectPath, ".git"), staticDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", directory, err)
		}
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("service-home"), 0o644); err != nil {
		t.Fatalf("write static index: %v", err)
	}
	return Config{
		Listen:       "127.0.0.1:8087",
		StateDir:     filepath.Join(base, "state"),
		StaticDir:    staticDir,
		ProjectRoots: []string{root},
		Version:      "test",
	}, projectPath
}

func serviceRequest(t *testing.T, handler http.Handler, method, path string, payload any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
