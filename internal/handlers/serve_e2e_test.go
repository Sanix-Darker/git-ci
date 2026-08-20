package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func doRawRequest(t *testing.T, handler http.Handler, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		switch value := body.(type) {
		case io.Reader:
			reader = value
		default:
			payload, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
			reader = bytes.NewReader(payload)
		}
	}

	req := httptest.NewRequest(method, target, reader)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestE2E_StaticRoutingAndClientFallback(t *testing.T) {
	state := newServeStateForTest(t)
	mux := buildServeMuxForTests(state)

	root := doRawRequest(t, mux, http.MethodGet, "/", nil, nil)
	if root.Code != http.StatusOK {
		t.Fatalf("root should serve dashboard, status=%d", root.Code)
	}
	if !strings.Contains(root.Body.String(), "Your single-vps replacement for hosted CI/CD runners.") {
		t.Fatalf("dashboard html missing expected content")
	}
	if !strings.Contains(root.Body.String(), "GitHub Actions") {
		t.Fatalf("dashboard should expose GitHub Actions section")
	}
	if !strings.Contains(root.Body.String(), "Workflow lookup") {
		t.Fatalf("dashboard should expose workflow lookup card")
	}
	if !strings.Contains(root.Body.String(), "Get by name") {
		t.Fatalf("dashboard should expose secret lookup action")
	}
	if !strings.Contains(root.Body.String(), "Auto-scroll ON") {
		t.Fatalf("dashboard should expose log auto-scroll control")
	}

	spaRoute := doRawRequest(t, mux, http.MethodGet, "/runs/flow-graph", nil, nil)
	if spaRoute.Code != http.StatusOK {
		t.Fatalf("client-side route should return SPA shell, status=%d", spaRoute.Code)
	}
	if !strings.Contains(spaRoute.Body.String(), "GitHub Actions") {
		t.Fatalf("SPA shell should be returned for unknown client route")
	}

	assetWithDot := doRawRequest(t, mux, http.MethodGet, "/foo.bar", nil, nil)
	if assetWithDot.Code != http.StatusNotFound {
		t.Fatalf("dot-asset path should be blocked by static policy: status=%d", assetWithDot.Code)
	}
}

func TestE2E_FeatureContractJourneyAcrossVersions(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	for _, rootPath := range []string{"/api", "/api/v1"} {
		rootResponse := doJSONRequest(t, mux, http.MethodGet, rootPath, nil, nil)
		if rootResponse.Code != http.StatusOK {
			t.Fatalf("%s root response status=%d body=%s", rootPath, rootResponse.Code, rootResponse.Body.String())
		}

		var rootPayload map[string]any
		decodeJSONResponse(t, rootResponse, &rootPayload)

		routesValue, ok := rootPayload["routes"].(map[string]any)
		if !ok {
			t.Fatalf("expected routes object at %s", rootPath)
		}

		if _, ok := routesValue["workflows"]; !ok {
			t.Fatalf("missing /workflows route in %s root", rootPath)
		}
		if _, ok := routesValue["runs"]; !ok {
			t.Fatalf("missing /runs route in %s root", rootPath)
		}
		if _, ok := routesValue["runById"]; !ok {
			t.Fatalf("missing /runs/{id} route in %s root", rootPath)
		}
		if _, ok := routesValue["runLogs"]; !ok {
			t.Fatalf("missing /runs/{id}/logs route in %s root", rootPath)
		}
		if _, ok := routesValue["runRetry"]; !ok {
			t.Fatalf("missing /runs/{id}/retry route in %s root", rootPath)
		}
		if _, ok := routesValue["runCancel"]; !ok {
			t.Fatalf("missing /runs/{id}/cancel route in %s root", rootPath)
		}
		if _, ok := routesValue["features"]; !ok {
			t.Fatalf("missing /features route in %s root", rootPath)
		}
		if _, ok := routesValue["secrets"]; !ok {
			t.Fatalf("missing /secrets route in %s root", rootPath)
		}
		if _, ok := routesValue["cronRuns"]; !ok {
			t.Fatalf("missing /cron-runs route in %s root", rootPath)
		}
	}

	features := doJSONRequest(t, mux, http.MethodGet, "/api/v1/features", nil, nil)
	var catalog featureCatalogResponse
	decodeJSONResponse(t, features, &catalog)
	if !catalog.OK || catalog.Version != "v1" {
		t.Fatalf("invalid features catalog payload: %#v", catalog)
	}
	if _, ok := catalog.Capabilities["workflows"]; !ok {
		t.Fatalf("workflow feature missing from catalog")
	}
	if _, ok := catalog.Capabilities["github-actions"]; !ok {
		t.Fatalf("github-actions feature missing from catalog")
	}

	workflowRun := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"file":    "ci.yml",
		"workdir": state.defaultWorkdir,
	}, nil)
	if workflowRun.Code != http.StatusCreated {
		t.Fatalf("workflow execution should be accepted: status=%d body=%s", workflowRun.Code, workflowRun.Body.String())
	}

	ghActions := doJSONRequest(t, mux, http.MethodGet, "/api/v1/features/github-actions", nil, nil)
	var ghaContract featurePlanResponse
	decodeJSONResponse(t, ghActions, &ghaContract)
	if ghaContract.Feature != "github-actions" {
		t.Fatalf("unexpected github-actions route contract: %#v", ghaContract)
	}
	if len(ghaContract.Plan.Endpoints) == 0 {
		t.Fatalf("github-actions contract endpoints should be advertised")
	}

	unknown := doRawRequest(t, mux, http.MethodGet, "/api/v1/features/does-not-exist", nil, nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("expected unknown feature to return 404, got %d", unknown.Code)
	}

	unsupportedMethod := doRawRequest(t, mux, http.MethodPost, "/api/v1/features", nil, nil)
	if unsupportedMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("catalog root supports only GET: status=%d", unsupportedMethod.Code)
	}

	subroute := doRawRequest(t, mux, http.MethodGet, "/api/v1/features/workflows/child", nil, nil)
	if subroute.Code != http.StatusNotFound {
		t.Fatalf("expected nested feature route to return 404, got %d", subroute.Code)
	}

	accepted := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "GITHUB_TOKEN",
		"value": "planned-only",
	}, nil)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("expected secret write acceptance: status=%d body=%s", accepted.Code, accepted.Body.String())
	}
}

