// Package compatibility owns git-ci's explicit provider support contract.
package compatibility

import (
	"fmt"
	"sort"
	"strings"
)

const Version = "2026-08-21"

const (
	StateSupported   = "supported"
	StatePartial     = "partial"
	StatePlanned     = "planned"
	StateUnsupported = "unsupported"
)

type Entry struct {
	ID         string   `json:"id"`
	Provider   string   `json:"provider"`
	Category   string   `json:"category"`
	Capability string   `json:"capability"`
	State      string   `json:"state"`
	Summary    string   `json:"summary"`
	Limitation string   `json:"limitation,omitempty"`
	Evidence   []string `json:"evidence"`
	Reference  string   `json:"reference,omitempty"`
}

type Filter struct {
	Provider string `json:"provider,omitempty"`
	Category string `json:"category,omitempty"`
	State    string `json:"state,omitempty"`
	Search   string `json:"search,omitempty"`
}

type Counts struct {
	Total       int `json:"total"`
	Supported   int `json:"supported"`
	Partial     int `json:"partial"`
	Planned     int `json:"planned"`
	Unsupported int `json:"unsupported"`
	GitHub      int `json:"github"`
	GitLab      int `json:"gitlab"`
	Shared      int `json:"shared"`
}

type Report struct {
	Version    string   `json:"version"`
	Filter     Filter   `json:"filter"`
	Items      []Entry  `json:"items"`
	Count      int      `json:"count"`
	Counts     Counts   `json:"counts"`
	Categories []string `json:"categories"`
}

