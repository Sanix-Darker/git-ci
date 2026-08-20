package webhooks

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerCreateStoresOnlyTokenHashAndDeliversOnce(t *testing.T) {
	ctx := context.Background()
	database, project, workflow := webhookTestFixture(t)
	enqueuer := &recordingEnqueuer{run: store.Run{ID: "queued-run"}}
	manager, err := NewManager(database, enqueuer)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	created, err := manager.Create(ctx, project.ID, "github-push", "github", workflow.ID, "refs/heads/main")
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}
	if created.Token == "" {
		t.Fatal("create endpoint returned an empty one-time token")
	}
	hash := sha256.Sum256([]byte(created.Token))
	if string(created.Endpoint.TokenHash) != string(hash[:]) {
		t.Fatal("endpoint token hash does not match the issued token")
	}
	if string(created.Endpoint.TokenHash) == created.Token {
		t.Fatal("endpoint persisted the plaintext token")
	}
	stored, err := database.GetWebhookEndpoint(ctx, created.Endpoint.ID)
	if err != nil {
		t.Fatalf("get endpoint: %v", err)
	}
	if string(stored.TokenHash) != string(hash[:]) {
		t.Fatal("stored endpoint hash changed after creation")
	}

	payload := []byte(`{"ref":"refs/heads/feature","after":"github-sha"}`)
	first, run, err := manager.Deliver(ctx, created.Endpoint.ID, created.Token, "delivery-1", "push", payload)
	if err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if !first.Created || first.Delivery.Status != store.WebhookDeliveryAccepted || run == nil || run.ID != "queued-run" {
		t.Fatalf("first delivery = %#v, run = %#v", first, run)
	}
	if len(enqueuer.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(enqueuer.calls))
	}
	if got := enqueuer.calls[0]; got.workflowID != workflow.ID || got.ref != "refs/heads/feature" || got.commitSHA != "github-sha" || got.trigger != "webhook" {
		t.Errorf("github enqueue call = %#v", got)
	}

	duplicate, duplicateRun, err := manager.Deliver(ctx, created.Endpoint.ID, created.Token, "delivery-1", "push", payload)
	if err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	if duplicate.Created || duplicate.Delivery.ID != first.Delivery.ID || duplicateRun != nil {
		t.Fatalf("duplicate delivery = %#v, run = %#v", duplicate, duplicateRun)
	}
	if len(enqueuer.calls) != 1 {
		t.Fatalf("duplicate delivery enqueued %d times, want 1", len(enqueuer.calls))
	}
}

func TestManagerRejectsInvalidAndDisabledEndpointTokens(t *testing.T) {
	ctx := context.Background()
	database, project, workflow := webhookTestFixture(t)
	enqueuer := &recordingEnqueuer{}
	manager, err := NewManager(database, enqueuer)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	created, err := manager.Create(ctx, project.ID, "generic", "generic", workflow.ID, "refs/heads/main")
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}

	if _, _, err := manager.Deliver(ctx, created.Endpoint.ID, "not-the-token", "invalid-token", "push", []byte(`{}`)); err == nil {
		t.Fatal("invalid token was accepted")
	}
	if len(enqueuer.calls) != 0 {
		t.Fatalf("invalid token enqueued %d runs", len(enqueuer.calls))
	}

	disabled, err := database.UpdateWebhookEndpoint(ctx, created.Endpoint.ID, store.UpdateWebhookEndpointParams{
		Provider: created.Endpoint.Provider, TokenHash: created.Endpoint.TokenHash, Metadata: created.Endpoint.Metadata, Enabled: false,
	})
	if err != nil {
		t.Fatalf("disable endpoint: %v", err)
	}
	if disabled.Enabled {
		t.Fatal("endpoint remained enabled")
	}
	if _, _, err := manager.Deliver(ctx, created.Endpoint.ID, created.Token, "disabled", "push", []byte(`{}`)); err == nil {
		t.Fatal("disabled endpoint was accepted")
	}
	if len(enqueuer.calls) != 0 {
		t.Fatalf("disabled endpoint enqueued %d runs", len(enqueuer.calls))
	}
}

func TestManagerMapsGitLabPayloadAndPersistsFailure(t *testing.T) {
	ctx := context.Background()
	database, project, workflow := webhookTestFixture(t)
	enqueueErr := errors.New("queue unavailable")
	enqueuer := &recordingEnqueuer{err: enqueueErr}
	manager, err := NewManager(database, enqueuer)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	created, err := manager.Create(ctx, project.ID, "gitlab-push", "gitlab", workflow.ID, "refs/heads/main")
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}

	payload := []byte(`{"ref":"refs/heads/release","after":"ignored-after","checkout_sha":"gitlab-sha"}`)
	recorded, run, err := manager.Deliver(ctx, created.Endpoint.ID, created.Token, "gitlab-1", "push", payload)
	if !errors.Is(err, enqueueErr) {
		t.Fatalf("delivery error = %v, want wrapped %v", err, enqueueErr)
	}
	if run != nil || !recorded.Created {
		t.Fatalf("failed delivery = %#v, run = %#v", recorded, run)
	}
	if len(enqueuer.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(enqueuer.calls))
	}
	if got := enqueuer.calls[0]; got.ref != "refs/heads/release" || got.commitSHA != "gitlab-sha" {
		t.Errorf("gitlab enqueue call = %#v", got)
	}

	deliveries, err := database.ListWebhookDeliveries(ctx, created.Endpoint.ID)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(deliveries) != 1 || deliveries[0].Status != store.WebhookDeliveryFailed || deliveries[0].ErrorMessage == nil || *deliveries[0].ErrorMessage != enqueueErr.Error() || deliveries[0].ProcessedAt == nil {
		t.Fatalf("failed delivery persistence = %#v", deliveries)
	}
}

type recordingEnqueuer struct {
	run   store.Run
	err   error
	calls []enqueueCall
}

type enqueueCall struct {
	workflowID string
	ref        string
	commitSHA  string
	trigger    string
}

func (e *recordingEnqueuer) EnqueueTriggered(_ context.Context, workflowID, ref, commitSHA, trigger string) (store.Run, error) {
	e.calls = append(e.calls, enqueueCall{workflowID: workflowID, ref: ref, commitSHA: commitSHA, trigger: trigger})
	if e.err != nil {
		return store.Run{}, e.err
	}
	return e.run, nil
}

func webhookTestFixture(t *testing.T) (*store.Store, store.Project, store.Workflow) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "webhooks.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close sqlite: %v", err)
		}
	})
	project, err := database.CreateProject(ctx, store.CreateProjectParams{
		Slug: "webhooks", Name: "Webhook tests", SourceType: "local", DefaultBranch: "main", Active: true,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	workflow, err := database.UpsertWorkflow(ctx, store.UpsertWorkflowParams{
		ProjectID: project.ID, Key: "build", Name: "Build", Definition: []byte(`{"jobs":{}}`), Environment: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return database, project, workflow
}
