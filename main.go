package main

import (
	"fmt"
	"log"
	"os"

	"github.com/sanix-darker/git-ci/internal/parsers"
	"github.com/urfave/cli/v2"
)


func main(){
    app := &cli.App{
        Name:  "git-ci",
        Usage: "Run CI workflows locally",
        Commands: []*cli.Command{
            {
                Name:  "ls",
                Usage: "List available jobs",
                Action: lsHandler,
            },
        },
    }

    if err := app.Run(os.Args); err != nil {
        log.Fatal(err)
    }
}

func lsHandler (c *cli.Context) error {
    parser := &parsers.GithubParser{}
    pipeline, err := parser.Parse(".github/workflows/ci.yml")
    if err != nil {
        return err
    }

    fmt.Printf("Pipeline: %s\n", pipeline.Name)
    fmt.Println("Jobs:")
    for name, job := range pipeline.Jobs {
        fmt.Printf("  - %s (runs on: %s)\n", name, job.RunsOn)
        for _, step := range job.Steps {
            fmt.Printf("    • %s\n", step.Name)
        }
    }
    return nil
}
