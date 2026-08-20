package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cli "github.com/urfave/cli/v2"
)

// buildApp constructs the real *cli.App the same way main() does, but
// stripped of os.Exit / metadata so it can be invoked from tests.
func buildApp() *cli.App {
	return &cli.App{
		Name:     "git-ci",
		Usage:    "Run CI/CD pipelines locally",
		Version:  formatVersion(),
		Flags:    globalFlags(),
		Commands: commands(),
	}
}

// runAppWithStdout captures stdout while invoking app.Run(...) and
// returns the captured output plus the error returned by Run. Uses
// defer to restore os.Stdout so a panic in app.Run doesn't leave
// subsequent tests writing to a closed pipe.
func runAppWithStdout(t *testing.T, args []string) (string, error) {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	var (
		buf  bytes.Buffer
		done = make(chan struct{})
	)
	go func() {
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		close(done)
	}()

	runErr := buildApp().Run(args)

	_ = w.Close()
	<-done

	return buf.String(), runErr
}

func runGoRunCommandOutput(t *testing.T, args []string) (string, error) {
	t.Helper()

	cmd := exec.Command("go", append([]string{"run", "."}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	return out.String(), runErr
}

func TestSetupEnvironment_PopulatesDefaultCIValues(t *testing.T) {
	originalCI := os.Getenv("CI")
	originalGitCI := os.Getenv("GIT_CI")
	originalGitCIVersion := os.Getenv("GIT_CI_VERSION")

	t.Cleanup(func() {
		_ = os.Setenv("CI", originalCI)
		_ = os.Setenv("GIT_CI", originalGitCI)
		_ = os.Setenv("GIT_CI_VERSION", originalGitCIVersion)
	})

	if err := os.Setenv("CI", ""); err != nil {
		t.Fatalf("set CI: %v", err)
	}
	if err := os.Setenv("GIT_CI", ""); err != nil {
		t.Fatalf("set GIT_CI: %v", err)
	}
	if err := os.Setenv("GIT_CI_VERSION", ""); err != nil {
		t.Fatalf("set GIT_CI_VERSION: %v", err)
	}

	setupEnvironment()

	if got := os.Getenv("CI"); got != "true" {
		t.Fatalf("expected CI=true, got %q", got)
	}
	if got := os.Getenv("GIT_CI"); got != "true" {
		t.Fatalf("expected GIT_CI=true, got %q", got)
	}
	if got := os.Getenv("GIT_CI_VERSION"); got == "" {
		t.Fatalf("expected GIT_CI_VERSION to be set")
	}
}

func TestSetupEnvironment_PreservesExistingCIValues(t *testing.T) {
	originalCI := os.Getenv("CI")
	originalGitCI := os.Getenv("GIT_CI")

	t.Cleanup(func() {
		_ = os.Setenv("CI", originalCI)
		_ = os.Setenv("GIT_CI", originalGitCI)
	})

	if err := os.Setenv("CI", "from-user"); err != nil {
		t.Fatalf("set CI: %v", err)
	}
	if err := os.Setenv("GIT_CI", "from-user"); err != nil {
		t.Fatalf("set GIT_CI: %v", err)
	}

	setupEnvironment()

	if got := os.Getenv("CI"); got != "from-user" {
		t.Fatalf("expected CI to remain from user, got %q", got)
	}
	if got := os.Getenv("GIT_CI"); got != "from-user" {
		t.Fatalf("expected GIT_CI to remain from user, got %q", got)
	}
}

func TestServeCommandUsesSafeDefaults(t *testing.T) {
	var serveCommand *cli.Command
	for _, command := range commands() {
		if command.Name == "serve" {
			serveCommand = command
			break
		}
	}
	if serveCommand == nil {
		t.Fatal("serve command is not registered")
	}

	flags := make(map[string]cli.Flag, len(serveCommand.Flags))
	for _, flag := range serveCommand.Flags {
		flags[flag.Names()[0]] = flag
	}
	listen, ok := flags["listen"].(*cli.StringFlag)
	if !ok || listen.Value != "127.0.0.1:8087" {
		t.Fatalf("listen default = %#v, want loopback 127.0.0.1:8087", flags["listen"])
	}
	stateDir, ok := flags["state-dir"].(*cli.StringFlag)
	if !ok || stateDir.Value != ".gci-service" {
		t.Fatalf("state-dir default = %#v", flags["state-dir"])
	}
	sessionTTL, ok := flags["session-ttl"].(*cli.DurationFlag)
	if !ok || sessionTTL.Value != 8*time.Hour {
		t.Fatalf("session-ttl default = %#v", flags["session-ttl"])
	}
	maxBody, ok := flags["max-body-bytes"].(*cli.Int64Flag)
	if !ok || maxBody.Value != 1<<20 {
		t.Fatalf("max-body-bytes default = %#v", flags["max-body-bytes"])
	}
}

func TestCliApp_Run_RespectsGITCIFileEnvVar(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	fixture := filepath.Join(dir, ".github", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(fixture), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := `name: cli-test-regression
on: [push]
jobs:
  hello:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`
	if err := os.WriteFile(fixture, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	workdir := dir
	if err := os.Setenv("GIT_CI_FILE", fixture); err != nil {
		t.Fatalf("set GIT_CI_FILE: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("GIT_CI_FILE", "")
		_ = os.Chdir(originalWD)
	})
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	out, err := runAppWithStdout(t, []string{"gci", "run", "--dry-run"})
	if err != nil {
		t.Fatalf("gci run --dry-run using GIT_CI_FILE failed: %v\nout: %s", err, out)
	}
	if !strings.Contains(out, "Running 1 job(s) sequentially") {
		t.Fatalf("expected run to execute selected workflow from GIT_CI_FILE, got: %s", out)
	}
}

// writeWorkflowFixture drops a minimal GitHub workflow into t.TempDir()/.github/workflows/ci.yml.
func writeWorkflowFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows", "ci.yml")
	if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := `name: cli-test-regression
on: [push]
jobs:
  hello:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
  world:
    runs-on: ubuntu-latest
    steps:
      - run: echo world
`
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return wf
}

func writeGitLabWorkflowFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	wf := filepath.Join(dir, ".gitlab-ci.yml")
	src := `stages:
  - test
  - verify
test:
  stage: test
  script:
    - echo test
verify:
  stage: verify
  script:
    - echo verify
  needs: [test]
`
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatalf("write gitlab fixture: %v", err)
	}
	return wf
}

func writeTwoJobWorkflowFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	wf := filepath.Join(dir, ".github", "workflows", "ci.yml")
	src := `name: cli-test-two-jobs
on: [push]
jobs:
  hello:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
  world:
    runs-on: ubuntu-latest
    steps:
      - run: echo world
`
	if err := os.MkdirAll(filepath.Dir(wf), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return wf
}

func writeTravisStyleFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	wf := filepath.Join(dir, ".travis.yml")
	src := `language: go
script:
  - echo hello
`
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatalf("write travis fixture: %v", err)
	}
	return wf
}

