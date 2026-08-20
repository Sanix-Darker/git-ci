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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sanix-darker/git-ci/internal/executionsemantics"
	"github.com/sanix-darker/git-ci/internal/runnerinventory"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
	"github.com/sanix-darker/git-ci/pkg/types"
)

const (
	defaultPollInterval           = 750 * time.Millisecond
	runWorkerLeaseTTL             = 15 * time.Second
	runWorkerHeartbeat            = 5 * time.Second
	environmentLeaseTTL           = 30 * time.Second
	environmentLeaseHeartbeat     = 10 * time.Second
	executionConcurrencyTTL       = 30 * time.Second
	executionConcurrencyHeartbeat = 10 * time.Second
	executionConcurrencyRetry     = 250 * time.Millisecond
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
	dataRoot      string
	archives      *archiveManager
	inventory     runnerinventory.Inventory
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

func WithDataRoot(root string) Option {
	return func(manager *Manager) { manager.dataRoot = root }
}

func WithRunnerInventory(inventory runnerinventory.Inventory) Option {
	return func(manager *Manager) { manager.inventory = inventory.Snapshot() }
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
		inventory:    runnerinventory.Local(runnerinventory.Config{}),
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
	if manager.dataRoot == "" && manager.workspaceRoot != "" {
		manager.dataRoot = filepath.Join(filepath.Dir(manager.workspaceRoot), "data")
	}
	archives, err := newArchiveManager(manager.dataRoot)
	if err != nil {
		return nil, err
	}
	manager.archives = archives
	return manager, nil
}

func (m *Manager) RunnerInventory() runnerinventory.Inventory {
	if m == nil {
		return runnerinventory.Inventory{}
	}
	return m.inventory.Snapshot()
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
	applyDefinitionsRunnerInventory(definitions, m.inventory)
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
	return m.EnqueueWorkflowWithInputs(ctx, workflowID, ref, commitSHA, nil)
}

func (m *Manager) EnqueueWorkflowWithInputs(ctx context.Context, workflowID, ref, commitSHA string, inputs map[string]string) (store.Run, error) {
	return m.enqueueTriggered(ctx, workflowID, ref, commitSHA, "manual", inputs)
}

func (m *Manager) EnqueueTriggered(ctx context.Context, workflowID, ref, commitSHA, trigger string) (store.Run, error) {
	return m.enqueueTriggered(ctx, workflowID, ref, commitSHA, trigger, nil)
}

func (m *Manager) enqueueTriggered(ctx context.Context, workflowID, ref, commitSHA, trigger string, inputs map[string]string) (store.Run, error) {
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
	applyDefinitionRunnerInventory(&definition, m.inventory)
	if err := validateDefinitionRunnerAvailability(definition); err != nil {
		return store.Run{}, err
	}
	runEnvironment, err := applyManualInputs(workflow.Environment, definition.TriggerPolicies, inputs)
	if err != nil {
		return store.Run{}, fmt.Errorf("execution: dispatch inputs: %w", err)
	}
	if ref == "" {
		ref = "refs/heads/" + project.DefaultBranch
	}
	resolvedCommit, err := resolveGitCommit(ctx, *project.CanonicalPath, ref, commitSHA)
	if err != nil {
		return store.Run{}, err
	}
	runEnvironment, workflowConcurrency, err := snapshotWorkflowConcurrency(runEnvironment, definition, ref, resolvedCommit, trigger)
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
		Environment: runEnvironment,
		Jobs:        jobs,
	})
	if err != nil {
		return store.Run{}, err
	}
	if workflowConcurrency != nil {
		if err := m.cancelSupersededConcurrencyRuns(ctx, project.ID, run, *workflowConcurrency); err != nil {
			return store.Run{}, err
		}
	}
	m.Notify()
	return run, nil
}

func applyManualInputs(environment []byte, policies []triggerpolicy.Policy, provided map[string]string) ([]byte, error) {
	resolved, err := triggerpolicy.ResolveManualInputs(policies, provided)
	if err != nil {
		return nil, err
	}
	if len(resolved) == 0 {
		return append([]byte(nil), environment...), nil
	}
	values := make(map[string]string)
	if len(environment) > 0 && string(environment) != "null" {
		if err := json.Unmarshal(environment, &values); err != nil {
			return nil, fmt.Errorf("decode workflow environment: %w", err)
		}
	}
	for name, value := range resolved {
		values[inputEnvironmentKey(name)] = value
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode workflow environment: %w", err)
	}
	return encoded, nil
}

