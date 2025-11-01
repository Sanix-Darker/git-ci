package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sanix-darker/git-ci/internal/parsers"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func parseInput(workflowFile string) (*types.Pipeline, error) {
	// Auto-detect parser based on file path
	var parser types.Parser

	if workflowFile == "" {
		// Try to auto-detect workflow file
		if _, err := os.Stat(".github/workflows/ci.yml"); err == nil {
			workflowFile = ".github/workflows/ci.yml"
			parser = parsers.NewGithubParser()
		} else if _, err := os.Stat(".gitlab-ci.yml"); err == nil {
			workflowFile = ".gitlab-ci.yml"
			parser = parsers.NewGitlabParser()
		} else {
            // TODO: handler the rest of future parsers
			return nil, fmt.Errorf("no CI configuration file found")
		}
	} else {
		// Detect parser from file path
		dir := filepath.Dir(workflowFile)
		base := filepath.Base(workflowFile)

		if strings.Contains(dir, ".github/workflows") || strings.Contains(base, "github") {
			parser = parsers.NewGithubParser()
		} else if strings.Contains(base, "gitlab") || base == ".gitlab-ci.yml" || base == ".gitlab-ci.yaml" {
			parser = parsers.NewGitlabParser()
		} else {
			// Default to GitHub parser
			parser = parsers.NewGithubParser()
		}
	}

	pipeline, err := parser.Parse(workflowFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse workflow: %w", err)
	}

	return pipeline, nil
}
