package secrets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerEncryptsResolvesRotatesAndRestarts(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(base, "gci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	project, err := database.CreateProject(ctx, store.CreateProjectParams{Slug: "alpha", Name: "Alpha", SourceType: "local", DefaultBranch: "main", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(base, "secret.key")
	manager, err := NewManager(database, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := database.UpsertEnvironment(ctx, store.UpsertEnvironmentParams{
		ProjectID: project.ID, Name: "production", DeploymentTier: store.DeploymentTierProduction,
		Protected: true, RequiredApprovals: 1, ConcurrencyMode: store.EnvironmentConcurrencyQueue,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := database.UpsertWorkflow(ctx, store.UpsertWorkflowParams{
		ProjectID: project.ID, Key: "deploy", Name: "Deploy", Definition: json.RawMessage(`{"jobs":{"deploy":{}}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.EnqueueRun(ctx, store.EnqueueRunParams{
		ProjectID: project.ID, WorkflowID: workflow.ID, TriggerType: "manual", SourcePath: base,
		Jobs: []store.EnqueueJob{{Key: "deploy", Name: "Deploy", EnvironmentName: "production", DeploymentTier: "production",
			DependencyKeys: json.RawMessage(`[]`), Steps: []store.EnqueueStep{{Key: "ship", Name: "Ship", Command: "true"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := database.ListDeploymentTargets(ctx, run.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("deployment targets = %#v, %v", targets, err)
	}
	metadata, err := manager.Upsert(ctx, project.ID, "DEPLOY_TOKEN", "first-value")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := database.GetSecretEnvelope(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope.Ciphertext), "first-value") {
		t.Fatal("ciphertext contains plaintext")
	}
	resolved, err := manager.ResolveProject(ctx, project.ID)
	if err != nil || resolved["DEPLOY_TOKEN"] != "first-value" {
		t.Fatalf("resolved = %#v, %v", resolved, err)
	}
	if _, err := manager.Upsert(ctx, project.ID, "DEPLOY_TOKEN", "rotated-value"); err != nil {
		t.Fatal(err)
	}
	environmentMetadata, err := manager.UpsertEnvironment(ctx, environment.ID, "DEPLOY_TOKEN", "production-value")
	if err != nil {
		t.Fatal(err)
	}
	environmentEnvelope, err := database.GetEnvironmentSecretEnvelope(ctx, environmentMetadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(environmentEnvelope.Ciphertext), "production-value") {
		t.Fatal("environment ciphertext contains plaintext")
	}
	if _, err := manager.ResolveForJob(ctx, targets[0].JobID); err == nil {
		t.Fatal("environment secret resolved before approval")
	}
	request, err := database.RequestEnvironmentApproval(ctx, store.RequestEnvironmentApprovalParams{JobID: targets[0].JobID, RequestedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DecideEnvironmentApproval(ctx, store.DecideEnvironmentApprovalParams{
		RequestID: request.ID, Decision: store.EnvironmentApprovalApproved, Actor: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(database, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = restarted.ResolveProject(ctx, project.ID)
	if err != nil || resolved["DEPLOY_TOKEN"] != "rotated-value" {
		t.Fatalf("restarted = %#v, %v", resolved, err)
	}
	resolved, err = restarted.ResolveForJob(ctx, targets[0].JobID)
	if err != nil || resolved["DEPLOY_TOKEN"] != "production-value" {
		t.Fatalf("environment-resolved = %#v, %v", resolved, err)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v, %v", info.Mode(), err)
	}
}

func TestManagerRejectsInvalidNamesAndWeakKeyPermissions(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(base, "gci.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	manager, err := NewManager(database, filepath.Join(base, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Upsert(ctx, "project", "bad-name", "value"); err == nil {
		t.Fatal("invalid name accepted")
	}
	weak := filepath.Join(base, "weak.key")
	if err := os.WriteFile(weak, make([]byte, 32), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(database, weak); err == nil {
		t.Fatal("weak key permissions accepted")
	}
}
