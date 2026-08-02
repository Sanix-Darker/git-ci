package handlers

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

func captureStdoutInit(t *testing.T, fn func()) string {
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

func newInitCtx(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.String("provider", "github", "")
	fs.String("template", "basic", "")
	fs.String("output", "", "")
	fs.Bool("force", false, "")

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}
	return cli.NewContext(nil, fs, nil)
}

func TestCmdInit_CreatesDefaultGitHubOutput(t *testing.T) {
	dir := t.TempDir()
	origHome := t.TempDir()
	t.Setenv("HOME", origHome)

	t.Chdir(dir)

	ctx := newInitCtx(t)
	_, err := os.Stat(filepath.Join(".github", "workflows", "ci.yml"))
	if err == nil {
		t.Fatalf("fixture already exists, expected clean temp dir")
	}

	out := captureStdoutInit(t, func() {
		if runErr := CmdInit(ctx); runErr != nil {
			t.Fatalf("CmdInit: %v", runErr)
		}
	})

	path := filepath.Join(".github", "workflows", "ci.yml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected output file %s to be created: %v", path, err)
	}
	if !strings.Contains(out, "Created github pipeline: "+path) {
		t.Errorf("expected success message for %s, got:\n%s", path, out)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !strings.Contains(string(content), "name: CI") {
		t.Errorf("expected default GitHub template content in %s", path)
	}
}

func TestCmdInit_DefaultsGitLabOutputPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	ctx := newInitCtx(t, "--provider", "gitlab")
	if err := CmdInit(ctx); err != nil {
		t.Fatalf("CmdInit: %v", err)
	}

	if _, err := os.Stat(".gitlab-ci.yml"); err != nil {
		t.Fatalf("expected .gitlab-ci.yml: %v", err)
	}
}

func TestCmdInit_RefusesOverwriteUntilForce(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	output := "existing.yml"
	if err := os.WriteFile(output, []byte("placeholder"), 0o644); err != nil {
		t.Fatalf("write existing output file: %v", err)
	}

	runErr := CmdInit(newInitCtx(t, "--provider", "github", "--output", output))
	if runErr == nil {
		t.Fatal("expected error when output exists and --force is not set")
	}
	if !strings.Contains(runErr.Error(), "already exists. Use --force to overwrite") {
		t.Fatalf("expected overwrite guidance, got: %v", runErr)
	}

	if err := CmdInit(newInitCtx(t, "--provider", "github", "--output", output, "--force")); err != nil {
		t.Fatalf("CmdInit --force: %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected output file after --force: %v", err)
	}
}
