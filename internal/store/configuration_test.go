package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConfigurationSecretsPersistAndNeverMarshalEnvelope(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "configuration.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, err := first.CreateProject(ctx, testProjectParams("secret-config"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.UpsertSecret(ctx, UpsertSecretParams{ProjectID: project.ID, Name: "DEPLOY_TOKEN", EncryptionAlgorithm: "AES-256-GCM", Nonce: []byte{1, 2, 3}, Ciphertext: []byte("opaque-ciphertext")})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	items, err := second.ListSecrets(ctx, project.ID)
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("list secrets = %#v, %v", items, err)
	}
	envelope, err := second.GetSecretEnvelope(ctx, created.ID)
	if err != nil || string(envelope.Ciphertext) != "opaque-ciphertext" {
		t.Fatalf("envelope = %#v, %v", envelope, err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil || string(encoded) == "" || string(encoded) == "null" || contains(string(encoded), "opaque-ciphertext") {
		t.Fatalf("secret JSON exposure: %s (%v)", encoded, err)
	}
	if err := second.DeleteSecret(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	_, err = second.GetSecret(ctx, created.ID)
	if !errors.Is(err, &ErrNotFound{}) {
		t.Fatalf("deleted secret error = %v", err)
	}
}

func TestConfigurationSchedulesAreValidatedPersistedAndClaimedOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedules.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	project, err := store.CreateProject(ctx, testProjectParams("schedule-config"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := store.UpsertWorkflow(ctx, UpsertWorkflowParams{ProjectID: project.ID, Key: "build", Name: "Build", Definition: json.RawMessage(`{"jobs":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	due := nowUTC().Add(-time.Minute)
	for _, cron := range []string{"* * * * *", "*/5 * * * *"} {
		if _, err := store.CreateWorkflowSchedule(ctx, CreateWorkflowScheduleParams{ProjectID: project.ID, WorkflowID: workflow.ID, Cron: cron, Timezone: "Europe/Paris", Enabled: true, NextRunAt: &due}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateWorkflowSchedule(ctx, CreateWorkflowScheduleParams{ProjectID: project.ID, WorkflowID: workflow.ID, Cron: "* * * *", Timezone: "UTC", NextRunAt: &due}); err == nil {
		t.Fatal("invalid cron accepted")
	}
	var wg sync.WaitGroup
	claims := make(chan []ScheduleClaim, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.ClaimDueWorkflowSchedules(ctx, nowUTC(), 2)
			claims <- got
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	total := 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for got := range claims {
		total += len(got)
	}
	if total != 2 {
		t.Fatalf("claimed = %d, want 2", total)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.ClaimDueWorkflowSchedules(ctx, nowUTC(), 10)
	if err != nil || len(again) != 0 {
		t.Fatalf("claims after restart = %#v, %v", again, err)
	}
}

func TestConfigurationWebhookIdempotencyAndDeployments(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	project, workflow := createExecutionProjectAndWorkflow(t, ctx, store, "configuration-deploy")
	run, err := store.EnqueueRun(ctx, EnqueueRunParams{ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", SourcePath: "/srv/configuration-deploy", Jobs: []EnqueueJob{{Key: "deploy", Name: "Deploy", DependencyKeys: json.RawMessage(`[]`), Steps: []EnqueueStep{{Key: "release", Name: "Release", Command: "true"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateWebhookEndpoint(ctx, CreateWebhookEndpointParams{ProjectID: project.ID, Name: "github", Provider: "github", TokenHash: []byte{5, 6}, Metadata: json.RawMessage(`{"hook":"push"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	params := RecordWebhookDeliveryParams{EndpointID: endpoint.ID, ProviderDeliveryID: "delivery-1", EventType: "push", PayloadSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Status: WebhookDeliveryReceived}
	first, err := store.RecordWebhookDelivery(ctx, params)
	if err != nil || !first.Created {
		t.Fatalf("first delivery = %#v, %v", first, err)
	}
	second, err := store.RecordWebhookDelivery(ctx, params)
	if err != nil || second.Created || second.Delivery.ID != first.Delivery.ID {
		t.Fatalf("duplicate delivery = %#v, %v", second, err)
	}
	deployment, err := store.CreateDeployment(ctx, CreateDeploymentParams{ProjectID: project.ID, RunID: run.ID, Environment: "production", Status: StatusQueued})
	if err != nil || len(deployment.History) != 1 {
		t.Fatalf("create deployment = %#v, %v", deployment, err)
	}
	deployment, err = store.TransitionDeployment(ctx, deployment.ID, StatusRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	deployment, err = store.TransitionDeployment(ctx, deployment.ID, StatusSucceeded, configurationStringPointer("released"))
	if err != nil || len(deployment.History) != 3 || deployment.FinishedAt == nil {
		t.Fatalf("transition deployment = %#v, %v", deployment, err)
	}
	_, err = store.TransitionDeployment(ctx, deployment.ID, StatusRunning, nil)
	if !errors.Is(err, &ErrInvalidStatusTransition{}) {
		t.Fatalf("terminal transition error = %v", err)
	}
}

func configurationStringPointer(value string) *string { return &value }
func contains(value, token string) bool {
	return len(token) > 0 && len(value) >= len(token) && (len(value) > 0 && stringContains(value, token))
}
func stringContains(value, token string) bool {
	for i := 0; i+len(token) <= len(value); i++ {
		if value[i:i+len(token)] == token {
			return true
		}
	}
	return false
}
