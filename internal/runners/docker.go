package runners

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

type DockerRunner struct {
	client     *client.Client
	config     *config.RunnerConfig
	containers []string
	formatter  *OutputFormatter
	mu         sync.Mutex
}

// NewDockerRunner creates a new Docker runner
func NewDockerRunner(cfg *config.RunnerConfig) (*DockerRunner, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	// Verify Docker is accessible
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pingResp, err := cli.Ping(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			return nil, fmt.Errorf("Docker daemon permission denied. Try: sudo usermod -aG docker $USER")
		}
		if strings.Contains(err.Error(), "cannot connect") {
			return nil, fmt.Errorf("Docker daemon is not running. Start Docker and try again")
		}
		return nil, fmt.Errorf("Docker daemon is not accessible: %w", err)
	}

	formatter := NewOutputFormatter(cfg.Verbose)

	// Show Docker version in verbose mode
	if cfg.Verbose {
		formatter.PrintDebug(fmt.Sprintf("Docker API version: %s", pingResp.APIVersion))
	}

	return &DockerRunner{
		client:     cli,
		config:     cfg,
		containers: []string{},
		formatter:  formatter,
	}, nil
}

func (r *DockerRunner) RunJob(job *types.Job, workdir string) error {
	ctx := context.Background()
	startTime := time.Now()

	imageName := r.getImageName(job)

	// Print job header
	r.formatter.PrintHeader(job.Name, workdir, fmt.Sprintf("docker (%s)", imageName))

	// Show dry run mode if enabled
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
			return err
		}
		progress.Complete(true)
	}

	// Print services if any
	if len(job.Services) > 0 {
		services := make(map[string]string)
		for name, svc := range job.Services {
			services[name] = svc.Image
		}
		r.formatter.PrintServices(services)
	}

	// Create and run container
	r.formatter.PrintInfo("Creating container")
	containerID, err := r.createContainer(ctx, job, imageName, workdir)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.containers = append(r.containers, containerID)
	r.mu.Unlock()

	// Start container
	r.formatter.PrintInfo("Starting container")
	if err := r.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Stream logs
	r.formatter.PrintSection("Container Output")
	if err := r.streamLogs(ctx, containerID); err != nil {
		summary.Success = false
		summary.Errors = append(summary.Errors, fmt.Sprintf("Log streaming error: %v", err))
	}

	// Wait for container to finish
	statusCh, errCh := r.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			summary.Success = false
			summary.Errors = append(summary.Errors, fmt.Sprintf("Container wait error: %v", err))
			return fmt.Errorf("container wait error: %w", err)
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			summary.Success = false
			summary.Errors = append(summary.Errors, fmt.Sprintf("Container exited with status %d", status.StatusCode))

			// Get last logs for debugging
			logs, _ := r.getContainerLogs(ctx, containerID, 20)
			if logs != "" {
				r.formatter.PrintSection("Last 20 lines of output")
				fmt.Print(logs)
			}

			return fmt.Errorf("container exited with status %d", status.StatusCode)
		}
		summary.CompletedSteps = len(job.Steps)
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

func (r *DockerRunner) RunStep(step *types.Step, env map[string]string, workdir string) error {
    // TODO:
	// Steps are executed as part of the job script in Docker
	// This could be enhanced to support individual step containers
    // for later
	return nil
}

func (r *DockerRunner) imageExists(ctx context.Context, imageName string) bool {
	images, err := r.client.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return false
	}

	for _, img := range images {
		for _, tag := range img.RepoTags {
			if tag == imageName {
				return true
			}
		}
	}
	return false
}

