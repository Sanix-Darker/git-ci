package auth

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecretFilesUseMode0600(t *testing.T) {
	manager, bootstrapToken, tokenPath, keyPath := newTestManager(t)
	if manager == nil {
		t.Fatal("NewManager returned a nil manager")
	}
	if bootstrapToken == "" {
		t.Fatal("NewManager did not return the created bootstrap token")
	}

	assertMode0600(t, tokenPath)
	assertMode0600(t, keyPath)

	if err := os.Chmod(tokenPath, 0o644); err != nil {
		t.Fatalf("chmod token: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod key: %v", err)
	}
	if _, _, err := NewManager(tokenPath, keyPath); err != nil {
		t.Fatalf("reloading manager: %v", err)
	}
	assertMode0600(t, tokenPath)
	assertMode0600(t, keyPath)
}

func TestExistingTokenIsReusedWithoutBeingReturned(t *testing.T) {
	_, bootstrapToken, tokenPath, keyPath := newTestManager(t)
	if bootstrapToken == "" {
		t.Fatal("expected initial bootstrap token")
	}

	manager, repeatedBootstrapToken, err := NewManager(tokenPath, keyPath)
	if err != nil {
		t.Fatalf("NewManager reload: %v", err)
	}
	if repeatedBootstrapToken != "" {
		t.Fatalf("reload returned a stored token: %q", repeatedBootstrapToken)
	}
	if _, err := manager.AuthenticateBearer(bootstrapToken); err != nil {
		t.Fatalf("existing token was not accepted: %v", err)
	}
}

func TestBearerAuthenticationAcceptsAndRejectsTokens(t *testing.T) {
	manager, bootstrapToken, _, _ := newTestManager(t)

	acceptedRequest := httptest.NewRequest(http.MethodPost, "http://git-ci.test/", nil)
	acceptedRequest.Header.Set("Authorization", "Bearer "+bootstrapToken)
	principal, err := manager.Authenticate(acceptedRequest)
	if err != nil {
		t.Fatalf("authenticate valid bearer: %v", err)
	}
	if principal.Subject != AdminSubject || principal.Method != AuthMethodBearer {
		t.Fatalf("unexpected principal: %+v", principal)
	}

	rejectedRequest := httptest.NewRequest(http.MethodGet, "http://git-ci.test/", nil)
	rejectedRequest.Header.Set("Authorization", "Bearer not-the-token")
	_, err = manager.Authenticate(rejectedRequest)
	requireAuthCode(t, err, CodeInvalidBearer)
}

func TestSessionCookieRejectsTamperingAndExpiry(t *testing.T) {
	current := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	manager, _, _, _ := newTestManager(t,
		WithClock(func() time.Time { return current }),
		WithSessionTTL(time.Hour),
	)
	_, cookie := issueSession(t, manager, httptest.NewRequest(http.MethodGet, "http://git-ci.test/", nil))

	tampered := *cookie
	parts := strings.Split(tampered.Value, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected cookie format: %q", tampered.Value)
	}
	if parts[1][0] == 'A' {
		parts[1] = "B" + parts[1][1:]
	} else {
		parts[1] = "A" + parts[1][1:]
	}
	tampered.Value = strings.Join(parts, ".")
	_, err := manager.Authenticate(requestWithCookie(http.MethodGet, &tampered))
	requireAuthCode(t, err, CodeInvalidSession)

	current = current.Add(time.Hour)
	_, err = manager.Authenticate(requestWithCookie(http.MethodGet, cookie))
	requireAuthCode(t, err, CodeExpiredSession)
}

func TestIssuedCookieHasRequiredFlagsAndSecureDetection(t *testing.T) {
	manager, _, _, _ := newTestManager(t)

	testCases := []struct {
		name    string
		request *http.Request
		secure  bool
	}{
		{
			name:    "http",
			request: httptest.NewRequest(http.MethodGet, "http://git-ci.test/", nil),
			secure:  false,
		},
		{
			name: "tls",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "https://git-ci.test/", nil)
				request.TLS = &tls.ConnectionState{}
				return request
			}(),
			secure: true,
		},
		{
			name: "forwarded https",
			request: func() *http.Request {
				request := httptest.NewRequest(http.MethodGet, "http://git-ci.test/", nil)
				request.Header.Set("X-Forwarded-Proto", "https")
				return request
			}(),
			secure: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, cookie := issueSession(t, manager, testCase.request)
			if cookie.Secure != testCase.secure {
				t.Fatalf("Secure = %t, want %t", cookie.Secure, testCase.secure)
			}
			if !cookie.HttpOnly {
				t.Fatal("cookie is not HttpOnly")
			}
			if cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("SameSite = %v, want Strict", cookie.SameSite)
			}
			if cookie.Expires.IsZero() || cookie.MaxAge <= 0 {
				t.Fatal("cookie does not expire")
			}
		})
	}
}

