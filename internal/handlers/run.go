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

// runJobsSequential runs jobs one by one
func runJobsSequential(c *cli.Context, jobs map[string]*types.Job, workdir string, cfg *config.RunnerConfig) error {
	continueOnError := c.Bool("continue-on-error")

	fmt.Printf("Running %d job(s) sequentially\n", len(jobs))
	fmt.Println(strings.Repeat("-", 80))

	startTime := time.Now()
	successCount := 0
	failureCount := 0

	for jobName, job := range jobs {
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
			fmt.Printf("Job '%s' failed after %s: %v\n", jobName, formatDuration(jobDuration), err)

			if !continueOnError && !job.AllowFailure {
				return fmt.Errorf("job '%s' failed: %w", jobName, err)
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

	if failureCount > 0 && !continueOnError {
		return fmt.Errorf("%d job(s) failed", failureCount)
	}

	return nil
}

// runJobsParallel runs jobs in parallel
func runJobsParallel(c *cli.Context, jobs map[string]*types.Job, workdir string, cfg *config.RunnerConfig) error {
	maxParallel := c.Int("max-parallel")
	if maxParallel <= 0 {
		maxParallel = runtime.NumCPU()
	}

	continueOnError := c.Bool("continue-on-error")

	fmt.Printf("Running %d job(s) in parallel (max %d)\n", len(jobs), maxParallel)
	fmt.Println(strings.Repeat("-", 80))

	startTime := time.Now()

	// Create semaphore for limiting parallelism
	sem := make(chan struct{}, maxParallel)

	// Create wait group
	var wg sync.WaitGroup

	// Results channel
	type jobResult struct {
		name     string
		err      error
		duration time.Duration
	}
	results := make(chan jobResult, len(jobs))

	// Run jobs
	for jobName, job := range jobs {
		wg.Add(1)

		go func(name string, j *types.Job) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Set job name if not set
			if j.Name == "" {
				j.Name = name
			}

			printVerbose(c, "Starting parallel job: %s\n", name)

			// Create runner
			runner, err := createRunner(c, cfg)
			if err != nil {
				results <- jobResult{
					name:     name,
					err:      fmt.Errorf("failed to create runner: %w", err),
					duration: 0,
				}
				return
			}

			// Run job
			jobStart := time.Now()
			err = runner.RunJob(j, workdir)
			jobDuration := time.Since(jobStart)

			// Cleanup
			if cleanupErr := runner.Cleanup(); cleanupErr != nil {
				printVerbose(c, "Warning: cleanup failed for job %s: %v\n", name, cleanupErr)
			}

			results <- jobResult{
				name:     name,
				err:      err,
				duration: jobDuration,
			}
		}(jobName, job)
	}

	// Wait for all jobs to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	successCount := 0
	failureCount := 0
	var firstError error

	for result := range results {
		if result.err != nil {
			failureCount++
			fmt.Printf("Job '%s' failed after %s: %v\n", result.name, formatDuration(result.duration), result.err)

			if firstError == nil && !continueOnError {
				firstError = result.err
			}
		} else {
			successCount++
			fmt.Printf("Job '%s' succeeded in %s\n", result.name, formatDuration(result.duration))
		}
	}

	totalDuration := time.Since(startTime)

	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("Pipeline completed in %s\n", formatDuration(totalDuration))
	fmt.Printf("Success: %d, Failed: %d, Total: %d\n", successCount, failureCount, len(jobs))

	if firstError != nil && !continueOnError {
		return fmt.Errorf("pipeline failed: %w", firstError)
	}

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