func TestE2E_GitHubActionsFeatureSurface(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	secret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "token",
		"value": "surface-secret",
		"scope": "project",
	}, nil)
	if secret.Code != http.StatusAccepted {
		t.Fatalf("secret create status=%d body=%s", secret.Code, secret.Body.String())
	}

	var secretPayload map[string]any
	decodeJSONResponse(t, secret, &secretPayload)
	if secretPayload["name"] != "TOKEN" {
		t.Fatalf("expected normalized secret name, got %#v", secretPayload["name"])
	}

	workflow := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"file":       "ci.yml",
		"workdir":    state.defaultWorkdir,
		"secretRefs": []string{"project:token"},
		"repository": "owner/repo",
	}, nil)
	if workflow.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch status=%d body=%s", workflow.Code, workflow.Body.String())
	}

	var workflowPayload map[string]any
	decodeJSONResponse(t, workflow, &workflowPayload)
	if workflowPayload["file"] != "ci.yml" {
		t.Fatalf("workflow response file mismatch: %#v", workflowPayload)
	}
	secrets := workflowPayload["secretRefs"]
	secretRefs, ok := secrets.([]any)
	if !ok || len(secretRefs) != 1 || secretRefs[0] != "project:token" {
		t.Fatalf("workflow run should carry secret refs: %#v", secrets)
	}

	cron := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "surface",
		"workflowFile": "ci.yml",
		"interval":     "3m",
		"workdir":      state.defaultWorkdir,
		"secretRefs":   []string{"project:token"},
	}, nil)
	if cron.Code != http.StatusCreated {
		t.Fatalf("cron create status=%d body=%s", cron.Code, cron.Body.String())
	}

	var cronPayload map[string]any
	decodeJSONResponse(t, cron, &cronPayload)
	id, ok := cronPayload["id"].(string)
	if !ok || id == "" {
		t.Fatalf("cron id expected, got %#v", cronPayload["id"])
	}

	trig := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+id+"/run", nil, nil)
	if trig.Code != http.StatusCreated {
		t.Fatalf("cron run trigger status=%d body=%s", trig.Code, trig.Body.String())
	}

	workflowID := encodeWorkflowID("ci.yml")
	workflowByID := doJSONRequest(t, mux, http.MethodGet, "/api/v1/workflows/"+url.PathEscape(workflowID), nil, nil)
	if workflowByID.Code != http.StatusOK {
		t.Fatalf("workflow lookup status=%d body=%s", workflowByID.Code, workflowByID.Body.String())
	}
	var workflowByIDPayload map[string]any
	decodeJSONResponse(t, workflowByID, &workflowByIDPayload)
	if workflowByIDPayload["file"] != "ci.yml" {
		t.Fatalf("workflow lookup should return file path, got %#v", workflowByIDPayload["file"])
	}

	cronRuns := doJSONRequest(t, mux, http.MethodGet, "/api/v1/cron-runs", nil, nil)
	if cronRuns.Code != http.StatusOK {
		t.Fatalf("cron list status=%d body=%s", cronRuns.Code, cronRuns.Body.String())
	}
	var cronList []map[string]any
	decodeJSONResponse(t, cronRuns, &cronList)
	if len(cronList) == 0 {
		t.Fatalf("cron list should include the created run")
	}

	secretList := doJSONRequest(t, mux, http.MethodGet, "/api/v1/secrets", nil, nil)
	if secretList.Code != http.StatusOK {
		t.Fatalf("secret list status=%d body=%s", secretList.Code, secretList.Body.String())
	}
	var listPayload map[string]any
	decodeJSONResponse(t, secretList, &listPayload)
	if count, ok := listPayload["count"].(float64); !ok || count < 1 {
		t.Fatalf("expected at least one secret in listing, got %#v", listPayload["count"])
	}
	if _, ok := listPayload["items"].([]any); !ok {
		t.Fatalf("expected secret list payload items")
	}
}

