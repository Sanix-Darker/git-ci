package gitrepository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTagCommitValidatesAndPeelsLocalTags(t *testing.T) {
	root := t.TempDir()
	runTagGit(t, root, "init", "-b", "main")
	runTagGit(t, root, "config", "user.email", "release@gci.invalid")
	runTagGit(t, root, "config", "user.name", "gci release test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTagGit(t, root, "add", "README.md")
	runTagGit(t, root, "commit", "-m", "release")
	head := strings.TrimSpace(runTagGit(t, root, "rev-parse", "HEAD"))
	runTagGit(t, root, "tag", "-a", "v1.2.3", "-m", "version 1.2.3")
	resolved, err := ResolveTagCommit(context.Background(), root, "v1.2.3")
	if err != nil || resolved != head {
		t.Fatalf("resolved tag = %q, %v; want %q", resolved, err, head)
	}
	for _, tag := range []string{"", "bad tag", "missing"} {
		if _, err := ResolveTagCommit(context.Background(), root, tag); err == nil {
			t.Fatalf("tag %q unexpectedly resolved", tag)
		}
	}
}

func runTagGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
