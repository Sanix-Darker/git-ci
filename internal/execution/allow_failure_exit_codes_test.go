package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerAllowsOnlyMatchingGitLabFailureExitCodes(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `allowed:
  allow_failure:
    exit_codes: 137
  script: ["exit 137"]
after-allowed:
  needs: [allowed]
  script: ["printf after-allowed"]
fatal:
  allow_failure:
    exit_codes: [137, 255]
  script: ["exit 1"]
after-fatal:
  needs: [fatal]
  script: ["printf should-not-run"]
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Run.Status != store.StatusFailed || jobStatus(t, graph, "allowed") != store.StatusFailed || jobStatus(t, graph, "after-allowed") != store.StatusSucceeded || jobStatus(t, graph, "fatal") != store.StatusFailed || jobStatus(t, graph, "after-fatal") != store.StatusSkipped {
		t.Fatalf("allow failure graph = %#v", graph)
	}
	attempt := jobGraph(t, graph, "allowed").Job.Attempts
	if len(attempt) != 1 || attempt[0].ExitCode == nil || *attempt[0].ExitCode != 137 {
		t.Fatalf("allowed attempt = %#v", attempt)
	}
}

func TestManagerUsesFinalRetryExitCodeForAllowedFailure(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "retried")
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `final-fatal:
  retry: 1
  allow_failure:
    exit_codes: 137
  script:
    - if test -f `+shellTestPath(marker)+`; then exit 1; else touch `+shellTestPath(marker)+`; exit 137; fi
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts := jobGraph(t, graph, "final-fatal").Job.Attempts
	if graph.Run.Status != store.StatusFailed || len(attempts) != 2 || attempts[0].ExitCode == nil || *attempts[0].ExitCode != 137 || attempts[1].ExitCode == nil || *attempts[1].ExitCode != 1 {
		t.Fatalf("final retry graph = %#v", graph)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("retry marker: %v", err)
	}
}

func TestPersistedJobFailureAllowanceUsesFinalAttempt(t *testing.T) {
	semantics, _ := json.Marshal(map[string]interface{}{"allowFailureExitCodes": []int{137}})
	environment, _ := json.Marshal(map[string]string{"GCI_JOB_SEMANTICS_JSON": string(semantics)})
	allowed, fatal := 137, 1
	job := store.Job{Status: store.StatusFailed, Environment: environment, Attempts: []store.JobAttempt{{Status: store.StatusFailed, ExitCode: &fatal}, {Status: store.StatusFailed, ExitCode: &allowed}}}
	if !persistedJobFailureAllowed(job) {
		t.Fatal("matching final persisted attempt was not allowed")
	}
	job.Attempts = append(job.Attempts, store.JobAttempt{Status: store.StatusFailed, ExitCode: &fatal})
	if persistedJobFailureAllowed(job) {
		t.Fatal("non-matching final persisted attempt was allowed")
	}
}
