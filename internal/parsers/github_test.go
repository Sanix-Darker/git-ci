package parsers

import (
	"path/filepath"
	"runtime"
	"testing"
)

func testdataPath(provider, filename string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "testdata", provider, filename)
}

func TestGithubParser_ParseBasic(t *testing.T) {
	parser := NewGithubParser()
	pipeline, err := parser.Parse(testdataPath("github", "basic.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pipeline.Name != "Basic CI" {
		t.Errorf("expected name 'Basic CI', got %q", pipeline.Name)
	}
	if pipeline.Provider != "github" {
		t.Errorf("expected provider 'github', got %q", pipeline.Provider)
	}

	if len(pipeline.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(pipeline.Jobs))
	}

	job, ok := pipeline.Jobs["test"]
	if !ok {
		t.Fatal("expected job 'test' to exist")
	}

	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("expected runs-on 'ubuntu-latest', got %q", job.RunsOn)
	}

	if len(job.Steps) != 2 {
		t.Errorf("expected 2 steps, got %d", len(job.Steps))
	}
}

func TestGithubParser_ParseMatrix(t *testing.T) {
	parser := NewGithubParser()
	pipeline, err := parser.Parse(testdataPath("github", "complex_matrix.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, ok := pipeline.Jobs["test"]
	if !ok {
		t.Fatal("expected job 'test' to exist")
	}

	if job.Strategy == nil {
		t.Fatal("expected strategy to be set")
	}

	if job.Strategy.FailFast {
		t.Error("expected fail-fast to be false")
	}

	if job.Strategy.MaxParallel != 4 {
		t.Errorf("expected max-parallel 4, got %d", job.Strategy.MaxParallel)
	}

	if len(job.Strategy.Matrix) < 2 {
		t.Errorf("expected at least 2 matrix dimensions, got %d", len(job.Strategy.Matrix))
	}

	if len(job.Strategy.Include) == 0 {
		t.Error("expected include entries to be parsed")
	}

	if len(job.Strategy.Exclude) == 0 {
		t.Error("expected exclude entries to be parsed")
	}
}

func TestGithubParser_ParseServices(t *testing.T) {
	parser := NewGithubParser()
	pipeline, err := parser.Parse(testdataPath("github", "services.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, ok := pipeline.Jobs["integration"]
	if !ok {
		t.Fatal("expected job 'integration' to exist")
	}

	if len(job.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(job.Services))
	}

	pg, ok := job.Services["postgres"]
	if !ok {
		t.Fatal("expected postgres service")
	}
	if pg.Image != "postgres:15" {
		t.Errorf("expected postgres:15, got %q", pg.Image)
	}

	redis, ok := job.Services["redis"]
	if !ok {
		t.Fatal("expected redis service")
	}
	if redis.Image != "redis:7" {
		t.Errorf("expected redis:7, got %q", redis.Image)
	}
}

func TestGithubParser_ParseNeeds(t *testing.T) {
	parser := NewGithubParser()
	pipeline, err := parser.Parse(testdataPath("github", "needs_chain.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Jobs) != 4 {
		t.Fatalf("expected 4 jobs, got %d", len(pipeline.Jobs))
	}

	testJob := pipeline.Jobs["test"]
	if len(testJob.Needs) != 1 || testJob.Needs[0] != "lint" {
		t.Errorf("expected test to need [lint], got %v", testJob.Needs)
	}

	buildJob := pipeline.Jobs["build"]
	if len(buildJob.Needs) != 2 {
		t.Errorf("expected build to need 2 jobs, got %d", len(buildJob.Needs))
	}

	deployJob := pipeline.Jobs["deploy"]
	if len(deployJob.Needs) != 1 || deployJob.Needs[0] != "build" {
		t.Errorf("expected deploy to need [build], got %v", deployJob.Needs)
	}
}

func TestGithubParser_ParseDefaults(t *testing.T) {
	parser := NewGithubParser()
	pipeline, err := parser.Parse(testdataPath("github", "defaults.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job := pipeline.Jobs["test"]
	if len(job.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(job.Steps))
	}

	// First step should inherit defaults
	if job.Steps[0].Shell != "bash" {
		t.Errorf("expected step 0 shell 'bash', got %q", job.Steps[0].Shell)
	}
	if job.Steps[0].WorkingDir != "src" {
		t.Errorf("expected step 0 workdir 'src', got %q", job.Steps[0].WorkingDir)
	}

	// Second step overrides shell
	if job.Steps[1].Shell != "sh" {
		t.Errorf("expected step 1 shell 'sh', got %q", job.Steps[1].Shell)
	}
}

func TestGithubParser_InvalidYAML(t *testing.T) {
	parser := NewGithubParser()
	_, err := parser.Parse(testdataPath("github", "invalid_yaml.yml"))
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestGithubParser_NoJobs(t *testing.T) {
	parser := NewGithubParser()
	_, err := parser.Parse(testdataPath("github", "no_jobs.yml"))
	if err == nil {
		t.Error("expected validation error for empty jobs, got nil")
	}
}

func TestGithubParser_FileNotFound(t *testing.T) {
	parser := NewGithubParser()
	_, err := parser.Parse("/nonexistent/path/workflow.yml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestGithubParser_UnicodeNames(t *testing.T) {
	parser := NewGithubParser()
	pipeline, err := parser.Parse(testdataPath("github", "unicode_names.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pipeline.Name != "Unicode Workflow 日本語テスト" {
		t.Errorf("expected unicode workflow name, got %q", pipeline.Name)
	}

	job, ok := pipeline.Jobs["test-unicode"]
	if !ok {
		t.Fatal("expected job 'test-unicode' to exist")
	}

	if job.Name != "Tëst with spëcial chàràctërs" {
		t.Errorf("expected unicode job name, got %q", job.Name)
	}
}

func TestGithubParser_GetProviderName(t *testing.T) {
	parser := NewGithubParser()
	if parser.GetProviderName() != "github" {
		t.Errorf("expected 'github', got %q", parser.GetProviderName())
	}
}

func TestGithubParser_Validate(t *testing.T) {
	parser := NewGithubParser()

	// nil pipeline
	if err := parser.Validate(nil); err == nil {
		t.Error("expected error for nil pipeline")
	}
}
