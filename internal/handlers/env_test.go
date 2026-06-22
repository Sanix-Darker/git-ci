package handlers

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

// testNameReplacer sanitises t.Name() output for use as an env-var
// suffix. Hoisted because Go test names can include "/" (subtests)
// and "-" which aren't always safe in env-var keys.
var testNameReplacer = strings.NewReplacer("/", "_", "-", "_")

// captureStdoutEnv swaps os.Stdout for a pipe, runs fn synchronously,
// and returns the captured output as a string. Synchronous because
// CmdEnvList / CmdEnvSet are synchronous and the synchronous form
// avoids close-on-channel races that a goroutine dance introduces.
// Uses defer to restore os.Stdout so a panic in fn doesn't leave
// subsequent tests writing to a closed pipe.
func captureStdoutEnv(t *testing.T, fn func() error) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	if err := fn(); err != nil {
		t.Logf("captured fn error: %v", err)
	}

	_ = w.Close()
	data, _ := io.ReadAll(r)
	_ = r.Close()
	return string(data)
}

// joinTempPath concats a tempdir prefix with a leaf filename using a
// forward slash (t.TempDir never returns a trailing slash).
func joinTempPath(dir, name string) string {
	return dir + "/" + name
}

// unfilteredEnvKey returns an env-var key that bypasses CmdEnvList's
// non-verbose filter (verbose || strings.HasPrefix(env, "GIT_CI_")
// || strings.HasPrefix(env, "CI")), so the test can prove --verbose
// is the only path that surfaces the var.
func unfilteredEnvKey(t *testing.T) string {
	t.Helper()
	return "ZZZ_TEST_REGRESSION_" + testNameReplacer.Replace(t.Name())
}

// -----------------------------------------------------------------------------
// BUG #2 sanity checks at the handler level. The real regression
// coverage (asserting that urfave/cli accepts --verbose on the env-list
// subcommand without "flag provided but not defined") lives in
// cmd/cli_test.go where we drive the real *cli.App.
// -----------------------------------------------------------------------------

