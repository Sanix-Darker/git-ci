package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubContainerJobUsesShAndPreservesServices(t *testing.T) {
	path := writeContainerParserFixture(t, "runtime.yml", `name: Runtime
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    container:
      image: alpine:3.20
      credentials:
        username: robot
        password: secret
    services:
      redis:
        image: redis:7-alpine
        ports: [6379]
        options: --health-cmd "redis-cli ping"
    steps:
      - run: echo ready
`)
	pipeline, err := NewGithubParser().Parse(path)
	if err != nil {
		t.Fatalf("parse GitHub runtime: %v", err)
	}
	job := pipeline.Jobs["test"]
	if job == nil || job.Container == nil || job.Container.Image != "alpine:3.20" {
		t.Fatalf("container = %#v", job)
	}
	if job.Steps[0].Shell != "sh" {
		t.Fatalf("container default shell = %q", job.Steps[0].Shell)
	}
	if job.Container.Credentials["username"] != "robot" {
		t.Fatalf("credentials = %#v", job.Container.Credentials)
	}
	if service := job.Services["redis"]; service == nil || service.Image != "redis:7-alpine" || service.Ports[0] != "6379" {
		t.Fatalf("service = %#v", service)
	}
}

func TestGitLabJobsInheritDefaultImageAndServices(t *testing.T) {
	path := writeContainerParserFixture(t, ".gitlab-ci.yml", `default:
  image:
    name: alpine:3.20
    entrypoint: [""]
  services:
    - name: redis:7-alpine
      alias: cache
stages: [test]
verify:
  stage: test
  script: ["echo ready"]
`)
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatalf("parse GitLab runtime: %v", err)
	}
	job := pipeline.Jobs["verify"]
	if job == nil || job.Container == nil || job.Container.Image != "alpine:3.20" {
		t.Fatalf("container = %#v", job)
	}
	if len(job.Container.Entrypoint) != 1 || job.Container.Entrypoint[0] != "" {
		t.Fatalf("entrypoint = %#v", job.Container.Entrypoint)
	}
	if service := job.Services["cache"]; service == nil || service.Image != "redis:7-alpine" || service.Alias != "cache" {
		t.Fatalf("service = %#v", service)
	}
}

func writeContainerParserFixture(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
