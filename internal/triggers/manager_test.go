package triggers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/execution"
	"github.com/sanix-darker/git-ci/internal/store"
)

func TestCommitTriggerBaselinesFiltersAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repository := filepath.Join(root, "project")
	workflowDir := filepath.Join(repository, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTriggerFile(t, filepath.Join(workflowDir, "push.yml"), "name: Push CI\non: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: printf 'ok\\n'\n")
	writeTriggerFile(t, filepath.Join(workflowDir, "manual.yml"), "name: Manual CI\non: workflow_dispatch\njobs:\n  manual:\n    runs-on: ubuntu-latest\n    steps:\n      - run: printf 'manual\\n'\n")
	gitTrigger(t, repository, "init", "-b", "main")
	gitTrigger(t, repository, "config", "user.email", "git-ci@example.invalid")
	gitTrigger(t, repository, "config", "user.name", "git-ci trigger tests")
	gitTrigger(t, repository, "add", "-A")
	gitTrigger(t, repository, "commit", "-m", "initial")

	database, err := store.Open(ctx, filepath.Join(root, "gci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	project, err := database.CreateProject(ctx, store.CreateProjectParams{
		Slug: "project", Name: "Project", SourceType: "local", CanonicalPath: &repository,
		DefaultBranch: "main", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := execution.NewManager(database, execution.WithWorkspaceRoot(filepath.Join(root, "workspaces")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.SyncProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, executor)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := manager.Configure(ctx, project.ID, "main", true)
	if err != nil || policy.LastCommitSHA == nil {
		t.Fatalf("configure policy=%#v err=%v", policy, err)
	}
	if err := manager.Process(ctx); err != nil {
		t.Fatal(err)
	}
	if runs, _ := database.ListRuns(ctx, project.ID); len(runs) != 0 {
		t.Fatalf("baseline created %d runs", len(runs))
	}

	gitTrigger(t, repository, "commit", "--allow-empty", "-m", "new commit")
	sha := gitTrigger(t, repository, "rev-parse", "HEAD")
	if err := manager.Process(ctx); err != nil {
		t.Fatal(err)
	}
	runs, err := database.ListRuns(ctx, project.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%d err=%v", len(runs), err)
	}
	if runs[0].TriggerType != "commit" || runs[0].CommitSHA == nil || *runs[0].CommitSHA != sha {
		t.Fatalf("run=%#v want commit %s", runs[0], sha)
	}
	if err := manager.Process(ctx); err != nil {
		t.Fatal(err)
	}
	if runs, _ := database.ListRuns(ctx, project.ID); len(runs) != 1 {
		t.Fatalf("duplicate poll created %d runs", len(runs))
	}
	if _, err := manager.Configure(ctx, project.ID, "main", false); err != nil {
		t.Fatal(err)
	}
	gitTrigger(t, repository, "commit", "--allow-empty", "-m", "disabled commit")
	if err := manager.Process(ctx); err != nil {
		t.Fatal(err)
	}
	if runs, _ := database.ListRuns(ctx, project.ID); len(runs) != 1 {
		t.Fatalf("disabled watcher created %d runs", len(runs))
	}
}

func writeTriggerFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitTrigger(t *testing.T, path string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}
