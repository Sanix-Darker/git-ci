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

	fmt.Printf("\n🚀 Running job: %s\n", job.Name)
	fmt.Printf("📁 Working directory: %s\n", absWorkdir)
	fmt.Printf("🖥️  Runner: bash (native)\n")
	fmt.Printf("🐚 Default shell: %s\n", r.getDefaultShell())

	if r.config.DryRun {
		fmt.Println("🔍 DRY RUN MODE - Commands will be displayed but not executed")
	}

	fmt.Println(strings.Repeat("─", 60))

	// Merge environments
	jobEnv := r.mergeEnvironments(job.Environment, r.config.Environment)

	// Setup job-level environment
	r.setupJobEnvironment(job, absWorkdir)

	// Execute steps with proper error handling
	totalSteps := len(job.Steps)
	successCount := 0

	for i, step := range job.Steps {
		stepNum := i + 1

		// Check for timeout
		if r.config.Timeout > 0 {
			elapsed := time.Since(startTime).Minutes()
			if elapsed > float64(r.config.Timeout) {
				return fmt.Errorf("job timeout exceeded (%d minutes)", r.config.Timeout)
			}
		}

		// Skip if condition not met
		if !r.shouldRunStep(&step, jobEnv) {
			fmt.Printf("⏭️  [%d/%d] Skipping: %s (condition not met)\n", stepNum, totalSteps, step.Name)
			continue
		}

		// Execute step
		stepStart := time.Now()
		err := r.RunStep(&step, jobEnv, absWorkdir)
		stepDuration := time.Since(stepStart)

		if err != nil {
			if step.ContinueOnErr {
				fmt.Printf("⚠️  [%d/%d] Step failed but continuing (continue-on-error: true): %v\n",
					stepNum, totalSteps, err)
			} else {
				fmt.Printf("❌ [%d/%d] Step failed after %s: %v\n",
					stepNum, totalSteps, stepDuration.Round(time.Second), err)
				return fmt.Errorf("step '%s' failed: %w", step.Name, err)
			}
		} else {
			successCount++
			fmt.Printf("✅ [%d/%d] Step completed in %s\n\n",
				stepNum, totalSteps, stepDuration.Round(time.Second))
		}
	}

	duration := time.Since(startTime)
	fmt.Println(strings.Repeat("─", 60))

	if successCount == totalSteps {
		fmt.Printf("✨ Job completed successfully! All %d steps passed in %s\n",
			totalSteps, duration.Round(time.Second))
	} else {
		fmt.Printf("⚠️  Job completed with warnings: %d/%d steps successful in %s\n",
			successCount, totalSteps, duration.Round(time.Second))
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

	fmt.Printf("▶️  %s\n", step.Name)

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
		// Resolve relative to job workdir
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

	// Execute with retry if configured
	if step.RetryPolicy != nil && step.RetryPolicy.MaxAttempts > 1 {
		return r.executeWithRetry(cmd, step)
	}

	// Normal execution
	return r.executeCommand(cmd, step.Name)
}

func (r *BashRunner) runActionStep(step *types.Step, env map[string]string, workdir string) error {
	fmt.Printf("🎬 Action: %s\n", step.Uses)

	// Parse action reference (e.g., "actions/checkout@v3")
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
	case "actions/setup-go":
		return r.runSetupGoAction(step, env)
	case "actions/setup-node":
		return r.runSetupNodeAction(step, env)
	case "actions/setup-python":
		return r.runSetupPythonAction(step, env)
	case "actions/cache":
		return r.runCacheAction(step, workdir)
	default:
		fmt.Printf("   ⏭️  Skipping unsupported action: %s@%s\n", action, version)
		if r.config.Verbose && len(step.With) > 0 {
			fmt.Println("   Parameters:")
			for k, v := range step.With {
				fmt.Printf("     %s: %s\n", k, v)
			}
		}
		return nil
	}
}

func (r *BashRunner) runCheckoutAction(step *types.Step, workdir string) error {
	fmt.Println("   📥 Simulating checkout action")

	if r.config.DryRun {
		fmt.Println("   Would run: git fetch && git checkout")
		return nil
	}

	// Check if we're in a git repository
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = workdir
	if err := cmd.Run(); err == nil {
		// We're in a git repo, fetch latest
		fetchCmd := exec.Command("git", "fetch", "--all", "--tags")
		fetchCmd.Dir = workdir
		if err := fetchCmd.Run(); err != nil {
			return fmt.Errorf("git fetch failed: %w", err)
		}
		fmt.Println("   ✅ Repository updated")
	} else {
		fmt.Println("   ℹ️  Not in a git repository, skipping checkout")
	}

	return nil
}

func (r *BashRunner) runSetupGoAction(step *types.Step, env map[string]string) error {
	goVersion := step.With["go-version"]
	if goVersion == "" {
		goVersion = "stable"
	}

	fmt.Printf("   🐹 Checking Go %s\n", goVersion)

	if r.config.DryRun {
		fmt.Printf("   Would check/install Go %s\n", goVersion)
		return nil
	}

	// Check if Go is installed
	cmd := exec.Command("go", "version")
	output, err := cmd.Output()
	if err == nil {
		fmt.Printf("   ✅ Go is installed: %s", output)
	} else {
		fmt.Println("   ⚠️  Go is not installed. Please install Go manually")
	}

	return nil
}

