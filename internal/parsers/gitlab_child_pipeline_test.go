package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitlabParserPreservesLocalChildPipelineContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`bridge:
  inherit:
    variables: false
  variables:
    TARGET: production
  trigger:
    include:
      - local: .gci/child.yml
    strategy: mirror
    forward:
      yaml_variables: false
      pipeline_variables: true
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	trigger := pipeline.Jobs["bridge"].Trigger
	if trigger == nil || trigger.Include != ".gci/child.yml" || trigger.IncludeKind != "local" || trigger.IncludeCount != 1 || trigger.Strategy != "mirror" {
		t.Fatalf("trigger = %#v", trigger)
	}
	if trigger.InheritVariables == nil || *trigger.InheritVariables || trigger.Forward == nil || trigger.Forward.YAMLVariables == nil || *trigger.Forward.YAMLVariables || trigger.Forward.PipelineVariables == nil || !*trigger.Forward.PipelineVariables {
		t.Fatalf("variable forwarding = %#v", trigger)
	}
}

func TestGitlabParserIdentifiesUnsupportedChildIncludeKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	if err := os.WriteFile(path, []byte("bridge:\n  trigger:\n    include:\n      artifact: generated.yml\n      job: generate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if trigger := pipeline.Jobs["bridge"].Trigger; trigger == nil || trigger.IncludeKind != "artifact" || trigger.Include != "generated.yml" {
		t.Fatalf("trigger = %#v", trigger)
	}
}