func TestCmdEnvList_VerboseIncludesUnfilteredVars(t *testing.T) {
	// Use an env var key NOT prefixed with GIT_CI_/CI so verbose mode is
	// the only thing that could surface it -- this is what BUG #2 is
	// actually testing.
	key := unfilteredEnvKey(t)
	t.Setenv(key, "should-show-when-verbose")

	fs := flag.NewFlagSet("env list", flag.ContinueOnError)
	fs.Bool("verbose", false, "")

	if err := fs.Parse([]string{"--verbose"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	out := captureStdoutEnv(t, func() error {
		return CmdEnvList(ctx)
	})

	// CmdEnvList formats via "%-30s = %s", so include the surrounding spaces.
	want := key + " = should-show-when-verbose"
	if !strings.Contains(out, want) {
		t.Errorf("verbose mode should print %q, got:\n%s", want, out)
	}
}

func TestCmdEnvList_NonVerboseHidesUnrelatedVars(t *testing.T) {
	// Same ZZZ_TEST_ prefix strategy so the env var is genuinely
	// filtered in non-verbose mode (versus GIT_CI_/CI prefixes which
	// CmdEnvList intentionally surfaces).
	key := unfilteredEnvKey(t)
	t.Setenv(key, "should-not-show-when-not-verbose")

	fs := flag.NewFlagSet("env list", flag.ContinueOnError)
	fs.Bool("verbose", false, "")

	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	out := captureStdoutEnv(t, func() error {
		return CmdEnvList(ctx)
	})

	if strings.Contains(out, key) {
		t.Errorf("non-verbose mode should hide %q, got:\n%s", key, out)
	}
}

// -----------------------------------------------------------------------------
// BUG #3 sanity checks at the handler level. Real regression coverage
// lives in cmd/cli_test.go.
// -----------------------------------------------------------------------------

func TestCmdEnvSet_FilterAcceptsPositionalEqualSignArgsOnly(t *testing.T) {
	dir := t.TempDir()
	out := joinTempPath(dir, "save-after.env")

	// Capture the host value before CmdEnvSet's os.Setenv mutates it
	// so the env var is restored after the test, regardless of which
	// KEY/VAL the test wrote.
	t.Setenv("FOO_FROM_POSITIONAL", "hi")

	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.Bool("save", false, "")

	// KEY=VAL comes FIRST, then --save --file (lib fix path).
	if err := fs.Parse([]string{"FOO_FROM_POSITIONAL=hi", "--save", "--file", out}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	if err := CmdEnvSet(ctx); err != nil {
		t.Fatalf("CmdEnvSet: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("env file not created: %v", err)
	}
	if !strings.Contains(string(data), "FOO_FROM_POSITIONAL=hi") {
		t.Errorf("expected file to persist FOO_FROM_POSITIONAL=hi, got:\n%s", data)
	}
}

func TestCmdEnvSet_SaveFlagBeforePositionalArgs(t *testing.T) {
	dir := t.TempDir()
	out := joinTempPath(dir, "save-before.env")

	t.Setenv("BAR", "lo")

	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.Bool("save", false, "")

	if err := fs.Parse([]string{"--save", "--file", out, "BAR=lo"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	if err := CmdEnvSet(ctx); err != nil {
		t.Fatalf("CmdEnvSet: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("env file not created: %v", err)
	}
	if !strings.Contains(string(data), "BAR=lo") {
		t.Errorf("expected file to persist BAR=lo, got:\n%s", data)
	}
}

func TestCmdEnvSet_NoArgsStillErrors(t *testing.T) {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.Bool("save", false, "")

	if err := fs.Parse([]string{"--save"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	err := CmdEnvSet(ctx)
	if err == nil {
		t.Fatal("expected error when no KEY=VAL provided, got nil")
	}
	if !strings.Contains(err.Error(), "no environment variables specified") {
		t.Errorf("expected 'no environment variables specified' error, got: %v", err)
	}
}

func TestCmdEnvSet_DoesNotPersistWithoutSaveFlag(t *testing.T) {
	dir := t.TempDir()
	out := joinTempPath(dir, "no-save.env")

	t.Setenv("NOSAVE", "x")

	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.Bool("save", false, "")

	if err := fs.Parse([]string{"NOSAVE=x", "--file", out}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	if err := CmdEnvSet(ctx); err != nil {
		t.Fatalf("CmdEnvSet: %v", err)
	}

	if _, err := os.Stat(out); err == nil {
		t.Errorf("expected no .env file when --save absent, but %s exists", out)
	}
}

// -----------------------------------------------------------------------------
// BUG #6 sanity check at the handler level. The FEATURES.md gap claim
// is "env set --save (no KEY=VALUE) succeeds silently without writing
// a file" -- that doesn't reproduce under the current code, which
// already errors at the empty-keyValArgs guard. The actual user-facing
// pain is the generic error message: it tells the user *what* failed
// ("no env vars specified") but not *why their invocation* failed
// (--save was the trigger). The v0.4.1 fix ties the error to --save by
// name so the failure is self-explaining. Real-app coverage lives in
// cmd/cli_test.go (TestCliApp_EnvSetSaveAlone_ErrorsAndNoSideEffect).
// -----------------------------------------------------------------------------

func TestCmdEnvSet_SaveFlagAlone_ErrorsMentioningSave(t *testing.T) {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.Bool("save", false, "")

	if err := fs.Parse([]string{"--save"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	err := CmdEnvSet(ctx)
	if err == nil {
		t.Fatal("expected error when --save alone (no KEY=VAL), got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--save") {
		t.Errorf("expected error to mention --save so the failure ties back to the flag the user typed, got: %v", err)
	}
	// Backward-compat: existing TestCmdEnvSet_NoArgsStillErrors still
	// matches on this substring, so keep it present.
	if !strings.Contains(msg, "no environment variables specified") {
		t.Errorf("expected error to retain 'no environment variables specified' phrasing for backward compatibility, got: %v", err)
	}
}

func TestCmdEnvSet_SaveFlagAndFileNoKeyVal_NoFileWritten(t *testing.T) {
	dir := t.TempDir()
	out := joinTempPath(dir, "should-not-exist.env")

	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	fs.Bool("save", false, "")

	if err := fs.Parse([]string{"--save", "--file", out}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := cli.NewContext(nil, fs, nil)

	err := CmdEnvSet(ctx)
	if err == nil {
		t.Fatal("expected error when --save --file out --no KEY=VAL, got nil")
	}
	if !strings.Contains(err.Error(), "--save") {
		t.Errorf("expected error to mention --save, got: %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Errorf("BUG #6 regression: env file was incorrectly created at %s on the error path", out)
	}
}

// -----------------------------------------------------------------------------
// BUG #6 quirk-path regression: covers the urfave/cli path where
// --save leaks into c.Args() instead of being consumed as a flag.
// We register NO --save flag on the stdlib FlagSet, then use the
// "--" stop-parsing marker so "--save" lands in fs.Args() (and
// therefore ctx.Args()). Without the env.go --save-quirk fallback
// in detectSaveFlag, the user would get the generic "no env vars
// specified" message instead of the --save-specific one.
// -----------------------------------------------------------------------------

func TestCmdEnvSet_SaveFlagInRawArgs_QuirkPath_ErrorsMentioningSave(t *testing.T) {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.String("file", "", "")
	// Deliberately NOT registering --save as a flag here so the
	// "--" stop-parsing marker route delivers --save into fs.Args()
	// without a parse error, simulating the urfave/cli quirk.

	if err := fs.Parse([]string{"--", "--save"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	app := &cli.App{Name: "gci"}
	ctx := cli.NewContext(app, fs, nil)

	rawArgs := ctx.Args().Slice()
	if len(rawArgs) != 1 || rawArgs[0] != "--save" {
		t.Fatalf("quirk simulation failed: expected rawArgs=[--save], got %v", rawArgs)
	}

	err := CmdEnvSet(ctx)
	if err == nil {
		t.Fatal("expected error when --save is quirked into positional args (no KEY=VAL), got nil")
	}
	if !strings.Contains(err.Error(), "--save") {
		t.Errorf("expected error to mention --save in quirk path, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// BUG #6 detectSaveFlag false-positive guard: a positional whose VALUE
// contains the literal "save" must NOT trigger detectSaveFlag. Catches
// future refactors to strings.Contains(arg, "save") or similar
// sloppiness.
// -----------------------------------------------------------------------------

func TestDetectSaveFlag_FalsePositiveKeyValContainsSaveLiteral_DoesNotMatch(t *testing.T) {
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	app := &cli.App{Name: "gci"}
	ctx := cli.NewContext(app, fs, nil)

	cases := []struct {
		name    string
		rawArgs []string
		want    bool
	}{
		// false-positive guards: value contains "save" but is not the
		// literal --save flag.
		{"KEY=--save", []string{"KEY=--save"}, false},
		{"KEY=--save-something", []string{"KEY=--save-something"}, false},
		{"KEY=--save=true", []string{"KEY=--save=true"}, false},
		{"KEY=save-but-not-flag", []string{"KEY=save-but-not-flag"}, false},
		// canonical match: the literal --save token alone.
		{"literal --save", []string{"--save"}, true},
		{"among other args", []string{"KEY=v", "--save", "EXTRA"}, true},
		{"multiple --save tokens", []string{"--save", "X", "--save"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectSaveFlag(ctx, tc.rawArgs); got != tc.want {
				t.Errorf("detectSaveFlag(%v) = %v, want %v", tc.rawArgs, got, tc.want)
			}
		})
	}
}
