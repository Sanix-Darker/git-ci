package handlers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	cli "github.com/urfave/cli/v2"
)

const (
	serveDefaultLogLimit     = 1000
	serveDefaultMaxRuns      = 200
	serveScannerBuffer       = 4 * 1024 * 1024
	runStatusPending         = "pending"
	runStatusRunning         = "running"
	runStatusSucceeded       = "succeeded"
	runStatusFailed          = "failed"
	runStatusCanceled        = "canceled"
	runRepositoryCacheDir    = ".gci-repos"
	runCronStatusActive      = "active"
	runCronStatusPaused      = "paused"
	plannedFeatureStatus     = "planned"
	implementedFeatureStatus = "implemented"
)

var (
	resolveExecutable  = os.Executable
	runCommandContext  = exec.CommandContext
	repoNameCleaner    = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
	errCronRunNotFound = errors.New("cron run not found")
)

type runExecutionRequest struct {
	Workdir       string         `json:"workdir"`
	File          string         `json:"file"`
	Job           string         `json:"job"`
	Stage         string         `json:"stage"`
	Only          []string       `json:"only"`
	Except        []string       `json:"except"`
	Parallel      bool           `json:"parallel"`
	MaxParallel   int            `json:"maxParallel"`
	ContinueOnErr bool           `json:"continueOnError"`
	Docker        bool           `json:"docker"`
	Podman        bool           `json:"podman"`
	DryRun        bool           `json:"dryRun"`
	Timeout       int            `json:"timeout"`
	Env           []string       `json:"env"`
	EnvFile       string         `json:"envFile"`
	NoCache       bool           `json:"noCache"`
	NoPull        bool           `json:"noPull"`
	Volume        []string       `json:"volume"`
	Network       string         `json:"network"`
	Memory        string         `json:"memory"`
	CPUs          string         `json:"cpus"`
	Verbose       bool           `json:"verbose"`
	Debug         bool           `json:"debug"`
	Quiet         bool           `json:"quiet"`
	MaxLogEntries int            `json:"maxLogEntries"`
	Repository    string         `json:"repository"`
	RepositoryURL string         `json:"repositoryUrl"`
	Ref           string         `json:"ref"`
	AutoFetch     bool           `json:"autoFetch"`
	SecretRefs    []string       `json:"secretRefs"`
	Inputs        map[string]any `json:"inputs"`
}

type runSession struct {
	ID            string
	Workdir       string
	File          string
	Command       []string
	Request       runExecutionRequest
	Ref           string         `json:"ref"`
	AutoFetch     bool           `json:"autoFetch"`
	SecretRefs    []string       `json:"secretRefs"`
	Inputs        map[string]any `json:"inputs,omitempty"`
	Repository    string         `json:"repository"`
	RepositoryURL string         `json:"repositoryUrl"`

	Status     string
	ExitCode   int
	Error      string
	StartedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time

	maxLogs int
	logs    []string
	cancel  context.CancelFunc

	mu sync.Mutex
}

type runSessionSnapshot struct {
	ID            string         `json:"id"`
	Workdir       string         `json:"workdir"`
	File          string         `json:"file"`
	Command       []string       `json:"command"`
	Ref           string         `json:"ref"`
	Repository    string         `json:"repository"`
	RepositoryURL string         `json:"repositoryUrl"`
	AutoFetch     bool           `json:"autoFetch"`
	SecretRefs    []string       `json:"secretRefs"`
	Inputs        map[string]any `json:"inputs,omitempty"`
	Status        string         `json:"status"`
	ExitCode      int            `json:"exitCode"`
	Error         string         `json:"error,omitempty"`
	StartedAt     time.Time      `json:"startedAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
	Logs          []string       `json:"logs"`
}

type runSessionListItem struct {
	ID            string         `json:"id"`
	Workdir       string         `json:"workdir"`
	File          string         `json:"file"`
	Command       []string       `json:"command"`
	Ref           string         `json:"ref"`
	Repository    string         `json:"repository"`
	RepositoryURL string         `json:"repositoryUrl"`
	AutoFetch     bool           `json:"autoFetch"`
	SecretRefs    []string       `json:"secretRefs"`
	Inputs        map[string]any `json:"inputs,omitempty"`
	Status        string         `json:"status"`
	ExitCode      int            `json:"exitCode"`
	Error         string         `json:"error,omitempty"`
	StartedAt     time.Time      `json:"startedAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	FinishedAt    *time.Time     `json:"finishedAt,omitempty"`
}

