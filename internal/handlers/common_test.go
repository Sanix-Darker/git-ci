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
