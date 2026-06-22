package runners

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

// -----------------------------------------------------------------------------
// BUG #5: --quiet leaked output through direct fmt.Printf/io.Copy calls
// in podman.go (dryRunJob, streamLogs) and docker.go (same). The
// OutputFormatter can only silence its own Print* methods.
//
// After the fix, acquireQuietRedirect / releaseQuietRedirect swap
// os.Stdout and os.Stderr at the Go level for any pipeline that wants
// opt-in quiet suppression. Runners acquire in their constructor and
// release in Cleanup.
//
// Each test below acquires and releases in a single function body so the
// package-level state never leaks across tests.
// -----------------------------------------------------------------------------

func TestQuietRedirect_AcquireFalseQuiet_DoesNotInstall(t *testing.T) {
	// Defensive: false quiet must be a no-op so non-quiet runs are
	// completely unaffected.
	origOut := os.Stdout
	origErr := os.Stderr

	if acquireQuietRedirect(false) {
		t.Fatal("expected acquire(quiet=false) to return false")
	}
	if quietRedirectActive() {
		t.Fatal("redirect should remain inactive when quiet=false")
	}
	if os.Stdout != origOut {
		t.Fatal("os.Stdout should be unchanged by acquire(quiet=false)")
	}
	if os.Stderr != origErr {
		t.Fatal("os.Stderr should be unchanged by acquire(quiet=false)")
	}
}

func TestQuietRedirect_AcquireAndRelease_RoundTrip(t *testing.T) {
	// Happy path: after acquire, the variables must point at a different
	// *os.File (the dev/null handle). After release they must be the
	// exact originals — pointer-equal, not just content-equal.
	origOut := os.Stdout
	origErr := os.Stderr

	if !acquireQuietRedirect(true) {
		t.Fatal("expected acquire(true) to install the redirect")
	}
	if !quietRedirectActive() {
		t.Fatal("redirect should be active after acquire(true)")
	}
	if os.Stdout == origOut {
		t.Fatal("os.Stdout should be swapped away from original")
	}
	if os.Stderr == origErr {
		t.Fatal("os.Stderr should be swapped away from original")
	}

	releaseQuietRedirect()

	if quietRedirectActive() {
		t.Fatal("redirect should be inactive after release")
	}
	if os.Stdout != origOut {
		t.Fatal("os.Stdout should be restored to the exact original")
	}
	if os.Stderr != origErr {
		t.Fatal("os.Stderr should be restored to the exact original")
	}
}

func TestQuietRedirect_RefCounting_InstallsUntilZero(t *testing.T) {
	// Parallel-mode safety: three acquires must keep the redirect
	// installed; only the third release restores. Any earlier release
	// must leave os.Stdout pointing at dev/null and the redirect active.
	origOut := os.Stdout

	if !acquireQuietRedirect(true) {
		t.Fatal("acquire 1")
	}
	if !acquireQuietRedirect(true) {
		t.Fatal("acquire 2")
	}
	if !acquireQuietRedirect(true) {
		t.Fatal("acquire 3")
	}
	if quietRedirectRefCount() != 3 {
		t.Fatalf("ref count: want 3, got %d", quietRedirectRefCount())
	}
	if !quietRedirectActive() {
		t.Fatal("redirect should be active with count=3")
	}

	releaseQuietRedirect() // 3 -> 2
	if quietRedirectRefCount() != 2 {
		t.Fatalf("after release 1: count=%d", quietRedirectRefCount())
	}
	if !quietRedirectActive() {
		t.Fatal("redirect should still be active with count=2")
	}
	if os.Stdout == origOut {
		t.Fatal("os.Stdout should still point at dev/null while count > 0")
	}

	releaseQuietRedirect() // 2 -> 1
	if !quietRedirectActive() {
		t.Fatal("redirect should still be active with count=1")
	}
	if os.Stdout == origOut {
		t.Fatal("os.Stdout should still point at dev/null with count=1")
	}

	releaseQuietRedirect() // 1 -> 0, restore
	if quietRedirectActive() {
		t.Fatal("redirect should be inactive after count reaches 0")
	}
	if os.Stdout != origOut {
		t.Fatal("os.Stdout should be restored to exact original after count=0")
	}
	if quietRedirectRefCount() != 0 {
		t.Fatalf("final count: want 0, got %d", quietRedirectRefCount())
	}
}

func TestQuietRedirect_DefensiveRelease_NoOpWhenStray(t *testing.T) {
	// Stray releases (no acquire, or more releases than acquires) must
	// not panic and must not corrupt the package state. This guards
	// against future caller bugs that could otherwise wedge the redirect
	// permanently installed across the entire test run.
	origOut := os.Stdout
	origErr := os.Stderr

	releaseQuietRedirect()
	releaseQuietRedirect()
	releaseQuietRedirect()

	if os.Stdout != origOut {
		t.Fatal("os.Stdout should be unchanged by stray releases")
	}
	if os.Stderr != origErr {
		t.Fatal("os.Stderr should be unchanged by stray releases")
	}
	if quietRedirectActive() {
		t.Fatal("redirect should remain inactive after stray releases")
	}
}

