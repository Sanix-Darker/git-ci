package runners

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

// PodmanRunner executes jobs using Podman CLI (lightweight, no SDK dependencies)
type PodmanRunner struct {
	config       *config.RunnerConfig
	containers   []string
	pods         []string
	formatter    *OutputFormatter
	mu           sync.Mutex
	binaryPath   string // Path to podman binary
	isDocker     bool   // If true, use docker command instead
}

// NewPodmanRunner creates a new Podman runner using CLI commands
func NewPodmanRunner(cfg *config.RunnerConfig) (*PodmanRunner, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	formatter := NewOutputFormatter(cfg.Verbose)

	// Find container runtime binary
	binaryPath, isDocker, err := findContainerRuntime()
	if err != nil {
		return nil, err
	}

	// Verify it works
	version, err := getContainerVersion(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("failed to verify %s: %w", binaryPath, err)
	}

	if cfg.Verbose {
		formatter.PrintDebug(fmt.Sprintf("Using %s version: %s", filepath.Base(binaryPath), version))
	}

	return &PodmanRunner{
		config:     cfg,
		containers: []string{},
		pods:       []string{},
		formatter:  formatter,
		binaryPath: binaryPath,
		isDocker:   isDocker,
	}, nil
}

// findContainerRuntime finds podman or docker binary
func findContainerRuntime() (string, bool, error) {
	// Check for podman first (preferred)
	if path, err := exec.LookPath("podman"); err == nil {
		return path, false, nil
	}

	// Fallback to docker with podman compatibility
	if path, err := exec.LookPath("docker"); err == nil {
		// Check if it's actually podman aliased as docker
		cmd := exec.Command(path, "version", "--format", "{{.Client.Version}}")
		output, _ := cmd.Output()
		isPodman := strings.Contains(string(output), "podman")
		return path, !isPodman, nil
	}

	return "", false, fmt.Errorf("neither podman nor docker found in PATH")
}

// getContainerVersion gets the version of the container runtime
func getContainerVersion(binaryPath string) (string, error) {
	cmd := exec.Command(binaryPath, "version", "--format", "{{.Version}}")
	output, err := cmd.Output()
	if err != nil {
		// Fallback for different version formats
		cmd = exec.Command(binaryPath, "--version")
		output, err = cmd.Output()
		if err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(output)), nil
}

// RunJob executes a job using Podman/Docker CLI
func (r *PodmanRunner) RunJob(job *types.Job, workdir string) error {
	ctx := context.Background()
	startTime := time.Now()

	imageName := r.getImageName(job)

	// Print job header
	runtime := "podman"
	if r.isDocker {
		runtime = "docker"
	}
	r.formatter.PrintHeader(job.Name, workdir, fmt.Sprintf("%s (%s)", runtime, imageName))

	if r.config.DryRun {
		r.formatter.PrintDryRun()
		return r.dryRunJob(job)
	}

	// Initialize job summary
	summary := &JobSummary{
		JobName:    job.Name,
		TotalSteps: len(job.Steps),
		Success:    true,
	}

	// Check if image exists locally
	imageExists := r.imageExists(ctx, imageName)

	// Pull image if needed
	if r.config.PullImages || !imageExists {
		progress := r.formatter.NewProgress(fmt.Sprintf("Pulling image %s", imageName))
		if err := r.pullImage(ctx, imageName); err != nil {
			progress.Complete(false)
			return fmt.Errorf("failed to pull image: %w", err)
		}
		progress.Complete(true)
	}

	// Create pod if services are needed (Podman only)
	var podName string
	if len(job.Services) > 0 && !r.isDocker {
		r.formatter.PrintSection("Setting up services")
		podName, err := r.createPod(ctx, job)
		if err != nil {
			return fmt.Errorf("failed to create pod: %w", err)
		}
		r.pods = append(r.pods, podName)

		if err := r.startServices(ctx, job, podName); err != nil {
			return fmt.Errorf("failed to start services: %w", err)
		}
	}

	// Create and run container
	r.formatter.PrintInfo("Creating container")
	containerID, err := r.runContainer(ctx, job, imageName, workdir, podName)
	if err != nil {
		return fmt.Errorf("failed to run container: %w", err)
	}

	r.mu.Lock()
	r.containers = append(r.containers, containerID)
	r.mu.Unlock()

	// Stream logs
	r.formatter.PrintSection("Container Output")
	logErr := make(chan error, 1)
	go func() {
		logErr <- r.streamLogs(ctx, containerID)
	}()

	// Wait for container to finish
	exitCode, err := r.waitContainer(ctx, containerID)

	// Get log streaming result
	select {
	case err := <-logErr:
		if err != nil {
			r.formatter.PrintWarning(fmt.Sprintf("Log streaming warning: %v", err))
		}
	case <-time.After(100 * time.Millisecond):
		// Don't wait forever for logs
	}

	if err != nil {
		summary.Success = false
		summary.Errors = append(summary.Errors, fmt.Sprintf("Container wait error: %v", err))
		return err
	}

	if exitCode != 0 {
		summary.Success = false
		summary.Errors = append(summary.Errors, fmt.Sprintf("Container exited with status %d", exitCode))
		if !job.AllowFailure {
			return fmt.Errorf("container exited with status %d", exitCode)
		}
	}

	summary.CompletedSteps = len(job.Steps)
	summary.Duration = time.Since(startTime)

	if r.config.Verbose {
		r.formatter.PrintJobSummary(summary)
	} else {
		r.formatter.PrintJobComplete(job.Name, summary.Duration, summary.Success)
	}

	return nil
}

