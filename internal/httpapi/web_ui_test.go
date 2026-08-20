package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestWebExecutionTelemetryFiltersAndLazyLogRoute(t *testing.T) {
	fixture := newAPIFixture(t, DefaultMaxBodyBytes)
	cookie, _ := fixture.login(t)
	runs := webRequest(fixture, http.MethodGet, "/app/runs?range=7d&status=failed", nil, cookie, true)
	if runs.Code != http.StatusOK {
		t.Fatalf("runs status = %d, body=%s", runs.Code, runs.Body.String())
	}
	for _, expected := range []string{"EXECUTION SIGNAL", "Time range", "value=\"7d\" class=\"active\"", "name=\"status\""} {
		if !strings.Contains(runs.Body.String(), expected) {
			t.Fatalf("runs body missing %q", expected)
		}
	}
	missing := webRequest(fixture, http.MethodGet, "/app/runs/missing/steps/missing/logs", nil, cookie, true)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing logs status = %d", missing.Code)
	}
}
