package triggers

import (
	"encoding/json"
	"testing"

	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
)

func TestAcceptsCommitEventAppliesRefAndPathPolicy(t *testing.T) {
	raw, err := json.Marshal(struct {
		Policies []triggerpolicy.Policy `json:"triggerPolicies"`
	}{Policies: []triggerpolicy.Policy{{Event: "push", Branches: []string{"main"}, Paths: []string{"cmd/**"}, Evaluable: true}}})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := acceptsCommitEvent(raw, triggerpolicy.Event{Type: "push", Ref: "refs/heads/main", ChangedPaths: []string{"cmd/gci/main.go"}, PathsKnown: true})
	if err != nil || !accepted {
		t.Fatalf("expected matching commit, accepted=%v err=%v", accepted, err)
	}
	rejected, err := acceptsCommitEvent(raw, triggerpolicy.Event{Type: "push", Ref: "refs/heads/main", ChangedPaths: []string{"README.md"}, PathsKnown: true})
	if err != nil || rejected {
		t.Fatalf("expected path-filtered commit rejection, accepted=%v err=%v", rejected, err)
	}
}
