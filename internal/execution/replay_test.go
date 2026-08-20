package execution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerReplaysJobClosureAndSingleStepInCleanPinnedRuns(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "replay-marker")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "replay.yml"), strings.Join([]string{
		"name: Replay pipeline", "on: workflow_dispatch", "jobs:",
		"  prepare:", "    runs-on: ubuntu-latest", "    steps:", "      - name: Prepare", "        run: printf P >> " + shellTestPath(marker),
		"  test:", "    needs: prepare", "    runs-on: ubuntu-latest", "    steps:", "      - name: Test", "        run: printf T >> " + shellTestPath(marker),
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	source := enqueueAndProcess(t, ctx, manager, workflow.ID)
	sourceGraph, err := database.GetRunGraph(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	testJob := jobGraph(t, sourceGraph, "test")
	jobReplay, err := manager.EnqueueRunReplay(ctx, store.EnqueueReplayParams{Kind: store.RunLineageJobReplay, SourceJobID: testJob.Job.ID, Actor: "operator", IdempotencyKey: "manager-job"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process job replay = %t, %v", processed, err)
	}
	jobGraphReplay, _ := database.GetRunGraph(ctx, jobReplay.ID)
	if jobGraphReplay.Run.Status != store.StatusSucceeded || len(jobGraphReplay.Jobs) != 2 {
		t.Fatalf("job replay = %#v", jobGraphReplay)
	}

	stepReplay, err := manager.EnqueueRunReplay(ctx, store.EnqueueReplayParams{Kind: store.RunLineageStepReplay, SourceStepID: testJob.Steps[0].ID, Actor: "operator", IdempotencyKey: "manager-step"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := manager.ProcessNext(ctx); err != nil || !processed {
		t.Fatalf("process step replay = %t, %v", processed, err)
	}
	stepGraph, _ := database.GetRunGraph(ctx, stepReplay.ID)
	if stepGraph.Run.Status != store.StatusSucceeded || len(stepGraph.Jobs) != 1 || len(stepGraph.Jobs[0].Steps) != 1 {
		t.Fatalf("step replay = %#v", stepGraph)
	}
}
