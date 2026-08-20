package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func withMockCommandRunner(t *testing.T) func() {
	t.Helper()

	oldResolveExecutable := resolveExecutable
	oldRunCommand := runCommandContext
	resolveExecutable = func() (string, error) { return "true", nil }
	runCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}

	return func() {
		resolveExecutable = oldResolveExecutable
		runCommandContext = oldRunCommand
	}
}

func newServeStateForTest(t *testing.T) *serveState {
	t.Helper()
	workdir := t.TempDir()
	return &serveState{
		apiPrefix:         "/api",
		staticDir:         "site",
		defaultWorkdir:    workdir,
		runs:              newRunRegistry(),
		secretStore:       newSecretRegistry(),
		cronRuns:          newCronRunRegistry(),
		maxLogEntries:     serveDefaultLogLimit,
		maxRunEntries:     serveDefaultMaxRuns,
		maxHookEvents:     100,
		hookWorkdirGitHub: workdir,
		hookWorkdirGitLab: workdir,
	}
}

func buildServeMuxForTests(state *serveState) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", state.handleHealth)

	for _, endpointPrefix := range buildAPIPrefixes(state.apiPrefix) {
		mux.HandleFunc(endpointPrefix, state.handleAPIRoot(endpointPrefix))
		mux.HandleFunc(endpointPrefix+"/", state.handleAPIRoot(endpointPrefix))
		mux.HandleFunc(endpointPrefix+"/health", state.handleHealth)
		mux.HandleFunc(endpointPrefix+"/system", state.handleSystem)
		mux.HandleFunc(endpointPrefix+"/pipelines", state.handlePipelines)
		mux.HandleFunc(endpointPrefix+"/jobs", state.handleJobs)
		mux.HandleFunc(endpointPrefix+"/validate", state.handleValidate)
		mux.HandleFunc(endpointPrefix+"/discover", state.handleDiscover)
		mux.HandleFunc(endpointPrefix+"/stack", state.handleStackDump)
		mux.HandleFunc(endpointPrefix+"/webhooks", state.handleHookEvents)
		mux.HandleFunc(endpointPrefix+"/webhook/github", state.handleWebhook("github", state.hookSecretGitHub, state.hookWorkdirGitHub))
		mux.HandleFunc(endpointPrefix+"/webhook/gitlab", state.handleWebhook("gitlab", state.hookSecretGitLab, state.hookWorkdirGitLab))
		mux.HandleFunc(endpointPrefix+"/features", state.handleFeatureCatalog)
		mux.HandleFunc(endpointPrefix+"/features/", state.handleFeatureByName)
		mux.HandleFunc(endpointPrefix+"/workflows", state.handleWorkflows)
		mux.HandleFunc(endpointPrefix+"/workflows/", state.handleWorkflowByName)
		mux.HandleFunc(endpointPrefix+"/secrets", state.handleSecrets)
		mux.HandleFunc(endpointPrefix+"/secrets/", state.handleSecretByName)
		mux.HandleFunc(endpointPrefix+"/cron-runs", state.handleCronRuns)
		mux.HandleFunc(endpointPrefix+"/cron-runs/", state.handleCronRunByID)
		mux.HandleFunc(endpointPrefix+"/runs/", state.handleRunByID(endpointPrefix))
		mux.HandleFunc(endpointPrefix+"/runs", state.handleRuns)
	}

	mux.HandleFunc("/", state.staticHandler)

	return mux
}

