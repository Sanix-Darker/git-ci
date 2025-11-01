package runners

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

type BashRunner struct {
	config      *config.RunnerConfig
	environment map[string]string
	formatter   *OutputFormatter
	mu          sync.Mutex
}

// NewBashRunner creates a new bash runner with configuration
func NewBashRunner(cfg *config.RunnerConfig) *BashRunner {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	return &BashRunner{
		config:      cfg,
		environment: make(map[string]string),
		formatter:   NewOutputFormatter(cfg.Verbose),
	}
}

func (r *BashRunner) RunJob(job *types.Job, workdir string) error {
	startTime := time.Now()

	// Resolve absolute workdir
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return fmt.Errorf("invalid workdir: %w", err)
	}

	// Validate workdir exists
	if _, err := os.Stat(absWorkdir); os.IsNotExist(err) {
		return fmt.Errorf("workdir does not exist: %s", absWorkdir)
	}

	// Print job header
	r.formatter.PrintHeader(job.Name, absWorkdir, "bash (native)")

	// Show dry run mode if enabled
	if r.config.DryRun {
		r.formatter.PrintDryRun()
	}

	// Setup job environment
	jobEnv := r.mergeEnvironments(job.Environment, r.config.Environment)
	r.setupJobEnvironment(job, absWorkdir)

	// Print environment variables if verbose
	if r.config.Verbose && len(jobEnv) > 0 {
		r.formatter.PrintEnvironment(jobEnv)
	}

	// Initialize job summary
	summary := &JobSummary{
		JobName:    job.Name,
		TotalSteps: len(job.Steps),
		Success:    true,
	}

	// Execute steps
	for i, step := range job.Steps {
		stepNum := i + 1
		stepStart := time.Now()

		// Check for timeout
		if r.config.Timeout > 0 {
			elapsed := time.Since(startTime).Minutes()
			if elapsed > float64(r.config.Timeout) {
				summary.Success = false
				summary.Errors = append(summary.Errors, fmt.Sprintf("Job timeout exceeded (%d minutes)", r.config.Timeout))
				break
			}
		}

		// Check if step should run
		if !r.shouldRunStep(&step, jobEnv) {
			r.formatter.PrintStepHeader(step.Name, stepNum, len(job.Steps))
			r.formatter.PrintStepSkipped("condition not met")
			summary.SkippedSteps++
			continue
		}

		// Print step header
		r.formatter.PrintStepHeader(step.Name, stepNum, len(job.Steps))

		// Execute step
		err := r.RunStep(&step, jobEnv, absWorkdir)
		stepDuration := time.Since(stepStart)

		if err != nil {
			summary.FailedSteps++
			if step.ContinueOnErr {
				r.formatter.PrintWarning(fmt.Sprintf("Step failed but continuing: %v", err))
				r.formatter.PrintStepComplete(stepDuration)
			} else {
				r.formatter.PrintStepFailed(err, stepDuration)
				summary.Success = false
				summary.Errors = append(summary.Errors, fmt.Sprintf("Step '%s' failed: %v", step.Name, err))
				break
			}
		} else {
			summary.CompletedSteps++
			r.formatter.PrintStepComplete(stepDuration)
		}
	}

	// Print job summary
	summary.Duration = time.Since(startTime)
	if r.config.Verbose {
		r.formatter.PrintJobSummary(summary)
	} else {
		r.formatter.PrintJobComplete(job.Name, summary.Duration, summary.Success)
	}

	return nil
}

func (r *BashRunner) RunStep(step *types.Step, env map[string]string, workdir string) error {
	// Handle action steps
	if step.Uses != "" {
		return r.runActionStep(step, env, workdir)
	}

	// Skip empty run steps
	if step.Run == "" {
		return nil
	}

	// Dry run mode
	if r.config.DryRun {
		r.printDryRun(step)
		return nil
	}

	// Determine shell and prepare command
	shell := r.getShell(step.Shell)
	cmd := r.prepareCommand(shell, step.Run)

	// Set working directory
	if step.WorkingDir != "" {
		cmd.Dir = filepath.Join(workdir, step.WorkingDir)
	} else {
		cmd.Dir = workdir
	}

	// Setup environment
	cmd.Env = r.buildStepEnvironment(env, step.Env)

	// Setup timeout for step
	ctx := context.Background()
	if step.TimeoutMin > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutMin)*time.Minute)
		defer cancel()
		cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
		cmd.Dir = workdir
		cmd.Env = r.buildStepEnvironment(env, step.Env)
	}

	// Print command if verbose
	if r.config.Verbose {
		r.formatter.PrintCommand(step.Run, 2)
	}

	// Execute with retry if configured
	if step.RetryPolicy != nil && step.RetryPolicy.MaxAttempts > 1 {
		return r.executeWithRetry(cmd, step)
	}

	// Normal execution
	return r.executeCommand(cmd, step.Name)
}

