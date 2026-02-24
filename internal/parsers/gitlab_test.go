package parsers

import (
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
