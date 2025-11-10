package runners

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/bindings/pods"
	"github.com/containers/podman/v5/pkg/bindings/system"
	"github.com/containers/podman/v5/pkg/domain/entities"
	"github.com/containers/podman/v5/pkg/specgen"
	"github.com/containers/podman/v5/libpod/define"
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/pkg/types"
)

// PodmanRunner executes jobs using Podman native Go SDK
type PodmanRunner struct {
	config     *config.RunnerConfig
	conn       context.Context
	containers []string
	pods       []string
	formatter  *OutputFormatter
	mu         sync.Mutex
}

// NewPodmanRunner creates a new Podman runner using native Go bindings
func NewPodmanRunner(cfg *config.RunnerConfig) (*PodmanRunner, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	// Create connection to Podman
	// This will use the default socket path for the current user
	// Unix socket: /run/user/<uid>/podman/podman.sock (rootless)
	// or /run/podman/podman.sock (root)
	sockPath := os.Getenv("PODMAN_SOCKET")
	if sockPath == "" {
		// Let the library determine the default socket
		sockPath = ""
	}

	conn, err := bindings.NewConnection(context.Background(), sockPath)
	if err != nil {
		// Try alternative connection methods
		conn, err = bindings.NewConnectionWithIdentity(context.Background(), sockPath, "", true)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to Podman: %w", err)
		}
	}

	// Test connection by getting version
	info, err := system.Info(conn, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Podman: %w", err)
	}

	formatter := NewOutputFormatter(cfg.Verbose)

	if cfg.Verbose {
		formatter.PrintDebug(fmt.Sprintf("Connected to Podman %s", info.Version.Version))
		formatter.PrintDebug(fmt.Sprintf("API Version: %s", info.Version.APIVersion))
		formatter.PrintDebug(fmt.Sprintf("OS: %s/%s", info.Host.OS, info.Host.Arch))
	}

	return &PodmanRunner{
		config:     cfg,
		conn:       conn,
		containers: []string{},
		pods:       []string{},
		formatter:  formatter,
	}, nil
}

// RunJob executes a job using Podman API
func (r *PodmanRunner) RunJob(job *types.Job, workdir string) error {
	ctx := context.Background()
	startTime := time.Now()

	imageName := r.getImageName(job)

	// Print job header
	r.formatter.PrintHeader(job.Name, workdir, fmt.Sprintf("podman (%s)", imageName))

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
	imageExists, err := r.imageExists(ctx, imageName)
	if err != nil {
		r.formatter.PrintWarning(fmt.Sprintf("Failed to check image: %v", err))
		imageExists = false
	}

	// Pull image if needed
	if r.config.PullImages || !imageExists {
		progress := r.formatter.NewProgress(fmt.Sprintf("Pulling image %s", imageName))
		if err := r.pullImage(ctx, imageName); err != nil {
			progress.Complete(false)
			return fmt.Errorf("failed to pull image: %w", err)
		}
		progress.Complete(true)
	}

	// Create pod if services are needed
	var podID string
	if len(job.Services) > 0 {
		r.formatter.PrintSection("Setting up services")
		podID, err = r.createPod(ctx, job)
		if err != nil {
			return fmt.Errorf("failed to create pod: %w", err)
		}

		r.mu.Lock()
		r.pods = append(r.pods, podID)
		r.mu.Unlock()

		// Start services in pod
		if err := r.startServices(ctx, job, podID); err != nil {
			return fmt.Errorf("failed to start services: %w", err)
		}
	}

	// Create and run container
	r.formatter.PrintInfo("Creating container")
	containerID, err := r.createContainer(ctx, job, imageName, workdir, podID)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	r.mu.Lock()
	r.containers = append(r.containers, containerID)
	r.mu.Unlock()

	// Start container
	r.formatter.PrintInfo("Starting container")
	if err := r.startContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	// Attach to container for logs
	r.formatter.PrintSection("Container Output")
	attachErr := make(chan error, 1)
	go func() {
		attachErr <- r.attachToContainer(ctx, containerID)
	}()

	// Wait for container to finish
	exitCode, err := r.waitContainer(ctx, containerID)
	if err != nil {
		summary.Success = false
		summary.Errors = append(summary.Errors, fmt.Sprintf("Container wait error: %v", err))
		return err
	}

	// Check attach errors
	select {
	case err := <-attachErr:
		if err != nil {
			r.formatter.PrintWarning(fmt.Sprintf("Attach warning: %v", err))
		}
	default:
	}

	if exitCode != 0 {
		summary.Success = false
		summary.Errors = append(summary.Errors, fmt.Sprintf("Container exited with status %d", exitCode))
		if !job.AllowFailure {
			return fmt.Errorf("container exited with status %d", exitCode)
		}
	}

	summary.CompletedSteps = len(job.Steps)

	// Print job summary
	summary.Duration = time.Since(startTime)
	if r.config.Verbose {
		r.formatter.PrintJobSummary(summary)
	} else {
		r.formatter.PrintJobComplete(job.Name, summary.Duration, summary.Success)
	}

	return nil
}

