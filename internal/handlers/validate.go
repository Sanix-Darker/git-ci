package handlers

import (
	"fmt"
	"strings"

	"github.com/sanix-darker/git-ci/pkg/types"
	cli "github.com/urfave/cli/v2"
)

// CmdValidate handles the validate command
func CmdValidate(c *cli.Context) error {
	filePath := c.String("file")
	strict := c.Bool("strict")
	provider := c.String("provider")

	// Parse pipeline
	pipeline, err := parseInputWithProvider(filePath, provider)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	printVerbose(c, "Validating pipeline: %s\n", pipeline.Name)

	// Perform validation
	errors := validatePipeline(pipeline, strict)

	if len(errors) > 0 {
		fmt.Println("Validation errors found:")
		fmt.Println(strings.Repeat("-", 60))
		for i, err := range errors {
			fmt.Printf("%d. %s\n", i+1, err)
		}
		fmt.Println(strings.Repeat("-", 60))
		return fmt.Errorf("validation failed with %d error(s)", len(errors))
	}

	fmt.Printf("✓ Pipeline '%s' is valid\n", pipeline.Name)

	// Print summary
	fmt.Printf("\nSummary:\n")
	fmt.Printf("  Provider: %s\n", pipeline.Provider)
	fmt.Printf("  Jobs: %d\n", len(pipeline.Jobs))

	totalSteps := 0
	for _, job := range pipeline.Jobs {
		totalSteps += len(job.Steps)
	}
	fmt.Printf("  Total steps: %d\n", totalSteps)

	if len(pipeline.Stages) > 0 {
		fmt.Printf("  Stages: %s\n", strings.Join(pipeline.Stages, ", "))
	}

	return nil
}

// validatePipeline performs validation on the pipeline
func validatePipeline(pipeline *types.Pipeline, strict bool) []string {
	var errors []string

	if pipeline == nil {
		return []string{"pipeline is nil"}
	}

	// Validate pipeline name
	if pipeline.Name == "" {
		errors = append(errors, "pipeline name is empty")
	}

	// Validate jobs
	if len(pipeline.Jobs) == 0 {
		errors = append(errors, "no jobs defined in pipeline")
	}

	// Validate job stages
	stageMap := make(map[string]bool)
	for _, stage := range pipeline.Stages {
		stageMap[stage] = true
	}

	// Track job names for dependency validation
	jobNames := make(map[string]bool)
	for name := range pipeline.Jobs {
		jobNames[name] = true
	}

	// Validate each job
	for jobName, job := range pipeline.Jobs {
		// Validate job has steps or is a trigger
		if len(job.Steps) == 0 && job.Trigger == nil {
			errors = append(errors, fmt.Sprintf("job '%s' has no steps or trigger", jobName))
		}

		// Validate stage exists if specified
		if job.Stage != "" && len(stageMap) > 0 && !stageMap[job.Stage] {
			errors = append(errors, fmt.Sprintf("job '%s' references undefined stage '%s'", jobName, job.Stage))
		}

		// Validate job dependencies exist
		for _, need := range job.Needs {
			if !jobNames[need] {
				errors = append(errors, fmt.Sprintf("job '%s' depends on non-existent job '%s'", jobName, need))
			}
		}

		// Check for circular dependencies
		if err := checkCircularDependencies(jobName, job, pipeline.Jobs, []string{}); err != nil {
			errors = append(errors, err.Error())
		}

		// Strict validation
		if strict {
			// Validate runner/image
			if job.RunsOn == "" && job.Image == "" && job.Container == nil && len(job.Tags) == 0 {
				errors = append(errors, fmt.Sprintf("job '%s' has no runner specified", jobName))
			}

			// Validate steps
			for i, step := range job.Steps {
				if step.Name == "" && step.Run == "" && step.Uses == "" {
					errors = append(errors, fmt.Sprintf("job '%s' step %d is empty", jobName, i+1))
				}

				// Validate timeout
				if step.TimeoutMin < 0 {
					errors = append(errors, fmt.Sprintf("job '%s' step %d has invalid timeout", jobName, i+1))
				}
			}

			// Validate environment variables
			for key := range job.Environment {
				if key == "" {
					errors = append(errors, fmt.Sprintf("job '%s' has empty environment variable key", jobName))
				}
			}

			// Validate artifacts
			if job.Artifacts != nil {
				if len(job.Artifacts.Paths) == 0 {
					errors = append(errors, fmt.Sprintf("job '%s' has artifacts defined but no paths", jobName))
				}
			}

			// Validate cache
			if job.Cache != nil {
				if len(job.Cache.Paths) == 0 {
					errors = append(errors, fmt.Sprintf("job '%s' has cache defined but no paths", jobName))
				}
			}
		}
	}

	return errors
}

// checkCircularDependencies checks for circular job dependencies
func checkCircularDependencies(jobName string, job *types.Job, allJobs map[string]*types.Job, visited []string) error {
	// Check if we've already visited this job (circular dependency)
	for _, v := range visited {
		if v == jobName {
			return fmt.Errorf("circular dependency detected: %s", strings.Join(append(visited, jobName), " -> "))
		}
	}

	visited = append(visited, jobName)

	// Check dependencies recursively
	for _, need := range job.Needs {
		if dependentJob, exists := allJobs[need]; exists {
			if err := checkCircularDependencies(need, dependentJob, allJobs, visited); err != nil {
				return err
			}
		}
	}

	return nil
}
