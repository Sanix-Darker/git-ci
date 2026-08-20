package parsers

import "testing"

func TestGitLabParseEnvironment(t *testing.T) {
	parser := &GitlabParser{}
	tests := []struct {
		name     string
		value    interface{}
		wantName string
		wantTier string
	}{
		{name: "string", value: "production", wantName: "production"},
		{name: "object", value: map[string]interface{}{"name": "review/app", "deployment_tier": "development"}, wantName: "review/app", wantTier: "development"},
		{name: "object without name", value: map[string]interface{}{"deployment_tier": "production"}},
		{name: "invalid", value: []interface{}{"production"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, tier := parser.parseEnvironment(test.value)
			if name != test.wantName || tier != test.wantTier {
				t.Fatalf("parseEnvironment() = (%q, %q), want (%q, %q)", name, tier, test.wantName, test.wantTier)
			}
		})
	}
}
