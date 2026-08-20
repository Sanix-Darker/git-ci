// Package site serves git-ci's embedded public landing page.
package site

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// content contains every public file served by Handler.
//
//go:embed index.html favicon.svg assets/*
var content embed.FS

var publicFiles = map[string]filePolicy{
	"favicon.svg": {
		contentType:  "image/svg+xml; charset=utf-8",
		cacheControl: "public, max-age=86400",
	},
	"assets/app.css": {
		contentType:  "text/css; charset=utf-8",
		cacheControl: "public, max-age=31536000, immutable",
	},
}

type filePolicy struct {
	contentType  string
	cacheControl string
}

// FS returns the read-only embedded public-site filesystem.
func FS() fs.FS {
	return content
}

// Handler returns a public-root handler for the landing page and its static
// assets. It intentionally returns 404 for service-owned routes, allowing a
// caller to mount it at "/" alongside more-specific ServeMux routes.
func Handler() http.Handler {
	return http.HandlerFunc(serve)
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if isReservedRoute(r.URL.Path) || unsafePath(r.URL.Path, r.URL.EscapedPath()) {
		http.NotFound(w, r)
		return
	}

	name, policy, ok := requestedFile(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := content.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", policy.contentType)
	w.Header().Set("Cache-Control", policy.cacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func requestedFile(requestPath string) (string, filePolicy, bool) {
	if requestPath == "/" {
		return "index.html", filePolicy{
			contentType:  "text/html; charset=utf-8",
			cacheControl: "no-cache",
		}, true
	}

	name := strings.TrimPrefix(requestPath, "/")
	policy, ok := publicFiles[name]
	if !ok {
		return "", filePolicy{}, false
	}
	if policy.contentType == "" {
		policy.contentType = mime.TypeByExtension(path.Ext(name))
	}
	return name, policy, true
}

func isReservedRoute(requestPath string) bool {
	for _, reserved := range []string{"/api", "/app", "/login", "/health"} {
		if requestPath == reserved || strings.HasPrefix(requestPath, reserved+"/") {
			return true
		}
	}
	return false
}

func unsafePath(requestPath, escapedPath string) bool {
	if !strings.HasPrefix(requestPath, "/") || strings.Contains(requestPath, "\\") {
		return true
	}
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}

	escapedPath = strings.ToLower(escapedPath)
	return strings.Contains(escapedPath, "%2e") ||
		strings.Contains(escapedPath, "%2f") ||
		strings.Contains(escapedPath, "%5c")
}
