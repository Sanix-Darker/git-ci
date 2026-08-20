package httpapi

import (
	"fmt"
	"mime"
	"net/http"
	"strings"
)

func (a *API) handleRunArtifacts(writer http.ResponseWriter, request *http.Request) {
	items, err := a.store.ListRunArtifacts(request.Context(), request.PathValue("run"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to list run artifacts")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleRunTestReports(writer http.ResponseWriter, request *http.Request) {
	items, err := a.store.ListRunTestReports(request.Context(), request.PathValue("run"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to list run test reports")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleProjectCaches(writer http.ResponseWriter, request *http.Request) {
	items, err := a.store.ListProjectCaches(request.Context(), request.PathValue("project"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to list project caches")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (a *API) handleArtifactDownload(writer http.ResponseWriter, request *http.Request) {
	artifact, file, err := a.execution.OpenRunArtifact(request.Context(), request.PathValue("run"), request.PathValue("artifact"))
	if err != nil {
		a.writeStoreError(writer, err, "failed to open artifact")
		return
	}
	defer file.Close()
	writer.Header().Set("Content-Type", "application/zip")
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", artifact.SizeBytes))
	writer.Header().Set("Digest", "sha-256="+artifact.SHA256)
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeDownloadName(artifact.Name) + ".zip"}))
	http.ServeContent(writer, request, artifact.Name+".zip", artifact.CreatedAt, file)
}

func safeDownloadName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, strings.TrimSpace(value))
	if value == "" {
		return "artifact"
	}
	return value
}

func formatOutputBytes(value int64) string {
	switch {
	case value >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(value)/float64(1<<30))
	case value >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(value)/float64(1<<20))
	case value >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(value)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", value)
	}
}
