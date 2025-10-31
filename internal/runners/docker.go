package runners

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/sanix-darker/git-ci/pkg/types"
)

type DockerRunner struct {
	client *client.Client
}

func NewDockerRunner() (*DockerRunner, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerRunner{client: cli}, nil
}

func (r *DockerRunner) RunJob(job *types.Job, workdir string) error {
	ctx := context.Background()

	imageName := r.mapRunsOnToImage(job.RunsOn)

	fmt.Printf("\n Running job: %s\n", job.Name)
	fmt.Printf(" Using image: %s\n\n", imageName)

	// Pull image
	fmt.Printf("Pulling image %s...\n", imageName)
	reader, err := r.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	io.Copy(os.Stdout, reader)
	reader.Close()

	// Build script
	script := r.buildScript(job)

	// Create container
	config := &container.Config{
		Image:      imageName,
		Cmd:        []string{"/bin/bash", "-c", script},
		WorkingDir: "/workspace",
		Env:        r.buildEnv(job.Environment),
	}

	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:/workspace", workdir),
		},
	}

	resp, err := r.client.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
	if err != nil {
		return err
	}
	defer r.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

	// Start container
	if err := r.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return err
	}

	// Stream logs
	logs, err := r.client.ContainerLogs(ctx, resp.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return err
	}
	defer logs.Close()

	io.Copy(os.Stdout, logs)

	fmt.Println("\n Job completed successfully!")
	return nil
}

func (r *DockerRunner) mapRunsOnToImage(runsOn string) string {
	switch {
	case strings.Contains(runsOn, "ubuntu"):
		return "ubuntu:22.04"
	default:
		return "ubuntu:22.04"
	}
}

func (r *DockerRunner) buildScript(job *types.Job) string {
	var parts []string
	for i, step := range job.Steps {
		if step.Run == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("echo '▶️  [%d] %s'", i+1, step.Name))
		parts = append(parts, step.Run)
	}
	return strings.Join(parts, "\n")
}

func (r *DockerRunner) buildEnv(env map[string]string) []string {
	var result []string
	for k, v := range env {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}
	return result
}