func TestE2E_GitHubActionsControlPlaneLifecycle(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	secret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "TOKEN",
		"value": "surface-secret",
		"scope": "project",
	}, nil)
	if secret.Code != http.StatusAccepted {
		t.Fatalf("secret create status=%d body=%s", secret.Code, secret.Body.String())
	}

	var secretPayload map[string]any
	decodeJSONResponse(t, secret, &secretPayload)
	if secretPayload["name"] != "TOKEN" {
		t.Fatalf("secret should be normalized name token")
	}

	workflow := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"file":       ".github/workflows/ci.yml",
		"workdir":    state.defaultWorkdir,
		"secretRefs": []string{"project:token"},
	}, nil)
	if workflow.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch status=%d body=%s", workflow.Code, workflow.Body.String())
	}

	var workflowPayload map[string]any
	decodeJSONResponse(t, workflow, &workflowPayload)
	if workflowPayload["file"] != ".github/workflows/ci.yml" {
		t.Fatalf("workflow response file mismatch: %#v", workflowPayload["file"])
	}

	cron := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "surface",
		"workflowFile": ".github/workflows/ci.yml",
		"interval":     "1m",
		"workdir":      state.defaultWorkdir,
	}, nil)
	if cron.Code != http.StatusCreated {
		t.Fatalf("cron create status=%d body=%s", cron.Code, cron.Body.String())
	}

	var cronPayload map[string]any
	decodeJSONResponse(t, cron, &cronPayload)
	id, ok := cronPayload["id"].(string)
	if !ok || id == "" {
		t.Fatalf("expected cron id, got %#v", cronPayload["id"])
	}

	pause := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+id+"/pause?reason=ui", nil, nil)
	if pause.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", pause.Code, pause.Body.String())
	}
	var paused map[string]any
	decodeJSONResponse(t, pause, &paused)
	if paused["status"] != "paused" {
		t.Fatalf("pause response status mismatch: %#v", paused["status"])
	}

	triggerWhilePaused := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+id+"/run", nil, nil)
	if triggerWhilePaused.Code != http.StatusBadRequest {
		t.Fatalf("expected paused trigger to return 400, got %d", triggerWhilePaused.Code)
	}

	resume := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+id+"/resume", nil, nil)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	var resumed map[string]any
	decodeJSONResponse(t, resume, &resumed)
	if resumed["status"] != "active" {
		t.Fatalf("resume response status mismatch: %#v", resumed["status"])
	}

	trigger := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+id+"/run", nil, nil)
	if trigger.Code != http.StatusCreated {
		t.Fatalf("cron trigger status=%d body=%s", trigger.Code, trigger.Body.String())
	}
	var triggerPayload map[string]any
	decodeJSONResponse(t, trigger, &triggerPayload)
	if triggerPayload["ok"] != true {
		t.Fatalf("trigger response should be ok")
	}
	runID, _ := triggerPayload["runId"].(string)
	if runID == "" {
		t.Fatalf("expected runId in trigger payload")
	}

	cronList := doJSONRequest(t, mux, http.MethodGet, "/api/v1/cron-runs", nil, nil)
	if cronList.Code != http.StatusOK {
		t.Fatalf("cron list status=%d body=%s", cronList.Code, cronList.Body.String())
	}
	var cronListPayload []map[string]any
	decodeJSONResponse(t, cronList, &cronListPayload)
	if len(cronListPayload) == 0 {
		t.Fatalf("expected cron run in list")
	}

	secretList := doJSONRequest(t, mux, http.MethodGet, "/api/v1/secrets?scope=project", nil, nil)
	if secretList.Code != http.StatusOK {
		t.Fatalf("secret list status=%d body=%s", secretList.Code, secretList.Body.String())
	}
	var secretListPayload map[string]any
	decodeJSONResponse(t, secretList, &secretListPayload)
	if count, ok := secretListPayload["count"].(float64); !ok || count < 1 {
		t.Fatalf("expected at least one project secret")
	}

	run := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID, nil, nil)
	if run.Code != http.StatusOK {
		t.Fatalf("run lookup status=%d body=%s", run.Code, run.Body.String())
	}
}