func inputEnvironmentKey(name string) string {
	var key strings.Builder
	key.WriteString("INPUT_")
	for _, character := range strings.ToUpper(strings.TrimSpace(name)) {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			key.WriteRune(character)
		} else {
			key.WriteByte('_')
		}
	}
	return strings.TrimRight(key.String(), "_")
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
	workflowLease, err := m.acquireRunConcurrency(workerCtx, *run)
	if err != nil {
		stopWorker()
		<-workerDone
		_ = m.store.ReleaseRunWorker(context.WithoutCancel(ctx), run.ID, m.workerID)
		return true, err
	}
	workflowConcurrencyDone := make(chan struct{})
	if workflowLease != nil {
		go m.heartbeatExecutionConcurrency(workerCtx, *workflowLease, stopWorker, workflowConcurrencyDone)
	} else {
		close(workflowConcurrencyDone)
	}
	workspace, workspaceErr := m.workspaces.Acquire(workerCtx, *run)
	if workspaceErr != nil {
		err = m.failWorkspaceSetup(workerCtx, *run, workspaceErr)
	} else {
		err = m.executeRun(workerCtx, *run, workspace.SourcePath)
	}
	stopWorker()
	<-workerDone
	<-workflowConcurrencyDone
	if workflowLease != nil {
		if _, releaseErr := m.store.ReleaseExecutionConcurrency(context.WithoutCancel(ctx), workflowLease.Scope, workflowLease.Group, workflowLease.HolderID, workflowLease.OwnerID); err == nil && releaseErr != nil {
			err = releaseErr
		}
	}
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
		decision := evaluateJobExecution(graph.Run, item.Job, dependencies, statuses, allowedFailure)
		if !decision.Run {
			if err := m.skipJobWithReason(ctx, item, decision.Reason); err != nil {
				return err
			}
			statuses[key] = store.StatusSkipped
			allowedFailure[key] = decision.AllowFailure
			continue
		}
		item.Job.Environment = mergeEnvironmentJSON(item.Job.Environment, decision.Variables)
		item.Job.AllowFailure = decision.AllowFailure
		allowedFailure[key] = decision.AllowFailure
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
		jobConcurrencyLease, err := m.acquireJobConcurrency(ctx, graph.Run, item.Job)
		if err != nil {
			return err
		}
		jobCtx := ctx
		stopEnvironment := func() {}
		environmentDone := make(chan struct{})
		if preparation.Lease != nil {
			jobCtx, stopEnvironment = context.WithCancel(ctx)
			go m.heartbeatEnvironment(jobCtx, item.Job.ID, stopEnvironment, environmentDone)
		}
		concurrencyDone := make(chan struct{})
		stopConcurrency := func() {}
		if jobConcurrencyLease != nil {
			jobCtx, stopConcurrency = context.WithCancel(jobCtx)
			go m.heartbeatExecutionConcurrency(jobCtx, *jobConcurrencyLease, stopConcurrency, concurrencyDone)
		} else {
			close(concurrencyDone)
		}
		status, err := m.executeJob(jobCtx, graph.Run, item, workspacePath, jobSecrets)
		stopConcurrency()
		<-concurrencyDone
		if jobConcurrencyLease != nil {
			released, releaseErr := m.store.ReleaseExecutionConcurrency(context.WithoutCancel(ctx), jobConcurrencyLease.Scope, jobConcurrencyLease.Group, jobConcurrencyLease.HolderID, jobConcurrencyLease.OwnerID)
			if err == nil && releaseErr != nil {
				err = fmt.Errorf("execution: release job concurrency: %w", releaseErr)
			} else if err == nil && !released {
				err = errors.New("execution: job concurrency ownership was lost")
			}
		}
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