func doJSONRequest(t *testing.T, handler http.Handler, method, target string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		switch value := body.(type) {
		case io.Reader:
			reader = value
		default:
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}
			reader = strings.NewReader(string(payload))
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

func decodeJSONResponse(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if target == nil {
		return
	}
	if rec.Result().StatusCode >= 400 {
		t.Fatalf("unexpected status=%d body=%s", rec.Result().StatusCode, strings.TrimSpace(rec.Body.String()))
	}
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(target); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
}

func TestBuildAPIPrefixesIncludesVersionedAlias(t *testing.T) {
	prefixes := buildAPIPrefixes("/api")
	if len(prefixes) != 2 {
		t.Fatalf("expected 2 prefixes, got %d", len(prefixes))
	}
	if prefixes[0] != "/api" || prefixes[1] != "/api/v1" {
		t.Fatalf("unexpected prefixes: %#v", prefixes)
	}
}

func TestParseWebhookDefaultsFromQuery_AutoFetchHeuristics(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/?repository=owner/repo&workdir=/tmp&maxParallel=8&timeout=12", nil)
	parsed, autoFetchSet := parseWebhookDefaultsFromQuery(req, "/fallback")

	if parsed.Workdir != "/tmp" {
		t.Fatalf("expected workdir /tmp, got %q", parsed.Workdir)
	}
	if parsed.AutoFetch != true {
		t.Fatalf("expected autoFetch to default true when repository is set")
	}
	if autoFetchSet {
		t.Fatalf("expected autoFetchSet=false when query does not pass autoFetch")
	}
	if parsed.MaxParallel != 8 {
		t.Fatalf("expected maxParallel 8, got %d", parsed.MaxParallel)
	}
	if parsed.Timeout != 12 {
		t.Fatalf("expected timeout 12, got %d", parsed.Timeout)
	}

	parsedExplicit, autoFetchSetExplicit := parseWebhookDefaultsFromQuery(
		httptest.NewRequest(http.MethodGet, "/?repository=owner/repo&autoFetch=false", nil),
		"/fallback",
	)

	if !autoFetchSetExplicit {
		t.Fatalf("expected autoFetchSet=true when autoFetch is explicit")
	}
	if parsedExplicit.AutoFetch {
		t.Fatalf("expected autoFetch=false for explicit override")
	}
}

func TestParseCronInterval(t *testing.T) {
	t.Run("positive durations", func(t *testing.T) {
		for _, value := range []string{"30m", "5s", "2h", "1h30m"} {
			duration, err := parseCronInterval(value)
			if err != nil {
				t.Fatalf("expected valid interval for %q: %v", value, err)
			}
			if duration <= 0 {
				t.Fatalf("expected positive duration for %q", value)
			}
		}
	})

	t.Run("invalid duration", func(t *testing.T) {
		for _, value := range []string{"", "abc", "0m", "-5s"} {
			if _, err := parseCronInterval(value); err == nil {
				t.Fatalf("expected invalid interval for %q", value)
			}
		}
	})
}

func TestWorkflowAndSecretIDHelpers(t *testing.T) {
	encoded := encodeWorkflowID(".github/workflows/ci.yml")
	decoded, err := decodeWorkflowID(encoded)
	if err != nil {
		t.Fatalf("decode workflow id failed: %v", err)
	}
	if decoded != ".github/workflows/ci.yml" {
		t.Fatalf("unexpected decode: %q", decoded)
	}

	secretScope, secretName, err := parseSecretRef("prod:GITHUB_TOKEN")
	if err != nil {
		t.Fatalf("parse secret ref failed: %v", err)
	}
	if secretScope != "prod" || secretName != "GITHUB_TOKEN" {
		t.Fatalf("unexpected secret ref parse: scope=%q name=%q", secretScope, secretName)
	}
}

func TestBuildRunEnvironmentInjectsSecrets(t *testing.T) {
	state := newServeStateForTest(t)
	state.secretStore.put("global", "API_TOKEN", "redacted")
	state.secretStore.put("project", "TOKEN", "project-secret")

	req := runExecutionRequest{
		Env:        []string{"BASE_ENV=enabled"},
		SecretRefs: []string{"global:api_token", "project:token", "GLOBAL_MISSING:ignore"},
	}

	_, err := state.buildRunEnvironment(req)
	if err == nil {
		t.Fatalf("expected unknown scope to fail when secret is missing")
	}

	req = runExecutionRequest{
		Env:        []string{"BASE_ENV=enabled"},
		SecretRefs: []string{"api_token", "project:token"},
	}
	env, err := state.buildRunEnvironment(req)
	if err != nil {
		t.Fatalf("buildRunEnvironment returned error: %v", err)
	}

	m := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			m[key] = value
		}
	}

	if m["BASE_ENV"] != "enabled" {
		t.Fatalf("expected BASE_ENV preserved, got %#v", m["BASE_ENV"])
	}
	if m["API_TOKEN"] != "redacted" {
		t.Fatalf("expected injected global secret, got %#v", m["API_TOKEN"])
	}
	if m["TOKEN"] != "project-secret" {
		t.Fatalf("expected injected project secret, got %#v", m["TOKEN"])
	}
}

func TestHandleWorkflowsDispatchByEncodedID(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	workflowID := encodeWorkflowID(".github/workflows/ci.yml")
	encoded := url.PathEscape(workflowID)
	run := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows/"+encoded+"/dispatch", map[string]any{
		"workdir": state.defaultWorkdir,
	}, nil)

	if run.Code != http.StatusCreated {
		t.Fatalf("workflow dispatch status: %d body=%s", run.Code, run.Body.String())
	}

	var payload map[string]any
	decodeJSONResponse(t, run, &payload)
	if payload["file"] != ".github/workflows/ci.yml" {
		t.Fatalf("workflow dispatch should keep resolved file, got %#v", payload["file"])
	}
	if payload["workdir"] != state.defaultWorkdir {
		t.Fatalf("workflow dispatch should use requested workdir, got %#v", payload["workdir"])
	}
}

