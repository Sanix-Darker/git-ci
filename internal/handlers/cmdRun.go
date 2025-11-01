package handlers

import (
	"fmt"
	"os"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/internal/runners"
	cli "github.com/urfave/cli/v2"
)

func CmdRun(c *cli.Context) error {
    jobName := c.String("job")
    useDocker := c.Bool("docker")
    filePath := c.String("file-path")

    pipeline, _ := parseInput(filePath)

    job, exists := pipeline.Jobs[jobName]
    if exists != true {
        return fmt.Errorf("job '%s' not found", jobName)
    }

    workdir, _ := os.Getwd()

    if useDocker {
        dockerRunner, err := runners.NewDockerRunner(config.DefaultConfig())
        if err != nil {
            return err
        }
        return dockerRunner.RunJob(job, workdir)
    }

    runner := &runners.BashRunner{}
    return runner.RunJob(job, workdir)
}
