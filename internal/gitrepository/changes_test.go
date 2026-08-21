package gitrepository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestChangedPathsSupportsDirectAndMergeBaseDiffs(t *testing.T) {
	repository := t.TempDir()
	git(t, repository, "init", "-b", "main")
	git(t, repository, "config", "user.email", "git-repository@gci.invalid")
	git(t, repository, "config", "user.name", "gci git repository tests")
	write(t, filepath.Join(repository, "README.md"), "base\n")
	git(t, repository, "add", ".")
	git(t, repository, "commit", "-m", "base")
	base := git(t, repository, "rev-parse", "HEAD")

	git(t, repository, "switch", "-c", "feature/paths")
	write(t, filepath.Join(repository, "src", "feature.go"), "package feature\n")
	git(t, repository, "add", ".")
	git(t, repository, "commit", "-m", "feature")
	head := git(t, repository, "rev-parse", "HEAD")

	for _, mode := range []DiffMode{DiffDirect, DiffMergeBase} {
		paths, err := ChangedPaths(context.Background(), repository, base, head, mode)
		if err != nil {
			t.Fatalf("mode %d: %v", mode, err)
		}
		if !reflect.DeepEqual(paths, []string{"src/feature.go"}) {
			t.Fatalf("mode %d paths = %#v", mode, paths)
		}
	}
	empty, err := ChangedPaths(context.Background(), repository, head, head, DiffDirect)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty diff = %#v, %v", empty, err)
	}
}

func TestChangedPathsRejectsUntrustedRevisions(t *testing.T) {
	_, err := ChangedPaths(context.Background(), t.TempDir(), "--output=/tmp/escape", strings.Repeat("a", 40), DiffDirect)
	if err == nil || !strings.Contains(err.Error(), "full object IDs") {
		t.Fatalf("invalid revision error = %v", err)
	}
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "safe.directory=" + repository, "-C", repository}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
