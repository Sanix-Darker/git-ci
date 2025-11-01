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
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

type DockerRunner struct {
	client     *client.Client
	config     *config.RunnerConfig
	containers []string     // Track containers for cleanup
	networks   []string     // Track networks for cleanup
	mu         sync.Mutex   // Thread safety for container/network lists
}

// NewDockerRunner creates a new Docker runner with error handling
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

	// This is to check if Docker is accessible or not (with a 5s timeout)
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

	// Log Docker version for debugging
	if cfg.Verbose {
		fmt.Printf("🐳 Docker API version: %s\n", pingResp.APIVersion)
	}

	return &DockerRunner{
		client:     cli,
		config:     cfg,
		containers: []string{},
		networks:   []string{},
	}, nil
}

func (r *DockerRunner) RunJob(job *types.Job, workdir string) error {
	ctx := context.Background()
	startTime := time.Now()

	imageName := r.getImageName(job)

	fmt.Printf("\n🐳 Running job: %s\n", job.Name)
	fmt.Printf("📁 Working directory: %s\n", workdir)
	fmt.Printf("🖼️  Docker image: %s\n", imageName)

	if r.config.DryRun {
		fmt.Println("🔍 DRY RUN MODE - Commands will be displayed but not executed")
		return r.dryRunJob(job)
	}

	fmt.Println(strings.Repeat("─", 60))

	// Check if image exists locally first
	imageExists := r.imageExists(ctx, imageName)

	// Pull image if requested or if it doesn't exist
	if r.config.PullImages || !imageExists {
		if err := r.pullImage(ctx, imageName); err != nil {
			return err
		}
	}

	// Create network for job if services are defined
	var networkID string
	if len(job.Services) > 0 {
		var err error
		networkID, err = r.createNetwork(ctx, job.Name)
		if err != nil {
			return fmt.Errorf("failed to create network: %w", err)
		}
	}

	// Start service containers if defined
	if err := r.startServices(ctx, job, networkID); err != nil {
		return fmt.Errorf("failed to start services: %w", err)
	}

	// Create and run main container
	containerID, err := r.createContainer(ctx, job, imageName, workdir, networkID)
	if err != nil {
		return err
	}

    // not sure yet about these....
	r.mu.Lock()
	r.containers = append(r.containers, containerID)
	r.mu.Unlock()

	// Start container with timeout support
	if err := r.startContainerWithTimeout(ctx, containerID, job.TimeoutMin); err != nil {
		return err
	}

	// Stream logs with better error handling on the stdout/stderr
	if err := r.streamLogs(ctx, containerID); err != nil {
		return err
	}

	// Wait for container to finish with timeout
	if err := r.waitForContainer(ctx, containerID, job.TimeoutMin); err != nil {
		return err
	}

	duration := time.Since(startTime)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("✨ Job completed successfully in %s!\n", duration.Round(time.Second))

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
    // FIXME: for most cases this fallback is not working, i should find a better other way later
	if job.Container != nil && job.Container.Image != "" {
		return job.Container.Image
	}

	// Enhanced mapping with more options
	runsOn := strings.ToLower(job.RunsOn)

	// Check for exact matches first
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

	// Fallback to pattern matching
	switch {
	case strings.Contains(runsOn, "ubuntu"):
		return "ubuntu:22.04"
	case strings.Contains(runsOn, "debian"):
		return "debian:latest"
	case strings.Contains(runsOn, "alpine"):
		return "alpine:latest"
	case strings.Contains(runsOn, "centos"):
		return "quay.io/centos/centos:stream9"
	case strings.Contains(runsOn, "fedora"):
		return "fedora:latest"
	case strings.Contains(runsOn, "rocky"):
		return "rockylinux:9"
	case strings.Contains(runsOn, "node"):
		return "node:lts-slim"
	case strings.Contains(runsOn, "python"):
		return "python:3-slim"
	case strings.Contains(runsOn, "golang") || strings.Contains(runsOn, "go"):
		return "golang:alpine"
	case strings.Contains(runsOn, "rust"):
		return "rust:slim"
	case strings.Contains(runsOn, "java"):
		return "eclipse-temurin:17-jre"
	default:
		// Default to Ubuntu LTS
		return "ubuntu:22.04"
	}
}

func (r *DockerRunner) pullImage(ctx context.Context, imageName string) error {
	fmt.Printf("📥 Pulling image %s...\n", imageName)

	reader, err := r.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Parse and display pull progress if verbose
	if r.config.Verbose {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			fmt.Printf("   %s\n", scanner.Text())
		}
	} else {
		// Discard output for cleaner display
		_, _ = io.Copy(io.Discard, reader)
	}

	fmt.Println("   ✅ Image pulled successfully")
	return nil
}

