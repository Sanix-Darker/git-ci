package execution

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestDiscoverNormalizesGitHubWorkflow(t *testing.T) {
	projectPath := t.TempDir()
	writeWorkflowFixture(t, projectPath, ".github/workflows/release.yaml", strings.Join([]string{
		"name: Release",
		"on:",
		"  push:",
		"env:",
		"  SHARED: common",
		"defaults:",
		"  run:",
		"    working-directory: services/api",
		"jobs:",
		"  lint:",
		"    runs-on: ubuntu-24.04",
		"    env:",
		"      LINT_MODE: strict",
		"    steps:",
		"      - name: Lint",
		"        run: go vet ./...",
		"        env:",
		"          GOFLAGS: -mod=readonly",
		"        timeout-minutes: 5",
		"  test:",
		"    needs: lint",
		"    runs-on: ubuntu-24.04",
		"    continue-on-error: true",
		"    env:",
		"      TEST_MODE: integration",
		"    steps:",
		"      - uses: actions/checkout@v4",
		"      - name: Test",
		"        run: go test ./...",
		"        working-directory: cmd/api",
		"        timeout-minutes: 12",
		"  deploy:",
		"    needs:",
		"      - test",
		"    runs-on: ubuntu-24.04",
		"    environment:",
		"      name: production",
		"      url: https://deploy.example.test",
		"    steps:",
		"      - run: ./deploy.sh",
	}, "\n"))

	definitions, err := Discover([]store.Project{fixtureProject(t, projectPath, "project-github")})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("Discover() returned %d definitions, want 1", len(definitions))
	}

	definition := definitions[0]
	if definition.Provider != ProviderGitHubActions {
		t.Errorf("Provider = %q, want %q", definition.Provider, ProviderGitHubActions)
	}
	if definition.File != ".github/workflows/release.yaml" {
		t.Errorf("File = %q, want GitHub workflow-relative file", definition.File)
	}
	if definition.Name != "Release" {
		t.Errorf("Name = %q, want Release", definition.Name)
	}
	if definition.Environment["SHARED"] != "common" {
		t.Errorf("pipeline environment = %#v, want SHARED=common", definition.Environment)
	}
	if want := []string{"lint", "test", "deploy"}; !reflect.DeepEqual(definition.TopologicalOrder, want) {
		t.Errorf("TopologicalOrder = %#v, want %#v", definition.TopologicalOrder, want)
	}
	if want := definition.TopologicalOrder; !reflect.DeepEqual(jobKeys(definition.Jobs), want) {
		t.Errorf("job order = %#v, want %#v", jobKeys(definition.Jobs), want)
	}

	lint := definition.Jobs[0]
	if lint.Key != "lint" || lint.RunnerHint != "ubuntu-24.04" {
		t.Errorf("lint metadata = %#v, want key and runner hint", lint)
	}
	if lint.Environment["LINT_MODE"] != "strict" {
		t.Errorf("lint environment = %#v, want LINT_MODE=strict", lint.Environment)
	}
	if len(lint.Steps) != 1 {
		t.Fatalf("lint steps = %#v, want one step", lint.Steps)
	}
	if lint.Steps[0].Command != "go vet ./..." {
		t.Errorf("lint command = %q, want go vet", lint.Steps[0].Command)
	}
	if lint.Steps[0].Environment["GOFLAGS"] != "-mod=readonly" {
		t.Errorf("lint step environment = %#v, want GOFLAGS", lint.Steps[0].Environment)
	}
	if lint.Steps[0].WorkingDirectory != "services/api" {
		t.Errorf("lint working directory = %q, want default", lint.Steps[0].WorkingDirectory)
	}
	if lint.Steps[0].TimeoutMinutes != 5 {
		t.Errorf("lint timeout = %d, want 5", lint.Steps[0].TimeoutMinutes)
	}

	test := definition.Jobs[1]
	if want := []string{"lint"}; !reflect.DeepEqual(test.Needs, want) {
		t.Errorf("test Needs = %#v, want %#v", test.Needs, want)
	}
	if !test.AllowFailure {
		t.Error("test AllowFailure = false, want true from continue-on-error")
	}
	if len(test.Steps) != 2 || test.Steps[0].Action != "actions/checkout@v4" {
		t.Errorf("test steps = %#v, want action step preserved", test.Steps)
	}
	if test.Steps[1].WorkingDirectory != "cmd/api" || test.Steps[1].TimeoutMinutes != 12 {
		t.Errorf("test step = %#v, want working directory and timeout", test.Steps[1])
	}
	if deploy := definition.Jobs[2]; deploy.EnvironmentName != "production" {
		t.Errorf("deploy EnvironmentName = %q, want production", deploy.EnvironmentName)
	}
}

