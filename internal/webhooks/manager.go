package webhooks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/sanix-darker/git-ci/internal/gitrepository"
	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
)

type Enqueuer interface {
	EnqueueTriggered(context.Context, string, string, string, string) (store.Run, error)
}

type Manager struct {
	store    *store.Store
	enqueuer Enqueuer
}

type EndpointConfig struct {
	WorkflowID string `json:"workflowId"`
	Ref        string `json:"ref,omitempty"`
}

type CreatedEndpoint struct {
	Endpoint store.WebhookEndpoint `json:"endpoint"`
	Token    string                `json:"token"`
}

func NewManager(database *store.Store, enqueuer Enqueuer) (*Manager, error) {
	if database == nil || enqueuer == nil {
		return nil, errors.New("webhooks: store and enqueuer are required")
	}
	return &Manager{store: database, enqueuer: enqueuer}, nil
}

func (m *Manager) Create(ctx context.Context, projectID, name, provider, workflowID, ref string) (CreatedEndpoint, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "github" && provider != "gitlab" && provider != "generic" {
		return CreatedEndpoint{}, errors.New("webhooks: provider must be github, gitlab, or generic")
	}
	workflow, err := m.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return CreatedEndpoint{}, err
	}
	if workflow.ProjectID != projectID {
		return CreatedEndpoint{}, errors.New("webhooks: workflow must belong to project")
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, tokenBytes); err != nil {
		return CreatedEndpoint{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	metadata, _ := json.Marshal(EndpointConfig{WorkflowID: workflowID, Ref: strings.TrimSpace(ref)})
	endpoint, err := m.store.CreateWebhookEndpoint(ctx, store.CreateWebhookEndpointParams{
		ProjectID: projectID, Name: name, Provider: provider, TokenHash: hash[:], Metadata: metadata, Enabled: true,
	})
	if err != nil {
		return CreatedEndpoint{}, err
	}
	return CreatedEndpoint{Endpoint: endpoint, Token: token}, nil
}

func (m *Manager) Deliver(ctx context.Context, endpointID, token, deliveryID, eventType string, payload []byte) (store.RecordedWebhookDelivery, *store.Run, error) {
	endpoint, err := m.store.GetWebhookEndpoint(ctx, endpointID)
	if err != nil {
		return store.RecordedWebhookDelivery{}, nil, err
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	if !endpoint.Enabled || subtle.ConstantTimeCompare(hash[:], endpoint.TokenHash) != 1 {
		return store.RecordedWebhookDelivery{}, nil, errors.New("webhooks: invalid endpoint token")
	}
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return store.RecordedWebhookDelivery{}, nil, errors.New("webhooks: delivery ID is required")
	}
	payloadHash := sha256.Sum256(payload)
	recorded, err := m.store.RecordWebhookDelivery(ctx, store.RecordWebhookDeliveryParams{
		EndpointID: endpoint.ID, ProviderDeliveryID: deliveryID, EventType: strings.TrimSpace(eventType),
		PayloadSHA256: hex.EncodeToString(payloadHash[:]), Status: store.WebhookDeliveryReceived,
	})
	if err != nil {
		return store.RecordedWebhookDelivery{}, nil, err
	}
	if !recorded.Created {
		return recorded, nil, nil
	}
	var config EndpointConfig
	if err := json.Unmarshal(endpoint.Metadata, &config); err != nil || config.WorkflowID == "" {
		message := "invalid endpoint workflow configuration"
		_, _ = m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryFailed, &message)
		return recorded, nil, errors.New("webhooks: " + message)
	}
	workflow, err := m.store.GetWorkflow(ctx, config.WorkflowID)
	if err != nil {
		message := err.Error()
		_, _ = m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryFailed, &message)
		return recorded, nil, fmt.Errorf("webhooks: workflow: %w", err)
	}
	var definition struct {
		Triggers        []string               `json:"triggers"`
		TriggerPolicies []triggerpolicy.Policy `json:"triggerPolicies"`
	}
	if err := json.Unmarshal(workflow.Definition, &definition); err != nil {
		message := "invalid workflow trigger policy"
		_, _ = m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryFailed, &message)
		return recorded, nil, fmt.Errorf("webhooks: %s: %w", message, err)
	}
	normalized := normalizeWebhookPayload(endpoint.Provider, eventType, config.Ref, payload)
	event := normalized.Event
	if !event.PathsKnown && triggerpolicy.NeedsChangedPaths(definition.TriggerPolicies, definition.Triggers, event) && normalized.DiffBase != "" && normalized.DiffHead != "" {
		project, projectErr := m.store.GetProject(ctx, workflow.ProjectID)
		if projectErr != nil || project.CanonicalPath == nil || strings.TrimSpace(*project.CanonicalPath) == "" {
			message := "changed paths require an active local project checkout"
			_, _ = m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryFailed, &message)
			return recorded, nil, errors.New("webhooks: " + message)
		}
		paths, pathsErr := gitrepository.ChangedPaths(ctx, *project.CanonicalPath, normalized.DiffBase, normalized.DiffHead, normalized.DiffMode)
		if pathsErr != nil {
			message := pathsErr.Error()
			_, _ = m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryFailed, &message)
			return recorded, nil, fmt.Errorf("webhooks: changed paths: %w", pathsErr)
		}
		event.ChangedPaths = paths
		event.PathsKnown = true
	}
	matches := len(definition.TriggerPolicies) == 0 && len(definition.Triggers) == 0
	if !matches {
		matches = triggerpolicy.Match(definition.TriggerPolicies, definition.Triggers, event)
	}
	if !matches {
		accepted, transitionErr := m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryAccepted, nil)
		if transitionErr != nil {
			return recorded, nil, transitionErr
		}
		recorded.Delivery = accepted
		return recorded, nil, nil
	}
	run, err := m.enqueuer.EnqueueTriggered(ctx, config.WorkflowID, normalized.RunRef, normalized.CommitSHA, "webhook")
	if err != nil {
		message := err.Error()
		_, _ = m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryFailed, &message)
		return recorded, nil, fmt.Errorf("webhooks: enqueue: %w", err)
	}
	accepted, err := m.store.TransitionWebhookDelivery(ctx, recorded.Delivery.ID, store.WebhookDeliveryAccepted, nil)
	if err != nil {
		return recorded, &run, err
	}
	recorded.Delivery = accepted
	return recorded, &run, nil
}