// -----------------------------------------------------------------------------
// BUG #2 real-app regression: cli.go must register a BoolFlag named
// `verbose` on the `env list` subcommand. urfave/cli only complains
// about unknown flags when we drive the *real* app, so this test
// covers what stdlib-flag-based unit tests cannot.
// -----------------------------------------------------------------------------

func TestCliApp_EnvListVerbose_NoUnknownFlag(t *testing.T) {
	key := "GIT_CI_TEST_VERBOSE_" + strings.NewReplacer("/", "_").Replace(t.Name())
	t.Setenv(key, "should-appear")

	out, err := runAppWithStdout(t, []string{"gci", "env", "list", "--verbose"})
	if err != nil {
		if strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("env list --verbose crashed (BUG #2 regression): %v", err)
		}
		t.Fatalf("unexpected run error: %v", err)
	}
	if !strings.Contains(out, key) {
		t.Errorf("env list --verbose should include %q from env, got output:\n%s", key, out)
	}
}

// -----------------------------------------------------------------------------
// BUG #3 real-app regression: `env set KEY=v --save --file out` must
// not return "invalid format: --save" when the user writes the flag
// after the positional KEY=VAL. stdlib flag.Parse does not reproduce
// urfave/cli's flag-after-positional quirk, so the unit test is not
// enough on its own.
// -----------------------------------------------------------------------------

func TestCliApp_EnvSetSaveAfterPositional_PersistsFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "save-after.env")

	runOut, err := runAppWithStdout(t, []string{
		"gci", "env", "set", "REAL_KEY=hello", "--save", "--file", out,
	})
	if err != nil {
		t.Fatalf("env set ... --save --file errored (BUG #3 regression): %v\nstdout:\n%s", err, runOut)
	}

	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatalf("env file not created: %v", rerr)
	}
	if !strings.Contains(string(data), "REAL_KEY=hello") {
		t.Errorf("expected REAL_KEY=hello persisted, got:\n%s", data)
	}
}

