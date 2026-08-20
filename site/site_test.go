package site

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesEmbeddedLandingPage(t *testing.T) {
	response := request(t, Handler(), http.MethodGet, "/")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("Cache-Control = %q", cacheControl)
	}
	if !strings.Contains(response.Body.String(), "RUN CI") {
		t.Fatal("landing page body does not contain expected content")
	}
}

func TestHandlerServesStaticAssetsWithCachePolicy(t *testing.T) {
	tests := []struct {
		path         string
		contentType  string
		cacheControl string
		bodyContains string
	}{
		{
			path:         "/assets/app.css",
			contentType:  "text/css; charset=utf-8",
			cacheControl: "public, max-age=31536000, immutable",
			bodyContains: "--font-sans",
		},
		{
			path:         "/favicon.svg",
			contentType:  "image/svg+xml; charset=utf-8",
			cacheControl: "public, max-age=86400",
			bodyContains: "<svg",
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := request(t, Handler(), http.MethodGet, test.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cacheControl {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cacheControl)
			}
			if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
			}
			if !strings.Contains(response.Body.String(), test.bodyContains) {
				t.Fatalf("body does not contain %q", test.bodyContains)
			}
		})
	}
}

func TestHandlerRejectsUnknownUnsafeAndReservedPaths(t *testing.T) {
	tests := []string{
		"/missing",
		"/index.html",
		"/assets/missing.css",
		"/../index.html",
		"/%2e%2e/index.html",
		"/assets%2fapp.css",
		"/assets\\app.css",
		"/api",
		"/api/v1/runs",
		"/app",
		"/app/projects",
		"/login",
		"/health",
		"/healthz",
	}

	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			response := request(t, Handler(), http.MethodGet, target)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHandlerAllowsServiceRoutesWhenMountedAtRoot(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.Handle("/app/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	mux.Handle("/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	mux.Handle("/health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.Handle("/", Handler())

	for target, want := range map[string]int{
		"/api/v1/runs":  http.StatusNoContent,
		"/app/projects": http.StatusAccepted,
		"/login":        http.StatusCreated,
		"/health":       http.StatusOK,
	} {
		t.Run(target, func(t *testing.T) {
			response := request(t, mux, http.MethodGet, target)
			if response.Code != want {
				t.Fatalf("status = %d, want %d", response.Code, want)
			}
		})
	}
}

func TestHandlerOnlyAllowsGetAndHead(t *testing.T) {
	response := request(t, Handler(), http.MethodPost, "/")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
	if allow := response.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Fatalf("Allow = %q", allow)
	}

	head := request(t, Handler(), http.MethodHead, "/assets/app.css")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", head.Code, http.StatusOK)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d, want 0", head.Body.Len())
	}
}

func request(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}
