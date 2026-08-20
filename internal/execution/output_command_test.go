package execution

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseGitHubOutputScalarMultilineAndLastWriteWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	contents := "version=1.0.0\nnotes<<END\nline one\nline two\nEND\nversion=1.0.1\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	outputs, err := parseGitHubOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	if outputs["version"] != "1.0.1" || outputs["notes"] != "line one\nline two" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestParseGitHubOutputRejectsUnsafeFilesAndCommands(t *testing.T) {
	t.Run("invalid name", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "output")
		_ = os.WriteFile(path, []byte("bad name=value\n"), 0o600)
		if _, err := parseGitHubOutput(path); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unterminated multiline", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "output")
		_ = os.WriteFile(path, []byte("value<<END\nmissing\n"), 0o600)
		if _, err := parseGitHubOutput(path); err == nil || !strings.Contains(err.Error(), "unterminated") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "output")
		_ = os.WriteFile(path, []byte("value="+strings.Repeat("x", maxGitHubOutputBytes)), 0o600)
		if _, err := parseGitHubOutput(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink permissions differ on Windows")
		}
		root := t.TempDir()
		target := filepath.Join(root, "target")
		link := filepath.Join(root, "output")
		_ = os.WriteFile(target, []byte("value=unsafe\n"), 0o600)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := parseGitHubOutput(link); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("error = %v", err)
		}
	})
}
