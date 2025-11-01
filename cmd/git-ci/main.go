package main

import (
	"log"
	"os"

	"github.com/sanix-darker/git-ci/internal/handlers"
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
                Flags: []cli.Flag{
                    &cli.StringFlag{
                        Name: "file-path",
                        Aliases: []string{"f"},
                        Value: ".github/workflows/ci.yml",
                        Required: false,
                        Usage: "File pah of the pipeline to be ran on",
                    },
                },
                Action: handlers.CmdList,
            },
            {
                Name:  "run",
                Usage: "Run a specific job",
                Flags: []cli.Flag{
                    &cli.StringFlag{
                        Name: "job",
                        Aliases: []string{"j"},
                        Required: true,
                        Usage: "Job name to run",
                    },
                    &cli.BoolFlag{
                        Name: "docker",
                        Aliases: []string{"d"},
                        Value: false,
                        Usage: "Use Docker runner",
                    },
                    &cli.StringFlag{
                        Name: "file-path",
                        Aliases: []string{"f"},
                        Value: ".github/workflows/ci.yml",
                        Required: false,
                        Usage: "File pah of the pipeline to be ran on",
                    },
                },
                Action: handlers.CmdRun,
            },
        },
    }

    if err := app.Run(os.Args); err != nil {
        log.Fatal(err)
    }
}

