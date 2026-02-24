package handlers

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sanix-darker/git-ci/internal/config"
	"github.com/sanix-darker/git-ci/internal/runners"
	"github.com/sanix-darker/git-ci/pkg/types"
	cli "github.com/urfave/cli/v2"
)

// CmdRun handles the run command
func CmdRun(c *cli.Context) error {
	// Get file path
	filePath := c.String("file")

	// Parse pipeline
	pipeline, err := parseInput(filePath)
	if err != nil {
		return fmt.Errorf("failed to parse pipeline: %w", err)
	}

	printVerbose(c, "Parsed pipeline: %s\n", pipeline.Name)

	// Get working directory
	workdir, err := getWorkdir(c)
	if err != nil {
		return err
	}

	// Build runner configuration
	cfg := buildRunnerConfig(c)

	// Determine which jobs to run
	jobs := selectJobsToRun(c, pipeline)
	if len(jobs) == 0 {
		return fmt.Errorf("no jobs to run")
	}

	// Expand matrix strategies into concrete job instances
	jobs = expandMatrixJobs(jobs)

	// Check if running in parallel
	if c.Bool("parallel") {
		return runJobsParallel(c, jobs, workdir, cfg)
	}

	// Run jobs sequentially
	return runJobsSequential(c, jobs, workdir, cfg)
}

// selectJobsToRun selects which jobs to run based on flags
func selectJobsToRun(c *cli.Context, pipeline *types.Pipeline) map[string]*types.Job {
	jobs := pipeline.Jobs

	// Filter by specific job name
	if jobName := c.String("job"); jobName != "" {
		if job, exists := jobs[jobName]; exists {
			return map[string]*types.Job{jobName: job}
		}
		// Try pattern matching
		matchedJobs := make(map[string]*types.Job)
		for name, j := range jobs {
			if matchPattern(name, jobName) {
				matchedJobs[name] = j
			}
		}
		if len(matchedJobs) > 0 {
			return matchedJobs
		}

		fmt.Printf("Warning: job '%s' not found\n", jobName)
		return nil
	}

	// Filter by stage
	if stage := c.String("stage"); stage != "" {
		jobs = getJobsByStage(pipeline, stage)
		if len(jobs) == 0 {
			fmt.Printf("Warning: no jobs found for stage '%s'\n", stage)
			return nil
		}
	}

	// Apply only/except filters
	only := c.StringSlice("only")
	except := c.StringSlice("except")
	jobs = filterJobs(jobs, only, except)

	return jobs
}

// runJobsSequential runs jobs one by one in dependency order
func runJobsSequential(c *cli.Context, jobs map[string]*types.Job, workdir string, cfg *config.RunnerConfig) error {
	continueOnError := c.Bool("continue-on-error")

	// Sort jobs by dependency order
	sortedNames, err := topologicalSort(jobs)
	if err != nil {
		return fmt.Errorf("failed to resolve job dependencies: %w", err)
	}

	fmt.Printf("Running %d job(s) sequentially\n", len(jobs))
	fmt.Println(strings.Repeat("-", 80))

	startTime := time.Now()
	successCount := 0
	failureCount := 0
	failedJobs := make(map[string]bool)

	for _, jobName := range sortedNames {
		job := jobs[jobName]

		// Skip if a dependency failed
		if shouldSkipJob(job, failedJobs, continueOnError) {
			fmt.Printf("Job '%s' skipped: dependency failed\n", jobName)
			failureCount++
			failedJobs[jobName] = true
			continue
		}

		// Set job name if not set
		if job.Name == "" {
			job.Name = jobName
		}

		printVerbose(c, "\nStarting job: %s\n", jobName)

		// Create runner
		runner, err := createRunner(c, cfg)
		if err != nil {
			return fmt.Errorf("failed to create runner for job %s: %w", jobName, err)
		}

		// Run job
		jobStart := time.Now()
		err = runner.RunJob(job, workdir)
		jobDuration := time.Since(jobStart)

		// Cleanup
		if cleanupErr := runner.Cleanup(); cleanupErr != nil {
			printVerbose(c, "Warning: cleanup failed for job %s: %v\n", jobName, cleanupErr)
		}

		if err != nil {
			failureCount++
			failedJobs[jobName] = true
			if job.AllowFailure {
				fmt.Printf("Job '%s' failed (allowed) after %s: %v\n", jobName, formatDuration(jobDuration), err)
			} else {
				fmt.Printf("Job '%s' failed after %s: %v\n", jobName, formatDuration(jobDuration), err)
				if !continueOnError {
					return fmt.Errorf("job '%s' failed: %w", jobName, err)
				}
			}
		} else {
			successCount++
			fmt.Printf("Job '%s' succeeded in %s\n", jobName, formatDuration(jobDuration))
		}
	}

	totalDuration := time.Since(startTime)

	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("Pipeline completed in %s\n", formatDuration(totalDuration))
	fmt.Printf("Success: %d, Failed: %d, Total: %d\n", successCount, failureCount, len(jobs))

	return nil
}

