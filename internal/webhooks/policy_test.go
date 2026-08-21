package webhooks

import (
	"testing"

	"github.com/sanix-darker/git-ci/internal/gitrepository"
)

func TestWebhookEventNormalizesGitLabMergeRequest(t *testing.T) {
	payload := []byte(`{"object_kind":"merge_request","object_attributes":{"action":"open","target_branch":"main","source_branch":"feature/policy","last_commit":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`)
	event := webhookEvent("gitlab", "Merge Request Hook", payload)
	if event.Type != "pull_request" || event.Action != "opened" || event.Ref != "refs/heads/main" {
		t.Fatalf("unexpected normalized event: %#v", event)
	}
	ref, commit := webhookRef("gitlab", "refs/heads/fallback", payload)
	if ref != "refs/heads/feature/policy" || commit != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("unexpected GitLab run target: ref=%q commit=%q", ref, commit)
	}
}

func TestWebhookEventNormalizesGitHubPullRequest(t *testing.T) {
	payload := []byte(`{"action":"synchronize","pull_request":{"base":{"ref":"main","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"head":{"ref":"feature/policy","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}`)
	normalized := normalizeWebhookPayload("github", "pull_request", "refs/heads/fallback", payload)
	if normalized.Event.Type != "pull_request" || normalized.Event.Action != "synchronize" || normalized.Event.Ref != "refs/heads/main" {
		t.Fatalf("unexpected normalized event: %#v", normalized.Event)
	}
	if normalized.RunRef != "refs/heads/feature/policy" || normalized.CommitSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || normalized.DiffBase != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || normalized.DiffMode != gitrepository.DiffMergeBase {
		t.Fatalf("unexpected normalized payload: %#v", normalized)
	}
}
