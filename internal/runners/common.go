package runners

import (
	"fmt"
	"strings"
	"time"
)

// ANSI color codes - subtle/muted versions
const (
	ColorReset   = "\033[0m"
	ColorRed     = "\033[31m"   // Red for errors
	ColorGreen   = "\033[32m"   // Green for success (subtle)
	ColorYellow  = "\033[33m"   // Yellow for warnings
	ColorBlue    = "\033[34m"   // Blue for info
	ColorGray    = "\033[90m"   // Gray for secondary info
	ColorDimGray = "\033[2;37m" // Dim gray for less important
	ColorBold    = "\033[1m"    // Bold
	ColorDim     = "\033[2m"    // Dim

	// Additional muted colors
	ColorDarkBlue  = "\033[34;2m" // Darker blue
	ColorDarkGray  = "\033[1;30m" // Dark gray
	ColorLightGray = "\033[37m"   // Light gray
)

// IndentLevel represents the current indentation level
type IndentLevel int

const (
	IndentNone   IndentLevel = 0
	IndentJob    IndentLevel = 1
	IndentStep   IndentLevel = 2
	IndentDetail IndentLevel = 3
	IndentOutput IndentLevel = 4
)

// OutputFormatter provides consistent output formatting for all runners
type OutputFormatter struct {
	Verbose    bool
	Quiet      bool
	Width      int
	UseColor   bool
	IndentSize int
}

// NewOutputFormatter creates a new output formatter
func NewOutputFormatter(verbose bool) *OutputFormatter {
	return &OutputFormatter{
		Verbose:    verbose,
		Quiet:      false,
		Width:      80,
		UseColor:   true, // Can be made configurable
		IndentSize: 2,    // Spaces per indent level
	}
}

// SetQuiet enables or disables non-essential output. When true, formatter
// Print* methods (except PrintError / PrintStepFailed) become no-ops.
func (f *OutputFormatter) SetQuiet(quiet bool) {
	f.Quiet = quiet
}

// silenced returns true when the formatter is in quiet mode. Print methods
// call this and bail out so output stays clean.
func (f *OutputFormatter) silenced() bool {
	return f.Quiet
}

// GetIndent returns the indentation string for a given level
func (f *OutputFormatter) GetIndent(level IndentLevel) string {
	return strings.Repeat(" ", int(level)*f.IndentSize)
}

// Color applies color to text if colors are enabled
func (f *OutputFormatter) Color(text string, color string) string {
	if !f.UseColor {
		return text
	}
	return color + text + ColorReset
}

// PrintHeader prints the job execution header
func (f *OutputFormatter) PrintHeader(jobName, workdir, runner string) {
	if f.silenced() {
		return
	}
	fmt.Println()
	fmt.Println(f.Line('='))
	fmt.Printf("%s Running Job: %s\n",
		f.GetIndent(IndentNone),
		f.Color(jobName, ColorBold))
	fmt.Println(f.Line('-'))
	fmt.Printf("%s Working Directory: %s\n",
		f.GetIndent(IndentJob),
		f.Color(workdir, ColorGray))
	fmt.Printf("%s Runner: %s\n",
		f.GetIndent(IndentJob),
		f.Color(runner, ColorGray))
	fmt.Println(f.Line('='))
}

// PrintStepHeader prints a step header with progress
func (f *OutputFormatter) PrintStepHeader(stepName string, current, total int) {
	if f.silenced() {
		return
	}
	fmt.Println()
	progress := fmt.Sprintf("[%d/%d]", current, total)
	fmt.Printf("%s%s %s\n",
		f.GetIndent(IndentStep),
		f.Color(progress, ColorDarkGray),
		f.Color(stepName, ColorBlue))
	fmt.Printf("%s%s\n",
		f.GetIndent(IndentStep),
		f.Color(f.Line('-'), ColorDimGray))
}

// PrintStepComplete prints step completion
func (f *OutputFormatter) PrintStepComplete(duration time.Duration) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s %s\n",
		f.GetIndent(IndentStep),
		f.Color("✓", ColorGreen),
		f.Color(fmt.Sprintf("Step completed in %s", f.FormatDuration(duration)), ColorGray))
}