type hookEvent struct {
	ID            string    `json:"id"`
	Provider      string    `json:"provider"`
	Event         string    `json:"event"`
	Ref           string    `json:"ref"`
	Commit        string    `json:"commit"`
	Workdir       string    `json:"workdir"`
	Repository    string    `json:"repository"`
	RepositoryURL string    `json:"repositoryUrl"`
	Status        string    `json:"status"`
	RunID         string    `json:"runId,omitempty"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

func newRunSession(id, workdir, file string, maxLogEntries int, command []string, req runExecutionRequest) *runSession {
	limit := serveDefaultLogLimit
	if maxLogEntries > 0 {
		limit = maxLogEntries
	}
	now := time.Now()
	return &runSession{
		ID:            id,
		Workdir:       workdir,
		File:          file,
		Command:       command,
		Request:       req,
		Ref:           req.Ref,
		AutoFetch:     req.AutoFetch,
		SecretRefs:    req.SecretRefs,
		Inputs:        copyInputs(req.Inputs),
		Repository:    req.Repository,
		RepositoryURL: req.RepositoryURL,
		Status:        runStatusPending,
		StartedAt:     now,
		UpdatedAt:     now,
		maxLogs:       limit,
		logs:          []string{"[init] run created"},
	}
}

func (s *runSession) appendLog(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if line == "" {
		return
	}

	s.logs = append(s.logs, fmt.Sprintf("%s %s", time.Now().Format(time.RFC3339), line))
	if len(s.logs) > s.maxLogs {
		s.logs = append([]string(nil), s.logs[len(s.logs)-s.maxLogs:]...)
	}
	s.UpdatedAt = time.Now()
}

func (s *runSession) updateStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.Status = status
	s.UpdatedAt = time.Now()
}

func (s *runSession) setResult(exitCode int, err error, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.Error = err.Error()
	}
	s.ExitCode = exitCode
	s.Status = status
	s.UpdatedAt = time.Now()
	now := s.UpdatedAt
	s.FinishedAt = &now
}

func (s *runSession) snapshot() runSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	var finishedAt *time.Time
	if s.FinishedAt != nil {
		copyAt := *s.FinishedAt
		finishedAt = &copyAt
	}

	return runSessionSnapshot{
		ID:            s.ID,
		Workdir:       s.Workdir,
		File:          s.File,
		Command:       append([]string(nil), s.Command...),
		Ref:           s.Ref,
		Repository:    s.Repository,
		RepositoryURL: s.RepositoryURL,
		AutoFetch:     s.AutoFetch,
		SecretRefs:    append([]string(nil), s.SecretRefs...),
		Inputs:        copyInputs(s.Inputs),
		Status:        s.Status,
		ExitCode:      s.ExitCode,
		Error:         s.Error,
		StartedAt:     s.StartedAt,
		UpdatedAt:     s.UpdatedAt,
		FinishedAt:    finishedAt,
		Logs:          append([]string(nil), s.logs...),
	}
}

func (s *runSession) logsFrom(offset int) ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if offset < 0 {
		offset = 0
	}
	total := len(s.logs)
	if offset > total {
		offset = total
	}
	return append([]string(nil), s.logs[offset:]...), total
}

func (s *runSession) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}
}

func (s *runSession) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancel = cancel
}

type runRegistry struct {
	mu   sync.RWMutex
	runs map[string]*runSession
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[string]*runSession)}
}

func (r *runRegistry) add(session *runSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[session.ID] = session
}

func (r *runRegistry) prune(keep int) {
	if keep <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.runs) <= keep {
		return
	}

	running := make([]*runSession, 0, len(r.runs))
	done := make([]*runSession, 0, len(r.runs))

	for _, session := range r.runs {
		snapshot := session.snapshot()
		switch snapshot.Status {
		case runStatusRunning, runStatusPending:
			running = append(running, session)
		default:
			done = append(done, session)
		}
	}

	if len(running) >= keep {
		return
	}

	sort.Slice(done, func(i, j int) bool {
		left := done[i].snapshot().StartedAt
		right := done[j].snapshot().StartedAt
		return left.After(right)
	})

	allowedDone := keep - len(running)
	if allowedDone < 0 {
		allowedDone = 0
	}

	for i := allowedDone; i < len(done); i++ {
		delete(r.runs, done[i].ID)
	}
}

func (r *runRegistry) get(id string) (*runSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	session, ok := r.runs[id]
	return session, ok
}

func (r *runRegistry) list() []*runSession {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*runSession, 0, len(r.runs))
	for _, session := range r.runs {
		out = append(out, session)
	}
	return out
}

func (s *runSession) listSnapshot() runSessionListItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	var finishedAt *time.Time
	if s.FinishedAt != nil {
		copyAt := *s.FinishedAt
		finishedAt = &copyAt
	}

	return runSessionListItem{
		ID:            s.ID,
		Workdir:       s.Workdir,
		File:          s.File,
		Command:       append([]string(nil), s.Command...),
		Ref:           s.Ref,
		Repository:    s.Repository,
		RepositoryURL: s.RepositoryURL,
		AutoFetch:     s.AutoFetch,
		Status:        s.Status,
		ExitCode:      s.ExitCode,
		Error:         s.Error,
		StartedAt:     s.StartedAt,
		UpdatedAt:     s.UpdatedAt,
		FinishedAt:    finishedAt,
	}
}

func copyInputs(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}

	out := make(map[string]any, len(raw))
	for key, value := range raw {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}

	return out
}

func copySecretRefs(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func encodeWorkflowID(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(path)))
}

func decodeWorkflowID(value string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(decoded))
	if path == "" {
		return "", fmt.Errorf("empty workflow identifier")
	}
	return path, nil
}

func sanitizeSecretScope(raw string) string {
	scope := strings.TrimSpace(strings.ToLower(raw))
	if scope == "" {
		return "global"
	}
	return scope
}

func sanitizeSecretName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	value = strings.ToUpper(value)
	value = strings.ReplaceAll(value, " ", "_")

	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_':
			return r
		default:
			return -1
		}
	}, value)

	return value
}

func scopeKey(scope, name string) string {
	scope = sanitizeSecretScope(scope)
	name = sanitizeSecretName(name)
	if scope == "" {
		scope = "global"
	}
	return scope + "\x1f" + name
}

func splitScopeKey(key string) (string, string) {
	parts := strings.SplitN(key, "\x1f", 2)
	if len(parts) != 2 {
		return "global", key
	}
	if parts[0] == "" {
		parts[0] = "global"
	}
	return parts[0], parts[1]
}

func parseSecretRef(raw string) (string, string, error) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return "", "", fmt.Errorf("empty secret reference")
	}

	// Support "scope:NAME" and global defaults.
	if idx := strings.Index(clean, ":"); idx >= 0 {
		scope := sanitizeSecretScope(clean[:idx])
		name := sanitizeSecretName(clean[idx+1:])
		if name == "" {
			return "", "", fmt.Errorf("missing secret name")
		}
		return scope, name, nil
	}

	return "global", sanitizeSecretName(clean), nil
}

func parseCronInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, fmt.Errorf("interval is required")
	}

	if duration, err := time.ParseDuration(raw); err == nil {
		if duration <= 0 {
			return 0, fmt.Errorf("interval must be positive")
		}
		return duration, nil
	}

	return 0, fmt.Errorf("invalid interval: %s", raw)
}

type pipelineArgs struct {
	File     string
	Workdir  string
	Provider string
	Strict   bool
}

type validateResponse struct {
	Valid  bool   `json:"valid"`
	Output string `json:"output"`
}

type serveState struct {
	apiPrefix         string
	staticDir         string
	defaultWorkdir    string
	runs              *runRegistry
	maxLogEntries     int
	maxRunEntries     int
	secretStore       *secretRegistry
	cronRuns          *cronRunRegistry
	hookEvents        []hookEvent
	hookMu            sync.RWMutex
	maxHookEvents     int
	hookSecretGitHub  string
	hookSecretGitLab  string
	hookWorkdirGitHub string
	hookWorkdirGitLab string
}

var serviceStartedAt = time.Now()

type jobInfo struct {
	Name        string   `json:"name"`
	Stage       string   `json:"stage"`
	Runner      string   `json:"runner"`
	Needs       []string `json:"needs"`
	ScriptCount int      `json:"scriptCount"`
	StepCount   int      `json:"stepCount"`
}

type jobsResponse struct {
	File    string    `json:"file"`
	Workdir string    `json:"workdir"`
	Jobs    []jobInfo `json:"jobs"`
}

type systemHealthResponse struct {
	Status          string `json:"status"`
	Time            string `json:"time"`
	Uptime          string `json:"uptime"`
	GoVersion       string `json:"goVersion"`
	GoRoutines      int    `json:"goRoutines"`
	CPUs            int    `json:"cpus"`
	RunningRuns     int    `json:"runningRuns"`
	PendingRuns     int    `json:"pendingRuns"`
	SucceededRuns   int    `json:"succeededRuns"`
	FailedRuns      int    `json:"failedRuns"`
	CanceledRuns    int    `json:"canceledRuns"`
	TotalRuns       int    `json:"totalRuns"`
	CachedRuns      int    `json:"cachedRuns"`
	DefaultWorkdir  string `json:"defaultWorkdir"`
	APIPath         string `json:"apiPath"`
	HeapObjects     uint64 `json:"heapObjects"`
	HeapAllocBytes  uint64 `json:"heapAllocBytes"`
	StackInUseBytes uint64 `json:"stackInUseBytes"`
	NumGC           uint32 `json:"numGC"`
}

type stackDumpResponse struct {
	Timestamp          string            `json:"timestamp"`
	Uptime             string            `json:"uptime"`
	GoVersion          string            `json:"goVersion"`
	GOMAXPROCS         int               `json:"gomaxprocs"`
	NumCPU             int               `json:"numCPU"`
	Goroutines         int               `json:"goroutines"`
	HeapAllocBytes     uint64            `json:"heapAllocBytes"`
	HeapObjects        uint64            `json:"heapObjects"`
	HeapSysBytes       uint64            `json:"heapSysBytes"`
	HeapInUseBytes     uint64            `json:"heapInUseBytes"`
	StackInUseBytes    uint64            `json:"stackInUseBytes"`
	PauseTotalNs       uint64            `json:"pauseTotalNs"`
	NumGC              uint32            `json:"numGC"`
	NumForcedGC        uint32            `json:"numForcedGC"`
	NextGCBytes        uint64            `json:"nextGCBytes"`
	LastGCTimeUnixNano uint64            `json:"lastGCTimeUnixNano"`
	StackSampleSize    int               `json:"stackSampleSize"`
	StackTrace         string            `json:"stackTrace"`
	ActiveRuns         int               `json:"activeRuns"`
	RecentRuns         []stackRunSummary `json:"recentRuns"`
}

type stackRunSummary struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Workdir    string    `json:"workdir"`
	File       string    `json:"file"`
	Repository string    `json:"repository"`
	Ref        string    `json:"ref"`
	StartedAt  time.Time `json:"startedAt"`
	ExitCode   int       `json:"exitCode"`
}

type workflowRecord struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	File          string `json:"file"`
	Provider      string `json:"provider"`
	Detected      bool   `json:"detected"`
	Jobs          int    `json:"jobs"`
	UpdatedAtUnix int64  `json:"updatedAtUnix"`
	Workdir       string `json:"workdir"`
	Path          string `json:"path"`
}

type workflowCatalogResponse struct {
	OK        bool             `json:"ok"`
	Workdir   string           `json:"workdir"`
	Directory string           `json:"directory"`
	Generated string           `json:"generatedAt"`
	Workflows []workflowRecord `json:"workflows"`
	Requested map[string]any   `json:"request"`
}

type secretEntry struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Masked    bool   `json:"masked"`
}

type secretPayload struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

type workflowSecret struct {
	Name  string
	Value string
}

type secretRegistry struct {
	mu      sync.RWMutex
	entries map[string]workflowSecret
}

func newSecretRegistry() *secretRegistry {
	return &secretRegistry{entries: make(map[string]workflowSecret)}
}

func (s *secretRegistry) put(scope, name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[scopeKey(scope, name)] = workflowSecret{Name: sanitizeSecretName(name), Value: value}
}

func (s *secretRegistry) get(scope, name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.entries[scopeKey(scope, name)]
	if !ok {
		entry, ok = s.entries[scopeKey("global", name)]
		if !ok {
			return "", false
		}
	}

	return entry.Value, true
}

func (s *secretRegistry) list(scope string) []secretEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]secretEntry, 0)
	for key, entry := range s.entries {
		_, entryScope := splitScopeKey(key)
		if scope != "" && scope != entryScope {
			continue
		}

		out = append(out, secretEntry{
			Name:      entry.Name,
			Scope:     entryScope,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			Masked:    true,
		})
	}

	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *secretRegistry) remove(scope, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := scopeKey(scope, name)
	if _, ok := s.entries[key]; !ok {
		return false
	}

	delete(s.entries, key)
	return true
}

type cronRun struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Repository   string    `json:"repository"`
	Workdir      string    `json:"workdir"`
	Workflow     string    `json:"workflow"`
	WorkflowFile string    `json:"workflowFile"`
	Ref          string    `json:"ref"`
	SecretRefs   []string  `json:"secretRefs"`
	Interval     string    `json:"interval"`
	NextRunAt    time.Time `json:"nextRunAt"`
	LastRunAt    time.Time `json:"lastRunAt,omitempty"`
	LastRunID    string    `json:"lastRunId,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	PausedReason string    `json:"pausedReason,omitempty"`
}

type cronRunRegistry struct {
	mu   sync.RWMutex
	runs map[string]*cronRun
}

func newCronRunRegistry() *cronRunRegistry {
	return &cronRunRegistry{runs: make(map[string]*cronRun)}
}

func (r *cronRunRegistry) list() []cronRun {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]cronRun, 0, len(r.runs))
	for _, item := range r.runs {
		out = append(out, *item)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (r *cronRunRegistry) get(id string) (*cronRun, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	run, ok := r.runs[id]
	return run, ok
}

func (r *cronRunRegistry) put(run cronRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run.ID] = &run
}

func (r *cronRunRegistry) delete(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.runs[id]; !ok {
		return false
	}
	delete(r.runs, id)
	return true
}

func (r *cronRunRegistry) touch(id string, updater func(*cronRun)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	run, ok := r.runs[id]
	if !ok {
		return false
	}
	updater(run)
	return true
}

type featurePlan struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Description string   `json:"description"`
	Notes       []string `json:"notes"`
	Endpoints   []string `json:"endpoints"`
}

type featurePlanResponse struct {
	OK         bool        `json:"ok"`
	Feature    string      `json:"feature"`
	Status     string      `json:"status"`
	Message    string      `json:"message"`
	ReceivedAt string      `json:"receivedAt"`
	Plan       featurePlan `json:"plan"`
	Request    any         `json:"request,omitempty"`
}

type featureCatalogResponse struct {
	OK           bool                   `json:"ok"`
	Version      string                 `json:"version"`
	API          string                 `json:"api"`
	Generated    string                 `json:"generatedAt"`
	Capabilities map[string]featurePlan `json:"features"`
}

