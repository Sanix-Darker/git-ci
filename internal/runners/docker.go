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
			// Logs already streamed above, no need to repeat
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
	// Using images that closely match GitHub Actions runners:
	// - catthehacker/ubuntu:* - Used by 'act' tool, closest to GitHub Actions
	// - ghcr.io/catthehacker/ubuntu:* - Same images on GitHub Container Registry
	// - buildpack-deps:* - Good alternative with many build tools
	runsOn := strings.ToLower(job.RunsOn)

	// Primary mappings using catthehacker images (closest to GitHub Actions)
	// These images are used by the popular 'act' tool and include
	// most tools that GitHub Actions runners have
	imageMap := map[string]string{
		// Ubuntu versions - using catthehacker images (GitHub Actions compatible)
		"ubuntu-24.04":  "catthehacker/ubuntu:act-24.04",
		"ubuntu-22.04":  "catthehacker/ubuntu:act-22.04",
		"ubuntu-20.04":  "catthehacker/ubuntu:act-20.04",
		"ubuntu-latest": "catthehacker/ubuntu:act-22.04",

		// Fallback to buildpack-deps for systems without catthehacker images
		"debian-12":     "buildpack-deps:bookworm",
		"debian-11":     "buildpack-deps:bullseye",
		"debian-latest": "buildpack-deps:bookworm",

		// Alpine (lightweight)
		"alpine-3.19":   "alpine:3.19",
		"alpine-3.18":   "alpine:3.18",
		"alpine-latest": "alpine:latest",

		// Node.js
		"node-22": "node:22",
		"node-20": "node:20",
		"node-18": "node:18",
		"node-16": "node:16",

		// Python
		"python-3.13":   "python:3.13",
		"python-3.12":   "python:3.12",
		"python-3.11":   "python:3.11",
		"python-3.10":   "python:3.10",
		"python-latest": "python:3",

		// Go
		"golang-1.23":   "golang:1.23",
		"golang-1.22":   "golang:1.22",
		"golang-1.21":   "golang:1.21",
		"golang-1.20":   "golang:1.20",
		"golang-latest": "golang:latest",
		"go-1.23":       "golang:1.23",
		"go-1.22":       "golang:1.22",
		"go-latest":     "golang:latest",
	}

	if image, ok := imageMap[runsOn]; ok {
		return image
	}

	// Pattern matching for partial matches
	switch {
	case strings.Contains(runsOn, "ubuntu"):
		// Default to catthehacker ubuntu image for GitHub Actions compatibility
		return "catthehacker/ubuntu:act-22.04"
	case strings.Contains(runsOn, "debian"):
		return "buildpack-deps:bookworm"
	case strings.Contains(runsOn, "alpine"):
		return "alpine:latest"
	case strings.Contains(runsOn, "node"):
		return "node:lts"
	case strings.Contains(runsOn, "python"):
		return "python:3"
	case strings.Contains(runsOn, "golang") || strings.Contains(runsOn, "go"):
		return "golang:latest"
	case strings.Contains(runsOn, "rust"):
		return "rust:latest"
	case strings.Contains(runsOn, "ruby"):
		return "ruby:latest"
	case strings.Contains(runsOn, "java") || strings.Contains(runsOn, "jdk"):
		return "eclipse-temurin:21"
	default:
		// Default to catthehacker ubuntu for best GitHub Actions compatibility
		return "catthehacker/ubuntu:act-22.04"
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

	// Build environment
	env := r.buildEnvironment(job)

	// Log details in verbose mode
	if r.config.Verbose {
		r.formatter.PrintSection("Generated Script")
		fmt.Println(script)
		r.formatter.PrintSection("Environment Variables")
		for _, e := range env {
			fmt.Printf("  %s\n", e)
		}
		r.formatter.PrintSection("Volumes")
		fmt.Printf("  %s -> /workspace\n", workdir)
		if job.Container != nil {
			for _, vol := range job.Container.Volumes {
				fmt.Printf("  %s\n", vol)
			}
		}
		r.formatter.PrintSection("Container Configuration")
	}

	// Prepare container config
	containerConfig := &container.Config{
		Image:      imageName,
		Cmd:        []string{"/bin/bash", "-c", script},
		WorkingDir: "/workspace",
		Env:        env,
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

	// Add shebang and shell options - use bash for better compatibility
	commands = append(commands, "#!/bin/bash")
	commands = append(commands, "set -e") // Exit on error

	if r.config.Verbose {
		commands = append(commands, "set -x") // Print commands
	}

	commands = append(commands, "")
	commands = append(commands, "echo 'Setting up environment...'")

	// Detect if we're using Ubuntu/Debian and install basic tools
	imageName := r.getImageName(job)
	if strings.Contains(imageName, "ubuntu") || strings.Contains(imageName, "debian") || strings.Contains(imageName, "catthehacker") {
		commands = append(commands, "")
		commands = append(commands, "# Install basic tools if needed")
		commands = append(commands, "if ! command -v sudo >/dev/null 2>&1; then")
		commands = append(commands, "  apt-get update -qq >/dev/null 2>&1 || true")
		commands = append(commands, "  apt-get install -y -qq sudo >/dev/null 2>&1 || true")
		commands = append(commands, "fi")

		// Create a sudo wrapper that just executes commands directly when running as root
		commands = append(commands, "")
		commands = append(commands, "# Create sudo wrapper for root execution")
		commands = append(commands, "if [ \"$(id -u)\" = \"0\" ]; then")
		commands = append(commands, "  echo '#!/bin/bash' > /usr/local/bin/sudo-wrapper")
		commands = append(commands, "  echo 'exec \"$@\"' >> /usr/local/bin/sudo-wrapper")
		commands = append(commands, "  chmod +x /usr/local/bin/sudo-wrapper")
		commands = append(commands, "  if [ -f /usr/bin/sudo ]; then")
		commands = append(commands, "    mv /usr/bin/sudo /usr/bin/sudo.real 2>/dev/null || true")
		commands = append(commands, "  fi")
		commands = append(commands, "  ln -sf /usr/local/bin/sudo-wrapper /usr/bin/sudo 2>/dev/null || true")
		commands = append(commands, "fi")
	}

	commands = append(commands, "")

	totalSteps := len(job.Steps)
	stepNum := 0

	for _, step := range job.Steps {
		if step.Uses != "" {
			stepNum++
			commands = append(commands, fmt.Sprintf("echo ''"))
			commands = append(commands, fmt.Sprintf("echo '[%d/%d] %s'", stepNum, totalSteps, step.Name))
			commands = append(commands, fmt.Sprintf("echo '%s'", strings.Repeat("=", 60)))

			// Handle specific actions
			if strings.Contains(step.Uses, "actions/checkout") {
				commands = append(commands, "echo 'Skipping checkout action (code already mounted)'")
			} else if strings.Contains(step.Uses, "actions/setup-go") {
				// Extract Go version from step.With if available
				goVersion := "" // will auto-detect latest
				if v, ok := step.With["go-version"]; ok {
					// Clean up the version string (remove ${{ }} expressions)
					goVersion = r.cleanExpressions(v)
				}
				commands = append(commands, "echo 'Setting up Go...'")
				commands = append(commands, "if ! command -v go >/dev/null 2>&1; then")
				commands = append(commands, "  echo 'Installing Go...'")
				commands = append(commands, "  export DEBIAN_FRONTEND=noninteractive")
				commands = append(commands, "  apt-get update -qq")
				commands = append(commands, "  apt-get install -y -qq wget curl jq tar >/dev/null 2>&1")
				if goVersion != "" && !strings.Contains(goVersion, ".") {
					// If only major.minor provided (like "1.23"), we need to find latest patch
					commands = append(commands, fmt.Sprintf("  GO_WANTED=%s", goVersion))
					commands = append(commands, "  GO_VERSION=$(curl -s 'https://go.dev/dl/?mode=json' | jq -r '.[].version' | grep \"go${GO_WANTED}\" | head -1 | sed 's/go//')")
					commands = append(commands, "  if [ -z \"$GO_VERSION\" ]; then GO_VERSION=$(curl -s 'https://go.dev/dl/?mode=json' | jq -r '.[0].version' | sed 's/go//'); fi")
				} else if goVersion != "" {
					// Full version provided
					commands = append(commands, fmt.Sprintf("  GO_VERSION=%s", goVersion))
				} else {
					// Auto-detect latest stable
					commands = append(commands, "  GO_VERSION=$(curl -s 'https://go.dev/dl/?mode=json' | jq -r '.[0].version' | sed 's/go//')")
				}
				commands = append(commands, "  echo \"Installing Go version: $GO_VERSION\"")
				commands = append(commands, "  wget -q \"https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz\" -O /tmp/go.tar.gz || { echo 'Failed to download Go'; exit 1; }")
				commands = append(commands, "  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tar.gz")
				commands = append(commands, "  rm /tmp/go.tar.gz")
				commands = append(commands, "  export PATH=$PATH:/usr/local/go/bin")
				commands = append(commands, "  echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc")
				commands = append(commands, "fi")
				commands = append(commands, "export PATH=$PATH:/usr/local/go/bin")
				commands = append(commands, "go version")
			} else if strings.Contains(step.Uses, "actions/setup-node") {
				nodeVersion := "20" // default LTS
				if v, ok := step.With["node-version"]; ok {
					nodeVersion = r.cleanExpressions(v)
					if nodeVersion == "" {
						nodeVersion = "20"
					}
				}
				commands = append(commands, "echo 'Setting up Node.js...'")
				commands = append(commands, "if ! command -v node >/dev/null 2>&1; then")
				commands = append(commands, "  echo 'Installing Node.js...'")
				commands = append(commands, "  export DEBIAN_FRONTEND=noninteractive")
				commands = append(commands, "  apt-get update -qq")
				commands = append(commands, "  apt-get install -y -qq curl >/dev/null 2>&1")
				commands = append(commands, fmt.Sprintf("  curl -fsSL https://deb.nodesource.com/setup_%s.x | bash -", nodeVersion))
				commands = append(commands, "  apt-get install -y -qq nodejs >/dev/null 2>&1")
				commands = append(commands, "fi")
				commands = append(commands, "node --version")
				commands = append(commands, "npm --version")
			} else if strings.Contains(step.Uses, "actions/setup-python") {
				commands = append(commands, "echo 'Setting up Python...'")
				commands = append(commands, "if ! command -v python3 >/dev/null 2>&1; then")
				commands = append(commands, "  echo 'Installing Python...'")
				commands = append(commands, "  export DEBIAN_FRONTEND=noninteractive")
				commands = append(commands, "  apt-get update -qq")
				commands = append(commands, "  apt-get install -y -qq python3 python3-pip python3-venv >/dev/null 2>&1")
				commands = append(commands, "fi")
				commands = append(commands, "python3 --version")
				commands = append(commands, "pip3 --version || true")
			} else if strings.Contains(step.Uses, "actions/upload-artifact") || strings.Contains(step.Uses, "actions/download-artifact") {
				commands = append(commands, fmt.Sprintf("echo 'Artifact actions not supported locally: %s'", step.Uses))
			} else {
				commands = append(commands, fmt.Sprintf("echo 'Skipping action: %s (not supported in Docker runner)'", step.Uses))
			}
			commands = append(commands, fmt.Sprintf("echo '%s'", strings.Repeat("=", 60)))
			continue
		}

		if step.Run == "" {
			continue
		}

		stepNum++
		commands = append(commands, fmt.Sprintf("echo ''"))
		commands = append(commands, fmt.Sprintf("echo '[%d/%d] %s'", stepNum, totalSteps, step.Name))

		// Handle working directory
		if step.WorkingDir != "" {
			commands = append(commands, fmt.Sprintf("cd %s", step.WorkingDir))
		}

		// Add environment variables for this step
		for k, v := range step.Env {
			commands = append(commands, fmt.Sprintf("export %s='%s'", k, v))
		}

		// Process the command - handle GitHub Actions expressions
		runCommand := r.processGitHubExpressions(step.Run, job)
		commands = append(commands, runCommand)

		// Handle continue-on-error
		if step.ContinueOnErr {
			commands[len(commands)-1] += " || true"
		}

		commands = append(commands, fmt.Sprintf("echo '%s'", strings.Repeat("-", 60)))

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
		// GitHub Actions compatible environment variables
		"GITHUB_ACTIONS=false",
		"RUNNER_OS=Linux",
		"RUNNER_ARCH=X64",
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

// cleanExpressions removes or replaces ${{ }} expressions with defaults
func (r *DockerRunner) cleanExpressions(value string) string {
	// Remove ${{ }} wrapper and extract the content
	value = strings.TrimSpace(value)

	// If it's a GitHub expression like ${{ env.GO_VERSION }}, try to extract meaningful default
	if strings.HasPrefix(value, "${{") && strings.HasSuffix(value, "}}") {
		inner := strings.TrimPrefix(value, "${{")
		inner = strings.TrimSuffix(inner, "}}")
		inner = strings.TrimSpace(inner)

		// Handle common patterns
		if strings.HasPrefix(inner, "env.") {
			// Can't resolve env vars at parse time, return empty
			return ""
		}
		if strings.HasPrefix(inner, "matrix.") {
			// Can't resolve matrix at parse time, return empty
			return ""
		}
		if strings.HasPrefix(inner, "secrets.") {
			return ""
		}

		return inner
	}

	return value
}

// processGitHubExpressions processes ${{ }} expressions in run commands
func (r *DockerRunner) processGitHubExpressions(command string, job *types.Job) string {
	result := command

	// Helper to find and replace expressions with various spacing
	replaceExpr := func(s, pattern, replacement string) string {
		// Handle patterns like ${{ matrix.os }}, ${{matrix.os}}, ${{ matrix.os}}
		variations := []string{
			"${{ " + pattern + " }}",
			"${{" + pattern + "}}",
			"${{ " + pattern + "}}",
			"${{" + pattern + " }}",
		}
		for _, v := range variations {
			s = strings.ReplaceAll(s, v, replacement)
		}
		return s
	}

	// Replace matrix expressions with environment variable fallbacks
	result = replaceExpr(result, "matrix.os", "${MATRIX_OS:-linux}")
	result = replaceExpr(result, "matrix.arch", "${MATRIX_ARCH:-amd64}")

	// Replace runner expressions
	result = replaceExpr(result, "runner.os", "Linux")
	result = replaceExpr(result, "runner.arch", "X64")

	// Replace github expressions
	result = replaceExpr(result, "github.workspace", "/workspace")
	result = replaceExpr(result, "github.repository", "${GITHUB_REPOSITORY:-local/repo}")
	result = replaceExpr(result, "github.ref", "${GITHUB_REF:-refs/heads/main}")
	result = replaceExpr(result, "github.sha", "${GITHUB_SHA:-local}")

	// Replace ${{ env.* }} with shell variable syntax
	// Find all ${{ env.VAR }} patterns and replace with $VAR
	for {
		// Try to find env. pattern with various spacing
		start := -1
		for _, prefix := range []string{"${{ env.", "${{env."} {
			if idx := strings.Index(result, prefix); idx != -1 {
				start = idx
				break
			}
		}
		if start == -1 {
			break
		}

		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start + 2

		expr := result[start:end]
		// Extract variable name
		varName := expr
		varName = strings.TrimPrefix(varName, "${{ env.")
		varName = strings.TrimPrefix(varName, "${{env.")
		varName = strings.TrimSuffix(varName, " }}")
		varName = strings.TrimSuffix(varName, "}}")
		varName = strings.TrimSpace(varName)

		result = result[:start] + "${" + varName + "}" + result[end:]
	}

	// Handle any remaining ${{ }} expressions - warn and replace with empty or default
	for {
		start := strings.Index(result, "${{")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start + 2

		expr := result[start:end]
		r.formatter.PrintWarning(fmt.Sprintf("Unsupported expression: %s (removing)", expr))
		result = result[:start] + result[end:]
	}

	return result
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
