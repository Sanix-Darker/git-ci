package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
)

const (
	defaultPollInterval       = 750 * time.Millisecond
	runWorkerLeaseTTL         = 15 * time.Second
	runWorkerHeartbeat        = 5 * time.Second
	environmentLeaseTTL       = 30 * time.Second
	environmentLeaseHeartbeat = 10 * time.Second
)

// Manager owns workflow synchronization, immutable run creation, and one
// local execution worker. The registered project path is snapshotted before a
// run enters the queue; workers never accept arbitrary paths or commands from
// HTTP requests.
type Manager struct {
	store         *store.Store
	secrets       SecretResolver
	workerID      string
	pollInterval  time.Duration
	wake          chan struct{}
	workspaceRoot string
	workspaces    *workspaceManager
}

type SecretResolver interface {
	ResolveProject(context.Context, string) (map[string]string, error)
}

type environmentSecretResolver interface {
	ResolveForJob(context.Context, string) (map[string]string, error)
}

type Option func(*Manager)

func WithSecretResolver(resolver SecretResolver) Option {
	return func(manager *Manager) { manager.secrets = resolver }
}

func WithWorkspaceRoot(root string) Option {
	return func(manager *Manager) { manager.workspaceRoot = root }
}

func NewManager(database *store.Store, options ...Option) (*Manager, error) {
	if database == nil {
		return nil, errors.New("execution: store is required")
	}
	manager := &Manager{
		store:        database,
		workerID:     fmt.Sprintf("local-%d", os.Getpid()),
		pollInterval: defaultPollInterval,
		wake:         make(chan struct{}, 1),
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	workspaces, err := newWorkspaceManager(manager.workspaceRoot)
	if err != nil {
		return nil, err
	}
	manager.workspaces = workspaces
	return manager, nil
}

// SyncProject discovers provider files beneath one registered project and
// makes that exact set active. Removed definitions stay stored but inactive so
// historical runs retain their workflow foreign key.
func (m *Manager) SyncProject(ctx context.Context, projectID string) ([]store.Workflow, error) {
	project, err := m.store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	definitions, err := DiscoverProject(project)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		definitionJSON, marshalErr := json.Marshal(definition)
		if marshalErr != nil {
			return nil, fmt.Errorf("execution: encode workflow %q: %w", definition.Key, marshalErr)
		}
		environmentJSON, marshalErr := json.Marshal(definition.Environment)
		if marshalErr != nil {
			return nil, fmt.Errorf("execution: encode workflow environment %q: %w", definition.Key, marshalErr)
		}
		if _, err := m.store.UpsertWorkflow(ctx, store.UpsertWorkflowParams{
			ProjectID:   project.ID,
			Key:         definition.Key,
			Name:        definition.Name,
			Definition:  definitionJSON,
			Environment: environmentJSON,
		}); err != nil {
			return nil, err
		}
		keys = append(keys, definition.Key)
	}
	if err := m.store.SetProjectWorkflowSet(ctx, project.ID, keys); err != nil {
		return nil, err
	}
	return m.store.ListWorkflows(ctx, project.ID)
}

// EnqueueWorkflow creates a complete immutable run graph from the currently
// active stored workflow definition.
func (m *Manager) EnqueueWorkflow(ctx context.Context, workflowID, ref, commitSHA string) (store.Run, error) {
	return m.EnqueueTriggered(ctx, workflowID, ref, commitSHA, "manual")
}

