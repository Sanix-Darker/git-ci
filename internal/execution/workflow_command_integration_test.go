package execution

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestE2EWorkflowCommandsMaskAcrossStepsAndPersistAnnotations(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "commands.yml"), strings.Join([]string{
		"name: Commands", "on: workflow_dispatch", "jobs:", "  first:", "    runs-on: linux", "    steps:",
		"      - name: Register", "        run: |", "          value='runtime-mask-value'", "          printf '::add-mask::%s\\n' \"$value\"", "          printf 'first=%s\\n' \"$value\"",
		"      - name: Diagnose", "        run: |", "          printf 'summary=%s\\n' 'runtime-mask-value' >> \"$GITHUB_STEP_SUMMARY\"", "          printf '::notice file=src/app.go,line=12,col=4,title=Compile hint::masked runtime-mask-value\\n'", "          printf '::stop-commands::pause-token-123\\n'", "          printf '::warning::ignored warning\\n'", "          printf '::pause-token-123::\\n'", "          printf '::warning file=src/app.go,line=13::real warning\\n'", "          printf '::error file=src/app.go,line=14::diagnostic error\\n'",
		"  second:", "    runs-on: linux", "    needs: first", "    steps:", "      - name: Isolated", "        run: printf 'isolated=%s\\n' 'runtime-mask-value'",
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	if len(graph.Jobs) != 2 || len(graph.Jobs[0].Steps) != 2 || len(graph.Jobs[1].Steps) != 1 {
		t.Fatalf("unexpected graph = %#v", graph)
	}
	diagnose := graph.Jobs[0].Steps[1]
	if diagnose.Summary != "summary=***\n" || len(diagnose.Annotations) != 3 {
		t.Fatalf("diagnose step = %#v", diagnose)
	}
	if diagnose.Annotations[0].Message != "masked ***" || diagnose.Annotations[1].Message != "real warning" || diagnose.Annotations[2].Level != store.AnnotationError {
		t.Fatalf("annotations = %#v", diagnose.Annotations)
	}
	for _, step := range graph.Jobs[0].Steps {
		lines, err := database.ListLogLines(ctx, step.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			if strings.Contains(line.Message, "runtime-mask-value") {
				t.Fatalf("unsafe first-job log = %#v", line)
			}
		}
	}
	isolatedLogs, err := database.ListLogLines(ctx, graph.Jobs[1].Steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range isolatedLogs {
		found = found || strings.Contains(line.Message, "runtime-mask-value")
	}
	if !found {
		t.Fatalf("job-local mask leaked into second job: %#v", isolatedLogs)
	}
}
