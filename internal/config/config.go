package config

import (
	"os"
	"path/filepath"
)

// RunnerConfig holds configuration for job runners
type RunnerConfig struct {
	DryRun      bool              // Show what would be executed without running
	Verbose     bool              // Enable verbose output
	PullImages  bool              // Pull Docker images before running
	NoCache     bool              // Disable caching
	WorkDir     string            // Working directory for execution
	Environment map[string]string // Additional environment variables
	Timeout     int               // Timeout in minutes (0 = no timeout)
}

// DefaultConfig returns a RunnerConfig with sensible defaults
func DefaultConfig() *RunnerConfig {
	workDir, _ := os.Getwd()

	return &RunnerConfig{
		DryRun:      false,
		Verbose:     false, // maybe false
		PullImages:  true,
		NoCache:     false,
		WorkDir:     workDir,
		Environment: make(map[string]string),
		Timeout:     30, // 30 minutes default timeout
	}
}

// GetCacheDir returns the cache directory for git-ci
func GetCacheDir() string {
	if cacheDir := os.Getenv("GIT_CI_CACHE_DIR"); cacheDir != "" {
		return cacheDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".git-ci-cache")
	}

	return filepath.Join(homeDir, ".cache", "git-ci")
}

// GetConfigDir returns the config directory for git-ci
func GetConfigDir() string {
	if configDir := os.Getenv("GIT_CI_CONFIG_DIR"); configDir != "" {
		return configDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".git-ci")
	}

	return filepath.Join(homeDir, ".config", "git-ci")
}
