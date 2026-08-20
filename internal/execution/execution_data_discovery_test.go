package execution

import (
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestDiscoverFreezesActionInputsAndGitLabOutputContracts(t *testing.T) {
	root := t.TempDir()
	writeWorkflowFixture(t, root, filepath.Join(".github", "workflows", "artifact.yml"), `
name: Artifact
on: workflow_dispatch
jobs:
  build:
    strategy:
      matrix:
        os: [linux]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/upload-artifact@v4
        with:
          name: build-${{ matrix.os }}
          path: dist/${{ matrix.os }}
`)
	definitions, err := Discover([]store.Project{{ID: "project", Slug: "project", CanonicalPath: &root}})
	if err != nil {
		t.Fatal(err)
	}
	inputs := definitions[0].Jobs[0].Steps[0].Inputs
	if inputs["name"] != "build-linux" || inputs["path"] != "dist/linux" {
		t.Fatalf("resolved action inputs = %#v", inputs)
	}
	root = t.TempDir()
	writeWorkflowFixture(t, root, ".gitlab-ci.yml", `
stages: [test]
test:
  stage: test
  script: ["printf ok"]
  cache:
    key: deps
    paths: [vendor/]
    fallback_keys: [deps-default]
  artifacts:
    name: test-output
    paths: [dist/]
    reports:
      junit: dist/junit.xml
`)
	definitions, err = Discover([]store.Project{{ID: "project", Slug: "project", CanonicalPath: &root}})
	if err != nil {
		t.Fatal(err)
	}
	job := definitions[0].Jobs[0]
	if job.Artifacts == nil || job.Artifacts.Reports["junit"] != "dist/junit.xml" || job.Cache == nil || job.Cache.Key != "deps" {
		t.Fatalf("GitLab output contracts = %#v", job)
	}
}
