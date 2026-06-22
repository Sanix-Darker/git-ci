package runners

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sanix-darker/git-ci/internal/config"
)

// captureStdoutRunners replaces os.Stdout with a buffer-backed writer,
// runs fn, and returns the captured bytes. Mirrors the helper used in
// internal/handlers/*_test.go but is duplicated here so the two test
// packages stay independently buildable. Uses defer to restore
// os.Stdout so a panic in fn doesn't leave subsequent tests writing
// to a closed pipe.
func captureStdoutRunners(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		done <- buf.String()
	}()

	fn()

	_ = w.Close()
	return <-done
}

// -----------------------------------------------------------------------------
// BUG #4: `gci --quiet` was a registered CLI flag but the runner stack
// never honoured it -- every OutputFormatter.Print* call still printed.
// After the fix, calling SetQuiet(true) must suppress all Print methods
// except ones that intentionally always render (errors + step failures).
// -----------------------------------------------------------------------------

func TestOutputFormatter_QuietSuppressesPrintHeader(t *testing.T) {
	f := NewOutputFormatter(false)
	f.SetQuiet(true)

	out := captureStdoutRunners(t, func() {
		f.PrintHeader("job", "/tmp", "bash")
	})
	if out != "" {
		t.Errorf("PrintHeader should be silent when quiet, got %q", out)
	}
}

func TestOutputFormatter_QuietSuppressesStepRendering(t *testing.T) {
	f := NewOutputFormatter(false)
	f.SetQuiet(true)

	out := captureStdoutRunners(t, func() {
		f.PrintStepHeader("build", 1, 3)
		f.PrintStepComplete(50 * time.Millisecond)
		f.PrintStepSkipped("no cache yet")
		f.PrintJobComplete("build", time.Second, true)
	})
	if out != "" {
		t.Errorf("step/job Print* methods should be silent when quiet, got %q", out)
	}
}

func TestOutputFormatter_QuietStillPrintsError(t *testing.T) {
	// PrintError and PrintStepFailed intentionally bypass quiet so users
	// never lose visibility of actual failures.
	f := NewOutputFormatter(false)
	f.SetQuiet(true)

	out := captureStdoutRunners(t, func() {
		f.PrintError("something blew up")
	})
	if !strings.Contains(out, "something blew up") {
		t.Errorf("PrintError must still render when quiet, got %q", out)
	}
}

func TestOutputFormatter_QuietStillPrintsStepFailed(t *testing.T) {
	f := NewOutputFormatter(false)
	f.SetQuiet(true)

	out := captureStdoutRunners(t, func() {
		f.PrintStepFailed(io.ErrUnexpectedEOF, 250*time.Millisecond)
	})
	if !strings.Contains(out, "FAILED") {
		t.Errorf("PrintStepFailed must still render when quiet, got %q", out)
	}
}

func TestOutputFormatter_NotQuietActuallyPrints(t *testing.T) {
	// Sanity check: without --quiet, the same Print calls must produce
	// visible output. This guards against an over-eager quiet fix that
	// silences everything unconditionally.
	f := NewOutputFormatter(false)
	f.SetQuiet(false)
	// Disable ANSI so substring assertions stay deterministic regardless
	// of whether the host terminal is a TTY.
	f.SetColorEnabled(false)

	out := captureStdoutRunners(t, func() {
		f.PrintHeader("job", "/tmp", "bash")
		f.PrintJobComplete("job", 100*time.Millisecond, true)
	})
	if out == "" {
		t.Fatal("expected non-empty output when quiet=false")
	}
	for _, want := range []string{"Running Job: job", "Job 'job'"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got %q", want, out)
		}
	}
}

func TestOutputFormatter_QuietChainedAcrossMultiplePrintCalls(t *testing.T) {
	// One end-to-end swathe so a regression in any Print method trips
	// the same test. PrintError is *deliberately* not in the list.
	f := NewOutputFormatter(false)
	f.SetQuiet(true)

	out := captureStdoutRunners(t, func() {
		f.PrintHeader("job", "/tmp", "bash")
		f.PrintStepHeader("step", 1, 1)
		f.PrintStepComplete(10 * time.Millisecond)
		f.PrintOutput("hello world", 4)
		f.PrintOutputWithLevel("nested", IndentOutput)
		f.PrintInfo("informational")
		f.PrintWarning("be careful")
		f.PrintDebug("debug noise")
		f.PrintDryRun()
		f.PrintSection("section header")
		f.PrintSubSection("subsection")
		f.PrintKeyValue("k1", "v1", 2)
		f.PrintKeyValueWithLevel("k2", "v2", IndentStep)
		f.PrintList("item1", 2)
		f.PrintListWithLevel("item2", IndentStep)
		f.PrintCommand("echo hi", 2)
		f.PrintCommandWithLevel("echo bye", IndentStep)
		f.PrintEnvironment(map[string]string{"FOO": "bar"})
		f.PrintServices(map[string]string{"db": "postgres"})
	})
	if out != "" {
		t.Errorf("all suppressed Print* calls leaked output when quiet: %q", out)
	}
}

func TestOutputFormatter_SetQuietRoundTrip(t *testing.T) {
	f := NewOutputFormatter(false)

	if f.silenced() {
		t.Fatal("expected silenced()=false on construction")
	}
	f.SetQuiet(true)
	if !f.silenced() {
		t.Fatal("expected silenced()=true after SetQuiet(true)")
	}
	f.SetQuiet(false)
	if f.silenced() {
		t.Fatal("expected silenced()=false after SetQuiet(false)")
	}
}

// -----------------------------------------------------------------------------
// Wiring regression for BUG #4: NewBashRunner must propagate cfg.Quiet
// to its OutputFormatter, otherwise --quiet at the CLI is silently lost.
// NewBashRunner is the cheap constructor: it does NOT shell out to
// podman/docker, so this is safe to drive from a unit test.
// -----------------------------------------------------------------------------

func TestNewBashRunner_PropagatesQuiet(t *testing.T) {
	cfg := &config.RunnerConfig{Quiet: true}
	r := NewBashRunner(cfg)
	// NewBashRunner with cfg.Quiet=true also acquires the package-level
	// os.Stdout/os.Stderr -> /dev/null redirect. Releasing it here keeps
	// the global state clean for subsequent tests. Cleanup is idempotent
	// so a stray call from a non-quiet test is harmless.
	defer func() { _ = r.Cleanup() }()
	if r == nil {
		t.Fatal("NewBashRunner returned nil")
	}
	if r.formatter == nil {
		t.Fatal("runner has no formatter")
	}
	if !r.formatter.silenced() {
		t.Fatal("expected formatter.silenced()==true when cfg.Quiet=true")
	}
}

func TestNewBashRunner_PropagatesVerbose(t *testing.T) {
	cfg := &config.RunnerConfig{Verbose: true}
	r := NewBashRunner(cfg)
	if !r.formatter.Verbose {
		t.Fatal("expected formatter.Verbose==true when cfg.Verbose=true")
	}
}

func TestNewBashRunner_DefaultsAreQuietFalse(t *testing.T) {
	// Defensive: even if config picks up a future Quiet=true
	// accidentally, the constructor must reflect cfg verbatim.
	cfg := config.DefaultConfig()
	r := NewBashRunner(cfg)
	if r.formatter.silenced() {
		t.Fatal("expected silenced()==false for DefaultConfig()")
	}
}