// imageExists checks if an image exists locally
func (r *PodmanRunner) imageExists(ctx context.Context, imageName string) bool {
	cmd := exec.CommandContext(ctx, r.binaryPath, "image", "exists", imageName)
	err := cmd.Run()
	return err == nil
}

// pullImage pulls an image
func (r *PodmanRunner) pullImage(ctx context.Context, imageName string) error {
	args := []string{"pull"}

	if !r.config.Verbose {
		args = append(args, "-q")
	}

	args = append(args, imageName)

	cmd := exec.CommandContext(ctx, r.binaryPath, args...)

	if r.config.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	return cmd.Run()
}

// createPod creates a pod for services (Podman only)
func (r *PodmanRunner) createPod(ctx context.Context, job *types.Job) (string, error) {
	if r.isDocker {
		// Docker doesn't support pods, use network instead
		networkName := fmt.Sprintf("git-ci-%s-%d",
			strings.ReplaceAll(strings.ToLower(job.Name), " ", "-"),
			time.Now().Unix())

		cmd := exec.CommandContext(ctx, r.binaryPath, "network", "create", networkName)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to create network: %w", err)
		}

		return networkName, nil
	}

	podName := fmt.Sprintf("git-ci-%s-%d",
		strings.ReplaceAll(strings.ToLower(job.Name), " ", "-"),
		time.Now().Unix())

	cmd := exec.CommandContext(ctx, r.binaryPath, "pod", "create", "--name", podName)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create pod: %w", err)
	}

	r.formatter.PrintDebug(fmt.Sprintf("Created pod: %s", podName))
	return podName, nil
}