func TestCliApp_EnvSetSaveBeforePositional_PersistsFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "save-before.env")

	runOut, err := runAppWithStdout(t, []string{
		"gci", "env", "set", "--save", "--file", out, "OTHER_KEY=world",
	})
	if err != nil {
		t.Fatalf("env set --save KEY=v errored: %v\nstdout:\n%s", err, runOut)
	}

	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatalf("env file not created: %v", rerr)
	}
	if !strings.Contains(string(data), "OTHER_KEY=world") {
		t.Errorf("expected OTHER_KEY=world persisted, got:\n%s", data)
	}
}

// -----------------------------------------------------------------------------
// BUG #1 real-app regression: `list --format json` must emit valid JSON.
// Drift in cli.go's registered flags would break this trip.
// -----------------------------------------------------------------------------

func TestCliApp_ListFormatJSON_EmitsValidJSON(t *testing.T) {
	fixture := writeWorkflowFixture(t)
	out, err := runAppWithStdout(t, []string{"gci", "list", "--format", "json", "-f", fixture})
	if err != nil {
		t.Fatalf("list --format json errored: %v", err)
	}

	var doc map[string]interface{}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); jerr != nil {
		t.Fatalf("not valid JSON (BUG #1 regression): %v\n--- raw ---\n%s\n--- end ---", jerr, out)
	}
	jobs, ok := doc["jobs"].(map[string]interface{})
	if !ok {
		t.Errorf("missing 'jobs' object: %v", doc["jobs"])
	}
	for _, want := range []string{"hello", "world"} {
		if _, ok := jobs[want]; !ok {
			t.Errorf("expected job %q in JSON, got keys %v", want, jobKeys(jobs))
		}
	}
}

func TestCliApp_List_MissingFileErrorsClearly(t *testing.T) {
	out, err := runAppWithStdout(t, []string{"gci", "list", "--file", "i-do-not-exist.yml"})
	if err == nil {
		t.Fatalf("expected list on missing file to fail, got nil\nstdout:\n%s", out)
	}
	if !strings.Contains(err.Error(), "workflow file not found") {
		t.Fatalf("expected missing-file error, got: %v", err)
	}
}

func TestCliApp_ListFormatYAML_EmitsYAML(t *testing.T) {
	fixture := writeWorkflowFixture(t)
	out, err := runAppWithStdout(t, []string{"gci", "list", "--format", "yaml", "-f", fixture})
	if err != nil {
		t.Fatalf("list --format yaml errored: %v", err)
	}
	for _, want := range []string{"name: cli-test-regression", "jobs:", "hello:"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected yaml to contain %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Pipeline:") || strings.Contains(out, "├──") {
		t.Errorf("yaml output looked like plain-text tree:\n%s", out)
	}
}

func TestCliApp_ListSupportsGitLabPath(t *testing.T) {
	fixture := writeGitLabWorkflowFixture(t)
	out, err := runAppWithStdout(t, []string{"gci", "list", "--format", "json", "-f", fixture})
	if err != nil {
		t.Fatalf("list --format json -f .gitlab-ci.yml errored: %v", err)
	}

	var doc map[string]interface{}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); jerr != nil {
		t.Fatalf("expected JSON output, got: %v", jerr)
	}
	jobs, ok := doc["jobs"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing jobs object: %#v", doc["jobs"])
	}
	if _, ok := jobs["test"]; !ok {
		t.Fatalf("expected gitlab fixture job 'test', got keys: %v", jobKeys(jobs))
	}
}

func TestCliApp_Run_DebugPrintsParsedPipeline(t *testing.T) {
	fixture := writeWorkflowFixture(t)
	out, err := runAppWithStdout(t, []string{
		"gci", "--debug", "run", "--dry-run", "-f", fixture,
	})
	if err != nil {
		t.Fatalf("gci --debug run --dry-run errored: %v", err)
	}
	if !strings.Contains(out, "Parsed pipeline:") {
		t.Errorf("expected debug output to include parsed pipeline header, got:\n%s", out)
	}
}