var plannedFeatureCatalog = map[string]featurePlan{
	"workflows": {
		Name:        "workflows",
		Status:      implementedFeatureStatus,
		Description: "Repository workflow discovery and dispatch endpoints.",
		Notes: []string{
			"Workflow discovery reads from local and remote repositories.",
			"Use `/workflows/{workflowId}/dispatch` for immediate workflow execution.",
		},
		Endpoints: []string{
			"/workflows",
			"/workflows/{workflowId}",
			"/workflows/{workflowId}/dispatch",
		},
	},
	"secrets": {
		Name:        "secrets",
		Status:      implementedFeatureStatus,
		Description: "Ephemeral in-memory secret storage with run-scoped injection.",
		Notes: []string{
			"Secrets are stored in-memory and never written to disk by default.",
			"Reference secrets by name and include `secretRefs` in run payloads.",
		},
		Endpoints: []string{
			"/secrets",
			"/secrets/{name}",
		},
	},
	"cron-runs": {
		Name:        "cron-runs",
		Status:      implementedFeatureStatus,
		Description: "Scheduled and recurring run definitions for repo workflows.",
		Notes: []string{
			"Cron-like triggers use interval-style schedules for in-memory dispatch.",
			"Recurring runs preserve last run IDs and execution metadata.",
		},
		Endpoints: []string{
			"/cron-runs",
			"/cron-runs/{id}",
			"/cron-runs/{id}/run",
			"/cron-runs/{id}/pause",
			"/cron-runs/{id}/resume",
		},
	},
	"github-actions": {
		Name:        "github-actions",
		Status:      implementedFeatureStatus,
		Description: "GitHub Actions and GitLab-style control primitives for VPS-native CI jobs.",
		Notes: []string{
			"Workflow dispatch is available through workflow endpoints and run APIs.",
			"/webhook/github is already usable for push-like triggers.",
		},
		Endpoints: []string{
			"/features/github-actions",
			"/workflows",
			"/workflows/{workflowId}",
			"/workflows/{workflowId}/dispatch",
			"/secrets",
			"/secrets/{name}",
			"/cron-runs",
			"/cron-runs/{id}/pause",
			"/cron-runs/{id}/resume",
			"/cron-runs/{id}/run",
			"/webhook/github",
			"/webhook/gitlab",
		},
	},
	"runs": {
		Name:        "runs",
		Status:      implementedFeatureStatus,
		Description: "Run lifecycle primitives for listing, log streaming, cancellation and retries.",
		Notes: []string{
			"Runs are tracked in-memory by default in process memory.",
			"Include `/runs/{id}/logs`, `/runs/{id}/retry` and `/runs/{id}/cancel` for control-plane operations.",
		},
		Endpoints: []string{
			"/runs",
			"/runs/{id}",
			"/runs/{id}/logs",
			"/runs/{id}/retry",
			"/runs/{id}/cancel",
		},
	},
	"webhooks": {
		Name:        "webhooks",
		Status:      implementedFeatureStatus,
		Description: "GitHub and GitLab webhook trigger endpoints with event metadata and audit trail.",
		Notes: []string{
			"HMAC / token verification can be enabled with CLI webhook secrets.",
			"Accepted webhook events are normalized into the in-memory event log.",
		},
		Endpoints: []string{
			"/webhooks",
			"/webhook/github",
			"/webhook/gitlab",
		},
	},
}

func serveJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func serveError(w http.ResponseWriter, status int, msg string) {
	serveJSON(w, status, map[string]string{"error": msg})
}

func normalizeAPIPath(prefix string) string {
	if prefix == "" {
		return "/api"
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimSuffix(prefix, "/")
}

func buildAPIPrefixes(prefix string) []string {
	normalized := normalizeAPIPath(prefix)
	prefixes := []string{normalized}

	versioned := normalized + "/v1"
	if !strings.HasSuffix(normalized, "/v1") && versioned != normalized {
		prefixes = append(prefixes, versioned)
	}

	return prefixes
}

func isAPIRoute(prefix, pathValue string) bool {
	for _, apiPrefix := range buildAPIPrefixes(prefix) {
		if pathValue == apiPrefix || strings.HasPrefix(pathValue, apiPrefix+"/") {
			return true
		}
	}
	return false
}

func parseBoolValue(raw string) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "1", "true", "yes", "on", "y":
		return true
	case "0", "false", "off", "no", "n":
		return false
	default:
		return false
	}
}

func parseBoolValueWithState(raw string) (bool, bool) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return false, false
	}
	return parseBoolValue(raw), true
}

func parseCSVValue(raw string) []string {
	if raw == "" {
		return nil
	}
	return toLowerTrimmedCSV(raw)
}

func toLowerTrimmedCSV(raw string) []string {
	values := strings.Split(raw, ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func appendWebhookEvent(items []hookEvent, item hookEvent, limit int) []hookEvent {
	items = append(items, item)
	if len(items) <= limit {
		return items
	}
	return items[len(items)-limit:]
}

func firstString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func mapString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := data[key]
		if !ok {
			continue
		}
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

func normalizeRepositoryRef(raw string) string {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return ""
	}
	ref = strings.TrimPrefix(ref, "refs/")
	ref = strings.TrimPrefix(ref, "heads/")
	return ref
}

func isLikelyRemoteRepository(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}

	if strings.HasPrefix(value, "git@") ||
		strings.HasPrefix(value, "http://") ||
		strings.HasPrefix(value, "https://") ||
		strings.HasPrefix(value, "ssh://") ||
		strings.HasSuffix(value, ".git") {
		return true
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		return true
	}

	if _, err := os.Stat(value); err == nil {
		return false
	}

	return strings.Count(value, "/") > 0
}

func sanitizeRepositoryName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}

	value = strings.TrimSuffix(value, ".git")
	value = strings.TrimPrefix(value, "git@")
	if idx := strings.Index(value, ":"); idx >= 0 {
		value = value[idx+1:]
	}
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "/", "_")

	return repoNameCleaner.ReplaceAllString(value, "_")
}

func repositoryInfoFromGitHub(raw map[string]any) (name string, repositoryURL string) {
	name = firstString(
		mapString(raw, "full_name", "path_with_namespace", "name"),
	)
	repositoryURL = firstString(
		mapString(raw, "html_url", "clone_url", "git_url", "ssh_url", "git@"),
	)

	return name, repositoryURL
}

func repositoryInfoFromGitLab(raw map[string]any) (name string, repositoryURL string) {
	name = firstString(
		mapString(raw, "path_with_namespace", "name", "path"),
	)
	repositoryURL = firstString(
		mapString(raw, "http_url_to_repo", "web_url", "git_http_url", "git_ssh_url"),
	)
	return name, repositoryURL
}

func (s *serveState) addWebhookEvent(item hookEvent) {
	s.hookMu.Lock()
	defer s.hookMu.Unlock()

	if item.ID == "" {
		item.ID = fmt.Sprintf("hook-%d", time.Now().UnixNano())
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now()
	}
	s.hookEvents = appendWebhookEvent(s.hookEvents, item, s.maxHookEvents)
}

func (s *serveState) listWebhookEvents() []hookEvent {
	s.hookMu.RLock()
	defer s.hookMu.RUnlock()

	out := make([]hookEvent, 0, len(s.hookEvents))
	for _, item := range s.hookEvents {
		out = append(out, item)
	}
	return out
}

func verifyGitHubSignature(secret string, signature string, payload []byte) bool {
	if secret == "" {
		return true
	}
	if signature == "" {
		return false
	}

	expected := strings.TrimPrefix(signature, "sha256=")
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	calculated := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(calculated)), []byte(strings.ToLower(expected)))
}

func generateRunID() string {
	randomBytes := make([]byte, 4)
	if _, err := rand.Read(randomBytes); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("run-%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(randomBytes))
}

func runCLIPipeOutput(ctx context.Context, binary string, args []string, workdir string) (string, string, error) {
	cmd := runCommandContext(ctx, binary, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func runCLIJSONOutput(ctx context.Context, args []string, workdir string) (any, error) {
	binaryPath, err := resolveExecutable()
	if err != nil {
		return nil, fmt.Errorf("failed to locate binary: %w", err)
	}

	output, stderr, err := runCLIPipeOutput(ctx, binaryPath, args, workdir)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}

	var payload any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}
	return payload, nil
}

func buildJobPayload(payload any) ([]jobInfo, error) {
	container, ok := payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid pipeline payload")
	}

	rawJobs, ok := container["jobs"]
	if !ok {
		return []jobInfo{}, nil
	}

	out := make([]jobInfo, 0)
	switch jobs := rawJobs.(type) {
	case map[string]any:
		for name, raw := range jobs {
			data, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, buildJobInfo(name, data))
		}
	case []any:
		for _, raw := range jobs {
			data, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := data["name"].(string)
			if name == "" {
				name = "[unknown]"
			}
			out = append(out, buildJobInfo(name, data))
		}
	default:
		return nil, fmt.Errorf("unexpected jobs payload type")
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Stage == out[j].Stage {
			return out[i].Name < out[j].Name
		}
		return out[i].Stage < out[j].Stage
	})

	return out, nil
}

func buildJobInfo(name string, data map[string]any) jobInfo {
	job := jobInfo{Name: name}
	if stage, ok := data["stage"].(string); ok {
		job.Stage = stage
	}
	if runner, ok := data["runs-on"].(string); ok {
		job.Runner = runner
	}
	if image, ok := data["image"].(string); ok && job.Runner == "" {
		job.Runner = image
	}
	if envImage, ok := data["container"].(map[string]any); ok {
		if i, ok := envImage["image"].(string); ok && job.Runner == "" {
			job.Runner = i
		}
	}
	if needsRaw, ok := data["needs"].([]any); ok {
		for _, v := range needsRaw {
			if item, ok := v.(string); ok {
				job.Needs = append(job.Needs, item)
			}
		}
	}
	if scripts, ok := data["script"].([]any); ok {
		job.ScriptCount = len(scripts)
	}
	if steps, ok := data["steps"].([]any); ok {
		job.StepCount = len(steps)
	}
	if script, ok := data["script"].([]string); ok {
		job.ScriptCount = len(script)
	}
	return job
}