// runContainer creates and starts a container
func (r *PodmanRunner) runContainer(ctx context.Context, job *types.Job, imageName, workdir, podName string) (string, error) {
	// Build script from steps
	script := r.buildJobScript(job)

	// Create temporary script file
	scriptFile := fmt.Sprintf("/tmp/git-ci-script-%d.sh", time.Now().Unix())
	if err := os.WriteFile(scriptFile, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("failed to write script: %w", err)
	}
	defer os.Remove(scriptFile)

	// Build run command
	args := []string{"run", "-d"}

	// Add name
	containerName := fmt.Sprintf("git-ci-%s-%d",
		strings.ReplaceAll(strings.ToLower(job.Name), " ", "-"),
		time.Now().Unix())
	args = append(args, "--name", containerName)

	// Working directory
	args = append(args, "-w", "/workspace")

	// Volumes
	args = append(args, "-v", fmt.Sprintf("%s:/workspace", workdir))
	args = append(args, "-v", fmt.Sprintf("%s:/tmp/script.sh:ro", scriptFile))

	// SELinux labels for Podman
	if !r.isDocker {
		// Add :z for automatic relabeling
		args[len(args)-2] = fmt.Sprintf("%s:/workspace:z", workdir)
		args[len(args)-1] = fmt.Sprintf("%s:/tmp/script.sh:ro,z", scriptFile)
	}

	// Additional volumes
	for _, vol := range r.config.Volumes {
		if !r.isDocker && !strings.Contains(vol, ":z") && !strings.Contains(vol, ":Z") {
			// Add z flag for SELinux if not present
			if strings.Contains(vol, ":") {
				vol = vol + ",z"
			}
		}
		args = append(args, "-v", vol)
	}

	// Pod or network
	if podName != "" {
		if r.isDocker {
			args = append(args, "--network", podName)
		} else {
			args = append(args, "--pod", podName)
		}
	} else if r.config.Network != "" {
		args = append(args, "--network", r.config.Network)
	}

	// Environment variables
	for k, v := range r.buildEnvironment(job) {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Resource limits
	if job.Container != nil {
		if job.Container.CPUs != "" {
			args = append(args, "--cpus", job.Container.CPUs)
		}
		if job.Container.Memory != "" {
			args = append(args, "--memory", job.Container.Memory)
		}
	}

	// Labels
	args = append(args, "--label", "git-ci=true")
	args = append(args, "--label", fmt.Sprintf("job=%s", job.Name))

	// Image and command
	args = append(args, imageName, "/bin/sh", "/tmp/script.sh")

	// Execute
	cmd := exec.CommandContext(ctx, r.binaryPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run container: %w\nOutput: %s", err, output)
	}

	containerID := strings.TrimSpace(string(output))
	r.formatter.PrintDebug(fmt.Sprintf("Container started: %s", containerID[:12]))

	return containerID, nil
}

// startServices starts service containers
func (r *PodmanRunner) startServices(ctx context.Context, job *types.Job, podOrNetwork string) error {
	for name, service := range job.Services {
		r.formatter.PrintInfo(fmt.Sprintf("Starting service: %s", name))

		args := []string{"run", "-d"}

		// Name
		serviceName := fmt.Sprintf("git-ci-svc-%s-%d", name, time.Now().Unix())
		args = append(args, "--name", serviceName)

		// Pod or network
		if r.isDocker {
			args = append(args, "--network", podOrNetwork)
		} else {
			args = append(args, "--pod", podOrNetwork)
		}

		// Environment
		for k, v := range service.Env {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}

		// Labels
		args = append(args, "--label", "git-ci=true")
		args = append(args, "--label", fmt.Sprintf("service=%s", name))

		// Image
		args = append(args, service.Image)

		// Command
		if len(service.Command) > 0 {
			args = append(args, service.Command...)
		}

		cmd := exec.CommandContext(ctx, r.binaryPath, args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to start service %s: %w\nOutput: %s", name, err, output)
		}

		r.mu.Lock()
		r.containers = append(r.containers, serviceName)
		r.mu.Unlock()
	}

	return nil
}

// streamLogs streams container logs
func (r *PodmanRunner) streamLogs(ctx context.Context, containerID string) error {
	cmd := exec.CommandContext(ctx, r.binaryPath, "logs", "-f", containerID)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	// Copy output
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(os.Stdout, stdout)
	}()

	go func() {
		defer wg.Done()
		io.Copy(os.Stderr, stderr)
	}()

	wg.Wait()
	return cmd.Wait()
}

// waitContainer waits for container to finish and returns exit code
func (r *PodmanRunner) waitContainer(ctx context.Context, containerID string) (int, error) {
	cmd := exec.CommandContext(ctx, r.binaryPath, "wait", containerID)
	output, err := cmd.Output()
	if err != nil {
		// Try to get exit code from inspect
		return r.getExitCode(ctx, containerID)
	}

	var exitCode int
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &exitCode)
	return exitCode, nil
}

// getExitCode gets container exit code from inspect
func (r *PodmanRunner) getExitCode(ctx context.Context, containerID string) (int, error) {
	cmd := exec.CommandContext(ctx, r.binaryPath, "inspect",
		"--format", "{{.State.ExitCode}}", containerID)

	output, err := cmd.Output()
	if err != nil {
		return -1, err
	}

	var exitCode int
	fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &exitCode)
	return exitCode, nil
}

