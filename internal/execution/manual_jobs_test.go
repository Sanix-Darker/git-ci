package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerBlockingManualJobResumesSameRun(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "released")
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `
stages: [build, deploy, verify]
prepare:
  stage: build
  script: ["printf prepared"]
release:
  stage: deploy
  needs: [prepare]
  when: manual
  allow_failure: false
  manual_confirmation: Release production?
  script: ["printf '%s' \"$TARGET\" > `+shellTestPath(marker)+`"]
verify:
  stage: verify
  needs: [release]
  script: ["test -f `+shellTestPath(marker)+`"]
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, _ := database.GetRunGraph(ctx, run.ID)
	if graph.Run.Status != store.StatusWaiting || jobGraph(t, graph, "release").Job.Status != store.StatusManual || jobGraph(t, graph, "verify").Job.Status != store.StatusQueued {
		t.Fatalf("blocked manual graph = %#v", graph)
	}
	release := jobGraph(t, graph, "release").Job
	result, err := manager.PlayManualJob(ctx, store.PlayManualJobParams{RunID: run.ID, JobID: release.ID, Actor: "operator", IdempotencyKey: "release-production", Confirmed: true, Variables: map[string]string{"TARGET": "production"}})
	if err != nil || result.Run.ID != run.ID {
		t.Fatalf("play manual job = %#v, %v", result, err)
	}
	processed, err := manager.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("resume manual run = %t, %v", processed, err)
	}
	graph, _ = database.GetRunGraph(ctx, run.ID)
	if graph.Run.Status != store.StatusSucceeded || jobGraph(t, graph, "release").Job.Status != store.StatusSucceeded || jobGraph(t, graph, "verify").Job.Status != store.StatusSucceeded {
		t.Fatalf("resumed manual graph = %#v", graph)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "production" {
		t.Fatalf("manual variable marker = %q, %v", contents, err)
	}
}

func TestManagerOptionalManualJobDoesNotBlockDependencies(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "optional")
	dependent := filepath.Join(t.TempDir(), "dependent")
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `
stages: [build, verify]
optional:
  stage: build
  when: manual
  script: ["printf optional > `+shellTestPath(marker)+`"]
consumer:
  stage: verify
  needs: [optional]
  script: ["printf continued > `+shellTestPath(dependent)+`"]
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, _ := database.GetRunGraph(ctx, run.ID)
	optional := jobGraph(t, graph, "optional").Job
	if graph.Run.Status != store.StatusSucceeded || optional.Status != store.StatusManual || jobGraph(t, graph, "consumer").Job.Status != store.StatusSucceeded {
		t.Fatalf("optional manual graph = %#v", graph)
	}
	if _, err := os.Stat(dependent); err != nil {
		t.Fatalf("dependent did not continue: %v", err)
	}
	if _, err := manager.PlayManualJob(ctx, store.PlayManualJobParams{RunID: run.ID, JobID: optional.ID, Actor: "operator", IdempotencyKey: "optional"}); err != nil {
		t.Fatal(err)
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process optional manual = %t, %v", processed, err)
	}
	graph, _ = database.GetRunGraph(ctx, run.ID)
	if graph.Run.Status != store.StatusSucceeded || jobGraph(t, graph, "optional").Job.Status != store.StatusSucceeded {
		t.Fatalf("played optional graph = %#v", graph)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("optional job did not run: %v", err)
	}
}