func TestDiscoverNormalizesGitLabWorkflow(t *testing.T) {
	projectPath := t.TempDir()
	writeWorkflowFixture(t, projectPath, ".gitlab-ci.yml", strings.Join([]string{
		"stages:",
		"  - verify",
		"  - deploy",
		"variables:",
		"  PIPELINE_MODE: release",
		"lint:",
		"  stage: verify",
		"  tags:",
		"    - linux",
		"  variables:",
		"    LINT_MODE: strict",
		"  script:",
		"    - echo lint",
		"deploy:",
		"  stage: deploy",
		"  needs:",
		"    - lint",
		"  tags:",
		"    - deploy-runner",
		"  allow_failure: true",
		"  timeout: 15 minutes",
		"  environment:",
		"    name: production",
		"    deployment_tier: production",
		"  variables:",
		"    DEPLOY_TARGET: production",
		"  script:",
		"    - echo deploy",
	}, "\n"))

	definitions, err := Discover([]store.Project{fixtureProject(t, projectPath, "project-gitlab")})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("Discover() returned %d definitions, want 1", len(definitions))
	}

	definition := definitions[0]
	if definition.Provider != ProviderGitLabCI || definition.File != ".gitlab-ci.yml" {
		t.Errorf("source metadata = %#v, want GitLab root workflow", definition)
	}
	if definition.Environment["PIPELINE_MODE"] != "release" {
		t.Errorf("pipeline environment = %#v, want PIPELINE_MODE=release", definition.Environment)
	}
	if want := []string{"verify", "deploy"}; !reflect.DeepEqual(definition.Stages, want) {
		t.Errorf("Stages = %#v, want %#v", definition.Stages, want)
	}
	if want := []string{"lint", "deploy"}; !reflect.DeepEqual(definition.TopologicalOrder, want) {
		t.Errorf("TopologicalOrder = %#v, want %#v", definition.TopologicalOrder, want)
	}

	deploy := definition.Jobs[1]
	if deploy.Stage != "deploy" || deploy.RunnerHint != "deploy-runner" {
		t.Errorf("deploy metadata = %#v, want stage and runner hint", deploy)
	}
	if !deploy.AllowFailure {
		t.Error("deploy AllowFailure = false, want true")
	}
	if deploy.TimeoutMinutes != 15 {
		t.Errorf("deploy TimeoutMinutes = %d, want 15", deploy.TimeoutMinutes)
	}
	if deploy.EnvironmentName != "production" {
		t.Errorf("deploy EnvironmentName = %q, want production", deploy.EnvironmentName)
	}
	if deploy.DeploymentTier != "production" {
		t.Errorf("deploy DeploymentTier = %q, want production", deploy.DeploymentTier)
	}
	if deploy.Environment["DEPLOY_TARGET"] != "production" {
		t.Errorf("deploy environment = %#v, want DEPLOY_TARGET", deploy.Environment)
	}
	if len(deploy.Steps) != 1 || deploy.Steps[0].Command != "echo deploy" {
		t.Errorf("deploy steps = %#v, want normalized command", deploy.Steps)
	}
}

func TestDiscoverRejectsDependencyCycle(t *testing.T) {
	projectPath := t.TempDir()
	writeWorkflowFixture(t, projectPath, ".github/workflows/cycle.yml", strings.Join([]string{
		"name: Cycle",
		"on: push",
		"jobs:",
		"  alpha:",
		"    needs: omega",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: echo alpha",
		"  omega:",
		"    needs: alpha",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: echo omega",
	}, "\n"))

	_, err := Discover([]store.Project{fixtureProject(t, projectPath, "project-cycle")})
	if err == nil {
		t.Fatal("Discover() error = nil, want cycle validation error")
	}
	if !strings.Contains(err.Error(), "workflow has dependency cycle: alpha -> omega -> alpha") {
		t.Errorf("Discover() error = %q, want deterministic cycle", err)
	}
}

