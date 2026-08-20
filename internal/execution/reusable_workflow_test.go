package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestDiscoverExpandsContainedReusableWorkflowDAG(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, filepath.Join(".github", "workflows", "caller.yml"), `name: Caller
on: workflow_dispatch
jobs:
  prepare:
    runs-on: ubuntu-latest
    steps: [{run: "echo prepare"}]
  shared:
    name: Shared gate
    needs: prepare
    uses: ./.github/workflows/shared.yml
    with:
      target: production
    secrets:
      token: ${{ secrets.DEPLOY_TOKEN }}
  publish:
    needs: shared
    runs-on: ubuntu-latest
    steps: [{run: "echo publish"}]
`)
	writeWorkflowFixture(t, root, filepath.Join(".github", "workflows", "shared.yml"), `name: Shared
on:
  workflow_call:
    inputs:
      target: {required: true, type: string}
    secrets:
      token: {required: true}
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: "echo ${{ inputs.target }} ${{ secrets.token }}"
  verify:
    needs: build
    runs-on: ubuntu-latest
    steps: [{run: "echo verify"}]
`)
	definitions, err := Discover([]store.Project{{ID: "project", Slug: "project", CanonicalPath: &root}})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var caller Definition
	for _, definition := range definitions {
		if definition.Name == "Caller" {
			caller = definition
		}
	}
	if len(caller.Jobs) != 4 {
		t.Fatalf("expanded jobs = %#v", caller.Jobs)
	}
	jobs := make(map[string]JobDefinition)
	for _, job := range caller.Jobs {
		jobs[job.Key] = job
	}
	if strings.Join(jobs["shared/build"].Needs, ",") != "prepare" {
		t.Fatalf("shared/build needs = %#v", jobs["shared/build"].Needs)
	}
	if strings.Join(jobs["shared/verify"].Needs, ",") != "shared/build" {
		t.Fatalf("shared/verify needs = %#v", jobs["shared/verify"].Needs)
	}
	if strings.Join(jobs["publish"].Needs, ",") != "shared/verify" {
		t.Fatalf("publish needs = %#v", jobs["publish"].Needs)
	}
	build := jobs["shared/build"]
	if build.WorkflowCall == nil || build.WorkflowCall.Uses != "./.github/workflows/shared.yml" {
		t.Fatalf("workflow call = %#v", build.WorkflowCall)
	}
	if command := build.Steps[0].Command; command != "echo production ${{ secrets.DEPLOY_TOKEN }}" {
		t.Fatalf("resolved command = %q", command)
	}
}

func TestDiscoverRejectsReusableWorkflowCycleAndMissingInput(t *testing.T) {
	for _, scenario := range []struct {
		name, caller, shared, want string
	}{
		{name: "cycle", caller: `name: Caller
on: workflow_dispatch
jobs:
  call:
    uses: ./.github/workflows/shared.yml
`, shared: `name: Shared
on: workflow_call
jobs:
  loop:
    uses: ./.github/workflows/caller.yml
`, want: "cycle"},
		{name: "required input", caller: `name: Caller
on: workflow_dispatch
jobs:
  call:
    uses: ./.github/workflows/shared.yml
`, shared: `name: Shared
on:
  workflow_call:
    inputs:
      target: {required: true, type: string}
jobs:
  test:
    runs-on: ubuntu-latest
    steps: [{run: "echo ${{ inputs.target }}"}]
`, want: "required input"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			writeWorkflowFixture(t, root, filepath.Join(".github", "workflows", "caller.yml"), scenario.caller)
			writeWorkflowFixture(t, root, filepath.Join(".github", "workflows", "shared.yml"), scenario.shared)
			_, err := Discover([]store.Project{{ID: "project", Slug: "project", CanonicalPath: &root}})
			if err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("error = %v, want %q", err, scenario.want)
			}
		})
	}
}

func TestReusableWorkflowRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "shared.yml")
	if err := os.WriteFile(outside, []byte("name: outside\non: workflow_call\njobs: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeWorkflowFixture(t, root, filepath.Join(".github", "workflows", "caller.yml"), `name: Caller
on: workflow_dispatch
jobs:
  call:
    uses: ./.github/workflows/shared.yml
`)
	workflowDir := filepath.Join(root, ".github", "workflows")
	if err := os.Symlink(outside, filepath.Join(workflowDir, "shared.yml")); err != nil {
		t.Fatal(err)
	}
	_, err := Discover([]store.Project{{ID: "project", Slug: "project", CanonicalPath: &root}})
	if err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("error = %v", err)
	}
}