// PrintStepFailed prints step failure
func (f *OutputFormatter) PrintStepFailed(err error, duration time.Duration) {
	fmt.Printf("%s%s Step FAILED after %s: %s\n",
		f.GetIndent(IndentStep),
		f.Color("✗", ColorRed),
		f.FormatDuration(duration),
		f.Color(err.Error(), ColorRed))
}

// PrintStepSkipped prints that a step was skipped
func (f *OutputFormatter) PrintStepSkipped(reason string) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s Step skipped: %s\n",
		f.GetIndent(IndentStep),
		f.Color("○", ColorYellow),
		f.Color(reason, ColorDimGray))
}

// PrintJobComplete prints job completion summary
func (f *OutputFormatter) PrintJobComplete(jobName string, duration time.Duration, success bool) {
	if f.silenced() {
		return
	}
	fmt.Println()
	fmt.Println(f.Line('='))

	status := "completed successfully"
	color := ColorGreen
	if !success {
		status = "FAILED"
		color = ColorRed
	}

	fmt.Printf("%s Job '%s' %s\n",
		f.GetIndent(IndentJob),
		f.Color(jobName, ColorBold),
		f.Color(status, color))
	fmt.Printf("%s Total duration: %s\n",
		f.GetIndent(IndentJob),
		f.Color(f.FormatDuration(duration), ColorGray))
	fmt.Println(f.Line('='))
	fmt.Println()
}

// PrintOutput prints command output with optional prefix and indentation
func (f *OutputFormatter) PrintOutput(line string, indent int) {
	if f.silenced() {
		return
	}
	// Use custom indent or convert to IndentLevel
	indentStr := strings.Repeat(" ", indent)

	// Mute the output color to gray for less distraction
	fmt.Printf("%s%s\n", indentStr, f.Color(line, ColorDimGray))
}

// PrintOutputWithLevel prints output with specific indent level
func (f *OutputFormatter) PrintOutputWithLevel(line string, level IndentLevel) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s\n",
		f.GetIndent(level),
		f.Color(line, ColorDimGray))
}

// PrintInfo prints an informational message
func (f *OutputFormatter) PrintInfo(message string) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s %s\n",
		f.GetIndent(IndentDetail),
		f.Color("ℹ", ColorBlue),
		f.Color(message, ColorLightGray))
}

// PrintWarning prints a warning message
func (f *OutputFormatter) PrintWarning(message string) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s %s\n",
		f.GetIndent(IndentDetail),
		f.Color("⚠", ColorYellow),
		f.Color(message, ColorYellow))
}

// PrintError prints an error message
func (f *OutputFormatter) PrintError(message string) {
	fmt.Printf("%s%s %s\n",
		f.GetIndent(IndentDetail),
		f.Color("✗", ColorRed),
		f.Color(message, ColorRed))
}

// PrintDebug prints a debug message if verbose mode is enabled
func (f *OutputFormatter) PrintDebug(message string) {
	if f.silenced() || !f.Verbose {
		return
	}
	fmt.Printf("%s%s %s\n",
		f.GetIndent(IndentOutput),
		f.Color("[DEBUG]", ColorDarkGray),
		f.Color(message, ColorDimGray))
}

// PrintDryRun prints dry run header
func (f *OutputFormatter) PrintDryRun() {
	if f.silenced() {
		return
	}
	fmt.Println()
	fmt.Println(f.Color(f.Line('*'), ColorYellow))
	fmt.Printf("%s %s\n",
		f.GetIndent(IndentJob),
		f.Color("DRY RUN MODE - Commands will be displayed but not executed", ColorYellow))
	fmt.Println(f.Color(f.Line('*'), ColorYellow))
}

// PrintSection prints a section header
func (f *OutputFormatter) PrintSection(title string) {
	if f.silenced() {
		return
	}
	fmt.Println()
	fmt.Printf("%s%s\n",
		f.GetIndent(IndentJob),
		f.Color(title, ColorBold))
	fmt.Printf("%s%s\n",
		f.GetIndent(IndentJob),
		f.Color(f.Line('-'), ColorDimGray))
}

// PrintSubSection prints a subsection with indent
func (f *OutputFormatter) PrintSubSection(title string) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s\n",
		f.GetIndent(IndentStep),
		f.Color(title, ColorBlue))
}

