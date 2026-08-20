package execution

import (
	"encoding/json"
	"testing"

	"github.com/sanix-darker/git-ci/internal/executionsemantics"
	citypes "github.com/sanix-darker/git-ci/pkg/types"
)

func TestContainerSemanticsAreCopiedResolvedAndFrozen(t *testing.T) {
	source := &citypes.Job{
		Name: "integration", Environment: map[string]string{},
		Container: &citypes.Container{Image: "tool:${{ matrix.version }}", Env: map[string]string{"TARGET": "${{ matrix.version }}"}, CPUs: "1", Memory: "256m"},
		Services:  map[string]*citypes.Service{"db": {Image: "postgres:${{ matrix.version }}", Alias: "database"}},
		Steps:     []citypes.Step{{Name: "probe", Run: "echo ready"}},
	}
	job, err := normalizeJob("integration", source, deploymentExtension{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if err := applyMatrixVariant(&job, executionsemantics.MatrixVariant{Values: map[string]string{"version": "16"}, Index: 1, Total: 1, Label: "VERSION=16"}, "github"); err != nil {
		t.Fatalf("apply matrix: %v", err)
	}
	if job.Container.Image != "tool:16" || job.Container.Env["TARGET"] != "16" || job.Services["db"].Image != "postgres:16" {
		t.Fatalf("resolved runtime = %#v / %#v", job.Container, job.Services)
	}
	source.Container.Image = "mutated"
	source.Services["db"].Image = "mutated"
	if job.Container.Image != "tool:16" || job.Services["db"].Image != "postgres:16" {
		t.Fatal("normalized runtime aliases parser-owned values")
	}
	var frozen frozenJobSemantics
	if err := json.Unmarshal([]byte(job.Environment["GCI_JOB_SEMANTICS_JSON"]), &frozen); err != nil {
		t.Fatalf("decode frozen semantics: %v", err)
	}
	if frozen.Container == nil || frozen.Container.Image != "tool:16" || frozen.Services["db"].Image != "postgres:16" {
		t.Fatalf("frozen runtime = %#v / %#v", frozen.Container, frozen.Services)
	}
}
