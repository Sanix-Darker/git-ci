package execution

import (
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerGitLabRegexRulesSelectFirstMatchingJobRule(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `
variables:
  BRANCH_PATTERN: '/^main$/'
regex-match:
  rules:
    - if: '$CI_COMMIT_BRANCH =~ $BRANCH_PATTERN'
  script: ["printf matched"]
regex-skip:
  rules:
    - if: '$CI_COMMIT_BRANCH !~ /^main$/'
  script: ["printf should-not-run"]
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Run.Status != store.StatusSucceeded || jobGraph(t, graph, "regex-match").Job.Status != store.StatusSucceeded || jobGraph(t, graph, "regex-skip").Job.Status != store.StatusSkipped {
		t.Fatalf("regex rule graph = %#v", graph)
	}
}