// shouldSkipJob returns true if any of the job's dependencies have failed
func shouldSkipJob(job *types.Job, failedJobs map[string]bool, continueOnError bool) bool {
	if continueOnError {
		return false
	}
	for _, need := range job.Needs {
		if failedJobs[need] {
			return true
		}
	}
	return false
}

// runJobsParallel runs jobs in parallel, respecting dependency levels
func runJobsParallel(c *cli.Context, jobs map[string]*types.Job, workdir string, cfg *config.RunnerConfig) error {
	maxParallel := c.Int("max-parallel")
	if maxParallel <= 0 {
		maxParallel = runtime.NumCPU()
	}

	continueOnError := c.Bool("continue-on-error")

	// Group jobs by dependency level
	levels, err := groupByDependencyLevel(jobs)
	if err != nil {
		return fmt.Errorf("failed to resolve job dependencies: %w", err)
	}

	fmt.Printf("Running %d job(s) in parallel (max %d, %d dependency levels)\n", len(jobs), maxParallel, len(levels))
	fmt.Println(strings.Repeat("-", 80))

	startTime := time.Now()
	successCount := 0
	failureCount := 0
	failedJobs := make(map[string]bool)

	type jobResult struct {
		name     string
		err      error
		duration time.Duration
	}

	// Execute level by level
	for levelIdx, level := range levels {
		printVerbose(c, "\nDependency level %d: %s\n", levelIdx+1, strings.Join(level, ", "))

		sem := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup
		results := make(chan jobResult, len(level))

		for _, jobName := range level {
			job := jobs[jobName]

			// Skip if a dependency failed
			if shouldSkipJob(job, failedJobs, continueOnError) {
				fmt.Printf("Job '%s' skipped: dependency failed\n", jobName)
				failureCount++
				failedJobs[jobName] = true
				continue
			}

			wg.Add(1)
			go func(name string, j *types.Job) {
				defer wg.Done()

				sem <- struct{}{}
				defer func() { <-sem }()

				if j.Name == "" {
					j.Name = name
				}

				printVerbose(c, "Starting parallel job: %s\n", name)

				runner, runnerErr := createRunner(c, cfg)
				if runnerErr != nil {
					results <- jobResult{name: name, err: fmt.Errorf("failed to create runner: %w", runnerErr)}
					return
				}

				jobStart := time.Now()
				runErr := runner.RunJob(j, workdir)
				jobDuration := time.Since(jobStart)

				if cleanupErr := runner.Cleanup(); cleanupErr != nil {
					printVerbose(c, "Warning: cleanup failed for job %s: %v\n", name, cleanupErr)
				}

				results <- jobResult{name: name, err: runErr, duration: jobDuration}
			}(jobName, job)
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		for result := range results {
			if result.err != nil {
				failureCount++
				failedJobs[result.name] = true
				fmt.Printf("Job '%s' failed after %s: %v\n", result.name, formatDuration(result.duration), result.err)
			} else {
				successCount++
				fmt.Printf("Job '%s' succeeded in %s\n", result.name, formatDuration(result.duration))
			}
		}

		// If not continuing on error and we had failures, stop
		if failureCount > 0 && !continueOnError {
			break
		}
	}

	totalDuration := time.Since(startTime)

	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("Pipeline completed in %s\n", formatDuration(totalDuration))
	fmt.Printf("Success: %d, Failed: %d, Total: %d\n", successCount, failureCount, len(jobs))

	if failureCount > 0 {
		return fmt.Errorf("%d job(s) failed", failureCount)
	}

	return nil
}

// createRunner creates the appropriate runner based on flags
func createRunner(c *cli.Context, cfg *config.RunnerConfig) (types.Runner, error) {
	// Check for Docker runner
	if c.Bool("docker") {
		runner, err := runners.NewDockerRunner(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker runner: %w", err)
		}
		return runner, nil
	}

	// Check for Podman runner
	if c.Bool("podman") {
		runner, err := runners.NewPodmanRunner(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create Podman runner: %w", err)
		}
		return runner, nil
	}

	// Default to Bash runner
	return runners.NewBashRunner(cfg), nil
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		seconds := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", hours, minutes)
}
