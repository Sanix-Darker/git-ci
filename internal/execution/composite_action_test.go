package execution

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/internal/runners"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestLocalCompositeActionsExpandNestedInputsEnvironmentAndExecute(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, ".github/actions/write/action.yaml", `
name: Write target
inputs:
  target:
    required: true
runs:
  using: composite
  steps:
    - name: Persist
      shell: bash
      run: printf '%s' '${{ inputs.target }}' > composite-target.txt
`)
	writeWorkflowFixture(t, root, ".github/actions/check/action.yml", `
name: Check target
inputs:
  target:
    required: true
  mode:
    default: strict
runs:
  using: composite
  steps:
    - name: Write target
      uses: ./.github/actions/write
      with:
        target: ${{ inputs.target }}
    - name: Verify ${{ inputs.mode }}
      shell: bash
      env:
        EXPECTED: ${{ inputs.target }}
      run: test "$(cat composite-target.txt)" = "$EXPECTED"
`)
	writeWorkflowFixture(t, root, ".github/workflows/composite.yml", `
name: Composite Delivery
on: workflow_dispatch
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - name: Local check
        uses: ./.github/actions/check
        env:
          SCOPE: caller
        with:
          target: service
      - name: Finish
        run: test -f composite-target.txt
`)

	definitions, err := Discover([]store.Project{fixtureProject(t, root, "project-composite")})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || len(definitions[0].Jobs) != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	steps := definitions[0].Jobs[0].Steps
	if len(steps) != 3 {
		t.Fatalf("expanded steps = %#v, want 3", steps)
	}
	if steps[0].Name != "Local check / Write target / Persist" || steps[1].Name != "Local check / Verify strict" || steps[2].Name != "Finish" {
		t.Fatalf("expanded provenance = %#v", steps)
	}
	if !strings.Contains(steps[0].Command, "'service'") || steps[1].Environment["EXPECTED"] != "service" || steps[1].Environment["SCOPE"] != "caller" {
		t.Fatalf("resolved composite semantics = %#v", steps)
	}
	if !strings.HasSuffix(steps[0].Environment["GITHUB_ACTION_PATH"], filepath.FromSlash(".github/actions/write")) {
		t.Fatalf("nested GITHUB_ACTION_PATH = %q", steps[0].Environment["GITHUB_ACTION_PATH"])
	}

	pipeline := &types.Pipeline{Jobs: map[string]*types.Job{"verify": {
		Name:  "verify",
		Steps: []types.Step{{Name: "Local check", Uses: "./.github/actions/check", With: map[string]string{"target": "service"}}},
	}}}
	if err := expandLocalCompositeActions(root, pipeline); err != nil {
		t.Fatal(err)
	}
	runner := runners.NewBashRunner(&config.RunnerConfig{WorkDir: root})
	defer runner.Cleanup()
	if err := runner.RunJob(pipeline.Jobs["verify"], root); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "composite-target.txt"))
	if err != nil || string(content) != "service" {
		t.Fatalf("composite execution content = %q, err = %v", content, err)
	}
}

func TestLocalCompositeActionsRejectRequiredInputCycleTraversalOutputsAndSymlink(t *testing.T) {
	t.Run("required input", func(t *testing.T) {
		root := t.TempDir()
		writeWorkflowFixture(t, root, "action/action.yml", `
name: Required
inputs:
  target:
    required: true
runs:
  using: composite
  steps:
    - shell: bash
      run: echo '${{ inputs.target }}'
`)
		if err := expandLocalCompositeActions(root, compositeFixturePipeline("./action")); err == nil || !strings.Contains(err.Error(), `requires input "target"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("cycle", func(t *testing.T) {
		root := t.TempDir()
		writeWorkflowFixture(t, root, "a/action.yml", "name: A\nruns:\n  using: composite\n  steps:\n    - uses: ./b")
		writeWorkflowFixture(t, root, "b/action.yml", "name: B\nruns:\n  using: composite\n  steps:\n    - uses: ./a")
		if err := expandLocalCompositeActions(root, compositeFixturePipeline("./a")); err == nil || !strings.Contains(err.Error(), "local action cycle: a -> b -> a") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("traversal", func(t *testing.T) {
		if err := expandLocalCompositeActions(t.TempDir(), compositeFixturePipeline("./../outside")); err == nil || !strings.Contains(err.Error(), "unsafe local action reference") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("outputs", func(t *testing.T) {
		root := t.TempDir()
		writeWorkflowFixture(t, root, "action/action.yml", `
name: Output
outputs:
  value:
    value: static
runs:
  using: composite
  steps:
    - shell: bash
      run: echo ok
`)
		if err := expandLocalCompositeActions(root, compositeFixturePipeline("./action")); err == nil || !strings.Contains(err.Error(), "outputs are not supported yet") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink permissions differ on Windows")
		}
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "action.yml")
		if err := os.WriteFile(outside, []byte("name: Outside\nruns:\n  using: composite\n  steps:\n    - shell: bash\n      run: echo no\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "action"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "action", "action.yml")); err != nil {
			t.Fatal(err)
		}
		if err := expandLocalCompositeActions(root, compositeFixturePipeline("./action")); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
			t.Fatalf("error = %v", err)
		}
	})
}

func compositeFixturePipeline(ref string) *types.Pipeline {
	return &types.Pipeline{Jobs: map[string]*types.Job{"job": {Steps: []types.Step{{Name: "Local", Uses: ref}}}}}
}