// RunStep implements the Runner interface
func (r *PodmanRunner) RunStep(step *types.Step, env map[string]string, workdir string) error {
	// Steps are executed as part of the job script in container
	return nil
}

// imageExists checks if an image exists locally using API
func (r *PodmanRunner) imageExists(ctx context.Context, imageName string) (bool, error) {
	exists, err := images.Exists(r.conn, imageName, nil)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// pullImage pulls a container image using Podman API
func (r *PodmanRunner) pullImage(ctx context.Context, imageName string) error {
	pullOptions := &images.PullOptions{}

	// Set quiet based on verbose flag
	quiet := !r.config.Verbose
	pullOptions.Quiet = &quiet

	// Pull the image
	pullReports, err := images.Pull(r.conn, imageName, pullOptions)
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageName, err)
	}

	// Read pull output
	if r.config.Verbose && len(pullReports) > 0 {
		for _, report := range pullReports {
			// The report is a string in v5
			if report != "" {
				r.formatter.PrintDebug(report)
			}
		}
	}

	return nil
}

// createPod creates a pod for grouping containers
func (r *PodmanRunner) createPod(ctx context.Context, job *types.Job) (string, error) {
	podName := fmt.Sprintf("git-ci-%s-%d",
		strings.ReplaceAll(strings.ToLower(job.Name), " ", "-"),
		time.Now().Unix())

	podSpec := specgen.NewPodSpecGenerator()
	podSpec.Name = podName
	podSpec.Labels = map[string]string{
		"git-ci":  "true",
		"job":     job.Name,
		"created": time.Now().Format(time.RFC3339),
	}

	// Network configuration - check if field exists in config
	// You may need to add Network field to RunnerConfig if it doesn't exist

	// Create the pod
	podCreateResponse, err := pods.CreatePodFromSpec(r.conn, &entities.PodSpec{PodSpecGen: *podSpec})
	if err != nil {
		return "", fmt.Errorf("failed to create pod: %w", err)
	}

	r.formatter.PrintDebug(fmt.Sprintf("Created pod: %s", podCreateResponse.Id[:12]))
	return podCreateResponse.Id, nil
}