func (m *Manager) EnqueueTriggered(ctx context.Context, workflowID, ref, commitSHA, trigger string) (store.Run, error) {
	workflow, err := m.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return store.Run{}, err
	}
	if !workflow.Active {
		return store.Run{}, errors.New("execution: workflow is no longer active")
	}
	project, err := m.store.GetProject(ctx, workflow.ProjectID)
	if err != nil {
		return store.Run{}, err
	}
	if project.CanonicalPath == nil || strings.TrimSpace(*project.CanonicalPath) == "" {
		return store.Run{}, errors.New("execution: workflow project has no local checkout")
	}
	var definition Definition
	if err := json.Unmarshal(workflow.Definition, &definition); err != nil {
		return store.Run{}, fmt.Errorf("execution: decode workflow definition: %w", err)
	}
	if ref == "" {
		ref = "refs/heads/" + project.DefaultBranch
	}
	resolvedCommit, err := resolveGitCommit(ctx, *project.CanonicalPath, ref, commitSHA)
	if err != nil {
		return store.Run{}, err
	}
	jobs := make([]store.EnqueueJob, 0, len(definition.Jobs))
	for _, job := range definition.Jobs {
		dependencies := uniqueStrings(append(append([]string{}, job.Needs...), job.Requires...))
		dependencyJSON, err := json.Marshal(dependencies)
		if err != nil {
			return store.Run{}, fmt.Errorf("execution: encode dependencies for %q: %w", job.Key, err)
		}
		jobEnvironment, err := json.Marshal(job.Environment)
		if err != nil {
			return store.Run{}, fmt.Errorf("execution: encode environment for %q: %w", job.Key, err)
		}
		steps := make([]store.EnqueueStep, 0, len(job.Steps))
		for _, step := range job.Steps {
			stepEnvironment, err := json.Marshal(step.Environment)
			if err != nil {
				return store.Run{}, fmt.Errorf("execution: encode environment for step %q: %w", step.Key, err)
			}
			steps = append(steps, store.EnqueueStep{
				Key:              step.Key,
				Name:             step.Name,
				Command:          step.Command,
				Action:           step.Action,
				Environment:      stepEnvironment,
				WorkingDirectory: step.WorkingDirectory,
				TimeoutMinutes:   step.TimeoutMinutes,
				Shell:            step.Shell,
				AllowFailure:     step.AllowFailure,
			})
		}
		jobs = append(jobs, store.EnqueueJob{
			Key:             job.Key,
			Name:            job.Name,
			Runner:          job.RunnerHint,
			EnvironmentName: job.EnvironmentName,
			DeploymentTier:  job.DeploymentTier,
			Environment:     jobEnvironment,
			DependencyKeys:  dependencyJSON,
			AllowFailure:    job.AllowFailure,
			TimeoutMinutes:  job.TimeoutMinutes,
			RollbackCommand: job.RollbackCommand,
			VerifyCommand:   job.VerifyCommand,
			Steps:           steps,
		})
	}
	run, err := m.store.EnqueueRun(ctx, store.EnqueueRunParams{
		ProjectID:   project.ID,
		WorkflowID:  workflow.ID,
		TriggerType: strings.TrimSpace(trigger),
		Ref:         strings.TrimSpace(ref),
		CommitSHA:   resolvedCommit,
		SourcePath:  *project.CanonicalPath,
		Environment: workflow.Environment,
		Jobs:        jobs,
	})
	if err != nil {
		return store.Run{}, err
	}
	m.Notify()
	return run, nil
}

func (m *Manager) EnqueueDeploymentRollback(ctx context.Context, params store.EnqueueRollbackParams) (store.Run, error) {
	if strings.TrimSpace(params.Actor) != "" && strings.TrimSpace(params.IdempotencyKey) != "" {
		if _, err := m.store.GetRunLineageByIdempotency(ctx, params.Actor, params.IdempotencyKey); err == nil {
			return m.store.EnqueueDeploymentRollback(ctx, params)
		} else {
			var notFound *store.ErrNotFound
			if !errors.As(err, &notFound) {
				return store.Run{}, err
			}
		}
	}
	target, err := m.store.GetDeployment(ctx, params.TargetDeploymentID)
	if err != nil {
		return store.Run{}, err
	}
	graph, err := m.store.GetRunGraph(ctx, target.RunID)
	if err != nil {
		return store.Run{}, err
	}
	if graph.Run.CommitSHA == nil {
		return store.Run{}, &store.ErrRollbackEligibility{Code: "target_commit_missing", Message: "rollback target has no pinned commit"}
	}
	resolved, err := resolveGitCommit(ctx, graph.Run.SourcePath, "", *graph.Run.CommitSHA)
	if err != nil || resolved != *graph.Run.CommitSHA {
		return store.Run{}, &store.ErrRollbackEligibility{Code: "target_commit_unavailable", Message: "rollback target commit is unavailable in the registered repository"}
	}
	run, err := m.store.EnqueueDeploymentRollback(ctx, params)
	if err != nil {
		return store.Run{}, err
	}
	m.Notify()
	return run, nil
}

