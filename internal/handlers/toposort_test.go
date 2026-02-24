package handlers

import (
	"testing"

	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestTopologicalSort_NoDeps(t *testing.T) {
	jobs := map[string]*types.Job{
		"a": {},
		"b": {},
		"c": {},
	}

	sorted, err := topologicalSort(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != 3 {
		t.Errorf("expected 3 jobs, got %d", len(sorted))
	}
}

func TestTopologicalSort_LinearChain(t *testing.T) {
	jobs := map[string]*types.Job{
		"deploy": {Needs: []string{"build"}},
		"build":  {Needs: []string{"test"}},
		"test":   {Needs: []string{"lint"}},
		"lint":   {},
	}

	sorted, err := topologicalSort(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sorted) != 4 {
		t.Fatalf("expected 4 jobs, got %d", len(sorted))
	}

	// Verify ordering: lint must come before test, test before build, build before deploy
	indexOf := make(map[string]int)
	for i, name := range sorted {
		indexOf[name] = i
	}

	if indexOf["lint"] >= indexOf["test"] {
		t.Errorf("lint should come before test: %v", sorted)
	}
	if indexOf["test"] >= indexOf["build"] {
		t.Errorf("test should come before build: %v", sorted)
	}
	if indexOf["build"] >= indexOf["deploy"] {
		t.Errorf("build should come before deploy: %v", sorted)
	}
}

func TestTopologicalSort_CircularDep(t *testing.T) {
	jobs := map[string]*types.Job{
		"a": {Needs: []string{"c"}},
		"b": {Needs: []string{"a"}},
		"c": {Needs: []string{"b"}},
	}

	_, err := topologicalSort(jobs)
	if err == nil {
		t.Error("expected circular dependency error, got nil")
	}
}

func TestTopologicalSort_FilteredDeps(t *testing.T) {
	// Job references a dependency that's not in the map (filtered out)
	jobs := map[string]*types.Job{
		"build": {Needs: []string{"test"}},
		"test":  {Needs: []string{"nonexistent"}},
	}

	sorted, err := topologicalSort(jobs)
	if err != nil {
		t.Fatalf("unexpected error (filtered deps should be treated as satisfied): %v", err)
	}
	if len(sorted) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(sorted))
	}
}

func TestGroupByDependencyLevel(t *testing.T) {
	jobs := map[string]*types.Job{
		"lint":   {},
		"test":   {Needs: []string{"lint"}},
		"build":  {Needs: []string{"test"}},
		"deploy": {Needs: []string{"build"}},
		"docs":   {},
	}

	levels, err := groupByDependencyLevel(jobs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(levels) != 4 {
		t.Errorf("expected 4 levels, got %d", len(levels))
	}

	// Level 0 should contain lint and docs (no deps)
	if len(levels[0]) != 2 {
		t.Errorf("expected 2 jobs in level 0, got %d: %v", len(levels[0]), levels[0])
	}
}

func TestGroupByDependencyLevel_Circular(t *testing.T) {
	jobs := map[string]*types.Job{
		"a": {Needs: []string{"b"}},
		"b": {Needs: []string{"a"}},
	}

	_, err := groupByDependencyLevel(jobs)
	if err == nil {
		t.Error("expected circular dependency error")
	}
}