func buildRunCommandArgs(req runExecutionRequest, defaultWorkdir string) []string {
	effectiveWorkdir := strings.TrimSpace(req.Workdir)
	if effectiveWorkdir == "" {
		effectiveWorkdir = strings.TrimSpace(defaultWorkdir)
	}
	if effectiveWorkdir == "" {
		effectiveWorkdir = "."
	}

	args := []string{"--workdir", effectiveWorkdir, "run"}
	if req.File != "" {
		args = append(args, "--file", req.File)
	}
	if req.Job != "" {
		args = append(args, "--job", req.Job)
	}
	if req.Stage != "" {
		args = append(args, "--stage", req.Stage)
	}
	for _, v := range req.Only {
		if strings.TrimSpace(v) != "" {
			args = append(args, "--only", v)
		}
	}
	for _, v := range req.Except {
		if strings.TrimSpace(v) != "" {
			args = append(args, "--except", v)
		}
	}
	if req.Parallel {
		args = append(args, "--parallel")
	}
	if req.MaxParallel > 0 {
		args = append(args, "--max-parallel", strconv.Itoa(req.MaxParallel))
	}
	if req.ContinueOnErr {
		args = append(args, "--continue-on-error")
	}
	if req.Docker {
		args = append(args, "--docker")
	}
	if req.Podman {
		args = append(args, "--podman")
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}
	if req.Timeout > 0 {
		args = append(args, "--timeout", strconv.Itoa(req.Timeout))
	}
	for _, envValue := range req.Env {
		if strings.TrimSpace(envValue) != "" {
			args = append(args, "--env", envValue)
		}
	}
	if req.EnvFile != "" {
		args = append(args, "--env-file", req.EnvFile)
	}
	if req.NoCache {
		args = append(args, "--no-cache")
	}
	if req.NoPull {
		args = append(args, "--pull=false")
	}
	if len(req.Volume) > 0 {
		for _, volume := range req.Volume {
			if strings.TrimSpace(volume) != "" {
				args = append(args, "--volume", volume)
			}
		}
	}
	if req.Network != "" {
		args = append(args, "--network", req.Network)
	}
	if req.Memory != "" {
		args = append(args, "--memory", req.Memory)
	}
	if req.CPUs != "" {
		args = append(args, "--cpus", req.CPUs)
	}
	if req.Verbose || req.Debug {
		args = append(args, "--verbose")
	}
	if req.Quiet {
		args = append(args, "--quiet")
	}
	return args
}

func parsePipelineArgs(r *http.Request) pipelineArgs {
	return pipelineArgs{
		File:     strings.TrimSpace(r.URL.Query().Get("file")),
		Workdir:  strings.TrimSpace(r.URL.Query().Get("workdir")),
		Provider: strings.TrimSpace(r.URL.Query().Get("provider")),
		Strict:   strings.EqualFold(r.URL.Query().Get("strict"), "1") || strings.EqualFold(r.URL.Query().Get("strict"), "true"),
	}
}

func (s *serveState) staticHandler(w http.ResponseWriter, r *http.Request) {
	if isAPIRoute(s.apiPrefix, r.URL.Path) {
		serveError(w, http.StatusNotFound, "not found")
		return
	}

	requestPath := filepath.Clean(r.URL.Path)
	if requestPath == "." || requestPath == "/" {
		requestPath = "index.html"
	}
	requestPath = strings.TrimPrefix(requestPath, "/")

	target := filepath.Join(s.staticDir, requestPath)
	if stat, err := os.Stat(target); err == nil && !stat.IsDir() {
		http.ServeFile(w, r, target)
		return
	}
	if strings.Contains(requestPath, ".") {
		serveError(w, http.StatusNotFound, "asset not found")
		return
	}
	http.ServeFile(w, r, filepath.Join(s.staticDir, "index.html"))
}

func (s *serveState) handleHealth(w http.ResponseWriter, r *http.Request) {
	serveJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *serveState) handleStackDump(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	runs := s.runs.list()
	runSummaries := make([]stackRunSummary, 0, min(len(runs), 6))
	activeRuns := 0
	for _, run := range runs {
		snapshot := run.snapshot()
		if snapshot.Status == runStatusRunning || snapshot.Status == runStatusPending {
			activeRuns++
		}
		if len(runSummaries) < 6 {
			runSummaries = append(runSummaries, stackRunSummary{
				ID:         snapshot.ID,
				Status:     snapshot.Status,
				Workdir:    snapshot.Workdir,
				File:       snapshot.File,
				Repository: snapshot.Repository,
				Ref:        snapshot.Ref,
				StartedAt:  snapshot.StartedAt,
				ExitCode:   snapshot.ExitCode,
			})
		}
	}

	sort.Slice(runSummaries, func(i, j int) bool {
		return runSummaries[i].StartedAt.After(runSummaries[j].StartedAt)
	})

	buffer := make([]byte, 64*1024)
	for {
		size := runtime.Stack(buffer, true)
		if size < len(buffer) {
			buffer = buffer[:size]
			break
		}
		buffer = make([]byte, len(buffer)*2)
	}

	serveJSON(w, http.StatusOK, stackDumpResponse{
		Timestamp:          time.Now().UTC().Format(time.RFC3339),
		Uptime:             time.Since(serviceStartedAt).Round(time.Second).String(),
		GoVersion:          runtime.Version(),
		GOMAXPROCS:         runtime.GOMAXPROCS(0),
		NumCPU:             runtime.NumCPU(),
		Goroutines:         runtime.NumGoroutine(),
		HeapAllocBytes:     memStats.HeapAlloc,
		HeapObjects:        memStats.HeapObjects,
		HeapSysBytes:       memStats.HeapSys,
		HeapInUseBytes:     memStats.HeapInuse,
		StackInUseBytes:    memStats.StackInuse,
		PauseTotalNs:       memStats.PauseTotalNs,
		NumGC:              memStats.NumGC,
		NumForcedGC:        memStats.NumForcedGC,
		NextGCBytes:        memStats.NextGC,
		LastGCTimeUnixNano: memStats.LastGC,
		ActiveRuns:         activeRuns,
		RecentRuns:         runSummaries,
		StackSampleSize:    len(buffer),
		StackTrace:         strings.TrimSuffix(string(buffer), "\n") + "\n" + string(debug.Stack()),
	})
}

func (s *serveState) handleAPIRoot(apiPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			serveError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		runCount := len(s.runs.list())
		serveJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"version":   "v1",
			"apiPrefix": apiPrefix,
			"routes": map[string]string{
				"health":               apiPrefix + "/health",
				"system":               apiPrefix + "/system",
				"runs":                 apiPrefix + "/runs",
				"runById":              apiPrefix + "/runs/{id}",
				"runLogs":              apiPrefix + "/runs/{id}/logs",
				"runRetry":             apiPrefix + "/runs/{id}/retry",
				"runCancel":            apiPrefix + "/runs/{id}/cancel",
				"pipelines":            apiPrefix + "/pipelines",
				"jobs":                 apiPrefix + "/jobs",
				"validate":             apiPrefix + "/validate",
				"discover":             apiPrefix + "/discover",
				"stack":                apiPrefix + "/stack",
				"webhooks":             apiPrefix + "/webhooks",
				"features":             apiPrefix + "/features",
				"workflows":            apiPrefix + "/workflows",
				"secrets":              apiPrefix + "/secrets",
				"cronRuns":             apiPrefix + "/cron-runs",
				"workflowsById":        apiPrefix + "/workflows/{workflowId}",
				"workflowDispatchById": apiPrefix + "/workflows/{workflowId}/dispatch",
				"secretByName":         apiPrefix + "/secrets/{name}",
				"cronRunById":          apiPrefix + "/cron-runs/{id}",
				"cronRunPause":         apiPrefix + "/cron-runs/{id}/pause",
				"cronRunResume":        apiPrefix + "/cron-runs/{id}/resume",
				"cronRunImmediateRun":  apiPrefix + "/cron-runs/{id}/run",
				"github":               apiPrefix + "/webhook/github",
				"gitlab":               apiPrefix + "/webhook/gitlab",
			},
			"runSummary": map[string]int{
				"totalRuns": runCount,
			},
		})
	}
}

func (s *serveState) handleFeatureCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	serveJSON(w, http.StatusOK, featureCatalogResponse{
		OK:           true,
		Version:      "v1",
		API:          s.apiPrefix,
		Generated:    time.Now().UTC().Format(time.RFC3339),
		Capabilities: plannedFeatureCatalog,
	})
}

func (s *serveState) handleFeatureContract(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, exists := plannedFeatureCatalog[feature]
		if !exists {
			serveError(w, http.StatusNotFound, "feature not found")
			return
		}

		switch r.Method {
		case http.MethodGet:
			serveJSON(w, http.StatusOK, featurePlanResponse{
				OK:         true,
				Feature:    plan.Name,
				Status:     plan.Status,
				Message:    "Feature is planned and contract-safe for now.",
				ReceivedAt: time.Now().UTC().Format(time.RFC3339),
				Plan:       plan,
			})
			return
		case http.MethodPost:
			var payload any
			if r.Body != nil {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
					serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid request payload: %v", err))
					return
				}
			}

			serveJSON(w, http.StatusAccepted, featurePlanResponse{
				OK:         true,
				Feature:    plan.Name,
				Status:     plan.Status,
				Message:    "Feature is planned. Request accepted for future workflow execution.",
				ReceivedAt: time.Now().UTC().Format(time.RFC3339),
				Plan:       plan,
				Request:    payload,
			})
			return
		default:
			serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func (s *serveState) handleFeatureByName(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, s.apiPrefix+"/features/")
	feature := strings.Trim(strings.TrimSpace(trimmed), "/")
	if feature == "" {
		serveError(w, http.StatusBadRequest, "missing feature name")
		return
	}
	slice := strings.Split(feature, "/")
	if len(slice) > 1 {
		serveError(w, http.StatusNotFound, "unsupported feature subroute")
		return
	}
	s.handleFeatureContract(slice[0])(w, r)
}

func (s *serveState) discoverWorkflowCatalog(ctx context.Context, workdir string, directory string) ([]workflowRecord, error) {
	binaryPath, err := resolveExecutable()
	if err != nil {
		return nil, fmt.Errorf("failed to locate binary: %w", err)
	}

	if workdir == "" {
		workdir = s.defaultWorkdir
	}
	if workdir == "" {
		workdir = "."
	}

	execArgs := []string{"--workdir", workdir, "discover", "--format", "json"}
	if strings.TrimSpace(directory) != "" {
		execArgs = append(execArgs, "--directory", directory)
	}

	output, _, err := runCLIPipeOutput(ctx, binaryPath, execArgs, workdir)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Directory string `json:"directory"`
		Total     int    `json:"total"`
		Files     []struct {
			Path     string `json:"path"`
			Provider string `json:"provider"`
			Jobs     int    `json:"jobs"`
			Detected bool   `json:"detected"`
		} `json:"files"`
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse discovery payload: %w", err)
	}

	if payload.Files == nil {
		payload.Files = []struct {
			Path     string `json:"path"`
			Provider string `json:"provider"`
			Jobs     int    `json:"jobs"`
			Detected bool   `json:"detected"`
		}{}
	}

	records := make([]workflowRecord, 0, len(payload.Files))
	recordedWorkdir := strings.TrimSpace(payload.Directory)
	if recordedWorkdir == "" {
		recordedWorkdir = workdir
	}
	for _, file := range payload.Files {
		filePath := strings.TrimSpace(file.Path)
		if filePath == "" {
			continue
		}

		records = append(records, workflowRecord{
			ID:            encodeWorkflowID(filePath),
			Name:          filepath.Base(filePath),
			File:          filePath,
			Path:          filePath,
			Workdir:       recordedWorkdir,
			Provider:      file.Provider,
			Jobs:          file.Jobs,
			Detected:      file.Detected,
			UpdatedAtUnix: time.Now().Unix(),
		})
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].Provider != records[j].Provider {
			return records[i].Provider < records[j].Provider
		}
		return records[i].Path < records[j].Path
	})

	return records, nil
}

