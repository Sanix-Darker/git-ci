package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	secretmanager "github.com/sanix-darker/git-ci/internal/secrets"
	"github.com/sanix-darker/git-ci/internal/store"
)

func TestManagerPausesProtectedDeploymentThenResumesWithEnvironmentSecrets(t *testing.T) {
	ctx, database, _, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "deployment-secret")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "deploy.yml"), strings.Join([]string{
		"name: Protected deployment",
		"on: workflow_dispatch",
		"jobs:",
		"  deploy:",
		"    runs-on: ubuntu-latest",
		"    environment: production",
		"    env:",
		"      DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}",
		"    steps:",
		`      - run: printf '%s' "$DEPLOY_TOKEN" > ` + shellTestPath(marker),
	}, "\n"))
	secrets, err := secretmanager.NewManager(database, filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, WithSecretResolver(secrets), WithWorkspaceRoot(filepath.Join(t.TempDir(), "workspaces")))
	if err != nil {
		t.Fatal(err)
	}
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run, err := manager.EnqueueWorkflow(ctx, workflow.ID, "refs/heads/main", managerRepositoryHead(t, root))
	if err != nil {
		t.Fatal(err)
	}
	workspacePath, err := manager.workspaces.SourcePath(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	deploy := jobGraph(t, graph, "deploy")
	targets, err := database.ListDeploymentTargets(ctx, run.ID)
	if err != nil || len(targets) != 1 || targets[0].JobID != deploy.Job.ID {
		t.Fatalf("deployment targets = %#v, %v", targets, err)
	}
	environment, err := database.UpsertEnvironment(ctx, store.UpsertEnvironmentParams{
		ProjectID: project.ID, Name: "production", DeploymentTier: store.DeploymentTierProduction,
		Protected: true, RequiredApprovals: 1, ConcurrencyMode: store.EnvironmentConcurrencyQueue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Upsert(ctx, project.ID, "DEPLOY_TOKEN", "project-value"); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.UpsertEnvironment(ctx, environment.ID, "DEPLOY_TOKEN", "environment-value"); err != nil {
		t.Fatal(err)
	}

	processed, err := manager.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("first ProcessNext() = (%t, %v)", processed, err)
	}
	graph, err = database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusWaiting || jobStatus(t, graph, "deploy") != store.StatusWaiting {
		t.Fatalf("waiting graph = %#v, %v", graph, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected command ran before approval: %v", err)
	}
	if info, err := os.Stat(workspacePath); err != nil || !info.IsDir() {
		t.Fatalf("waiting workspace = %#v, %v", info, err)
	}
	request, err := database.RequestEnvironmentApproval(ctx, store.RequestEnvironmentApprovalParams{JobID: deploy.Job.ID, RequestedBy: "test"})
	if err != nil || request.Status != store.EnvironmentApprovalPending {
		t.Fatalf("approval request = %#v, %v", request, err)
	}
	if _, err := database.DecideEnvironmentApproval(ctx, store.DecideEnvironmentApprovalParams{
		RequestID: request.ID, Decision: store.EnvironmentApprovalApproved, Actor: "operator", Reason: "approved in test",
	}); err != nil {
		t.Fatal(err)
	}

	processed, err = manager.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("resumed ProcessNext() = (%t, %v)", processed, err)
	}
	graph, err = database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded || jobStatus(t, graph, "deploy") != store.StatusSucceeded {
		t.Fatalf("completed graph = %#v, %v", graph, err)
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "environment-value" {
		t.Fatalf("deployment secret = %q, %v", contents, err)
	}
	if _, err := os.Stat(workspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal workspace still exists: %v", err)
	}
	waits, err := database.ListJobWaits(ctx)
	if err != nil || len(waits) != 0 {
		t.Fatalf("remaining waits = %#v, %v", waits, err)
	}
	deployments, err := database.ListDeployments(ctx, project.ID)
	if err != nil || len(deployments) != 1 || deployments[0].Status != store.StatusSucceeded || deployments[0].Environment != "production" {
		t.Fatalf("deployments = %#v, %v", deployments, err)
	}
}

func TestManagerRejectsProtectedDeploymentFromDisallowedRef(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	marker := filepath.Join(t.TempDir(), "disallowed-ran")
	writeManagerWorkflow(t, filepath.Join(root, ".github", "workflows", "deploy.yml"), strings.Join([]string{
		"name: Ref protected deployment",
		"on: workflow_dispatch",
		"jobs:",
		"  deploy:",
		"    runs-on: ubuntu-latest",
		"    environment: production",
		"    steps:",
		"      - run: printf unsafe > " + shellTestPath(marker),
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run, err := manager.EnqueueWorkflow(ctx, workflow.ID, "refs/heads/feature", managerRepositoryHead(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.UpsertEnvironment(ctx, store.UpsertEnvironmentParams{
		ProjectID: project.ID, Name: "production", DeploymentTier: store.DeploymentTierProduction,
		Protected: true, AllowedRefs: []string{"refs/heads/main"}, ConcurrencyMode: store.EnvironmentConcurrencyQueue,
	}); err != nil {
		t.Fatal(err)
	}
	processed, err := manager.ProcessNext(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessNext() = (%t, %v)", processed, err)
	}
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusFailed || jobStatus(t, graph, "deploy") != store.StatusFailed {
		t.Fatalf("rejected graph = %#v, %v", graph, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disallowed deployment command ran: %v", err)
	}
	deployments, err := database.ListDeployments(ctx, project.ID)
	if err != nil || len(deployments) != 1 || deployments[0].Status != store.StatusFailed {
		t.Fatalf("rejected deployments = %#v, %v", deployments, err)
	}
}