func TestHandleWorkflowByNameReturnsLastRun(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)
	create := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"file":    "ci.yml",
		"workdir": state.defaultWorkdir,
	}, nil)
	if create.Code != http.StatusCreated {
		t.Fatalf("workflow create status: %d body=%s", create.Code, create.Body.String())
	}

	id := encodeWorkflowID("ci.yml")
	res := doJSONRequest(t, mux, http.MethodGet, "/api/v1/workflows/"+url.PathEscape(id), nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("workflow get by id status: %d body=%s", res.Code, res.Body.String())
	}

	var payload map[string]any
	decodeJSONResponse(t, res, &payload)
	if payload["file"] != "ci.yml" {
		t.Fatalf("expected workflow file in response, got %#v", payload["file"])
	}
	if _, ok := payload["lastRun"]; !ok {
		t.Fatalf("expected lastRun in workflow payload after execution")
	}
}

func TestCronRunDeletionPreventsTrigger(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)
	created := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "delete-me",
		"workflowFile": "ci.yml",
		"interval":     "2m",
		"workdir":      state.defaultWorkdir,
	}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("cron create status: %d body=%s", created.Code, created.Body.String())
	}

	var createdPayload map[string]any
	decodeJSONResponse(t, created, &createdPayload)
	id := createdPayload["id"].(string)

	deleteResp := doJSONRequest(t, mux, http.MethodDelete, "/api/v1/cron-runs/"+id, nil, nil)
	if deleteResp.Code != http.StatusOK {
		t.Fatalf("delete cron run status: %d body=%s", deleteResp.Code, deleteResp.Body.String())
	}

	trigger := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+id+"/run", nil, nil)
	if trigger.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on trigger for deleted cron run, got %d", trigger.Code)
	}
}

func TestCronRunPauseResumeFlow(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	created := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "paused-runner",
		"workflowFile": "ci.yml",
		"interval":     "30s",
		"workdir":      state.defaultWorkdir,
	}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create cron run status: %d body=%s", created.Code, created.Body.String())
	}

	var createdPayload map[string]any
	decodeJSONResponse(t, created, &createdPayload)
	cronID, _ := createdPayload["id"].(string)
	if cronID == "" {
		t.Fatalf("expected cron run id")
	}

	pause := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/pause?reason=maintenance", nil, nil)
	if pause.Code != http.StatusOK {
		t.Fatalf("pause cron run status: %d body=%s", pause.Code, pause.Body.String())
	}

	var paused map[string]any
	decodeJSONResponse(t, pause, &paused)
	if paused["status"] != "paused" {
		t.Fatalf("expected paused status, got %#v", paused["status"])
	}

	item := doJSONRequest(t, mux, http.MethodGet, "/api/v1/cron-runs/"+cronID, nil, nil)
	if item.Code != http.StatusOK {
		t.Fatalf("cron run lookup status: %d body=%s", item.Code, item.Body.String())
	}
	var cronItem map[string]any
	decodeJSONResponse(t, item, &cronItem)
	if cronItem["status"] != "paused" {
		t.Fatalf("expected paused status in cron record, got %#v", cronItem["status"])
	}
	if cronItem["pausedReason"] != "maintenance" {
		t.Fatalf("expected pausedReason=maintenance, got %#v", cronItem["pausedReason"])
	}

	triggerWhilePaused := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/run", nil, nil)
	if triggerWhilePaused.Code != http.StatusBadRequest {
		t.Fatalf("trigger while paused should return bad request, got %d", triggerWhilePaused.Code)
	}

	resume := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/resume", nil, nil)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume cron run status: %d body=%s", resume.Code, resume.Body.String())
	}
	var resumed map[string]any
	decodeJSONResponse(t, resume, &resumed)
	if resumed["status"] != "active" {
		t.Fatalf("expected active status, got %#v", resumed["status"])
	}
}