// createContainer creates a new container using Podman API
func (r *PodmanRunner) createContainer(ctx context.Context, job *types.Job, imageName, workdir, podID string) (string, error) {
	// Build script from steps
	script := r.buildJobScript(job)

	// Create a temporary script file
	scriptFile := fmt.Sprintf("/tmp/git-ci-script-%d.sh", time.Now().Unix())
	if err := os.WriteFile(scriptFile, []byte(script), 0755); err != nil {
		return "", fmt.Errorf("failed to write script file: %w", err)
	}
	defer os.Remove(scriptFile)

	// Create container spec
	s := specgen.NewSpecGenerator(imageName, false)

	// Basic configuration
	s.Name = fmt.Sprintf("git-ci-%s-%d",
		strings.ReplaceAll(strings.ToLower(job.Name), " ", "-"),
		time.Now().Unix())

	// Labels
	s.Labels = map[string]string{
		"git-ci":  "true",
		"job":     job.Name,
		"created": time.Now().Format(time.RFC3339),
	}

	// Command and entrypoint
	s.Entrypoint = []string{"/bin/sh"}
	s.Command = []string{"/tmp/script.sh"}

	// Working directory
	s.WorkDir = "/workspace"

	// Environment variables
	s.Env = r.buildEnvironment(job)

	// Mounts
	s.Mounts = []specs.Mount{
		{
			Type:        "bind",
			Source:      workdir,
			Destination: "/workspace",
			Options:     []string{"rw", "z"}, // z for SELinux relabeling
		},
		{
			Type:        "bind",
			Source:      scriptFile,
			Destination: "/tmp/script.sh",
			Options:     []string{"ro", "z"},
		},
	}

	// Add volumes from job container config
	if job.Container != nil {
		for _, vol := range job.Container.Volumes {
			parts := strings.Split(vol, ":")
			if len(parts) >= 2 {
				s.Mounts = append(s.Mounts, specs.Mount{
					Type:        "bind",
					Source:      parts[0],
					Destination: parts[1],
					Options:     []string{"rw", "z"},
				})
			}
		}
	}

	// Resource limits - check if these fields exist in Container type
	// You may need to add Memory and CPUs fields to Container type if they don't exist

	// Add to pod if specified
	if podID != "" {
		s.Pod = podID
	}

	// Security options
	s.Privileged = false
	s.ReadOnlyFilesystem = false

	// Terminal
	s.Terminal = false

	// Create the container
	createResponse, err := containers.CreateWithSpec(r.conn, s, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	r.formatter.PrintDebug(fmt.Sprintf("Container created: %s", createResponse.ID[:12]))
	return createResponse.ID, nil
}

// startContainer starts a container using API
func (r *PodmanRunner) startContainer(ctx context.Context, containerID string) error {
	err := containers.Start(r.conn, containerID, nil)
	if err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}
	return nil
}

// startServices starts service containers in a pod
func (r *PodmanRunner) startServices(ctx context.Context, job *types.Job, podID string) error {
	for name, service := range job.Services {
		r.formatter.PrintInfo(fmt.Sprintf("Starting service: %s", name))

		// Create service container spec
		s := specgen.NewSpecGenerator(service.Image, false)
		s.Name = fmt.Sprintf("git-ci-svc-%s-%d", name, time.Now().Unix())
		s.Pod = podID

		// Environment variables
		env := make(map[string]string)
		for k, v := range service.Env {
			env[k] = v
		}
		s.Env = env

		// Command if specified
		if len(service.Command) > 0 {
			s.Command = service.Command
		}

		// Create and start service container
		createResponse, err := containers.CreateWithSpec(r.conn, s, nil)
		if err != nil {
			return fmt.Errorf("failed to create service %s: %w", name, err)
		}

		// Track container for cleanup
		r.mu.Lock()
		r.containers = append(r.containers, createResponse.ID)
		r.mu.Unlock()

		// Start the service container
		if err := containers.Start(r.conn, createResponse.ID, nil); err != nil {
			return fmt.Errorf("failed to start service %s: %w", name, err)
		}

		r.formatter.PrintDebug(fmt.Sprintf("Service %s started: %s", name, createResponse.ID[:12]))
	}

	return nil
}

// attachToContainer attaches to container for logs
func (r *PodmanRunner) attachToContainer(ctx context.Context, containerID string) error {
	// Setup attach options
	attachOptions := new(containers.AttachOptions).
		WithStream(true).
		WithStdout(true).
		WithStderr(true).
		WithLogs(true)

	// Attach to container
	attachChan, err := containers.Attach(r.conn, containerID, attachOptions)
	if err != nil {
		return fmt.Errorf("failed to attach to container: %w", err)
	}

	// Process output
	for msg := range attachChan {
		switch msg.Stream {
		case 1: // stdout
			fmt.Print(string(msg.Body))
		case 2: // stderr
			fmt.Fprint(os.Stderr, string(msg.Body))
		}
	}

	return nil
}

// waitContainer waits for a container to finish
func (r *PodmanRunner) waitContainer(ctx context.Context, containerID string) (int32, error) {
	// Set up wait condition
	waitOptions := new(containers.WaitOptions).
		WithCondition([]define.ContainerStatus{define.ContainerStateExited})

	// Wait for container
	waitResponse, err := containers.Wait(r.conn, containerID, waitOptions)
	if err != nil {
		return -1, fmt.Errorf("failed to wait for container: %w", err)
	}

	// The response is an int32 exit code
	return int32(waitResponse), nil
}