var entries = []Entry{
	feature("shared-local-projects", "shared", "projects", "Local project registry", StateSupported, "Discover and register canonical Git checkouts from approved VPS roots.", "", "store + HTTP + Chromium", ""),
	feature("shared-registration-discovery", "shared", "projects", "Registration workflow discovery", StateSupported, "Browser and API registration perform a read-only provider workflow scan.", "", "HTTP + Chromium + production smoke", ""),
	feature("shared-manual-dispatch", "shared", "dispatch", "Manual dispatch", StateSupported, "Queue a workflow with an explicit ref, optional commit, and typed inputs.", "", "execution + API + Chromium", ""),
	feature("shared-commit-watch", "shared", "triggers", "Local commit watch", StateSupported, "Poll an opted-in local branch and deduplicate immutable commit runs.", "", "trigger manager + restart tests", ""),
	feature("shared-cron", "shared", "triggers", "Durable cron schedules", StateSupported, "SQLite-backed schedules support timezone, pause, claims, and restart recovery.", "", "scheduler + store + Chromium", ""),
	feature("shared-webhooks", "shared", "triggers", "Signed provider webhooks", StatePartial, "GitHub, GitLab, and generic signed endpoints can enqueue normalized push workflows.", "The full GitHub event and GitLab pipeline-source matrices are not implemented.", "webhook fixtures + API", ""),
	feature("shared-run-graph", "shared", "graph", "Immutable workflow and run DAG", StateSupported, "Render jobs, stages, dependencies, and immutable run snapshots before and after execution.", "", "graph store + desktop/mobile Chromium", ""),
	feature("shared-runner-selection", "shared", "runners", "Runner labels and availability", StateSupported, "Match normalized requirements against the local runner inventory before dispatch.", "", "runner inventory + parser fixtures", ""),
	feature("shared-secrets", "shared", "data", "Project and environment secrets", StateSupported, "Encrypt scoped secrets at rest and redact values from logs, summaries, and annotations.", "", "secret canary + protected delivery E2E", ""),
	feature("shared-deployments", "shared", "delivery", "Deployments, approvals, and rollback", StateSupported, "Gate protected environments, serialize delivery, verify, and queue provenance-preserving rollback.", "", "store + API + Chromium", ""),
	feature("shared-logs-replay", "shared", "operations", "Durable logs and replay", StateSupported, "Persist redacted logs and replay eligible jobs or shell steps with immutable lineage.", "", "restart + replay + Chromium", ""),
	feature("shared-project-lifecycle", "shared", "projects", "Reversible project lifecycle", StateSupported, "Unregister safely, retain history, and reactivate the same checkout under its original ID.", "", "SQLite + CSRF + production smoke", ""),
	feature("shared-audit", "shared", "operations", "Audit trail", StateSupported, "Query immutable actor, action, project, resource, and metadata events through SQLite-backed API and web ledgers with time histograms.", "", "store + filtered API + responsive Chromium", ""),
	feature("shared-provider-status", "shared", "operations", "Provider commit status callbacks", StatePlanned, "Publish pending, running, and final status to the source provider.", "Requires provider credentials, retry policy, and outbound delivery audit.", "roadmap", ""),
	feature("shared-email-alerts", "shared", "operations", "Email alert delivery", StatePlanned, "Deliver deduplicated run and deployment alerts to configured recipients.", "The Settings page is currently a UI-only preview.", "roadmap", ""),
	feature("shared-release-objects", "shared", "delivery", "Release objects", StatePlanned, "Model provider releases and release pages as durable delivery records.", "Current delivery support ends at deployments and rollback.", "roadmap", ""),

	feature("github-discovery", "github", "workflow", "Workflow file discovery", StateSupported, "Discover and parse .github/workflows YAML definitions.", "", "GitHub parser + path containment tests", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax"),
	feature("github-workflow-dispatch", "github", "dispatch", "workflow_dispatch inputs", StateSupported, "Normalize string, choice, boolean, and number inputs into typed dispatch controls.", "", "trigger policy + Chromium", "https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflow_dispatch"),
	feature("github-push-filters", "github", "triggers", "Push branch, tag, and path filters", StatePartial, "Normalize branches, tags, paths, and ignore filters for push admission.", "Complex expression contexts and every event activity type are not equivalent to GitHub-hosted evaluation.", "trigger policy fixtures", "https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#push"),
	feature("github-pull-request", "github", "triggers", "Pull request events", StatePartial, "Parse pull_request policies and expose their filters in the workflow contract.", "Provider delivery execution remains push-centric and fork trust policy is not complete.", "parser + trigger policy", "https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request"),
	feature("github-schedule", "github", "triggers", "Scheduled workflows", StateSupported, "Parse schedule declarations and execute explicit durable gci schedules.", "", "parser + scheduler", "https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule"),
	feature("github-needs", "github", "graph", "Jobs and needs graph", StateSupported, "Normalize job dependencies into deterministic execution levels and immutable edges.", "", "parser + graph fixtures", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idneeds"),
	feature("github-matrix", "github", "execution", "Matrix strategy", StateSupported, "Expand bounded matrices with include, exclude, fail-fast, and max-parallel metadata.", "", "matrix semantics + execution tests", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstrategy"),
	feature("github-conditions", "github", "execution", "if conditions", StatePartial, "Evaluate a documented deterministic expression subset before job and step execution.", "Arbitrary GitHub expression functions and contexts are not supported.", "condition semantics fixtures", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idif"),
	feature("github-concurrency", "github", "execution", "Concurrency groups", StateSupported, "Persist workflow and job groups with cancel-in-progress behavior.", "", "concurrency store + execution race tests", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#concurrency"),
	feature("github-shell-defaults", "github", "execution", "Shell and working-directory defaults", StateSupported, "Apply workflow, job, and step shell and working-directory contracts in isolated workspaces.", "", "parser + execution containment", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#defaults"),
	feature("github-actions", "github", "execution", "Action uses steps", StatePartial, "Run supported built-in adapters, local composite actions, and local reusable workflows.", "Arbitrary marketplace JavaScript and Docker actions are rejected rather than executed implicitly.", "action adapter + composite fixtures", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsuses"),
	feature("github-reuse", "github", "reuse", "Reusable workflows", StatePartial, "Resolve contained local workflow_call targets and typed inputs/outputs.", "Remote repository reusable workflows are not fetched.", "local reuse fixtures", "https://docs.github.com/en/actions/how-tos/sharing-automations/reusing-workflows"),
	feature("github-runtime-files", "github", "data", "Environment and output files", StateSupported, "Honor GITHUB_ENV, GITHUB_PATH, GITHUB_OUTPUT, and GITHUB_STEP_SUMMARY with redaction.", "", "runtime file + restart tests", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands#environment-files"),
	feature("github-workflow-commands", "github", "operations", "Workflow commands", StateSupported, "Process masks, command suspension, notices, warnings, errors, and nested log groups durably.", "", "workflow command + log section E2E", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands"),
	feature("github-artifacts-cache", "github", "artifacts", "Artifacts, cache, and test reports", StateSupported, "Store checksummed project-scoped artifacts, isolated caches, and JUnit report metadata.", "", "artifact/cache/report integration", "https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching"),
	feature("github-containers", "github", "runtime", "Job and service containers", StatePartial, "Execute job containers and services through the configured rootless Podman runtime.", "Docker-host parity, hosted images, and every network option are not supported.", "container runtime integration", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idcontainer"),
	feature("github-permissions", "github", "security", "GITHUB_TOKEN permissions", StateUnsupported, "No implicit repository token is minted for a local run.", "Workflow permissions and provider token scopes require an explicit credential broker.", "security boundary", "https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions"),
	feature("github-oidc", "github", "security", "OIDC federation", StateUnsupported, "The local runner does not mint GitHub-compatible OIDC identity tokens.", "Requires a service identity issuer and audience policy.", "security boundary", "https://docs.github.com/en/actions/concepts/security/openid-connect"),
	feature("github-hosted-runners", "github", "runners", "GitHub-hosted runners", StateUnsupported, "gci schedules only operator-owned local runner capacity.", "Hosted runner images and billing are intentionally outside the self-hosted service.", "architecture", "https://docs.github.com/en/actions/concepts/runners/github-hosted-runners"),

	feature("gitlab-discovery", "gitlab", "workflow", "Root pipeline discovery", StateSupported, "Discover and parse repository .gitlab-ci.yml definitions.", "", "GitLab parser + path containment", "https://docs.gitlab.com/ci/yaml/"),
	feature("gitlab-stages-needs", "gitlab", "graph", "Stages and needs graph", StateSupported, "Normalize stage ordering, needs, dependencies, and artifact edges into one DAG.", "", "GitLab parser + graph fixtures", "https://docs.gitlab.com/ci/yaml/#needs"),
	feature("gitlab-variables", "gitlab", "data", "Pipeline and job variables", StateSupported, "Merge normalized workflow, job, matrix, environment, and dispatch variables into execution.", "", "parser + environment tests", "https://docs.gitlab.com/ci/yaml/#variables"),
	feature("gitlab-rules", "gitlab", "triggers", "workflow and job rules", StatePartial, "Evaluate a deterministic subset of if, changes, exists, variables, when, and allow_failure.", "Regex, every predefined variable, and full GitLab expression parity are not supported.", "rules semantics fixtures", "https://docs.gitlab.com/ci/yaml/#rules"),
	feature("gitlab-only-except", "gitlab", "triggers", "only and except", StatePartial, "Normalize common ref, change, and variable policies.", "Advanced repository, Kubernetes, and external pipeline selectors are not supported.", "parser + admission tests", "https://docs.gitlab.com/ci/yaml/deprecated_keywords/#only--except"),
	feature("gitlab-parallel", "gitlab", "execution", "Parallel and matrix jobs", StateSupported, "Expand bounded parallel and parallel:matrix jobs into immutable nodes.", "", "matrix semantics + parser fixtures", "https://docs.gitlab.com/ci/yaml/#parallel"),
	feature("gitlab-concurrency", "gitlab", "execution", "Resource groups and interruptible jobs", StatePartial, "Normalize interruptible and concurrency metadata and enforce durable gci leases.", "GitLab process modes and every auto-cancel policy are not implemented.", "concurrency + cancellation tests", "https://docs.gitlab.com/ci/yaml/#resource_group"),
	feature("gitlab-scripts", "gitlab", "execution", "before_script, script, and after_script", StateSupported, "Execute normalized script phases with timeout and allow-failure contracts.", "", "parser + execution tests", "https://docs.gitlab.com/ci/yaml/#before_script"),
	feature("gitlab-artifacts-cache", "gitlab", "artifacts", "Artifacts, cache, and reports", StateSupported, "Persist paths, cache keys, JUnit reports, and scoped metadata on local disk.", "", "artifact/cache/report integration", "https://docs.gitlab.com/ci/yaml/#artifacts"),
	feature("gitlab-dotenv", "gitlab", "data", "Dotenv reports", StateSupported, "Import redacted dotenv report values into dependent jobs with durable provenance.", "", "dotenv parser + execution tests", "https://docs.gitlab.com/ci/yaml/artifacts_reports/#artifactsreportsdotenv"),
	feature("gitlab-image-services", "gitlab", "runtime", "Image and services", StatePartial, "Run normalized images and service containers through rootless Podman.", "Docker executor parity and every service alias/network option are not supported.", "container runtime integration", "https://docs.gitlab.com/ci/yaml/#image"),
	feature("gitlab-includes", "gitlab", "reuse", "Local include and extends", StatePartial, "Resolve repository-contained local include and extends definitions.", "Remote, project, component, and template includes are not fetched.", "local include fixtures", "https://docs.gitlab.com/ci/yaml/includes/"),
	feature("gitlab-environments", "gitlab", "delivery", "Environments and deployments", StateSupported, "Normalize environment names and tiers into protected gci deployment policy.", "", "environment parser + protected delivery E2E", "https://docs.gitlab.com/ci/yaml/#environment"),
	feature("gitlab-retry", "gitlab", "execution", "Automatic retry", StatePlanned, "Retry failed jobs according to bounded GitLab retry policy.", "Today operators use explicit provenance-linked job replay.", "roadmap", "https://docs.gitlab.com/ci/yaml/#retry"),
	feature("gitlab-manual-jobs", "gitlab", "execution", "Manual jobs", StateSupported, "Pause the same durable pipeline at when: manual, expose a graph play control, and resume with bounded variables.", "Optional jobs become playable after the initial pipeline pass; play variables are visible operational values, not secrets.", "parser + SQLite lifecycle + API + Chromium E2E", "https://docs.gitlab.com/ci/jobs/job_control/#create-a-job-that-must-be-run-manually"),
	feature("gitlab-child-pipelines", "gitlab", "reuse", "Child and multi-project pipelines", StatePlanned, "Create policy-gated downstream pipeline lineage.", "Local workflow reuse does not dispatch another project.", "roadmap", "https://docs.gitlab.com/ci/pipelines/downstream_pipelines/"),
	feature("gitlab-identity", "gitlab", "security", "Identity and ID tokens", StateUnsupported, "gci does not mint GitLab-compatible federated identity tokens.", "Requires an operator-owned identity issuer and cloud audience policy.", "security boundary", "https://docs.gitlab.com/ci/yaml/#identity"),
}

func feature(id, provider, category, capability, state, summary, limitation, evidence, reference string) Entry {
	return Entry{ID: id, Provider: provider, Category: category, Capability: capability, State: state, Summary: summary, Limitation: limitation, Evidence: []string{evidence}, Reference: reference}
}

func Query(filter Filter) (Report, error) {
	filter = normalizeFilter(filter)
	if err := validateFilter(filter); err != nil {
		return Report{}, err
	}
	if err := Validate(entries); err != nil {
		return Report{}, err
	}
	all := sortedEntries(entries)
	result := make([]Entry, 0, len(all))
	for _, item := range all {
		if filter.Provider != "" && item.Provider != filter.Provider {
			continue
		}
		if filter.Category != "" && item.Category != filter.Category {
			continue
		}
		if filter.State != "" && item.State != filter.State {
			continue
		}
		if filter.Search != "" && !entryMatches(item, filter.Search) {
			continue
		}
		result = append(result, item)
	}
	return Report{Version: Version, Filter: filter, Items: result, Count: len(result), Counts: countEntries(result), Categories: categories(all)}, nil
}

func Validate(items []Entry) error {
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Provider) == "" || strings.TrimSpace(item.Category) == "" || strings.TrimSpace(item.Capability) == "" || strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("compatibility: entry %d has an empty required field", index)
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("compatibility: duplicate entry ID %q", item.ID)
		}
		seen[item.ID] = struct{}{}
		if item.Provider != "shared" && item.Provider != "github" && item.Provider != "gitlab" {
			return fmt.Errorf("compatibility: entry %q has invalid provider %q", item.ID, item.Provider)
		}
		if !validState(item.State) {
			return fmt.Errorf("compatibility: entry %q has invalid state %q", item.ID, item.State)
		}
		if (item.State == StatePartial || item.State == StatePlanned || item.State == StateUnsupported) && strings.TrimSpace(item.Limitation) == "" {
			return fmt.Errorf("compatibility: entry %q must state its limitation", item.ID)
		}
		if len(item.Evidence) == 0 || strings.TrimSpace(item.Evidence[0]) == "" {
			return fmt.Errorf("compatibility: entry %q has no evidence key", item.ID)
		}
	}
	return nil
}

func normalizeFilter(filter Filter) Filter {
	filter.Provider = strings.ToLower(strings.TrimSpace(filter.Provider))
	filter.Category = strings.ToLower(strings.TrimSpace(filter.Category))
	filter.State = strings.ToLower(strings.TrimSpace(filter.State))
	filter.Search = strings.ToLower(strings.TrimSpace(filter.Search))
	if filter.Provider == "all" {
		filter.Provider = ""
	}
	if filter.State == "all" {
		filter.State = ""
	}
	return filter
}

func validateFilter(filter Filter) error {
	if filter.Provider != "" && filter.Provider != "shared" && filter.Provider != "github" && filter.Provider != "gitlab" {
		return fmt.Errorf("compatibility: provider must be shared, github, or gitlab")
	}
	if filter.State != "" && !validState(filter.State) {
		return fmt.Errorf("compatibility: state must be supported, partial, planned, or unsupported")
	}
	if filter.Category != "" {
		found := false
		for _, item := range entries {
			if item.Category == filter.Category {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("compatibility: unknown category %q", filter.Category)
		}
	}
	return nil
}

func validState(state string) bool {
	return state == StateSupported || state == StatePartial || state == StatePlanned || state == StateUnsupported
}

func sortedEntries(items []Entry) []Entry {
	result := append([]Entry(nil), items...)
	rank := map[string]int{"shared": 0, "github": 1, "gitlab": 2}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if rank[left.Provider] != rank[right.Provider] {
			return rank[left.Provider] < rank[right.Provider]
		}
		if left.Category != right.Category {
			return left.Category < right.Category
		}
		if left.Capability != right.Capability {
			return left.Capability < right.Capability
		}
		return left.ID < right.ID
	})
	return result
}

func entryMatches(item Entry, search string) bool {
	haystack := strings.ToLower(strings.Join([]string{item.ID, item.Provider, item.Category, item.Capability, item.State, item.Summary, item.Limitation, strings.Join(item.Evidence, " ")}, " "))
	return strings.Contains(haystack, search)
}

func countEntries(items []Entry) Counts {
	counts := Counts{Total: len(items)}
	for _, item := range items {
		switch item.State {
		case StateSupported:
			counts.Supported++
		case StatePartial:
			counts.Partial++
		case StatePlanned:
			counts.Planned++
		case StateUnsupported:
			counts.Unsupported++
		}
		switch item.Provider {
		case "github":
			counts.GitHub++
		case "gitlab":
			counts.GitLab++
		case "shared":
			counts.Shared++
		}
	}
	return counts
}

func categories(items []Entry) []string {
	seen := make(map[string]struct{})
	for _, item := range items {
		seen[item.Category] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for category := range seen {
		result = append(result, category)
	}
	sort.Strings(result)
	return result
}
