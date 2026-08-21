package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
)

const (
	workflowRunDispatchBatch = 32
	workflowRunMaxDepth      = 3
	workflowRunSourceIDKey   = "GCI_WORKFLOW_RUN_SOURCE_ID"
	workflowRunNameKey       = "GCI_WORKFLOW_RUN_WORKFLOW"
	workflowRunRevisionKey   = "GCI_WORKFLOW_RUN_WORKFLOW_REVISION"
	workflowRunConclusionKey = "GCI_WORKFLOW_RUN_CONCLUSION"
	workflowRunDepthKey      = "GCI_WORKFLOW_RUN_DEPTH"
	workflowRunRefKey        = "GCI_WORKFLOW_RUN_HEAD_REF"
	workflowRunSHAKey        = "GCI_WORKFLOW_RUN_HEAD_SHA"
)

func (m *Manager) reconcileWorkflowRunDispatches(ctx context.Context) error {
	dispatches, err := m.store.ListPendingWorkflowRunDispatches(ctx, workflowRunDispatchBatch)
	if err != nil {
		return err
	}
	for _, dispatch := range dispatches {
		if err := m.dispatchCompletedWorkflow(ctx, dispatch); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) dispatchCompletedWorkflow(ctx context.Context, dispatch store.WorkflowRunDispatch) error {
	graph, err := m.store.GetRunGraph(ctx, dispatch.SourceRunID)
	if err != nil {
		return fmt.Errorf("load source run %s: %w", dispatch.SourceRunID, err)
	}
	source := graph.Run
	depth := 0
	if graph.WorkflowRun != nil {
		depth = graph.WorkflowRun.Depth
	}
	if depth >= workflowRunMaxDepth {
		return m.store.MarkWorkflowRunDispatched(ctx, source.ID)
	}
	if source.WorkflowID == nil || source.Ref == nil || source.CommitSHA == nil {
		return fmt.Errorf("source run %s has incomplete workflow, ref, or commit provenance", source.ID)
	}
	sourceWorkflow, err := m.store.GetWorkflow(ctx, *source.WorkflowID)
	if err != nil {
		return err
	}
	var sourceDefinition Definition
	if err := json.Unmarshal(sourceWorkflow.Definition, &sourceDefinition); err != nil {
		return fmt.Errorf("decode source workflow definition: %w", err)
	}
	if sourceDefinition.Provider != ProviderGitHubActions {
		return m.store.MarkWorkflowRunDispatched(ctx, source.ID)
	}
	targets, err := m.store.ListWorkflows(ctx, source.ProjectID)
	if err != nil {
		return err
	}
	event := triggerpolicy.Event{Type: "workflow_run", Ref: *source.Ref, Action: "completed", Workflow: dispatch.SourceWorkflowName}
	for _, target := range targets {
		var definition Definition
		if err := json.Unmarshal(target.Definition, &definition); err != nil {
			return fmt.Errorf("decode target workflow %s: %w", target.ID, err)
		}
		if definition.Provider != ProviderGitHubActions || !triggerpolicy.Match(definition.TriggerPolicies, definition.Triggers, event) {
			continue
		}
		key := "workflow_run:" + source.ID + ":" + target.ID
		existing, err := m.store.GetWorkflowRunLinkByIdempotency(ctx, key)
		if err == nil {
			if existing.SourceRunID != source.ID || existing.TargetWorkflowID != target.ID {
				return fmt.Errorf("workflow_run idempotency key %q has incompatible provenance", key)
			}
			continue
		}
		var notFound *store.ErrNotFound
		if !errors.As(err, &notFound) {
			return err
		}
		applyDefinitionRunnerInventory(&definition, m.inventory)
		if err := validateDefinitionRunnerAvailability(definition); err != nil {
			return err
		}
		environment, err := workflowRunEnvironment(target.Environment, dispatch, source, depth+1)
		if err != nil {
			return err
		}
		environment, concurrency, err := snapshotWorkflowConcurrency(environment, definition, *source.Ref, *source.CommitSHA, "workflow_run")
		if err != nil {
			return err
		}
		jobs, err := snapshotDefinitionJobs(definition)
		if err != nil {
			return err
		}
		link := store.EnqueueWorkflowRunLink{
			SourceRunID: source.ID, SourceWorkflowName: dispatch.SourceWorkflowName,
			SourceWorkflowRevision: dispatch.SourceWorkflowRevision, SourceConclusion: dispatch.Conclusion,
			TargetWorkflowID: target.ID, TargetWorkflowRevision: target.Revision,
			Depth: depth + 1, IdempotencyKey: key,
		}
		run, err := m.store.EnqueueRun(ctx, store.EnqueueRunParams{
			ProjectID: source.ProjectID, WorkflowID: target.ID, TriggerType: "workflow_run",
			Ref: *source.Ref, CommitSHA: *source.CommitSHA, SourcePath: source.SourcePath,
			Environment: environment, Jobs: jobs, WorkflowRun: &link,
		})
		if err != nil {
			if duplicate, lookupErr := m.store.GetWorkflowRunLinkByIdempotency(ctx, key); lookupErr == nil && duplicate.SourceRunID == source.ID && duplicate.TargetWorkflowID == target.ID {
				continue
			}
			return fmt.Errorf("enqueue workflow_run target %s: %w", target.ID, err)
		}
		if concurrency != nil {
			if err := m.cancelSupersededConcurrencyRuns(ctx, source.ProjectID, run, *concurrency); err != nil {
				return err
			}
		}
	}
	if err := m.store.MarkWorkflowRunDispatched(ctx, source.ID); err != nil {
		return err
	}
	m.Notify()
	return nil
}

func workflowRunEnvironment(base json.RawMessage, dispatch store.WorkflowRunDispatch, source store.Run, depth int) (json.RawMessage, error) {
	values := make(map[string]string)
	if len(base) > 0 && string(base) != "null" {
		if err := json.Unmarshal(base, &values); err != nil {
			return nil, fmt.Errorf("execution: decode workflow_run environment: %w", err)
		}
	}
	values[workflowRunSourceIDKey] = source.ID
	values[workflowRunNameKey] = dispatch.SourceWorkflowName
	values[workflowRunRevisionKey] = strconv.FormatInt(dispatch.SourceWorkflowRevision, 10)
	values[workflowRunConclusionKey] = string(dispatch.Conclusion)
	values[workflowRunDepthKey] = strconv.Itoa(depth)
	values[workflowRunRefKey] = pointerValue(source.Ref)
	values[workflowRunSHAKey] = pointerValue(source.CommitSHA)
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("execution: encode workflow_run environment: %w", err)
	}
	return encoded, nil
}

func addWorkflowRunConditionValues(values map[string]interface{}, environment map[string]string) {
	conclusion := strings.TrimSpace(environment[workflowRunConclusionKey])
	if conclusion == "" {
		return
	}
	values["github.event.workflow_run.id"] = environment[workflowRunSourceIDKey]
	values["github.event.workflow_run.name"] = environment[workflowRunNameKey]
	values["github.event.workflow_run.conclusion"] = conclusion
	values["github.event.workflow_run.head_branch"] = strings.TrimPrefix(environment[workflowRunRefKey], "refs/heads/")
	values["github.event.workflow_run.head_sha"] = environment[workflowRunSHAKey]
	if depth, err := strconv.Atoi(environment[workflowRunDepthKey]); err == nil {
		values["github.event.workflow_run.depth"] = depth
	}
}
