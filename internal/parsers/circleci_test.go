package parsers

import (
	"testing"
)

func TestCircleCIParser_ParseBasic(t *testing.T) {
	parser := NewCircleCIParser()
	pipeline, err := parser.Parse(testdataPath("circleci", "basic.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pipeline.Provider != "circleci" {
		t.Errorf("expected provider 'circleci', got %q", pipeline.Provider)
	}

	if len(pipeline.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(pipeline.Jobs))
	}

	for _, name := range []string{"lint", "test", "build"} {
		if _, ok := pipeline.Jobs[name]; !ok {
			t.Errorf("expected job %q to exist", name)
		}
	}

	// Check workflow dependencies
	testJob := pipeline.Jobs["test"]
	if len(testJob.Needs) != 1 || testJob.Needs[0] != "lint" {
		t.Errorf("expected test to need [lint], got %v", testJob.Needs)
	}

	buildJob := pipeline.Jobs["build"]
	if len(buildJob.Needs) != 1 || buildJob.Needs[0] != "test" {
		t.Errorf("expected build to need [test], got %v", buildJob.Needs)
	}
}

func TestCircleCIParser_Services(t *testing.T) {
	parser := NewCircleCIParser()
	pipeline, err := parser.Parse(testdataPath("circleci", "services.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(pipeline.Jobs))
	}

	job := pipeline.Jobs["test"]
	if len(job.Steps) == 0 {
		t.Error("expected at least 1 step")
	}
}

func TestCircleCIParser_NoJobs(t *testing.T) {
	parser := NewCircleCIParser()
	_, err := parser.Parse(testdataPath("circleci", "no_jobs.yml"))
	if err == nil {
		t.Error("expected validation error for empty jobs, got nil")
	}
}

func TestCircleCIParser_FileNotFound(t *testing.T) {
	parser := NewCircleCIParser()
	_, err := parser.Parse("/nonexistent/path/config.yml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestCircleCIParser_GetProviderName(t *testing.T) {
	parser := NewCircleCIParser()
	if parser.GetProviderName() != "circleci" {
		t.Errorf("expected 'circleci', got %q", parser.GetProviderName())
	}
}

func TestCircleCIParser_Validate(t *testing.T) {
	parser := NewCircleCIParser()
	if err := parser.Validate(nil); err == nil {
		t.Error("expected error for nil pipeline")
	}
}
