package execution

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestDiscoverExpandsGitHubMatrixAndFansOutDependencies(t *testing.T) {
	projectPath := t.TempDir()
	writeWorkflowFixture(t, projectPath, ".github/workflows/matrix.yml", strings.Join([]string{
		"name: Matrix pipeline",
		"on: workflow_dispatch",
		"jobs:",
		"  build:",
		"    if: ${{ matrix.os != 'blocked' }}",
		"    strategy:",
		"      fail-fast: true",
		"      max-parallel: 2",
		"      matrix:",
		"        os: [linux, windows]",
		"        version: ['1.24', '1.25']",
		"    runs-on: ${{ matrix.os }}",
		"    steps:",
		"      - if: ${{ matrix.version == '1.25' }}",
		"        run: echo ${{ matrix.os }}-${{ matrix.version }}",
		"  publish:",
		"    needs: build",
		"    runs-on: linux",
		"    steps:",
		"      - run: echo publish",
	}, "\n"))

	definitions, err := Discover([]store.Project{fixtureProject(t, projectPath, "project-matrix")})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("definitions = %d, want 1", len(definitions))
	}
	definition := definitions[0]
	var matrixJobs []JobDefinition
	var publish JobDefinition
	for _, job := range definition.Jobs {
		if job.SourceKey == "build" {
			matrixJobs = append(matrixJobs, job)
		}
		if job.SourceKey == "publish" {
			publish = job
		}
	}
	if len(matrixJobs) != 4 {
		t.Fatalf("matrix jobs = %d, want 4: %#v", len(matrixJobs), matrixJobs)
	}
	keys := make([]string, 0, len(matrixJobs))
	for _, job := range matrixJobs {
		keys = append(keys, job.Key)
		if job.MatrixTotal != 4 || job.MatrixIndex < 1 || job.MatrixLabel == "" {
			t.Errorf("matrix metadata = %#v, want indexed four-variant contract", job)
		}
		if job.FailFast != true || job.MaxParallel != 2 {
			t.Errorf("strategy = (%t, %d), want (true, 2)", job.FailFast, job.MaxParallel)
		}
		if strings.Contains(job.RunnerHint, "matrix.") || strings.Contains(job.Steps[0].Command, "matrix.") {
			t.Errorf("matrix template was not resolved: %#v", job)
		}
		if job.Condition.Expression == "" || !job.Condition.Evaluable || job.Steps[0].Condition.Expression == "" {
			t.Errorf("condition contracts were not frozen: %#v", job)
		}
		if !strings.Contains(job.Environment["GCI_JOB_SEMANTICS_JSON"], `"provider":"github"`) {
			t.Errorf("job semantics = %q, want provider contract", job.Environment["GCI_JOB_SEMANTICS_JSON"])
		}
	}
	if !reflect.DeepEqual(publish.Needs, keys) {
		t.Errorf("publish needs = %#v, want every matrix key %#v", publish.Needs, keys)
	}
}

func TestDiscoverRejectsMatrixBeyondBoundedExecutionLimit(t *testing.T) {
	projectPath := t.TempDir()
	writeWorkflowFixture(t, projectPath, ".github/workflows/too-large.yml", strings.Join([]string{
		"name: Too large",
		"on: push",
		"jobs:",
		"  build:",
		"    strategy:",
		"      matrix:",
		"        a: [1, 2, 3, 4, 5]",
		"        b: [1, 2, 3, 4, 5]",
		"        c: [1, 2, 3, 4, 5]",
		"    runs-on: linux",
		"    steps:",
		"      - run: echo bounded",
	}, "\n"))

	_, err := Discover([]store.Project{fixtureProject(t, projectPath, "project-large-matrix")})
	if err == nil || !strings.Contains(err.Error(), "exceeds limit 64") {
		t.Fatalf("Discover() error = %v, want bounded matrix rejection", err)
	}
}
