package handlers

import (
	"testing"

	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestValidatePipeline_Nil(t *testing.T) {
	errors := validatePipeline(nil, false)
	if len(errors) != 1 || errors[0] != "pipeline is nil" {
		t.Errorf("expected 'pipeline is nil' error, got %v", errors)
	}
}

func TestValidatePipeline_Empty(t *testing.T) {
	pipeline := &types.Pipeline{}
	errors := validatePipeline(pipeline, false)
	if len(errors) < 2 {
		t.Errorf("expected at least 2 errors (name + jobs), got %d: %v", len(errors), errors)
	}
}

func TestValidatePipeline_Valid(t *testing.T) {
	pipeline := &types.Pipeline{
		Name: "Test Pipeline",
		Jobs: map[string]*types.Job{
			"test": {
				RunsOn: "ubuntu-latest",
				Steps:  []types.Step{{Run: "echo hello"}},
			},
		},
	}

	errors := validatePipeline(pipeline, false)
	if len(errors) != 0 {
		t.Errorf("expected no errors, got %v", errors)
	}
}

func TestValidatePipeline_InvalidDep(t *testing.T) {
	pipeline := &types.Pipeline{
		Name: "Test Pipeline",
		Jobs: map[string]*types.Job{
			"test": {
				RunsOn: "ubuntu-latest",
				Needs:  []string{"nonexistent"},
				Steps:  []types.Step{{Run: "echo hello"}},
			},
		},
	}

	errors := validatePipeline(pipeline, false)
	hasDepError := false
	for _, e := range errors {
		if e == "job 'test' depends on non-existent job 'nonexistent'" {
			hasDepError = true
		}
	}
	if !hasDepError {
		t.Errorf("expected dependency error, got %v", errors)
	}
}

func TestValidatePipeline_CircularDeps(t *testing.T) {
	pipeline := &types.Pipeline{
		Name: "Test Pipeline",
		Jobs: map[string]*types.Job{
			"a": {
				RunsOn: "ubuntu-latest",
				Needs:  []string{"b"},
				Steps:  []types.Step{{Run: "echo a"}},
			},
			"b": {
				RunsOn: "ubuntu-latest",
				Needs:  []string{"a"},
				Steps:  []types.Step{{Run: "echo b"}},
			},
		},
	}

	errors := validatePipeline(pipeline, false)
	hasCycleError := false
	for _, e := range errors {
		if len(e) > 0 {
			hasCycleError = true
		}
	}
	if !hasCycleError {
		t.Error("expected circular dependency error")
	}
}

func TestValidatePipeline_Strict(t *testing.T) {
	pipeline := &types.Pipeline{
		Name: "Test Pipeline",
		Jobs: map[string]*types.Job{
			"test": {
				Steps: []types.Step{{Run: "echo hello"}},
			},
		},
	}

	errors := validatePipeline(pipeline, true)
	hasRunnerError := false
	for _, e := range errors {
		if e == "job 'test' has no runner specified" {
			hasRunnerError = true
		}
	}
	if !hasRunnerError {
		t.Errorf("expected runner error in strict mode, got %v", errors)
	}
}

func TestCheckCircularDependencies_NoCycle(t *testing.T) {
	jobs := map[string]*types.Job{
		"a": {},
		"b": {Needs: []string{"a"}},
	}

	err := checkCircularDependencies("b", jobs["b"], jobs, []string{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckCircularDependencies_Cycle(t *testing.T) {
	jobs := map[string]*types.Job{
		"a": {Needs: []string{"b"}},
		"b": {Needs: []string{"a"}},
	}

	err := checkCircularDependencies("a", jobs["a"], jobs, []string{})
	if err == nil {
		t.Error("expected circular dependency error")
	}
}
