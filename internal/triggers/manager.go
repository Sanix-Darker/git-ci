package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sanix-darker/git-ci/internal/store"
)

const defaultPollInterval = time.Second

type Enqueuer interface {
	SyncProject(context.Context, string) ([]store.Workflow, error)
	EnqueueTriggered(context.Context, string, string, string, string) (store.Run, error)
}

type Manager struct {
	store        *store.Store
	enqueuer     Enqueuer
	pollInterval time.Duration
	wake         chan struct{}
}

type Option func(*Manager)

func WithPollInterval(interval time.Duration) Option {
	return func(manager *Manager) {
		if interval > 0 {
			manager.pollInterval = interval
		}
	}
}

func NewManager(database *store.Store, enqueuer Enqueuer, options ...Option) (*Manager, error) {
	if database == nil || enqueuer == nil {
		return nil, errors.New("triggers: store and enqueuer are required")
	}
	manager := &Manager{store: database, enqueuer: enqueuer, pollInterval: defaultPollInterval, wake: make(chan struct{}, 1)}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager, nil
}

func (m *Manager) Configure(ctx context.Context, projectID, ref string, enabled bool) (store.ProjectCommitTrigger, error) {
	project, err := m.store.GetProject(ctx, projectID)
	if err != nil {
		return store.ProjectCommitTrigger{}, err
	}
	branch, err := normalizeBranch(ref, project.DefaultBranch)
	if err != nil {
		return store.ProjectCommitTrigger{}, err
	}
	var baseline *string
	if enabled {
		if project.CanonicalPath == nil || strings.TrimSpace(*project.CanonicalPath) == "" {
			return store.ProjectCommitTrigger{}, errors.New("triggers: project has no local checkout")
		}
		sha, err := resolveBranchCommit(ctx, *project.CanonicalPath, branch)
		if err != nil {
			return store.ProjectCommitTrigger{}, err
		}
		baseline = &sha
	}
	policy, err := m.store.UpsertProjectCommitTrigger(ctx, store.UpsertProjectCommitTriggerParams{
		ProjectID: project.ID, Ref: branch, Enabled: enabled, LastCommitSHA: baseline,
	})
	if err == nil {
		m.Notify()
	}
	return policy, err
}

func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		if err := m.Process(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

func (m *Manager) Process(ctx context.Context) error {
	policies, err := m.store.ListEnabledProjectCommitTriggers(ctx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if err := m.processPolicy(ctx, policy); err != nil {
			message := err.Error()
			if recordErr := m.store.RecordProjectCommitTriggerCheck(ctx, policy.ProjectID, nil, false, &message); recordErr != nil {
				return errors.Join(err, recordErr)
			}
		}
	}
	return nil
}

func (m *Manager) processPolicy(ctx context.Context, policy store.ProjectCommitTrigger) error {
	project, err := m.store.GetProject(ctx, policy.ProjectID)
	if err != nil {
		return err
	}
	if !project.Active || project.CanonicalPath == nil || strings.TrimSpace(*project.CanonicalPath) == "" {
		return errors.New("triggers: project checkout is not active")
	}
	sha, err := resolveBranchCommit(ctx, *project.CanonicalPath, policy.Ref)
	if err != nil {
		return err
	}
	if policy.LastCommitSHA == nil || strings.TrimSpace(*policy.LastCommitSHA) == "" {
		return m.store.RecordProjectCommitTriggerCheck(ctx, policy.ProjectID, &sha, false, nil)
	}
	if sha == *policy.LastCommitSHA {
		return m.store.RecordProjectCommitTriggerCheck(ctx, policy.ProjectID, &sha, false, nil)
	}
	workflows, err := m.enqueuer.SyncProject(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("triggers: sync workflows: %w", err)
	}
	triggered := false
	for _, workflow := range workflows {
		accepts, err := acceptsCommit(workflow.Definition)
		if err != nil {
			return fmt.Errorf("triggers: inspect workflow %q: %w", workflow.Key, err)
		}
		if !accepts {
			continue
		}
		exists, err := m.store.CommitTriggeredRunExists(ctx, workflow.ID, sha)
		if err != nil {
			return err
		}
		if exists {
			triggered = true
			continue
		}
		if _, err := m.enqueuer.EnqueueTriggered(ctx, workflow.ID, "refs/heads/"+policy.Ref, sha, "commit"); err != nil {
			return fmt.Errorf("triggers: enqueue workflow %q: %w", workflow.Key, err)
		}
		triggered = true
	}
	return m.store.RecordProjectCommitTriggerCheck(ctx, policy.ProjectID, &sha, triggered, nil)
}

func (m *Manager) Notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func acceptsCommit(raw []byte) (bool, error) {
	var definition struct {
		Triggers []string `json:"triggers"`
	}
	if err := json.Unmarshal(raw, &definition); err != nil {
		return false, err
	}
	for _, trigger := range definition.Triggers {
		switch strings.ToLower(strings.TrimSpace(trigger)) {
		case "push", "commit":
			return true, nil
		}
	}
	return false, nil
}

func normalizeBranch(ref, fallback string) (string, error) {
	branch := strings.TrimSpace(ref)
	if branch == "" {
		branch = strings.TrimSpace(fallback)
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" || strings.HasPrefix(branch, "-") {
		return "", errors.New("triggers: branch is invalid")
	}
	command := exec.Command("git", "check-ref-format", "--branch", branch)
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("triggers: invalid branch %q: %s", branch, strings.TrimSpace(string(output)))
	}
	return branch, nil
}

func resolveBranchCommit(ctx context.Context, path, branch string) (string, error) {
	ref := "refs/heads/" + branch + "^{commit}"
	command := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--verify", "--end-of-options", ref)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("triggers: resolve branch %q: %s", branch, strings.TrimSpace(string(output)))
	}
	sha := strings.TrimSpace(string(output))
	if len(sha) != 40 && len(sha) != 64 {
		return "", errors.New("triggers: Git returned an invalid commit SHA")
	}
	return sha, nil
}
