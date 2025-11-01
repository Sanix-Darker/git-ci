package runners

import (
	"fmt"
	"strings"
	"time"
)

// Helper function that was missing - Fixed
func truncateMultiline(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}

	truncated := strings.Join(lines[:maxLines], "\n")
	return fmt.Sprintf("%s\n   ... (%d more lines)", truncated, len(lines)-maxLines)
}

// Helper function to escape shell commands
func escapeShellCommand(cmd string) string {
	// Escape single quotes in the command
	return strings.ReplaceAll(cmd, "'", "'\\''")
}

// OutputFormatter provides consistent output formatting for all runners
type OutputFormatter struct {
	Verbose bool
	Width   int
}

// NewOutputFormatter creates a new output formatter
func NewOutputFormatter(verbose bool) *OutputFormatter {
	return &OutputFormatter{
		Verbose: verbose,
		Width:   80, // Default terminal width
	}
}

// PrintHeader prints the job execution header
func (f *OutputFormatter) PrintHeader(jobName, workdir, runner string) {
	fmt.Println()
	fmt.Println(f.Line('='))
	fmt.Printf(" Running Job: %s\n", jobName)
	fmt.Println(f.Line('-'))
	fmt.Printf(" Working Directory: %s\n", workdir)
	fmt.Printf(" Runner: %s\n", runner)
	fmt.Println(f.Line('='))
}

// PrintStepHeader prints a step header with progress
func (f *OutputFormatter) PrintStepHeader(stepName string, current, total int) {
	fmt.Println()
	fmt.Printf("[%d/%d] %s\n", current, total, stepName)
	fmt.Println(f.Line('-'))
}

// PrintStepComplete prints step completion
func (f *OutputFormatter) PrintStepComplete(duration time.Duration) {
	fmt.Printf("Step completed in %s\n", f.FormatDuration(duration))
}

// PrintStepFailed prints step failure
func (f *OutputFormatter) PrintStepFailed(err error, duration time.Duration) {
	fmt.Printf("Step FAILED after %s: %v\n", f.FormatDuration(duration), err)
}

// PrintStepSkipped prints that a step was skipped
func (f *OutputFormatter) PrintStepSkipped(reason string) {
	fmt.Printf("Step skipped: %s\n", reason)
}

// PrintJobComplete prints job completion summary
func (f *OutputFormatter) PrintJobComplete(jobName string, duration time.Duration, success bool) {
	fmt.Println()
	fmt.Println(f.Line('='))
	if success {
		fmt.Printf(" Job '%s' completed successfully\n", jobName)
	} else {
		fmt.Printf(" Job '%s' FAILED\n", jobName)
	}
	fmt.Printf(" Total duration: %s\n", f.FormatDuration(duration))
	fmt.Println(f.Line('='))
	fmt.Println()
}

// PrintOutput prints command output with optional prefix
func (f *OutputFormatter) PrintOutput(line string, indent int) {
	prefix := strings.Repeat(" ", indent)
	fmt.Printf("%s%s\n", prefix, line)
}

// PrintInfo prints an informational message
func (f *OutputFormatter) PrintInfo(message string) {
	fmt.Printf("INFO: %s\n", message)
}

// PrintWarning prints a warning message
func (f *OutputFormatter) PrintWarning(message string) {
	fmt.Printf("WARNING: %s\n", message)
}

// PrintError prints an error message
func (f *OutputFormatter) PrintError(message string) {
	fmt.Printf("ERROR: %s\n", message)
}

// PrintDebug prints a debug message if verbose mode is enabled
func (f *OutputFormatter) PrintDebug(message string) {
	if f.Verbose {
		fmt.Printf("DEBUG: %s\n", message)
	}
}

// PrintDryRun prints dry run header
func (f *OutputFormatter) PrintDryRun() {
	fmt.Println()
	fmt.Println(f.Line('*'))
	fmt.Println(" DRY RUN MODE - Commands will be displayed but not executed")
	fmt.Println(f.Line('*'))
}

// PrintSection prints a section header
func (f *OutputFormatter) PrintSection(title string) {
	fmt.Println()
	fmt.Printf("%s\n", title)
	fmt.Println(f.Line('-'))
}

// PrintSubSection prints a subsection with indent
func (f *OutputFormatter) PrintSubSection(title string) {
	fmt.Printf("  %s\n", title)
}

// PrintKeyValue prints a key-value pair
func (f *OutputFormatter) PrintKeyValue(key, value string, indent int) {
	prefix := strings.Repeat(" ", indent)
	fmt.Printf("%s%s: %s\n", prefix, key, value)
}

// PrintList prints a list item
func (f *OutputFormatter) PrintList(item string, indent int) {
	prefix := strings.Repeat(" ", indent)
	fmt.Printf("%s- %s\n", prefix, item)
}

// PrintCommand prints a command that will be or was executed
func (f *OutputFormatter) PrintCommand(cmd string, indent int) {
	prefix := strings.Repeat(" ", indent)

	// Split long commands for readability
	if len(cmd) > (f.Width - indent - 4) {
		lines := f.WrapText(cmd, f.Width-indent-4)
		for i, line := range lines {
			if i == 0 {
				fmt.Printf("%s$ %s\n", prefix, line)
			} else {
				fmt.Printf("%s  %s\n", prefix, line)
			}
		}
	} else {
		fmt.Printf("%s$ %s\n", prefix, cmd)
	}
}