// buildJobScript builds shell script from job steps
func (r *PodmanRunner) buildJobScript(job *types.Job) string {
	var script strings.Builder

	script.WriteString("#!/bin/sh\n")
	script.WriteString("set -e\n")

	if r.config.Verbose {
		script.WriteString("set -x\n")
	}

	script.WriteString("\necho 'Starting job: ")
	script.WriteString(job.Name)
	script.WriteString("'\n\n")

	for i, step := range job.Steps {
		if step.Run == "" {
			continue
		}

		script.WriteString(fmt.Sprintf("echo '[%d/%d] %s'\n", i+1, len(job.Steps), step.Name))
		script.WriteString("echo '------------------------------------------------------------'\n")

		// Working directory
		if step.WorkingDir != "" {
			script.WriteString(fmt.Sprintf("cd %s\n", step.WorkingDir))
		}

		// Environment
		for k, v := range step.Env {
			script.WriteString(fmt.Sprintf("export %s='%s'\n", k, v))
		}

		// Command
		script.WriteString(step.Run)
		script.WriteString("\n")

		// Continue on error
		if step.ContinueOnErr {
			script.WriteString(" || true\n")
		}

		script.WriteString("echo 'Step completed'\n\n")

		// Reset directory
		if step.WorkingDir != "" {
			script.WriteString("cd /workspace\n")
		}
	}

	script.WriteString("echo 'Job completed successfully!'\n")

	return script.String()
}

// buildEnvironment builds environment variables
func (r *PodmanRunner) buildEnvironment(job *types.Job) map[string]string {
	env := make(map[string]string)

	// Defaults
	env["CI"] = "true"
	env["GIT_CI"] = "true"
	env["JOB_NAME"] = job.Name

	// Job environment
	for k, v := range job.Environment {
		env[k] = v
	}

	// Config environment
	for k, v := range r.config.Environment {
		env[k] = v
	}

	// Container environment
	if job.Container != nil {
		for k, v := range job.Container.Env {
			env[k] = v
		}
	}

	return env
}

// Cleanup removes containers and pods
func (r *PodmanRunner) Cleanup() error {
	if len(r.containers) == 0 && len(r.pods) == 0 {
		return nil
	}

	ctx := context.Background()
	r.formatter.PrintSection("Cleaning up resources")

	var errors []string

	// Remove containers
	for _, containerID := range r.containers {
		cmd := exec.CommandContext(ctx, r.binaryPath, "rm", "-f", containerID)
		if err := cmd.Run(); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to remove container %s", containerID[:12]))
		} else {
			r.formatter.PrintInfo(fmt.Sprintf("Removed container %s", containerID[:12]))
		}
	}

	// Remove pods (Podman only)
	if !r.isDocker {
		for _, pod := range r.pods {
			cmd := exec.CommandContext(ctx, r.binaryPath, "pod", "rm", "-f", pod)
			if err := cmd.Run(); err != nil {
				errors = append(errors, fmt.Sprintf("Failed to remove pod %s", pod))
			} else {
				r.formatter.PrintInfo(fmt.Sprintf("Removed pod %s", pod))
			}
		}
	}

	r.containers = []string{}
	r.pods = []string{}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup had %d errors: %s", len(errors), strings.Join(errors, "; "))
	}

	return nil
}

// dryRunJob performs dry run
func (r *PodmanRunner) dryRunJob(job *types.Job) error {
	r.formatter.PrintSection("Would execute the following steps")

	for i, step := range job.Steps {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(job.Steps), step.Name)

		if step.Run != "" {
			fmt.Printf("  Command:\n")
			for _, line := range strings.Split(step.Run, "\n") {
				fmt.Printf("    %s\n", line)
			}
		}
	}

	return nil
}

// GetRunnerType returns the runner type
func (r *PodmanRunner) GetRunnerType() types.RunnerType {
	if r.isDocker {
		return types.RunnerTypeDocker
	}
	return types.RunnerTypePodman
}

// getImageName determines the image name
func (r *PodmanRunner) getImageName(job *types.Job) string {
	if job.Container != nil && job.Container.Image != "" {
		return job.Container.Image
	}

	if job.Image != "" {
		return job.Image
	}

	// Map common runs-on values
	runsOn := strings.ToLower(job.RunsOn)

	imageMap := map[string]string{
		"ubuntu-latest": "ubuntu:latest",
		"ubuntu-22.04":  "ubuntu:22.04",
		"ubuntu-20.04":  "ubuntu:20.04",
		"debian-latest": "debian:latest",
		"alpine-latest": "alpine:latest",
	}

	if image, ok := imageMap[runsOn]; ok {
		return image
	}

	// Default
	return "ubuntu:22.04"
}

// RunStep implements Runner interface
func (r *PodmanRunner) RunStep(step *types.Step, env map[string]string, workdir string) error {
	// Steps are executed as part of the job script
	return nil
}
