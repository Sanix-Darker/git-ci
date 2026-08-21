package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuditAPIAndResponsiveWebLedger(t *testing.T) {
	fixture := newAPIFixture(t, 1<<20)
	unauthorized := fixture.request(t, http.MethodGet, "/api/v1/audit", nil, "", nil, "", nil)
	assertAPIError(t, unauthorized, http.StatusUnauthorized, "missing_credentials")
	cookie, _ := fixture.login(t)
	response := fixture.request(t, http.MethodGet, "/api/v1/audit?range=24h&q=session.login&limit=10", nil, fixture.token, nil, "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"action":"session.login"`) || !strings.Contains(response.Body.String(), `"buckets"`) {
		t.Fatalf("audit API status = %d, body=%s", response.Code, response.Body.String())
	}
	invalid := fixture.request(t, http.MethodGet, "/api/v1/audit?range=forever", nil, fixture.token, nil, "", nil)
	assertAPIError(t, invalid, http.StatusBadRequest, "invalid_audit_filter")
	page := fixture.request(t, http.MethodGet, "/app/audit?range=24h&q=session.login", nil, "", cookie, "", nil)
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, "AUDIT") || !strings.Contains(body, "Audit event histogram") || !strings.Contains(body, "session.login") || !strings.Contains(body, "Audit event ledger") {
		t.Fatalf("audit page status = %d, body=%s", page.Code, body)
	}
}
