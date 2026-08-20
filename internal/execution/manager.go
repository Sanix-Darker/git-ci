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

	"github.com/sanix-darker/git-ci/internal/store"
)

const defaultPollInterval = 750 * time.Millisecond

// Manager owns workflow synchronization, immutable run creation, and one
// local execution worker. The registered project path is snapshotted before a
// run enters the queue; workers never accept arbitrary paths or commands from
// HTTP requests.
type Manager struct {
	store        *store.Store
	workerID     string
	pollInterval time.Duration
	wake         chan struct{}
}

func NewManager(database *store.Store) (*Manager, error) {
	if database == nil {
		return nil, errors.New("execution: store is required")
	}
	return &Manager{
		store:        database,
		workerID:     fmt.Sprintf("local-%d", os.Getpid()),
		pollInterval: defaultPollInterval,
		wake:         make(chan struct{}, 1),
	}, nil
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
			Key:            job.Key,
			Name:           job.Name,
			Runner:         job.RunnerHint,
			Environment:    jobEnvironment,
			DependencyKeys: dependencyJSON,
			AllowFailure:   job.AllowFailure,
			TimeoutMinutes: job.TimeoutMinutes,
			Steps:          steps,
		})
	}
	run, err := m.store.EnqueueRun(ctx, store.EnqueueRunParams{
		ProjectID:   project.ID,
		WorkflowID:  workflow.ID,
		TriggerType: "manual",
		Ref:         strings.TrimSpace(ref),
		CommitSHA:   strings.TrimSpace(commitSHA),
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

// Run recovers any interrupted work once, then drains queued runs serially.
func (m *Manager) Run(ctx context.Context) error {
	if _, err := m.store.MarkInterruptedRunningRunsFailed(ctx); err != nil {
		return fmt.Errorf("execution: recover interrupted runs: %w", err)
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
	run, err := m.store.ClaimNextQueuedRun(ctx, m.workerID)
	if err != nil {
		return false, fmt.Errorf("execution: claim run: %w", err)
	}
	if run == nil {
		return false, nil
	}
	if err := m.executeRun(ctx, *run); err != nil {
		return true, err
	}
	return true, nil
}

func (m *Manager) executeRun(ctx context.Context, run store.Run) error {
	graph, err := m.store.GetRunGraph(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("execution: load claimed run: %w", err)
	}
	statuses := make(map[string]store.Status, len(graph.Jobs))
	allowedFailure := make(map[string]bool, len(graph.Jobs))
	for _, item := range graph.Jobs {
		key := pointerValue(item.Job.Key)
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
		status, err := m.executeJob(ctx, graph.Run, item)
		if err != nil {
			return err
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

func (m *Manager) executeJob(ctx context.Context, run store.Run, item store.JobGraph) (store.Status, error) {
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
		err = m.executeStep(jobCtx, run, item.Job, step)
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

func (m *Manager) executeStep(ctx context.Context, run store.Run, job store.Job, step store.Step) error {
	if step.Action != nil {
		if strings.HasPrefix(*step.Action, "actions/checkout@") {
			return m.appendSystem(ctx, step.ID, "using registered checkout "+run.SourcePath)
		}
		return fmt.Errorf("unsupported action %q", *step.Action)
	}
	if step.Command == nil {
		return errors.New("step has no command")
	}
	directory, err := containedWorkingDirectory(run.SourcePath, pointerValue(step.WorkingDirectory))
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

	shell, shellArgs := shellCommand(pointerValue(step.Shell), *step.Command)
	command := exec.CommandContext(stepCtx, shell, shellArgs...)
	command.Dir = directory
	command.Env = mergedEnvironment(run.Environment, job.Environment, step.Environment, map[string]string{
		"CI":              "true",
		"GCI_RUN_ID":      run.ID,
		"GCI_JOB_ID":      job.ID,
		"GCI_PROJECT_DIR": run.SourcePath,
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
			if err := m.captureLines(stepCtx, step.ID, stream, reader); err != nil {
				errorsFound <- err
			}
		}(stream, reader)
	}
	commandErr := command.Wait()
	wait.Wait()
	close(errorsFound)
	for captureErr := range errorsFound {
		if commandErr == nil {
			commandErr = captureErr
		}
	}
	return commandErr
}

func (m *Manager) captureLines(ctx context.Context, stepID string, stream store.LogStream, reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if _, err := m.store.AppendLogLine(ctx, store.AppendLogLineParams{StepID: stepID, Stream: stream, Message: scanner.Text()}); err != nil {
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

func containedWorkingDirectory(root, relative string) (string, error) {
	root = filepath.Clean(root)
	if relative == "" {
		return root, nil
	}
	if filepath.IsAbs(relative) {
		return "", errors.New("execution: working directory must be relative")
	}
	target := filepath.Clean(filepath.Join(root, relative))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("execution: working directory escapes registered project")
	}
	return target, nil
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

func mergedEnvironment(values ...any) []string {
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
