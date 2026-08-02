package handlers

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	cli "github.com/urfave/cli/v2"
)

func captureStdoutConfig(t *testing.T, fn func()) string {
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

func newConfigCtx(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.String("config", "", "")
	fs.String("output", ".git-ci.yml", "")
	fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}
	return cli.NewContext(nil, fs, nil)
}

func TestCmdConfigShow_WhenNoConfigExists(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())

	out := captureStdoutConfig(t, func() {
		if err := CmdConfigShow(newConfigCtx(t)); err != nil {
			t.Fatalf("CmdConfigShow: %v", err)
		}
	})

	if !strings.Contains(out, "No configuration file found") {
		t.Fatalf("expected no config message, got:\n%s", out)
	}
	if !strings.Contains(out, "git-ci config init") {
		t.Errorf("expected help hint for initialization, got:\n%s", out)
	}
}

func TestCmdConfigShow_DisplaysExistingConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())

	const content = `defaults:
  runner: docker
  timeout: 30
`
	if err := os.WriteFile(".git-ci.yml", []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}

	out := captureStdoutConfig(t, func() {
		if err := CmdConfigShow(newConfigCtx(t)); err != nil {
			t.Fatalf("CmdConfigShow: %v", err)
		}
	})

	if !strings.Contains(out, "Configuration from: .git-ci.yml") {
		t.Fatalf("expected output to show config path, got:\n%s", out)
	}
	if !strings.Contains(out, "runner: docker") {
		t.Errorf("expected runner override from config, got:\n%s", out)
	}
}

func TestCmdConfigInit_CreatesFileAtDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := CmdConfigInit(newConfigCtx(t)); err != nil {
		t.Fatalf("CmdConfigInit: %v", err)
	}

	data, err := os.ReadFile(".git-ci.yml")
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	if !strings.Contains(string(data), "# git-ci configuration file") {
		t.Fatalf("expected header comment in created config")
	}
	if !strings.Contains(string(data), "defaults:") {
		t.Fatalf("expected default config content in created file")
	}
}

func TestCmdConfigInit_RefusesOverwriteUntilForce(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	output := "custom-config.yml"
	if err := os.WriteFile(output, []byte("old"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	runErr := CmdConfigInit(newConfigCtx(t, "--output", output))
	if runErr == nil {
		t.Fatalf("expected error on overwrite without --force")
	}
	if !strings.Contains(runErr.Error(), "already exists. Use --force to overwrite") {
		t.Fatalf("expected overwrite guidance, got: %v", runErr)
	}

	if err := CmdConfigInit(newConfigCtx(t, "--output", output, "--force")); err != nil {
		t.Fatalf("CmdConfigInit --force: %v", err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected config file after --force: %v", err)
	}
}

func newConfigApplyContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("config-apply", flag.ContinueOnError)
	for _, f := range []cli.Flag{
		&cli.StringFlag{Name: "config"},
		&cli.IntFlag{Name: "timeout"},
		&cli.BoolFlag{Name: "parallel"},
		&cli.IntFlag{Name: "max-parallel"},
		&cli.BoolFlag{Name: "continue-on-error"},
		&cli.BoolFlag{Name: "verbose"},
		&cli.BoolFlag{Name: "docker"},
		&cli.BoolFlag{Name: "pull"},
		&cli.StringFlag{Name: "network"},
		&cli.StringSliceFlag{Name: "volume"},
	} {
		if err := f.Apply(fs); err != nil {
			t.Fatalf("register flag: %v", err)
		}
	}

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}

	return cli.NewContext(nil, fs, nil)
}

func TestLoadConfigWithDefaults_AppliesRunnerDefaultsAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	configContent := `defaults:
  runner: docker
  timeout: 22
  parallel: true
  max_parallel: 6
  continue_on_error: true
  verbose: true
docker:
  pull: true
  network: host
  volumes:
    - ./cache
    - ./artifacts
environment:
  APP: from-config
  CI: from-config
  PRESET: should-stay-from-env
`

	if err := os.WriteFile(".git-ci.yml", []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("CI", "")
	t.Setenv("GIT_CI", "")
	t.Setenv("PRESET", "from-env")

	ctx := newConfigApplyContext(t, "--config", ".git-ci.yml")
	loaded, err := LoadConfigWithDefaults(ctx)
	if err != nil {
		t.Fatalf("LoadConfigWithDefaults: %v", err)
	}
	if loaded == nil || loaded.Defaults.Runner != "docker" {
		t.Fatalf("expected config to load, got %#v", loaded)
	}

	if !ctx.Bool("docker") {
		t.Fatalf("expected defaults.runner=docker to set --docker")
	}
	if !ctx.Bool("parallel") {
		t.Errorf("expected defaults.parallel=true to set --parallel")
	}
	if got := ctx.Int("max-parallel"); got != 6 {
		t.Errorf("expected max-parallel from config, got %d", got)
	}
	if got := ctx.Int("timeout"); got != 22 {
		t.Errorf("expected timeout from config, got %d", got)
	}
	if !ctx.Bool("continue-on-error") {
		t.Errorf("expected continue_on_error from config")
	}
	if !ctx.Bool("verbose") {
		t.Errorf("expected verbose from config")
	}
	if !ctx.Bool("pull") {
		t.Errorf("expected docker.pull from config to set --pull")
	}
	if got := ctx.String("network"); got != "host" {
		t.Fatalf("expected docker.network to set --network, got %q", got)
	}

	volumes := ctx.StringSlice("volume")
	if len(volumes) != 2 || volumes[0] != "./cache" || volumes[1] != "./artifacts" {
		t.Fatalf("expected docker.volumes from config, got %#v", volumes)
	}
	if got := os.Getenv("APP"); got != "from-config" {
		t.Errorf("expected APP injected from config env, got %q", got)
	}
	if got := os.Getenv("PRESET"); got != "from-env" {
		t.Errorf("expected PRESET env to remain from caller, got %q", got)
	}
	if got := os.Getenv("CI"); got == "" {
		t.Errorf("expected CI default env to be injected from config env")
	}
}