func (m *Manager) heartbeatExecutionConcurrency(ctx context.Context, lease store.ExecutionConcurrencyLease, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(executionConcurrencyHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			result, err := m.store.AcquireExecutionConcurrency(ctx, store.AcquireExecutionConcurrencyParams{
				Scope: lease.Scope, Group: lease.Group, RunID: lease.RunID, HolderID: lease.HolderID,
				OwnerID: lease.OwnerID, TTL: executionConcurrencyTTL, Now: now.UTC(),
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
	jobSemantics, semanticsPresent, semanticsErr := decodeJobSemantics(item.Job.Environment)
	if semanticsErr != nil {
		return "", fmt.Errorf("execution: decode job semantics: %w", semanticsErr)
	}
	var semantics *frozenJobSemantics
	if semanticsPresent {
		semantics = &jobSemantics
	}
	if semantics != nil && semantics.Cache != nil {
		if err := m.restoreDeclaredCache(ctx, run, item.Steps, workspacePath, semantics.Cache); err != nil && len(item.Steps) > 0 {
			_ = m.appendSystem(ctx, item.Steps[0].ID, "cache restore warning: "+err.Error())
		}
	}
	jobCtx := ctx
	jobCancel := func() {}
	if item.Job.TimeoutMinutes > 0 {
		jobCtx, jobCancel = context.WithTimeout(ctx, time.Duration(item.Job.TimeoutMinutes)*time.Minute)
	}
	defer jobCancel()
	var runtime *dockerJobSession
	if semantics != nil && (semantics.Container != nil || len(semantics.Services) > 0) {
		var runtimeErr error
		runtime, runtimeErr = newDockerJobSession(jobCtx, dockerJobSessionConfig{
			RunID: run.ID, JobID: item.Job.ID, Workspace: workspacePath,
			Container: semantics.Container, Services: semantics.Services, Secrets: secretValues,
		})
		if runtimeErr != nil {
			return m.failJobSetup(ctx, item, runtimeErr)
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = runtime.Close(cleanupCtx)
		}()
	}
	jobFailed := false
	stepStatuses := make(map[string]store.Status, len(item.Steps))
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
		condition, conditionErr := decodeStepCondition(step.Environment)
		if conditionErr != nil {
			if err := m.skipStepWithReason(ctx, step, "condition metadata is invalid: "+conditionErr.Error()); err != nil {
				return "", err
			}
			stepStatuses[pointerValue(step.Key)] = store.StatusSkipped
			continue
		}
		conditionContext := buildConditionContext(run, item.Job, semantics, nil, nil, stepStatuses, !jobFailed, jobFailed)
		shouldRun, reason := evaluateConditionContract(condition, conditionContext)
		if !shouldRun {
			if err := m.skipStepWithReason(ctx, step, reason); err != nil {
				return "", err
			}
			stepStatuses[pointerValue(step.Key)] = store.StatusSkipped
			continue
		}
		if _, err := m.store.TransitionStep(ctx, step.ID, store.StatusRunning); err != nil {
			return "", fmt.Errorf("execution: start step: %w", err)
		}
		err = m.executeStepInRuntime(jobCtx, run, item.Job, step, workspacePath, secretValues, runtime, semantics)
		if err == nil {
			if _, transitionErr := m.store.TransitionStep(ctx, step.ID, store.StatusSucceeded); transitionErr != nil {
				return "", transitionErr
			}
			stepStatuses[pointerValue(step.Key)] = store.StatusSucceeded
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
		stepStatuses[pointerValue(step.Key)] = store.StatusFailed
		if step.AllowFailure {
			continue
		}
		jobFailed = true
	}
	status := store.StatusSucceeded
	if jobFailed {
		status = store.StatusFailed
	}
	if status == store.StatusSucceeded {
		if err := m.saveJobCaches(ctx, run, item.Steps, workspacePath, semantics); err != nil {
			status = store.StatusFailed
			if len(item.Steps) > 0 {
				_ = m.appendSystem(ctx, item.Steps[len(item.Steps)-1].ID, "cache save failed: "+err.Error())
			}
		}
	}
	if semantics != nil && semantics.Artifacts != nil {
		if err := m.captureJobArtifact(ctx, run, item.Job, item.Steps, workspacePath, semantics.Artifacts, status == store.StatusSucceeded); err != nil {
			status = store.StatusFailed
			if len(item.Steps) > 0 {
				_ = m.appendSystem(ctx, item.Steps[len(item.Steps)-1].ID, "artifact capture failed: "+err.Error())
			}
		}
	}
	if _, err := m.store.TransitionJob(ctx, item.Job.ID, status); err != nil {
		return "", fmt.Errorf("execution: finish job: %w", err)
	}
	return status, nil
}

func (m *Manager) executeStep(ctx context.Context, run store.Run, job store.Job, step store.Step, workspacePath string, secretValues map[string]string) error {
	return m.executeStepInRuntime(ctx, run, job, step, workspacePath, secretValues, nil, nil)
}

func (m *Manager) executeStepInRuntime(ctx context.Context, run store.Run, job store.Job, step store.Step, workspacePath string, secretValues map[string]string, runtime *dockerJobSession, semantics *frozenJobSemantics) error {
	if step.Action != nil {
		handled, err := m.executeBuiltinAction(ctx, run, job, step, workspacePath)
		if handled {
			return err
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
	if runtime != nil {
		commandText = runtime.resolveServiceExpressions(commandText)
	}
	shell, shellArgs := shellCommand(pointerValue(step.Shell), commandText)
	projectDirectory := workspacePath
	if runtime != nil && runtime.HasJobContainer() {
		projectDirectory = "/workspace"
	}
	baseEnvironment := map[string]string(nil)
	if semantics != nil && semantics.Container != nil {
		baseEnvironment = semantics.Container.Env
	}
	environment := mergedEnvironment(secretValues, run.Environment, job.Environment, baseEnvironment, step.Environment, map[string]string{
		"CI":               "true",
		"GCI_RUN_ID":       run.ID,
		"GCI_JOB_ID":       job.ID,
		"GCI_PROJECT_DIR":  projectDirectory,
		"GITHUB_WORKSPACE": projectDirectory,
		"CI_PROJECT_DIR":   projectDirectory,
	})
	if runtime != nil {
		environment = runtime.resolveEnvironment(environment)
	}
	if runtime != nil && runtime.HasJobContainer() {
		containerDirectory, err := runtime.ContainerWorkingDirectory(directory)
		if err != nil {
			return err
		}
		return m.executeContainerCommand(stepCtx, step.ID, runtime, containerDirectory, shell, shellArgs, environment, secretValues)
	}
	command := exec.CommandContext(stepCtx, shell, shellArgs...)
	command.Dir = directory
	command.Env = environment
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

func (m *Manager) executeContainerCommand(ctx context.Context, stepID string, runtime *dockerJobSession, directory, shell string, shellArgs, environment []string, secretValues map[string]string) error {
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	result := make(chan error, 1)
	go func() {
		err := runtime.Exec(ctx, dockerExecRequest{WorkingDirectory: directory, Environment: environment, Command: append([]string{shell}, shellArgs...)}, stdoutWriter, stderrWriter)
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		result <- err
	}()
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for stream, reader := range map[store.LogStream]io.Reader{store.LogStreamStdout: stdoutReader, store.LogStreamStderr: stderrReader} {
		wait.Add(1)
		go func(stream store.LogStream, reader io.Reader) {
			defer wait.Done()
			if err := m.captureLines(ctx, stepID, stream, reader, secretValues); err != nil {
				errorsFound <- err
			}
		}(stream, reader)
	}
	wait.Wait()
	commandErr := <-result
	close(errorsFound)
	for captureErr := range errorsFound {
		if commandErr == nil {
			commandErr = captureErr
		}
	}
	return commandErr
}

func (m *Manager) failJobSetup(ctx context.Context, item store.JobGraph, setupErr error) (store.Status, error) {
	if len(item.Steps) > 0 {
		first := item.Steps[0]
		if err := m.appendSystem(ctx, first.ID, "job runtime setup failed: "+setupErr.Error()); err != nil {
			return "", err
		}
		if _, err := m.store.TransitionStep(ctx, first.ID, store.StatusFailed); err != nil {
			return "", err
		}
		if err := m.skipSteps(ctx, item.Steps[1:]); err != nil {
			return "", err
		}
	}
	if _, err := m.store.TransitionJob(ctx, item.Job.ID, store.StatusFailed); err != nil {
		return "", err
	}
	return store.StatusFailed, nil
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

func (m *Manager) skipJobWithReason(ctx context.Context, item store.JobGraph, reason string) error {
	if reason != "" && len(item.Steps) > 0 {
		if err := m.appendSystem(ctx, item.Steps[0].ID, "job skipped: "+reason); err != nil {
			return err
		}
	}
	return m.skipJob(ctx, item)
}

func (m *Manager) skipStepWithReason(ctx context.Context, step store.Step, reason string) error {
	if reason != "" {
		if err := m.appendSystem(ctx, step.ID, "step skipped: "+reason); err != nil {
			return err
		}
	}
	_, err := m.store.TransitionStep(ctx, step.ID, store.StatusSkipped)
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

type frozenJobSemantics struct {
	Provider      string                               `json:"provider"`
	SourceKey     string                               `json:"sourceKey"`
	Matrix        map[string]string                    `json:"matrix"`
	MatrixIndex   int                                  `json:"matrixIndex"`
	MatrixTotal   int                                  `json:"matrixTotal"`
	MatrixLabel   string                               `json:"matrixLabel"`
	Condition     executionsemantics.ConditionContract `json:"condition"`
	Rules         []RuleDefinition                     `json:"rules"`
	Only          *OnlyExceptDefinition                `json:"only"`
	Except        *OnlyExceptDefinition                `json:"except"`
	When          string                               `json:"when"`
	Concurrency   *ConcurrencyDefinition               `json:"concurrency"`
	Interruptible bool                                 `json:"interruptible"`
	FailFast      bool                                 `json:"failFast"`
	MaxParallel   int                                  `json:"maxParallel"`
	WorkflowCall  *types.WorkflowCall                  `json:"workflowCall"`
	Container     *types.Container                     `json:"container"`
	Services      map[string]*types.Service            `json:"services"`
	Artifacts     *types.ArtifactConfig                `json:"artifacts"`
	Cache         *types.CacheConfig                   `json:"cache"`
}

type workflowConcurrencySnapshot struct {
	Group            string `json:"group"`
	CancelInProgress bool   `json:"cancelInProgress,omitempty"`
}

type jobExecutionDecision struct {
	Run          bool
	Reason       string
	Variables    map[string]string
	AllowFailure bool
}

func evaluateJobExecution(run store.Run, job store.Job, dependencies []string, statuses map[string]store.Status, allowed map[string]bool) jobExecutionDecision {
	decision := jobExecutionDecision{AllowFailure: job.AllowFailure}
	semantics, present, err := decodeJobSemantics(job.Environment)
	if err != nil {
		decision.Reason = "execution metadata is invalid: " + err.Error()
		return decision
	}
	ready, successful, failed := dependencyState(dependencies, statuses, allowed)
	if !ready {
		decision.Reason = "a required job has not completed"
		return decision
	}
	if !present {
		decision.Run = successful
		if !successful {
			decision.Reason = "a required job did not succeed"
		}
		return decision
	}
	conditionContext := buildConditionContext(run, job, &semantics, dependencies, statuses, nil, successful, failed)
	conditionMatches, reason := evaluateConditionContract(semantics.Condition, conditionContext)
	if !conditionMatches {
		decision.Reason = reason
		return decision
	}

	if len(semantics.Rules) > 0 {
		for index, rule := range semantics.Rules {
			if len(rule.Changes) > 0 || len(rule.Exists) > 0 {
				decision.Reason = fmt.Sprintf("rule %d uses changes/exists, which is not runtime-evaluable yet", index+1)
				return decision
			}
			matches, ruleReason := evaluateConditionContract(rule.Condition, conditionContext)
			if !matches {
				if rule.Condition.Expression != "" && !rule.Condition.Evaluable {
					decision.Reason = ruleReason
					return decision
				}
				continue
			}
			when := strings.ToLower(strings.TrimSpace(rule.When))
			if when == "never" || when == "manual" || when == "delayed" {
				decision.Reason = "matched rule requires when: " + when
				return decision
			}
			decision.Run = true
			decision.Variables = rule.Variables
			decision.AllowFailure = decision.AllowFailure || rule.AllowFailure
			return decision
		}
		decision.Reason = "no GitLab rule matched"
		return decision
	}

	if semantics.Only != nil && !matchesRefSelector(semantics.Only.Refs, run) {
		decision.Reason = "run ref does not match only selector"
		return decision
	}
	if semantics.Except != nil && matchesRefSelector(semantics.Except.Refs, run) {
		decision.Reason = "run ref matches except selector"
		return decision
	}
	when := strings.ToLower(strings.TrimSpace(semantics.When))
	if when == "never" || when == "manual" || when == "delayed" {
		decision.Reason = "job requires when: " + when
		return decision
	}
	decision.Run = true
	return decision
}

func decodeJobSemantics(environment json.RawMessage) (frozenJobSemantics, bool, error) {
	values := decodeEnvironmentJSON(environment)
	encoded := strings.TrimSpace(values["GCI_JOB_SEMANTICS_JSON"])
	if encoded == "" {
		return frozenJobSemantics{}, false, nil
	}
	var semantics frozenJobSemantics
	if err := json.Unmarshal([]byte(encoded), &semantics); err != nil {
		return frozenJobSemantics{}, true, err
	}
	return semantics, true, nil
}

func decodeStepCondition(environment json.RawMessage) (executionsemantics.ConditionContract, error) {
	encoded := strings.TrimSpace(decodeEnvironmentJSON(environment)["GCI_STEP_CONDITION_JSON"])
	if encoded == "" {
		return executionsemantics.ConditionContract{Evaluable: true}, nil
	}
	var condition executionsemantics.ConditionContract
	if err := json.Unmarshal([]byte(encoded), &condition); err != nil {
		return executionsemantics.ConditionContract{}, err
	}
	return condition, nil
}

func evaluateConditionContract(condition executionsemantics.ConditionContract, context executionsemantics.ConditionContext) (bool, string) {
	if !condition.Evaluable {
		diagnostic := condition.Diagnostic
		if diagnostic == "" {
			diagnostic = "condition is not supported"
		}
		return false, diagnostic
	}
	if condition.Expression == "" {
		if context.Success {
			return true, ""
		}
		return false, "default success condition was not met"
	}
	if !usesStatusFunction(condition.Expression) && !context.Success {
		return false, "implicit success condition was not met"
	}
	matches, err := executionsemantics.EvaluateCondition(condition.Expression, context)
	if err != nil {
		return false, "condition could not be evaluated: " + err.Error()
	}
	if !matches {
		return false, "condition evaluated to false: " + condition.Expression
	}
	return true, ""
}

func usesStatusFunction(expression string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(expression, " ", ""))
	for _, function := range []string{"success(", "failure(", "cancelled(", "always("} {
		if strings.Contains(normalized, function) {
			return true
		}
	}
	return false
}

func dependencyState(dependencies []string, statuses map[string]store.Status, allowed map[string]bool) (ready, successful, failed bool) {
	ready = true
	successful = true
	for _, dependency := range dependencies {
		status, exists := statuses[dependency]
		if !exists {
			ready = false
			successful = false
			continue
		}
		if status == store.StatusSucceeded || status == store.StatusFailed && allowed[dependency] {
			continue
		}
		successful = false
		if status == store.StatusFailed {
			failed = true
		}
	}
	return ready, successful, failed
}

func buildConditionContext(run store.Run, job store.Job, semantics *frozenJobSemantics, dependencies []string, statuses map[string]store.Status, stepStatuses map[string]store.Status, success, failure bool) executionsemantics.ConditionContext {
	values := make(map[string]interface{})
	ref := pointerValue(run.Ref)
	refName := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	eventName := run.TriggerType
	if eventName == "manual" {
		eventName = "workflow_dispatch"
	}
	values["github.ref"] = ref
	values["github.ref_name"] = refName
	values["github.sha"] = pointerValue(run.CommitSHA)
	values["github.event_name"] = eventName
	values["CI_COMMIT_REF_NAME"] = refName
	values["CI_COMMIT_BRANCH"] = refName
	values["CI_COMMIT_SHA"] = pointerValue(run.CommitSHA)
	values["CI_PIPELINE_SOURCE"] = gitLabPipelineSource(run.TriggerType)
	for key, value := range decodeEnvironmentJSON(run.Environment) {
		values["env."+key] = value
		values[key] = value
		if strings.HasPrefix(key, "INPUT_") {
			values["inputs."+strings.ToLower(strings.TrimPrefix(key, "INPUT_"))] = value
		}
	}
	for key, value := range decodeEnvironmentJSON(job.Environment) {
		values["env."+key] = value
		values[key] = value
	}
	if semantics != nil {
		for key, value := range semantics.Matrix {
			values["matrix."+key] = value
		}
	}
	for _, dependency := range dependencies {
		if status, exists := statuses[dependency]; exists {
			values["needs."+dependency+".result"] = providerStatus(status)
		}
	}
	for key, status := range stepStatuses {
		values["steps."+key+".outcome"] = providerStatus(status)
		values["steps."+key+".conclusion"] = providerStatus(status)
	}
	return executionsemantics.ConditionContext{
		Values: values, Success: success, Failure: failure, Cancelled: false,
		CaseInsensitive: semantics != nil && semantics.Provider == string(ProviderGitLabCI),
	}
}

func matchesRefSelector(selectors []string, run store.Run) bool {
	if len(selectors) == 0 {
		return true
	}
	ref := pointerValue(run.Ref)
	refName := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	for _, selector := range selectors {
		switch strings.TrimSpace(selector) {
		case "branches":
			if strings.HasPrefix(ref, "refs/heads/") {
				return true
			}
		case "tags":
			if strings.HasPrefix(ref, "refs/tags/") {
				return true
			}
		case "merge_requests":
			if run.TriggerType == "pull_request" || run.TriggerType == "merge_request" {
				return true
			}
		case "schedules":
			if run.TriggerType == "schedule" {
				return true
			}
		default:
			if selector == ref || selector == refName {
				return true
			}
		}
	}
	return false
}

func gitLabPipelineSource(trigger string) string {
	switch trigger {
	case "manual", "workflow_dispatch":
		return "web"
	case "pull_request", "merge_request":
		return "merge_request_event"
	case "schedule":
		return "schedule"
	default:
		return "push"
	}
}

func providerStatus(status store.Status) string {
	switch status {
	case store.StatusSucceeded:
		return "success"
	case store.StatusFailed:
		return "failure"
	default:
		return string(status)
	}
}

func decodeEnvironmentJSON(environment json.RawMessage) map[string]string {
	values := make(map[string]string)
	_ = json.Unmarshal(environment, &values)
	return values
}

func mergeEnvironmentJSON(environment json.RawMessage, overlay map[string]string) json.RawMessage {
	if len(overlay) == 0 {
		return environment
	}
	values := decodeEnvironmentJSON(environment)
	for key, value := range overlay {
		values[key] = value
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return environment
	}
	return encoded
}

func snapshotWorkflowConcurrency(environment json.RawMessage, definition Definition, ref, commitSHA, trigger string) (json.RawMessage, *workflowConcurrencySnapshot, error) {
	if definition.Concurrency == nil {
		return environment, nil, nil
	}
	values := runtimeTemplateValues(ref, commitSHA, trigger, definition.Name, decodeEnvironmentJSON(environment))
	group, err := resolveConcurrencyGroup(definition.Concurrency.Group, values)
	if err != nil {
		return nil, nil, fmt.Errorf("execution: workflow concurrency: %w", err)
	}
	snapshot := &workflowConcurrencySnapshot{Group: group, CancelInProgress: definition.Concurrency.CancelInProgress}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("execution: encode workflow concurrency: %w", err)
	}
	return mergeEnvironmentJSON(environment, map[string]string{"GCI_WORKFLOW_CONCURRENCY_JSON": string(encoded)}), snapshot, nil
}

func decodeWorkflowConcurrency(environment json.RawMessage) (*workflowConcurrencySnapshot, error) {
	encoded := strings.TrimSpace(decodeEnvironmentJSON(environment)["GCI_WORKFLOW_CONCURRENCY_JSON"])
	if encoded == "" {
		return nil, nil
	}
	var snapshot workflowConcurrencySnapshot
	if err := json.Unmarshal([]byte(encoded), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (m *Manager) cancelSupersededConcurrencyRuns(ctx context.Context, projectID string, current store.Run, snapshot workflowConcurrencySnapshot) error {
	runs, err := m.store.ListRuns(ctx, projectID)
	if err != nil {
		return fmt.Errorf("execution: list concurrency runs: %w", err)
	}
	for _, candidate := range runs {
		if candidate.ID == current.ID || candidate.Status != store.StatusQueued && !(snapshot.CancelInProgress && candidate.Status == store.StatusRunning) {
			continue
		}
		other, err := decodeWorkflowConcurrency(candidate.Environment)
		if err != nil || other == nil || !strings.EqualFold(other.Group, snapshot.Group) {
			continue
		}
		if _, err := m.store.RequestRunCancellation(ctx, candidate.ID); err != nil {
			return fmt.Errorf("execution: supersede concurrency run %q: %w", candidate.ID, err)
		}
	}
	return nil
}

func (m *Manager) acquireRunConcurrency(ctx context.Context, run store.Run) (*store.ExecutionConcurrencyLease, error) {
	snapshot, err := decodeWorkflowConcurrency(run.Environment)
	if err != nil {
		return nil, fmt.Errorf("execution: decode workflow concurrency: %w", err)
	}
	if snapshot == nil || snapshot.Group == "" {
		return nil, nil
	}
	return m.waitForExecutionConcurrency(ctx, store.ExecutionConcurrencyWorkflow, concurrencyLeaseGroup(run.ProjectID, snapshot.Group), run.ID, run.ID, snapshot.CancelInProgress)
}

func (m *Manager) acquireJobConcurrency(ctx context.Context, run store.Run, job store.Job) (*store.ExecutionConcurrencyLease, error) {
	semantics, present, err := decodeJobSemantics(job.Environment)
	if err != nil {
		return nil, fmt.Errorf("execution: decode job concurrency: %w", err)
	}
	if !present || semantics.Concurrency == nil || strings.TrimSpace(semantics.Concurrency.Group) == "" {
		return nil, nil
	}
	conditionContext := buildConditionContext(run, job, &semantics, nil, nil, nil, true, false)
	group, err := resolveConcurrencyGroup(semantics.Concurrency.Group, conditionContext.Values)
	if err != nil {
		return nil, fmt.Errorf("execution: job concurrency: %w", err)
	}
	return m.waitForExecutionConcurrency(ctx, store.ExecutionConcurrencyJob, concurrencyLeaseGroup(run.ProjectID, group), run.ID, job.ID, semantics.Concurrency.CancelInProgress)
}

func (m *Manager) waitForExecutionConcurrency(ctx context.Context, scope store.ExecutionConcurrencyScope, group, runID, holderID string, cancelInProgress bool) (*store.ExecutionConcurrencyLease, error) {
	lastCancellation := ""
	for {
		result, err := m.store.AcquireExecutionConcurrency(ctx, store.AcquireExecutionConcurrencyParams{
			Scope: scope, Group: group, RunID: runID, HolderID: holderID,
			OwnerID: m.workerID, TTL: executionConcurrencyTTL, Now: time.Now().UTC(),
		})
		if err != nil {
			return nil, fmt.Errorf("execution: acquire concurrency group %q: %w", group, err)
		}
		if result.Acquired {
			return &result.Lease, nil
		}
		if cancelInProgress && result.Lease.RunID != runID && result.Lease.RunID != lastCancellation {
			if _, err := m.store.RequestRunCancellation(ctx, result.Lease.RunID); err != nil {
				return nil, fmt.Errorf("execution: cancel concurrency holder: %w", err)
			}
			lastCancellation = result.Lease.RunID
		}
		timer := time.NewTimer(executionConcurrencyRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func concurrencyLeaseGroup(projectID, group string) string {
	return strings.ToLower(strings.TrimSpace(projectID)) + ":" + strings.ToLower(strings.TrimSpace(group))
}

func resolveConcurrencyGroup(group string, values map[string]interface{}) (string, error) {
	for {
		start := strings.Index(group, "${{")
		if start < 0 {
			break
		}
		endOffset := strings.Index(group[start+3:], "}}")
		if endOffset < 0 {
			return "", errors.New("template is missing closing braces")
		}
		end := start + 3 + endOffset
		name := strings.TrimSpace(group[start+3 : end])
		value, exists := values[name]
		if !exists {
			return "", fmt.Errorf("template context %q is unavailable", name)
		}
		group = group[:start] + fmt.Sprint(value) + group[end+2:]
	}
	group = strings.NewReplacer(
		"$CI_COMMIT_REF_NAME", fmt.Sprint(values["CI_COMMIT_REF_NAME"]),
		"$CI_COMMIT_BRANCH", fmt.Sprint(values["CI_COMMIT_BRANCH"]),
	).Replace(group)
	return executionsemantics.NormalizeConcurrencyGroup(group)
}

func runtimeTemplateValues(ref, commitSHA, trigger, workflow string, environment map[string]string) map[string]interface{} {
	refName := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
	values := map[string]interface{}{
		"github.ref": ref, "github.ref_name": refName, "github.sha": commitSHA,
		"github.event_name": trigger, "github.workflow": workflow,
		"CI_COMMIT_REF_NAME": refName, "CI_COMMIT_BRANCH": refName, "CI_COMMIT_SHA": commitSHA,
	}
	for key, value := range environment {
		values["env."+key] = value
		if strings.HasPrefix(key, "INPUT_") {
			values["inputs."+strings.ToLower(strings.TrimPrefix(key, "INPUT_"))] = value
		}
	}
	return values
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
