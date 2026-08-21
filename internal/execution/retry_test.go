package execution

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestManagerGitLabRetrySucceedsWithinSameJob(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "attempted")
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), `
retry-once:
  retry:
    max: 2
    when: script_failure
    exit_codes: [17]
  script:
    - if test -f `+shellTestPath(marker)+`; then printf recovered; else touch `+shellTestPath(marker)+`; exit 17; fi
`)
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	job := jobGraph(t, graph, "retry-once").Job
	if graph.Run.Status != store.StatusSucceeded || job.Status != store.StatusSucceeded {
		t.Fatalf("retry graph = %#v", graph)
	}
	if len(job.Attempts) != 2 || job.Attempts[0].Status != store.StatusFailed || !job.Attempts[0].WillRetry || job.Attempts[0].ExitCode == nil || *job.Attempts[0].ExitCode != 17 || job.Attempts[1].Status != store.StatusSucceeded {
		t.Fatalf("retry attempts = %#v", job.Attempts)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("retry marker: %v", err)
	}
}

func TestRetryPolicySelectorAndLimitSemantics(t *testing.T) {
	code := 17
	outcome := jobAttemptOutcome{FailureKind: "script_failure", ExitCode: &code}
	if !retryPolicyMatches(&types.RetryPolicy{MaxAttempts: 2, When: []string{"runner_system_failure"}, ExitCodes: []int{17}}, 1, store.StatusFailed, outcome, false) {
		t.Fatal("when and exit_codes must combine with OR")
	}
	if retryPolicyMatches(&types.RetryPolicy{MaxAttempts: 1, When: []string{"script_failure"}}, 2, store.StatusFailed, outcome, false) {
		t.Fatal("attempt beyond retry maximum was accepted")
	}
	if retryPolicyMatches(&types.RetryPolicy{MaxAttempts: 2, When: []string{"always"}}, 1, store.StatusFailed, outcome, true) {
		t.Fatal("cancelled execution was retried")
	}
}
