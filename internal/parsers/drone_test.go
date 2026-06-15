package parsers

import (
	"testing"
)

func TestDroneParser_ParseBasic(t *testing.T) {
	parser := &DroneParser{}
	pipeline, err := parser.Parse(testdataPath("drone", "basic.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if pipeline.Provider != "drone" {
		t.Errorf("expected provider 'drone', got %q", pipeline.Provider)
	}

	if len(pipeline.Jobs) != 2 {
		t.Fatalf("expected 2 steps/jobs, got %d", len(pipeline.Jobs))
	}

	for _, name := range []string{"test", "build"} {
		if _, ok := pipeline.Jobs[name]; !ok {
			t.Errorf("expected step %q to exist", name)
		}
	}

	// Check depends_on mapping
	buildJob := pipeline.Jobs["build"]
	if len(buildJob.Needs) != 1 || buildJob.Needs[0] != "test" {
		t.Errorf("expected build to depend on [test], got %v", buildJob.Needs)
	}
}

func TestDroneParser_Services(t *testing.T) {
	parser := &DroneParser{}
	pipeline, err := parser.Parse(testdataPath("drone", "services.yml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(pipeline.Jobs) != 2 {
		t.Fatalf("expected 2 steps/jobs, got %d", len(pipeline.Jobs))
	}

	// Check that services are passed to at least one job
	job := pipeline.Jobs["test"]
	if job.Services == nil || len(job.Services) != 2 {
		t.Errorf("expected 2 services on test job, got %d", len(job.Services))
	}

	if job.Image != "golang:1.22" {
		t.Errorf("expected image 'golang:1.22' (resolved from ${GO_VERSION}), got %q", job.Image)
	}
}

func TestDroneParser_NoSteps(t *testing.T) {
	parser := &DroneParser{}
	_, err := parser.Parse(testdataPath("drone", "no_steps.yml"))
	if err == nil {
		t.Error("expected validation error for empty steps, got nil")
	}
}

func TestDroneParser_FileNotFound(t *testing.T) {
	parser := &DroneParser{}
	_, err := parser.Parse("/nonexistent/path/.drone.yml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestDroneParser_GetProviderName(t *testing.T) {
	parser := &DroneParser{}
	if parser.GetProviderName() != "drone" {
		t.Errorf("expected 'drone', got %q", parser.GetProviderName())
	}
}

func TestDroneParser_Validate(t *testing.T) {
	parser := &DroneParser{}
	if err := parser.Validate(nil); err == nil {
		t.Error("expected error for nil pipeline")
	}
}