func TestHandleImplementedFeatureEndpoints(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	catalog := doJSONRequest(t, mux, http.MethodGet, "/api/v1/features", nil, nil)
	if catalog.Code != http.StatusOK {
		t.Fatalf("features response status: %d body=%s", catalog.Code, catalog.Body.String())
	}

	var catalogResponse featureCatalogResponse
	decodeJSONResponse(t, catalog, &catalogResponse)
	if !catalogResponse.OK {
		t.Fatalf("expected ok=true in feature catalog")
	}
	if _, ok := catalogResponse.Capabilities["workflows"]; !ok {
		t.Fatalf("feature catalog missing workflows contract")
	}

	workflows := doJSONRequest(t, mux, http.MethodPost, "/api/v1/workflows", map[string]any{
		"file":    "ci.yml",
		"workdir": state.defaultWorkdir,
	}, nil)
	if workflows.Code != http.StatusCreated {
		t.Fatalf("workflows dispatch response status: %d body=%s", workflows.Code, workflows.Body.String())
	}
	var workflowResponse map[string]any
	decodeJSONResponse(t, workflows, &workflowResponse)
	if workflowResponse["file"] != "ci.yml" {
		t.Fatalf("expected workflow dispatch to preserve file, got %#v", workflowResponse["file"])
	}

	secrets := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "GITHUB_TOKEN",
		"value": "redacted",
	}, nil)
	if secrets.Code != http.StatusAccepted {
		t.Fatalf("secrets response status: %d body=%s", secrets.Code, secrets.Body.String())
	}
	var secretResponse map[string]any
	decodeJSONResponse(t, secrets, &secretResponse)
	if secretResponse["name"] != "GITHUB_TOKEN" {
		t.Fatalf("expected stored secret name, got %#v", secretResponse["name"])
	}

	cron := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "nightly",
		"workflowFile": "ci.yml",
		"interval":     "2m",
		"workdir":      state.defaultWorkdir,
	}, nil)
	if cron.Code != http.StatusCreated {
		t.Fatalf("cron create response status: %d body=%s", cron.Code, cron.Body.String())
	}
	var cronResponse cronRun
	decodeJSONResponse(t, cron, &cronResponse)
	if cronResponse.Interval != "2m" {
		t.Fatalf("expected 2m interval, got %#v", cronResponse.Interval)
	}

	var ghaContract featurePlanResponse
	ghActions := doJSONRequest(t, mux, http.MethodGet, "/api/v1/features/github-actions", nil, nil)
	decodeJSONResponse(t, ghActions, &ghaContract)
	foundPause := false
	foundResume := false
	foundRun := false
	for _, endpoint := range ghaContract.Plan.Endpoints {
		if endpoint == "/cron-runs/{id}/pause" {
			foundPause = true
		}
		if endpoint == "/cron-runs/{id}/resume" {
			foundResume = true
		}
		if endpoint == "/cron-runs/{id}/run" {
			foundRun = true
		}
	}

	if !foundPause || !foundResume || !foundRun {
		t.Fatalf("github-actions feature contract missing cron controls: %v", ghaContract.Plan.Endpoints)
	}
}

func TestHandleAPIRootContainsActionPlaneRoutes(t *testing.T) {
	state := newServeStateForTest(t)
	mux := buildServeMuxForTests(state)

	for _, rootPath := range []string{"/api", "/api/v1"} {
		response := doJSONRequest(t, mux, http.MethodGet, rootPath, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: expected root API response, got %d", rootPath, response.Code)
		}

		var payload map[string]any
		decodeJSONResponse(t, response, &payload)
		routesValue, ok := payload["routes"].(map[string]any)
		if !ok {
			t.Fatalf("%s: missing routes map", rootPath)
		}

		required := []string{
			"runs",
			"runById",
			"runLogs",
			"runRetry",
			"runCancel",
			"workflows",
			"workflowsById",
			"workflowDispatchById",
			"secrets",
			"secretByName",
			"cronRuns",
			"cronRunById",
			"cronRunPause",
			"cronRunResume",
			"cronRunImmediateRun",
			"github",
			"gitlab",
		}

		for _, key := range required {
			if _, ok := routesValue[key]; !ok {
				t.Fatalf("%s: route key %s missing", rootPath, key)
			}
		}
	}
}

