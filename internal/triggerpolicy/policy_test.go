package triggerpolicy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

func TestGitHubWorkflowRunParsesAndMatchesWorkflowActivityAndBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.yml")
	contents := `name: Delivery
on:
  workflow_run:
    workflows: [CI, Security]
    types: [completed]
    branches: [main, release/**]
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: true
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	policies, err := ParseFile("github", path, nil)
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if len(policies) != 1 || !reflect.DeepEqual(policies[0].Workflows, []string{"CI", "Security"}) || !reflect.DeepEqual(policies[0].Actions, []string{"completed"}) {
		t.Fatalf("workflow_run policies = %#v", policies)
	}
	for _, event := range []Event{
		{Type: "workflow_run", Workflow: "CI", Action: "completed", Ref: "refs/heads/main"},
		{Type: "workflow_run", Workflow: "Security", Action: "completed", Ref: "refs/heads/release/1.2"},
	} {
		if !Match(policies, nil, event) {
			t.Errorf("Match(%#v) = false, want true", event)
		}
	}
	for _, event := range []Event{
		{Type: "workflow_run", Workflow: "Build", Action: "completed", Ref: "refs/heads/main"},
		{Type: "workflow_run", Workflow: "CI", Action: "requested", Ref: "refs/heads/main"},
		{Type: "workflow_run", Workflow: "CI", Action: "completed", Ref: "refs/heads/develop"},
	} {
		if Match(policies, nil, event) {
			t.Errorf("Match(%#v) = true, want false", event)
		}
	}
	if Match([]Policy{{Event: "workflow_run", Evaluable: true}}, nil, Event{Type: "workflow_run", Workflow: "CI", Action: "completed", Ref: "refs/heads/main"}) {
		t.Fatal("workflow_run without an explicit workflows list matched")
	}
}