func (m *Manager) EnqueueRunReplay(ctx context.Context, params store.EnqueueReplayParams) (store.Run, error) {
	if strings.TrimSpace(params.Actor) != "" && strings.TrimSpace(params.IdempotencyKey) != "" {
		if _, err := m.store.GetRunLineageByIdempotency(ctx, params.Actor, params.IdempotencyKey); err == nil {
			return m.store.EnqueueRunReplay(ctx, params)
		} else {
			var notFound *store.ErrNotFound
			if !errors.As(err, &notFound) {
				return store.Run{}, err
			}
		}
	}
	source, err := m.store.GetReplaySourceRun(ctx, params)
	if err != nil {
		return store.Run{}, err
	}
	if source.CommitSHA == nil {
		return store.Run{}, &store.ErrReplayEligibility{Code: "source_commit_missing", Message: "replay source has no pinned commit"}
	}
	resolved, err := resolveGitCommit(ctx, source.SourcePath, "", *source.CommitSHA)
	if err != nil || resolved != *source.CommitSHA {
		return store.Run{}, &store.ErrReplayEligibility{Code: "source_commit_unavailable", Message: "replay source commit is unavailable in the registered repository"}
	}
	run, err := m.store.EnqueueRunReplay(ctx, params)
	if err != nil {
		return store.Run{}, err
	}
	m.Notify()
	return run, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Run recovers expired worker leases, then drains queued runs serially.
func (m *Manager) Run(ctx context.Context) error {
	if _, err := m.store.RecoverExpiredRunWorkers(ctx, time.Now().UTC(), time.Now().UTC().Add(-runWorkerLeaseTTL)); err != nil {
		return fmt.Errorf("execution: recover interrupted runs: %w", err)
	}
	if err := m.workspaces.CleanupRecovered(ctx, m.store); err != nil {
		return fmt.Errorf("execution: recover run workspaces: %w", err)
	}
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		processed, err := m.ProcessNext(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

// ProcessNext claims and executes at most one queued run. It is public to make
// deterministic service integration tests possible without timing sleeps.
func (m *Manager) ProcessNext(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	if _, err := m.store.RecoverExpiredRunWorkers(ctx, now, now.Add(-runWorkerLeaseTTL)); err != nil {
		return false, fmt.Errorf("execution: recover expired workers: %w", err)
	}
	if err := m.resumeWaitingJobs(ctx, now); err != nil {
		return false, fmt.Errorf("execution: resume waiting jobs: %w", err)
	}
	run, err := m.store.ClaimNextQueuedRun(ctx, m.workerID)
	if err != nil {
		return false, fmt.Errorf("execution: claim run: %w", err)
	}
	if run == nil {
		return false, nil
	}
	if err := m.store.HeartbeatRunWorker(ctx, run.ID, m.workerID, now, runWorkerLeaseTTL); err != nil {
		return true, fmt.Errorf("execution: establish worker lease: %w", err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go m.heartbeatRunWorker(workerCtx, run.ID, stopWorker, workerDone)
	workspace, workspaceErr := m.workspaces.Acquire(workerCtx, *run)
	if workspaceErr != nil {
		err = m.failWorkspaceSetup(workerCtx, *run, workspaceErr)
	} else {
		err = m.executeRun(workerCtx, *run, workspace.SourcePath)
	}
	stopWorker()
	<-workerDone
	if releaseErr := m.store.ReleaseRunWorker(context.WithoutCancel(ctx), run.ID, m.workerID); err == nil && releaseErr != nil {
		err = releaseErr
	}
	if cleanupErr := m.cleanupTerminalWorkspace(context.WithoutCancel(ctx), run.ID); err == nil && cleanupErr != nil {
		err = cleanupErr
	}
	if err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) executeRun(ctx context.Context, run store.Run, workspacePath string) error {
	graph, err := m.store.GetRunGraph(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("execution: load claimed run: %w", err)
	}
	statuses := make(map[string]store.Status, len(graph.Jobs))
	allowedFailure := make(map[string]bool, len(graph.Jobs))
	secretValues := map[string]string{}
	if m.secrets != nil {
		secretValues, err = m.secrets.ResolveProject(ctx, run.ProjectID)
		if err != nil {
			for _, item := range graph.Jobs {
				if skipErr := m.skipJob(ctx, item); skipErr != nil {
					return skipErr
				}
			}
			_, transitionErr := m.store.TransitionRun(ctx, run.ID, store.StatusFailed)
			return transitionErr
		}
	}
	for _, item := range graph.Jobs {
		key := pointerValue(item.Job.Key)
		allowedFailure[key] = item.Job.AllowFailure
		if isTerminalExecutionStatus(item.Job.Status) {
			statuses[key] = item.Job.Status
			continue
		}
		dependencies := decodeStringList(item.Job.DependencyKeys)
		if !dependenciesSatisfied(dependencies, statuses, allowedFailure) {
			if err := m.skipJob(ctx, item); err != nil {
				return err
			}
			statuses[key] = store.StatusSkipped
			allowedFailure[key] = item.Job.AllowFailure
			continue
		}
		cancelled, err := m.isCancelled(ctx, run.ID)
		if err != nil {
			return err
		}
		if cancelled {
			if err := m.cancelRemaining(ctx, graph, statuses); err != nil {
				return err
			}
			_, err = m.store.TransitionRun(ctx, run.ID, store.StatusCancelled)
			return err
		}
		jobSecrets := secretValues
		preparation, err := m.prepareDeploymentJob(ctx, graph.Run, item, secretValues)
		if err != nil {
			return err
		}
		if preparation.Paused {
			return nil
		}
		if preparation.Failed {
			statuses[key] = store.StatusFailed
			continue
		}
		if preparation.Secrets != nil {
			jobSecrets = preparation.Secrets
		}
		jobCtx := ctx
		stopEnvironment := func() {}
		environmentDone := make(chan struct{})
		if preparation.Lease != nil {
			jobCtx, stopEnvironment = context.WithCancel(ctx)
			go m.heartbeatEnvironment(jobCtx, item.Job.ID, stopEnvironment, environmentDone)
		}
		status, err := m.executeJob(jobCtx, graph.Run, item, workspacePath, jobSecrets)
		if preparation.Lease != nil {
			stopEnvironment()
			<-environmentDone
			released, releaseErr := m.store.ReleaseEnvironmentLease(context.WithoutCancel(ctx), preparation.Lease.EnvironmentID, item.Job.ID, m.workerID)
			if err == nil && releaseErr != nil {
				err = fmt.Errorf("execution: release environment lease: %w", releaseErr)
			} else if err == nil && !released {
				err = errors.New("execution: environment lease ownership was lost")
			}
		}
		if err != nil {
			return err
		}
		if preparation.DeploymentID != "" {
			reason := "workflow job completed"
			if _, transitionErr := m.store.TransitionDeployment(ctx, preparation.DeploymentID, status, &reason); transitionErr != nil {
				return fmt.Errorf("execution: finish deployment: %w", transitionErr)
			}
		}
		statuses[key] = status
		allowedFailure[key] = item.Job.AllowFailure
		if status == store.StatusCancelled {
			if err := m.cancelRemaining(ctx, graph, statuses); err != nil {
				return err
			}
			_, err = m.store.TransitionRun(ctx, run.ID, store.StatusCancelled)
			return err
		}
	}
	result := store.StatusSucceeded
	for key, status := range statuses {
		if status == store.StatusFailed && !allowedFailure[key] {
			result = store.StatusFailed
			break
		}
	}
	_, err = m.store.TransitionRun(ctx, run.ID, result)
	if err != nil {
		return fmt.Errorf("execution: finish run: %w", err)
	}
	return nil
}

func (m *Manager) failWorkspaceSetup(ctx context.Context, run store.Run, cause error) error {
	graph, err := m.store.GetRunGraph(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("execution: load run after workspace failure: %w", err)
	}
	for index, item := range graph.Jobs {
		if index == 0 && len(item.Steps) > 0 {
			if err := m.appendSystem(ctx, item.Steps[0].ID, "workspace setup failed: "+cause.Error()); err != nil {
				return err
			}
		}
		if err := m.skipJob(ctx, item); err != nil {
			return err
		}
	}
	if _, err := m.store.TransitionRun(ctx, run.ID, store.StatusFailed); err != nil {
		return fmt.Errorf("execution: fail run after workspace setup: %w", err)
	}
	return nil
}

func (m *Manager) cleanupTerminalWorkspace(ctx context.Context, runID string) error {
	graph, err := m.store.GetRunGraph(ctx, runID)
	if err != nil {
		return err
	}
	if !isTerminalExecutionStatus(graph.Run.Status) {
		return nil
	}
	return m.workspaces.Cleanup(runID)
}

type deploymentPreparation struct {
	Paused       bool
	Failed       bool
	Secrets      map[string]string
	Lease        *store.EnvironmentLease
	DeploymentID string
}

func (m *Manager) prepareDeploymentJob(ctx context.Context, run store.Run, item store.JobGraph, projectSecrets map[string]string) (deploymentPreparation, error) {
	_, err := m.store.GetDeploymentTargetForJob(ctx, item.Job.ID)
	if err != nil {
		var notFound *store.ErrNotFound
		if errors.As(err, &notFound) {
			return deploymentPreparation{Secrets: projectSecrets}, nil
		}
		return deploymentPreparation{}, fmt.Errorf("execution: read deployment target: %w", err)
	}
	environment, err := m.store.EnsureEnvironmentForJob(ctx, item.Job.ID)
	if err != nil {
		return deploymentPreparation{}, fmt.Errorf("execution: ensure environment: %w", err)
	}
	deployment, err := m.store.EnsureDeploymentForJob(ctx, item.Job.ID)
	if err != nil {
		return deploymentPreparation{}, fmt.Errorf("execution: ensure deployment: %w", err)
	}
	if environment.Protected && environment.RequiredApprovals > 0 {
		if _, err := m.store.RequestEnvironmentApproval(ctx, store.RequestEnvironmentApprovalParams{JobID: item.Job.ID, RequestedBy: m.workerID}); err != nil {
			return deploymentPreparation{}, fmt.Errorf("execution: request environment approval: %w", err)
		}
	}
	access, err := m.store.EvaluateEnvironmentAccess(ctx, item.Job.ID, time.Now().UTC())
	if err != nil {
		return deploymentPreparation{}, fmt.Errorf("execution: evaluate environment protection: %w", err)
	}
	if !access.Ready {
		switch access.Reason {
		case "approval_rejected", "approval_cancelled", "ref_not_allowed":
			return m.failProtectedJob(ctx, item, deployment.ID, access.Reason)
		default:
			reason := store.JobWaitApproval
			if access.Reason == "wait_timer" {
				reason = store.JobWaitTimer
			}
			if _, err := m.store.PauseJob(ctx, store.PauseJobParams{
				RunID: run.ID, JobID: item.Job.ID, Reason: reason, Detail: access.Reason, AvailableAt: access.WaitUntil,
			}); err != nil {
				return deploymentPreparation{}, fmt.Errorf("execution: pause protected job: %w", err)
			}
			return deploymentPreparation{Paused: true, DeploymentID: deployment.ID}, nil
		}
	}
	lease, err := m.store.AcquireEnvironmentLease(ctx, store.AcquireEnvironmentLeaseParams{
		JobID: item.Job.ID, OwnerID: m.workerID, TTL: environmentLeaseTTL, Now: time.Now().UTC(),
	})
	if err != nil {
		return deploymentPreparation{}, fmt.Errorf("execution: acquire environment lease: %w", err)
	}
	if !lease.Acquired {
		if environment.ConcurrencyMode == store.EnvironmentConcurrencyCancelInProgress && lease.Lease.RunID != run.ID {
			if _, err := m.store.RequestRunCancellation(ctx, lease.Lease.RunID); err != nil {
				return deploymentPreparation{}, fmt.Errorf("execution: cancel superseded deployment: %w", err)
			}
		}
		if _, err := m.store.PauseJob(ctx, store.PauseJobParams{
			RunID: run.ID, JobID: item.Job.ID, Reason: store.JobWaitConcurrency, Detail: "environment lease is held",
		}); err != nil {
			return deploymentPreparation{}, fmt.Errorf("execution: pause concurrent deployment: %w", err)
		}
		return deploymentPreparation{Paused: true, DeploymentID: deployment.ID}, nil
	}
	jobSecrets := projectSecrets
	if resolver, ok := m.secrets.(environmentSecretResolver); ok {
		jobSecrets, err = resolver.ResolveForJob(ctx, item.Job.ID)
		if err != nil {
			_, _ = m.store.ReleaseEnvironmentLease(context.WithoutCancel(ctx), environment.ID, item.Job.ID, m.workerID)
			return deploymentPreparation{}, fmt.Errorf("execution: resolve environment secrets: %w", err)
		}
	}
	if deployment.Status == store.StatusQueued {
		transitionReason := "environment protection satisfied"
		if _, err := m.store.TransitionDeployment(ctx, deployment.ID, store.StatusRunning, &transitionReason); err != nil {
			_, _ = m.store.ReleaseEnvironmentLease(context.WithoutCancel(ctx), environment.ID, item.Job.ID, m.workerID)
			return deploymentPreparation{}, fmt.Errorf("execution: start deployment: %w", err)
		}
	} else if deployment.Status != store.StatusRunning {
		_, _ = m.store.ReleaseEnvironmentLease(context.WithoutCancel(ctx), environment.ID, item.Job.ID, m.workerID)
		return deploymentPreparation{}, fmt.Errorf("execution: deployment %s cannot resume from %s", deployment.ID, deployment.Status)
	}
	return deploymentPreparation{Secrets: jobSecrets, Lease: &lease.Lease, DeploymentID: deployment.ID}, nil
}

func (m *Manager) failProtectedJob(ctx context.Context, item store.JobGraph, deploymentID, reason string) (deploymentPreparation, error) {
	if err := m.skipSteps(ctx, item.Steps); err != nil {
		return deploymentPreparation{}, err
	}
	if _, err := m.store.TransitionJob(ctx, item.Job.ID, store.StatusFailed); err != nil {
		return deploymentPreparation{}, fmt.Errorf("execution: fail protected job: %w", err)
	}
	if _, err := m.store.TransitionDeployment(ctx, deploymentID, store.StatusFailed, &reason); err != nil {
		return deploymentPreparation{}, fmt.Errorf("execution: fail protected deployment: %w", err)
	}
	return deploymentPreparation{Failed: true, DeploymentID: deploymentID}, nil
}

func (m *Manager) resumeWaitingJobs(ctx context.Context, now time.Time) error {
	waits, err := m.store.ListJobWaits(ctx)
	if err != nil {
		return err
	}
	for _, wait := range waits {
		environment, err := m.store.EnsureEnvironmentForJob(ctx, wait.JobID)
		if err != nil {
			return err
		}
		if environment.Protected && environment.RequiredApprovals > 0 {
			if _, err := m.store.RequestEnvironmentApproval(ctx, store.RequestEnvironmentApprovalParams{JobID: wait.JobID, RequestedBy: m.workerID}); err != nil {
				return err
			}
		}
		access, err := m.store.EvaluateEnvironmentAccess(ctx, wait.JobID, now)
		if err != nil {
			return err
		}
		terminalGate := access.Reason == "approval_rejected" || access.Reason == "approval_cancelled" || access.Reason == "ref_not_allowed"
		if access.Ready || terminalGate || wait.Reason == store.JobWaitConcurrency {
			if err := m.store.ResumeJob(ctx, wait.RunID, wait.JobID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) heartbeatRunWorker(ctx context.Context, runID string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(runWorkerHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.store.HeartbeatRunWorker(ctx, runID, m.workerID, time.Now().UTC(), runWorkerLeaseTTL); err != nil {
				cancel()
				return
			}
		}
	}
}

func (m *Manager) heartbeatEnvironment(ctx context.Context, jobID string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(environmentLeaseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := m.store.AcquireEnvironmentLease(ctx, store.AcquireEnvironmentLeaseParams{
				JobID: jobID, OwnerID: m.workerID, TTL: environmentLeaseTTL, Now: time.Now().UTC(),
			})
			if err != nil || !result.Acquired {
				cancel()
				return
			}
		}
	}
}

func isTerminalExecutionStatus(status store.Status) bool {
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusCancelled, store.StatusSkipped:
		return true
	default:
		return false
	}
}

func (m *Manager) executeJob(ctx context.Context, run store.Run, item store.JobGraph, workspacePath string, secretValues map[string]string) (store.Status, error) {
	if _, err := m.store.TransitionJob(ctx, item.Job.ID, store.StatusRunning); err != nil {
		return "", fmt.Errorf("execution: start job: %w", err)
	}
	jobCtx := ctx
	jobCancel := func() {}
	if item.Job.TimeoutMinutes > 0 {
		jobCtx, jobCancel = context.WithTimeout(ctx, time.Duration(item.Job.TimeoutMinutes)*time.Minute)
	}
	defer jobCancel()
	jobFailed := false
	for index, step := range item.Steps {
		cancelled, err := m.isCancelled(jobCtx, run.ID)
		if err != nil {
			return "", err
		}
		if cancelled || jobCtx.Err() != nil {
			if _, err := m.store.TransitionStep(ctx, step.ID, store.StatusCancelled); err != nil {
				return "", err
			}
			if err := m.skipSteps(ctx, item.Steps[index+1:]); err != nil {
				return "", err
			}
			if _, err := m.store.TransitionJob(ctx, item.Job.ID, store.StatusCancelled); err != nil {
				return "", err
			}
			return store.StatusCancelled, nil
		}
		if _, err := m.store.TransitionStep(ctx, step.ID, store.StatusRunning); err != nil {
			return "", fmt.Errorf("execution: start step: %w", err)
		}
		err = m.executeStep(jobCtx, run, item.Job, step, workspacePath, secretValues)
		if err == nil {
			if _, transitionErr := m.store.TransitionStep(ctx, step.ID, store.StatusSucceeded); transitionErr != nil {
				return "", transitionErr
			}
			continue
		}
		cancelled, cancelErr := m.isCancelled(ctx, run.ID)
		if cancelErr != nil {
			return "", cancelErr
		}
		if cancelled {
			if _, transitionErr := m.store.TransitionStep(ctx, step.ID, store.StatusCancelled); transitionErr != nil {
				return "", transitionErr
			}
			if err := m.skipSteps(ctx, item.Steps[index+1:]); err != nil {
				return "", err
			}
			if _, transitionErr := m.store.TransitionJob(ctx, item.Job.ID, store.StatusCancelled); transitionErr != nil {
				return "", transitionErr
			}
			return store.StatusCancelled, nil
		}
		if _, transitionErr := m.store.TransitionStep(ctx, step.ID, store.StatusFailed); transitionErr != nil {
			return "", transitionErr
		}
		_ = m.appendSystem(ctx, step.ID, "step failed: "+err.Error())
		if step.AllowFailure {
			continue
		}
		jobFailed = true
		if err := m.skipSteps(ctx, item.Steps[index+1:]); err != nil {
			return "", err
		}
		break
	}
	status := store.StatusSucceeded
	if jobFailed {
		status = store.StatusFailed
	}
	if _, err := m.store.TransitionJob(ctx, item.Job.ID, status); err != nil {
		return "", fmt.Errorf("execution: finish job: %w", err)
	}
	return status, nil
}

func (m *Manager) executeStep(ctx context.Context, run store.Run, job store.Job, step store.Step, workspacePath string, secretValues map[string]string) error {
	if step.Action != nil {
		if strings.HasPrefix(*step.Action, "actions/checkout@") {
			return m.appendSystem(ctx, step.ID, "using pinned commit workspace "+pointerValue(run.CommitSHA))
		}
		return fmt.Errorf("unsupported action %q", *step.Action)
	}
	if step.Command == nil {
		return errors.New("step has no command")
	}
	directory, err := containedWorkingDirectory(workspacePath, pointerValue(step.WorkingDirectory))
	if err != nil {
		return err
	}
	var stepCtx context.Context
	var cancel context.CancelFunc
	if step.TimeoutMinutes > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutMinutes)*time.Minute)
	} else {
		stepCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	cancelPollingDone := make(chan struct{})
	go m.pollCancellation(stepCtx, run.ID, cancel, cancelPollingDone)
	defer close(cancelPollingDone)

	commandText := expandSecrets(*step.Command, secretValues)
	shell, shellArgs := shellCommand(pointerValue(step.Shell), commandText)
	command := exec.CommandContext(stepCtx, shell, shellArgs...)
	command.Dir = directory
	command.Env = mergedEnvironment(secretValues, run.Environment, job.Environment, step.Environment, map[string]string{
		"CI":              "true",
		"GCI_RUN_ID":      run.ID,
		"GCI_JOB_ID":      job.ID,
		"GCI_PROJECT_DIR": workspacePath,
	})
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for stream, reader := range map[store.LogStream]io.Reader{
		store.LogStreamStdout: stdout,
		store.LogStreamStderr: stderr,
	} {
		wait.Add(1)
		go func(stream store.LogStream, reader io.Reader) {
			defer wait.Done()
			if err := m.captureLines(stepCtx, step.ID, stream, reader, secretValues); err != nil {
				errorsFound <- err
			}
		}(stream, reader)
	}
	wait.Wait()
	commandErr := command.Wait()
	close(errorsFound)
	for captureErr := range errorsFound {
		if commandErr == nil {
			commandErr = captureErr
		}
	}
	return commandErr
}

func (m *Manager) captureLines(ctx context.Context, stepID string, stream store.LogStream, reader io.Reader, secretValues map[string]string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if _, err := m.store.AppendLogLine(ctx, store.AppendLogLineParams{StepID: stepID, Stream: stream, Message: redactSecrets(scanner.Text(), secretValues)}); err != nil {
			return err
		}
	}
	err := scanner.Err()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (m *Manager) pollCancellation(ctx context.Context, runID string, cancel context.CancelFunc, done <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			state, err := m.store.GetRunCancellation(ctx, runID)
			if err == nil && state.Requested {
				cancel()
				return
			}
		}
	}
}

func (m *Manager) appendSystem(ctx context.Context, stepID, message string) error {
	_, err := m.store.AppendLogLine(ctx, store.AppendLogLineParams{StepID: stepID, Stream: store.LogStreamSystem, Message: message})
	return err
}

func (m *Manager) isCancelled(ctx context.Context, runID string) (bool, error) {
	cancellation, err := m.store.GetRunCancellation(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("execution: read cancellation: %w", err)
	}
	return cancellation.Requested, nil
}

func (m *Manager) skipJob(ctx context.Context, item store.JobGraph) error {
	if isTerminalExecutionStatus(item.Job.Status) {
		return nil
	}
	if err := m.skipSteps(ctx, item.Steps); err != nil {
		return err
	}
	_, err := m.store.TransitionJob(ctx, item.Job.ID, store.StatusSkipped)
	return err
}

func (m *Manager) skipSteps(ctx context.Context, steps []store.Step) error {
	for _, step := range steps {
		if step.Status != store.StatusQueued {
			continue
		}
		if _, err := m.store.TransitionStep(ctx, step.ID, store.StatusSkipped); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) cancelRemaining(ctx context.Context, graph store.RunGraph, statuses map[string]store.Status) error {
	for _, item := range graph.Jobs {
		key := pointerValue(item.Job.Key)
		if _, done := statuses[key]; done || item.Job.Status != store.StatusQueued {
			continue
		}
		if err := m.skipSteps(ctx, item.Steps); err != nil {
			return err
		}
		if _, err := m.store.TransitionJob(ctx, item.Job.ID, store.StatusCancelled); err != nil {
			return err
		}
	}
	return nil
}

func dependenciesSatisfied(dependencies []string, statuses map[string]store.Status, allowed map[string]bool) bool {
	for _, dependency := range dependencies {
		status, exists := statuses[dependency]
		if !exists {
			return false
		}
		if status != store.StatusSucceeded && !(status == store.StatusFailed && allowed[dependency]) {
			return false
		}
	}
	return true
}

func decodeStringList(value json.RawMessage) []string {
	var result []string
	_ = json.Unmarshal(value, &result)
	return result
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func shellCommand(shell, command string) (string, []string) {
	switch strings.TrimSpace(shell) {
	case "bash":
		return "bash", []string{"-euo", "pipefail", "-c", command}
	case "sh", "":
		return "sh", []string{"-eu", "-c", command}
	default:
		return "sh", []string{"-eu", "-c", command}
	}
}

func mergedEnvironment(secretValues map[string]string, values ...any) []string {
	environment := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, found := strings.Cut(item, "=")
		if found {
			environment[key] = value
		}
	}
	for _, value := range values {
		switch typed := value.(type) {
		case json.RawMessage:
			var decoded map[string]string
			if json.Unmarshal(typed, &decoded) == nil {
				for key, item := range decoded {
					environment[key] = item
				}
			}
		case map[string]string:
			for key, item := range typed {
				environment[key] = item
			}
		}
	}
	for key, value := range environment {
		environment[key] = expandSecrets(value, secretValues)
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func expandSecrets(value string, secretValues map[string]string) string {
	for name, secret := range secretValues {
		value = strings.ReplaceAll(value, "${{ secrets."+name+" }}", secret)
		value = strings.ReplaceAll(value, "${{secrets."+name+"}}", secret)
	}
	return value
}

func redactSecrets(value string, secretValues map[string]string) string {
	secrets := make([]string, 0, len(secretValues))
	for _, secret := range secretValues {
		if secret != "" {
			secrets = append(secrets, secret)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "***")
	}
	return value
}