func TestHandleRunsLogPaginationSupportsOffset(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	create := doJSONRequest(t, mux, http.MethodPost, "/api/v1/runs", map[string]any{
		"workdir":       state.defaultWorkdir,
		"file":          "pipeline.yml",
		"maxLogEntries": 80,
	}, nil)
	if create.Code != http.StatusCreated {
		t.Fatalf("run create status: %d body=%s", create.Code, create.Body.String())
	}

	var created map[string]any
	decodeJSONResponse(t, create, &created)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("run id is required, got %#v", created)
	}

	first := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID+"/logs?offset=0", nil, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("run log fetch status: %d body=%s", first.Code, first.Body.String())
	}

	var firstPayload map[string]any
	decodeJSONResponse(t, first, &firstPayload)
	firstLines, _ := firstPayload["lines"].([]any)
	if len(firstLines) == 0 {
		t.Fatalf("expected first page logs")
	}

	totalLinesRaw, ok := firstPayload["totalLines"].(float64)
	if !ok {
		t.Fatalf("totalLines missing from logs payload")
	}
	totalLines := int(totalLinesRaw)
	if totalLines == 0 {
		t.Fatalf("totalLines should be positive")
	}

	tail := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID+"/logs?offset="+strconv.Itoa(totalLines+99), nil, nil)
	if tail.Code != http.StatusOK {
		t.Fatalf("run log tail status: %d body=%s", tail.Code, tail.Body.String())
	}

	var tailPayload map[string]any
	decodeJSONResponse(t, tail, &tailPayload)
	tailLines, _ := tailPayload["lines"].([]any)
	if len(tailLines) != 0 {
		t.Fatalf("expected empty page for oversized offset, got %d lines", len(tailLines))
	}
}

func TestHandleRunCancelConflictsForCompletedRun(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	session := newRunSession("run-complete", state.defaultWorkdir, "pipeline.yml", 20, []string{"gci", "run", "pipeline.yml"}, runExecutionRequest{Workdir: state.defaultWorkdir, File: "pipeline.yml"})
	session.setResult(0, nil, runStatusSucceeded)
	state.runs.add(session)

	cancel := doJSONRequest(t, mux, http.MethodPost, "/api/v1/runs/run-complete/cancel", map[string]any{}, nil)
	if cancel.Code != http.StatusConflict {
		t.Fatalf("cancel completed run status: expected %d, got %d body=%s", http.StatusConflict, cancel.Code, cancel.Body.String())
	}

	var payload map[string]any
	decodeJSONResponse(t, cancel, &payload)
	if payload["error"] == nil {
		t.Fatalf("expected error message for completed run cancellation")
	}
}

func TestHandleRunsSupportsStatusFileAndLimitFilters(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	succeeded := newRunSession("run-succeeded", state.defaultWorkdir, "alpha.yml", 20, []string{"gci", "run", "alpha.yml"}, runExecutionRequest{Workdir: state.defaultWorkdir, File: "alpha.yml"})
	succeeded.setResult(0, nil, runStatusSucceeded)
	state.runs.add(succeeded)

	failed := newRunSession("run-failed", state.defaultWorkdir, "beta.yml", 20, []string{"gci", "run", "beta.yml"}, runExecutionRequest{Workdir: state.defaultWorkdir, File: "beta.yml"})
	failed.setResult(1, nil, runStatusFailed)
	state.runs.add(failed)

	pending := newRunSession("run-pending", state.defaultWorkdir, "alpha.yml", 20, []string{"gci", "run", "alpha.yml", "--job", "build"}, runExecutionRequest{Workdir: state.defaultWorkdir, File: "alpha.yml"})
	state.runs.add(pending)

	filterStatus := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs?status=succeeded", nil, nil)
	if filterStatus.Code != http.StatusOK {
		t.Fatalf("runs status filter status=%d body=%s", filterStatus.Code, filterStatus.Body.String())
	}

	var statusFiltered []map[string]any
	decodeJSONResponse(t, filterStatus, &statusFiltered)
	if len(statusFiltered) != 1 || statusFiltered[0]["id"] != "run-succeeded" {
		t.Fatalf("status filter should return only succeeded run, got %#v", statusFiltered)
	}

	filterFile := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs?file=alpha.yml", nil, nil)
	if filterFile.Code != http.StatusOK {
		t.Fatalf("runs file filter status=%d body=%s", filterFile.Code, filterFile.Body.String())
	}
	var fileFiltered []map[string]any
	decodeJSONResponse(t, filterFile, &fileFiltered)
	if len(fileFiltered) != 2 {
		t.Fatalf("file filter should return alpha runs, got %d", len(fileFiltered))
	}

	filterJob := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs?job=build", nil, nil)
	if filterJob.Code != http.StatusOK {
		t.Fatalf("runs job filter status=%d body=%s", filterJob.Code, filterJob.Body.String())
	}
	var jobFiltered []map[string]any
	decodeJSONResponse(t, filterJob, &jobFiltered)
	if len(jobFiltered) != 1 || jobFiltered[0]["id"] != "run-pending" {
		t.Fatalf("job filter should return run with build flag, got %#v", jobFiltered)
	}

	limit := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs?limit=1", nil, nil)
	if limit.Code != http.StatusOK {
		t.Fatalf("runs limit status=%d body=%s", limit.Code, limit.Body.String())
	}
	var limited []map[string]any
	decodeJSONResponse(t, limit, &limited)
	if len(limited) != 1 {
		t.Fatalf("limit should reduce list to one item, got %d", len(limited))
	}
}

