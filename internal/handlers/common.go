package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/internal/parsers"
	"github.com/sanix-darker/git-ci/pkg/types"
	cli "github.com/urfave/cli/v2"
)

// parseInput parses the workflow file with auto-detection
func parseInput(workflowFile string) (*types.Pipeline, error) {
	// Auto-detect parser based on file path
	var parser types.Parser

	if workflowFile == "" {
		// Try to auto-detect workflow file
		if _, err := os.Stat(".github/workflows/ci.yml"); err == nil {
			workflowFile = ".github/workflows/ci.yml"
			parser = &parsers.GithubParser{}
		} else if _, err := os.Stat(".gitlab-ci.yml"); err == nil {
			workflowFile = ".gitlab-ci.yml"
			parser = &parsers.GitlabParser{}
		} else {
			// Try to find any workflow file
			patterns := []string{
				".github/workflows/*.yml",
				".github/workflows/*.yaml",
				".gitlab-ci.yml",
				".gitlab-ci.yaml",
				"bitbucket-pipelines.yml",
				"azure-pipelines.yml",
				".circleci/config.yml",
			}

			for _, pattern := range patterns {
				matches, _ := filepath.Glob(pattern)
				if len(matches) > 0 {
					workflowFile = matches[0]
					break
				}
			}

			if workflowFile == "" {
				return nil, fmt.Errorf("no CI configuration file found. Use -f to specify file")
			}
		}
	}

	// Detect parser from file path if not already set
	if parser == nil {
		parser = detectParser(workflowFile)
	}

	pipeline, err := parser.Parse(workflowFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow: %w", err)
	}

	return pipeline, nil
}

// detectParser detects the appropriate parser based on file path
func detectParser(filePath string) types.Parser {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)

	if strings.Contains(dir, ".github/workflows") || strings.Contains(base, "github") {
		return &parsers.GithubParser{}
	} else if strings.Contains(base, "gitlab") || base == ".gitlab-ci.yml" || base == ".gitlab-ci.yaml" {
		return &parsers.GitlabParser{}
	} else if strings.Contains(base, "bitbucket") {
		// return &parsers.BitbucketParser{} // If implemented
		return &parsers.GithubParser{} // Fallback
	} else if strings.Contains(base, "azure") {
		// return &parsers.AzureParser{} // If implemented
		return &parsers.GithubParser{} // Fallback
	} else {
		// Default to GitHub parser
		return &parsers.GithubParser{}
	}
}

// getWorkdir gets the working directory from context or current directory
func getWorkdir(c *cli.Context) (string, error) {
	workdir := c.String("workdir")

	if workdir == "" || workdir == "." {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("invalid workdir: %w", err)
	}

	// Verify workdir exists
	if _, err := os.Stat(absWorkdir); os.IsNotExist(err) {
		return "", fmt.Errorf("workdir does not exist: %s", absWorkdir)
	}

	return absWorkdir, nil
}

// buildRunnerConfig builds runner configuration from CLI context
func buildRunnerConfig(c *cli.Context) *config.RunnerConfig {
	cfg := config.DefaultConfig()

	// Update from flags
	cfg.Verbose = c.Bool("verbose")
	cfg.DryRun = c.Bool("dry-run")
	cfg.PullImages = c.Bool("pull")
	cfg.Timeout = c.Int("timeout")

	// Set working directory
	if workdir, err := getWorkdir(c); err == nil {
		cfg.WorkDir = workdir
	}

	// Parse environment variables
	cfg.Environment = parseEnvironmentVars(c)

	// Parse volumes
	if volumes := c.StringSlice("volume"); len(volumes) > 0 {
		cfg.Volumes = volumes
	}

	// Set network
	if network := c.String("network"); network != "" {
		cfg.Network = network
	}

	return cfg
}

// parseEnvironmentVars parses environment variables from context
func parseEnvironmentVars(c *cli.Context) map[string]string {
	env := make(map[string]string)

	// Add from --env flags
	for _, e := range c.StringSlice("env") {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			env[parts[0]] = parts[1]
		}
	}

	// Add from --env-file
	if envFile := c.String("env-file"); envFile != "" {
		if fileEnv, err := loadEnvFile(envFile); err == nil {
			for k, v := range fileEnv {
				env[k] = v
			}
		}
	}

	return env
}

// loadEnvFile loads environment variables from a file
func loadEnvFile(filename string) (map[string]string, error) {
	env := make(map[string]string)

	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read env file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			// Remove quotes if present
			value = strings.Trim(value, `"'`)

			env[key] = value
		}
	}

	return env, nil
}

// filterJobs filters jobs based on only/except lists
func filterJobs(jobs map[string]*types.Job, only, except []string) map[string]*types.Job {
	if len(only) == 0 && len(except) == 0 {
		return jobs
	}

	filtered := make(map[string]*types.Job)

	for name, job := range jobs {
		// Check only list
		if len(only) > 0 {
			found := false
			for _, pattern := range only {
				if matchPattern(name, pattern) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Check except list
		if len(except) > 0 {
			skip := false
			for _, pattern := range except {
				if matchPattern(name, pattern) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
		}

		filtered[name] = job
	}

	return filtered
}

// matchPattern checks if a name matches a pattern (supports wildcards)
func matchPattern(name, pattern string) bool {
	if pattern == name {
		return true
	}

	// Simple wildcard support
	if strings.Contains(pattern, "*") {
		pattern = strings.ReplaceAll(pattern, "*", "")
		return strings.Contains(name, pattern)
	}

	return false
}

// getJobsByStage returns jobs belonging to a specific stage
func getJobsByStage(pipeline *types.Pipeline, stage string) map[string]*types.Job {
	jobs := make(map[string]*types.Job)

	for name, job := range pipeline.Jobs {
		if job.Stage == stage {
			jobs[name] = job
		}
	}

	return jobs
}

// printVerbose prints message if verbose mode is enabled
func printVerbose(c *cli.Context, format string, args ...interface{}) {
	if c.Bool("verbose") || c.Bool("debug") {
		fmt.Printf(format, args...)
	}
}

// printDebug prints message if debug mode is enabled
func printDebug(c *cli.Context, format string, args ...interface{}) {
	if c.Bool("debug") {
		fmt.Printf("[DEBUG] "+format, args...)
	}
}

// detectProvider auto-detects CI provider from file or environment
func detectProvider(c *cli.Context) string {
	provider := c.String("provider")
	if provider != "" && provider != "auto" {
		return provider
	}

	// Check environment variables
	if os.Getenv("GITHUB_ACTIONS") != "" {
		return "github"
	}
	if os.Getenv("GITLAB_CI") != "" {
		return "gitlab"
	}

	// Check file paths
	if file := c.String("file"); file != "" {
		if strings.Contains(file, "github") || strings.Contains(file, ".github") {
			return "github"
		}
		if strings.Contains(file, "gitlab") {
			return "gitlab"
		}
	}

	// Try to detect from existing files
	if _, err := os.Stat(".github/workflows"); err == nil {
		return "github"
	}
	if _, err := os.Stat(".gitlab-ci.yml"); err == nil {
		return "gitlab"
	}

	return "auto"
}
