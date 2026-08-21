package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitlabParserManualJobDefaultsAndConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`
stages: [deploy]
optional:
  stage: deploy
  when: manual
  manual_confirmation: Ship the optional build?
  script: ["printf optional"]
blocking:
  stage: deploy
  when: manual
  allow_failure: false
  script: ["printf blocking"]
rule-blocking:
  stage: deploy
  rules:
    - if: '$CI_COMMIT_BRANCH == "main"'
      when: manual
  script: ["printf rule-blocking"]
rule-optional:
  stage: deploy
  rules:
    - when: manual
      allow_failure: true
  script: ["printf rule-optional"]
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if job := pipeline.Jobs["optional"]; job == nil || job.When != "manual" || !job.AllowFailure || job.ManualConfirmation != "Ship the optional build?" {
		t.Fatalf("optional manual job = %#v", job)
	}
	if job := pipeline.Jobs["blocking"]; job == nil || job.AllowFailure {
		t.Fatalf("blocking manual job = %#v", job)
	}
	if job := pipeline.Jobs["rule-blocking"]; job == nil || len(job.Rules) != 1 || job.Rules[0].When != "manual" || job.Rules[0].AllowFailure {
		t.Fatalf("blocking manual rule = %#v", job)
	}
	if job := pipeline.Jobs["rule-optional"]; job == nil || len(job.Rules) != 1 || !job.Rules[0].AllowFailure {
		t.Fatalf("optional manual rule = %#v", job)
	}
}
