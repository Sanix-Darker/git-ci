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

// detectParser detects the appropriate parser based on file path and content
func detectParser(filePath string) types.Parser {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)

	// Path-based detection (most reliable)
	if strings.Contains(dir, ".github/workflows") || strings.Contains(base, "github") {
		return &parsers.GithubParser{}
	} else if base == ".gitlab-ci.yml" || base == ".gitlab-ci.yaml" || strings.Contains(base, "gitlab") {
		return &parsers.GitlabParser{}
	} else if strings.Contains(dir, ".circleci") {
		return parsers.NewCircleCIParser()
	} else if base == ".drone.yml" || base == ".drone.yaml" {
		return &parsers.DroneParser{}
	} else if base == ".travis.yml" || base == ".travis.yaml" {
		return &parsers.TravisParser{}
	} else if strings.Contains(base, "bitbucket") {
		return &parsers.GithubParser{} // Fallback
	} else if strings.Contains(base, "azure") {
		return &parsers.GithubParser{} // Fallback
	}

	// Content-based detection for files with non-standard names
	if data, err := os.ReadFile(filePath); err == nil {
		content := string(data)

		// CircleCI indicators: version 2.1 + jobs + workflows
		hasCircleCIVersion := strings.Contains(content, "version: 2.1")
		hasCircleCIJobs := strings.Contains(content, "\njobs:") || strings.HasPrefix(content, "jobs:")
		hasWorkflows := strings.Contains(content, "\nworkflows:")
		hasExecutor := strings.Contains(content, "executor:") || strings.Contains(content, "executors:")

		// Drone indicators: kind: pipeline
		hasDroneKind := strings.Contains(content, "kind: pipeline")

		// Travis indicators: language: at top level
		hasTravisLang := strings.Contains(content, "language:")

		// GitHub indicators: "on:" trigger + "jobs:" with "runs-on:"
		hasGitHubOn := strings.Contains(content, "\non:") || strings.HasPrefix(content, "on:")
		hasRunsOn := strings.Contains(content, "runs-on:")

		// GitLab indicators: "stages:" or jobs with "script:"
		hasStages := strings.Contains(content, "\nstages:") || strings.HasPrefix(content, "stages:")
		hasScript := strings.Contains(content, "script:")

		// Prioritize most specific indicators first
		if hasDroneKind {
			return &parsers.DroneParser{}
		} else if hasCircleCIVersion && (hasCircleCIJobs || hasWorkflows || hasExecutor) {
			return parsers.NewCircleCIParser()
		} else if hasTravisLang && (hasScript || hasStages) && !hasGitHubOn {
			// Travis has language: at top level, GitHub workflows don't
			return &parsers.TravisParser{}
		} else if hasTravisLang {
			// language: alone is strongly Travis
			return &parsers.TravisParser{}
		} else if hasGitHubOn && hasRunsOn {
			return &parsers.GithubParser{}
		} else if hasStages || hasScript {
			return &parsers.GitlabParser{}
		}
	}

	// Default fallback
	fmt.Fprintf(os.Stderr, "Warning: could not detect CI provider for %q, defaulting to GitHub Actions parser\n", filePath)
	return &parsers.GithubParser{}
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
	cfg.Quiet = c.Bool("quiet")
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

	// Set resource limits
	if memory := c.String("memory"); memory != "" {
		cfg.Memory = memory
	}
	if cpus := c.String("cpus"); cpus != "" {
		cfg.CPUs = cpus
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