func TestE2E_GitHubActionsFullControlSurface(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	catalog := doJSONRequest(t, mux, http.MethodGet, "/api/v1/features", nil, nil)
	if catalog.Code != http.StatusOK {
		t.Fatalf("features catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}

	var catalogPayload featureCatalogResponse
	decodeJSONResponse(t, catalog, &catalogPayload)

	for _, feature := range []string{"workflows", "secrets", "cron-runs", "github-actions"} {
		plan, ok := catalogPayload.Capabilities[feature]
		if !ok {
			t.Fatalf("expected feature %q in catalog", feature)
		}
		if plan.Status == "" || len(plan.Endpoints) == 0 {
			t.Fatalf("feature %q should expose at least one endpoint", feature)
		}
	}

	featureContract := doJSONRequest(t, mux, http.MethodPost, "/api/v1/features/github-actions", map[string]any{
		"target": "ui-control-plane",
	}, nil)
	if featureContract.Code != http.StatusAccepted {
		t.Fatalf("feature contract submission status=%d body=%s", featureContract.Code, featureContract.Body.String())
	}

	createdSecret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "TOKEN",
		"value": "feature-secret",
		"scope": "project",
	}, nil)
	if createdSecret.Code != http.StatusAccepted {
		t.Fatalf("create secret status=%d body=%s", createdSecret.Code, createdSecret.Body.String())
	}
	var secretPayload map[string]any
	decodeJSONResponse(t, createdSecret, &secretPayload)
	if secretPayload["name"] != "TOKEN" {
		t.Fatalf("expected normalized secret name, got %#v", secretPayload["name"])
	}

	secretByName := doJSONRequest(t, mux, http.MethodGet, "/api/v1/secrets/TOKEN?scope=project", nil, nil)
	if secretByName.Code != http.StatusOK {
		t.Fatalf("get secret status=%d body=%s", secretByName.Code, secretByName.Body.String())
	}
	var secretGet map[string]any
	decodeJSONResponse(t, secretByName, &secretGet)
	if secretGet["revealed"] != false {
		t.Fatalf("secret should be masked by default, got %#v", secretGet["revealed"])
	}

	secretReveal := doJSONRequest(t, mux, http.MethodGet, "/api/v1/secrets/TOKEN?scope=project&reveal=1", nil, nil)
	if secretReveal.Code != http.StatusOK {
		t.Fatalf("reveal secret status=%d body=%s", secretReveal.Code, secretReveal.Body.String())
	}
	var secretRevealPayload map[string]any
	decodeJSONResponse(t, secretReveal, &secretRevealPayload)
	if secretRevealPayload["revealed"] != true {
		t.Fatalf("secret should be revealed when query asks for it")
	}

	workflow := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"file":       ".github/workflows/ci.yml",
		"workdir":    state.defaultWorkdir,
		"secretRefs": []string{"project:TOKEN"},
	}, nil)
	if workflow.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch status=%d body=%s", workflow.Code, workflow.Body.String())
	}
	var workflowPayload map[string]any
	decodeJSONResponse(t, workflow, &workflowPayload)
	if workflowPayload["file"] != ".github/workflows/ci.yml" {
		t.Fatalf("workflow response should preserve file path")
	}

	workflowID := encodeWorkflowID(".github/workflows/ci.yml")
	workflowByID := doJSONRequest(t, mux, http.MethodGet, "/api/v1/workflows/"+url.PathEscape(workflowID), nil, nil)
	if workflowByID.Code != http.StatusOK {
		t.Fatalf("workflow lookup status=%d body=%s", workflowByID.Code, workflowByID.Body.String())
	}
	var workflowByIDPayload map[string]any
	decodeJSONResponse(t, workflowByID, &workflowByIDPayload)
	if workflowByIDPayload["file"] != ".github/workflows/ci.yml" {
		t.Fatalf("workflow lookup should include matching workflow file")
	}

	workflowDispatch := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows/"+url.PathEscape(workflowID)+"/dispatch", map[string]any{
		"workdir": state.defaultWorkdir,
	}, nil)
	if workflowDispatch.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch by id status=%d body=%s", workflowDispatch.Code, workflowDispatch.Body.String())
	}

	cron := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "full-surface",
		"workflowFile": ".github/workflows/ci.yml",
		"interval":     "2m",
		"workdir":      state.defaultWorkdir,
	}, nil)
	if cron.Code != http.StatusCreated {
		t.Fatalf("create cron run status=%d body=%s", cron.Code, cron.Body.String())
	}
	var cronPayload map[string]any
	decodeJSONResponse(t, cron, &cronPayload)
	cronID, _ := cronPayload["id"].(string)
	if cronID == "" {
		t.Fatalf("expected cron id")
	}

	pause := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/pause?reason=full-surface", nil, nil)
	if pause.Code != http.StatusOK {
		t.Fatalf("pause cron status=%d body=%s", pause.Code, pause.Body.String())
	}

	runWhilePaused := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/run", nil, nil)
	if runWhilePaused.Code != http.StatusBadRequest {
		t.Fatalf("paused cron should return bad request, got=%d", runWhilePaused.Code)
	}

	resume := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/resume", nil, nil)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume cron status=%d body=%s", resume.Code, resume.Body.String())
	}

	trigger := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/run", nil, nil)
	if trigger.Code != http.StatusCreated {
		t.Fatalf("trigger cron run status=%d body=%s", trigger.Code, trigger.Body.String())
	}
	var triggerPayload map[string]any
	decodeJSONResponse(t, trigger, &triggerPayload)
	runID, _ := triggerPayload["runId"].(string)
	if runID == "" {
		t.Fatalf("expected run id from cron trigger")
	}

	runs := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs", nil, nil)
	if runs.Code != http.StatusOK {
		t.Fatalf("run listing status=%d body=%s", runs.Code, runs.Body.String())
	}
	var runPayloads []map[string]any
	decodeJSONResponse(t, runs, &runPayloads)
	foundRun := false
	for _, item := range runPayloads {
		if item["id"] == runID {
			foundRun = true
			break
		}
	}
	if !foundRun {
		t.Fatalf("expected triggered cron run %s to be present in /runs listing", runID)
	}
}

