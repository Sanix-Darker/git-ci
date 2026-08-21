package executionsemantics

import "testing"

func TestEvaluateContinueOnErrorPerMatrixVariant(t *testing.T) {
	for _, test := range []struct {
		name   string
		expr   string
		matrix map[string]string
		want   bool
	}{
		{name: "experimental", expr: "${{ matrix.experimental }}", matrix: map[string]string{"experimental": "true"}, want: true},
		{name: "stable", expr: "${{ matrix.experimental }}", matrix: map[string]string{"experimental": "false"}, want: false},
		{name: "comparison", expr: "${{ matrix.version >= 22 && matrix.channel == 'edge' }}", matrix: map[string]string{"version": "22", "channel": "edge"}, want: true},
		{name: "static", expr: "${{ false }}", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := EvaluateContinueOnError(test.expr, test.matrix)
			if err != nil || got != test.want {
				t.Fatalf("EvaluateContinueOnError() = %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestEvaluateContinueOnErrorFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		expr   string
		matrix map[string]string
	}{
		{name: "missing", expr: "${{ matrix.experimental }}"},
		{name: "non-boolean", expr: "${{ matrix.experimental }}", matrix: map[string]string{"experimental": "sometimes"}},
		{name: "malformed", expr: "${{ matrix.experimental ~= true }}", matrix: map[string]string{"experimental": "true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := EvaluateContinueOnError(test.expr, test.matrix); err == nil || got {
				t.Fatalf("expected fail-closed result, got %v, %v", got, err)
			}
		})
	}
}
