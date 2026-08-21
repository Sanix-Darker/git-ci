package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRendererEscapesNestedLogSections(t *testing.T) {
	renderer, err := New()
	if err != nil {
		t.Fatal(err)
	}
	line := &LogView{Sequence: 2, Stream: "STDOUT", Message: "<script>line</script>"}
	group := &LogGroupView{Provider: "gitlab", Name: "Build <unsafe>", Dot: "green", LineCount: 1, Entries: []LogEntryView{{Line: line}}}
	recorder := httptest.NewRecorder()
	renderer.RenderStepLogs(recorder, http.StatusOK, StepLogView{StepName: "Build", Terminal: true, Entries: []LogEntryView{{Group: group}}})
	body := recorder.Body.String()
	for _, expected := range []string{"log-group", "Build &lt;unsafe&gt;", "&lt;script&gt;line&lt;/script&gt;", "01 LINES"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "<script>line</script>") || strings.Contains(body, "<details class=\"log-group\" open") {
		t.Fatalf("unsafe or incorrectly open group: %s", body)
	}
}
