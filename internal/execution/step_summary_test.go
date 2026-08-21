package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubStepSummaryParserAndGuards(t *testing.T) {
	t.Run("normalizes CRLF", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "summary")
		if err := os.WriteFile(path, []byte("# Build\r\n\r\n- pass\r\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		summary, err := parseGitHubStepSummary(path)
		if err != nil || summary != "# Build\n\n- pass\n" {
			t.Fatalf("summary = %q, %v", summary, err)
		}
	})
	for name, contents := range map[string][]byte{
		"null":     []byte("bad\x00summary"),
		"utf8":     {0xff, 0xfe},
		"oversize": []byte(strings.Repeat("x", maxGitHubStepSummaryBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "summary")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := parseGitHubStepSummary(path); err == nil {
				t.Fatal("unsafe summary unexpectedly accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target")
		if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(directory, "summary")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := parseGitHubStepSummary(link); err == nil {
			t.Fatal("summary symlink unexpectedly accepted")
		}
	})
}

func TestGitHubStepSummaryJobLimitResets(t *testing.T) {
	context := newRuntimeOutputContext()
	context.beginJob(nil)
	for index := 0; index < maxGitHubStepSummaries; index++ {
		if !context.reserveGitHubStepSummary() {
			t.Fatalf("summary %d unexpectedly rejected", index+1)
		}
	}
	if context.reserveGitHubStepSummary() {
		t.Fatal("summary beyond job limit unexpectedly accepted")
	}
	context.beginJob(nil)
	if !context.reserveGitHubStepSummary() {
		t.Fatal("new job did not reset summary limit")
	}
}