func (r *BashRunner) runSetupNodeAction(step *types.Step, env map[string]string) error {
	nodeVersion := step.With["node-version"]
	if nodeVersion == "" {
		nodeVersion = "lts"
	}

	fmt.Printf("   📦 Checking Node.js %s\n", nodeVersion)

	if r.config.DryRun {
		fmt.Printf("   Would check/install Node.js %s\n", nodeVersion)
		return nil
	}

	// Check if Node is installed
	cmd := exec.Command("node", "--version")
	output, err := cmd.Output()
	if err == nil {
		fmt.Printf("   ✅ Node.js is installed: %s", output)
	} else {
		fmt.Println("   ⚠️  Node.js is not installed. Please install Node.js manually")
	}

	return nil
}

func (r *BashRunner) runSetupPythonAction(step *types.Step, env map[string]string) error {
	pythonVersion := step.With["python-version"]
	if pythonVersion == "" {
		pythonVersion = "3.x"
	}

	fmt.Printf("   🐍 Checking Python %s\n", pythonVersion)

	if r.config.DryRun {
		fmt.Printf("   Would check/install Python %s\n", pythonVersion)
		return nil
	}

	// Check if Python is installed
	cmd := exec.Command("python3", "--version")
	output, err := cmd.Output()
	if err == nil {
		fmt.Printf("   ✅ Python is installed: %s", output)
	} else {
		fmt.Println("   ⚠️  Python is not installed. Please install Python manually")
	}

	return nil
}

func (r *BashRunner) runCacheAction(step *types.Step, workdir string) error {
	path := step.With["path"]
	key := step.With["key"]

	fmt.Printf("   💾 Cache action: %s (key: %s)\n", path, key)

	if r.config.DryRun {
		fmt.Println("   Would handle cache")
		return nil
	}

	// Simple cache implementation using local directory
	cacheDir := filepath.Join(config.GetCacheDir(), "actions-cache", key)
	targetPath := filepath.Join(workdir, path)

	if step.With["restore"] == "true" {
		// Restore from cache
		if _, err := os.Stat(cacheDir); err == nil {
			fmt.Printf("   📥 Restoring cache from %s\n", cacheDir)
			// In production, would use proper file copying
		} else {
			fmt.Println("   ℹ️  Cache not found")
		}
	}

	if step.With["save"] == "true" {
		// Save to cache
		if _, err := os.Stat(targetPath); err == nil {
			fmt.Printf("   💾 Saving cache to %s\n", cacheDir)
			os.MkdirAll(filepath.Dir(cacheDir), 0755)
			// In production, would use proper file copying
		}
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
	case "ruby":
		return exec.Command("ruby", "-e", script)
	case "perl":
		return exec.Command("perl", "-e", script)
	default:
		// Try to use the shell directly (from the scheban...)
		return exec.Command(shell, "-c", script)
	}
}

func (r *BashRunner) executeCommand(cmd *exec.Cmd, stepName string) error {
	// Create pipes for real-time output streaming
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

	// Capture output for error reporting
	var stdoutBuf, stderrBuf bytes.Buffer

	go r.streamOutput(stdout, "   ", &stdoutBuf, &wg)
	go r.streamOutput(stderr, "   ", &stderrBuf, &wg)

	// Wait for streams to finish
	wg.Wait()

	// Wait for command to complete
	if err := cmd.Wait(); err != nil {
		// Include stderr in error message for debugging
		errMsg := fmt.Sprintf("command failed: %v", err)
		if stderrBuf.Len() > 0 {
			errMsg += fmt.Sprintf("\nStderr: %s", stderrBuf.String())
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
			fmt.Printf("   🔄 Retry attempt %d/%d\n", attempt, maxAttempts)

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
			fmt.Printf("   ⚠️  Attempt %d failed: %v\n", attempt, err)
		} else {
			return nil // Success
		}
	}

	return fmt.Errorf("all %d attempts failed, last error: %w", maxAttempts, lastErr)
}

func (r *BashRunner) streamOutput(reader io.Reader, prefix string, capture *bytes.Buffer, wg *sync.WaitGroup) {
	defer wg.Done()

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Printf("%s%s\n", prefix, line)

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

	// Detect and set additional environment info
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

	// Add step-specific environment (highest priority)
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

	// Simple condition evaluation (can be enhanced)
	condition := step.If

	// Handle basic conditions
	switch condition {
	case "always()":
		return true
	case "success()":
		return true // Assuming previous steps succeeded
	case "failure()":
		return false // Assuming no failures yet
	case "cancelled()":
		return false
	default:
		// For now, run by default
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
	// Check common shells in order of preference
	shells := []string{"bash", "sh"}

	for _, shell := range shells {
		if _, err := exec.LookPath(shell); err == nil {
			return shell
		}
	}

	return "sh" // Fallback
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
	fmt.Println("   📝 Would execute:")
	fmt.Printf("   Shell: %s\n", r.getShell(step.Shell))

	if step.WorkingDir != "" {
		fmt.Printf("   Working Dir: %s\n", step.WorkingDir)
	}

	if len(step.Env) > 0 {
		fmt.Println("   Environment:")
		for k, v := range step.Env {
			fmt.Printf("     %s=%s\n", k, v)
		}
	}

	fmt.Printf("   Command: %s\n", truncateMultiline(step.Run, 5))
}

func (r *BashRunner) Cleanup() error {
	// Clean up any temporary files or resources
	// For bash runner, typically minimal cleanup needed
	return nil
}
