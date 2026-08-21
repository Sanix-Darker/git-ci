package execution

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sanix-darker/git-ci/pkg/types"
)

const (
	maxGitLabDotenvBytes     = 5 << 10
	maxGitLabDotenvVariables = 20
)

var gitLabDotenvName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func loadGitLabDotenvReport(workspace string, config *types.ArtifactConfig) (map[string]string, bool, error) {
	if config == nil {
		return nil, false, nil
	}
	configured := ""
	for kind, value := range config.Reports {
		if strings.EqualFold(kind, "dotenv") {
			configured = strings.TrimSpace(value)
			break
		}
	}
	if configured == "" {
		return nil, false, nil
	}
	paths := actionPathList(configured)
	if len(paths) != 1 {
		return nil, true, errors.New("execution: GitLab dotenv report requires exactly one file")
	}
	path, safe, err := safeExistingPath(workspace, paths[0], false)
	if err != nil {
		return nil, true, err
	}
	if !safe {
		return nil, true, fmt.Errorf("execution: GitLab dotenv report %q is not a regular non-symlink workspace file", paths[0])
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, true, err
	}
	if info.Size() > maxGitLabDotenvBytes {
		return nil, true, fmt.Errorf("execution: GitLab dotenv report exceeds %d bytes", maxGitLabDotenvBytes)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	variables, err := parseGitLabDotenv(contents)
	return variables, true, err
}

func parseGitLabDotenv(contents []byte) (map[string]string, error) {
	if len(contents) > maxGitLabDotenvBytes {
		return nil, fmt.Errorf("execution: GitLab dotenv report exceeds %d bytes", maxGitLabDotenvBytes)
	}
	if !utf8.Valid(contents) || strings.IndexByte(string(contents), 0) >= 0 {
		return nil, errors.New("execution: GitLab dotenv report must be valid UTF-8 without null bytes")
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(contents), "\r\n", "\n"))
	if text == "" {
		return map[string]string{}, nil
	}
	variables := make(map[string]string)
	for index, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			return nil, fmt.Errorf("execution: GitLab dotenv line %d cannot be empty or a comment", index+1)
		}
		name, value, found := strings.Cut(line, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !found || !gitLabDotenvName.MatchString(name) {
			return nil, fmt.Errorf("execution: invalid GitLab dotenv variable on line %d", index+1)
		}
		variables[name] = value
		if len(variables) > maxGitLabDotenvVariables {
			return nil, fmt.Errorf("execution: GitLab dotenv report exceeds %d inherited variables", maxGitLabDotenvVariables)
		}
	}
	return variables, nil
}