func (s *serveState) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	workdir := strings.TrimSpace(r.URL.Query().Get("workdir"))
	directory := strings.TrimSpace(r.URL.Query().Get("directory"))
	if workdir == "" {
		workdir = s.defaultWorkdir
	}
	if workdir == "" {
		workdir = "."
	}

	switch r.Method {
	case http.MethodGet:
		records, err := s.discoverWorkflowCatalog(r.Context(), workdir, directory)
		if err != nil {
			serveError(w, http.StatusBadRequest, err.Error())
			return
		}

		response := workflowCatalogResponse{
			OK:        true,
			Workdir:   workdir,
			Directory: directory,
			Generated: time.Now().UTC().Format(time.RFC3339),
			Workflows: records,
			Requested: map[string]any{"workdir": workdir, "directory": directory},
		}

		serveJSON(w, http.StatusOK, response)
	case http.MethodPost:
		req, err := parseRunRequest(r)
		if err != nil {
			serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if req.File == "" {
			req.File = strings.TrimSpace(r.URL.Query().Get("file"))
		}

		if req.File == "" {
			serveError(w, http.StatusBadRequest, "workflow dispatch requires file")
			return
		}

		if req.Workdir == "" {
			req.Workdir = workdir
		}
		if req.MaxLogEntries <= 0 {
			req.MaxLogEntries = s.maxLogEntries
		}

		session, err := s.runCommandAsync(context.Background(), req)
		if err != nil {
			serveError(w, http.StatusBadRequest, err.Error())
			return
		}

		serveJSON(w, http.StatusCreated, session.snapshot())
	default:
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *serveState) handleWorkflowByName(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, s.apiPrefix+"/workflows/")
	trimmed = strings.Trim(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		serveError(w, http.StatusNotFound, "missing workflow id")
		return
	}

	workflowID := parts[0]
	if workflowID == "" {
		serveError(w, http.StatusNotFound, "missing workflow id")
		return
	}

	if len(parts) == 1 {
		filePath, err := decodeWorkflowID(workflowID)
		if err != nil {
			serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid workflow id: %v", err))
			return
		}

		if r.Method == http.MethodGet {
			payload := map[string]any{"id": workflowID, "file": filePath}
			runs := s.runs.list()
			for _, run := range runs {
				if run.File == filePath {
					payload["lastRun"] = run.snapshot()
					break
				}
			}
			serveJSON(w, http.StatusOK, payload)
			return
		}

		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if parts[1] != "dispatch" {
		serveError(w, http.StatusNotFound, "unsupported workflow route")
		return
	}

	if r.Method != http.MethodPost {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	filePath, err := decodeWorkflowID(workflowID)
	if err != nil {
		serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid workflow id: %v", err))
		return
	}

	req, err := parseRunRequest(r)
	if err != nil {
		serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if req.File == "" {
		req.File = filePath
	}

	session, err := s.runCommandAsync(context.Background(), req)
	if err != nil {
		serveError(w, http.StatusBadRequest, err.Error())
		return
	}
	serveJSON(w, http.StatusCreated, session.snapshot())
}

func (s *serveState) handleSecrets(w http.ResponseWriter, r *http.Request) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	scope = sanitizeSecretScope(scope)

	switch r.Method {
	case http.MethodGet:
		secretList := s.secretStore.list(scope)
		serveJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"scope": scope,
			"count": len(secretList),
			"items": secretList,
		})
	case http.MethodPost:
		payload := secretPayload{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid payload: %v", err))
			return
		}
		name := sanitizeSecretName(payload.Name)
		if name == "" {
			serveError(w, http.StatusBadRequest, "secret name is required")
			return
		}
		if strings.TrimSpace(payload.Value) == "" {
			serveError(w, http.StatusBadRequest, "secret value is required")
			return
		}

		if payload.Scope == "" {
			payload.Scope = scope
		}

		s.secretStore.put(payload.Scope, name, payload.Value)
		serveJSON(w, http.StatusAccepted, map[string]any{
			"ok":     true,
			"name":   name,
			"scope":  payload.Scope,
			"status": "stored",
		})
	default:
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *serveState) handleSecretByName(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, s.apiPrefix+"/secrets/")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		serveError(w, http.StatusNotFound, "missing secret name")
		return
	}

	name, err := url.PathUnescape(trimmed)
	if err != nil {
		serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid secret name: %v", err))
		return
	}
	name = sanitizeSecretName(name)
	if name == "" {
		serveError(w, http.StatusBadRequest, "invalid secret name")
		return
	}

	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	scope = sanitizeSecretScope(scope)

	switch r.Method {
	case http.MethodGet:
		value, found := s.secretStore.get(scope, name)
		if !found {
			serveError(w, http.StatusNotFound, "secret not found")
			return
		}
		masked := strings.Repeat("*", min(len(value), 8))
		revealed := strings.EqualFold(r.URL.Query().Get("reveal"), "1") || strings.EqualFold(r.URL.Query().Get("reveal"), "true")
		var resolvedValue any
		if revealed {
			resolvedValue = value
		}

		serveJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"name":     name,
			"scope":    scope,
			"masked":   masked,
			"present":  true,
			"valueLen": len(value),
			"revealed": revealed,
			"value":    resolvedValue,
		})
	case http.MethodDelete:
		if !s.secretStore.remove(scope, name) {
			serveError(w, http.StatusNotFound, "secret not found")
			return
		}
		serveJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": name, "scope": scope})
	default:
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *serveState) handleCronRuns(w http.ResponseWriter, r *http.Request) {
	workdir := strings.TrimSpace(r.URL.Query().Get("workdir"))
	if workdir == "" {
		workdir = s.defaultWorkdir
	}

	switch r.Method {
	case http.MethodGet:
		items := s.cronRuns.list()
		serveJSON(w, http.StatusOK, items)
	case http.MethodPost:
		payload := struct {
			Name         string   `json:"name"`
			Workdir      string   `json:"workdir"`
			Repository   string   `json:"repository"`
			Workflow     string   `json:"workflow"`
			WorkflowFile string   `json:"workflowFile"`
			Ref          string   `json:"ref"`
			SecretRefs   []string `json:"secretRefs"`
			Interval     string   `json:"interval"`
		}{
			Workdir: workdir,
		}

		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid payload: %v", err))
			return
		}

		if strings.TrimSpace(payload.Workflow) != "" {
			payload.Workflow = strings.TrimSpace(payload.Workflow)
		}

		if strings.TrimSpace(payload.WorkflowFile) == "" && strings.TrimSpace(payload.Workflow) != "" {
			payload.WorkflowFile = strings.TrimSpace(payload.Workflow)
		}

		if strings.TrimSpace(payload.Interval) == "" {
			serveError(w, http.StatusBadRequest, "interval is required")
			return
		}

		interval, err := parseCronInterval(payload.Interval)
		if err != nil {
			serveError(w, http.StatusBadRequest, err.Error())
			return
		}

		if payload.WorkflowFile == "" {
			serveError(w, http.StatusBadRequest, "workflowFile is required")
			return
		}

		if payload.Name == "" {
			payload.Name = filepath.Base(payload.WorkflowFile)
		}

		entry := cronRun{
			ID:           generateRunID(),
			Name:         payload.Name,
			Workdir:      workdir,
			Repository:   strings.TrimSpace(payload.Repository),
			Workflow:     strings.TrimSpace(payload.Workflow),
			WorkflowFile: payload.WorkflowFile,
			Ref:          strings.TrimSpace(payload.Ref),
			SecretRefs:   copySecretRefs(payload.SecretRefs),
			Interval:     payload.Interval,
			Status:       runCronStatusActive,
			NextRunAt:    time.Now().UTC().Add(interval),
			CreatedAt:    time.Now().UTC(),
			UpdatedAt:    time.Now().UTC(),
		}

		s.cronRuns.put(entry)
		serveJSON(w, http.StatusCreated, entry)
	default:
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *serveState) handleCronRunByID(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, s.apiPrefix+"/cron-runs/")
	trimmed = strings.Trim(trimmed, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		serveError(w, http.StatusNotFound, "missing cron id")
		return
	}

	id := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, ok := s.cronRuns.get(id)
			if !ok {
				serveError(w, http.StatusNotFound, "cron run not found")
				return
			}
			serveJSON(w, http.StatusOK, item)
		case http.MethodDelete:
			if !s.cronRuns.delete(id) {
				serveError(w, http.StatusNotFound, "cron run not found")
				return
			}
			serveJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
		default:
			serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if len(parts) > 2 {
		serveError(w, http.StatusNotFound, "unsupported cron route")
		return
	}

	if len(parts) == 2 {
		subroute := strings.ToLower(strings.TrimSpace(parts[1]))
		switch subroute {
		case "run":
			if r.Method != http.MethodPost {
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}

			runID, err := s.triggerCronRun(id)
			if err != nil {
				if errors.Is(err, errCronRunNotFound) {
					serveError(w, http.StatusNotFound, err.Error())
					return
				}
				serveError(w, http.StatusBadRequest, err.Error())
				return
			}
			serveJSON(w, http.StatusCreated, map[string]any{"ok": true, "runId": runID})
			return

		case "pause":
			if r.Method != http.MethodPost {
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}

			reason := strings.TrimSpace(r.URL.Query().Get("reason"))
			updated := s.cronRuns.touch(id, func(item *cronRun) {
				item.Status = runCronStatusPaused
				if reason == "" {
					item.PausedReason = "manual"
				} else {
					item.PausedReason = reason
				}
				item.UpdatedAt = time.Now().UTC()
			})

			if !updated {
				serveError(w, http.StatusNotFound, "cron run not found")
				return
			}

			serveJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "status": runCronStatusPaused})
			return

		case "resume":
			if r.Method != http.MethodPost {
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}

			updated := s.cronRuns.touch(id, func(item *cronRun) {
				item.Status = runCronStatusActive
				item.PausedReason = ""
				item.UpdatedAt = time.Now().UTC()
			})

			if !updated {
				serveError(w, http.StatusNotFound, "cron run not found")
				return
			}

			serveJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "status": runCronStatusActive})
			return

		default:
			serveError(w, http.StatusNotFound, "unsupported cron route")
			return
		}
	}
}

