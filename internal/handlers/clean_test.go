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

func captureStdoutClean(t *testing.T, fn func()) string {
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

func newCleanCtx(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.Bool("all", false, "")
	fs.Bool("containers", false, "")
	fs.Bool("images", false, "")
	fs.Bool("cache", false, "")
	fs.Bool("force", false, "")

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}
	return cli.NewContext(nil, fs, nil)
}

func TestCmdClean_NoTargetsPrintsGuide(t *testing.T) {
	ctx := newCleanCtx(t, "--all=false", "--cache=false", "--containers=false", "--images=false")

	out := captureStdoutClean(t, func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if strings.TrimSpace(out) != "Nothing to clean. Use --all or specify what to clean." {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestCmdClean_CacheOnlyRemovesKnownDirectories(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Chdir(root)

	cacheDirs := []string{
		".git-ci-cache",
		".git-ci",
		filepath.Join("tmp", "git-ci"),
		filepath.Join(home, ".cache", "git-ci"),
		filepath.Join(home, ".git-ci"),
	}

	for _, dir := range cacheDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	ctx := newCleanCtx(t, "--cache")
	out := captureStdoutClean(t, func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if !strings.Contains(out, "Cleaning up resources...") {
		t.Fatalf("expected cache clean banner, got:\n%s", out)
	}
	if !strings.Contains(out, "Removed 5 cache director(ies)") {
		t.Fatalf("expected cache removal count, got:\n%s", out)
	}

	for _, dir := range cacheDirs {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("expected %s removed, got exists", dir)
		}
	}
}
