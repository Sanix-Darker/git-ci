package parsers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitlabParserPreservesOptionalNeedsAndAllowsMissingOptionalTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`producer:
  script: ["printf producer"]
consumer:
  needs:
    - job: producer
      optional: true
    - job: absent
      optional: true
  script: ["printf consumer"]
required:
  needs:
    - job: producer
      optional: false
  script: ["printf required"]
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	consumer := pipeline.Jobs["consumer"]
	if consumer == nil || !consumer.NeedsOptional["producer"] || !consumer.NeedsOptional["absent"] {
		t.Fatalf("consumer optional needs = %#v", consumer)
	}
	if pipeline.Jobs["required"].NeedsOptional["producer"] {
		t.Fatal("optional: false must remain a required edge")
	}
}

func TestGitlabParserRejectsMissingRequiredNeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	if err := os.WriteFile(path, []byte("consumer:\n  needs: [absent]\n  script: [\"printf consumer\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewGitlabParser().Parse(path)
	if err == nil || !strings.Contains(err.Error(), "non-existent job 'absent'") {
		t.Fatalf("missing required need error = %v", err)
	}
}
