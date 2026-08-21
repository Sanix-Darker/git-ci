package executionsemantics

import (
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestExpandGitHubMatrixIncludeExclude(t *testing.T) {
	job := &types.Job{Strategy: &types.Strategy{
		Matrix: map[string][]interface{}{
			"animal": {"cat", "dog"},
			"fruit":  {"apple", "pear"},
		},
		Exclude: []map[string]interface{}{{"fruit": "pear", "animal": "dog"}},
		Include: []map[string]interface{}{
			{"color": "green"},
			{"animal": "cat", "color": "pink"},
			{"fruit": "banana"},
		},
	}}
	variants, err := ExpandMatrix(job)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 4 {
		t.Fatalf("expected three filtered products plus standalone include, got %#v", variants)
	}
	if variants[0].Values["animal"] != "cat" || variants[0].Values["color"] != "pink" {
		t.Fatalf("ordered include was not applied: %#v", variants[0])
	}
	if variants[3].Values["fruit"] != "banana" {
		t.Fatalf("standalone include missing: %#v", variants[3])
	}
}

func TestExpandGitLabParallelMatrixAndEnvironment(t *testing.T) {
	job := &types.Job{Parallel: &types.Parallel{Matrix: []map[string]interface{}{
		{"PROVIDER": "aws", "STACK": []interface{}{"monitoring", "app"}},
		{"PROVIDER": []interface{}{"gcp", "vultr"}, "STACK": []interface{}{"data", "processing"}},
	}}}
	variants, err := ExpandMatrix(job)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 6 {
		t.Fatalf("expected six GitLab variants, got %d", len(variants))
	}
	environment, err := MatrixEnvironment(variants[5], "gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if environment["PROVIDER"] != "vultr" || environment["MATRIX_STACK"] != "processing" || environment["CI_NODE_TOTAL"] != "6" {
		t.Fatalf("unexpected GitLab matrix environment: %#v", environment)
	}
}

func TestMatrixBoundsAndDuplicateFailClosed(t *testing.T) {
	values := make([]interface{}, MaxMatrixVariants+1)
	for index := range values {
		values[index] = index
	}
	if _, err := ExpandMatrix(&types.Job{Strategy: &types.Strategy{Matrix: map[string][]interface{}{"version": values}}}); err == nil {
		t.Fatal("expected oversized matrix to fail")
	}
	if _, err := ExpandMatrix(&types.Job{Strategy: &types.Strategy{Include: []map[string]interface{}{{"version": 1}, {"version": 1}}}}); err == nil {
		t.Fatal("expected duplicate standalone include to fail")
	}
}

func TestConditionEvaluationSubset(t *testing.T) {
	context := ConditionContext{
		Values: map[string]interface{}{
			"github.ref":        "refs/heads/Main",
			"github.event_name": "push",
			"matrix.version":    22,
		},
		Success: true, CaseInsensitive: true,
	}
	expression := "success() && github.ref == 'refs/heads/main' && (matrix.version >= 20 || github.event_name != 'push')"
	contract := CompileCondition(expression)
	if !contract.Evaluable {
		t.Fatalf("expected evaluable contract: %#v", contract)
	}
	result, err := EvaluateCondition(expression, context)
	if err != nil || !result {
		t.Fatalf("expected true condition, result=%v err=%v", result, err)
	}
	result, err = EvaluateCondition("failure() || contains(github.ref, 'release')", context)
	if err != nil || result {
		t.Fatalf("expected false condition, result=%v err=%v", result, err)
	}
}

func TestConditionUnsupportedSyntaxAndMissingContextFailClosed(t *testing.T) {
	contract := CompileCondition("github.ref ~= 'refs/heads/main'")
	if contract.Evaluable || !strings.Contains(contract.Diagnostic, "unsupported") {
		t.Fatalf("unknown syntax should be diagnosed: %#v", contract)
	}
	if result, err := EvaluateCondition("inputs.target == 'prod'", ConditionContext{}); err == nil || result {
		t.Fatalf("missing context should fail closed, result=%v err=%v", result, err)
	}
}

func TestStaticTemplateAndConcurrencyNormalization(t *testing.T) {
	resolved, err := ResolveStaticTemplate("test-${{ matrix.os }}-${{ matrix.version }}", map[string]string{
		"matrix.os": "linux", "matrix.version": "22",
	})
	if err != nil || resolved != "test-linux-22" {
		t.Fatalf("unexpected template result %q, err=%v", resolved, err)
	}
	if _, err := ResolveStaticTemplate("${{ needs.build.outputs.matrix }}", nil); err == nil {
		t.Fatal("dynamic context should fail closed")
	}
	partial, err := ResolveMatrixTemplate("${{ matrix.os }} / ${{ secrets.TOKEN }}", map[string]string{"os": "linux"})
	if err != nil || partial != "linux / ${{ secrets.TOKEN }}" {
		t.Fatalf("matrix-only substitution changed runtime context: %q, err=%v", partial, err)
	}
	group, err := NormalizeConcurrencyGroup(" Deploy/Main ")
	if err != nil || group != "deploy/main" {
		t.Fatalf("unexpected concurrency group %q, err=%v", group, err)
	}
}