func TestCronRunManualTrigger(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	created := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs", map[string]any{
		"name":         "manual",
		"workflowFile": "ci.yml",
		"interval":     "1m",
		"workdir":      state.defaultWorkdir,
	}, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create cron run status: %d body=%s", created.Code, created.Body.String())
	}

	var item map[string]any
	decodeJSONResponse(t, created, &item)
	cronID, _ := item["id"].(string)
	if cronID == "" {
		t.Fatalf("expected cron run id")
	}

	triggered := doJSONRequest(t, mux, http.MethodPost, "/api/v1/cron-runs/"+cronID+"/run", nil, nil)
	if triggered.Code != http.StatusCreated {
		t.Fatalf("trigger cron run status: %d body=%s", triggered.Code, triggered.Body.String())
	}

	var triggeredPayload map[string]any
	decodeJSONResponse(t, triggered, &triggeredPayload)
	if triggeredPayload["ok"] != true {
		t.Fatalf("expected ok=true in trigger response")
	}
	if runID, _ := triggeredPayload["runId"].(string); runID == "" {
		t.Fatalf("expected run id in trigger response")
	}
}

func TestSecretsListGetByName(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	created := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "project_token",
		"value": "super-secret",
		"scope": "global",
	}, nil)
	if created.Code != http.StatusAccepted {
		t.Fatalf("create secret status: %d body=%s", created.Code, created.Body.String())
	}

	list := doJSONRequest(t, mux, http.MethodGet, "/api/v1/secrets", nil, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list secrets status: %d body=%s", list.Code, list.Body.String())
	}
	var listPayload map[string]any
	decodeJSONResponse(t, list, &listPayload)
	count, ok := listPayload["count"].(float64)
	if !ok || count < 1 {
		t.Fatalf("expected at least one secret after create")
	}

	get := doJSONRequest(t, mux, http.MethodGet, "/api/v1/secrets/PROJECT_TOKEN", nil, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("get secret status: %d body=%s", get.Code, get.Body.String())
	}
	var secretPayload map[string]any
	decodeJSONResponse(t, get, &secretPayload)
	if secretPayload["name"] != "PROJECT_TOKEN" {
		t.Fatalf("expected normalized secret name, got %#v", secretPayload["name"])
	}
	revealed, ok := secretPayload["revealed"].(bool)
	if !ok || revealed {
		t.Fatalf("secret should be hidden by default")
	}
}

func TestHandleRunsPostListRetryLifecycle(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	create := doJSONRequest(t, mux, http.MethodPost, "/api/v1/runs", map[string]any{
		"workdir":       state.defaultWorkdir,
		"file":          "pipeline.yml",
		"maxLogEntries": 80,
	}, nil)
	if create.Code != http.StatusCreated {
		t.Fatalf("run create status: %d body=%s", create.Code, create.Body.String())
	}

	var created map[string]any
	decodeJSONResponse(t, create, &created)
	runID, _ := created["id"].(string)
	if runID == "" {
		t.Fatalf("run id is required, got %#v", created)
	}

	list := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs", nil, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("run list status: %d body=%s", list.Code, list.Body.String())
	}
	var listPayload []map[string]any
	decodeJSONResponse(t, list, &listPayload)
	if len(listPayload) == 0 {
		t.Fatalf("expected at least one run in listing")
	}

	found := false
	for _, item := range listPayload {
		if item["id"] == runID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created run not in response payload")
	}

	details := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID, nil, nil)
	if details.Code != http.StatusOK {
		t.Fatalf("run details status: %d body=%s", details.Code, details.Body.String())
	}
	var detail map[string]any
	decodeJSONResponse(t, details, &detail)
	if detail["id"] != runID {
		t.Fatalf("run details id mismatch: got=%v expected=%s", detail["id"], runID)
	}

	retry := doJSONRequest(t, mux, http.MethodPost, "/api/v1/runs/"+runID+"/retry", map[string]any{}, map[string]string{
		"Content-Type": "application/json",
	})
	if retry.Code != http.StatusCreated {
		t.Fatalf("run retry status: %d body=%s", retry.Code, retry.Body.String())
	}
	var retried map[string]any
	decodeJSONResponse(t, retry, &retried)
	retryID, _ := retried["id"].(string)
	if retryID == "" {
		t.Fatalf("retry must return run id")
	}
	if retryID == runID {
		t.Fatalf("retry must create a new run id")
	}
}

