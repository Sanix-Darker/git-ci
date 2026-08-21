package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
)

func snapshotDefinitionJobs(definition Definition) ([]store.EnqueueJob, error) {
	jobs := make([]store.EnqueueJob, 0, len(definition.Jobs))
	for _, job := range definition.Jobs {
		dependencies := uniqueStrings(append(append([]string{}, job.Needs...), job.Requires...))
		dependencyJSON, err := json.Marshal(dependencies)
		if err != nil {
			return nil, fmt.Errorf("execution: encode dependencies for %q: %w", job.Key, err)
		}
		jobEnvironment, err := json.Marshal(job.Environment)
		if err != nil {
			return nil, fmt.Errorf("execution: encode environment for %q: %w", job.Key, err)
		}
		var childPipeline json.RawMessage
		if job.ChildPipeline != nil {
			childPipeline, err = json.Marshal(job.ChildPipeline)
			if err != nil {
				return nil, fmt.Errorf("execution: encode child pipeline for %q: %w", job.Key, err)
			}
		}
		steps := make([]store.EnqueueStep, 0, len(job.Steps))
		for _, step := range job.Steps {
			stepEnvironment, err := json.Marshal(step.Environment)
			if err != nil {
				return nil, fmt.Errorf("execution: encode environment for step %q: %w", step.Key, err)
			}
			steps = append(steps, store.EnqueueStep{Key: step.Key, Name: step.Name, Command: step.Command, Action: step.Action, Environment: stepEnvironment, WorkingDirectory: step.WorkingDirectory, TimeoutMinutes: step.TimeoutMinutes, Shell: step.Shell, AllowFailure: step.AllowFailure})
		}
		jobs = append(jobs, store.EnqueueJob{
			Key: job.Key, Name: job.Name, Runner: job.RunnerHint, EnvironmentName: job.EnvironmentName,
			DeploymentTier: job.DeploymentTier, Environment: jobEnvironment, DependencyKeys: dependencyJSON,
			AllowFailure: job.AllowFailure, TimeoutMinutes: job.TimeoutMinutes, RollbackCommand: job.RollbackCommand,
			VerifyCommand: job.VerifyCommand, ChildPipeline: childPipeline, Steps: steps,
		})
	}
	return jobs, nil
}

func (m *Manager) executeChildPipelineTrigger(ctx context.Context, parent store.Run, bridge store.JobGraph) (store.Status, bool, error) {
	var snapshot ChildPipelineDefinition
	if err := json.Unmarshal(bridge.Job.ChildPipeline, &snapshot); err != nil {
		return "", false, fmt.Errorf("execution: decode child pipeline bridge %s: %w", bridge.Job.ID, err)
	}
	if snapshot.Definition == nil || parent.WorkflowID == nil || parent.Ref == nil || parent.CommitSHA == nil {
		return "", false, errors.New("execution: child pipeline bridge has an incomplete immutable snapshot")
	}
	link, err := m.store.GetChildPipelineForJob(ctx, bridge.Job.ID)
	if err != nil {
		var notFound *store.ErrNotFound
		if !errors.As(err, &notFound) {
			return "", false, err
		}
		jobs, err := snapshotDefinitionJobs(*snapshot.Definition)
		if err != nil {
			return "", false, err
		}
		environment, err := childPipelineEnvironment(parent.Environment, snapshot)
		if err != nil {
			return "", false, err
		}
		child, err := m.store.EnqueueRun(ctx, store.EnqueueRunParams{
			ProjectID: parent.ProjectID, WorkflowID: *parent.WorkflowID, TriggerType: "parent_pipeline",
			Ref: *parent.Ref, CommitSHA: *parent.CommitSHA, SourcePath: parent.SourcePath,
			Environment: environment, Jobs: jobs,
			ChildPipeline: &store.EnqueueChildPipelineLink{ParentRunID: parent.ID, ParentJobID: bridge.Job.ID, SourceFile: snapshot.SourceFile, Strategy: store.ChildPipelineStrategy(snapshot.Strategy), Depth: snapshot.Depth},
		})
		if err != nil {
			return "", false, fmt.Errorf("execution: enqueue child pipeline %q: %w", snapshot.SourceFile, err)
		}
		link = store.ChildPipelineLink{ParentRunID: parent.ID, ParentJobID: bridge.Job.ID, ChildRunID: child.ID, SourceFile: snapshot.SourceFile, Strategy: store.ChildPipelineStrategy(snapshot.Strategy), Depth: snapshot.Depth, ChildStatus: child.Status}
		m.Notify()
	}
	if link.Strategy == store.ChildPipelineMirror || link.Strategy == store.ChildPipelineDepend {
		return store.StatusWaiting, true, nil
	}
	if bridge.Job.Status == store.StatusQueued {
		if _, err := m.store.TransitionJob(ctx, bridge.Job.ID, store.StatusRunning); err != nil {
			return "", false, fmt.Errorf("execution: start asynchronous child bridge: %w", err)
		}
		if _, err := m.store.TransitionJob(ctx, bridge.Job.ID, store.StatusSucceeded); err != nil {
			return "", false, fmt.Errorf("execution: finish asynchronous child bridge: %w", err)
		}
	}
	return store.StatusSucceeded, false, nil
}

func childPipelineEnvironment(parent json.RawMessage, snapshot ChildPipelineDefinition) (json.RawMessage, error) {
	values := make(map[string]string)
	if snapshot.InheritVariables || snapshot.ForwardPipelineVariables {
		var inherited map[string]string
		if len(parent) > 0 && json.Unmarshal(parent, &inherited) != nil {
			return nil, errors.New("execution: decode parent variables for child pipeline")
		}
		for key, value := range inherited {
			if !strings.HasPrefix(key, "GCI_") {
				values[key] = value
			}
		}
	}
	for key, value := range snapshot.Definition.Environment {
		values[key] = value
	}
	if snapshot.ForwardYAMLVariables {
		for key, value := range snapshot.Variables {
			values[key] = value
		}
	}
	return json.Marshal(values)
}