type normalizedWebhookPayload struct {
	Event     triggerpolicy.Event
	RunRef    string
	CommitSHA string
	DiffBase  string
	DiffHead  string
	DiffMode  gitrepository.DiffMode
}

func normalizeWebhookPayload(provider, eventType, fallback string, payload []byte) normalizedWebhookPayload {
	normalized := normalizedWebhookPayload{Event: triggerpolicy.Event{Type: normalizeWebhookEvent(provider, eventType)}, RunRef: strings.TrimSpace(fallback)}
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return normalized
	}
	normalized.Event.Ref = stringValue(value, "ref")
	normalized.Event.Action = strings.ToLower(strings.TrimSpace(stringValue(value, "action")))
	if normalized.Event.Ref != "" {
		normalized.RunRef = normalized.Event.Ref
	}
	normalized.CommitSHA = stringValue(value, "after")
	normalized.DiffBase = stringValue(value, "before")
	normalized.DiffHead = normalized.CommitSHA
	normalized.DiffMode = gitrepository.DiffDirect
	collectChangedPaths(&normalized.Event, value)
	if commits, present := value["commits"].([]any); present {
		count := numericValue(value, "size")
		if provider == "gitlab" {
			count = numericValue(value, "total_commits_count")
		}
		if count > len(commits) {
			normalized.Event.PathsKnown = false
			normalized.Event.ChangedPaths = nil
		}
	}

	if provider == "github" && normalized.Event.Type == "pull_request" {
		pullRequest := objectValue(value, "pull_request")
		base := objectValue(pullRequest, "base")
		head := objectValue(pullRequest, "head")
		normalized.Event.Ref = branchRef(stringValue(base, "ref"))
		normalized.RunRef = branchRef(stringValue(head, "ref"))
		if normalized.RunRef == "" {
			normalized.RunRef = strings.TrimSpace(fallback)
		}
		normalized.CommitSHA = stringValue(head, "sha")
		normalized.DiffBase = stringValue(base, "sha")
		normalized.DiffHead = normalized.CommitSHA
		normalized.DiffMode = gitrepository.DiffMergeBase
		normalized.Event.PathsKnown = false
		normalized.Event.ChangedPaths = nil
	}

	if provider == "gitlab" {
		if kind, ok := value["object_kind"].(string); ok && strings.TrimSpace(kind) != "" {
			normalized.Event.Type = normalizeWebhookEvent(provider, kind)
		}
		if attributes := objectValue(value, "object_attributes"); attributes != nil {
			if action, ok := attributes["action"].(string); ok {
				normalized.Event.Action = normalizeMergeRequestAction(action)
			}
			if normalized.Event.Type == "pull_request" {
				normalized.Event.Ref = branchRef(stringValue(attributes, "target_branch"))
				normalized.RunRef = branchRef(stringValue(attributes, "source_branch"))
				if normalized.RunRef == "" {
					normalized.RunRef = strings.TrimSpace(fallback)
				}
				normalized.CommitSHA = stringValue(objectValue(attributes, "last_commit"), "id")
				normalized.Event.PathsKnown = false
				normalized.Event.ChangedPaths = nil
			}
		}
		if normalized.Event.Type == "push" {
			if sha := stringValue(value, "checkout_sha"); sha != "" {
				normalized.CommitSHA = sha
				normalized.DiffHead = sha
			}
		}
	}
	return normalized
}