func TestWebhookHandlingAcceptsGitHubPayload(t *testing.T) {
	state := newServeStateForTest(t)
	state.hookSecretGitHub = "webhook-secret"
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	payload := `{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"."}}`
	mac := hmac.New(sha256.New, []byte("webhook-secret"))
	_, _ = mac.Write([]byte(payload))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	webhook := doJSONRequest(
		t,
		mux,
		http.MethodPost,
		"/api/v1/webhook/github?workdir="+state.defaultWorkdir+"&repository=.",
		strings.NewReader(payload),
		map[string]string{"X-Hub-Signature-256": signature},
	)
	var decoded map[string]any
	decodeJSONResponse(t, webhook, &decoded)
	if webhook.Code != http.StatusAccepted {
		t.Fatalf("webhook status: %d body=%s", webhook.Code, webhook.Body.String())
	}

	if _, ok := decoded["runId"]; !ok {
		t.Fatalf("expected runId in webhook response")
	}
	runID, _ := decoded["runId"].(string)

	webhooks := doJSONRequest(t, mux, http.MethodGet, "/api/v1/webhooks", nil, nil)
	var events []map[string]any
	decodeJSONResponse(t, webhooks, &events)
	if len(events) == 0 {
		t.Fatalf("expected webhook event log entry")
	}
	if events[0]["runId"] != runID {
		t.Fatalf("webhook run id mismatch in log: got=%v expected=%s", events[0]["runId"], runID)
	}
}

func TestWebhookHandlingRejectsInvalidSignature(t *testing.T) {
	state := newServeStateForTest(t)
	state.hookSecretGitHub = "secret"
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)
	mux := buildServeMuxForTests(state)

	webhook := doJSONRequest(t, mux, http.MethodPost, "/api/v1/webhook/github", strings.NewReader(`{"repository":{"full_name":"."}}`), map[string]string{
		"X-Hub-Signature-256": "sha256=deadbeef",
		"X-GitHub-Event":      "push",
	})
	if webhook.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", webhook.Code)
	}
}

func TestHandleStackEndpointContainsHeapStats(t *testing.T) {
	state := newServeStateForTest(t)
	mux := buildServeMuxForTests(state)

	response := doJSONRequest(t, mux, http.MethodGet, "/api/v1/stack", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("stack status: %d body=%s", response.Code, response.Body.String())
	}
	var payload map[string]any
	decodeJSONResponse(t, response, &payload)
	if _, ok := payload["heapObjects"]; !ok {
		t.Fatalf("expected heapObjects in stack response")
	}
	if _, ok := payload["activeRuns"]; !ok {
		t.Fatalf("expected activeRuns in stack response")
	}
	if _, ok := payload["recentRuns"]; !ok {
		t.Fatalf("expected recentRuns in stack response")
	}
}

func TestHandleSecretStoreScopeIsolation(t *testing.T) {
	state := newServeStateForTest(t)
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	globalSecret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "global_token",
		"value": "global-secret",
	}, nil)
	if globalSecret.Code != http.StatusAccepted {
		t.Fatalf("store global secret status=%d body=%s", globalSecret.Code, globalSecret.Body.String())
	}

	projectSecret := doJSONRequest(t, mux, http.MethodPost, "/api/v1/secrets", map[string]any{
		"name":  "project_token",
		"value": "project-secret",
		"scope": "project",
	}, nil)
	if projectSecret.Code != http.StatusAccepted {
		t.Fatalf("store project secret status=%d body=%s", projectSecret.Code, projectSecret.Body.String())
	}

	allSecretViews := []struct {
		path          string
		expectCount   int
		expectName    string
		expectScope   string
		expectEntries int
	}{
		{"/api/v1/secrets", 1, "GLOBAL_TOKEN", "global", 1},
		{"/api/v1/secrets?scope=project", 1, "PROJECT_TOKEN", "project", 1},
		{"/api/v1/secrets?scope=global", 1, "GLOBAL_TOKEN", "global", 1},
	}

	for _, view := range allSecretViews {
		response := doJSONRequest(t, mux, http.MethodGet, view.path, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("secret list status=%d path=%q body=%s", response.Code, view.path, response.Body.String())
		}

		var payload map[string]any
		decodeJSONResponse(t, response, &payload)
		count, ok := payload["count"].(float64)
		if !ok {
			t.Fatalf("unexpected count payload for %q: %#v", view.path, payload["count"])
		}
		if int(count) != view.expectCount {
			t.Fatalf("unexpected count for %q: got=%v expected=%d", view.path, count, view.expectCount)
		}

		items, ok := payload["items"].([]any)
		if !ok {
			t.Fatalf("missing items for %q", view.path)
		}
		if len(items) != view.expectEntries {
			t.Fatalf("unexpected item count for %q: got=%d expected=%d", view.path, len(items), view.expectEntries)
		}

		entry, ok := items[0].(map[string]any)
		if !ok {
			t.Fatalf("secret entry shape invalid for %q", view.path)
		}
		if entry["name"] != view.expectName {
			t.Fatalf("unexpected secret name for %q: got=%v expected=%s", view.path, entry["name"], view.expectName)
		}
		if entry["scope"] != view.expectScope {
			t.Fatalf("unexpected secret scope for %q: got=%v expected=%s", view.path, entry["scope"], view.expectScope)
		}
	}
}

