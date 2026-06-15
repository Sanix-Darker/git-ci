package parsers

import (
	"testing"
)

func TestTravisParser_ParseBasic(t *testing.T) {
	parser := &TravisParser{}
	pipeline, err := parser.Parse(testdataPath("travis", "basic.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pipeline.Provider != "travis" {
		t.Errorf("expected provider 'travis', got %q", pipeline.Provider)
	}

	// Should create matrix jobs from Go version list
	if len(pipeline.Jobs) != 2 {
		t.Fatalf("expected 2 matrix jobs, got %d", len(pipeline.Jobs))
	}

	hasVersion1 := false
	hasVersion2 := false
	for name := range pipeline.Jobs {
		if name == "test (1.22)" {
			hasVersion1 = true
		}
		if name == "test (1.23)" {
			hasVersion2 = true
		}
	}
	if !hasVersion1 {
		t.Error("expected matrix job 'test (1.22)'")
	}
	if !hasVersion2 {
		t.Error("expected matrix job 'test (1.23)'")
	}

	// Check env propagation
	job := pipeline.Jobs["test (1.22)"]
	if job == nil {
		t.Fatal("expected job 'test (1.22)'")
	}
	if job.Environment["GO111MODULE"] != "on" {
		t.Errorf("expected GO111MODULE=on, got %q", job.Environment["GO111MODULE"])
	}
}

func TestTravisParser_ParseStages(t *testing.T) {
	parser := &TravisParser{}
	pipeline, err := parser.Parse(testdataPath("travis", "stages.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(pipeline.Jobs))
	}

	for _, name := range []string{"lint-go", "test-go", "build-go"} {
		if _, ok := pipeline.Jobs[name]; !ok {
			t.Errorf("expected job %q to exist", name)
		}
	}

	if len(pipeline.Stages) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(pipeline.Stages))
	}

	// Check stage ordering: lint -> test -> build
	buildJob := pipeline.Jobs["build-go"]
	if buildJob == nil {
		t.Fatal("expected 'build-go' job")
	}
	if len(buildJob.Needs) < 1 {
		t.Error("expected build to have stage dependencies")
	}
}

func TestTravisParser_NoJobs(t *testing.T) {
	parser := &TravisParser{}
	pipeline, err := parser.Parse(testdataPath("travis", "no_jobs.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Travis always generates at least one job from lifecycle scripts
	if len(pipeline.Jobs) == 0 {
		t.Error("expected at least one job")
	}
}

func TestTravisParser_FileNotFound(t *testing.T) {
	parser := &TravisParser{}
	_, err := parser.Parse("/nonexistent/path/.travis.yml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestTravisParser_GetProviderName(t *testing.T) {
	parser := &TravisParser{}
	if parser.GetProviderName() != "travis" {
		t.Errorf("expected 'travis', got %q", parser.GetProviderName())
	}
}

func TestTravisParser_Validate(t *testing.T) {
	parser := &TravisParser{}
	if err := parser.Validate(nil); err == nil {
		t.Error("expected error for nil pipeline")
	}
}
