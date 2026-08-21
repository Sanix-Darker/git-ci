package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverFreezesNestedLocalChildPipelines(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, ".gitlab-ci.yml", `variables:
  ROOT: inherited
bridge:
  variables:
    TARGET: service
  trigger:
    include:
      local: .gci/child.yml
    strategy: mirror`)
	writeWorkflowFixture(t, root, ".gci/child.yml", `verify:
  script: ["printf child"]
grandchild:
  needs: [verify]
  trigger:
    include: .gci/grandchild.yml
    strategy: depend`)
	writeWorkflowFixture(t, root, ".gci/grandchild.yml", `audit:
  script: ["printf grandchild"]`)
	definitions, err := DiscoverProject(fixtureProject(t, root, "child-project"))
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 || len(definitions[0].Jobs) != 1 {
		t.Fatalf("definitions = %#v", definitions)
	}
	child := definitions[0].Jobs[0].ChildPipeline
	if child == nil || child.SourceFile != ".gci/child.yml" || child.Strategy != "mirror" || child.Depth != 1 || !child.InheritVariables || !child.ForwardYAMLVariables || child.Variables["TARGET"] != "service" {
		t.Fatalf("child = %#v", child)
	}
	if child.Definition == nil || len(child.Definition.Jobs) != 2 || child.Definition.Jobs[1].ChildPipeline == nil || child.Definition.Jobs[1].ChildPipeline.Depth != 2 || child.Definition.Jobs[1].ChildPipeline.Strategy != "depend" {
		t.Fatalf("nested child = %#v", child.Definition)
	}
}

func TestDiscoverRejectsUnsafeAndUnsupportedChildPipelines(t *testing.T) {
	tests := []struct{ name, trigger, want string }{
		{"project", "project: group/service", "multi-project"},
		{"artifact", "include:\n      artifact: generated.yml\n      job: generate", "include kind \"artifact\""},
		{"multiple", "include: [.gci/a.yml, .gci/b.yml]", "exactly one local include"},
		{"traversal", "include: ../outside.yml", "leaves the registered project"},
		{"strategy", "include: .gci/a.yml\n    strategy: unsupported", "strategy \"unsupported\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeWorkflowFixture(t, root, ".gitlab-ci.yml", "bridge:\n  trigger:\n    "+test.trigger)
			writeWorkflowFixture(t, root, ".gci/a.yml", "job:\n  script: [echo a]")
			writeWorkflowFixture(t, root, ".gci/b.yml", "job:\n  script: [echo b]")
			_, err := DiscoverProject(fixtureProject(t, root, "reject-"+test.name))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDiscoverRejectsChildCycleDepthAndSymlink(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		root := t.TempDir()
		writeWorkflowFixture(t, root, ".gitlab-ci.yml", "bridge:\n  trigger:\n    include: .gci/child.yml")
		writeWorkflowFixture(t, root, ".gci/child.yml", "back:\n  trigger:\n    include: .gitlab-ci.yml")
		_, err := DiscoverProject(fixtureProject(t, root, "cycle"))
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		writeWorkflowFixture(t, root, ".gitlab-ci.yml", "one:\n  trigger:\n    include: one.yml")
		writeWorkflowFixture(t, root, "one.yml", "two:\n  trigger:\n    include: two.yml")
		writeWorkflowFixture(t, root, "two.yml", "three:\n  trigger:\n    include: three.yml")
		writeWorkflowFixture(t, root, "three.yml", "job:\n  script: [echo three]")
		_, err := DiscoverProject(fixtureProject(t, root, "depth"))
		if err == nil || !strings.Contains(err.Error(), "exceeds 2 levels") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		writeWorkflowFixture(t, root, ".gitlab-ci.yml", "bridge:\n  trigger:\n    include: child.yml")
		if err := os.WriteFile(filepath.Join(outside, "child.yml"), []byte("job:\n  script: [echo unsafe]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "child.yml"), filepath.Join(root, "child.yml")); err != nil {
			t.Fatal(err)
		}
		_, err := DiscoverProject(fixtureProject(t, root, "symlink"))
		if err == nil || !strings.Contains(err.Error(), "non-symlinked") {
			t.Fatalf("error = %v", err)
		}
	})
}