func TestCliApp_Run_VerbosePrintsParsedPipeline(t *testing.T) {
	fixture := writeWorkflowFixture(t)
	out, err := runAppWithStdout(t, []string{
		"gci", "--verbose", "run", "--dry-run", "-f", fixture,
	})
	if err != nil {
		t.Fatalf("gci --verbose run --dry-run errored: %v", err)
	}
	if !strings.Contains(out, "Parsed pipeline:") {
		t.Errorf("expected verbose output to include parsed pipeline header, got:\n%s", out)
	}
}

func TestCliApp_Run_MissingEnvFileFails(t *testing.T) {
	fixture := writeWorkflowFixture(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.env")

	out, err := runAppWithStdout(t, []string{
		"gci", "run", "--dry-run", "--env-file", missing, "--file", fixture,
	})
	if err == nil {
		t.Fatalf("expected error for missing env file, got nil; stdout:\n%s", out)
	}
	if !strings.Contains(err.Error(), "failed to load env file") {
		t.Errorf("expected env file load error, got: %v", err)
	}
}

func writeStageWorkflowFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	wf := filepath.Join(dir, ".gitlab-ci.yml")
	src := `stages:
  - test
  - build
test:
  stage: test
  script:
    - echo test
build:
  stage: build
  needs: [test]
  script:
    - echo build
`
	if err := os.WriteFile(wf, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return wf
}

func TestCliApp_ListAliasLs(t *testing.T) {
	fixture := writeWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{"gci", "ls", "--format", "json", "-f", fixture})
	if err != nil {
		t.Fatalf("gci ls --format json errored: %v", err)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("expected JSON from ls alias: %v\nstdout:\n%s", err, out)
	}
	if _, ok := doc["jobs"]; !ok {
		t.Fatalf("expected jobs in ls output: %v", doc)
	}
}

func TestCliApp_Run_DryRunRunsFilteredJob(t *testing.T) {
	fixture := writeWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{"gci", "run", "--dry-run", "--job", "hello", "-f", fixture})
	if err != nil {
		t.Fatalf("run --dry-run --job hello errored: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "Running 1 job(s) sequentially") {
		t.Errorf("expected single-job dry-run message, got:\n%s", out)
	}
	if !strings.Contains(out, "Job 'hello' succeeded") {
		t.Errorf("expected hello job completion marker, got:\n%s", out)
	}
}

func TestCliApp_Run_OnlyPatternNoMatchWarnsAndErrors(t *testing.T) {
	fixture := writeWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{
		"gci", "run", "--dry-run", "--job", "does-not-exist", "-f", fixture,
	})
	if err == nil {
		t.Fatalf("expected no-job error, got nil; stdout:\n%s", out)
	}
	if !strings.Contains(out, "Warning: job 'does-not-exist' not found") {
		t.Errorf("expected unmatched-job warning, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no jobs to run") {
		t.Errorf("expected no-jobs-to-run error, got: %v", err)
	}
}

func TestCliApp_Run_OnlyPatternByWildcard(t *testing.T) {
	fixture := writeWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{"gci", "run", "--dry-run", "--job", "h*", "-f", fixture})
	if err != nil {
		t.Fatalf("run --dry-run --job h* errored: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "Running 1 job(s) sequentially") {
		t.Errorf("expected one selected job in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Job 'hello'") {
		t.Errorf("expected selected hello job to run, got:\n%s", out)
	}
}

func TestCliApp_Run_StageFilter(t *testing.T) {
	fixture := writeStageWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{
		"gci", "run", "--dry-run", "--stage", "test", "-f", fixture,
	})
	if err != nil {
		t.Fatalf("run --dry-run --stage test errored: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "Running 1 job(s) sequentially") {
		t.Errorf("expected one test-stage job, got:\n%s", out)
	}
}

func TestCliApp_Run_DryRunRunsAllJobs(t *testing.T) {
	fixture := writeTwoJobWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{"gci", "run", "--dry-run", "-f", fixture})
	if err != nil {
		t.Fatalf("run --dry-run -f fixture errored: %v\nstdout:\n%s", err, out)
	}
	if !strings.Contains(out, "Running 2 job(s) sequentially") {
		t.Errorf("expected full run with two jobs, got:\n%s", out)
	}
	if !strings.Contains(out, "Job 'hello' succeeded") {
		t.Errorf("expected hello job completion marker, got:\n%s", out)
	}
	if !strings.Contains(out, "Job 'world' succeeded") {
		t.Errorf("expected world job completion marker, got:\n%s", out)
	}
}

