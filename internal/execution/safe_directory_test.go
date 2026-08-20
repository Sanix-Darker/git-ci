package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestApprovedCheckoutSupportsDifferentOwner(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	gitSafeDirectoryTest(t, repository, "init", "-b", "main")
	gitSafeDirectoryTest(t, repository, "config", "user.email", "git-ci@example.invalid")
	gitSafeDirectoryTest(t, repository, "config", "user.name", "git-ci tests")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("approved checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitSafeDirectoryTest(t, repository, "add", "-A")
	gitSafeDirectoryTest(t, repository, "commit", "-m", "initial")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")

	commit, err := resolveGitCommit(context.Background(), repository, "refs/heads/main", "")
	if err != nil {
		t.Fatalf("resolve different-owner checkout: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := materializeGitArchive(context.Background(), repository, commit, destination); err != nil {
		t.Fatalf("archive different-owner checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "README.md")); err != nil {
		t.Fatalf("archived README: %v", err)
	}
}

func gitSafeDirectoryTest(t *testing.T, path string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}
