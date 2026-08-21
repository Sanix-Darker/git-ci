package execution

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxGitHubRuntimeFileBytes = 64 << 10
	maxGitHubRuntimeEntries   = 256
	maxGitHubRuntimeLineBytes = 4 << 10
	maxGitHubStepSummaryBytes = 1 << 20
	maxGitHubStepSummaries    = 20
)

var runtimeEnvironmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type githubCommandFile struct {
	hostPath    string
	runtimePath string
}

type githubRuntimeFiles struct {
	output      githubCommandFile
	environment githubCommandFile
	path        githubCommandFile
	summary     githubCommandFile
}

func prepareGitHubRuntimeFiles(workspace, stepID string, container bool) (githubRuntimeFiles, error) {
	var files githubRuntimeFiles
	prepare := func(kind string, destination *githubCommandFile) error {
		hostPath, runtimePath, err := prepareGitHubCommandFile(workspace, stepID, kind, container)
		if err != nil {
			return err
		}
		*destination = githubCommandFile{hostPath: hostPath, runtimePath: runtimePath}
		return nil
	}
	if err := prepare("output", &files.output); err != nil {
		return githubRuntimeFiles{}, err
	}
	if err := prepare("env", &files.environment); err != nil {
		files.cleanup()
		return githubRuntimeFiles{}, err
	}
	if err := prepare("path", &files.path); err != nil {
		files.cleanup()
		return githubRuntimeFiles{}, err
	}
	if err := prepare("summary", &files.summary); err != nil {
		files.cleanup()
		return githubRuntimeFiles{}, err
	}
	return files, nil
}

func (files githubRuntimeFiles) cleanup() {
	for _, path := range []string{files.output.hostPath, files.environment.hostPath, files.path.hostPath, files.summary.hostPath} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

func (context *runtimeOutputContext) reserveGitHubStepSummary() bool {
	if context.summaryCount >= maxGitHubStepSummaries {
		return false
	}
	context.summaryCount++
	return true
}

func parseGitHubStepSummary(path string) (string, error) {
	contents, err := readGitHubRuntimeFileWithLimit(path, "GITHUB_STEP_SUMMARY", maxGitHubStepSummaryBytes)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(contents), "\r\n", "\n"), nil
}

func (context *runtimeOutputContext) consumeGitHubRuntimeFiles(environmentPath, pathPath string) ([]string, int, error) {
	environment, environmentErr := parseGitHubEnvironmentFile(environmentPath)
	paths, pathErr := parseGitHubPathFile(pathPath)
	if err := errors.Join(environmentErr, pathErr); err != nil {
		return nil, 0, err
	}
	for name, value := range environment {
		context.environment[name] = value
	}
	if len(paths) > 0 {
		seen := make(map[string]struct{}, len(paths)+len(context.paths))
		merged := make([]string, 0, len(paths)+len(context.paths))
		for _, path := range append(append([]string(nil), paths...), context.paths...) {
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			merged = append(merged, path)
		}
		context.paths = merged
	}
	return sortedOutputNames(environment), len(paths), nil
}

func (context *runtimeOutputContext) runtimeEnvironment() map[string]string {
	return copyStringMap(context.environment)
}

func (context *runtimeOutputContext) runtimePaths() []string {
	return append([]string(nil), context.paths...)
}

func prependGitHubPaths(environment []string, paths []string) []string {
	if len(paths) == 0 {
		return environment
	}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if found {
			values[name] = value
		}
	}
	prefix := strings.Join(paths, string(os.PathListSeparator))
	if current := values["PATH"]; current != "" {
		prefix += string(os.PathListSeparator) + current
	}
	values["PATH"] = prefix
	return environmentList(values)
}

func parseGitHubEnvironmentFile(path string) (map[string]string, error) {
	contents, err := readGitHubRuntimeFile(path, "GITHUB_ENV")
	if err != nil {
		return nil, err
	}
	lines := runtimeFileLines(contents)
	values := make(map[string]string)
	entries := 0
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		if line == "" {
			continue
		}
		if len(line) > maxGitHubRuntimeLineBytes {
			return nil, fmt.Errorf("execution: GITHUB_ENV command exceeds %d bytes", maxGitHubRuntimeLineBytes)
		}
		entries++
		if entries > maxGitHubRuntimeEntries {
			return nil, fmt.Errorf("execution: GITHUB_ENV exceeds %d entries", maxGitHubRuntimeEntries)
		}
		if marker := strings.Index(line, "<<"); marker > 0 && !strings.Contains(line[:marker], "=") {
			name, delimiter := line[:marker], line[marker+2:]
			if err := validateRuntimeEnvironmentName(name); err != nil || delimiter == "" {
				return nil, fmt.Errorf("execution: invalid GITHUB_ENV command %q", line)
			}
			start := index + 1
			for index = start; index < len(lines) && lines[index] != delimiter; index++ {
			}
			if index >= len(lines) {
				return nil, fmt.Errorf("execution: unterminated GITHUB_ENV value %q", name)
			}
			values[name] = strings.Join(lines[start:index], "\n")
			continue
		}
		name, value, found := strings.Cut(line, "=")
		if !found || validateRuntimeEnvironmentName(name) != nil {
			return nil, fmt.Errorf("execution: invalid GITHUB_ENV command %q", line)
		}
		values[name] = value
	}
	return values, nil
}

func validateRuntimeEnvironmentName(name string) error {
	if !runtimeEnvironmentNamePattern.MatchString(name) {
		return errors.New("invalid environment name")
	}
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "GITHUB_") || strings.HasPrefix(upper, "RUNNER_") || upper == "NODE_OPTIONS" {
		return errors.New("protected environment name")
	}
	return nil
}

func parseGitHubPathFile(path string) ([]string, error) {
	contents, err := readGitHubRuntimeFile(path, "GITHUB_PATH")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, line := range runtimeFileLines(contents) {
		if line == "" {
			continue
		}
		if len(line) > maxGitHubRuntimeLineBytes {
			return nil, fmt.Errorf("execution: GITHUB_PATH entry exceeds %d bytes", maxGitHubRuntimeLineBytes)
		}
		if _, exists := seen[line]; exists {
			continue
		}
		if len(paths) >= maxGitHubRuntimeEntries {
			return nil, fmt.Errorf("execution: GITHUB_PATH exceeds %d entries", maxGitHubRuntimeEntries)
		}
		seen[line] = struct{}{}
		paths = append(paths, line)
	}
	return paths, nil
}

func readGitHubRuntimeFile(path, label string) ([]byte, error) {
	return readGitHubRuntimeFileWithLimit(path, label, maxGitHubRuntimeFileBytes)
}

func readGitHubRuntimeFileWithLimit(path, label string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("execution: %s must remain a regular non-symlink file", label)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("execution: %s exceeds %d bytes", label, limit)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(contents) || strings.IndexByte(string(contents), 0) >= 0 {
		return nil, fmt.Errorf("execution: %s must be valid UTF-8 without null bytes", label)
	}
	return contents, nil
}

func runtimeFileLines(contents []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	return lines
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