func (s *serveState) triggerCronRun(id string) (string, error) {
	item, ok := s.cronRuns.get(id)
	if !ok {
		return "", errCronRunNotFound
	}

	if item.Status != runCronStatusActive {
		return "", fmt.Errorf("cron run is paused")
	}

	interval, err := parseCronInterval(item.Interval)
	if err != nil {
		return "", err
	}

	req := runExecutionRequest{
		Workdir:    item.Workdir,
		File:       item.WorkflowFile,
		Ref:        item.Ref,
		Repository: item.Repository,
		SecretRefs: copySecretRefs(item.SecretRefs),
	}

	session, err := s.runCommandAsync(context.Background(), req)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	s.cronRuns.touch(id, func(run *cronRun) {
		run.LastRunAt = now
		run.LastRunID = session.ID
		run.UpdatedAt = now
		run.NextRunAt = now.Add(interval)
	})

	return session.ID, nil
}

func (s *serveState) startCronScheduler() {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for now := range ticker.C {
			now = now.UTC()
			items := s.cronRuns.list()
			for _, item := range items {
				if item.Status != runCronStatusActive {
					continue
				}
				if item.NextRunAt.IsZero() {
					interval, err := parseCronInterval(item.Interval)
					if err != nil {
						continue
					}
					s.cronRuns.touch(item.ID, func(run *cronRun) {
						run.NextRunAt = now.Add(interval)
						run.UpdatedAt = now
					})
					continue
				}
				if item.NextRunAt.After(now) {
					continue
				}
				_, _ = s.triggerCronRun(item.ID)
			}
		}
	}()
}

func (s *serveState) handlePipelines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	args := parsePipelineArgs(r)
	workdir := args.Workdir
	if workdir == "" {
		workdir = s.defaultWorkdir
	}
	if workdir == "" {
		workdir = "."
	}

	binaryPath, err := resolveExecutable()
	if err != nil {
		serveError(w, http.StatusInternalServerError, fmt.Sprintf("failed to locate binary: %v", err))
		return
	}

	execArgs := []string{"--workdir", workdir, "list", "--format", "json"}
	if args.File != "" {
		execArgs = append(execArgs, "--file", args.File)
	}

	output, stderr, err := runCLIPipeOutput(r.Context(), binaryPath, execArgs, workdir)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		serveError(w, http.StatusBadRequest, msg)
		return
	}

	var payload any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		serveError(w, http.StatusInternalServerError, fmt.Sprintf("failed to parse pipeline JSON: %v", err))
		return
	}
	serveJSON(w, http.StatusOK, payload)
}

func (s *serveState) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	args := parsePipelineArgs(r)
	workdir := args.Workdir
	if workdir == "" {
		workdir = s.defaultWorkdir
	}
	if workdir == "" {
		workdir = "."
	}

	execArgs := []string{"--workdir", workdir, "list", "--format", "json"}
	if args.File != "" {
		execArgs = append(execArgs, "--file", args.File)
	}

	pipelinePayload, err := runCLIJSONOutput(r.Context(), execArgs, workdir)
	if err != nil {
		serveError(w, http.StatusBadRequest, err.Error())
		return
	}

	jobs, err := buildJobPayload(pipelinePayload)
	if err != nil {
		serveError(w, http.StatusBadRequest, err.Error())
		return
	}

	serveJSON(w, http.StatusOK, jobsResponse{
		File:    args.File,
		Workdir: workdir,
		Jobs:    jobs,
	})
}

func (s *serveState) handleSystem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	runs := s.runs.list()
	running := 0
	pending := 0
	succeeded := 0
	failed := 0
	canceled := 0
	for _, session := range runs {
		snapshot := session.snapshot()
		switch snapshot.Status {
		case runStatusRunning:
			running++
		case runStatusPending:
			pending++
		case runStatusSucceeded:
			succeeded++
		case runStatusFailed:
			failed++
		case runStatusCanceled:
			canceled++
		}
	}

	snapshot := systemHealthResponse{
		Status:         "ok",
		Time:           time.Now().UTC().Format(time.RFC3339),
		Uptime:         time.Since(serviceStartedAt).Round(time.Second).String(),
		GoVersion:      runtime.Version(),
		GoRoutines:     runtime.NumGoroutine(),
		CPUs:           runtime.NumCPU(),
		RunningRuns:    running,
		PendingRuns:    pending,
		SucceededRuns:  succeeded,
		FailedRuns:     failed,
		CanceledRuns:   canceled,
		TotalRuns:      len(runs),
		CachedRuns:     len(runs),
		DefaultWorkdir: s.defaultWorkdir,
		APIPath:        s.apiPrefix,
	}
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	snapshot.HeapObjects = memStats.HeapObjects
	snapshot.HeapAllocBytes = memStats.HeapAlloc
	snapshot.StackInUseBytes = memStats.StackInuse
	snapshot.NumGC = memStats.NumGC

	serveJSON(w, http.StatusOK, snapshot)
}

func (s *serveState) handleValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	args := parsePipelineArgs(r)
	workdir := args.Workdir
	if workdir == "" {
		workdir = s.defaultWorkdir
	}
	if workdir == "" {
		workdir = "."
	}
	provider := args.Provider
	if provider == "" {
		provider = "auto"
	}

	execArgs := []string{"--workdir", workdir, "validate", "--provider", provider}
	if args.Strict {
		execArgs = append(execArgs, "--strict")
	}
	if args.File != "" {
		execArgs = append(execArgs, "--file", args.File)
	}

	binaryPath, err := resolveExecutable()
	if err != nil {
		serveError(w, http.StatusInternalServerError, fmt.Sprintf("failed to locate binary: %v", err))
		return
	}

	output, stderr, err := runCLIPipeOutput(r.Context(), binaryPath, execArgs, workdir)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		serveJSON(w, http.StatusOK, validateResponse{
			Valid:  false,
			Output: msg,
		})
		return
	}

	serveJSON(w, http.StatusOK, validateResponse{
		Valid:  true,
		Output: strings.TrimSpace(output),
	})
}

func (s *serveState) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	workdir := strings.TrimSpace(r.URL.Query().Get("workdir"))
	if workdir == "" {
		workdir = s.defaultWorkdir
	}
	if workdir == "" {
		workdir = "."
	}
	directory := strings.TrimSpace(r.URL.Query().Get("directory"))
	if directory == "" {
		directory = "."
	}

	binaryPath, err := resolveExecutable()
	if err != nil {
		serveError(w, http.StatusInternalServerError, fmt.Sprintf("failed to locate binary: %v", err))
		return
	}

	execArgs := []string{"--workdir", workdir, "discover", "--format", "json", "--directory", directory}
	output, stderr, err := runCLIPipeOutput(r.Context(), binaryPath, execArgs, workdir)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		serveError(w, http.StatusBadRequest, msg)
		return
	}

	var payload any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		serveError(w, http.StatusInternalServerError, fmt.Sprintf("failed to parse discovery JSON: %v", err))
		return
	}

	serveJSON(w, http.StatusOK, payload)
}

func (s *serveState) runCommandSession(ctx context.Context, session *runSession, args []string) {
	if session == nil {
		return
	}

	binaryPath, err := resolveExecutable()
	if err != nil {
		session.setResult(-1, fmt.Errorf("failed to locate binary: %v", err), runStatusFailed)
		return
	}

	session.updateStatus(runStatusRunning)
	session.appendLog("starting gci " + strings.Join(args, " "))

	cmd := runCommandContext(ctx, binaryPath, args...)
	if session.Workdir != "" {
		cmd.Dir = session.Workdir
	}

	env, err := s.buildRunEnvironment(session.Request)
	if err != nil {
		session.setResult(-1, err, runStatusFailed)
		return
	}
	if len(env) > 0 {
		cmd.Env = env
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		session.setResult(-1, fmt.Errorf("failed to open stdout pipe: %v", err), runStatusFailed)
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		session.setResult(-1, fmt.Errorf("failed to open stderr pipe: %v", err), runStatusFailed)
		return
	}

	if err := cmd.Start(); err != nil {
		session.setResult(-1, fmt.Errorf("failed to start command: %v", err), runStatusFailed)
		return
	}

	teeOutput := func(reader io.Reader, channel string, done chan struct{}) {
		defer func() {
			done <- struct{}{}
		}()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 1024), serveScannerBuffer)
		for scanner.Scan() {
			session.appendLog(fmt.Sprintf("[%s] %s", channel, scanner.Text()))
		}
		if err := scanner.Err(); err != nil {
			session.appendLog(fmt.Sprintf("[%s] stream error: %v", channel, err))
		}
	}

	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go teeOutput(stdout, "stdout", stdoutDone)
	go teeOutput(stderr, "stderr", stderrDone)

	err = cmd.Wait()
	<-stdoutDone
	<-stderrDone

	exitCode := 0
	status := runStatusSucceeded
	if err != nil {
		exitCode = -1
		status = runStatusFailed

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			status = runStatusCanceled
		}
	}

	session.setResult(exitCode, err, status)
}

