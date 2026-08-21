package execution

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestE2EGitHubEnvironmentAndPathFilesStayWithinJob(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	consumerMarker := filepath.Join(t.TempDir(), "consumer")
	overrideMarker := filepath.Join(t.TempDir(), "override")
	isolationMarker := filepath.Join(t.TempDir(), "isolated")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "runtime-files.yml"), strings.Join([]string{
		"name: Runtime files", "on: workflow_dispatch", "jobs:", "  a_runtime:", "    runs-on: linux", "    steps:",
		"      - name: Export runtime state", "        run: |",
		"          mkdir -p .runtime-bin",
		"          printf '%s\\n' '#!/bin/sh' 'printf path-ready' > .runtime-bin/runtime-probe",
		"          chmod +x .runtime-bin/runtime-probe",
		"          printf 'DYNAMIC_STATE=runtime\\n' >> \"$GITHUB_ENV\"",
		"          printf 'MULTILINE<<END\\nline-one\\nline-two\\nEND\\n' >> \"$GITHUB_ENV\"",
		"          hidden=$(printf 'runtime-%s' 'hidden-value')",
		"          printf 'EPHEMERAL_SECRET=%s\\n' \"$hidden\" >> \"$GITHUB_ENV\"",
		"          printf '%s\\n' \"$GITHUB_WORKSPACE/.runtime-bin\" >> \"$GITHUB_PATH\"",
		"          test -z \"${DYNAMIC_STATE+x}\"",
		"          ! command -v runtime-probe",
		"      - name: Consume runtime state", "        run: |",
		"          printf '%s|%s|%s' \"$DYNAMIC_STATE\" \"$MULTILINE\" \"$(runtime-probe)\" > " + shellTestPath(consumerMarker),
		"          test \"$EPHEMERAL_SECRET\" = \"$(printf 'runtime-%s' 'hidden-value')\"",
		"      - name: Explicit step override", "        env:", "          DYNAMIC_STATE: explicit", "        run: printf '%s' \"$DYNAMIC_STATE\" > " + shellTestPath(overrideMarker),
		"      - name: Failed exporter", "        continue-on-error: true", "        run: printf 'AFTER_FAILURE=yes\\n' >> \"$GITHUB_ENV\"; exit 9",
		"      - name: Consume failed exporter", "        run: test \"$AFTER_FAILURE\" = yes",
		"  b_isolated:", "    needs: a_runtime", "    runs-on: linux", "    steps:",
		"      - name: Assert isolation", "        run: test -z \"${DYNAMIC_STATE+x}\"; ! command -v runtime-probe; printf isolated > " + shellTestPath(isolationMarker),
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	assertOutputFile(t, consumerMarker, "runtime|line-one\nline-two|path-ready")
	assertOutputFile(t, overrideMarker, "explicit")
	assertOutputFile(t, isolationMarker, "isolated")
	encoded, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "runtime-hidden-value") {
		t.Fatal("ephemeral environment value was persisted in the run graph")
	}
	for _, job := range graph.Jobs {
		for _, step := range job.Steps {
			lines, logErr := database.ListLogLines(ctx, step.ID)
			if logErr != nil {
				t.Fatal(logErr)
			}
			logJSON, _ := json.Marshal(lines)
			if strings.Contains(string(logJSON), "runtime-hidden-value") {
				t.Fatal("ephemeral environment value leaked to logs")
			}
		}
	}
}

func TestE2EGitHubRuntimeFilesPropagateAcrossNestedCompositeSteps(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "composite-runtime")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "actions", "runtime", "action.yml"), strings.Join([]string{
		"name: Runtime bridge", "runs:", "  using: composite", "  steps:",
		"    - name: Export", "      shell: sh", "      run: |",
		"        mkdir -p .composite-bin",
		"        printf '%s\\n' '#!/bin/sh' 'printf composite-path' > .composite-bin/composite-probe",
		"        chmod +x .composite-bin/composite-probe",
		"        printf 'COMPOSITE_ENV=available\\n' >> \"$GITHUB_ENV\"",
		"        printf '%s\\n' \"$GITHUB_WORKSPACE/.composite-bin\" >> \"$GITHUB_PATH\"",
		"    - name: Internal consume", "      shell: sh", "      run: test \"$COMPOSITE_ENV\" = available; test \"$(composite-probe)\" = composite-path",
	}, "\n"))
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "composite-runtime.yml"), strings.Join([]string{
		"name: Composite runtime", "on: workflow_dispatch", "jobs:", "  verify:", "    runs-on: linux", "    steps:",
		"      - uses: ./.github/actions/runtime",
		"      - run: printf '%s|%s' \"$COMPOSITE_ENV\" \"$(composite-probe)\" > " + shellTestPath(marker),
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	assertOutputFile(t, marker, "available|composite-path")
}
