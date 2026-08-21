package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitlabParserPreservesDotenvInheritanceControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`
stages: [build, test]
build:
  stage: build
  script: [echo build]
  artifacts:
    reports:
      dotenv: build.env
blocked:
  stage: test
  needs:
    - job: build
      artifacts: false
  script: [echo blocked]
isolated:
  stage: test
  dependencies: []
  script: [echo isolated]
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := pipeline.Jobs["build"].Artifacts.Reports["dotenv"]; got != "build.env" {
		t.Fatalf("dotenv report = %q", got)
	}
	if pipeline.Jobs["blocked"].NeedsArtifacts["build"] {
		t.Fatal("needs.artifacts: false was not preserved")
	}
	if !pipeline.Jobs["isolated"].DependenciesDefined || len(pipeline.Jobs["isolated"].Dependencies) != 0 {
		t.Fatalf("isolated dependencies = %#v", pipeline.Jobs["isolated"])
	}
}
