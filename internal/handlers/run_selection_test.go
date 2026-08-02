package handlers

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func captureStdoutRun(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	data, _ := io.ReadAll(r)
	_ = r.Close()
	return string(data)
}

func newRunSelectContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := (&cli.StringFlag{Name: "job"}).Apply(fs); err != nil {
		t.Fatalf("register job flag: %v", err)
	}
	if err := (&cli.StringFlag{Name: "stage"}).Apply(fs); err != nil {
		t.Fatalf("register stage flag: %v", err)
	}
	if err := (&cli.StringSliceFlag{Name: "only"}).Apply(fs); err != nil {
		t.Fatalf("register only flag: %v", err)
	}
	if err := (&cli.StringSliceFlag{Name: "except"}).Apply(fs); err != nil {
		t.Fatalf("register except flag: %v", err)
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}

	return cli.NewContext(nil, fs, nil)
}

func pipelineFixtureForSelection() *types.Pipeline {
	return &types.Pipeline{
		Name: "fixture-pipeline",
		Stages: []string{
			"test",
		},
		Jobs: map[string]*types.Job{
		"build": {
			Name:   "build",
			Stage:  "build",
			Needs:  []string{"test"},
			Steps:  []types.Step{{Run: "echo build"}},
		},
		"test": {
			Name:  "test",
			Stage: "test",
			Needs: []string{"lint"},
			Steps: []types.Step{{Run: "echo test"}},
		},
		"lint": {
			Name:  "lint",
			Stage: "test",
			Steps: []types.Step{{Run: "echo lint"}},
		},
		},
	}
}

func TestSelectJobsToRun_ExactMatchByFlag(t *testing.T) {
	ctx := newRunSelectContext(t, "--job", "build")
	jobs := selectJobsToRun(ctx, pipelineFixtureForSelection())

	if jobs == nil || len(jobs) != 1 {
		t.Fatalf("expected 1 selected job, got %v", jobs)
	}
	if _, ok := jobs["build"]; !ok {
		t.Errorf("expected job 'build' to be selected")
	}
}

func TestSelectJobsToRun_PatternMatchByFlag(t *testing.T) {
	ctx := newRunSelectContext(t, "--job", "t*")
	jobs := selectJobsToRun(ctx, pipelineFixtureForSelection())

	if jobs == nil || len(jobs) != 2 {
		t.Fatalf("expected 2 jobs matching pattern 't*', got %v", jobs)
	}
	for _, want := range []string{"test", "lint"} {
		if _, ok := jobs[want]; !ok {
			t.Errorf("expected %q in selection", want)
		}
	}
}

func TestSelectJobsToRun_UnmatchedPatternReturnsNil(t *testing.T) {
	ctx := newRunSelectContext(t, "--job", "does-not-exist")
	out := captureStdoutRun(t, func() {
		jobs := selectJobsToRun(ctx, pipelineFixtureForSelection())
		if jobs != nil {
			t.Fatalf("expected nil when no matches, got %v", jobs)
		}
	})

	if !strings.Contains(out, "Warning: job 'does-not-exist' not found") {
		t.Fatalf("expected unmatched-job warning, got:\n%s", out)
	}
}

func TestSelectJobsToRun_StageFilter(t *testing.T) {
	ctx := newRunSelectContext(t, "--stage", "test")
	jobs := selectJobsToRun(ctx, pipelineFixtureForSelection())

	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs in test stage, got %d", len(jobs))
	}
	for _, want := range []string{"test", "lint"} {
		if _, ok := jobs[want]; !ok {
			t.Errorf("expected %q in selected jobs", want)
		}
	}
}

func TestSelectJobsToRun_StageFilterNoJobsPrintsWarning(t *testing.T) {
	ctx := newRunSelectContext(t, "--stage", "release")
	out := captureStdoutRun(t, func() {
		jobs := selectJobsToRun(ctx, pipelineFixtureForSelection())
		if jobs != nil {
			t.Fatalf("expected nil when stage has no jobs, got %v", jobs)
		}
	})

	if !strings.Contains(out, "Warning: no jobs found for stage 'release'") {
		t.Fatalf("expected no-jobs warning, got:\n%s", out)
	}
}

func TestSelectJobsToRun_OnlyAndExceptFilters(t *testing.T) {
	ctx := newRunSelectContext(t, "--only", "test", "--except", "lint")
	jobs := selectJobsToRun(ctx, pipelineFixtureForSelection())

	if len(jobs) != 1 {
		t.Fatalf("expected only one job after filters, got %d", len(jobs))
	}
	if _, ok := jobs["test"]; !ok {
		t.Fatalf("expected filtered job 'test', got keys %v", jobNames(jobs))
	}
	if _, ok := jobs["lint"]; ok {
		t.Fatalf("job 'lint' should be filtered out, got keys %v", jobNames(jobs))
	}
}

func jobNames(jobs map[string]*types.Job) []string {
	keys := make([]string, 0, len(jobs))
	for name := range jobs {
		keys = append(keys, name)
	}
	return keys
}
