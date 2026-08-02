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
	"gopkg.in/yaml.v3"
)

type discoverResult struct {
	Directory string           `json:"directory" yaml:"directory"`
	Total     int              `json:"total" yaml:"total"`
	Files     []discoveredFile `json:"files" yaml:"files"`
}

type discoveredFile struct {
	Path     string `json:"path" yaml:"path"`
	Provider string `json:"provider" yaml:"provider"`
	Jobs     int    `json:"jobs,omitempty" yaml:"jobs,omitempty"`
	Detected bool   `json:"detected" yaml:"detected"`
}

func captureStdoutDiscover(t *testing.T, fn func()) string {
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

func writeWorkflowFixtureForDiscover(t *testing.T, dir string, relPath string, content string) {
	t.Helper()

	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

func newDiscoverCtx(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	fs.String("directory", "", "")
	fs.String("format", "tree", "")

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}
	return cli.NewContext(nil, fs, nil)
}

func TestCmdDiscover_WithTreeFormat(t *testing.T) {
	dir := t.TempDir()
	writeWorkflowFixtureForDiscover(t, dir, ".github/workflows/ci.yml", `name: discover-gh
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)

	writeWorkflowFixtureForDiscover(t, dir, ".gitlab-ci.yml", `stages:
  - test
test:
  stage: test
  script:
    - echo hi
`)

	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs dir: %v", err)
	}

	ctx := newDiscoverCtx(t, "--directory", dir, "--format", "tree")
	out := captureStdoutDiscover(t, func() {
		if err := CmdDiscover(ctx); err != nil {
			t.Fatalf("CmdDiscover: %v", err)
		}
	})

	if !strings.Contains(out, absDir) {
		t.Errorf("expected output to include directory %q, got:\n%s", absDir, out)
	}
	if !strings.Contains(out, ".github/workflows/ci.yml [GitHub Actions]") {
		t.Errorf("expected tree output to include GitHub entry, got:\n%s", out)
	}
	if !strings.Contains(out, ".gitlab-ci.yml [GitLab CI]") {
		t.Errorf("expected tree output to include GitLab entry, got:\n%s", out)
	}
	if !strings.Contains(out, "Total: 2 file(s) across 2 provider(s)") {
		t.Errorf("expected two files in output summary, got:\n%s", out)
	}
}

func TestCmdDiscover_WithJSONFormat(t *testing.T) {
	dir := t.TempDir()
	writeWorkflowFixtureForDiscover(t, dir, ".github/workflows/ci.yml", `name: discover-gh
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)

	ctx := newDiscoverCtx(t, "--directory", dir, "--format", "json")
	out := captureStdoutDiscover(t, func() {
		if err := CmdDiscover(ctx); err != nil {
			t.Fatalf("CmdDiscover: %v", err)
		}
	})

	var result discoverResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\n--- out ---\n%s\n---", err, out)
	}
	if result.Total != 1 {
		t.Fatalf("expected one discovered file, got total=%d", result.Total)
	}
	if result.Directory != filepath.Clean(dir) {
		// ensure absolute path for deterministic check
		absDir, _ := filepath.Abs(dir)
		if result.Directory != absDir {
			t.Fatalf("expected directory to be %q or %q, got %q", filepath.Clean(dir), absDir, result.Directory)
		}
	}
	if len(result.Files) != 1 || result.Files[0].Path != ".github/workflows/ci.yml" {
		t.Fatalf("expected one GitHub file entry, got %#v", result.Files)
	}
	if result.Files[0].Provider != "GitHub Actions" {
		t.Fatalf("expected GitHub provider, got %q", result.Files[0].Provider)
	}
	if result.Files[0].Jobs != 1 || !result.Files[0].Detected {
		t.Fatalf("expected one detected job in discover output, got %+v", result.Files[0])
	}
}

func TestCmdDiscover_WithYAMLFormat(t *testing.T) {
	dir := t.TempDir()
	writeWorkflowFixtureForDiscover(t, dir, ".travis.yml", `language: go
go: 1.22
script:
  - go test ./...
`)

	ctx := newDiscoverCtx(t, "--directory", dir, "--format", "yaml")
	out := captureStdoutDiscover(t, func() {
		if err := CmdDiscover(ctx); err != nil {
			t.Fatalf("CmdDiscover: %v", err)
		}
	})

	var result discoverResult
	if err := yaml.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid YAML output: %v\n--- out ---\n%s\n---", err, out)
	}
	if result.Total != 1 {
		t.Fatalf("expected one discovered file, got %d", result.Total)
	}
	if result.Files[0].Provider != "Travis CI" {
		t.Fatalf("expected Travis provider, got %q", result.Files[0].Provider)
	}
}

func TestCmdDiscover_MissingDirectoryErrors(t *testing.T) {
	ghost := filepath.Join(t.TempDir(), "does-not-exist")
	ctx := newDiscoverCtx(t, "--directory", ghost)

	if err := CmdDiscover(ctx); err == nil {
		t.Fatal("expected error for missing directory, got nil")
	}
}
