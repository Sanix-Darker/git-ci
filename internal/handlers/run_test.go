package handlers

import (
	"flag"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
	cli "github.com/urfave/cli/v2"
)

type mockRunner struct {
	cfg      *config.RunnerConfig
	runFn    func(*types.Job, string) error
	runCalls int
	cleanup  int
}

func (m *mockRunner) RunJob(job *types.Job, workdir string) error {
	m.runCalls++
	if m.runFn == nil {
		return nil
	}
	return m.runFn(job, workdir)
}

func (m *mockRunner) RunStep(_ *types.Step, _ map[string]string, _ string) error {
	return nil
}

func (m *mockRunner) Cleanup() error {
	m.cleanup++
	return nil
}

func (m *mockRunner) GetRunnerType() types.RunnerType {
	return types.RunnerTypeBash
}

func newRunTestContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := (&cli.BoolFlag{Name: "docker"}).Apply(fs); err != nil {
		t.Fatalf("register docker flag: %v", err)
	}
	if err := (&cli.BoolFlag{Name: "podman"}).Apply(fs); err != nil {
		t.Fatalf("register podman flag: %v", err)
	}
	if err := (&cli.BoolFlag{Name: "parallel"}).Apply(fs); err != nil {
		t.Fatalf("register parallel flag: %v", err)
	}
	if err := (&cli.IntFlag{Name: "max-parallel"}).Apply(fs); err != nil {
		t.Fatalf("register max-parallel flag: %v", err)
	}
	if err := (&cli.BoolFlag{Name: "continue-on-error"}).Apply(fs); err != nil {
		t.Fatalf("register continue-on-error flag: %v", err)
	}
	if err := (&cli.IntFlag{Name: "timeout"}).Apply(fs); err != nil {
		t.Fatalf("register timeout flag: %v", err)
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}

	return cli.NewContext(nil, fs, nil)
}

func TestCreateRunner_UsesDockerRunnerWhenFlagSet(t *testing.T) {
	origDocker := newDockerRunner
	origPodman := newPodmanRunner
	origBash := newBashRunner
	defer func() {
		newDockerRunner = origDocker
		newPodmanRunner = origPodman
		newBashRunner = origBash
	}()

	ctx := newRunTestContext(t, "--docker")

	newDockerRunner = func(_ *config.RunnerConfig) (types.Runner, error) {
		return &mockRunner{}, nil
	}
	newPodmanRunner = func(_ *config.RunnerConfig) (types.Runner, error) {
		t.Fatal("podman runner should not be created when docker flag is set")
		return nil, nil
	}
	newBashRunner = func(_ *config.RunnerConfig) types.Runner {
		t.Fatal("bash runner should not be created when docker flag is set")
		return nil
	}

	_, err := createRunner(ctx, &config.RunnerConfig{})
	if err != nil {
		t.Fatalf("createRunner: %v", err)
	}
}

func TestCreateRunner_UsesPodmanRunnerWhenFlagSet(t *testing.T) {
	origDocker := newDockerRunner
	origPodman := newPodmanRunner
	origBash := newBashRunner
	defer func() {
		newDockerRunner = origDocker
		newPodmanRunner = origPodman
		newBashRunner = origBash
	}()

	ctx := newRunTestContext(t, "--podman")

	newPodmanRunner = func(_ *config.RunnerConfig) (types.Runner, error) {
		return &mockRunner{}, nil
	}
	newDockerRunner = func(_ *config.RunnerConfig) (types.Runner, error) {
		t.Fatal("docker runner should not be created when podman flag is set")
		return nil, nil
	}
	newBashRunner = func(_ *config.RunnerConfig) types.Runner {
		t.Fatal("bash runner should not be created when podman flag is set")
		return nil
	}

	_, err := createRunner(ctx, &config.RunnerConfig{})
	if err != nil {
		t.Fatalf("createRunner: %v", err)
	}
}

func TestCreateRunner_DefaultsToBashRunner(t *testing.T) {
	origDocker := newDockerRunner
	origPodman := newPodmanRunner
	origBash := newBashRunner
	defer func() {
		newDockerRunner = origDocker
		newPodmanRunner = origPodman
		newBashRunner = origBash
	}()

	ctx := newRunTestContext(t)

	newBashRunner = func(_ *config.RunnerConfig) types.Runner {
		return &mockRunner{}
	}

	if _, err := createRunner(ctx, &config.RunnerConfig{}); err != nil {
		t.Fatalf("createRunner: %v", err)
	}
}

