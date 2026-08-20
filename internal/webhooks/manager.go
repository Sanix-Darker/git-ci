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
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
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
	ref, commit := webhookRef(endpoint.Provider, config.Ref, payload)
	run, err := m.enqueuer.EnqueueTriggered(ctx, config.WorkflowID, ref, commit, "webhook")
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

func webhookRef(provider, fallback string, payload []byte) (string, string) {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return fallback, ""
	}
	ref, _ := value["ref"].(string)
	if ref == "" {
		ref = fallback
	}
	commit, _ := value["after"].(string)
	if provider == "gitlab" {
		if sha, ok := value["checkout_sha"].(string); ok {
			commit = sha
		}
	}
	return ref, commit
}
