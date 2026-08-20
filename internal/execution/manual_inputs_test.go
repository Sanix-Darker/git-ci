package execution

import (
	"encoding/json"
	"testing"

	"github.com/sanix-darker/git-ci/internal/triggerpolicy"
)

func TestApplyManualInputsFreezesValidatedValues(t *testing.T) {
	policies := []triggerpolicy.Policy{{
		Event: "workflow_dispatch", Evaluable: true,
		Inputs: []triggerpolicy.Input{
			{Name: "target", Type: "choice", Required: true, Default: "staging", Options: []string{"staging", "production"}},
			{Name: "dry-run", Type: "boolean", Default: "false"},
		},
	}}
	raw, err := applyManualInputs([]byte(`{"BASE":"stable"}`), policies, map[string]string{"target": "production", "dry-run": "true"})
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err := json.Unmarshal(raw, &environment); err != nil {
		t.Fatal(err)
	}
	if environment["BASE"] != "stable" || environment["INPUT_TARGET"] != "production" || environment["INPUT_DRY_RUN"] != "true" {
		t.Fatalf("unexpected immutable input environment: %#v", environment)
	}
	if _, err := applyManualInputs(nil, policies, map[string]string{"target": "invalid"}); err == nil {
		t.Fatal("expected invalid choice to be rejected")
	}
}