func TestHandleWebhookGitLabTokenValidationAndMetadata(t *testing.T) {
	state := newServeStateForTest(t)
	state.hookSecretGitLab = "gitlab-secret"
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	payload := map[string]any{
		"object_kind": "push",
		"ref":         "refs/heads/main",
		"after":       "def456",
		"project": map[string]any{
			"path_with_namespace": "owner/repo",
			"http_url_to_repo":    "https://gitlab.com/owner/repo.git",
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	rejected := doJSONRequest(
		t,
		mux,
		http.MethodPost,
		"/api/v1/webhook/gitlab?workdir="+state.defaultWorkdir+"&repository=.",
		strings.NewReader(string(raw)),
		map[string]string{"X-Gitlab-Token": "bad"},
	)
	if rejected.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized webhook status=%d body=%s", rejected.Code, rejected.Body.String())
	}

	accepted := doJSONRequest(
		t,
		mux,
		http.MethodPost,
		"/api/v1/webhook/gitlab?workdir="+state.defaultWorkdir+"&repository=.",
		strings.NewReader(string(raw)),
		map[string]string{
			"X-Gitlab-Token": "gitlab-secret",
			"X-Gitlab-Event": "Push Hook",
		},
	)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("expected accepted webhook status=%d body=%s", accepted.Code, accepted.Body.String())
	}

	var acceptedPayload map[string]any
	decodeJSONResponse(t, accepted, &acceptedPayload)
	runID, ok := acceptedPayload["runId"].(string)
	if !ok || runID == "" {
		t.Fatalf("missing runId in accepted webhook response: %#v", acceptedPayload)
	}

	runResp := doJSONRequest(t, mux, http.MethodGet, "/api/v1/runs/"+runID, nil, nil)
	var runPayload map[string]any
	decodeJSONResponse(t, runResp, &runPayload)
	if runPayload["repository"] != "." {
		t.Fatalf("webhook repository resolution mismatch: %#v", runPayload["repository"])
	}
	if runPayload["autoFetch"] != true {
		t.Fatalf("webhook autoFetch should be true for repository-backed payload, got %#v", runPayload["autoFetch"])
	}
	if runPayload["ref"] != "refs/heads/main" {
		t.Fatalf("webhook ref mismatch: %#v", runPayload["ref"])
	}
}

func TestWebhookEventsHonorConfiguredLimit(t *testing.T) {
	state := newServeStateForTest(t)
	state.hookSecretGitHub = "github-secret"
	state.maxHookEvents = 2
	restore := withMockCommandRunner(t)
	t.Cleanup(restore)

	mux := buildServeMuxForTests(state)

	payload := `{"ref":"refs/heads/main","after":"abc123","repository":{"full_name":"."}}`
	mac := hmac.New(sha256.New, []byte("github-secret"))
	_, _ = mac.Write([]byte(payload))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	for i := 0; i < 4; i++ {
		response := doJSONRequest(
			t,
			mux,
			http.MethodPost,
			"/api/v1/webhook/github?workdir="+state.defaultWorkdir+"&repository=.",
			strings.NewReader(payload),
			map[string]string{"X-Hub-Signature-256": signature},
		)
		if response.Code != http.StatusAccepted {
			t.Fatalf("webhook send %d failed: status=%d body=%s", i, response.Code, response.Body.String())
		}
	}

	logs := doJSONRequest(t, mux, http.MethodGet, "/api/v1/webhooks", nil, nil)
	var events []map[string]any
	decodeJSONResponse(t, logs, &events)
	if len(events) != state.maxHookEvents {
		t.Fatalf("expected webhook list to honor retention size %d, got %d", state.maxHookEvents, len(events))
	}
}
