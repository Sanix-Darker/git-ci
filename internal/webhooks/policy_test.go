package webhooks

import "testing"

func TestWebhookEventNormalizesGitLabMergeRequest(t *testing.T) {
	event := webhookEvent("gitlab", "Merge Request Hook", []byte(`{"object_kind":"merge_request","object_attributes":{"action":"open","source_branch":"feature/policy"}}`))
	if event.Type != "pull_request" || event.Action != "open" || event.Ref != "refs/heads/feature/policy" {
		t.Fatalf("unexpected normalized event: %#v", event)
	}
}
