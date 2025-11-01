package handlers

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	cli "github.com/urfave/cli/v2"
)

// Tree drawing characters
const (
	TreeBranch = "├──"
	TreePipe   = "│  "
	TreeEnd    = "└──"
	TreeSpace  = "   "
)

func CmdList(c *cli.Context) error {
	workflowFile := c.String("file-path")

	// Parse input
	pipeline, err := parseInput(workflowFile)
	if err != nil {
		return fmt.Errorf("failed to parse workflow: %w", err)
	}

	// Display pipeline information
	fmt.Printf("\nPipeline: %s\n", pipeline.Name)

	if pipeline.Provider != "" {
		fmt.Printf("Provider: %s\n", pipeline.Provider)
	}

	if pipeline.Description != "" {
		fmt.Printf("Description: %s\n", pipeline.Description)
	}

	// Display stages if available
	if len(pipeline.Stages) > 0 {
		fmt.Printf("\nStages:\n")
		for i, stage := range pipeline.Stages {
			if i == len(pipeline.Stages)-1 {
				fmt.Printf("%s %s\n", TreeEnd, stage)
			} else {
				fmt.Printf("%s %s\n", TreeBranch, stage)
			}
		}
	}

	// Display triggers if available
	if len(pipeline.Triggers) > 0 {
		fmt.Printf("\nTriggers:\n")
		for i, trigger := range pipeline.Triggers {
			if i == len(pipeline.Triggers)-1 {
				fmt.Printf("%s %s\n", TreeEnd, trigger)
			} else {
				fmt.Printf("%s %s\n", TreeBranch, trigger)
			}
		}
	}

	// Display global environment variables
	if len(pipeline.Environment) > 0 {
		fmt.Printf("\nGlobal Environment:\n")
		envKeys := getSortedKeys(pipeline.Environment)
		for i, key := range envKeys {
			value := pipeline.Environment[key]
			if i == len(envKeys)-1 {
				fmt.Printf("%s %s=%s\n", TreeEnd, key, value)
			} else {
				fmt.Printf("%s %s=%s\n", TreeBranch, key, value)
			}
		}
	}

	// Display jobs
	fmt.Printf("\nJobs:\n")

	// Sort job names for consistent display
	jobNames := make([]string, 0, len(pipeline.Jobs))
	for name := range pipeline.Jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)

	// Display each job
	for idx, jobName := range jobNames {
		job := pipeline.Jobs[jobName]
		isLastJob := idx == len(jobNames)-1

		var jobPrefix, childPrefix string
		if isLastJob {
			jobPrefix = TreeEnd
			childPrefix = TreeSpace
		} else {
			jobPrefix = TreeBranch
			childPrefix = TreePipe
		}

		// Display job name and runner info
		fmt.Printf("%s %s\n", jobPrefix, jobName)

		// Display job details
		displayJobDetails(job, childPrefix)
	}

	// Display summary
	fmt.Printf("\nTotal: %d jobs\n", len(pipeline.Jobs))

	return nil
}

