package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionDataPersistsImmutableArtifactsCachesAndJUnit(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	projectPath := t.TempDir()
	project, err := database.CreateProject(ctx, CreateProjectParams{
		Slug: "outputs", Name: "Outputs", SourceType: "local", CanonicalPath: &projectPath,
		DefaultBranch: "main", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := database.UpsertWorkflow(ctx, UpsertWorkflowParams{
		ProjectID: project.ID, Key: "ci", Name: "CI", Definition: []byte(`{"jobs":{}}`), Environment: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual",
		Ref: "refs/heads/feature", SourcePath: filepath.Join(projectPath, ".github/workflows/ci.yml"),
		Environment: []byte(`{}`), Jobs: []EnqueueJob{{
			Key: "build", Name: "Build", Environment: []byte(`{}`), DependencyKeys: []byte(`[]`),
			Steps: []EnqueueStep{{Key: "upload", Name: "Upload", Command: "true", Environment: []byte(`{}`)}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, step := graph.Jobs[0].Job, graph.Jobs[0].Steps[0]
	artifact, err := database.CreateArtifact(ctx, CreateArtifactParams{
		ProjectID: project.ID, RunID: run.ID, JobID: job.ID, StepID: step.ID,
		Name: "dist", StorageKey: "artifacts/body.zip", SHA256: strings.Repeat("a", 64),
		SizeBytes: 42, FileCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.RunID != run.ID || artifact.FileCount != 2 {
		t.Fatalf("artifact = %#v", artifact)
	}
	if _, err := database.CreateArtifact(ctx, CreateArtifactParams{
		ProjectID: project.ID, RunID: run.ID, JobID: job.ID, Name: "dist",
		StorageKey: "artifacts/duplicate.zip", SHA256: strings.Repeat("b", 64),
	}); err == nil {
		t.Fatal("duplicate immutable artifact name was accepted")
	} else {
		var conflict *ErrConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("duplicate artifact error = %T %v", err, err)
		}
	}
	cache, err := database.PutCacheEntry(ctx, PutCacheEntryParams{
		ProjectID: project.ID, Ref: "refs/heads/feature", Key: "go-linux-v1",
		StorageKey: "cache/go.tar.gz", SHA256: strings.Repeat("c", 64), SizeBytes: 99, FileCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := database.FindCacheEntry(ctx, project.ID, []string{"refs/heads/feature", "refs/heads/main"}, "missing", []string{"go-linux-"})
	if err != nil || !ok || found.ID != cache.ID {
		t.Fatalf("prefix cache = %#v ok=%v err=%v", found, ok, err)
	}
	if _, err := database.PutCacheEntry(ctx, PutCacheEntryParams{
		ProjectID: project.ID, Ref: cache.Ref, Key: cache.Key, StorageKey: "cache/other.tar.gz",
		SHA256: strings.Repeat("d", 64),
	}); err == nil {
		t.Fatal("duplicate immutable cache key was accepted")
	}
	report, err := database.CreateTestReport(ctx, CreateTestReportParams{
		ArtifactID: artifact.ID, ProjectID: project.ID, RunID: run.ID, JobID: job.ID,
		StepID: step.ID, Name: "junit.xml", Tests: 12, Failures: 1, Errors: 2,
		Skipped: 3, DurationSeconds: 4.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	reports, err := database.ListRunTestReports(ctx, run.ID)
	if err != nil || len(reports) != 1 || reports[0].ID != report.ID {
		t.Fatalf("reports = %#v err=%v", reports, err)
	}
}