func (r *DockerRunner) createNetwork(ctx context.Context, jobName string) (string, error) {
	networkName := fmt.Sprintf("git-ci-%s-%d", jobName, time.Now().Unix())

	resp, err := r.client.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"git-ci": "true",
			"job":    jobName,
		},
	})

	if err != nil {
		return "", fmt.Errorf("failed to create network: %w", err)
	}

	r.mu.Lock()
	r.networks = append(r.networks, resp.ID)
	r.mu.Unlock()

	return resp.ID, nil
}

func (r *DockerRunner) startServices(ctx context.Context, job *types.Job, networkID string) error {
	for serviceName, service := range job.Services {
		fmt.Printf("🔧 Starting service: %s (%s)\n", serviceName, service.Image)

		// Create service container
		containerConfig := &container.Config{
			Image: service.Image,
			Env:   r.buildServiceEnvironment(service),
		}

		hostConfig := &container.HostConfig{}

		// Add to network if available
		if networkID != "" {
			hostConfig.NetworkMode = container.NetworkMode(networkID)
		}

		// Map ports if specified
		if len(service.Ports) > 0 {
            //TODO:
			// Parse and configure port bindings
			// Implementation depends on port format (e.g., "5432:5432")
		}

		resp, err := r.client.ContainerCreate(
			ctx,
			containerConfig,
			hostConfig,
			nil,
			nil,
			fmt.Sprintf("git-ci-service-%s-%d", serviceName, time.Now().Unix()),
		)

		if err != nil {
			return fmt.Errorf("failed to create service %s: %w", serviceName, err)
		}

		r.mu.Lock()
		r.containers = append(r.containers, resp.ID)
		r.mu.Unlock()

		// Start service container
		if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("failed to start service %s: %w", serviceName, err)
		}

		// Wait for health check if defined
		if service.HealthCheck != nil {
			// Implement health check waiting logic
			fmt.Printf("   ⏳ Waiting for service %s to be healthy...\n", serviceName)
			time.Sleep(2 * time.Second) // Simple delay for now
		}
	}

	return nil
}

func (r *DockerRunner) createContainer(ctx context.Context, job *types.Job, imageName, workdir, networkID string) (string, error) {
	// Build script from steps
	script := r.buildJobScript(job)

	// Save script to temporary file for better debugging
	if r.config.Verbose {
		fmt.Printf("📝 Generated script:\n%s\n", script)
	}

	// Prepare container config
	containerConfig := &container.Config{
		Image:      imageName,
		Cmd:        []string{"/bin/sh", "-c", script},
		WorkingDir: "/workspace",
		Env:        r.buildEnvironment(job),
		Tty:        false,
		Labels: map[string]string{
			"git-ci":  "true",
			"job":     job.Name,
			"workdir": workdir,
		},
	}

	// Prepare host config with enhanced options
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: workdir,
				Target: "/workspace",
			},
		},
		AutoRemove: false, // We'll clean up manually
		Resources: container.Resources{
			Memory:     2 * 1024 * 1024 * 1024, // 2GB default
			MemorySwap: 2 * 1024 * 1024 * 1024,
			CPUShares:  1024,
		},
	}

	// Add to network if available
	if networkID != "" {
		hostConfig.NetworkMode = container.NetworkMode(networkID)
	}

	// Add additional volumes if specified
	if job.Container != nil {
		// Parse container options
		if job.Container.Options != "" {
            // TODO:
			// Parse and apply Docker run options
			// This could include --privileged, --cap-add, etc.
		}

		// Add volumes
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

	containerName := fmt.Sprintf("git-ci-%s-%d", strings.ReplaceAll(job.Name, " ", "-"), time.Now().Unix())

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

	return resp.ID, nil
}

func (r *DockerRunner) startContainerWithTimeout(ctx context.Context, containerID string, timeoutMin int) error {
	// Set timeout if specified
	if timeoutMin > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)
		defer cancel()
	}

	if err := r.client.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	return nil
}

func (r *DockerRunner) waitForContainer(ctx context.Context, containerID string, timeoutMin int) error {
	// Set timeout if specified
	if timeoutMin > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMin)*time.Minute)
		defer cancel()
	}

	statusCh, errCh := r.client.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("container wait error: %w", err)
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			// Try to get logs for debugging
			logs, _ := r.getContainerLogs(ctx, containerID, 50)
			return fmt.Errorf("container exited with status %d\nLast 50 lines:\n%s", status.StatusCode, logs)
		}
	case <-ctx.Done():
		// Timeout reached
		return fmt.Errorf("job timeout exceeded (%d minutes)", timeoutMin)
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