// Line generates a line of the specified character
func (f *OutputFormatter) Line(char rune) string {
	return strings.Repeat(string(char), f.Width)
}

// FormatDuration formats a duration in a human-readable way
func (f *OutputFormatter) FormatDuration(d time.Duration) string {
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

// WrapText wraps text to fit within the specified width
func (f *OutputFormatter) WrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	var lines []string
	words := strings.Fields(text)

	if len(words) == 0 {
		return []string{""}
	}

	currentLine := words[0]

	for _, word := range words[1:] {
		if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = word
		}
	}

	if currentLine != "" {
		lines = append(lines, currentLine)
	}

	return lines
}

// TruncateText truncates text to fit within the specified width
func (f *OutputFormatter) TruncateText(text string, width int) string {
	if len(text) <= width {
		return text
	}
	if width <= 3 {
		return text[:width]
	}
	return text[:width-3] + "..."
}

// Progress shows a progress indicator for long-running operations
type Progress struct {
	formatter *OutputFormatter
	message   string
	start     time.Time
}

// NewProgress creates a new progress indicator
func (f *OutputFormatter) NewProgress(message string) *Progress {
	p := &Progress{
		formatter: f,
		message:   message,
		start:     time.Now(),
	}
	fmt.Printf("%s... ", message)
	return p
}

// Complete marks the progress as complete
func (p *Progress) Complete(success bool) {
	duration := time.Since(p.start)
	if success {
		fmt.Printf("done (%s)\n", p.formatter.FormatDuration(duration))
	} else {
		fmt.Printf("FAILED (%s)\n", p.formatter.FormatDuration(duration))
	}
}

// Update updates the progress message
func (p *Progress) Update(message string) {
	fmt.Printf("\r%s... ", message)
}

// JobSummary represents a summary of job execution
type JobSummary struct {
	JobName      string
	TotalSteps   int
	CompletedSteps int
	FailedSteps  int
	SkippedSteps int
	Duration     time.Duration
	Success      bool
	Errors       []string
}

// PrintJobSummary prints a detailed job summary
func (f *OutputFormatter) PrintJobSummary(summary *JobSummary) {
	fmt.Println()
	fmt.Println(f.Line('='))
	fmt.Println(" JOB SUMMARY")
	fmt.Println(f.Line('-'))

	f.PrintKeyValue("Job Name", summary.JobName, 2)
	f.PrintKeyValue("Total Steps", fmt.Sprintf("%d", summary.TotalSteps), 2)
	f.PrintKeyValue("Completed", fmt.Sprintf("%d", summary.CompletedSteps), 2)

	if summary.FailedSteps > 0 {
		f.PrintKeyValue("Failed", fmt.Sprintf("%d", summary.FailedSteps), 2)
	}

	if summary.SkippedSteps > 0 {
		f.PrintKeyValue("Skipped", fmt.Sprintf("%d", summary.SkippedSteps), 2)
	}

	f.PrintKeyValue("Duration", f.FormatDuration(summary.Duration), 2)

	status := "SUCCESS"
	if !summary.Success {
		status = "FAILED"
	}
	f.PrintKeyValue("Status", status, 2)

	if len(summary.Errors) > 0 {
		fmt.Println()
		fmt.Println(" Errors:")
		for _, err := range summary.Errors {
			f.PrintList(err, 2)
		}
	}

	fmt.Println(f.Line('='))
}

// StepResult represents the result of a step execution
type StepResult struct {
	Name     string
	Success  bool
	Skipped  bool
	Duration time.Duration
	Output   string
	Error    error
}

// PrintStepResult prints a formatted step result
func (f *OutputFormatter) PrintStepResult(result *StepResult, current, total int) {
	status := "OK"
	if result.Skipped {
		status = "SKIPPED"
	} else if !result.Success {
		status = "FAILED"
	}

	fmt.Printf("[%d/%d] %-50s [%s] %s\n",
		current, total,
		f.TruncateText(result.Name, 50),
		status,
		f.FormatDuration(result.Duration))

	if f.Verbose && result.Output != "" {
		lines := strings.Split(strings.TrimSpace(result.Output), "\n")
		for _, line := range lines {
			if line != "" {
				f.PrintOutput(line, 4)
			}
		}
	}

	if result.Error != nil {
		f.PrintError(fmt.Sprintf("  %v", result.Error))
	}
}

// PrintEnvironment prints environment variables in a formatted way
func (f *OutputFormatter) PrintEnvironment(env map[string]string) {
	if len(env) == 0 {
		return
	}

	f.PrintSection("Environment Variables")

	// Get sorted keys for consistent output
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}

	for _, key := range keys {
		f.PrintKeyValue(key, env[key], 2)
	}
}

// PrintServices prints service information
func (f *OutputFormatter) PrintServices(services map[string]string) {
	if len(services) == 0 {
		return
	}

	f.PrintSection("Services")

	for name, image := range services {
		f.PrintKeyValue(name, image, 2)
	}
}