func TestE2E_RunLifecycleForPlannedActionsMetadata(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	runPayload := map[string]any{
		"workdir":       state.defaultWorkdir,
		"file":          "pipeline.yml",
		"repository":    "https://github.com/sanixdarker/git-ci-plan.git",
		"repositoryUrl": "https://github.com/sanixdarker/git-ci-plan.git",
		"ref":           "refs/heads/main",
		"autoFetch":     false,
		"maxLogEntries": 64,
	}

	create := doJSONRequest(t, mux, http.MethodPost, "/api/v1/runs", runPayload, nil)
	if create.Code != http.StatusCreated {
		t.Fatalf("run create status: %d body=%s", create.Code, create.Body.String())
	}

	var created map[string]any
	decodeJSONResponse(t, create, &created)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("expected run id in response")
	}
	if created["repository"] != runPayload["repository"] {
		t.Fatalf("expected repository to echo in run response")
	}
	if created["autoFetch"] != runPayload["autoFetch"] {
		t.Fatalf("expected autoFetch to preserve payload value")
	}
	if created["ref"] != runPayload["ref"] {
		t.Fatalf("expected ref to echo in run response")
	}

	list := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs", nil, nil)
	var sessions []map[string]any
	decodeJSONResponse(t, list, &sessions)
	found := false
	for _, session := range sessions {
		if session["id"] == runID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created run not found in run list payload")
	}

	details := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID, nil, nil)
	var run map[string]any
	decodeJSONResponse(t, details, &run)
	if run["id"] != runID {
		t.Fatalf("run id mismatch: got=%v expected=%s", run["id"], runID)
	}
	if run["status"] == "" {
		t.Fatalf("run status should be present")
	}

	logs := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID+"/logs?offset=0", nil, nil)
	var logPayload map[string]any
	decodeJSONResponse(t, logs, &logPayload)
	lines, hasLines := logPayload["lines"].([]any)
	if !hasLines || len(lines) == 0 {
		t.Fatalf("run logs should expose an initial log line")
	}

	retry := doJSONRequest(t, mux, http.MethodPost, "/api/v1/runs/"+runID+"/retry", map[string]any{}, map[string]string{
		"Content-Type": "application/json",
	})
	if retry.Code != http.StatusCreated {
		t.Fatalf("run retry status: %d body=%s", retry.Code, retry.Body.String())
	}
	var retried map[string]any
	decodeJSONResponse(t, retry, &retried)
	retriedID, _ := retried["id"].(string)
	if retriedID == "" {
		t.Fatalf("retry should return run id")
	}
	if retriedID == runID {
		t.Fatalf("retry should create a different run id")
	}
}

