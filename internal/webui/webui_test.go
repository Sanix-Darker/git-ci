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
	for _, expected := range []string{"app-frame", "Workflows", "csrf-token", "route-skeleton", "app.js", "includeIndicatorStyles", "&lt;script&gt;unsafe&lt;/script&gt;"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q", expected)
		}
	}
	if strings.Contains(body, "<script>unsafe</script>") {
		t.Fatal("renderer did not escape page data")
	}
}

func TestRendererBuildsLazyLogFragmentsAndEscapesOutput(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := httptest.NewRecorder()
	renderer.RenderStepLogs(recorder, http.StatusOK, StepLogView{
		RunID: "run-1", StepID: "step-1", StepName: "Test", Terminal: true,
		Logs: []LogView{{Sequence: 1, Stream: "STDOUT", Message: "<script>secret</script>"}},
	})
	body := recorder.Body.String()
	for _, expected := range []string{"Logs for Test", "aria-busy=\"false\"", "&lt;script&gt;secret&lt;/script&gt;"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "every 1s") || strings.Contains(body, "<script>secret</script>") {
		t.Fatalf("terminal fragment polls or failed escaping: %s", body)
	}
}

func TestEmbeddedAssetCachePolicy(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	recorder := httptest.NewRecorder()
	renderer.Assets().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset status = %d, cache=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
}
