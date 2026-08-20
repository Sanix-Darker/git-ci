package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
)

type fakeEnqueuer struct {
	calls               int
	workflowID, trigger string
}

func (f *fakeEnqueuer) EnqueueTriggered(_ context.Context, workflowID, _, _, trigger string) (store.Run, error) {
	f.calls++
	f.workflowID = workflowID
	f.trigger = trigger
	return store.Run{ID: "run"}, nil
}

func TestManagerCreatesClaimsAndAdvancesSchedule(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "gci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	project, err := database.CreateProject(ctx, store.CreateProjectParams{Slug: "cron", Name: "Cron", SourceType: "local", DefaultBranch: "main", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := database.UpsertWorkflow(ctx, store.UpsertWorkflowParams{ProjectID: project.ID, Key: "ci", Name: "CI", Definition: []byte(`{"jobs":{}}`), Environment: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	enqueuer := &fakeEnqueuer{}
	manager, err := NewManager(database, enqueuer)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	schedule, err := manager.Create(ctx, project.ID, workflow.ID, "*/5 * * * *", "main", "UTC", true)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.NextRunAt == nil {
		t.Fatal("next run missing")
	}
	due := *schedule.NextRunAt
	if err := manager.ProcessDue(ctx, due); err != nil {
		t.Fatal(err)
	}
	if enqueuer.calls != 1 || enqueuer.workflowID != workflow.ID || enqueuer.trigger != "schedule" {
		t.Fatalf("enqueuer = %#v", enqueuer)
	}
	updated, err := database.GetWorkflowSchedule(ctx, schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(due) || updated.NextRunAt == nil || !updated.NextRunAt.After(due) {
		t.Fatalf("updated = %#v", updated)
	}
	if err := manager.ProcessDue(ctx, due); err != nil {
		t.Fatal(err)
	}
	if enqueuer.calls != 1 {
		t.Fatalf("duplicate calls = %d", enqueuer.calls)
	}
}
