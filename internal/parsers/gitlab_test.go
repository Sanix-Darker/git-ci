package parsers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitlabParser_ParseBasic(t *testing.T) {
	parser := NewGitlabParser()
	pipeline, err := parser.Parse(testdataPath("gitlab", "basic.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Jobs) == 0 {
		t.Fatal("expected at least 1 job")
	}

	if len(pipeline.Stages) == 0 {
		t.Error("expected stages to be parsed")
	}
}

func TestGitlabParser_ParseComplex(t *testing.T) {
	parser := NewGitlabParser()
	pipeline, err := parser.Parse(testdataPath("gitlab", "complex.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Stages) != 4 {
		t.Errorf("expected 4 stages, got %d", len(pipeline.Stages))
	}

	if len(pipeline.Jobs) < 4 {
		t.Errorf("expected at least 4 jobs, got %d", len(pipeline.Jobs))
	}
}

func TestGitlabParser_ParseServices(t *testing.T) {
	parser := NewGitlabParser()
	pipeline, err := parser.Parse(testdataPath("gitlab", "services.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Jobs) == 0 {
		t.Fatal("expected at least 1 job")
	}
}

func TestGitlabParser_FileNotFound(t *testing.T) {
	parser := NewGitlabParser()
	_, err := parser.Parse("/nonexistent/path/.gitlab-ci.yml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestGitlabParser_GetProviderName(t *testing.T) {
	parser := NewGitlabParser()
	if parser.GetProviderName() != "gitlab" {
		t.Errorf("expected 'gitlab', got %q", parser.GetProviderName())
	}
}

func TestGitlabParser_PreservesArtifactReportsAndCacheFallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".gitlab-ci.yml")
	contents := []byte(`
stages: [test]
test:
  stage: test
  script: ["printf ok"]
  cache:
    key: deps
    paths: [vendor/]
    fallback_keys: [deps-main, deps-default]
  artifacts:
    paths: [dist/]
    reports:
      junit:
        - dist/unit.xml
        - dist/integration.xml
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGitlabParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	job := pipeline.Jobs["test"]
	if job == nil || job.Artifacts == nil || job.Cache == nil {
		t.Fatalf("parsed output contracts = %#v", job)
	}
	if job.Artifacts.Reports["junit"] != "dist/unit.xml\ndist/integration.xml" {
		t.Fatalf("JUnit reports = %#v", job.Artifacts.Reports)
	}
	if len(job.Cache.Fallback) != 2 || job.Cache.Fallback[0] != "deps-main" || job.Cache.Fallback[1] != "deps-default" {
		t.Fatalf("cache fallbacks = %#v", job.Cache.Fallback)
	}
}
