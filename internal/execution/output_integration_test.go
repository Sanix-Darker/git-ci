package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestE2EGitHubStepAndJobOutputs(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	stepMarker := filepath.Join(t.TempDir(), "step-output")
	jobMarker := filepath.Join(t.TempDir(), "job-output")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "outputs.yml"), strings.Join([]string{
		"name: Output flow",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    runs-on: linux",
		"    outputs:",
		"      version: ${{ steps.meta.outputs.version }}",
		"    steps:",
		"      - id: meta",
		"        run: |",
		"          printf 'version=1.2.3\\n' >> \"$GITHUB_OUTPUT\"",
		"          printf 'notes<<END\\nline one\\nline two\\nEND\\n' >> \"$GITHUB_OUTPUT\"",
		"      - id: consume",
		"        if: ${{ steps.meta.outputs.version == '1.2.3' }}",
		"        env:",
		"          VERSION: ${{ steps.meta.outputs.version }}",
		"          NOTES: ${{ steps.meta.outputs.notes }}",
		"        run: printf '%s|%s' \"$VERSION\" \"$NOTES\" > " + shellTestPath(stepMarker),
		"  release:",
		"    needs: build",
		"    if: ${{ needs.build.outputs.version == '1.2.3' }}",
		"    runs-on: linux",
		"    steps:",
		"      - env:",
		"          VERSION: ${{ needs.build.outputs.version }}",
		"        run: printf '%s' \"$VERSION\" > " + shellTestPath(jobMarker),
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	assertOutputFile(t, stepMarker, "1.2.3|line one\nline two")
	assertOutputFile(t, jobMarker, "1.2.3")
}

func TestE2ENestedCompositeActionOutputs(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "nested-output")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "actions", "inner", "action.yml"), strings.Join([]string{
		"name: Inner",
		"outputs:",
		"  value:",
		"    description: generated value",
		"    value: ${{ steps.generate.outputs.value }}",
		"runs:",
		"  using: composite",
		"  steps:",
		"    - id: generate",
		"      shell: sh",
		"      run: printf 'value=contained\\n' >> \"$GITHUB_OUTPUT\"",
	}, "\n"))
	writeManagerWorkflow(t, filepath.Join(root, ".github", "actions", "outer", "action.yml"), strings.Join([]string{
		"name: Outer",
		"outputs:",
		"  result:",
		"    description: forwarded value",
		"    value: ${{ steps.inner.outputs.value }}",
		"runs:",
		"  using: composite",
		"  steps:",
		"    - id: inner",
		"      uses: ./.github/actions/inner",
	}, "\n"))
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "composite-outputs.yml"), strings.Join([]string{
		"name: Composite output flow",
		"on: workflow_dispatch",
		"jobs:",
		"  verify:",
		"    runs-on: linux",
		"    steps:",
		"      - id: outer",
		"        uses: ./.github/actions/outer",
		"      - if: ${{ steps.outer.outputs.result == 'contained' }}",
		"        run: printf '%s' '${{ steps.outer.outputs.result }}' > " + shellTestPath(marker),
	}, "\n"))

	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	assertOutputFile(t, marker, "contained")
}

func assertOutputFile(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != want {
		t.Fatalf("output %q = %q, error = %v; want %q", path, contents, err, want)
	}
}
