package executionsemantics

import (
	"strings"
	"testing"
)

func TestGitLabRegexConditions(t *testing.T) {
	context := ConditionContext{Values: map[string]interface{}{
		"CI_COMMIT_BRANCH": "release/v25",
		"PATTERN":          `/^release\/v[0-9]+$/`,
		"OTHER":            "main",
	}}
	tests := []struct {
		expression string
		want       bool
	}{
		{`$CI_COMMIT_BRANCH =~ /^release\/v[0-9]+$/`, true},
		{`$CI_COMMIT_BRANCH !~ /^feature/`, true},
		{`$CI_COMMIT_BRANCH =~ /^RELEASE/i`, true},
		{`$CI_COMMIT_BRANCH =~ $PATTERN`, true},
		{`($CI_COMMIT_BRANCH =~ /^release/ && $OTHER == "main") || $OTHER == "never"`, true},
		{`$CI_COMMIT_BRANCH =~ /^feature/ || $OTHER != "main"`, false},
	}
	for _, test := range tests {
		contract := CompileCondition(test.expression)
		if !contract.Evaluable {
			t.Fatalf("%q contract = %#v", test.expression, contract)
		}
		got, err := EvaluateCondition(test.expression, context)
		if err != nil || got != test.want {
			t.Errorf("%q = %t, %v; want %t", test.expression, got, err, test.want)
		}
	}
}

func TestGitLabRegexConditionsFailClosed(t *testing.T) {
	oversized := "/" + strings.Repeat("a", maxConditionRegexBytes) + "/"
	for _, expression := range []string{
		`$VALUE =~ /./`,
		`$VALUE =~ /[/`,
		`$VALUE =~ /valid/z`,
		`$VALUE =~ /unterminated`,
		`$VALUE =~ ` + oversized,
	} {
		if contract := CompileCondition(expression); contract.Evaluable || contract.Diagnostic == "" {
			t.Errorf("invalid regex %q compiled as %#v", expression, contract)
		}
	}
	if matched, err := EvaluateCondition(`$VALUE =~ $PATTERN`, ConditionContext{Values: map[string]interface{}{"VALUE": "main", "PATTERN": "main"}}); err == nil || matched {
		t.Fatalf("invalid variable regex = %t, %v", matched, err)
	}
}
