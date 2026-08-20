package execution

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerExecutesPinnedCommitOutsideRegisteredCheckout(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	resultPath := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "pinned.yml"), strings.Join([]string{
		"name: Pinned source",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: cat payload > " + shellTestPath(resultPath),
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	pinned := managerRepositoryHead(t, root)
	run, err := manager.EnqueueWorkflow(ctx, workflow.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if run.CommitSHA == nil || *run.CommitSHA != pinned {
		t.Fatalf("run commit = %v, want %s", run.CommitSHA, pinned)
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitManagerRepository(t, root)
	if managerRepositoryHead(t, root) == pinned {
		t.Fatal("test branch did not move")
	}
	processed, err := manager.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = (%t, %v)", processed, err)
	}
	contents, err := os.ReadFile(resultPath)
	if err != nil || string(contents) != "first" {
		t.Fatalf("pinned result = %q, %v", contents, err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("pinned run = %#v, %v", graph.Run, err)
	}
	workspacePath, _ := manager.workspaces.SourcePath(run.ID)
	if _, err := os.Stat(workspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal workspace exists: %v", err)
	}
}

func TestResolveGitCommitRejectsNonRepositoryAndInvalidCandidate(t *testing.T) {
	if _, err := resolveGitCommit(t.Context(), t.TempDir(), "HEAD", ""); err == nil {
		t.Fatal("non-Git directory resolved a commit")
	}
	root := t.TempDir()
	runManagerGit(t, root, "init", "-b", "main")
	runManagerGit(t, root, "config", "user.email", "git-ci@example.invalid")
	runManagerGit(t, root, "config", "user.name", "git-ci tests")
	commitManagerRepository(t, root)
	if _, err := resolveGitCommit(t.Context(), root, "HEAD", "--help"); err == nil {
		t.Fatal("option-looking commit candidate was accepted")
	}
	if _, err := resolveGitCommit(t.Context(), root, "refs/heads/missing", ""); err == nil {
		t.Fatal("missing ref resolved a commit")
	}
}

func TestExtractGitArchivePreservesExecutableAndRejectsEscapes(t *testing.T) {
	destination := t.TempDir()
	valid := tarFixture(t, tar.Header{Name: "bin/run", Typeflag: tar.TypeReg, Mode: 0o755, Size: 7}, []byte("#!/bin\n"))
	if err := extractGitArchive(bytes.NewReader(valid), destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "run"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("archived executable mode = %v, %v", info, err)
	}

	escaping := tarFixture(t, tar.Header{Name: "../outside", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}, []byte("x"))
	if err := extractGitArchive(bytes.NewReader(escaping), t.TempDir()); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("escaping archive error = %v", err)
	}
	unsafeLink := tarFixture(t, tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Mode: 0o777, Linkname: "../outside"}, nil)
	if err := extractGitArchive(bytes.NewReader(unsafeLink), t.TempDir()); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("escaping symlink error = %v", err)
	}
}

func TestContainedWorkingDirectoryRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := containedWorkingDirectory(root, "outside"); err == nil || !strings.Contains(err.Error(), "symlink escapes") {
		t.Fatalf("symlink containment error = %v", err)
	}
}

func tarFixture(t *testing.T, header tar.Header, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