func (r *DockerRunner) getImageName(job *types.Job) string {
	// Use container image if specified
	if job.Container != nil && job.Container.Image != "" {
		return job.Container.Image
	}

	// Use job image if specified
	if job.Image != "" {
		return job.Image
	}

	// Map runs-on to Docker images
	runsOn := strings.ToLower(job.RunsOn)

	// Common mappings
	imageMap := map[string]string{
		"ubuntu-24.04": "ubuntu:24.04",
		"ubuntu-22.04": "ubuntu:22.04",
		"ubuntu-20.04": "ubuntu:20.04",
		"ubuntu-latest": "ubuntu:latest",
		"debian-12": "debian:12",
		"debian-11": "debian:11",
		"alpine-3.19": "alpine:3.19",
		"alpine-3.18": "alpine:3.18",
		"node-23": "node:23",
		"node-22": "node:22",
		"node-20": "node:20",
		"node-18": "node:18-slim",
		"python-3.14": "python:3.14-slim",
		"python-3.13": "python:3.13-slim",
		"python-3.12": "python:3.12-slim",
		"python-3.11": "python:3.11-slim",
		"golang-1.23": "golang:1.23-alpine",
		"golang-1.22": "golang:1.22-alpine",
		"golang-1.20": "golang:1.20-alpine",
	}

	if image, ok := imageMap[runsOn]; ok {
		return image
	}

	// Pattern matching for partial matches
	switch {
	case strings.Contains(runsOn, "ubuntu"):
		return "ubuntu:22.04"
	case strings.Contains(runsOn, "debian"):
		return "debian:latest"
	case strings.Contains(runsOn, "alpine"):
		return "alpine:latest"
	case strings.Contains(runsOn, "node"):
		return "node:lts-slim"
	case strings.Contains(runsOn, "python"):
		return "python:3-slim"
	case strings.Contains(runsOn, "golang") || strings.Contains(runsOn, "go"):
		return "golang:alpine"
	default:
		return "ubuntu:22.04"
	}
}

func (r *DockerRunner) pullImage(ctx context.Context, imageName string) error {
	reader, err := r.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Parse and display pull progress if verbose
	if r.config.Verbose {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			r.formatter.PrintDebug(scanner.Text())
		}
	} else {
		// Discard output
		_, _ = io.Copy(io.Discard, reader)
	}

	return nil
}

func (r *DockerRunner) createContainer(ctx context.Context, job *types.Job, imageName, workdir string) (string, error) {
	// Build script from steps
	script := r.buildJobScript(job)

	// Log script in debug mode
	if r.config.Verbose {
		r.formatter.PrintSection("Generated Script")
		fmt.Println(script)
		r.formatter.PrintSection("Container Configuration")
	}

	// Prepare container config
	containerConfig := &container.Config{
		Image:      imageName,
		Cmd:        []string{"/bin/sh", "-c", script},
		WorkingDir: "/workspace",
		Env:        r.buildEnvironment(job),
		Tty:        false,
	}

	// Prepare host config
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: workdir,
				Target: "/workspace",
			},
		},
		AutoRemove: false,
		Resources: container.Resources{
			Memory:     2 * 1024 * 1024 * 1024, // 2GB
			MemorySwap: 2 * 1024 * 1024 * 1024,
			CPUShares:  1024,
		},
	}

	// Add additional volumes if specified
	if job.Container != nil {
		for _, vol := range job.Container.Volumes {
			parts := strings.Split(vol, ":")
			if len(parts) >= 2 {
				hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
					Type:     mount.TypeBind,
					Source:   parts[0],
					Target:   parts[1],
					ReadOnly: len(parts) > 2 && parts[2] == "ro",
				})
			}
		}
	}

	containerName := fmt.Sprintf("git-ci-%s-%d",
		strings.ReplaceAll(strings.ToLower(job.Name), " ", "-"),
		time.Now().Unix())

	resp, err := r.client.ContainerCreate(
		ctx,
		containerConfig,
		hostConfig,
		nil,
		nil,
		containerName,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	r.formatter.PrintDebug(fmt.Sprintf("Container created: %s", resp.ID[:12]))
	return resp.ID, nil
}

func (r *DockerRunner) buildJobScript(job *types.Job) string {
	var commands []string

	// Add shebang and shell options
	commands = append(commands, "#!/bin/sh")
	commands = append(commands, "set -e")  // Exit on error

	if r.config.Verbose {
		commands = append(commands, "set -x")  // Print commands
	}

	commands = append(commands, "")
	commands = append(commands, "echo 'Setting up environment...'")
	commands = append(commands, "")

	totalSteps := len(job.Steps)
	stepNum := 0

	for _, step := range job.Steps {
		if step.Uses != "" {
			stepNum++
			commands = append(commands, fmt.Sprintf("echo ''"))
			commands = append(commands, fmt.Sprintf("echo '[%d/%d] %s'", stepNum, totalSteps, step.Name))
			commands = append(commands, fmt.Sprintf("echo '%s'", strings.Repeat("-", 60)))
			commands = append(commands, fmt.Sprintf("echo 'Skipping action: %s (not supported in Docker runner)'", step.Name))
			continue
		}

		if step.Run == "" {
			continue
		}

		stepNum++
		commands = append(commands, fmt.Sprintf("echo ''"))
		commands = append(commands, fmt.Sprintf("echo '[%d/%d] %s'", stepNum, totalSteps, step.Name))
		commands = append(commands, fmt.Sprintf("echo '%s'", strings.Repeat("-", 60)))

		// Handle working directory
		if step.WorkingDir != "" {
			commands = append(commands, fmt.Sprintf("cd %s", step.WorkingDir))
		}

		// Add environment variables for this step
		for k, v := range step.Env {
			commands = append(commands, fmt.Sprintf("export %s='%s'", k, v))
		}

		// Add the actual command
		commands = append(commands, step.Run)

		// Handle continue-on-error
		if step.ContinueOnErr {
			commands = append(commands, "|| true")
		}

		commands = append(commands, "echo 'Step completed'")

		// Reset directory if changed
		if step.WorkingDir != "" {
			commands = append(commands, "cd /workspace")
		}
	}

	commands = append(commands, "")
	commands = append(commands, "echo ''")
	commands = append(commands, "echo 'All steps completed successfully!'")

	return strings.Join(commands, "\n")
}

