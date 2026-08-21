package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseLifecyclePersistsAndProtectsProvenance(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "releases.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "release-lifecycle")
	commit := strings.Repeat("a", 40)
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", Ref: "refs/heads/main", CommitSHA: commit, SourcePath: "/srv/release-lifecycle", Jobs: []EnqueueJob{{Key: "release", Name: "Release", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "package", Name: "Package", Command: "true"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimNextQueuedRun(ctx, "release-test-worker")
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim release source = %#v, %v", claimed, err)
	}
	if _, err := database.TransitionRun(ctx, run.ID, StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	created, err := database.CreateRelease(ctx, CreateReleaseParams{ProjectID: project.ID, RunID: run.ID, TagName: "v1.0.0", TargetCommitSHA: commit, Name: "Version 1", Notes: "initial", Actor: "test"})
	if err != nil || created.State != ReleaseDraft || created.ProjectName != project.Name {
		t.Fatalf("create release = %#v, %v", created, err)
	}
	if _, err := database.CreateRelease(ctx, CreateReleaseParams{ProjectID: project.ID, RunID: run.ID, TagName: "v1.0.0", TargetCommitSHA: commit, Name: "duplicate", Actor: "test"}); !errors.Is(err, &ErrConflict{}) {
		t.Fatalf("duplicate release error = %T %v", err, err)
	}
	updated, err := database.UpdateRelease(ctx, UpdateReleaseParams{ReleaseID: created.ID, Name: "Version 1 stable", Notes: "frozen next", Prerelease: false})
	if err != nil || updated.RunID != run.ID || updated.TagName != "v1.0.0" || updated.Notes != "frozen next" {
		t.Fatalf("update release = %#v, %v", updated, err)
	}
	published, err := database.PublishRelease(ctx, created.ID, "publisher")
	if err != nil || published.State != ReleasePublished || published.PublishedAt == nil || published.PublishedBy == nil {
		t.Fatalf("publish release = %#v, %v", published, err)
	}
	again, err := database.PublishRelease(ctx, created.ID, "other")
	if err != nil || again.PublishedAt == nil || !again.PublishedAt.Equal(*published.PublishedAt) || *again.PublishedBy != "publisher" {
		t.Fatalf("idempotent publish = %#v, %v", again, err)
	}
	if _, err := database.UpdateRelease(ctx, UpdateReleaseParams{ReleaseID: created.ID, Name: "mutated", Notes: "", Prerelease: false}); !errors.Is(err, &ErrReleaseTransition{}) {
		t.Fatalf("published update error = %T %v", err, err)
	}
	if err := database.DeleteDraftRelease(ctx, created.ID); !errors.Is(err, &ErrReleaseTransition{}) {
		t.Fatalf("published delete error = %T %v", err, err)
	}
	latest, err := database.GetLatestRelease(ctx, project.ID)
	if err != nil || latest.ID != created.ID {
		t.Fatalf("latest release = %#v, %v", latest, err)
	}
	if _, err := database.DeactivateProject(ctx, project.ID, project.Slug); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.GetRelease(ctx, created.ID)
	if err != nil || persisted.ProjectID != project.ID || persisted.State != ReleasePublished {
		t.Fatalf("persisted release = %#v, %v", persisted, err)
	}
}

func TestReleaseRejectsUnsuccessfulForeignAndMismatchedSources(t *testing.T) {
	ctx := context.Background()
	database, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, database, "release-invalid")
	other, _ := database.CreateProject(ctx, testProjectParams("release-other"))
	commit := strings.Repeat("b", 40)
	run, err := database.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", CommitSHA: commit, SourcePath: "/srv/release-invalid", Jobs: []EnqueueJob{{Key: "test", Name: "Test", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "test", Name: "Test", Command: "true"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	base := CreateReleaseParams{ProjectID: project.ID, RunID: run.ID, TagName: "v0.1.0", TargetCommitSHA: commit, Name: "Version", Actor: "test"}
	if _, err := database.CreateRelease(ctx, base); !errors.Is(err, &ErrReleaseTransition{}) {
		t.Fatalf("queued source error = %T %v", err, err)
	}
	claimed, _ := database.ClaimNextQueuedRun(ctx, "release-invalid-worker")
	if claimed == nil {
		t.Fatal("source run was not claimed")
	}
	if _, err := database.TransitionRun(ctx, run.ID, StatusSucceeded); err != nil {
		t.Fatal(err)
	}
	foreign := base
	foreign.ProjectID = other.ID
	if _, err := database.CreateRelease(ctx, foreign); !errors.Is(err, &ErrReleaseTransition{}) {
		t.Fatalf("foreign source error = %T %v", err, err)
	}
	mismatch := base
	mismatch.TargetCommitSHA = strings.Repeat("c", 40)
	if _, err := database.CreateRelease(ctx, mismatch); !errors.Is(err, &ErrReleaseTransition{}) {
		t.Fatalf("mismatched source error = %T %v", err, err)
	}
}