// PrintKeyValue prints a key-value pair with proper indentation
func (f *OutputFormatter) PrintKeyValue(key, value string, indent int) {
	if f.silenced() {
		return
	}
	prefix := strings.Repeat(" ", indent)
	fmt.Printf("%s%s: %s\n",
		prefix,
		f.Color(key, ColorDarkGray),
		f.Color(value, ColorLightGray))
}

// PrintKeyValueWithLevel prints a key-value pair at specific indent level
func (f *OutputFormatter) PrintKeyValueWithLevel(key, value string, level IndentLevel) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s: %s\n",
		f.GetIndent(level),
		f.Color(key, ColorDarkGray),
		f.Color(value, ColorLightGray))
}

// PrintList prints a list item with proper indentation
func (f *OutputFormatter) PrintList(item string, indent int) {
	if f.silenced() {
		return
	}
	prefix := strings.Repeat(" ", indent)
	fmt.Printf("%s%s %s\n",
		prefix,
		f.Color("•", ColorDarkGray),
		f.Color(item, ColorLightGray))
}

// PrintListWithLevel prints a list item at specific indent level
func (f *OutputFormatter) PrintListWithLevel(item string, level IndentLevel) {
	if f.silenced() {
		return
	}
	fmt.Printf("%s%s %s\n",
		f.GetIndent(level),
		f.Color("•", ColorDarkGray),
		f.Color(item, ColorLightGray))
}

// PrintCommand prints a command that will be or was executed
func (f *OutputFormatter) PrintCommand(cmd string, indent int) {
	if f.silenced() {
		return
	}
	prefix := strings.Repeat(" ", indent)

	// Split long commands for readability
	if len(cmd) > (f.Width - indent - 4) {
		lines := f.WrapText(cmd, f.Width-indent-4)
		for i, line := range lines {
			if i == 0 {
				fmt.Printf("%s%s %s\n",
					prefix,
					f.Color("$", ColorBlue),
					f.Color(line, ColorGray))
			} else {
				fmt.Printf("%s  %s\n",
					prefix,
					f.Color(line, ColorGray))
			}
		}
	} else {
		fmt.Printf("%s%s %s\n",
			prefix,
			f.Color("$", ColorBlue),
			f.Color(cmd, ColorGray))
	}
}

// PrintCommandWithLevel prints a command at specific indent level
func (f *OutputFormatter) PrintCommandWithLevel(cmd string, level IndentLevel) {
	f.PrintCommand(cmd, int(level)*f.IndentSize)
}