func (r *DockerRunner) buildJobScript(job *types.Job) string {
	var commands []string

	// Add shebang and shell options
	commands = append(commands, "#!/bin/sh")
	commands = append(commands, "set -e")  // Exit on error

	if r.config.Verbose {
		commands = append(commands, "set -x")  // Print commands
	}

	commands = append(commands, "")

	// Add environment setup
	commands = append(commands, "# Environment setup")
	commands = append(commands, "echo '🔧 Setting up environment...'")
	commands = append(commands, "")

	for i, step := range job.Steps {
		if step.Uses != "" {
			commands = append(commands, fmt.Sprintf("echo '⏭️  [%d/%d] Skipping action: %s (not supported)'", i+1, len(job.Steps), step.Name))
			continue
		}

		if step.Run == "" {
			continue
		}

		// Add step header
		commands = append(commands, fmt.Sprintf("echo ''"))
		commands = append(commands, fmt.Sprintf("echo '▶️  [%d/%d] %s'", i+1, len(job.Steps), step.Name))
		commands = append(commands, fmt.Sprintf("echo '────────────────────────────────'"))

		// Handle working directory if specified
		if step.WorkingDir != "" {
			commands = append(commands, fmt.Sprintf("cd %s", step.WorkingDir))
		}

		// Add environment variables for this step
		for k, v := range step.Env {
			commands = append(commands, fmt.Sprintf("export %s='%s'", k, v))
		}

		// Handle different shells
		shell := step.Shell
		if shell == "" {
			shell = "sh"
		}

		// Add the actual command with proper shell
		switch shell {
		case "bash":
			commands = append(commands, fmt.Sprintf("bash -eo pipefail -c '%s'", escapeShellCommand(step.Run)))
		case "pwsh", "powershell":
			commands = append(commands, fmt.Sprintf("pwsh -Command '%s'", escapeShellCommand(step.Run)))
		default:
			commands = append(commands, step.Run)
		}

		// Handle continue-on-error
		if step.ContinueOnErr {
			commands = append(commands, "|| true")
		}

		commands = append(commands, fmt.Sprintf("echo '✅ Step completed'"))
	}

	commands = append(commands, "")
	commands = append(commands, "echo '🎉 All steps completed successfully!'")

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

func (r *DockerRunner) buildServiceEnvironment(service *types.Service) []string {
	var env []string
	for k, v := range service.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
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

	// Use stdcopy to properly demultiplex stdout/stderr from Docker
	_, err = stdcopy.StdCopy(os.Stdout, os.Stderr, reader)
	if err != nil && err != io.EOF {
		return fmt.Errorf("error streaming logs: %w", err)
	}

	return nil
}

func (r *DockerRunner) dryRunJob(job *types.Job) error { // Fixed: now returns error
	fmt.Println("\n📝 Would execute the following steps:")

	for i, step := range job.Steps {
		fmt.Printf("\n[%d/%d] %s\n", i+1, len(job.Steps), step.Name)

		if step.Uses != "" {
			fmt.Printf("   Action: %s\n", step.Uses)
			if len(step.With) > 0 {
				fmt.Println("   With:")
				for k, v := range step.With {
					fmt.Printf("     %s: %s\n", k, v)
				}
			}
		}

		if step.Run != "" {
			fmt.Printf("   Command: %s\n", truncateMultiline(step.Run, 3))
		}

		if len(step.Env) > 0 {
			fmt.Println("   Environment:")
			for k, v := range step.Env {
				fmt.Printf("     %s=%s\n", k, v)
			}
		}

		if step.WorkingDir != "" {
			fmt.Printf("   Working Dir: %s\n", step.WorkingDir)
		}
	}

	return nil // Fixed: now returns nil error
}

func (r *DockerRunner) Cleanup() error {
	if len(r.containers) == 0 && len(r.networks) == 0 {
		return nil
	}

	ctx := context.Background()
	fmt.Println("\n🧹 Cleaning up resources...")

	var errors []error

	// Clean up containers
	r.mu.Lock()
	containersToRemove := make([]string, len(r.containers))
	copy(containersToRemove, r.containers)
	r.mu.Unlock()

	for _, containerID := range containersToRemove {
		// Stop container first if it's running
		_ = r.client.ContainerStop(ctx, containerID, container.StopOptions{})

		// Force remove container
		err := r.client.ContainerRemove(ctx, containerID, container.RemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to remove container %s: %w", containerID[:12], err))
			fmt.Printf("   ⚠️  Failed to remove container %s: %v\n", containerID[:12], err)
		} else {
			fmt.Printf("   ✅ Removed container %s\n", containerID[:12])
		}
	}

	// Clean up networks
	r.mu.Lock()
	networksToRemove := make([]string, len(r.networks))
	copy(networksToRemove, r.networks)
	r.mu.Unlock()

	for _, networkID := range networksToRemove {
		err := r.client.NetworkRemove(ctx, networkID)
		if err != nil {
			errors = append(errors, fmt.Errorf("failed to remove network %s: %w", networkID[:12], err))
		} else {
			fmt.Printf("   ✅ Removed network %s\n", networkID[:12])
		}
	}

	// Clear the lists
	r.mu.Lock()
	r.containers = []string{}
	r.networks = []string{}
	r.mu.Unlock()

	if len(errors) > 0 {
		return fmt.Errorf("cleanup completed with %d errors", len(errors))
	}

	return nil
}
