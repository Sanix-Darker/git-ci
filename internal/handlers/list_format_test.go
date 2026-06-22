package handlers

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

// writeListFixture drops a minimal GitHub workflow .github/workflows/ci.yml
// in the supplied directory and returns its absolute path.
func writeListFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	src := `name: regression-list-format
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
	path := filepath.Join(wfDir, "ci.yml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// newListCtx builds a minimal *cli.Context that mirrors the flags the
// `git-ci list` subcommand registers on cmd/cli.go. We use stdlib flag
// + urfave/cli's NewContext so we don't need to drive the whole CLI app.
func newListCtx(t *testing.T, file, format string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.String("file", "", "pipeline file")
	fs.String("format", "tree", "output format (tree|json|yaml)")

	args := []string{}
	if file != "" {
		args = append(args, "--file", file)
	}
	if format != "" {
		args = append(args, "--format", format)
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("flag.Parse: %v", err)
	}
	return cli.NewContext(nil, fs, nil)
}

// captureStdout swaps os.Stdout for a pipe, runs fn synchronously, and
// returns whatever was written to it as a string. Synchronous because
// all callers (CmdList, CmdEnvSet, OutputFormatter.Print*) are
// synchronous, and the synchronous form avoids the close-on-channel
// races that a goroutine dance introduces. Uses defer to restore
// os.Stdout so a panic in fn doesn't leave subsequent tests writing
// to a closed pipe.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	data, _ := io.ReadAll(r)
	_ = r.Close()
	return string(data)
}

// TestcmdList_FormatJSON is a regression test for BUG #1 -- the --format
// flag must produce valid JSON, not the plain-text tree rendering.
func TestCmdList_FormatJSON(t *testing.T) {
	fixture := writeListFixture(t)
	ctx := newListCtx(t, fixture, "json")

	out := captureStdout(t, func() {
		if err := CmdList(ctx); err != nil {
			t.Fatalf("CmdList: %v", err)
		}
	})

	out = strings.TrimSpace(out)
	if out == "" {
		t.Fatal("expected JSON output, got empty string")
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v\n--- raw ---\n%s\n--- end ---", err, out)
	}

	jobs, ok := doc["jobs"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected 'jobs' object in JSON, got %T (%v)", doc["jobs"], doc["jobs"])
	}
	if _, ok := jobs["hello"]; !ok {
		t.Errorf("expected 'hello' job in JSON, got keys %v", jobKeys(jobs))
	}
	if _, ok := jobs["world"]; !ok {
		t.Errorf("expected 'world' job in JSON, got keys %v", jobKeys(jobs))
	}
}

// TestcmdList_FormatYAML is a regression test for BUG #1 (yaml branch).
func TestCmdList_FormatYAML(t *testing.T) {
	fixture := writeListFixture(t)
	ctx := newListCtx(t, fixture, "yaml")

	out := captureStdout(t, func() {
		if err := CmdList(ctx); err != nil {
			t.Fatalf("CmdList: %v", err)
		}
	})

	if !strings.Contains(out, "name: regression-list-format") {
		t.Errorf("expected YAML to contain 'name: regression-list-format', got:\n%s", out)
	}
	if !strings.Contains(out, "jobs:") {
		t.Errorf("expected YAML to contain 'jobs:' key, got:\n%s", out)
	}
	if !strings.Contains(out, "hello:") {
		t.Errorf("expected YAML to contain 'hello:' job, got:\n%s", out)
	}
	if !strings.Contains(out, "world:") {
		t.Errorf("expected YAML to contain 'world:' job, got:\n%s", out)
	}

	// Bug surfaced earlier produced plain-text tree rendering; assert we
	// really emitted YAML by sniffing for `key: value` lines.
	if strings.Contains(out, "Pipeline:") || strings.Contains(out, "├──") {
		t.Errorf("YAML output looked like a plain-text tree:\n%s", out)
	}

	// YAML output must end with a trailing newline so consumers don't have
	// to special-case the last line.
	if !strings.HasSuffix(out, "\n") {
		t.Error("expected YAML output to end with a newline")
	}
}

// TestcmdList_FormatTreeKeepsHumanRendering guards against accidentally
// turning the default tree renderer off while fixing the json/yaml bug.
func TestCmdList_FormatTreeKeepsHumanRendering(t *testing.T) {
	fixture := writeListFixture(t)
	ctx := newListCtx(t, fixture, "tree")

	out := captureStdout(t, func() {
		if err := CmdList(ctx); err != nil {
			t.Fatalf("CmdList: %v", err)
		}
	})

	if !strings.Contains(out, "Pipeline:") {
		t.Errorf("expected tree output to start with 'Pipeline:' header, got:\n%s", out)
	}
	if !strings.Contains(out, "Jobs:") {
		t.Errorf("expected tree output to contain 'Jobs:' section, got:\n%s", out)
	}
	if !strings.Contains(out, "Total:") {
		t.Errorf("expected tree output to contain 'Total:' summary, got:\n%s", out)
	}
}

func jobKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
