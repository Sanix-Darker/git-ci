package config

import (
	"os"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if cfg.DryRun {
		t.Error("expected DryRun to be false by default")
	}

	if cfg.Verbose {
		t.Error("expected Verbose to be false by default")
	}

	if !cfg.PullImages {
		t.Error("expected PullImages to be true by default")
	}

	if cfg.Timeout != 30 {
		t.Errorf("expected Timeout 30, got %d", cfg.Timeout)
	}

	if cfg.WorkDir == "" {
		t.Error("expected WorkDir to be set")
	}

	if cfg.Environment == nil {
		t.Error("expected Environment to be initialized")
	}
}

func TestGetCacheDir(t *testing.T) {
	// With env var set
	os.Setenv("GIT_CI_CACHE_DIR", "/tmp/test-cache")
	defer os.Unsetenv("GIT_CI_CACHE_DIR")

	dir := GetCacheDir()
	if dir != "/tmp/test-cache" {
		t.Errorf("expected '/tmp/test-cache', got %q", dir)
	}
}

func TestGetCacheDir_Default(t *testing.T) {
	os.Unsetenv("GIT_CI_CACHE_DIR")

	dir := GetCacheDir()
	if dir == "" {
		t.Error("expected non-empty cache dir")
	}
}

func TestGetConfigDir(t *testing.T) {
	os.Setenv("GIT_CI_CONFIG_DIR", "/tmp/test-config")
	defer os.Unsetenv("GIT_CI_CONFIG_DIR")

	dir := GetConfigDir()
	if dir != "/tmp/test-config" {
		t.Errorf("expected '/tmp/test-config', got %q", dir)
	}
}

func TestGetConfigDir_Default(t *testing.T) {
	os.Unsetenv("GIT_CI_CONFIG_DIR")

	dir := GetConfigDir()
	if dir == "" {
		t.Error("expected non-empty config dir")
	}
}