func webhookRef(provider, fallback string, payload []byte) (string, string) {
	normalized := normalizeWebhookPayload(provider, "", fallback, payload)
	return normalized.RunRef, normalized.CommitSHA
}

func webhookEvent(provider, eventType string, payload []byte) triggerpolicy.Event {
	return normalizeWebhookPayload(provider, eventType, "", payload).Event
}

func collectChangedPaths(event *triggerpolicy.Event, value map[string]any) {
	commits, present := value["commits"].([]any)
	event.PathsKnown = present
	seen := make(map[string]struct{})
	for _, rawCommit := range commits {
		commit, ok := rawCommit.(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"added", "modified", "removed"} {
			paths, _ := commit[field].([]any)
			for _, rawPath := range paths {
				path, _ := rawPath.(string)
				if path == "" {
					continue
				}
				if _, exists := seen[path]; !exists {
					seen[path] = struct{}{}
					event.ChangedPaths = append(event.ChangedPaths, path)
				}
			}
		}
	}
}

func objectValue(value map[string]any, key string) map[string]any {
	if value == nil {
		return nil
	}
	object, _ := value[key].(map[string]any)
	return object
}

func stringValue(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}

func numericValue(value map[string]any, key string) int {
	if value == nil {
		return 0
	}
	switch number := value[key].(type) {
	case float64:
		return int(number)
	case json.Number:
		parsed, _ := strconv.Atoi(number.String())
		return parsed
	}
	return 0
}

func branchRef(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return ""
	}
	if strings.HasPrefix(branch, "refs/heads/") {
		return branch
	}
	return "refs/heads/" + branch
}

func normalizeMergeRequestAction(action string) string {
	switch action = strings.ToLower(strings.TrimSpace(action)); action {
	case "open":
		return "opened"
	case "update":
		return "synchronize"
	case "reopen":
		return "reopened"
	case "close", "merge":
		return "closed"
	default:
		return action
	}
}

func normalizeWebhookEvent(provider, eventType string) string {
	event := strings.ToLower(strings.TrimSpace(eventType))
	event = strings.TrimSuffix(event, " hook")
	event = strings.ReplaceAll(event, " ", "_")
	switch event {
	case "push", "tag_push":
		return "push"
	case "pull_request", "merge_request", "merge_request_event":
		return "pull_request"
	case "pipeline", "pipeline_event":
		if provider == "gitlab" {
			return "push"
		}
	}
	return event
}