// Line generates a line of the specified character
func (f *OutputFormatter) Line(char rune) string {
	// Make lines slightly shorter for cleaner look
	width := f.Width - (f.IndentSize * 2)
	return strings.Repeat(string(char), width)
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
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// Progress shows a progress indicator for long-running operations
type Progress struct {
	formatter *OutputFormatter
	message   string
	start     time.Time
	level     IndentLevel
	quiet     bool
}

// NewProgress creates a new progress indicator
func (f *OutputFormatter) NewProgress(message string) *Progress {
	return f.NewProgressWithLevel(message, IndentDetail)
}

// NewProgressWithLevel creates a new progress indicator at specific indent level
func (f *OutputFormatter) NewProgressWithLevel(message string, level IndentLevel) *Progress {
	p := &Progress{
		formatter: f,
		message:   message,
		start:     time.Now(),
		level:     level,
		quiet:     f.silenced(),
	}
	if !p.quiet {
		fmt.Printf("%s%s... ",
			f.GetIndent(level),
			f.Color(message, ColorGray))
	}
	return p
}

// Complete marks the progress as complete
func (p *Progress) Complete(success bool) {
	if p.quiet {
		return
	}
	duration := time.Since(p.start)
	if success {
		fmt.Printf("%s (%s)\n",
			p.formatter.Color("done", ColorGreen),
			p.formatter.Color(p.formatter.FormatDuration(duration), ColorDimGray))
	} else {
		fmt.Printf("%s (%s)\n",
			p.formatter.Color("FAILED", ColorRed),
			p.formatter.Color(p.formatter.FormatDuration(duration), ColorDimGray))
	}
}

// Update updates the progress message
func (p *Progress) Update(message string) {
	if p.quiet {
		return
	}
	fmt.Printf("\r%s%s... ",
		p.formatter.GetIndent(p.level),
		p.formatter.Color(message, ColorGray))
}

// JobSummary represents a summary of job execution (in order to track the execution of job)
type JobSummary struct {
	JobName        string
	TotalSteps     int
	CompletedSteps int
	FailedSteps    int
	SkippedSteps   int
	Duration       time.Duration
	Success        bool
	Errors         []string
}

// PrintJobSummary prints a detailed job summary
func (f *OutputFormatter) PrintJobSummary(summary *JobSummary) {
	if f.silenced() {
		return
	}
	fmt.Println()
	fmt.Println(f.Color(f.Line('='), ColorDimGray))
	fmt.Printf("%s %s\n",
		f.GetIndent(IndentJob),
		f.Color("JOB SUMMARY", ColorBold))
	fmt.Println(f.Color(f.Line('-'), ColorDimGray))

	f.PrintKeyValueWithLevel("Job Name", summary.JobName, IndentStep)
	f.PrintKeyValueWithLevel("Total Steps", fmt.Sprintf("%d", summary.TotalSteps), IndentStep)
	f.PrintKeyValueWithLevel("Completed", fmt.Sprintf("%d", summary.CompletedSteps), IndentStep)

	if summary.FailedSteps > 0 {
		f.PrintKeyValueWithLevel("Failed",
			f.Color(fmt.Sprintf("%d", summary.FailedSteps), ColorRed),
			IndentStep)
	}

	if summary.SkippedSteps > 0 {
		f.PrintKeyValueWithLevel("Skipped",
			f.Color(fmt.Sprintf("%d", summary.SkippedSteps), ColorYellow),
			IndentStep)
	}

	f.PrintKeyValueWithLevel("Duration", f.FormatDuration(summary.Duration), IndentStep)

	status := f.Color("SUCCESS", ColorGreen)
	if !summary.Success {
		status = f.Color("FAILED", ColorRed)
	}
	f.PrintKeyValueWithLevel("Status", status, IndentStep)

	if len(summary.Errors) > 0 {
		fmt.Println()
		fmt.Printf("%s %s:\n",
			f.GetIndent(IndentStep),
			f.Color("Errors", ColorRed))
		for _, err := range summary.Errors {
			f.PrintListWithLevel(err, IndentDetail)
		}
	}

	fmt.Println(f.Color(f.Line('='), ColorDimGray))
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
	if f.silenced() {
		return
	}
	status := f.Color("OK", ColorGreen)
	if result.Skipped {
		status = f.Color("SKIPPED", ColorYellow)
	} else if !result.Success {
		status = f.Color("FAILED", ColorRed)
	}

	progress := fmt.Sprintf("[%d/%d]", current, total)

	fmt.Printf("%s%s %-50s [%s] %s\n",
		f.GetIndent(IndentStep),
		f.Color(progress, ColorDarkGray),
		f.TruncateText(result.Name, 50),
		status,
		f.Color(f.FormatDuration(result.Duration), ColorDimGray))

	if f.Verbose && result.Output != "" {
		lines := strings.Split(strings.TrimSpace(result.Output), "\n")
		for _, line := range lines {
			if line != "" {
				f.PrintOutputWithLevel(line, IndentOutput)
			}
		}
	}

	if result.Error != nil {
		f.PrintError(fmt.Sprintf("  %v", result.Error))
	}
}

// PrintEnvironment prints environment variables in a formatted way
func (f *OutputFormatter) PrintEnvironment(env map[string]string) {
	if f.silenced() || len(env) == 0 {
		return
	}

	f.PrintSection("Environment Variables")

	// Get sorted keys for consistent output
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}

	for _, key := range keys {
		f.PrintKeyValueWithLevel(key, env[key], IndentStep)
	}
}

// PrintServices prints service information
func (f *OutputFormatter) PrintServices(services map[string]string) {
	if f.silenced() || len(services) == 0 {
		return
	}

	f.PrintSection("Services")

	for name, image := range services {
		f.PrintKeyValueWithLevel(name, image, IndentStep)
	}
}

// SetColorEnabled enables or disables color output
func (f *OutputFormatter) SetColorEnabled(enabled bool) {
	f.UseColor = enabled
}

// IsColorEnabled returns whether colors are enabled
func (f *OutputFormatter) IsColorEnabled() bool {
	return f.UseColor
}
