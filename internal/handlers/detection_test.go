package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectParser_PathBasedGitHub(t *testing.T) {
	parser := detectParser(".github/workflows/ci.yml")
	if parser == nil || parser.GetProviderName() != "github" {
		t.Fatalf("expected github parser, got %#v", parser)
	}
}

func TestDetectParser_PathBasedGitLab(t *testing.T) {
	parser := detectParser(".gitlab-ci.yml")
	if parser == nil || parser.GetProviderName() != "gitlab" {
		t.Fatalf("expected gitlab parser, got %#v", parser)
	}
}

func TestDetectParser_PathBasedCircleCI(t *testing.T) {
	parser := detectParser(".circleci/config.yml")
	if parser == nil || parser.GetProviderName() != "circleci" {
		t.Fatalf("expected circleci parser, got %#v", parser)
	}
}

func TestDetectParser_PathBasedDrone(t *testing.T) {
	parser := detectParser(".drone.yml")
	if parser == nil || parser.GetProviderName() != "drone" {
		t.Fatalf("expected drone parser, got %#v", parser)
	}
}

func TestDetectParser_PathBasedTravis(t *testing.T) {
	parser := detectParser(".travis.yml")
	if parser == nil || parser.GetProviderName() != "travis" {
		t.Fatalf("expected travis parser, got %#v", parser)
	}
}

func TestDetectParser_PathBasedBitbucketFallbackToGitHub(t *testing.T) {
	parser := detectParser("bitbucket-pipelines.yml")
	if parser == nil || parser.GetProviderName() != "github" {
		t.Fatalf("expected github fallback parser for bitbucket file name, got %#v", parser)
	}
}

func TestDetectParser_PathBasedAzureFallbackToGitHub(t *testing.T) {
	parser := detectParser("azure-pipelines.yml")
	if parser == nil || parser.GetProviderName() != "github" {
		t.Fatalf("expected github fallback parser for azure file name, got %#v", parser)
	}
}

func TestDetectParser_ContentBasedCircleCI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weird-ci.yml")
	if err := os.WriteFile(path, []byte(`version: 2.1
orbs:
  go: circleci/go@3.2.1
executors:
  default:
    docker:
      - image: cimg/base:stable
jobs:
  test:
    executor: default
    steps:
      - checkout
      - run:
          name: test
          command: echo hi
workflows:
  version: 2
  ci:
    jobs:
      - test
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pipeline, err := parseInput(path)
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "circleci" {
		t.Fatalf("expected provider circleci from content markers, got %q", pipeline.Provider)
	}
}

func TestDetectParser_ContentBasedDrone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yml")
	if err := os.WriteFile(path, []byte(`kind: pipeline
steps:
  - name: test
    image: golang:1.22
    commands:
      - go test ./...
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pipeline, err := parseInput(path)
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "drone" {
		t.Fatalf("expected provider drone from content markers, got %q", pipeline.Provider)
	}
}

func TestDetectParser_ContentBasedTravis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yml")
	if err := os.WriteFile(path, []byte(`language: go
script:
  - go test ./...
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pipeline, err := parseInput(path)
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "travis" {
		t.Fatalf("expected provider travis from content markers, got %q", pipeline.Provider)
	}
}

func TestDetectParser_ContentBasedGitHub(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yml")
	if err := os.WriteFile(path, []byte(`name: detect-github
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pipeline, err := parseInput(path)
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "github" {
		t.Fatalf("expected provider github from content markers, got %q", pipeline.Provider)
	}
}

func TestDetectParser_ContentBasedGitLabFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.yml")
	if err := os.WriteFile(path, []byte(`stages:
  - test
test:
  stage: test
  script:
    - echo hi
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pipeline, err := parseInput(path)
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "gitlab" {
		t.Fatalf("expected provider gitlab from content markers, got %q", pipeline.Provider)
	}
}

func TestDetectParser_FallbackToGitHubForUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(path, []byte(`steps:
  - run: echo hi
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	parser := detectParser(path)
	if parser == nil || parser.GetProviderName() != "github" {
		t.Fatalf("expected github fallback parser, got %#v", parser)
	}

	pipeline, err := parseInput(path)
	if err == nil {
		t.Fatalf("expected parse failure for unknown schema when fallback parser validates, got %#v", pipeline)
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("expected fallback github validation failure, got %v", err)
	}
}

func TestDetectParser_FallbackToGitHubForUnrecognizableContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(path, []byte(`not-a-known-schema: true
still-not-known: yes
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	parser := detectParser(path)
	if parser == nil || parser.GetProviderName() != "github" {
		t.Fatalf("expected fallback github parser for unrecognized content, got %#v", parser)
	}

	_, err := parser.Parse(path)
	if err == nil {
		t.Fatalf("expected github parser to fail on unrecognized content")
	}
}

func TestParseInputWithProvider_ForcesRequestedParser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipeline.yml")
	if err := os.WriteFile(path, []byte(`language: go
script:
  - go test ./...
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	autoPipeline, err := parseInputWithProvider(path, "auto")
	if err != nil {
		t.Fatalf("parseInputWithProvider(auto): %v", err)
	}
	if autoPipeline.Provider != "travis" {
		t.Fatalf("expected auto provider detection to infer travis, got %q", autoPipeline.Provider)
	}

	_, err = parseInputWithProvider(path, "github")
	if err == nil {
		t.Fatalf("expected forced github parser to fail on travis fixture, got success")
	}
}
