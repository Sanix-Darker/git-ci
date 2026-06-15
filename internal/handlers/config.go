package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cli "github.com/urfave/cli/v2"
	yaml "gopkg.in/yaml.v3"
)

// GitCIConfig represents the git-ci configuration
type GitCIConfig struct {
	Defaults    DefaultsConfig    `yaml:"defaults"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Docker      DockerConfig      `yaml:"docker,omitempty"`
	Cache       CacheConfig       `yaml:"cache,omitempty"`
	Artifacts   ArtifactsConfig   `yaml:"artifacts,omitempty"`
	Hooks       HooksConfig       `yaml:"hooks,omitempty"`
}

// DefaultsConfig represents default settings
type DefaultsConfig struct {
	Runner          string `yaml:"runner,omitempty"`
	Timeout         int    `yaml:"timeout,omitempty"`
	Parallel        bool   `yaml:"parallel,omitempty"`
	MaxParallel     int    `yaml:"max_parallel,omitempty"`
	ContinueOnError bool   `yaml:"continue_on_error,omitempty"`
	Verbose         bool   `yaml:"verbose,omitempty"`
}

// DockerConfig represents Docker-specific configuration
type DockerConfig struct {
	Pull     bool              `yaml:"pull,omitempty"`
	Network  string            `yaml:"network,omitempty"`
	Volumes  []string          `yaml:"volumes,omitempty"`
	Registry string            `yaml:"registry,omitempty"`
	Auth     map[string]string `yaml:"auth,omitempty"`
}

// CacheConfig represents cache configuration
type CacheConfig struct {
	Enabled bool     `yaml:"enabled,omitempty"`
	Paths   []string `yaml:"paths,omitempty"`
	Key     string   `yaml:"key,omitempty"`
}

// ArtifactsConfig represents artifacts configuration
type ArtifactsConfig struct {
	Paths    []string `yaml:"paths,omitempty"`
	ExpireIn string   `yaml:"expire_in,omitempty"`
	Storage  string   `yaml:"storage,omitempty"`
}

// HooksConfig represents hook configuration
type HooksConfig struct {
	BeforeJob []string `yaml:"before_job,omitempty"`
	AfterJob  []string `yaml:"after_job,omitempty"`
	OnFailure []string `yaml:"on_failure,omitempty"`
	OnSuccess []string `yaml:"on_success,omitempty"`
}

// CmdConfigShow handles the config show command
func CmdConfigShow(c *cli.Context) error {
	configFile := c.String("config")
	if configFile == "" {
		configFile = findConfigFile()
	}

	if configFile == "" {
		fmt.Println("No configuration file found")
		fmt.Println("\nTo create a configuration file, run:")
		fmt.Println("  git-ci config init")
		return nil
	}

	// Load configuration
	config, err := loadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Display configuration
	fmt.Printf("Configuration from: %s\n", configFile)
	fmt.Println(strings.Repeat("=", 60))

	// Display as YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal configuration: %w", err)
	}

	fmt.Println(string(data))
	return nil
}

// CmdConfigInit handles the config init command
func CmdConfigInit(c *cli.Context) error {
	configFile := c.String("output")
	if configFile == "" {
		configFile = ".git-ci.yml"
	}

	// Check if file exists
	if _, err := os.Stat(configFile); err == nil && !c.Bool("force") {
		return fmt.Errorf("configuration file %s already exists. Use --force to overwrite", configFile)
	}

	// Create default configuration
	config := createDefaultConfig()

	// Write configuration
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal configuration: %w", err)
	}

	// Add header comment
	content := "# git-ci configuration file\n# https://github.com/sanix-darker/git-ci\n\n" + string(data)

	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write configuration file: %w", err)
	}

	fmt.Printf("✓ Created configuration file: %s\n", configFile)
	fmt.Println("\nYou can now customize the configuration and run:")
	fmt.Printf("  git-ci run --config %s\n", configFile)

	return nil
}

// loadConfig loads configuration from file
func loadConfig(filename string) (*GitCIConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config GitCIConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// LoadConfigWithDefaults loads configuration and applies to CLI context
func LoadConfigWithDefaults(c *cli.Context) (*GitCIConfig, error) {
	configFile := c.String("config")
	if configFile == "" {
		configFile = findConfigFile()
	}

	if configFile == "" {
		// Return empty config if no file found
		return &GitCIConfig{}, nil
	}

	config, err := loadConfig(configFile)
	if err != nil {
		return nil, err
	}

	// Apply configuration to context (if not already set by flags)
	applyConfigToContext(c, config)

	return config, nil
}

// findConfigFile searches for configuration file
func findConfigFile() string {
	// Search paths in order of priority
	searchPaths := []string{
		".git-ci.yml",
		".git-ci.yaml",
		".github/.git-ci.yml",
		".gitlab/.git-ci.yml",
	}

	// Also check home directory
	if home, err := os.UserHomeDir(); err == nil {
		searchPaths = append(searchPaths,
			filepath.Join(home, ".git-ci.yml"),
			filepath.Join(home, ".config", "git-ci", "config.yml"),
		)
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// createDefaultConfig creates a default configuration
func createDefaultConfig() *GitCIConfig {
	return &GitCIConfig{
		Defaults: DefaultsConfig{
			Runner:          "bash",
			Timeout:         30,
			Parallel:        false,
			MaxParallel:     4,
			ContinueOnError: false,
			Verbose:         false,
		},
		Environment: map[string]string{
			"CI":     "true",
			"GIT_CI": "true",
		},
		Docker: DockerConfig{
			Pull:    true,
			Network: "bridge",
			Volumes: []string{},
		},
		Cache: CacheConfig{
			Enabled: true,
			Paths: []string{
				"node_modules",
				".cache",
				"vendor",
			},
		},
		Artifacts: ArtifactsConfig{
			Paths: []string{
				"dist",
				"build",
				"coverage",
			},
			ExpireIn: "1 week",
		},
		Hooks: HooksConfig{
			BeforeJob: []string{},
			AfterJob:  []string{},
			OnFailure: []string{},
			OnSuccess: []string{},
		},
	}
}

// applyConfigToContext applies configuration to CLI context
func applyConfigToContext(c *cli.Context, config *GitCIConfig) {
	// Only apply if not already set by flags

	// Apply defaults
	if !c.IsSet("timeout") && config.Defaults.Timeout > 0 {
		_ = c.Set("timeout", fmt.Sprintf("%d", config.Defaults.Timeout))
	}

	if !c.IsSet("parallel") && config.Defaults.Parallel {
		_ = c.Set("parallel", "true")
	}

	if !c.IsSet("max-parallel") && config.Defaults.MaxParallel > 0 {
		_ = c.Set("max-parallel", fmt.Sprintf("%d", config.Defaults.MaxParallel))
	}

	if !c.IsSet("continue-on-error") && config.Defaults.ContinueOnError {
		_ = c.Set("continue-on-error", "true")
	}

	if !c.IsSet("verbose") && config.Defaults.Verbose {
		_ = c.Set("verbose", "true")
	}

	// Apply Docker configuration
	if !c.IsSet("docker") && config.Defaults.Runner == "docker" {
		_ = c.Set("docker", "true")
	}

	if !c.IsSet("pull") && config.Docker.Pull {
		_ = c.Set("pull", "true")
	}

	if !c.IsSet("network") && config.Docker.Network != "" {
		_ = c.Set("network", config.Docker.Network)
	}

	// Apply volumes
	if len(config.Docker.Volumes) > 0 && !c.IsSet("volume") {
		for _, vol := range config.Docker.Volumes {
			_ = c.Set("volume", vol)
		}
	}

	// Apply environment variables
	for key, value := range config.Environment {
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}
