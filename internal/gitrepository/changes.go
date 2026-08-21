package gitrepository

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxChangedPathOutput = 8 << 20

type DiffMode uint8

const (
	DiffDirect DiffMode = iota
	DiffMergeBase
)

var objectIDPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func ChangedPaths(ctx context.Context, path, before, after string, mode DiffMode) ([]string, error) {
	path = strings.TrimSpace(path)
	before = strings.TrimSpace(before)
	after = strings.TrimSpace(after)
	if path == "" {
		return nil, errors.New("git repository: path is required")
	}
	if !objectIDPattern.MatchString(before) || !objectIDPattern.MatchString(after) {
		return nil, errors.New("git repository: changed-path revisions must be full object IDs")
	}

	arguments := []string{"-c", "safe.directory=" + path, "-C", path, "diff", "--name-only", "--diff-filter=ACDMRTUXB", "-z"}
	switch mode {
	case DiffDirect:
		arguments = append(arguments, before, after)
	case DiffMergeBase:
		arguments = append(arguments, before+"..."+after)
	default:
		return nil, errors.New("git repository: unsupported diff mode")
	}
	arguments = append(arguments, "--")

	var stdout, stderr cappedBuffer
	stdout.limit = maxChangedPathOutput
	stderr.limit = 64 << 10
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git repository: inspect changed paths: %s", message)
	}
	if stdout.exceeded {
		return nil, fmt.Errorf("git repository: changed-path output exceeds %d bytes", maxChangedPathOutput)
	}

	seen := make(map[string]struct{})
	paths := make([]string, 0)
	for _, raw := range bytes.Split(stdout.Bytes(), []byte{0}) {
		path := filepath.ToSlash(strings.TrimSpace(string(raw)))
		if path == "" {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// ResolveTagCommit validates a bare tag name and resolves the existing local
// tag, including annotated tags, to its full commit object ID. It never mutates
// refs or contacts a remote.
func ResolveTagCommit(ctx context.Context, path, tag string) (string, error) {
	path = strings.TrimSpace(path)
	tag = strings.TrimSpace(tag)
	if path == "" {
		return "", errors.New("git repository: path is required")
	}
	if tag == "" || strings.IndexByte(tag, 0) >= 0 {
		return "", errors.New("git repository: tag is required")
	}
	ref := "refs/tags/" + tag
	if _, err := runGitText(ctx, path, "check-ref-format", ref); err != nil {
		return "", fmt.Errorf("git repository: invalid release tag %q: %w", tag, err)
	}
	resolved, err := runGitText(ctx, path, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("git repository: release tag %q does not resolve to a local commit: %w", tag, err)
	}
	resolved = strings.TrimSpace(resolved)
	if !objectIDPattern.MatchString(resolved) {
		return "", errors.New("git repository: release tag resolved to an invalid object ID")
	}
	return strings.ToLower(resolved), nil
}

func runGitText(ctx context.Context, path string, arguments ...string) (string, error) {
	commandArguments := []string{"-c", "safe.directory=" + path, "-C", path}
	commandArguments = append(commandArguments, arguments...)
	var stdout, stderr cappedBuffer
	stdout.limit = 64 << 10
	stderr.limit = 64 << 10
	command := exec.CommandContext(ctx, "git", commandArguments...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	if stdout.exceeded {
		return "", errors.New("git output exceeded 65536 bytes")
	}
	return stdout.String(), nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return written, nil
	}
	if len(value) > remaining {
		buffer.exceeded = true
		value = value[:remaining]
	}
	_, _ = buffer.Buffer.Write(value)
	return written, nil
}
