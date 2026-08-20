// Package webui renders the git-ci operator interface as server-owned HTML.
package webui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/projects"
	"github.com/sanix-darker/git-ci/internal/store"
)

//go:embed templates/*.html assets/*
var embedded embed.FS

type PageData struct {
	Page        string
	Title       string
	Kicker      string
	Description string
	Actor       string
	CSRFToken   string
	Version     string
	Error       string
	Projects    []store.Project
	Candidates  []projects.Project
}

type Renderer struct {
	templates *template.Template
	assets    http.Handler
}

func New() (*Renderer, error) {
	functions := template.FuncMap{
		"base":  filepath.Base,
		"pad2":  func(value int) string { return fmt.Sprintf("%02d", value) },
		"upper": strings.ToUpper,
		"itoa":  strconv.Itoa,
	}
	parsed, err := template.New("git-ci").Funcs(functions).ParseFS(embedded, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("webui: parse templates: %w", err)
	}
	assetFS, err := fs.Sub(embedded, "assets")
	if err != nil {
		return nil, fmt.Errorf("webui: asset filesystem: %w", err)
	}
	return &Renderer{
		templates: parsed,
		assets:    immutableAssets(http.FileServer(http.FS(assetFS))),
	}, nil
}

func (r *Renderer) Assets() http.Handler {
	return r.assets
}

func (r *Renderer) RenderLogin(writer http.ResponseWriter, status int, data PageData) {
	r.render(writer, status, "login", data)
}

func (r *Renderer) RenderLoginFeedback(writer http.ResponseWriter, status int, message string) {
	r.render(writer, status, "login_feedback", PageData{Error: message})
}

func (r *Renderer) RenderApp(writer http.ResponseWriter, status int, data PageData, fragment bool) {
	name := "app"
	if fragment {
		name = "app_frame"
	}
	r.render(writer, status, name, data)
}

func (r *Renderer) render(writer http.ResponseWriter, status int, name string, data PageData) {
	var output bytes.Buffer
	if err := r.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(writer, "template rendering failed", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(output.Bytes())
}

func immutableAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(writer, request)
	})
}
