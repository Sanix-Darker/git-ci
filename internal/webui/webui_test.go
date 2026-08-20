package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRendererBuildsComponentPagesAndEscapesData(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := httptest.NewRecorder()
	renderer.RenderApp(recorder, http.StatusOK, PageData{
		Page:        "settings",
		Title:       "Settings",
		Kicker:      "Service policy",
		Description: "<script>unsafe</script>",
		Actor:       "gci:admin",
		CSRFToken:   "csrf-token",
	}, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"app-frame", "Workflows", "csrf-token", "&lt;script&gt;unsafe&lt;/script&gt;"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q", expected)
		}
	}
	if strings.Contains(body, "<script>unsafe</script>") {
		t.Fatal("renderer did not escape page data")
	}
}

func TestEmbeddedAssetCachePolicy(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := httptest.NewRecorder()
	renderer.Assets().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/htmx.min.js", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset status = %d, cache=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
}