// buildJobScript builds a shell script from job steps
func (r *PodmanRunner) buildJobScript(job *types.Job) string {
	var commands []string

	// Add shebang and shell options
	commands = append(commands, "#!/bin/sh")
	commands = append(commands, "set -e") // Exit on error

	if r.config.Verbose {
		commands = append(commands, "set -x") // Print commands
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
			commands = append(commands, fmt.Sprintf("echo 'Skipping action: %s (not supported in Podman runner)'", step.Name))
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
			commands[len(commands)-1] += " || true"
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

// buildEnvironment builds environment variables for the container
func (r *PodmanRunner) buildEnvironment(job *types.Job) map[string]string {
	env := make(map[string]string)

	// Set default environment
	env["CI"] = "true"
	env["GIT_CI"] = "true"
	env["PODMAN_RUNNER"] = "true"
	env["JOB_NAME"] = job.Name

	// Add job environment variables
	for k, v := range job.Environment {
		env[k] = v
	}

	// Add runner config environment variables
	for k, v := range r.config.Environment {
		env[k] = v
	}

	// Add container-specific environment variables
	if job.Container != nil {
		for k, v := range job.Container.Env {
			env[k] = v
		}
	}

	return env
}

// parseMemoryLimit parses memory limit string to bytes
func (r *PodmanRunner) parseMemoryLimit(memStr string) int64 {
	memStr = strings.ToLower(strings.TrimSpace(memStr))

	multiplier := int64(1)
	if strings.HasSuffix(memStr, "g") || strings.HasSuffix(memStr, "gb") {
		multiplier = 1024 * 1024 * 1024
		memStr = strings.TrimSuffix(strings.TrimSuffix(memStr, "gb"), "g")
	} else if strings.HasSuffix(memStr, "m") || strings.HasSuffix(memStr, "mb") {
		multiplier = 1024 * 1024
		memStr = strings.TrimSuffix(strings.TrimSuffix(memStr, "mb"), "m")
	} else if strings.HasSuffix(memStr, "k") || strings.HasSuffix(memStr, "kb") {
		multiplier = 1024
		memStr = strings.TrimSuffix(strings.TrimSuffix(memStr, "kb"), "k")
	}

	var value int64
	fmt.Sscanf(memStr, "%d", &value)
	return value * multiplier
}

// parseCPULimit parses CPU limit string to quota microseconds
func (r *PodmanRunner) parseCPULimit(cpuStr string) int64 {
	cpuStr = strings.TrimSpace(cpuStr)

	var cpuValue float64
	fmt.Sscanf(cpuStr, "%f", &cpuValue)

	// Convert to microseconds (period is 100000us = 100ms)
	// So 0.5 CPUs = 50000us quota per 100000us period
	return int64(cpuValue * 100000)
}

// dryRunJob performs a dry run of the job
func (r *PodmanRunner) dryRunJob(job *types.Job) error {
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

// Cleanup removes containers and cleans up resources
func (r *PodmanRunner) Cleanup() error {
	if len(r.containers) == 0 && len(r.pods) == 0 {
		return nil
	}

	ctx := context.Background()
	r.formatter.PrintSection("Cleaning up resources")

	var errors []string

	// Remove containers
	r.mu.Lock()
	containersToRemove := make([]string, len(r.containers))
	copy(containersToRemove, r.containers)
	r.mu.Unlock()

	for _, containerID := range containersToRemove {
		shortID := containerID
		if len(containerID) > 12 {
			shortID = containerID[:12]
		}

		// Force remove container (stops if running)
		force := true
		volumes := true
		removeOptions := new(containers.RemoveOptions).
			WithForce(&force).
			WithVolumes(&volumes)

		reports, err := containers.Remove(r.conn, containerID, removeOptions)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to remove container %s: %v", shortID, err))
			r.formatter.PrintWarning(fmt.Sprintf("Failed to remove container %s", shortID))
		} else {
			if len(reports) > 0 && reports[0].Err != nil {
				errors = append(errors, fmt.Sprintf("Failed to remove container %s: %v", shortID, reports[0].Err))
			} else {
				r.formatter.PrintInfo(fmt.Sprintf("Removed container %s", shortID))
			}
		}
	}

	// Remove pods
	r.mu.Lock()
	podsToRemove := make([]string, len(r.pods))
	copy(podsToRemove, r.pods)
	r.mu.Unlock()

	for _, podID := range podsToRemove {
		shortID := podID
		if len(podID) > 12 {
			shortID = podID[:12]
		}

		// Force remove pod
		force := true
		removeOptions := new(pods.RemoveOptions).WithForce(&force)

		response, err := pods.Remove(r.conn, podID, removeOptions)
		if err != nil {
			errors = append(errors, fmt.Sprintf("Failed to remove pod %s: %v", shortID, err))
			r.formatter.PrintWarning(fmt.Sprintf("Failed to remove pod %s", shortID))
		} else if response.Err != nil {
			errors = append(errors, fmt.Sprintf("Failed to remove pod %s: %v", shortID, response.Err))
		} else {
			r.formatter.PrintInfo(fmt.Sprintf("Removed pod %s", shortID))
		}
	}

	// Clear the lists
	r.mu.Lock()
	r.containers = []string{}
	r.pods = []string{}
	r.mu.Unlock()

	if len(errors) > 0 {
		return fmt.Errorf("cleanup completed with %d errors", len(errors))
	}

	return nil
}

// GetRunnerType returns the type of this runner
func (r *PodmanRunner) GetRunnerType() types.RunnerType {
	return types.RunnerTypePodman
}

// getImageName determines the image name from job configuration
func (r *PodmanRunner) getImageName(job *types.Job) string {
	// Use container image if specified
	if job.Container != nil && job.Container.Image != "" {
		return job.Container.Image
	}

	// Use job image if specified
	if job.Image != "" {
		return job.Image
	}

	// Map runs-on to container images
	runsOn := strings.ToLower(job.RunsOn)

	// Common mappings
	imageMap := map[string]string{
		"ubuntu-latest": "docker.io/library/ubuntu:latest",
		"ubuntu-24.04":  "docker.io/library/ubuntu:24.04",
		"ubuntu-22.04":  "docker.io/library/ubuntu:22.04",
		"ubuntu-20.04":  "docker.io/library/ubuntu:20.04",
		"debian-latest": "docker.io/library/debian:latest",
		"alpine-latest": "docker.io/library/alpine:latest",
		"fedora-latest": "registry.fedoraproject.org/fedora:latest",
		"centos-latest": "quay.io/centos/centos:stream9",
		"rockylinux":    "docker.io/library/rockylinux:9",
		"node-latest":   "docker.io/library/node:lts",
		"python-latest": "docker.io/library/python:3-slim",
		"golang-latest": "docker.io/library/golang:alpine",
		"rust-latest":   "docker.io/library/rust:alpine",
	}

	if image, ok := imageMap[runsOn]; ok {
		return image
	}

	// Pattern matching for partial matches
	switch {
	case strings.Contains(runsOn, "ubuntu"):
		return "docker.io/library/ubuntu:22.04"
	case strings.Contains(runsOn, "debian"):
		return "docker.io/library/debian:latest"
	case strings.Contains(runsOn, "alpine"):
		return "docker.io/library/alpine:latest"
	case strings.Contains(runsOn, "fedora"):
		return "registry.fedoraproject.org/fedora:latest"
	case strings.Contains(runsOn, "centos"):
		return "quay.io/centos/centos:stream9"
	case strings.Contains(runsOn, "rocky"):
		return "docker.io/library/rockylinux:9"
	case strings.Contains(runsOn, "node"):
		return "docker.io/library/node:lts-slim"
	case strings.Contains(runsOn, "python"):
		return "docker.io/library/python:3-slim"
	case strings.Contains(runsOn, "golang") || strings.Contains(runsOn, "go"):
		return "docker.io/library/golang:alpine"
	case strings.Contains(runsOn, "rust"):
		return "docker.io/library/rust:alpine"
	default:
		// Default to Ubuntu for compatibility
		return "docker.io/library/ubuntu:22.04"
	}
}