func (r *DockerRunner) buildEnvironment(job *types.Job) []string {
	env := []string{
		"CI=true",
		"GIT_CI=true",
		"DOCKER_RUNNER=true",
		fmt.Sprintf("JOB_NAME=%s", job.Name),
	}

	// Add job environment variables
	for k, v := range job.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Add runner config environment variables
	for k, v := range r.config.Environment {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Add container-specific environment variables
	if job.Container != nil {
		for k, v := range job.Container.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	return env
}

func (r *DockerRunner) streamLogs(ctx context.Context, containerID string) error {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	}

	reader, err := r.client.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return fmt.Errorf("failed to get container logs: %w", err)
	}
	defer reader.Close()

	// Use stdcopy to properly demultiplex stdout/stderr
	_, err = stdcopy.StdCopy(os.Stdout, os.Stderr, reader)
	if err != nil && err != io.EOF {
		return fmt.Errorf("error streaming logs: %w", err)
	}

	return nil
}

func (r *DockerRunner) getContainerLogs(ctx context.Context, containerID string, tailLines int) (string, error) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", tailLines),
	}

	reader, err := r.client.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	var output strings.Builder
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		output.WriteString(scanner.Text() + "\n")
	}

	return output.String(), nil
}

func (r *DockerRunner) dryRunJob(job *types.Job) error {
	r.formatter.PrintSection("Would execute the following steps")

	for i, step := range job.Steps {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(job.Steps), step.Name)

		if step.Uses != "" {
			r.formatter.PrintKeyValue("Action", step.Uses, 2)
			if len(step.With) > 0 {
				r.formatter.PrintSubSection("  Parameters:")
				for k, v := range step.With {
					r.formatter.PrintKeyValue(k, v, 4)
				}
			}
		}

		if step.Run != "" {
			r.formatter.PrintSubSection("  Command:")
			lines := strings.Split(step.Run, "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					r.formatter.PrintOutput(line, 4)
				}
			}
		}

		if len(step.Env) > 0 {
			r.formatter.PrintSubSection("  Environment:")
			for k, v := range step.Env {
				r.formatter.PrintKeyValue(k, v, 4)
			}
		}

		if step.WorkingDir != "" {
			r.formatter.PrintKeyValue("Working Dir", step.WorkingDir, 2)
		}
	}

	return nil
}

func (r *DockerRunner) Cleanup() error {
	if len(r.containers) == 0 {
		return nil
	}

	ctx := context.Background()
	r.formatter.PrintSection("Cleaning up containers")

	r.mu.Lock()
	containersToRemove := make([]string, len(r.containers))
	copy(containersToRemove, r.containers)
	r.mu.Unlock()

	var errors []string
	for _, containerID := range containersToRemove {
		shortID := containerID[:12]

		// Stop container first
		_ = r.client.ContainerStop(ctx, containerID, container.StopOptions{})

		// Remove container
		err := r.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to remove %s: %v", shortID, err))
			r.formatter.PrintWarning(fmt.Sprintf("Failed to remove container %s", shortID))
		} else {
			r.formatter.PrintInfo(fmt.Sprintf("Removed container %s", shortID))
		}
	}

	// Clear the container list
	r.mu.Lock()
	r.containers = []string{}
	r.mu.Unlock()

	if len(errors) > 0 {
		return fmt.Errorf("cleanup completed with %d errors", len(errors))
	}

	return nil
}

// GetRunnerType returns the type of this runner
func (r *DockerRunner) GetRunnerType() types.RunnerType {
	return types.RunnerTypeDocker
}
