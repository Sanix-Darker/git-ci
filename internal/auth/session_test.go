package auth

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestCurrentSessionRecoversBrowserCSRFState(t *testing.T) {
	base := t.TempDir()
	manager, _, err := NewManager(
		filepath.Join(base, "admin.token"),
		filepath.Join(base, "session.key"),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	issuedResponse := httptest.NewRecorder()
	issuedRequest := httptest.NewRequest("POST", "https://gci.example.test/api/v1/session/login", nil)
	issued, err := manager.IssueSession(issuedResponse, issuedRequest)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	cookies := issuedResponse.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}

	request := httptest.NewRequest("GET", "https://gci.example.test/api/v1/session", nil)
	request.AddCookie(cookies[0])
	current, err := manager.CurrentSession(request)
	if err != nil {
		t.Fatalf("CurrentSession: %v", err)
	}
	if current.CSRFToken != issued.CSRFToken || !current.ExpiresAt.Equal(issued.ExpiresAt.UTC()) {
		t.Fatalf("CurrentSession = %#v, issued = %#v", current, issued)
	}

	request = httptest.NewRequest("GET", "https://gci.example.test/api/v1/session", nil)
	if _, err := manager.CurrentSession(request); err == nil {
		t.Fatal("CurrentSession accepted a request without a session cookie")
	}
}
