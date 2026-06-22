package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

// buildApp constructs the real *cli.App the same way main() does, but
// stripped of os.Exit / metadata so it can be invoked from tests.
func buildApp() *cli.App {
	return &cli.App{
		Name:     "git-ci",
		Usage:    "Run CI/CD pipelines locally",
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

// jobKeys returns the sorted key set of a JSON-decoded jobs map.
func jobKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
