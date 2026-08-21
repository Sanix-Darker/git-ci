package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerExecutesArtifactCacheAndJUnitContracts(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), strings.Join([]string{
		"stages: [test]",
		"build:",
		"  stage: test",
		"  cache:",
		"    key: dependency-v1",
		"    paths: [.cache/]",
		"  script:",
		"    - mkdir -p dist .cache",
		"    - printf 'binary' > dist/app.txt",
		"    - printf 'cached' > .cache/dependency",
		"    - printf '<testsuite tests=\"4\" failures=\"1\" errors=\"0\" skipped=\"1\" time=\"0.25\"></testsuite>' > dist/junit.xml",
		"  artifacts:",
		"    name: build-output",
		"    paths: [dist/]",
		"    expire_in: 2 days",
		"    reports:",
		"      junit: dist/junit.xml",
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph=%#v err=%v", graph, err)
	}
	artifacts, err := database.ListRunArtifacts(ctx, run.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Name != "build-output" || artifacts[0].ExpiresAt == nil {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	reports, err := database.ListRunTestReports(ctx, run.ID)
	if err != nil || len(reports) != 1 || reports[0].Tests != 4 || reports[0].Failures != 1 || reports[0].Skipped != 1 {
		t.Fatalf("reports=%#v err=%v", reports, err)
	}
	caches, err := database.ListProjectCaches(ctx, project.ID)
	if err != nil || len(caches) != 1 || caches[0].Key != "dependency-v1" {
		t.Fatalf("caches=%#v err=%v", caches, err)
	}
	artifact, body, err := manager.OpenRunArtifact(ctx, run.ID, artifacts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = body.Close()
	if artifact.SHA256 != artifacts[0].SHA256 {
		t.Fatalf("opened artifact = %#v", artifact)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "app.txt")); !os.IsNotExist(err) {
		t.Fatalf("execution leaked generated output into registered checkout: %v", err)
	}
}

func TestManagerExecutesGitHubCacheRestoreAndSaveSplitActions(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	restored := filepath.Join(t.TempDir(), "restored")
	writeManagerWorkflow(t, filepath.Join(root, ".github/workflows/cache-split.yml"), strings.Join([]string{
		"name: Cache Split",
		"on: workflow_dispatch",
		"jobs:",
		"  seed:",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: mkdir -p deps && printf seed > deps/value.txt",
		"      - id: save",
		"        uses: actions/cache/save@v4",
		"        with:",
		"          path: deps",
		"          key: split-v1",
		"  restore:",
		"    needs: seed",
		"    runs-on: ubuntu-latest",
		"    steps:",
		"      - run: rm -rf deps",
		"      - id: restore-cache",
		"        uses: actions/cache/restore@v4",
		"        with:",
		"          path: deps",
		"          key: split-v1",
		"      - run: test \"${{ steps.restore-cache.outputs.cache-hit }}\" = true",
		"      - run: test \"${{ steps.restore-cache.outputs.cache-primary-key }}\" = split-v1",
		"      - run: test \"${{ steps.restore-cache.outputs.cache-matched-key }}\" = split-v1",
		"      - if: steps.restore-cache.outputs.cache-hit != 'true'",
		"        run: exit 99",
		"      - run: printf '%s' \"$(cat deps/value.txt)\" > " + shellTestPath(restored),
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph=%#v err=%v", graph, err)
	}
	assertOutputFile(t, restored, "seed")
	caches, err := database.ListProjectCaches(ctx, project.ID)
	if err != nil || len(caches) != 1 || caches[0].Key != "split-v1" {
		t.Fatalf("caches=%#v err=%v", caches, err)
	}
	for _, item := range graph.Jobs {
		if pointerValue(item.Job.Key) != "restore" {
			continue
		}
		for _, step := range item.Steps {
			if pointerValue(step.Command) == "exit 99" && step.Status != store.StatusSkipped {
				t.Fatalf("cache-hit guarded step status = %s, want skipped", step.Status)
			}
		}
	}
}