func TestE2E_WebhookFlowPersistsRunAndEventMetadata(t *testing.T) {
	state := newServeStateForTest(t)
	state.hookSecretGitHub = "test-webhook-secret"
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	rawPayload := map[string]any{
		"ref":   "refs/heads/main",
		"after": "a1b2c3",
		"repository": map[string]any{
			"full_name":    "sanixdarker/git-ci-plan",
			"html_url":     "https://github.com/sanixdarker/git-ci-plan",
			"clone_url":    "https://github.com/sanixdarker/git-ci-plan.git",
			"ssh_url":      "git@github.com:sanixdarker/git-ci-plan.git",
			"path_with_ns": "sanixdarker/git-ci-plan",
		},
	}
	payload, err := json.Marshal(rawPayload)
	if err != nil {
		t.Fatalf("marshal webhook payload: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(state.hookSecretGitHub))
	_, _ = mac.Write(payload)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	webhook := doRawRequest(t, mux, http.MethodPost, "/api/v1/webhook/github?workdir="+state.defaultWorkdir+"&autoFetch=false", bytes.NewReader(payload), map[string]string{
		"X-Hub-Signature-256": signature,
	})
	if webhook.Code != http.StatusAccepted {
		t.Fatalf("webhook request status=%d body=%s", webhook.Code, webhook.Body.String())
	}

	var webhookResponse map[string]any
	decodeJSONResponse(t, webhook, &webhookResponse)
	runID, _ := webhookResponse["runId"].(string)
	if runID == "" {
		t.Fatalf("expected runId in webhook response")
	}

	runResponse := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID, nil, nil)
	var run map[string]any
	decodeJSONResponse(t, runResponse, &run)
	if run["autoFetch"] != false {
		t.Fatalf("explicit autoFetch=false should be preserved from webhook query")
	}

	events := doJSONRequest(t, mux, http.MethodGet, "/api/v1/webhooks", nil, nil)
	var eventLog []map[string]any
	decodeJSONResponse(t, events, &eventLog)
	if len(eventLog) == 0 {
		t.Fatalf("expected webhook event to be logged")
	}
	if eventLog[0]["provider"] != "github" {
		t.Fatalf("expected github webhook provider in event log")
	}
	if eventLog[0]["runId"] == "" {
		t.Fatalf("event log should include runId for accepted webhook")
	}
}

func TestE2E_GitHubAndGitLabControlSurfaceJourney(t *testing.T) {
	state := newServeStateForTest(t)
	state.hookSecretGitHub = "gh-secret"
	state.hookSecretGitLab = "gl-secret"
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	health := doRawRequest(t, mux, http.MethodGet, "/health", nil, nil)
	if health.Code != http.StatusOK {
		t.Fatalf("health check failed: status=%d body=%s", health.Code, health.Body.String())
	}

	for _, rootPath := range []string{"/api", "/api/v1"} {
		root := doJSONRequest(t, mux, http.MethodGet, rootPath, nil, nil)
		if root.Code != http.StatusOK {
			t.Fatalf("%s root status=%d", rootPath, root.Code)
		}
		var rootPayload map[string]any
		decodeJSONResponse(t, root, &rootPayload)
		routes, ok := rootPayload["routes"].(map[string]any)
		if !ok {
			t.Fatalf("%s routes missing", rootPath)
		}
		for _, required := range []string{"workflows", "runs", "secrets", "cronRuns", "features", "webhooks"} {
			if _, ok := routes[required]; !ok {
				t.Fatalf("%s missing route key %s", rootPath, required)
			}
		}
	}

	globalSecret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "global_token",
		"value": "global-value",
	}, nil)
	if globalSecret.Code != http.StatusAccepted {
		t.Fatalf("global secret store failed: status=%d body=%s", globalSecret.Code, globalSecret.Body.String())
	}

	projectSecret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "token",
		"value": "project-value",
		"scope": "project",
	}, nil)
	if projectSecret.Code != http.StatusAccepted {
		t.Fatalf("project secret store failed: status=%d body=%s", projectSecret.Code, projectSecret.Body.String())
	}

	workflowRun := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"workdir":    state.defaultWorkdir,
		"file":       ".github/workflows/ci.yml",
		"secretRefs": []string{"project:TOKEN", "GLOBAL_TOKEN"},
	}, nil)
	if workflowRun.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch failed: status=%d body=%s", workflowRun.Code, workflowRun.Body.String())
	}
	var workflowPayload map[string]any
	decodeJSONResponse(t, workflowRun, &workflowPayload)
	runIDs := map[string]struct{}{}
	if runID, ok := workflowPayload["id"].(string); ok && runID != "" {
		runIDs[runID] = struct{}{}
	}

	workflowID := encodeWorkflowID(".github/workflows/ci.yml")
	workflowByID := doJSONRequest(t, mux, http.MethodGet, "/api/v1/workflows/"+url.PathEscape(workflowID), nil, nil)
	if workflowByID.Code != http.StatusOK {
		t.Fatalf("workflow lookup failed: status=%d body=%s", workflowByID.Code, workflowByID.Body.String())
	}
	var workflowByIDPayload map[string]any
	decodeJSONResponse(t, workflowByID, &workflowByIDPayload)
	if workflowByIDPayload["file"] != ".github/workflows/ci.yml" {
		t.Fatalf("workflow lookup should keep file path: %#v", workflowByIDPayload["file"])
	}

	workflowDispatch := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows/"+url.PathEscape(workflowID)+"/dispatch", map[string]any{
		"workdir": state.defaultWorkdir,
	}, nil)
	if workflowDispatch.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch by id failed: status=%d body=%s", workflowDispatch.Code, workflowDispatch.Body.String())
	}
	var workflowDispatchPayload map[string]any
	decodeJSONResponse(t, workflowDispatch, &workflowDispatchPayload)
	if runID, ok := workflowDispatchPayload["id"].(string); ok && runID != "" {
		runIDs[runID] = struct{}{}
	}

	cronCreate := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "control-surface",
		"workdir":      state.defaultWorkdir,
		"workflowFile": ".github/workflows/ci.yml",
		"interval":     "90s",
		"secretRefs":   []string{"project:TOKEN"},
	}, nil)
	if cronCreate.Code != http.StatusCreated {
		t.Fatalf("cron create failed: status=%d body=%s", cronCreate.Code, cronCreate.Body.String())
	}
	var cronPayload map[string]any
	decodeJSONResponse(t, cronCreate, &cronPayload)
	cronID, ok := cronPayload["id"].(string)
	if !ok || cronID == "" {
		t.Fatalf("expected cron id in response: %#v", cronPayload)
	}

	pause := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/pause?reason=ui", nil, nil)
	if pause.Code != http.StatusOK {
		t.Fatalf("pause cron failed: status=%d body=%s", pause.Code, pause.Body.String())
	}
	if trigger := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/run", nil, nil); trigger.Code != http.StatusBadRequest {
		t.Fatalf("paused cron should fail to run: status=%d body=%s", trigger.Code, trigger.Body.String())
	}

	resume := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/resume", nil, nil)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume cron failed: status=%d body=%s", resume.Code, resume.Body.String())
	}
	resumePayload := map[string]any{}
	decodeJSONResponse(t, resume, &resumePayload)
	if resumePayload["status"] != "active" {
		t.Fatalf("unexpected resume payload: %#v", resumePayload["status"])
	}

	trigger := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/run", nil, nil)
	if trigger.Code != http.StatusCreated {
		t.Fatalf("cron trigger failed: status=%d body=%s", trigger.Code, trigger.Body.String())
	}
	var triggerPayload map[string]any
	decodeJSONResponse(t, trigger, &triggerPayload)
	if runID, ok := triggerPayload["runId"].(string); ok && runID != "" {
		runIDs[runID] = struct{}{}
	}

	ghPayload := `{"ref":"refs/heads/main","after":"abc111","repository":{"full_name":"."}}`
	ghMac := hmac.New(sha256.New, []byte("gh-secret"))
	_, _ = ghMac.Write([]byte(ghPayload))
	ghSig := "sha256=" + hex.EncodeToString(ghMac.Sum(nil))
	ghResponse := doRawRequest(
		t,
		mux,
		http.MethodPost,
		"/api/v1/webhook/github?workdir="+state.defaultWorkdir+"&repository=.",
		bytes.NewBufferString(ghPayload),
		map[string]string{"X-Hub-Signature-256": ghSig},
	)
	if ghResponse.Code != http.StatusAccepted {
		t.Fatalf("github webhook failed: status=%d body=%s", ghResponse.Code, ghResponse.Body.String())
	}
	var ghAccepted map[string]any
	decodeJSONResponse(t, ghResponse, &ghAccepted)
	if runID, ok := ghAccepted["runId"].(string); ok && runID != "" {
		runIDs[runID] = struct{}{}
	}

	glPayload := `{"object_kind":"push","ref":"refs/heads/main","after":"def222","project":{"path_with_namespace":"."}}`
	glResponse := doRawRequest(
		t,
		mux,
		http.MethodPost,
		"/api/v1/webhook/gitlab?workdir="+state.defaultWorkdir+"&repository=.",
		bytes.NewBufferString(glPayload),
		map[string]string{"X-Gitlab-Token": "gl-secret"},
	)
	if glResponse.Code != http.StatusAccepted {
		t.Fatalf("gitlab webhook failed: status=%d body=%s", glResponse.Code, glResponse.Body.String())
	}
	var glAccepted map[string]any
	decodeJSONResponse(t, glResponse, &glAccepted)
	if runID, ok := glAccepted["runId"].(string); ok && runID != "" {
		runIDs[runID] = struct{}{}
	}

	runsResponse := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs", nil, nil)
	if runsResponse.Code != http.StatusOK {
		t.Fatalf("runs listing failed: status=%d body=%s", runsResponse.Code, runsResponse.Body.String())
	}
	var runs []map[string]any
	decodeJSONResponse(t, runsResponse, &runs)
	for runID := range runIDs {
		found := false
		for _, run := range runs {
			if run["id"] == runID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("run %s not found in /runs listing", runID)
		}
	}

	for runID := range runIDs {
		logs := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID+"/logs?offset=0", nil, nil)
		if logs.Code != http.StatusOK {
			t.Fatalf("run logs failed for %s: status=%d body=%s", runID, logs.Code, logs.Body.String())
		}
		var logPayload map[string]any
		decodeJSONResponse(t, logs, &logPayload)
		if _, ok := logPayload["lines"].([]any); !ok {
			t.Fatalf("run logs missing lines for %s", runID)
		}
	}

	webhooks := doJSONRequest(t, mux, http.MethodGet, "/api/v1/webhooks", nil, nil)
	var webhookLog []map[string]any
	decodeJSONResponse(t, webhooks, &webhookLog)
	if len(webhookLog) == 0 {
		t.Fatalf("expected webhook events after webhook actions")
	}
	providers := map[string]struct{}{}
	for _, event := range webhookLog {
		provider, _ := event["provider"].(string)
		providers[provider] = struct{}{}
	}
	if _, ok := providers["github"]; !ok {
		t.Fatalf("missing github webhook event")
	}
	if _, ok := providers["gitlab"]; !ok {
		t.Fatalf("missing gitlab webhook event")
	}

	featureContract := doJSONRequest(t, mux, http.MethodGet, "/api/v1/features/github-actions", nil, nil)
	var feature featurePlanResponse
	decodeJSONResponse(t, featureContract, &feature)
	if feature.Feature != "github-actions" {
		t.Fatalf("expected github-actions feature plan")
	}
	if feature.Plan.Status == "" {
		t.Fatalf("expected feature plan status")
	}
}
