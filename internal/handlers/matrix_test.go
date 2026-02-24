package handlers

import (
	"testing"

	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestComputeCartesianProduct(t *testing.T) {
	matrix := map[string][]interface{}{
		"os":         {"ubuntu", "macos"},
		"go-version": {"1.21", "1.22"},
	}

	combos := computeCartesianProduct(matrix)
	if len(combos) != 4 {
		t.Errorf("expected 4 combinations, got %d", len(combos))
	}
}

func TestComputeCartesianProduct_Empty(t *testing.T) {
	combos := computeCartesianProduct(map[string][]interface{}{})
	if combos != nil {
		t.Errorf("expected nil for empty matrix, got %v", combos)
	}
}

func TestComputeCartesianProduct_Single(t *testing.T) {
	matrix := map[string][]interface{}{
		"os": {"ubuntu", "macos", "windows"},
	}

	combos := computeCartesianProduct(matrix)
	if len(combos) != 3 {
		t.Errorf("expected 3 combinations, got %d", len(combos))
	}
}

func TestApplyExcludes(t *testing.T) {
	combos := []map[string]interface{}{
		{"os": "ubuntu", "version": "1.21"},
		{"os": "ubuntu", "version": "1.22"},
		{"os": "macos", "version": "1.21"},
		{"os": "macos", "version": "1.22"},
	}

	excludes := []map[string]interface{}{
		{"os": "macos", "version": "1.21"},
	}

	result := applyExcludes(combos, excludes)
	if len(result) != 3 {
		t.Errorf("expected 3 after exclude, got %d", len(result))
	}
}

func TestApplyIncludes(t *testing.T) {
	combos := []map[string]interface{}{
		{"os": "ubuntu", "version": "1.22"},
	}

	includes := []map[string]interface{}{
		{"os": "ubuntu", "version": "1.22", "experimental": true},
		{"os": "windows", "version": "1.22"},
	}

	result := applyIncludes(combos, includes)
	// Should have 2: original (with experimental merged) + new windows entry
	if len(result) != 2 {
		t.Errorf("expected 2 after include, got %d", len(result))
	}
}

func TestExpandMatrixJobs_NoMatrix(t *testing.T) {
	jobs := map[string]*types.Job{
		"test": {Name: "Test", Steps: []types.Step{{Run: "echo hi"}}},
	}

	expanded := expandMatrixJobs(jobs)
	if len(expanded) != 1 {
		t.Errorf("expected 1 job (no expansion), got %d", len(expanded))
	}
}

func TestExpandMatrixJobs_WithMatrix(t *testing.T) {
	jobs := map[string]*types.Job{
		"test": {
			Name: "Test",
			Strategy: &types.Strategy{
				Matrix: map[string][]interface{}{
					"os": {"ubuntu", "macos"},
				},
			},
			Steps: []types.Step{{Run: "echo hi"}},
		},
	}

	expanded := expandMatrixJobs(jobs)
	if len(expanded) != 2 {
		t.Errorf("expected 2 expanded jobs, got %d", len(expanded))
	}

	// Each expanded job should have matrix env vars
	for _, job := range expanded {
		if _, ok := job.Environment["os"]; !ok {
			t.Error("expected 'os' env var in expanded job")
		}
		if _, ok := job.Environment["MATRIX_OS"]; !ok {
			t.Error("expected 'MATRIX_OS' env var in expanded job")
		}
	}
}

func TestExpandMatrixJobs_NeedsFixup(t *testing.T) {
	jobs := map[string]*types.Job{
		"build": {
			Name: "Build",
			Strategy: &types.Strategy{
				Matrix: map[string][]interface{}{
					"os": {"ubuntu", "macos"},
				},
			},
			Steps: []types.Step{{Run: "make build"}},
		},
		"deploy": {
			Name:  "Deploy",
			Needs: []string{"build"},
			Steps: []types.Step{{Run: "make deploy"}},
		},
	}

	expanded := expandMatrixJobs(jobs)
	// build should expand to 2, deploy stays as 1
	if len(expanded) != 3 {
		t.Errorf("expected 3 total jobs, got %d", len(expanded))
	}

	// deploy's Needs should be updated to reference expanded build names
	for name, job := range expanded {
		if name == "deploy" {
			if len(job.Needs) != 2 {
				t.Errorf("expected deploy to need 2 expanded build jobs, got %d: %v", len(job.Needs), job.Needs)
			}
		}
	}
}

func TestFormatCombination(t *testing.T) {
	combo := map[string]interface{}{"go": "1.22", "os": "ubuntu"}
	result := formatCombination(combo)
	if result != "1.22, ubuntu" {
		t.Errorf("expected '1.22, ubuntu', got %q", result)
	}
}
