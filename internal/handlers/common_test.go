package handlers

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

func buildRunContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := (&cli.BoolFlag{Name: "verbose"}).Apply(fs); err != nil {
		t.Fatalf("register verbose flag: %v", err)
	}
	if err := (&cli.BoolFlag{Name: "debug"}).Apply(fs); err != nil {
		t.Fatalf("register debug flag: %v", err)
	}
	if err := (&cli.StringSliceFlag{Name: "env"}).Apply(fs); err != nil {
		t.Fatalf("register env flag: %v", err)
	}
	if err := (&cli.StringFlag{Name: "env-file"}).Apply(fs); err != nil {
		t.Fatalf("register env-file flag: %v", err)
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}

	return cli.NewContext(nil, fs, nil)
}

func writeTempEnvFile(t *testing.T, content string) string {
	t.Helper()

	dir := t.TempDir()
	file := filepath.Join(dir, ".env")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return file
}

func TestBuildRunnerConfig_DebugEnablesVerboseAndParsesEnvFile(t *testing.T) {
	envFile := writeTempEnvFile(t, "FILE_FOO=file-value\n")

	ctx := buildRunContext(t, "--debug", "--env", "CLI_BAR=cli-value", "--env-file", envFile)
	cfg, err := buildRunnerConfig(ctx)
	if err != nil {
		t.Fatalf("buildRunnerConfig: %v", err)
	}

	if !cfg.Verbose {
		t.Errorf("expected cfg.Verbose=true when --debug is set")
	}
	if got := cfg.Environment["CLI_BAR"]; got != "cli-value" {
		t.Errorf("expected CLI_BAR from --env, got %q", got)
	}
	if got := cfg.Environment["FILE_FOO"]; got != "file-value" {
		t.Errorf("expected FILE_FOO from --env-file, got %q", got)
	}
}

func TestBuildRunnerConfig_EnvFileMissingReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.env")
	ctx := buildRunContext(t, "--env-file", missing)

	_, err := buildRunnerConfig(ctx)
	if err == nil {
		t.Fatalf("expected error for missing env file %q, got nil", missing)
	}
	if !strings.Contains(err.Error(), "failed to load env file") {
		t.Errorf("unexpected error message %q", err)
	}
}

func TestBuildRunnerConfig_RespectsEnvFileCommentsAndQuotes(t *testing.T) {
	file := writeTempEnvFile(t, `# leading comment

PLAIN=plain_value
QUOTED="value with spaces"
SINGLE='single quoted value'
EMPTY=
`)

	ctx := buildRunContext(t, "--env-file", file)
	cfg, err := buildRunnerConfig(ctx)
	if err != nil {
		t.Fatalf("buildRunnerConfig: %v", err)
	}

	if got := cfg.Environment["PLAIN"]; got != "plain_value" {
		t.Errorf("expected plain value from env-file, got %q", got)
	}
	if got := cfg.Environment["QUOTED"]; got != "value with spaces" {
		t.Errorf("expected quotes to be stripped for quoted value, got %q", got)
	}
	if got := cfg.Environment["SINGLE"]; got != "single quoted value" {
		t.Errorf("expected single quotes to be stripped, got %q", got)
	}
	if _, ok := cfg.Environment["EMPTY"]; !ok {
		t.Errorf("expected empty variable key to be parsed from env-file")
	}
	if got := cfg.Environment["EMPTY"]; got != "" {
		t.Errorf("expected empty variable value from env-file, got %q", got)
	}
}

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
	return path
}

func TestParseInput_NoCIFilesReturnsHelpfulError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	_, err := parseInput("")
	if err == nil {
		t.Fatal("expected error when no CI config exists")
	}
	if !strings.Contains(err.Error(), "no CI configuration file found") {
		t.Fatalf("expected missing-CI error, got %v", err)
	}
}

func TestParseInput_AutoDetectsGitHubWorkflowFirst(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// When both files exist, auto-detect prefers .github/workflows/ci.yml.
	writeTempFile(t, filepath.Join(dir, ".github", "workflows"), "ci.yml", `name: github-auto
on: [push]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`)
	writeTempFile(t, dir, ".gitlab-ci.yml", `stages:
  - test
test:
  stage: test
  script:
    - echo hello
`)

	pipeline, err := parseInput("")
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "github" {
		t.Fatalf("expected github provider from precedence, got %q", pipeline.Provider)
	}
}

func TestParseInput_AutoDetectsGitLabWhenGithubMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	writeTempFile(t, dir, ".gitlab-ci.yml", `stages:
  - test
test:
  stage: test
  script:
    - echo hello
`)

	pipeline, err := parseInput("")
	if err != nil {
		t.Fatalf("parseInput: %v", err)
	}
	if pipeline.Provider != "gitlab" {
		t.Fatalf("expected gitlab provider, got %q", pipeline.Provider)
	}
}

func TestGetWorkdir_DefaultUsesCurrentDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	fs := flag.NewFlagSet("workdir", flag.ContinueOnError)
	fs.String("workdir", ".", "")
	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	got, err := getWorkdir(ctx)
	if err != nil {
		t.Fatalf("getWorkdir: %v", err)
	}
	want, err := filepath.Abs(tmp)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if got != want {
		t.Fatalf("expected workdir %q, got %q", want, got)
	}
}

func TestGetWorkdir_MissingDirErrors(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist")

	fs := flag.NewFlagSet("workdir", flag.ContinueOnError)
	fs.String("workdir", "", "")
	if err := fs.Parse([]string{"--workdir", missing}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	_, err := getWorkdir(ctx)
	if err == nil {
		t.Fatal("expected missing-workdir error")
	}
	if !strings.Contains(err.Error(), "workdir does not exist") {
		t.Fatalf("expected workdir missing message, got %v", err)
	}
}