func TestDeterministicTopologicalOrderRejectsMissingDependencies(t *testing.T) {
	_, err := deterministicTopologicalOrder(
		[]string{"deploy", "lint"},
		map[string][]string{
			"deploy": {"missing", "absent"},
		},
	)
	if err == nil {
		t.Fatal("deterministicTopologicalOrder() error = nil, want missing dependency")
	}
	if got, want := err.Error(), `job "deploy" has missing dependency "absent"`; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestDiscoverIsStableAcrossProjectsFilesAndJobs(t *testing.T) {
	firstProjectPath := t.TempDir()
	secondProjectPath := t.TempDir()
	writeWorkflowFixture(t, firstProjectPath, ".github/workflows/zeta.yml", simpleGitHubWorkflow("Zeta", "zeta"))
	writeWorkflowFixture(t, firstProjectPath, ".github/workflows/alpha.yaml", strings.Join([]string{
		"name: Alpha",
		"on: push",
		"jobs:",
		"  zebra:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: echo zebra",
		"  alpha:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: echo alpha",
		"  middle:",
		"    needs: alpha",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: echo middle",
	}, "\n"))
	writeWorkflowFixture(t, firstProjectPath, ".gitlab-ci.yaml", strings.Join([]string{
		"lint:",
		"  script:",
		"    - echo lint",
	}, "\n"))
	writeWorkflowFixture(t, secondProjectPath, ".github/workflows/only.yml", simpleGitHubWorkflow("Second", "only"))

	projects := []store.Project{
		fixtureProject(t, secondProjectPath, "project-second"),
		fixtureProject(t, firstProjectPath, "project-first"),
	}
	first, err := Discover(projects)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("Discover() definitions = %d, want 4", len(first))
	}

	firstProjectFiles := make([]string, 0, 3)
	for _, definition := range first {
		if definition.ProjectID == "project-first" {
			firstProjectFiles = append(firstProjectFiles, definition.File)
		}
	}
	if want := []string{
		".github/workflows/alpha.yaml",
		".github/workflows/zeta.yml",
		".gitlab-ci.yaml",
	}; !reflect.DeepEqual(firstProjectFiles, want) {
		t.Errorf("first project files = %#v, want %#v", firstProjectFiles, want)
	}
	for _, definition := range first {
		if definition.ProjectID == "project-first" && definition.File == ".github/workflows/alpha.yaml" {
			if want := []string{"alpha", "middle", "zebra"}; !reflect.DeepEqual(definition.TopologicalOrder, want) {
				t.Errorf("alpha workflow order = %#v, want %#v", definition.TopologicalOrder, want)
			}
		}
	}

	for attempt := 0; attempt < 8; attempt++ {
		next, err := Discover(projects)
		if err != nil {
			t.Fatalf("Discover() attempt %d error = %v", attempt, err)
		}
		if !reflect.DeepEqual(next, first) {
			t.Fatalf("Discover() attempt %d changed output:\nfirst=%#v\nnext=%#v", attempt, first, next)
		}
	}
}

func TestDiscoverReturnsNoDefinitionsWhenRegisteredProjectsHaveNoWorkflows(t *testing.T) {
	projectPath := t.TempDir()
	definitions, err := Discover([]store.Project{
		fixtureProject(t, projectPath, "project-empty"),
		{ID: "remote-project"},
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(definitions) != 0 {
		t.Errorf("Discover() = %#v, want no definitions", definitions)
	}
	if definitions == nil {
		t.Error("Discover() returned nil slice, want persistence-friendly empty slice")
	}
}

func TestDiscoverNeverFollowsWorkflowSymlinksOrUnsafeGitLabIncludes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}

	projectPath := t.TempDir()
	outsidePath := t.TempDir()
	writeWorkflowFixture(t, outsidePath, "outside.yml", simpleGitHubWorkflow("Outside", "outside"))
	workflowDirectory := filepath.Join(projectPath, ".github", "workflows")
	if err := os.MkdirAll(workflowDirectory, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", workflowDirectory, err)
	}
	if err := os.Symlink(filepath.Join(outsidePath, "outside.yml"), filepath.Join(workflowDirectory, "outside.yml")); err != nil {
		t.Fatalf("Symlink(): %v", err)
	}

	definitions, err := Discover([]store.Project{fixtureProject(t, projectPath, "project-safe")})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(definitions) != 0 {
		t.Errorf("Discover() followed symlink and returned %#v", definitions)
	}

	writeWorkflowFixture(t, projectPath, ".gitlab-ci.yml", strings.Join([]string{
		"include:",
		"  local: ../outside.yml",
		"lint:",
		"  script:",
		"    - echo lint",
	}, "\n"))
	_, err = Discover([]store.Project{fixtureProject(t, projectPath, "project-safe")})
	if err == nil {
		t.Fatal("Discover() error = nil, want unsafe GitLab include rejection")
	}
	if !strings.Contains(err.Error(), "not a regular file inside the project") {
		t.Errorf("Discover() error = %q, want unsafe include message", err)
	}
}

func fixtureProject(t *testing.T, path, id string) store.Project {
	t.Helper()
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	canonicalPath = filepath.Clean(canonicalPath)
	return store.Project{
		ID:            id,
		Slug:          id,
		CanonicalPath: &canonicalPath,
	}
}

func writeWorkflowFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func simpleGitHubWorkflow(name, job string) string {
	return strings.Join([]string{
		"name: " + name,
		"on: push",
		"jobs:",
		"  " + job + ":",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: echo " + job,
	}, "\n")
}

func jobKeys(jobs []JobDefinition) []string {
	keys := make([]string, 0, len(jobs))
	for _, job := range jobs {
		keys = append(keys, job.Key)
	}
	return keys
}