func (s *serveState) buildRunEnvironment(req runExecutionRequest) ([]string, error) {
	base := append([]string(nil), os.Environ()...)
	base = append(base, req.Env...)

	if s.secretStore == nil || len(req.SecretRefs) == 0 {
		return base, nil
	}

	for _, rawRef := range req.SecretRefs {
		scope, name, err := parseSecretRef(rawRef)
		if err != nil {
			return nil, fmt.Errorf("invalid secret reference: %w", err)
		}

		value, found := s.secretStore.get(scope, name)
		if !found {
			return nil, fmt.Errorf("secret %q is not configured", rawRef)
		}

		base = mergeEnvironment(base, name+"="+value)
	}

	return base, nil
}

func mergeEnvironment(base []string, candidate string) []string {
	key, value, hasValue := strings.Cut(candidate, "=")
	if !hasValue || key == "" {
		return append(base, candidate)
	}

	for i, envLine := range base {
		envKey, _, hasValue := strings.Cut(envLine, "=")
		if !hasValue {
			continue
		}
		if envKey == key {
			base[i] = key + "=" + value
			return base
		}
	}

	return append(base, key+"="+value)
}

func normalizeRunRepositoryRequest(req runExecutionRequest) (string, string) {
	repositoryURL := strings.TrimSpace(req.RepositoryURL)
	repository := strings.TrimSpace(req.Repository)

	if repositoryURL == "" {
		return repositoryURL, repository
	}
	return repositoryURL, repository
}

func runGitCommand(ctx context.Context, workdir string, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("git command is empty")
	}

	cmd := runCommandContext(ctx, "git", args...)
	if workdir != "" {
		cmd.Dir = workdir
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func cloneOrOpenRepository(ctx context.Context, repository string, workspace string, checkoutRef string, autoFetch bool) error {
	if repository == "" {
		return fmt.Errorf("repository not provided")
	}

	workspace = filepath.Clean(workspace)
	gitDir := filepath.Join(workspace, ".git")

	_, workspaceErr := os.Stat(gitDir)
	if autoFetch {
		if workspaceErr != nil {
			workspaceParent := filepath.Dir(workspace)
			if err := os.MkdirAll(filepath.Dir(workspace), 0o755); err != nil {
				return fmt.Errorf("failed to create workspace directory: %w", err)
			}
			if _, err := os.Stat(workspace); err == nil {
				return fmt.Errorf("workspace path already exists but is not a git repository: %s", workspace)
			}
			if workspaceParent != workspace {
				if _, err := os.Stat(workspaceParent); err != nil {
					return fmt.Errorf("failed to validate workspace parent: %w", err)
				}
			}

			if err := runGitCommand(ctx, "", "clone", repository, workspace); err != nil {
				return fmt.Errorf("failed to clone repository %q: %w", repository, err)
			}
		} else if err := runGitCommand(ctx, workspace, "fetch", "--all", "--prune"); err != nil {
			return fmt.Errorf("failed to fetch repository %q: %w", repository, err)
		}
	} else if workspaceErr != nil {
		return fmt.Errorf("workspace does not contain a git repository: %s", workspace)
	}

	if checkoutRef != "" {
		if err := runGitCommand(ctx, workspace, "checkout", normalizeRepositoryRef(checkoutRef)); err != nil {
			return fmt.Errorf("failed to checkout %q in %s: %w", checkoutRef, workspace, err)
		}
	}

	return nil
}

func (s *serveState) resolveRunWorkspace(ctx context.Context, req runExecutionRequest) (runExecutionRequest, error) {
	requestedWorkdir := strings.TrimSpace(req.Workdir)
	baseWorkdir := requestedWorkdir
	if baseWorkdir == "" {
		baseWorkdir = strings.TrimSpace(s.defaultWorkdir)
	}
	if baseWorkdir == "" {
		baseWorkdir = "."
	}

	repositoryURL, repositoryName := normalizeRunRepositoryRequest(req)
	if repositoryURL == "" && repositoryName == "" {
		req.Workdir = baseWorkdir
		if req.Workdir == "" {
			req.Workdir = "."
		}
		return req, nil
	}

	rawRepo := firstString(repositoryURL, repositoryName)
	if requestedWorkdir == "" {
		if isLikelyRemoteRepository(rawRepo) {
			workspace := filepath.Join(baseWorkdir, runRepositoryCacheDir, sanitizeRepositoryName(rawRepo))
			if err := cloneOrOpenRepository(ctx, rawRepo, workspace, req.Ref, req.AutoFetch); err != nil {
				return req, err
			}
			req.Workdir = workspace
			return req, nil
		}

		if _, err := os.Stat(rawRepo); err == nil {
			req.Workdir = rawRepo
			return req, nil
		}
	}

	req.Workdir = requestedWorkdir
	if req.Workdir == "" {
		req.Workdir = "."
	}

	if isLikelyRemoteRepository(rawRepo) {
		if err := cloneOrOpenRepository(ctx, rawRepo, req.Workdir, req.Ref, req.AutoFetch); err != nil {
			return req, err
		}
	}

	return req, nil
}

func (s *serveState) runCommandAsync(ctx context.Context, req runExecutionRequest) (*runSession, error) {
	prepared, err := s.resolveRunWorkspace(ctx, req)
	if err != nil {
		return nil, err
	}
	req = prepared

	args := buildRunCommandArgs(req, s.defaultWorkdir)
	maxLogEntries := req.MaxLogEntries
	if maxLogEntries <= 0 {
		maxLogEntries = s.maxLogEntries
	}
	session := newRunSession(generateRunID(), req.Workdir, req.File, maxLogEntries, args, req)

	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	session.setCancel(cancel)
	s.runs.add(session)
	s.runs.prune(s.maxRunEntries)

	go s.runCommandSession(ctx, session, args)

	return session, nil
}

func parseRunRequest(r *http.Request) (runExecutionRequest, error) {
	if r.Body == nil {
		return runExecutionRequest{}, nil
	}
	defer r.Body.Close()

	var req runExecutionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return req, err
	}
	return req, nil
}

type githubWebhookPayload struct {
	Ref        string         `json:"ref"`
	After      string         `json:"after"`
	Event      string         `json:"event_name"`
	Repository map[string]any `json:"repository"`
}

type gitlabWebhookPayload struct {
	ObjectKind string         `json:"object_kind"`
	Ref        string         `json:"ref"`
	After      string         `json:"after"`
	Project    map[string]any `json:"project"`
}

func parseWebhookDefaultsFromQuery(r *http.Request, defaultWorkdir string) (runExecutionRequest, bool) {
	q := r.URL.Query()
	req := runExecutionRequest{
		Workdir:       strings.TrimSpace(q.Get("workdir")),
		File:          strings.TrimSpace(q.Get("file")),
		Job:           strings.TrimSpace(q.Get("job")),
		Stage:         strings.TrimSpace(q.Get("stage")),
		Parallel:      parseBoolValue(q.Get("parallel")),
		ContinueOnErr: parseBoolValue(q.Get("continue_on_error")),
		Docker:        parseBoolValue(q.Get("docker")),
		Podman:        parseBoolValue(q.Get("podman")),
		DryRun:        parseBoolValue(q.Get("dry_run")),
		Verbose:       parseBoolValue(q.Get("verbose")),
		Quiet:         parseBoolValue(q.Get("quiet")),
		Repository:    strings.TrimSpace(q.Get("repository")),
		RepositoryURL: strings.TrimSpace(q.Get("repositoryUrl")),
		Ref:           strings.TrimSpace(q.Get("ref")),
	}

	autoFetchSet := false
	req.AutoFetch, autoFetchSet = parseBoolValueWithState(q.Get("autoFetch"))

	if req.Workdir == "" {
		req.Workdir = defaultWorkdir
	}
	if req.Memory == "" {
		req.Memory = strings.TrimSpace(q.Get("memory"))
	}
	if req.CPUs == "" {
		req.CPUs = strings.TrimSpace(q.Get("cpus"))
	}
	if req.Network == "" {
		req.Network = strings.TrimSpace(q.Get("network"))
	}
	if req.MaxParallel == 0 {
		if value := strings.TrimSpace(q.Get("maxParallel")); value == "" {
			req.MaxParallel = 0
		} else if parsed, err := strconv.Atoi(value); err == nil {
			req.MaxParallel = parsed
		}
	}
	if req.Timeout == 0 {
		if value := strings.TrimSpace(q.Get("timeout")); value == "" {
			req.Timeout = 0
		} else if parsed, err := strconv.Atoi(value); err == nil {
			req.Timeout = parsed
		}
	}
	req.Only = parseCSVValue(q.Get("only"))
	req.Except = parseCSVValue(q.Get("except"))
	req.Volume = parseCSVValue(q.Get("volume"))
	if env := q.Get("env"); env != "" {
		req.Env = parseCSVValue(env)
	}
	return req, autoFetchSet
}

func (s *serveState) handleHookEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	serveJSON(w, http.StatusOK, s.listWebhookEvents())
}

