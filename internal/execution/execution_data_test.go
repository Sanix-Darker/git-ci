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