func displayJobDetails(job *types.Job, prefix string) {
	details := []struct {
		label string
		value string
		show  bool
	}{
		{"Stage", job.Stage, job.Stage != ""},
		{"Runner", getRunnerInfo(job), true},
		{"Image", job.Image, job.Image != ""},
		{"Timeout", fmt.Sprintf("%d minutes", job.TimeoutMin), job.TimeoutMin > 0},
		{"Allow Failure", "true", job.AllowFailure || job.ContinueOnErr},
		{"When", job.When, job.When != ""},
	}

	// Add basic job info
	for _, d := range details {
		if d.show {
			fmt.Printf("%s%s %s: %s\n", prefix, TreeBranch, d.label, d.value)
		}
	}

	// Display tags
	if len(job.Tags) > 0 {
		fmt.Printf("%s%s Tags: %s\n", prefix, TreeBranch, strings.Join(job.Tags, ", "))
	}

	// Display dependencies
	if len(job.Needs) > 0 {
		fmt.Printf("%s%s Depends on: %s\n", prefix, TreeBranch, strings.Join(job.Needs, ", "))
	}

	// Display environment variables
	if len(job.Environment) > 0 {
		fmt.Printf("%s%s Environment variables:\n", prefix, TreeBranch)
		envKeys := getSortedKeys(job.Environment)
		for i, key := range envKeys {
			envPrefix := TreeBranch
			if i == len(envKeys)-1 {
				envPrefix = TreeEnd
			}
			fmt.Printf("%s%s  %s %s=%s\n", prefix, TreePipe, envPrefix, key, job.Environment[key])
		}
	}

	// Display services
	if len(job.Services) > 0 {
		fmt.Printf("%s%s Services:\n", prefix, TreeBranch)
		serviceNames := make([]string, 0, len(job.Services))
		for name := range job.Services {
			serviceNames = append(serviceNames, name)
		}
		sort.Strings(serviceNames)

		for i, name := range serviceNames {
			service := job.Services[name]
			servicePrefix := TreeBranch
			if i == len(serviceNames)-1 {
				servicePrefix = TreeEnd
			}
			fmt.Printf("%s%s  %s %s: %s\n", prefix, TreePipe, servicePrefix, name, service.Image)
		}
	}

	// Display artifacts
	if job.Artifacts != nil && len(job.Artifacts.Paths) > 0 {
		fmt.Printf("%s%s Artifacts:\n", prefix, TreeBranch)
		for i, path := range job.Artifacts.Paths {
			artifactPrefix := TreeBranch
			if i == len(job.Artifacts.Paths)-1 {
				artifactPrefix = TreeEnd
			}
			fmt.Printf("%s%s  %s %s\n", prefix, TreePipe, artifactPrefix, path)
		}
	}

	// Display cache
	if job.Cache != nil && len(job.Cache.Paths) > 0 {
		fmt.Printf("%s%s Cache:\n", prefix, TreeBranch)
		for i, path := range job.Cache.Paths {
			cachePrefix := TreeBranch
			if i == len(job.Cache.Paths)-1 {
				cachePrefix = TreeEnd
			}
			fmt.Printf("%s%s  %s %s\n", prefix, TreePipe, cachePrefix, path)
		}
	}

	// Display steps (always last)
	if len(job.Steps) > 0 {
		fmt.Printf("%s%s Steps (%d):\n", prefix, TreeEnd, len(job.Steps))
		for i, step := range job.Steps {
			stepPrefix := TreeBranch
			if i == len(job.Steps)-1 {
				stepPrefix = TreeEnd
			}

			stepName := step.Name
			if stepName == "" {
				stepName = fmt.Sprintf("Step %d", i+1)
			}

			fmt.Printf("%s%s  %s %s", prefix, TreeSpace, stepPrefix, stepName)

			// Add step details
			if step.Uses != "" {
				fmt.Printf(" (action: %s)", step.Uses)
			} else if step.Shell != "" && step.Shell != "bash" && step.Shell != "sh" {
				fmt.Printf(" (shell: %s)", step.Shell)
			}

			if step.TimeoutMin > 0 {
				fmt.Printf(" (timeout: %dm)", step.TimeoutMin)
			}

			if step.ContinueOnErr {
				fmt.Printf(" (continue-on-error)")
			}

			if step.WorkingDir != "" {
				fmt.Printf(" (workdir: %s)", step.WorkingDir)
			}

			fmt.Println()
		}
	}
}

func getRunnerInfo(job *types.Job) string {
	if job.RunsOn != "" {
		return job.RunsOn
	}
	if job.Container != nil && job.Container.Image != "" {
		return job.Container.Image
	}
	if job.Image != "" {
		return job.Image
	}
	if len(job.Tags) > 0 {
		return fmt.Sprintf("tags: %s", strings.Join(job.Tags, ","))
	}
	return "default"
}

func getSortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