func (s *serveState) handleWebhook(provider string, secret string, defaultWorkdir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			serveError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			serveError(w, http.StatusBadRequest, fmt.Sprintf("failed to read webhook payload: %v", err))
			return
		}

		// Keep raw body for signature validation and debugging.
		verified := true
		var eventName string

		switch provider {
		case "github":
			headerSignature := r.Header.Get("X-Hub-Signature-256")
			if secret != "" && !verifyGitHubSignature(secret, headerSignature, body) {
				verified = false
			}
			eventName = r.Header.Get("X-GitHub-Event")
			if eventName == "" {
				eventName = "push"
			}

		case "gitlab":
			headerToken := r.Header.Get("X-Gitlab-Token")
			if secret != "" && headerToken != secret {
				verified = false
			}
			eventName = r.Header.Get("X-Gitlab-Event")
			if eventName == "" {
				eventName = "push"
			}
		}

		base, autoFetchSet := parseWebhookDefaultsFromQuery(r, defaultWorkdir)
		req := base
		if !autoFetchSet && (req.Repository != "" || req.RepositoryURL != "") {
			req.AutoFetch = true
		}

		// Preserve common metadata in env for run visibility.
		if req.Env == nil {
			req.Env = []string{}
		}

		if !verified {
			s.addWebhookEvent(hookEvent{
				Provider:  provider,
				Event:     eventName,
				Status:    "rejected",
				Workdir:   base.Workdir,
				Error:     "invalid webhook signature",
				CreatedAt: time.Now(),
			})
			serveError(w, http.StatusUnauthorized, "invalid webhook signature")
			return
		}

		commitSHA := ""

		switch provider {
		case "github":
			var payload githubWebhookPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				s.addWebhookEvent(hookEvent{
					Provider: provider,
					Event:    eventName,
					Status:   "invalid",
					Workdir:  base.Workdir,
					Error:    err.Error(),
				})
				serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid github payload: %v", err))
				return
			}

			repoName, repoURL := repositoryInfoFromGitHub(payload.Repository)
			if req.Ref == "" {
				req.Ref = payload.Ref
			}
			commitSHA = payload.After
			if repoName != "" {
				req.Repository = firstString(req.Repository, repoName)
			}
			if repoURL != "" {
				req.RepositoryURL = firstString(req.RepositoryURL, repoURL)
			}

		case "gitlab":
			var payload gitlabWebhookPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				s.addWebhookEvent(hookEvent{
					Provider: provider,
					Event:    eventName,
					Status:   "invalid",
					Workdir:  base.Workdir,
					Error:    err.Error(),
				})
				serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid gitlab payload: %v", err))
				return
			}

			repoName, repoURL := repositoryInfoFromGitLab(payload.Project)
			if req.Ref == "" {
				req.Ref = payload.Ref
			}
			commitSHA = payload.After
			if repoName != "" {
				req.Repository = firstString(req.Repository, repoName)
			}
			if repoURL != "" {
				req.RepositoryURL = firstString(req.RepositoryURL, repoURL)
			}
		}

		if req.Ref != "" {
			req.Env = append(req.Env, "GCI_REF="+req.Ref)
		}
		if commitSHA != "" {
			req.Env = append(req.Env, "GCI_COMMIT="+commitSHA)
		}
		if req.Repository != "" {
			req.Env = append(req.Env, "GCI_REPOSITORY="+req.Repository)
			req.Env = append(req.Env, "CI_PROJECT_NAME="+req.Repository)
		}
		if req.RepositoryURL != "" {
			req.Env = append(req.Env, "GCI_REPOSITORY_URL="+req.RepositoryURL)
		}

		req.Env = append(req.Env, "CI_PROVIDER="+provider)
		req.Env = append(req.Env, "CI_EVENT="+eventName)
		req.Env = append(req.Env, fmt.Sprintf("GCI_WORKDIR=%s", req.Workdir))

		session, err := s.runCommandAsync(context.Background(), req)
		if err != nil {
			s.addWebhookEvent(hookEvent{
				Provider:      provider,
				Event:         eventName,
				Status:        "failed",
				Workdir:       req.Workdir,
				Ref:           req.Ref,
				Repository:    req.Repository,
				RepositoryURL: req.RepositoryURL,
				Error:         err.Error(),
				CreatedAt:     time.Now(),
			})
			serveError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.addWebhookEvent(hookEvent{
			Provider:      provider,
			Event:         eventName,
			Status:        "started",
			Workdir:       req.Workdir,
			Ref:           req.Ref,
			Commit:        commitSHA,
			Repository:    req.Repository,
			RepositoryURL: req.RepositoryURL,
			RunID:         session.ID,
			Error:         "",
			CreatedAt:     time.Now(),
		})

		serveJSON(w, http.StatusAccepted, map[string]any{
			"ok":       true,
			"provider": provider,
			"runId":    session.ID,
			"event":    eventName,
			"status":   "accepted",
			"received": time.Now().UTC().Format(time.RFC3339),
			"workdir":  req.Workdir,
			"resource": "/runs/" + session.ID,
		})
	}
}

func (s *serveState) handleRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		statusFilter := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("status")))
		fileFilter := strings.TrimSpace(r.URL.Query().Get("file"))
		jobFilter := strings.TrimSpace(r.URL.Query().Get("job"))

		sessions := s.runs.list()
		snapshots := make([]runSessionListItem, 0, len(sessions))
		for _, session := range sessions {
			snapshot := session.listSnapshot()

			if statusFilter != "" && strings.ToLower(snapshot.Status) != statusFilter {
				continue
			}
			if fileFilter != "" && snapshot.File != fileFilter {
				continue
			}
			if jobFilter != "" {
				command := strings.Join(snapshot.Command, " ")
				if !strings.Contains(command, jobFilter) {
					continue
				}
			}

			snapshots = append(snapshots, snapshot)
		}
		sort.Slice(snapshots, func(i, j int) bool {
			return snapshots[i].StartedAt.After(snapshots[j].StartedAt)
		})
		if limitValue := strings.TrimSpace(r.URL.Query().Get("limit")); limitValue != "" {
			if limit, err := strconv.Atoi(limitValue); err == nil && limit > 0 && limit < len(snapshots) {
				snapshots = snapshots[:limit]
			}
		}
		serveJSON(w, http.StatusOK, snapshots)

	case http.MethodPost:
		req, err := parseRunRequest(r)
		if err != nil {
			serveError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if req.Workdir == "" {
			req.Workdir = s.defaultWorkdir
		}

		session, err := s.runCommandAsync(context.Background(), req)
		if err != nil {
			serveError(w, http.StatusBadRequest, err.Error())
			return
		}
		serveJSON(w, http.StatusCreated, session.snapshot())

	default:
		serveError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *serveState) handleRunByID(apiPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trimmed := strings.TrimPrefix(r.URL.Path, apiPrefix+"/runs/")
		parts := strings.Split(strings.Trim(trimmed, "/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			serveError(w, http.StatusBadRequest, "missing run id")
			return
		}

		session, ok := s.runs.get(parts[0])
		if !ok {
			serveError(w, http.StatusNotFound, "run not found")
			return
		}

		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				serveJSON(w, http.StatusOK, session.snapshot())
			default:
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
			return
		}

		switch parts[1] {
		case "retry":
			if r.Method != http.MethodPost {
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}

			snapshot := session.snapshot()
			if len(snapshot.Command) == 0 {
				serveError(w, http.StatusConflict, "no command information for retry")
				return
			}

			retryReq := session.Request
			retryReq.Workdir = session.Workdir
			if retryReq.Workdir == "" {
				retryReq.Workdir = s.defaultWorkdir
			}
			if retryReq.Workdir == "" {
				retryReq.Workdir = "."
			}

			newSession, err := s.runCommandAsync(context.Background(), retryReq)
			if err != nil {
				serveError(w, http.StatusBadRequest, err.Error())
				return
			}
			serveJSON(w, http.StatusCreated, newSession.snapshot())
			return

		case "logs":
			if r.Method != http.MethodGet {
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			offset := 0
			if value := strings.TrimSpace(r.URL.Query().Get("offset")); value != "" {
				if parsed, err := strconv.Atoi(value); err == nil {
					offset = parsed
				}
			}
			logs, total := session.logsFrom(offset)
			snapshot := session.snapshot()
			serveJSON(w, http.StatusOK, map[string]any{
				"runId":      session.ID,
				"status":     snapshot.Status,
				"exitCode":   snapshot.ExitCode,
				"file":       snapshot.File,
				"error":      snapshot.Error,
				"startedAt":  snapshot.StartedAt,
				"updatedAt":  snapshot.UpdatedAt,
				"finishedAt": snapshot.FinishedAt,
				"offset":     offset,
				"totalLines": total,
				"lines":      logs,
			})
			return

		case "cancel":
			if r.Method != http.MethodPost {
				serveError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}

			snapshot := session.snapshot()
			if snapshot.Status != runStatusRunning && snapshot.Status != runStatusPending {
				serveError(w, http.StatusConflict, "run is not active")
				return
			}

			session.stop()
			session.appendLog("pipeline execution canceled by user")
			session.setResult(-1, fmt.Errorf("execution canceled"), runStatusCanceled)
			serveJSON(w, http.StatusOK, session.snapshot())
			return
		default:
			serveError(w, http.StatusNotFound, "invalid run endpoint")
		}
	}
}

func CmdServe(c *cli.Context) error {
	listen := c.String("listen")
	staticDir := c.String("static-dir")
	apiPrefix := normalizeAPIPath(c.String("api-prefix"))
	apiOnly := c.Bool("api-only")
	maxLogEntries := c.Int("max-run-logs")
	maxRunEntries := c.Int("max-runs")
	maxHookEvents := c.Int("max-hook-events")
	ghSecret := c.String("github-webhook-secret")
	glSecret := c.String("gitlab-webhook-secret")
	ghWorkdir := strings.TrimSpace(c.String("github-webhook-workdir"))
	glWorkdir := strings.TrimSpace(c.String("gitlab-webhook-workdir"))

	workdir := strings.TrimSpace(c.String("workdir"))
	if workdir == "" {
		workdir = "."
	}
	if maxLogEntries <= 0 {
		maxLogEntries = serveDefaultLogLimit
	}
	if maxRunEntries <= 0 {
		maxRunEntries = serveDefaultMaxRuns
	}
	if maxHookEvents <= 0 {
		maxHookEvents = 100
	}

	if ghWorkdir == "" {
		ghWorkdir = workdir
	}
	if glWorkdir == "" {
		glWorkdir = workdir
	}

	if strings.TrimSpace(staticDir) == "" {
		staticDir = "site"
	}

	state := &serveState{
		apiPrefix:         apiPrefix,
		staticDir:         staticDir,
		defaultWorkdir:    workdir,
		runs:              newRunRegistry(),
		secretStore:       newSecretRegistry(),
		cronRuns:          newCronRunRegistry(),
		maxLogEntries:     maxLogEntries,
		maxRunEntries:     maxRunEntries,
		maxHookEvents:     maxHookEvents,
		hookSecretGitHub:  ghSecret,
		hookSecretGitLab:  glSecret,
		hookWorkdirGitHub: ghWorkdir,
		hookWorkdirGitLab: glWorkdir,
	}

	apiPrefixes := buildAPIPrefixes(apiPrefix)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", state.handleHealth)
	for _, endpointPrefix := range apiPrefixes {
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

	state.startCronScheduler()

	if apiOnly {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			serveError(w, http.StatusNotFound, "API-only mode")
		})
	} else {
		mux.HandleFunc("/", state.staticHandler)
	}

	server := &http.Server{
		Addr:         listen,
		Handler:      mux,
		ReadTimeout:  45 * time.Second,
		WriteTimeout: 45 * time.Second,
	}
	return server.ListenAndServe()
}
