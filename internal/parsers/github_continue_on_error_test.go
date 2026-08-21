package parsers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGithubContinueOnErrorPreservesMatrixExpression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "continue.yml")
	contents := []byte(`name: Continue on error
on: [push]
jobs:
  matrix:
    runs-on: ubuntu-latest
    continue-on-error: ${{ matrix.experimental }}
    strategy:
      matrix:
        experimental: [false, true]
    steps:
      - run: exit 1
  literal:
    runs-on: ubuntu-latest
    continue-on-error: true
    steps:
      - run: exit 1
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline, err := NewGithubParser().Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	matrix := pipeline.Jobs["matrix"]
	if matrix == nil || matrix.ContinueOnErr || matrix.ContinueOnErrorExpression != "${{ matrix.experimental }}" {
		t.Fatalf("matrix continue-on-error = %#v", matrix)
	}
	literal := pipeline.Jobs["literal"]
	if literal == nil || !literal.ContinueOnErr || literal.ContinueOnErrorExpression != "" {
		t.Fatalf("literal continue-on-error = %#v", literal)
	}
}

func TestGithubContinueOnErrorRejectsNonBooleanNonExpressionValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yml")
	contents := []byte("name: Invalid\non: [push]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    continue-on-error: 1\n    steps: [{run: 'exit 1'}]\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewGithubParser().Parse(path)
	if err == nil || !strings.Contains(err.Error(), "continue-on-error") {
		t.Fatalf("invalid continue-on-error error = %v", err)
	}
}