func TestCookieCSRFEnforcementAndBearerExemption(t *testing.T) {
	manager, bootstrapToken, _, _ := newTestManager(t)
	session, cookie := issueSession(t, manager, httptest.NewRequest(http.MethodGet, "http://git-ci.test/", nil))

	missingCSRF := requestWithCookie(http.MethodPost, cookie)
	_, err := manager.Authenticate(missingCSRF)
	requireAuthCode(t, err, CodeCSRF)

	wrongCSRF := requestWithCookie(http.MethodPost, cookie)
	wrongCSRF.Header.Set("X-CSRF-Token", "wrong")
	_, err = manager.Authenticate(wrongCSRF)
	requireAuthCode(t, err, CodeCSRF)

	matchingCSRF := requestWithCookie(http.MethodPost, cookie)
	matchingCSRF.Header.Set("X-CSRF-Token", session.CSRFToken)
	principal, err := manager.Authenticate(matchingCSRF)
	if err != nil {
		t.Fatalf("authenticate matching csrf token: %v", err)
	}
	if principal.Method != AuthMethodSession {
		t.Fatalf("method = %q, want %q", principal.Method, AuthMethodSession)
	}

	bearerRequest := httptest.NewRequest(http.MethodPost, "http://git-ci.test/", nil)
	bearerRequest.Header.Set("Authorization", "Bearer "+bootstrapToken)
	principal, err = manager.Authenticate(bearerRequest)
	if err != nil {
		t.Fatalf("bearer request unexpectedly required csrf: %v", err)
	}
	if principal.Method != AuthMethodBearer {
		t.Fatalf("method = %q, want %q", principal.Method, AuthMethodBearer)
	}
}

func TestCookieMethodSafety(t *testing.T) {
	manager, _, _, _ := newTestManager(t)
	_, cookie := issueSession(t, manager, httptest.NewRequest(http.MethodGet, "http://git-ci.test/", nil))

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			principal, err := manager.Authenticate(requestWithCookie(method, cookie))
			if err != nil {
				t.Fatalf("safe method rejected: %v", err)
			}
			if principal.Method != AuthMethodSession {
				t.Fatalf("method = %q, want %q", principal.Method, AuthMethodSession)
			}
		})
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect} {
		t.Run(method, func(t *testing.T) {
			_, err := manager.Authenticate(requestWithCookie(method, cookie))
			requireAuthCode(t, err, CodeCSRF)
		})
	}
}

func newTestManager(t *testing.T, options ...Option) (*Manager, string, string, string) {
	t.Helper()
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "admin.token")
	keyPath := filepath.Join(directory, "session.key")
	manager, bootstrapToken, err := NewManager(tokenPath, keyPath, options...)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return manager, bootstrapToken, tokenPath, keyPath
}

func issueSession(t *testing.T, manager *Manager, request *http.Request) (Session, *http.Cookie) {
	t.Helper()
	recorder := httptest.NewRecorder()
	session, err := manager.IssueSession(recorder, request)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	response := recorder.Result()
	t.Cleanup(func() { _ = response.Body.Close() })
	for _, cookie := range response.Cookies() {
		if cookie.Name == manager.CookieName() {
			return session, cookie
		}
	}
	t.Fatalf("session cookie %q was not set", manager.CookieName())
	return Session{}, nil
}

func requestWithCookie(method string, cookie *http.Cookie) *http.Request {
	request := httptest.NewRequest(method, "http://git-ci.test/", nil)
	request.AddCookie(cookie)
	return request
}

func assertMode0600(t *testing.T, path string) {
	t.Helper()
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if permissions := fileInfo.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("permissions for %s = %o, want 600", path, permissions)
	}
}

func requireAuthCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected authentication error %q, got nil", code)
	}
	var authError *AuthError
	if !errors.As(err, &authError) {
		t.Fatalf("error %v is not an AuthError", err)
	}
	if authError.Code != code {
		t.Fatalf("auth error code = %q, want %q", authError.Code, code)
	}
}
