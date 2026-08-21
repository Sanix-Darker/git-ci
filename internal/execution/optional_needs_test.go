package execution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestDiscoverExpandsAndPrunesOptionalNeeds(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, ".gitlab-ci.yml", `matrix-source:
  parallel:
    matrix:
      - TARGET: [api, worker]
  script: ["printf source"]
consumer:
  needs:
    - job: matrix-source
      optional: true
  script: ["printf consumer"]
standalone:
  needs:
    - job: absent
      optional: true
  script: ["printf standalone"]
`)
	definitions, err := Discover([]store.Project{{ID: "project", Slug: "project", CanonicalPath: &root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d", len(definitions))
	}
	jobs := make(map[string]JobDefinition)
	for _, job := range definitions[0].Jobs {
		jobs[job.Key] = job
	}
	consumer := jobs["consumer"]
	if len(consumer.Needs) != 2 {
		t.Fatalf("consumer needs = %#v", consumer.Needs)
	}
	for _, need := range consumer.Needs {
		if !strings.HasPrefix(need, "matrix-source[") || !consumer.NeedsOptional[need] {
			t.Errorf("expanded optional need %q, metadata %#v", need, consumer.NeedsOptional)
		}
	}
	if standalone := jobs["standalone"]; len(standalone.Needs) != 0 || len(standalone.NeedsOptional) != 0 {
		t.Fatalf("standalone optional needs = %#v / %#v", standalone.Needs, standalone.NeedsOptional)
	}
}

func TestManagerAdmitsSkippedOptionalNeedOnly(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `skipped-optional:
  rules:
    - if: '$CI_COMMIT_BRANCH == "feature"'
  script: ["printf skipped"]
skipped-required:
  rules:
    - if: '$CI_COMMIT_BRANCH == "feature"'
  script: ["printf skipped"]
optional-consumer:
  needs:
    - job: skipped-optional
      optional: true
  script: ["printf optional-consumer"]
required-consumer:
  needs: [skipped-required]
  script: ["printf required-consumer"]
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Run.Status != store.StatusSucceeded || jobStatus(t, graph, "skipped-optional") != store.StatusSkipped || jobStatus(t, graph, "skipped-required") != store.StatusSkipped || jobStatus(t, graph, "optional-consumer") != store.StatusSucceeded || jobStatus(t, graph, "required-consumer") != store.StatusSkipped {
		t.Fatalf("optional needs graph = %#v", graph)
	}
}
