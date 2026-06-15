package handlers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	cli "github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// DiscoveredFile represents a single CI/CD configuration file found during discovery.
type DiscoveredFile struct {
	Path     string `json:"path" yaml:"path"`
	Provider string `json:"provider" yaml:"provider"`
	Jobs     int    `json:"jobs,omitempty" yaml:"jobs,omitempty"`
	Detected bool   `json:"detected" yaml:"detected"`
}

// DiscoverResult is the top-level structure returned by the discover command.
type DiscoverResult struct {
	Directory string           `json:"directory" yaml:"directory"`
	Total     int              `json:"total" yaml:"total"`
	Files     []DiscoveredFile `json:"files" yaml:"files"`
}

// ciFilePattern defines a known CI configuration file pattern and its provider.
type ciFilePattern struct {
	Pattern  string // glob pattern relative to the search root
	Provider string // human-readable provider name
	FileOnly bool   // if true, match exact file name (not a glob)
}

// knownCIFiles lists all CI/CD configuration files by provider, ordered by
// specificity (more specific patterns first). This covers both the fully
// implemented parsers (GitHub, GitLab) and the planned providers.
var knownCIFiles = []ciFilePattern{
	// GitHub Actions — all workflow files under .github/workflows/
	{Pattern: ".github/workflows/*.yml", Provider: "GitHub Actions"},
	{Pattern: ".github/workflows/*.yaml", Provider: "GitHub Actions"},

	// GitLab CI
	{Pattern: ".gitlab-ci.yml", Provider: "GitLab CI", FileOnly: true},
	{Pattern: ".gitlab-ci.yaml", Provider: "GitLab CI", FileOnly: true},

	// Bitbucket Pipelines
	{Pattern: "bitbucket-pipelines.yml", Provider: "Bitbucket Pipelines", FileOnly: true},

	// Azure DevOps
	{Pattern: "azure-pipelines.yml", Provider: "Azure DevOps", FileOnly: true},
	{Pattern: "azure-pipelines.yaml", Provider: "Azure DevOps", FileOnly: true},

	// CircleCI
	{Pattern: ".circleci/config.yml", Provider: "CircleCI", FileOnly: true},

	// Jenkins
	{Pattern: "Jenkinsfile", Provider: "Jenkins", FileOnly: true},

	// Drone CI
	{Pattern: ".drone.yml", Provider: "Drone CI", FileOnly: true},

	// Travis CI
	{Pattern: ".travis.yml", Provider: "Travis CI", FileOnly: true},

	// Tekton
	{Pattern: "tekton.yaml", Provider: "Tekton", FileOnly: true},

	// Argo Workflows
	{Pattern: "argo-workflow.yaml", Provider: "Argo Workflows", FileOnly: true},
	{Pattern: "argo-workflow.yml", Provider: "Argo Workflows", FileOnly: true},
}

// CmdDiscover handles the discover command: it walks the project directory
// looking for known CI/CD configuration files and reports what it finds.
func CmdDiscover(c *cli.Context) error {
	dir := c.String("directory")
	if dir == "" {
		dir = "."
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("invalid directory %q: %w", dir, err)
	}

	// Verify the directory exists
	if fi, err := os.Stat(absDir); err != nil {
		return fmt.Errorf("directory %q does not exist: %w", absDir, err)
	} else if !fi.IsDir() {
		return fmt.Errorf("%q is not a directory", absDir)
	}

	format := c.String("format")
	if format == "" {
		format = "tree"
	}

	printVerbose(c, "Discovering CI/CD files in %s...\n", absDir)

	// Walk all patterns and collect matches
	discovered := walkPatterns(absDir)
	sort.Slice(discovered, func(i, j int) bool {
		return discovered[i].Path < discovered[j].Path
	})

	// Attempt to parse each discovered file to count jobs
	for idx := range discovered {
		jobs, detected := countJobs(discovered[idx].Path)
		discovered[idx].Jobs = jobs
		discovered[idx].Detected = detected
	}

	result := DiscoverResult{
		Directory: absDir,
		Total:     len(discovered),
		Files:     discovered,
	}

	// Print in the requested format
	switch format {
	case "json":
		return printJSON(result)
	case "yaml":
		return printYAML(result)
	default:
		printTree(result)
	}

	return nil
}

// walkPatterns searches the given root directory for all known CI file patterns.
func walkPatterns(root string) []DiscoveredFile {
	var files []DiscoveredFile
	seen := make(map[string]bool) // deduplicate — a file may match multiple patterns

	for _, pattern := range knownCIFiles {
		var matches []string

		if pattern.FileOnly {
			// Exact file check (much faster than glob for single files)
			fullPath := filepath.Join(root, pattern.Pattern)
			if _, err := os.Stat(fullPath); err == nil {
				matches = []string{fullPath}
			}
		} else {
			// Glob pattern (e.g., .github/workflows/*.yml)
			matches, _ = filepath.Glob(filepath.Join(root, pattern.Pattern))
		}

		for _, match := range matches {
			relPath, err := filepath.Rel(root, match)
			if err != nil {
				relPath = match
			}

			if !seen[relPath] {
				seen[relPath] = true
				files = append(files, DiscoveredFile{
					Path:     relPath,
					Provider: pattern.Provider,
				})
			}
		}
	}

	// If no matches found, check for common variant names.
	if len(files) == 0 {
		extraPatterns := []string{
			"Jenkinsfile.*",    // Jenkinsfile with extension
			".gitlab-ci.yml.*", // Common typos or variants
		}
		for _, pat := range extraPatterns {
			matches, _ := filepath.Glob(filepath.Join(root, pat))
			for _, match := range matches {
				relPath, _ := filepath.Rel(root, match)
				if !seen[relPath] {
					seen[relPath] = true
					files = append(files, DiscoveredFile{
						Path:     relPath,
						Provider: "Unknown",
					})
				}
			}
		}
	}

	return files
}

// countJobs tries to parse the file and returns the number of jobs found.
// If parsing fails, it returns 0 and detected=false.
func countJobs(filePath string) (int, bool) {
	parser := detectParser(filePath)
	pipeline, err := parser.Parse(filePath)
	if err != nil {
		return 0, false
	}
	return len(pipeline.Jobs), true
}

// printJSON outputs the discovery result as indented JSON.
func printJSON(result DiscoverResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// printYAML outputs the discovery result as YAML.
func printYAML(result DiscoverResult) error {
	data, err := yaml.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}
	fmt.Print(string(data))
	return nil
}

// printTree outputs the discovery result in a human-readable tree format.
func printTree(result DiscoverResult) {
	fmt.Println()
	fmt.Printf("CI/CD Configuration Files\n")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Directory: %s\n", result.Directory)
	fmt.Println()

	if result.Total == 0 {
		fmt.Println("No CI/CD configuration files found.")
		return
	}

	for i, f := range result.Files {
		prefix := TreeBranch
		if i == len(result.Files)-1 {
			prefix = TreeEnd
		}

		status := ""
		if f.Detected {
			status = fmt.Sprintf(" (%d jobs)", f.Jobs)
		} else {
			status = " (unable to parse)"
		}

		fmt.Printf("%s %s [%s]%s\n", prefix, f.Path, f.Provider, status)
	}

	fmt.Println()
	fmt.Printf("Total: %d file(s) across %d provider(s)\n", result.Total, countProviders(result.Files))
}

// countProviders returns the number of unique provider names in the list.
func countProviders(files []DiscoveredFile) int {
	seen := make(map[string]bool)
	for _, f := range files {
		seen[f.Provider] = true
	}
	return len(seen)
}