func (r *BashRunner) runActionStep(step *types.Step, env map[string]string, workdir string) error {
	r.formatter.PrintInfo(fmt.Sprintf("Action: %s", step.Uses))

	// Parse action reference
	parts := strings.Split(step.Uses, "@")
	action := parts[0]
	version := "latest"
	if len(parts) > 1 {
		version = parts[1]
	}

	// Handle common GitHub Actions with bash equivalents
	switch action {
	case "actions/checkout":
		return r.runCheckoutAction(step, workdir)
	case "actions/setup-go", "actions/setup-node", "actions/setup-python":
		return r.runSetupAction(action, step, version)
	default:
		r.formatter.PrintWarning(fmt.Sprintf("Unsupported action: %s@%s (skipping)", action, version))
		if r.config.Verbose && len(step.With) > 0 {
			r.formatter.PrintSection("Action Parameters")
			for k, v := range step.With {
				r.formatter.PrintKeyValue(k, v, 2)
			}
		}
		return nil
	}
}

func (r *BashRunner) runCheckoutAction(step *types.Step, workdir string) error {
	r.formatter.PrintInfo("Simulating checkout action")

	if r.config.DryRun {
		r.formatter.PrintCommand("git fetch && git checkout", 2)
		return nil
	}

	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = workdir
	if err := cmd.Run(); err == nil {
		fetchCmd := exec.Command("git", "fetch", "--all", "--tags")
		fetchCmd.Dir = workdir
		if err := fetchCmd.Run(); err != nil {
			return fmt.Errorf("git fetch failed: %w", err)
		}
		r.formatter.PrintInfo("Repository updated")
	} else {
		r.formatter.PrintInfo("Not in a git repository, skipping checkout")
	}

	return nil
}

func (r *BashRunner) runSetupAction(action string, step *types.Step, version string) error {
	toolName := strings.TrimPrefix(action, "actions/setup-")
	toolVersion := step.With[fmt.Sprintf("%s-version", toolName)]
	if toolVersion == "" {
		toolVersion = version
	}

	r.formatter.PrintInfo(fmt.Sprintf("Checking %s %s", toolName, toolVersion))

	if r.config.DryRun {
		r.formatter.PrintInfo(fmt.Sprintf("Would check/install %s %s", toolName, toolVersion))
		return nil
	}

	// Check if tool is installed
	var checkCmd *exec.Cmd
	switch toolName {
	case "go":
		checkCmd = exec.Command("go", "version")
	case "node":
		checkCmd = exec.Command("node", "--version")
	case "python":
		checkCmd = exec.Command("python3", "--version")
	default:
		checkCmd = exec.Command(toolName, "--version")
	}

	output, err := checkCmd.Output()
	if err == nil {
		r.formatter.PrintInfo(fmt.Sprintf("%s is installed: %s", toolName, strings.TrimSpace(string(output))))
	} else {
		r.formatter.PrintWarning(fmt.Sprintf("%s is not installed. Please install it manually", toolName))
	}

	return nil
}

func (r *BashRunner) prepareCommand(shell, script string) *exec.Cmd {
	switch shell {
	case "bash":
		return exec.Command("bash", "-eo", "pipefail", "-c", script)
	case "sh":
		return exec.Command("sh", "-e", "-c", script)
	case "pwsh", "powershell":
		return exec.Command("pwsh", "-Command", script)
	case "python", "python3":
		return exec.Command("python3", "-c", script)
	case "node":
		return exec.Command("node", "-e", script)
	default:
		return exec.Command(shell, "-c", script)
	}
}

func (r *BashRunner) executeCommand(cmd *exec.Cmd, stepName string) error {
	// Create pipes for output streaming
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	// Stream output in real-time
	var wg sync.WaitGroup
	wg.Add(2)

	var stdoutBuf, stderrBuf bytes.Buffer

	go r.streamOutput(stdout, &stdoutBuf, &wg, 2)
	go r.streamOutput(stderr, &stderrBuf, &wg, 2)

	wg.Wait()

	// Wait for command to complete
	if err := cmd.Wait(); err != nil {
		errMsg := fmt.Sprintf("command failed: %v", err)
		if stderrBuf.Len() > 0 && r.config.Verbose {
			errMsg += fmt.Sprintf("\nStderr output:\n%s", stderrBuf.String())
		}
		return fmt.Errorf(errMsg)
	}

	return nil
}

