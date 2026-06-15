package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	cli "github.com/urfave/cli/v2"
)

// CmdClean handles the clean command
func CmdClean(c *cli.Context) error {
	all := c.Bool("all")
	containers := c.Bool("containers") || all
	images := c.Bool("images") || all
	// TODO: handle pod cleaning too, if needed
	cache := c.Bool("cache") || all
	force := c.Bool("force")

	if !containers && !images && !cache {
		fmt.Println("Nothing to clean. Use --all or specify what to clean.")
		return nil
	}

	fmt.Println("Cleaning up resources...")

	// Clean Docker resources if Docker is available
	if err := cleanDockerResources(containers, images, force); err != nil {
		printVerbose(c, "Warning: Docker cleanup failed: %v\n", err)
	}

	// Clean cache
	if cache {
		if err := cleanCache(); err != nil {
			return fmt.Errorf("failed to clean cache: %w", err)
		}
	}

	fmt.Println("✓ Cleanup completed")
	return nil
}

// cleanDockerResources cleans Docker containers and images
func cleanDockerResources(containers, images, force bool) error {
	// Create Docker client
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return fmt.Errorf("failed to create Docker client: %w", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Clean containers
	if containers {
		fmt.Println("  Cleaning containers...")
		if err := cleanContainers(ctx, cli, force); err != nil {
			return fmt.Errorf("failed to clean containers: %w", err)
		}
	}

	// Clean images
	if images {
		fmt.Println("  Cleaning images...")
		if err := cleanImages(ctx, cli, force); err != nil {
			return fmt.Errorf("failed to clean images: %w", err)
		}
	}

	return nil
}

// cleanContainers removes git-ci related containers
func cleanContainers(ctx context.Context, cli *client.Client, force bool) error {
	// List containers with git-ci label or name prefix
	filterArgs := filters.NewArgs()
	filterArgs.Add("label", "git-ci=true")

	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		// Also try to find by name prefix
		containers, err = cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			return err
		}

		// Filter by name prefix
		var gitCiContainers []container.Summary
		for _, c := range containers {
			for _, name := range c.Names {
				if strings.Contains(name, "git-ci") {
					gitCiContainers = append(gitCiContainers, c)
					break
				}
			}
		}
		containers = gitCiContainers
	}

	removedCount := 0
	for _, c := range containers {
		name := c.Names[0]
		if len(name) > 0 && name[0] == '/' {
			name = name[1:]
		}

		if !force {
			fmt.Printf("    Remove container %s? [y/N]: ", name)
			var response string
			_, _ = fmt.Scanln(&response)
			if response != "y" && response != "Y" {
				continue
			}
		}

		// Stop container if running
		if c.State == "running" {
			fmt.Printf("    Stopping container %s...\n", name)
			stopOptions := container.StopOptions{}
			if err := cli.ContainerStop(ctx, c.ID, stopOptions); err != nil {
				fmt.Printf("    Warning: failed to stop %s: %v\n", name, err)
			}
		}

		// Remove container
		fmt.Printf("    Removing container %s...\n", name)
		if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{
			Force:         force,
			RemoveVolumes: true,
		}); err != nil {
			fmt.Printf("    Warning: failed to remove %s: %v\n", name, err)
		} else {
			removedCount++
		}
	}

	fmt.Printf("    Removed %d container(s)\n", removedCount)
	return nil
}

// cleanImages removes git-ci related images
func cleanImages(ctx context.Context, cli *client.Client, force bool) error {
	// List images
	images, err := cli.ImageList(ctx, image.ListOptions{})
	if err != nil {
		return err
	}

	// Filter git-ci related images
	var gitCiImages []image.Summary
	for _, img := range images {
		for _, tag := range img.RepoTags {
			if strings.Contains(tag, "git-ci") {
				gitCiImages = append(gitCiImages, img)
				break
			}
		}
	}

	removedCount := 0
	for _, img := range gitCiImages {
		tag := "<none>"
		if len(img.RepoTags) > 0 {
			tag = img.RepoTags[0]
		}

		if !force {
			fmt.Printf("    Remove image %s? [y/N]: ", tag)
			var response string
			_, _ = fmt.Scanln(&response)
			if response != "y" && response != "Y" {
				continue
			}
		}

		fmt.Printf("    Removing image %s...\n", tag)
		_, err := cli.ImageRemove(ctx, img.ID, image.RemoveOptions{
			Force:         force,
			PruneChildren: true,
		})
		if err != nil {
			fmt.Printf("    Warning: failed to remove %s: %v\n", tag, err)
		} else {
			removedCount++
		}
	}

	fmt.Printf("    Removed %d image(s)\n", removedCount)

	// Prune dangling images if force
	if force {
		fmt.Println("    Pruning dangling images...")
		pruneReport, err := cli.ImagesPrune(ctx, filters.NewArgs())
		if err == nil && len(pruneReport.ImagesDeleted) > 0 {
			fmt.Printf("    Pruned %d dangling image(s)\n", len(pruneReport.ImagesDeleted))
		}
	}

	return nil
}

// cleanCache removes cached data
func cleanCache() error {
	fmt.Println("  Cleaning cache...")

	// Common cache directories
	cacheDirs := []string{
		".git-ci-cache",
		".git-ci",
		"tmp/git-ci",
	}

	// Also check home directory
	if home, err := os.UserHomeDir(); err == nil {
		cacheDirs = append(cacheDirs,
			filepath.Join(home, ".cache", "git-ci"),
			filepath.Join(home, ".git-ci"),
		)
	}

	removedCount := 0
	for _, dir := range cacheDirs {
		if _, err := os.Stat(dir); err == nil {
			fmt.Printf("    Removing %s...\n", dir)
			if err := os.RemoveAll(dir); err != nil {
				fmt.Printf("    Warning: failed to remove %s: %v\n", dir, err)
			} else {
				removedCount++
			}
		}
	}

	fmt.Printf("    Removed %d cache director(ies)\n", removedCount)
	return nil
}
