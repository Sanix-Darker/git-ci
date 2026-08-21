package triggerpolicy

import "testing"

func TestMatchAppliesPullRequestDefaultsAndTargetBranch(t *testing.T) {
	policies := []Policy{{Event: "pull_request", Branches: []string{"main"}, Evaluable: true}}
	for _, action := range []string{"opened", "synchronize", "reopened"} {
		if !Match(policies, nil, Event{Type: "pull_request", Action: action, Ref: "refs/heads/main"}) {
			t.Fatalf("default pull-request action %q did not match", action)
		}
	}
	for _, event := range []Event{{Type: "pull_request", Action: "closed", Ref: "refs/heads/main"}, {Type: "pull_request", Action: "opened", Ref: "refs/heads/develop"}, {Type: "pull_request", Action: "opened"}} {
		if Match(policies, nil, event) {
			t.Fatalf("unexpected pull-request match: %#v", event)
		}
	}
}

func TestMatchDoesNotEvaluatePathFiltersForTagPushes(t *testing.T) {
	policies := []Policy{{Event: "push", Tags: []string{"v*"}, Paths: []string{"src/**"}, Evaluable: true}}
	if !Match(policies, nil, Event{Type: "push", Ref: "refs/tags/v1.0.0", PathsKnown: false}) {
		t.Fatal("tag push incorrectly required changed paths")
	}
	if NeedsChangedPaths(policies, nil, Event{Type: "push", Ref: "refs/tags/v1.0.0"}) {
		t.Fatal("tag push requested changed paths")
	}
}

func TestNeedsChangedPathsAppliesNonPathAdmissionFirst(t *testing.T) {
	policies := []Policy{{Event: "pull_request", Branches: []string{"main"}, Paths: []string{"src/**"}, Evaluable: true}}
	if !NeedsChangedPaths(policies, nil, Event{Type: "pull_request", Action: "opened", Ref: "refs/heads/main"}) {
		t.Fatal("matching pull request did not request paths")
	}
	if NeedsChangedPaths(policies, nil, Event{Type: "pull_request", Action: "closed", Ref: "refs/heads/main"}) {
		t.Fatal("ignored action requested paths")
	}
	if NeedsChangedPaths(policies, nil, Event{Type: "pull_request", Action: "opened", Ref: "refs/heads/develop"}) {
		t.Fatal("ignored target branch requested paths")
	}
}