func TestQuietRedirect_SwallowsDirectFmtPrint_WhenActive(t *testing.T) {
	// The actual proof that the documented leak is now closed. We wire a
	// pipe to os.Stdout, then acquire the redirect (which now points
	// os.Stdout at /dev/null), then call fmt.Println directly. If the
	// redirect works, zero bytes should reach the pipe.
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut := os.Stdout
	os.Stdout = pipeW
	defer func() { os.Stdout = origOut }()

	if !acquireQuietRedirect(true) {
		t.Fatal("acquire failed")
	}
	defer releaseQuietRedirect()

	// Direct Go-level write to os.Stdout. With the redirect active this
	// lands in /dev/null and never reaches the pipe.
	fmt.Println("this must not reach the pipe")

	// Close the write end and drain.
	_ = pipeW.Close()
	data, _ := io.ReadAll(pipeR)
	_ = pipeR.Close()

	if len(data) > 0 {
		t.Fatalf("expected zero bytes through the pipe, got %d: %q",
			len(data), data)
	}
}

// -----------------------------------------------------------------------------
// Runner-level integration: prove the BashRunner / PodmanRunner leak sites
// are now closed end-to-end. These tests run with Quiet=true and capture
// the host os.Stdout via a pipe — if any direct fmt.Printf/io.Copy in the
// runner escapes, the test will see bytes on the pipe.
// -----------------------------------------------------------------------------

func TestBashRunner_QuietRunJob_ProducesNoStdout(t *testing.T) {
	tmpDir := t.TempDir()

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut := os.Stdout
	os.Stdout = pipeW
	defer func() { os.Stdout = origOut }()

	cfg := &config.RunnerConfig{Quiet: true, WorkDir: tmpDir}
	runner := NewBashRunner(cfg)
	// Cleanup releases both the formatter state and the redirect.
	// Idempotent across repeat invocations.
	defer func() { _ = runner.Cleanup() }()

	job := &types.Job{
		Name: "test-quiet",
		Steps: []types.Step{
			{Name: "noop", Run: "true"},
		},
	}
	if err := runner.RunJob(job, tmpDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_ = pipeW.Close()
	data, _ := io.ReadAll(pipeR)
	_ = pipeR.Close()

	if len(data) > 0 {
		t.Fatalf("BashRunner.RunJob under quiet leaked %d bytes: %q",
			len(data), data)
	}
}

func TestBashRunner_NonQuietRunJob_ProducesVisibleOutput(t *testing.T) {
	// Parity check: with Quiet=false the same RunJob must produce
	// visible output. Guards against an over-eager fix that silences
	// output unconditionally.
	tmpDir := t.TempDir()

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut := os.Stdout
	os.Stdout = pipeW
	defer func() { os.Stdout = origOut }()

	cfg := &config.RunnerConfig{Quiet: false, WorkDir: tmpDir}
	runner := NewBashRunner(cfg)
	defer func() { _ = runner.Cleanup() }()

	job := &types.Job{
		Name: "test-noisy",
		Steps: []types.Step{
			{Name: "noop", Run: "true"},
		},
	}
	if err := runner.RunJob(job, tmpDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_ = pipeW.Close()
	data, _ := io.ReadAll(pipeR)
	_ = pipeR.Close()

	if len(data) == 0 {
		t.Fatal("expected non-empty output when Quiet=false")
	}
	if !bytes.Contains(data, []byte("Running Job")) {
		t.Fatalf("expected output to contain 'Running Job', got %q", data)
	}
}

func TestPodmanRunner_DryRunJob_QuietProducesNoOutput(t *testing.T) {
	// Direct proof that the dryRunJob fmt.Printf leaks (4 calls) are
	// swallowed when quiet=true. We bypass NewPodmanRunner because it
	// shells out to findContainerRuntime() — instead we manually
	// construct the struct so this test runs without a real
	// podman/docker binary.
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut := os.Stdout
	os.Stdout = pipeW
	defer func() { os.Stdout = origOut }()

	cfg := &config.RunnerConfig{Quiet: true}
	r := &PodmanRunner{
		config:    cfg,
		formatter: NewOutputFormatter(true), // verbose=true to keep dispatcher on the noisy branch
	}
	r.formatter.SetQuiet(true)

	if !acquireQuietRedirect(true) {
		t.Fatal("acquire failed")
	}
	defer releaseQuietRedirect()

	job := &types.Job{
		Name: "test",
		Steps: []types.Step{
			{Name: "step1", Run: "echo hi"},
			{Name: "step2", Run: "echo bye"},
		},
	}
	if err := r.dryRunJob(job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_ = pipeW.Close()
	data, _ := io.ReadAll(pipeR)
	_ = pipeR.Close()

	if len(data) > 0 {
		t.Fatalf("PodmanRunner.dryRunJob under quiet leaked %d bytes: %q",
			len(data), data)
	}
}

func TestPodmanRunner_DryRunJob_NonQuietProducesVisibleOutput(t *testing.T) {
	// Parity check for the dryRunJob fix.
	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origOut := os.Stdout
	os.Stdout = pipeW
	defer func() { os.Stdout = origOut }()

	cfg := &config.RunnerConfig{Quiet: false}
	r := &PodmanRunner{
		config:    cfg,
		formatter: NewOutputFormatter(true),
	}
	// Quiet=false -> no acquireRedirect call; formatter.SetQuiet defaults false.

	job := &types.Job{
		Name: "test",
		Steps: []types.Step{
			{Name: "step1", Run: "echo hi"},
		},
	}
	if err := r.dryRunJob(job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_ = pipeW.Close()
	data, _ := io.ReadAll(pipeR)
	_ = pipeR.Close()

	if len(data) == 0 {
		t.Fatal("expected non-empty output when Quiet=false")
	}
	// dryRunJob uses fmt.Printf for the "[N/M] step" line.
	if !bytes.Contains(data, []byte("step1")) {
		t.Fatalf("expected output to contain 'step1', got %q", data)
	}
}
