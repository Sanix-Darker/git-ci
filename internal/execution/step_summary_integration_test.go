package execution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestE2EGitHubStepSummariesPersistAfterSuccessAndAllowedFailure(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "summaries.yml"), strings.Join([]string{
		"name: Step summaries", "on: workflow_dispatch", "jobs:", "  verify:", "    runs-on: linux", "    steps:",
		"      - name: Test summary", "        run: |", "          printf '# Test summary\\n\\n- tests: 42\\n<script>blocked</script>\\n' >> \"$GITHUB_STEP_SUMMARY\"",
		"      - name: Allowed failure summary", "        continue-on-error: true", "        run: |", "          printf '## Failure detail\\n\\nexit: 7\\n' >> \"$GITHUB_STEP_SUMMARY\"", "          exit 7",
		"      - name: Invalid summary", "        run: |", "          rm -f \"$GITHUB_STEP_SUMMARY\"", "          ln -s /dev/null \"$GITHUB_STEP_SUMMARY\"",
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	steps := graph.Jobs[0].Steps
	if len(steps) != 3 {
		t.Fatalf("step count = %d, want 3", len(steps))
	}
	if !strings.Contains(steps[0].Summary, "# Test summary") || !strings.Contains(steps[0].Summary, "<script>blocked</script>") {
		t.Fatalf("test summary = %q", steps[0].Summary)
	}
	if !strings.Contains(steps[1].Summary, "## Failure detail") {
		t.Fatalf("allowed failure summary = %q", steps[1].Summary)
	}
	if steps[2].Summary != "" {
		t.Fatalf("invalid summary persisted = %q", steps[2].Summary)
	}
	lines, err := database.ListLogLines(ctx, steps[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded := ""
	for _, line := range lines {
		encoded += line.Message
	}
	if !strings.Contains(encoded, "step summary ignored") {
		t.Fatalf("invalid summary warning missing from logs: %q", encoded)
	}
}