func TestCliApp_Run_PullEqualsFalseIsAccepted(t *testing.T) {
	fixture := writeWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{
		"gci", "run", "--dry-run", "--pull=false", "-f", fixture,
	})
	if err != nil {
		t.Fatalf("run --dry-run --pull=false should be accepted, got: %v\nstdout:\n%s", err, out)
	}
}

func TestCliApp_Run_PullEqualsTrueIsAccepted(t *testing.T) {
	fixture := writeWorkflowFixture(t)

	out, err := runAppWithStdout(t, []string{
		"gci", "run", "--dry-run", "--pull=true", "-f", fixture,
	})
	if err != nil {
		t.Fatalf("run --dry-run --pull=true should be accepted, got: %v\nstdout:\n%s", err, out)
	}
}

func TestCliApp_Validate_ProviderForcesParser(t *testing.T) {
	fixture := writeTravisStyleFixture(t)

	autoOut, autoErr := runAppWithStdout(t, []string{"gci", "validate", "--provider", "auto", "-f", fixture})
	if autoErr != nil {
		t.Fatalf("validate --provider auto should follow auto-detection, got %v output:\n%s", autoErr, autoOut)
	}

	githubOut, githubErr := runAppWithStdout(t, []string{"gci", "validate", "--provider", "github", "-f", fixture})
	if githubErr == nil {
		t.Fatalf("validate --provider github should force GitHub parser on travis fixture, expected validation parse failure, got success; output:\n%s", githubOut)
	}
	if !strings.Contains(githubErr.Error(), "validation failed") && !strings.Contains(githubOut, "Validation errors found") {
		t.Fatalf("expected validation failure output for forced parser path, got error=%v output:\n%s", githubErr, githubOut)
	}
}

// jobKeys returns the sorted key set of a JSON-decoded jobs map.
func jobKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// -----------------------------------------------------------------------------
// BUG #6 real-app regression: `env set --save --file out` (no
// positional KEY=VALUE) must return an error AND must NOT create the
// env file AND the error must mention --save so the user can link
// the failure back to the flag they typed. urfave/cli quirk handling
// only surfaces through a real *cli.App run, so the stdlib flag test
// in env_test.go is not enough on its own.
// -----------------------------------------------------------------------------

func TestCliApp_EnvSetSaveAlone_ErrorsAndNoSideEffect(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "should-not-exist.env")

	runOut, runErr := runAppWithStdout(t, []string{
		"gci", "env", "set", "--save", "--file", out,
	})
	if runErr == nil {
		t.Fatalf("expected error when env set --save alone, got nil; stdout:\n%s", runOut)
	}
	if !strings.Contains(runErr.Error(), "--save") {
		t.Errorf("BUG #6 regression: error doesn't mention --save so the user can't tie the failure to their --save flag: %v", runErr)
	}

	if _, statErr := os.Stat(out); statErr == nil {
		t.Errorf("BUG #6 regression: env file was incorrectly created at %s on the error path (silent side effect)", out)
	}
}

func TestCliApp_NoArgsShowsHelp(t *testing.T) {
	out, err := runAppWithStdout(t, []string{"gci"})
	if err != nil {
		t.Fatalf("running git-ci with no args should show help, got error: %v", err)
	}
	if !strings.Contains(out, "USAGE:") || !strings.Contains(out, "git-ci") {
		t.Errorf("expected top-level usage in output, got:\n%s", out)
	}
}

func TestCliApp_HelpFlagWorks(t *testing.T) {
	out, err := runAppWithStdout(t, []string{"gci", "--help"})
	if err != nil {
		t.Fatalf("gci --help should not fail, got: %v", err)
	}
	if !strings.Contains(out, "USAGE:") || !strings.Contains(out, "COMMANDS:") {
		t.Errorf("expected help sections in output, got:\n%s", out)
	}
}

func TestCliApp_VersionFlag(t *testing.T) {
	out, err := runAppWithStdout(t, []string{"gci", "--version"})
	if err != nil {
		t.Fatalf("gci --version should not fail, got: %v", err)
	}
	if !strings.Contains(out, "git-ci version ") {
		t.Errorf("expected version string in output, got:\n%s", out)
	}
}

func TestCliApp_UnknownSubcommand(t *testing.T) {
	out, err := runGoRunCommandOutput(t, []string{"banana"})
	if !strings.Contains(out, "No help topic for 'banana'") {
		t.Fatalf("expected unknown-subcommand message, got: %v (stdout: %s)", err, out)
	}
}
