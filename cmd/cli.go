package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/sanix-darker/git-ci/internal/handlers"
	cli "github.com/urfave/cli/v2"
)

// Version information (set by build flags)
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
	Branch    = "unknown"
)

func main() {
	app := &cli.App{
		Name:     "git-ci",
		Usage:    "Run CI/CD pipelines locally",
		Version:  formatVersion(),
		Compiled: parseTime(BuildTime),
		Authors: []*cli.Author{
			{
				Name:  "Sanix Darker",
				Email: "s4nixd@gmail.com",
			},
		},
		Copyright:            "Copyright (c) 2025 Sanix Darker",
		EnableBashCompletion: true,
		Before:               beforeAction,
		Flags:                globalFlags(),
		Commands:             commands(),
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func globalFlags() []cli.Flag {
	return []cli.Flag{
		//&cli.BoolFlag{
		//	Name:    "verbose",
		//	Aliases: []string{"v"},
		//	Usage:   "Enable verbose output",
		//	EnvVars: []string{"GIT_CI_VERBOSE"},
		//},
		&cli.BoolFlag{
			Name:    "debug",
			Usage:   "Enable debug mode",
			EnvVars: []string{"GIT_CI_DEBUG"},
		},
		&cli.BoolFlag{
			Name:    "quiet",
			Aliases: []string{"q"},
			Usage:   "Suppress output",
			EnvVars: []string{"GIT_CI_QUIET"},
		},
		&cli.StringFlag{
			Name:    "config",
			Aliases: []string{"c"},
			Usage:   "Config file path",
			EnvVars: []string{"GIT_CI_CONFIG"},
		},
		&cli.StringFlag{
			Name:    "workdir",
			Aliases: []string{"w"},
			Usage:   "Working directory",
			EnvVars: []string{"GIT_CI_WORKDIR"},
			Value:   ".",
		},
	}
}

func commands() []*cli.Command {
	return []*cli.Command{
		{
			Name:    "list",
			Aliases: []string{"ls"},
			Usage:   "List jobs and pipelines",
			Action:  handlers.CmdList,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "file",
					Aliases: []string{"f"},
					Usage:   "Pipeline file path",
					EnvVars: []string{"GIT_CI_FILE"},
				},
				&cli.StringFlag{
					Name:  "format",
					Usage: "Output format (tree, json, yaml)",
					Value: "tree",
				},
			},
		},
		{
			Name:    "run",
			Aliases: []string{"r", "exec"},
			Usage:   "Run jobs or pipelines",
			Action:  handlers.CmdRun,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "file",
					Aliases: []string{"f"},
					Usage:   "Pipeline file path",
					EnvVars: []string{"GIT_CI_FILE"},
				},
				&cli.StringFlag{
					Name:    "job",
					Aliases: []string{"j"},
					Usage:   "Job name to run",
					EnvVars: []string{"GIT_CI_JOB"},
				},
				&cli.StringFlag{
					Name:    "stage",
					Aliases: []string{"s"},
					Usage:   "Stage name to run",
					EnvVars: []string{"GIT_CI_STAGE"},
				},
				&cli.StringSliceFlag{
					Name:    "only",
					Usage:   "Run only these jobs",
					EnvVars: []string{"GIT_CI_ONLY"},
				},
				&cli.StringSliceFlag{
					Name:    "except",
					Usage:   "Run all jobs except these",
					EnvVars: []string{"GIT_CI_EXCEPT"},
				},
				&cli.BoolFlag{
					Name:    "docker",
					Aliases: []string{"d"},
					Usage:   "Use Docker runner",
					EnvVars: []string{"GIT_CI_DOCKER"},
				},
				&cli.BoolFlag{
					Name:    "podman",
					Usage:   "Use Podman runner",
					EnvVars: []string{"GIT_CI_PODMAN"},
				},
				&cli.BoolFlag{
					Name:    "dry-run",
					Aliases: []string{"n"},
					Usage:   "Perform a dry run",
					EnvVars: []string{"GIT_CI_DRY_RUN"},
				},
				&cli.BoolFlag{
					Name:    "parallel",
					Aliases: []string{"p"},
					Usage:   "Run jobs in parallel",
					EnvVars: []string{"GIT_CI_PARALLEL"},
				},
				&cli.IntFlag{
					Name:    "max-parallel",
					Usage:   "Maximum parallel jobs",
					EnvVars: []string{"GIT_CI_MAX_PARALLEL"},
					Value:   runtime.NumCPU(),
				},
				&cli.BoolFlag{
					Name:    "continue-on-error",
					Usage:   "Continue running on error",
					EnvVars: []string{"GIT_CI_CONTINUE_ON_ERROR"},
				},
				&cli.IntFlag{
					Name:    "timeout",
					Aliases: []string{"t"},
					Usage:   "Job timeout in minutes",
					EnvVars: []string{"GIT_CI_TIMEOUT"},
					Value:   30,
				},
				&cli.StringSliceFlag{
					Name:    "env",
					Aliases: []string{"e"},
					Usage:   "Set environment variables (KEY=VALUE)",
					EnvVars: []string{"GIT_CI_ENV"},
				},
				&cli.StringFlag{
					Name:    "env-file",
					Usage:   "Environment file path",
					EnvVars: []string{"GIT_CI_ENV_FILE"},
				},
				&cli.BoolFlag{
					Name:    "pull",
					Usage:   "Pull docker images",
					EnvVars: []string{"GIT_CI_PULL"},
					Value:   true,
				},
				&cli.BoolFlag{
					Name:    "no-cache",
					Usage:   "Disable cache",
					EnvVars: []string{"GIT_CI_NO_CACHE"},
				},
				&cli.StringSliceFlag{
					Name:    "volume",
					Aliases: []string{"V"},
					Usage:   "Bind mount volumes",
				},
				&cli.StringFlag{
					Name:    "network",
					Usage:   "Docker network mode",
					EnvVars: []string{"GIT_CI_NETWORK"},
					Value:   "bridge",
				},
			},
		},
		{
			Name:    "validate",
			Aliases: []string{"check", "v"},
			Usage:   "Validate pipeline syntax",
			Action:  handlers.CmdValidate,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "file",
					Aliases: []string{"f"},
					Usage:   "Pipeline file path",
					EnvVars: []string{"GIT_CI_FILE"},
				},
				&cli.StringFlag{
					Name:    "provider",
					Aliases: []string{"p"},
					Usage:   "CI provider (github, gitlab, auto)",
					Value:   "auto",
				},
				&cli.BoolFlag{
					Name:  "strict",
					Usage: "Enable strict validation",
				},
			},
		},
		{
			Name:   "init",
			Usage:  "Initialize a new pipeline",
			Action: handlers.CmdInit,
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:    "provider",
					Aliases: []string{"p"},
					Usage:   "CI provider (github, gitlab)",
					Value:   "github",
				},
				&cli.StringFlag{
					Name:    "template",
					Aliases: []string{"t"},
					Usage:   "Template (basic, node, python, go, docker)",
					Value:   "basic",
				},
				&cli.StringFlag{
					Name:    "output",
					Aliases: []string{"o"},
					Usage:   "Output file path",
				},
				&cli.BoolFlag{
					Name:  "force",
					Usage: "Overwrite existing file",
				},
			},
		},
		{
			Name:   "clean",
			Usage:  "Clean up resources",
			Action: handlers.CmdClean,
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:    "all",
					Aliases: []string{"a"},
					Usage:   "Clean all resources",
				},
				&cli.BoolFlag{
					Name:  "containers",
					Usage: "Clean containers only",
				},
				&cli.BoolFlag{
					Name:  "images",
					Usage: "Clean images only",
				},
				&cli.BoolFlag{
					Name:  "cache",
					Usage: "Clean cache only",
				},
				&cli.BoolFlag{
					Name:    "force",
					Aliases: []string{"f"},
					Usage:   "Force cleanup",
				},
			},
		},
		{
			Name:  "env",
			Usage: "Manage environment variables",
			Subcommands: []*cli.Command{
				{
					Name:   "list",
					Usage:  "List environment variables",
					Action: handlers.CmdEnvList,
				},
				{
					Name:      "set",
					Usage:     "Set environment variables",
					Action:    handlers.CmdEnvSet,
					ArgsUsage: "KEY=VALUE [KEY=VALUE...]",
					Flags: []cli.Flag{
						&cli.BoolFlag{
							Name:  "save",
							Usage: "Save to .env file",
						},
						&cli.StringFlag{
							Name:  "file",
							Usage: "Environment file path",
							Value: ".env",
						},
					},
				},
				{
					Name:   "load",
					Usage:  "Load environment from file",
					Action: handlers.CmdEnvLoad,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:    "file",
							Aliases: []string{"f"},
							Usage:   "Environment file path",
							Value:   ".env",
						},
					},
				},
			},
		},
		{
			Name:  "config",
			Usage: "Manage configuration",
			Subcommands: []*cli.Command{
				{
					Name:   "show",
					Usage:  "Show current configuration",
					Action: handlers.CmdConfigShow,
				},
				{
					Name:   "init",
					Usage:  "Initialize configuration file",
					Action: handlers.CmdConfigInit,
					Flags: []cli.Flag{
						&cli.StringFlag{
							Name:    "output",
							Aliases: []string{"o"},
							Usage:   "Output file path",
							Value:   ".git-ci.yml",
						},
						&cli.BoolFlag{
							Name:  "force",
							Usage: "Overwrite existing file",
						},
					},
				},
			},
		},
	}
}

func beforeAction(c *cli.Context) error {
	// Setup environment
	setupEnvironment()

	// Load configuration if specified
	if _, err := handlers.LoadConfigWithDefaults(c); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	return nil
}

func setupEnvironment() {
	// Set default environment variables
	defaults := map[string]string{
		"GIT_CI":         "true",
		"CI":             "true",
		"GIT_CI_VERSION": Version,
	}

	for key, value := range defaults {
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
}

func formatVersion() string {
	v := Version
	if Commit != "unknown" && len(Commit) > 7 {
		v += fmt.Sprintf(" (%s)", Commit[:7])
	}
	if Branch != "unknown" && Branch != "main" && Branch != "master" {
		v += fmt.Sprintf(" [%s]", Branch)
	}
	return v
}

func parseTime(timeStr string) time.Time {
	var t time.Time
	if timeStr != "unknown" {
		formats := []string{
			"20060102.150405",
			time.RFC3339,
			"2006-01-02T15:04:05Z",
		}
		for _, format := range formats {
			if parsed, err := time.Parse(format, timeStr); err == nil {
				t = parsed
				break
			}
		}
	}
	return t
}
