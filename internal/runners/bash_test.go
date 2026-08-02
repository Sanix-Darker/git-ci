package runners

import (
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestBashRunner_RunJob_ExecutesRunStep(t *testing.T) {
	root := t.TempDir()
	runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
	defer func() { _ = runner.Cleanup() }()

	job := &types.Job{
		Name: "hello",
		Steps: []types.Step{
			{Name: "echo", Run: "printf \"hello from shell\\n\""},
		},
	}

	if err := runner.RunJob(job, root); err != nil {
		t.Fatalf("RunJob should succeed for successful command: %v", err)
	}
}

func TestBashRunner_RunActionCheckoutSkipsWithGitHint(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		step := &types.Step{Uses: "actions/checkout@v3"}
		if err := runner.RunStep(step, map[string]string{}, root); err != nil {
			t.Fatalf("RunStep should not error for checkout: %v", err)
		}
	})

	if !strings.Contains(out, "Not in a git repository, skipping checkout") {
		t.Fatalf("expected checkout fallback message, got: %q", out)
	}
}

func TestBashRunner_RunActionSetupGoChecksTool(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		step := &types.Step{
			Uses: "actions/setup-go@v5",
			With: map[string]string{"go-version": "1.22"},
		}
		if err := runner.RunStep(step, map[string]string{}, root); err != nil {
			t.Fatalf("RunStep should not error for setup-go: %v", err)
		}
	})

	if !strings.Contains(out, "Checking go") {
		t.Fatalf("expected setup-go check message, got: %q", out)
	}
}

func TestBashRunner_RunActionUnsupportedEmitsWarning(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		step := &types.Step{Uses: "example/unknown-action@v1"}
		if err := runner.RunStep(step, map[string]string{}, root); err != nil {
			t.Fatalf("RunStep should not error for unknown actions: %v", err)
		}
	})

	if !strings.Contains(out, "Unsupported action: example/unknown-action@v1 (skipping)") {
		t.Fatalf("expected unsupported action warning, got: %q", out)
	}
}

func TestBashRunner_RunJob_ContinuesWhenStepAllowsFailure(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		job := &types.Job{
			Name: "continuation",
			Steps: []types.Step{
				{Name: "fail", Run: "false", ContinueOnErr: true},
				{Name: "ok", Run: "printf \"done\\n\""},
			},
		}

		if err := runner.RunJob(job, root); err != nil {
			t.Fatalf("RunJob should succeed when failed step allows continue: %v", err)
		}
	})

	if !strings.Contains(out, "Step failed but continuing") {
		t.Fatalf("expected continue-on-error warning, got: %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("expected second step output after failed step, got: %q", out)
	}
}

func TestBashRunner_RunStep_StreamsStdoutAndStderr(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		step := &types.Step{Run: "sh -c 'printf stdout-output; printf stderr-output 1>&2'"}
		if err := runner.RunStep(step, map[string]string{}, root); err != nil {
			t.Fatalf("RunStep should execute and stream output: %v", err)
		}
	})

	if !strings.Contains(out, "stdout-output") {
		t.Fatalf("expected stdout output to be streamed, got: %q", out)
	}
	if !strings.Contains(out, "stderr-output") {
		t.Fatalf("expected stderr output to be streamed, got: %q", out)
	}
}

func TestBashRunner_DryRunPrintsCommandAndEnvironment(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{Verbose: true, DryRun: true, WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		job := &types.Job{
			Name: "verbose-dry-run",
			Steps: []types.Step{{
				Name: "echo",
				Run:  "echo HELLO",
				Env:  map[string]string{"FOO": "bar"},
			}},
		}
		if err := runner.RunJob(job, root); err != nil {
			t.Fatalf("RunJob dry-run should not execute commands: %v", err)
		}
	})

	if !strings.Contains(out, "DRY RUN MODE - Commands will be displayed but not executed") {
		t.Fatalf("expected dry-run banner, got: %q", out)
	}
	if !strings.Contains(out, "Would execute") {
		t.Fatalf("expected dry-run command section, got: %q", out)
	}
	if !strings.Contains(out, "FOO") || !strings.Contains(out, "bar") {
		t.Fatalf("expected environment in dry-run output, got: %q", out)
	}
	if !strings.Contains(out, "Command:") {
		t.Fatalf("expected command section in dry-run output, got: %q", out)
	}
}

func TestBashRunner_RunJob_FailsOnCommandErrorWithoutContinue(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		job := &types.Job{
			Name: "fail",
			Steps: []types.Step{{
				Name: "bad",
				Run:  "false",
			}},
		}

		if err := runner.RunJob(job, root); err == nil {
			t.Fatalf("RunJob should return error for failing step when ContinueOnErr is false")
		}
	})

	if !strings.Contains(out, "Job '") || !strings.Contains(strings.ToUpper(out), "FAILED") {
		t.Fatalf("expected job failure output, got: %q", out)
	}
}

func TestBashRunner_RunJob_EmitsVerboseEnvironmentAndCommand(t *testing.T) {
	root := t.TempDir()
	out := captureStdoutRunners(t, func() {
		runner := NewBashRunner(&config.RunnerConfig{Verbose: true, WorkDir: root})
		defer func() { _ = runner.Cleanup() }()

		job := &types.Job{
			Name:        "env-job",
			Environment: map[string]string{"APP": "ok"},
			Steps: []types.Step{{
				Name: "show",
				Run:  "printf hello",
			}},
		}

		if err := runner.RunJob(job, root); err != nil {
			t.Fatalf("RunJob failed unexpectedly: %v", err)
		}
	})

	if !strings.Contains(out, "Environment Variables") || !strings.Contains(out, "APP") || !strings.Contains(out, "ok") {
		t.Fatalf("expected verbose environment block, got: %q", out)
	}
	if !strings.Contains(out, "printf hello") {
		t.Fatalf("expected command output or print in verbose mode, got: %q", out)
	}
}