func (r *BashRunner) executeWithRetry(cmd *exec.Cmd, step *types.Step) error {
	policy := step.RetryPolicy
	maxAttempts := policy.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			r.formatter.PrintInfo(fmt.Sprintf("Retry attempt %d/%d", attempt, maxAttempts))

			// Parse and apply delay
			if policy.Delay != "" {
				if duration, err := time.ParseDuration(policy.Delay); err == nil {
					time.Sleep(duration)
				}
			}
		}

		// Clone command for retry
		retryCmd := exec.Command(cmd.Path, cmd.Args[1:]...)
		retryCmd.Dir = cmd.Dir
		retryCmd.Env = cmd.Env

		if err := r.executeCommand(retryCmd, step.Name); err != nil {
			lastErr = err
			r.formatter.PrintWarning(fmt.Sprintf("Attempt %d failed: %v", attempt, err))
		} else {
			return nil
		}
	}

	return fmt.Errorf("all %d attempts failed, last error: %w", maxAttempts, lastErr)
}

func (r *BashRunner) streamOutput(reader io.Reader, capture *bytes.Buffer, wg *sync.WaitGroup, indent int) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		r.formatter.PrintOutput(line, indent)

		if capture != nil {
			capture.WriteString(line + "\n")
		}
	}
}

func (r *BashRunner) setupJobEnvironment(job *types.Job, workdir string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Set standard CI environment variables
	r.environment["CI"] = "true"
	r.environment["GIT_CI"] = "true"
	r.environment["BASH_RUNNER"] = "true"
	r.environment["JOB_NAME"] = job.Name
	r.environment["WORKSPACE"] = workdir

	// Detect git information
	if gitBranch := r.getGitBranch(workdir); gitBranch != "" {
		r.environment["GIT_BRANCH"] = gitBranch
	}

	if gitCommit := r.getGitCommit(workdir); gitCommit != "" {
		r.environment["GIT_COMMIT"] = gitCommit
	}
}

func (r *BashRunner) buildStepEnvironment(jobEnv map[string]string, stepEnv map[string]string) []string {
	// Start with OS environment
	env := os.Environ()

	// Add runner environment
	for k, v := range r.environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Add job environment
	for k, v := range jobEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Add step-specific environment
	for k, v := range stepEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	return env
}

func (r *BashRunner) mergeEnvironments(envs ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, env := range envs {
		for k, v := range env {
			result[k] = v
		}
	}
	return result
}

func (r *BashRunner) shouldRunStep(step *types.Step, env map[string]string) bool {
	if step.If == "" {
		return true
	}

	// Simple condition evaluation
	condition := step.If

	switch condition {
	case "always()":
		return true
	case "success()":
		return true
	case "failure()":
		return false
	case "cancelled()":
		return false
	default:
		return true
	}
}

func (r *BashRunner) getShell(specified string) string {
	if specified != "" {
		return specified
	}
	return r.getDefaultShell()
}

func (r *BashRunner) getDefaultShell() string {
	shells := []string{"bash", "sh"}

	for _, shell := range shells {
		if _, err := exec.LookPath(shell); err == nil {
			return shell
		}
	}

	return "sh"
}

func (r *BashRunner) getGitBranch(workdir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workdir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (r *BashRunner) getGitCommit(workdir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workdir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (r *BashRunner) printDryRun(step *types.Step) {
	r.formatter.PrintSection("Would execute")
	r.formatter.PrintKeyValue("Shell", r.getShell(step.Shell), 2)

	if step.WorkingDir != "" {
		r.formatter.PrintKeyValue("Working Dir", step.WorkingDir, 2)
	}

	if len(step.Env) > 0 {
		r.formatter.PrintSubSection("Environment:")
		for k, v := range step.Env {
			r.formatter.PrintKeyValue(k, v, 4)
		}
	}

	r.formatter.PrintSubSection("Command:")
	r.formatter.PrintCommand(step.Run, 4)
}

func (r *BashRunner) Cleanup() error {
	// Clean up any temporary resources
	return nil
}

// GetRunnerType returns the type of this runner
func (r *BashRunner) GetRunnerType() types.RunnerType {
	return types.RunnerTypeBash
}