func TestRunJobsSequential_ContinuesOnErrorWhenEnabled(t *testing.T) {
	origBash := newBashRunner
	t.Cleanup(func() { newBashRunner = origBash })

	executedUpstream := false
	executedDownstream := false

	jobs := map[string]*types.Job{
		"upstream": {
			Name:  "upstream",
			Steps: []types.Step{{Run: "exit 1"}},
		},
		"downstream": {
			Name:  "downstream",
			Needs: []string{"upstream"},
			Steps: []types.Step{{Run: "echo downstream"}},
		},
	}

	newBashRunner = func(cfg *config.RunnerConfig) types.Runner {
		r := &mockRunner{}
		r.runFn = func(job *types.Job, _ string) error {
			if job.Name == "upstream" {
				executedUpstream = true
				return fmt.Errorf("upstream failed")
			}
			if job.Name == "downstream" {
				executedDownstream = true
			}
			return nil
		}
		return r
	}

	ctx := newRunTestContext(t, "--continue-on-error")
	cfg, err := buildRunnerConfig(ctx)
	if err != nil {
		t.Fatalf("buildRunnerConfig: %v", err)
	}
	cfg.WorkDir = t.TempDir()

	if err := runJobsSequential(ctx, jobs, t.TempDir(), cfg); err != nil {
		t.Fatalf("expected continue-on-error to allow pipeline to complete, got error %v", err)
	}
	if !executedUpstream {
		t.Fatalf("expected upstream job to run")
	}
	if !executedDownstream {
		t.Fatalf("expected downstream job to run when continue-on-error is set")
	}
}

func TestRunJobsSequential_PassesTimeoutToRunner(t *testing.T) {
	origBash := newBashRunner
	t.Cleanup(func() { newBashRunner = origBash })

	ctx := newRunTestContext(t, "--timeout", "7")
	cfg, err := buildRunnerConfig(ctx)
	if err != nil {
		t.Fatalf("buildRunnerConfig: %v", err)
	}
	cfg.WorkDir = t.TempDir()

	passedTimeout := -1
	newBashRunner = func(cfg *config.RunnerConfig) types.Runner {
		passedTimeout = cfg.Timeout
		return &mockRunner{}
	}

	jobs := map[string]*types.Job{
		"job": {
			Name:  "job",
			Steps: []types.Step{{Run: "echo hi"}},
		},
	}

	if err := runJobsSequential(ctx, jobs, t.TempDir(), cfg); err != nil {
		t.Fatalf("runJobsSequential: %v", err)
	}
	if passedTimeout != 7 {
		t.Fatalf("expected timeout=7 passed to runner, got %d", passedTimeout)
	}
}

func TestRunJobsParallel_RespectsMaxParallel(t *testing.T) {
	origBash := newBashRunner
	t.Cleanup(func() { newBashRunner = origBash })

	var active, maxActive int
	var mu sync.Mutex

	newBashRunner = func(cfg *config.RunnerConfig) types.Runner {
		return &mockRunner{
			runFn: func(_ *types.Job, _ string) error {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()

				time.Sleep(40 * time.Millisecond)

				mu.Lock()
				active--
				mu.Unlock()
				return nil
			},
			cfg: cfg,
		}
	}

	jobs := map[string]*types.Job{
		"job-a": {Name: "job-a"},
		"job-b": {Name: "job-b"},
		"job-c": {Name: "job-c"},
		"job-d": {Name: "job-d"},
	}

	ctx := newRunTestContext(t, "--parallel", "--max-parallel", "2")
	cfg, err := buildRunnerConfig(ctx)
	if err != nil {
		t.Fatalf("buildRunnerConfig: %v", err)
	}
	cfg.WorkDir = t.TempDir()

	if err := runJobsParallel(ctx, jobs, t.TempDir(), cfg); err != nil {
		t.Fatalf("runJobsParallel: %v", err)
	}

	if maxActive > 2 {
		t.Fatalf("expected no more than 2 concurrent jobs, saw %d", maxActive)
	}
}
