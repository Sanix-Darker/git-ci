package triggerpolicy

import "testing"

func TestMatchBranchPathAndActionFilters(t *testing.T) {
	policies := []Policy{{Event: "push", Branches: []string{"main", "release/**"}, Paths: []string{"src/**", "!src/generated/**"}, Evaluable: true}}
	if !Match(policies, nil, Event{Type: "push", Ref: "refs/heads/main", ChangedPaths: []string{"src/app.go"}, PathsKnown: true}) {
		t.Fatal("matching push was rejected")
	}
	if Match(policies, nil, Event{Type: "push", Ref: "refs/heads/main", ChangedPaths: []string{"docs/readme.md"}, PathsKnown: true}) {
		t.Fatal("non-matching path was accepted")
	}
	if Match(policies, nil, Event{Type: "push", Ref: "refs/heads/feature", ChangedPaths: []string{"src/app.go"}, PathsKnown: true}) {
		t.Fatal("non-matching branch was accepted")
	}
	pr := []Policy{{Event: "pull_request", Actions: []string{"opened", "synchronize"}, Evaluable: true}}
	if !Match(pr, nil, Event{Type: "pull_request", Action: "opened", Ref: "refs/heads/topic"}) || Match(pr, nil, Event{Type: "pull_request", Action: "closed", Ref: "refs/heads/topic"}) {
		t.Fatal("pull request action filter mismatch")
	}
}

func TestResolveManualInputs(t *testing.T) {
	policies := []Policy{{Event: "workflow_dispatch", Evaluable: true, Inputs: []Input{
		{Name: "target", Type: "choice", Required: true, Options: []string{"staging", "production"}},
		{Name: "dry_run", Type: "boolean", Default: "false"},
	}}}
	values, err := ResolveManualInputs(policies, map[string]string{"target": "production"})
	if err != nil || values["target"] != "production" || values["dry_run"] != "false" {
		t.Fatalf("values=%v err=%v", values, err)
	}
	if _, err := ResolveManualInputs(policies, map[string]string{"target": "invalid"}); err == nil {
		t.Fatal("invalid choice was accepted")
	}
}
