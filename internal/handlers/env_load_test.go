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

func captureStdoutLoad(t *testing.T, fn func()) string {
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

func newEnvLoadCtx(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("env load", flag.ContinueOnError)
	fs.String("file", ".env", "")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}
	return cli.NewContext(nil, fs, nil)
}

func TestCmdEnvLoad_MissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	err := CmdEnvLoad(newEnvLoadCtx(t))
	if err == nil {
		t.Fatal("expected missing-file error")
	}
	if !strings.Contains(err.Error(), "environment file not found: .env") {
		t.Fatalf("expected .env missing-file error, got %v", err)
	}
}

func TestCmdEnvLoad_LoadsVariablesAndMasksSensitiveValues(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	envFile := filepath.Join(dir, "sample.env")
	content := `# environment fixture
FOO=bar
API_KEY=supersecret
`
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	out := captureStdoutLoad(t, func() {
		if err := CmdEnvLoad(newEnvLoadCtx(t, "--file", envFile)); err != nil {
			t.Fatalf("CmdEnvLoad: %v", err)
		}
	})

	if got := os.Getenv("FOO"); got != "bar" {
		t.Fatalf("expected FOO=bar after load, got %q", got)
	}
	if got := os.Getenv("API_KEY"); got != "supersecret" {
		t.Fatalf("expected API_KEY=supersecret after load, got %q", got)
	}
	if !strings.Contains(out, "Loading environment from") {
		t.Fatalf("expected loading header, got:\n%s", out)
	}
	if !strings.Contains(out, "✓ Loaded 2 environment variable(s)") {
		t.Fatalf("expected count footer, got:\n%s", out)
	}
}

func TestCmdEnvLoad_EmptyFilePrintsNotice(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	envFile := filepath.Join(dir, "empty.env")
	if err := os.WriteFile(envFile, []byte("  \n# comment\n"), 0o600); err != nil {
		t.Fatalf("write empty env file: %v", err)
	}

	out := captureStdoutLoad(t, func() {
		if err := CmdEnvLoad(newEnvLoadCtx(t, "--file", envFile)); err != nil {
			t.Fatalf("CmdEnvLoad: %v", err)
		}
	})

	if !strings.Contains(out, "No environment variables found in "+envFile) {
		t.Fatalf("expected empty-file notice, got:\n%s", out)
	}
}
