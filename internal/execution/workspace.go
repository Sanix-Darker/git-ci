package execution

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sanix-darker/git-ci/internal/store"
)

const workspaceMarkerName = "workspace.json"

var gitObjectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type workspaceManager struct {
	root string
}

type runWorkspace struct {
	SourcePath string
}

type workspaceMarker struct {
	Version    int    `json:"version"`
	RunID      string `json:"runId"`
	ProjectID  string `json:"projectId"`
	SourcePath string `json:"sourcePath"`
	CommitSHA  string `json:"commitSha"`
}

func newWorkspaceManager(root string) (*workspaceManager, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("execution: workspace root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("execution: resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("execution: create workspace root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("execution: inspect workspace root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("execution: workspace root must be a real directory")
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("execution: secure workspace root: %w", err)
	}
	return &workspaceManager{root: filepath.Clean(absolute)}, nil
}

func resolveGitCommit(ctx context.Context, sourcePath, ref, commitSHA string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if !filepath.IsAbs(sourcePath) || filepath.Clean(sourcePath) != sourcePath {
		return "", errors.New("execution: registered checkout path must be absolute and clean")
	}
	inside, err := gitCommandOutput(ctx, sourcePath, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return "", errors.New("execution: registered checkout is not a Git work tree")
	}
	candidate := strings.TrimSpace(commitSHA)
	if candidate == "" {
		candidate = strings.TrimSpace(ref)
	}
	if candidate == "" {
		candidate = "HEAD"
	}
	if strings.HasPrefix(candidate, "-") || strings.ContainsAny(candidate, "\r\n\x00") {
		return "", errors.New("execution: invalid Git commit candidate")
	}
	resolved, err := gitCommandOutput(ctx, sourcePath, "rev-parse", "--verify", candidate+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("execution: resolve Git commit: %w", err)
	}
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if !gitObjectIDPattern.MatchString(resolved) {
		return "", errors.New("execution: Git returned an invalid commit object ID")
	}
	return resolved, nil
}

func gitCommandOutput(ctx context.Context, sourcePath string, arguments ...string) (string, error) {
	commandArguments := append([]string{"-c", "safe.directory=" + sourcePath, "-C", sourcePath}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", errors.New(detail)
	}
	return string(output), nil
}

func (manager *workspaceManager) Acquire(ctx context.Context, run store.Run) (runWorkspace, error) {
	if run.CommitSHA == nil || !gitObjectIDPattern.MatchString(strings.ToLower(strings.TrimSpace(*run.CommitSHA))) {
		return runWorkspace{}, errors.New("execution: run has no valid pinned commit")
	}
	container, err := manager.containerPath(run.ID)
	if err != nil {
		return runWorkspace{}, err
	}
	if _, err := os.Lstat(container); err == nil {
		return manager.openExisting(run, container)
	} else if !errors.Is(err, os.ErrNotExist) {
		return runWorkspace{}, fmt.Errorf("execution: inspect run workspace: %w", err)
	}

	temporary, err := os.MkdirTemp(manager.root, ".gci-tmp-")
	if err != nil {
		return runWorkspace{}, fmt.Errorf("execution: create temporary workspace: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return runWorkspace{}, fmt.Errorf("execution: secure temporary workspace: %w", err)
	}
	source := filepath.Join(temporary, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		return runWorkspace{}, fmt.Errorf("execution: create workspace source: %w", err)
	}
	if err := materializeGitArchive(ctx, run.SourcePath, *run.CommitSHA, source); err != nil {
		return runWorkspace{}, err
	}
	marker := workspaceMarker{
		Version: 1, RunID: run.ID, ProjectID: run.ProjectID,
		SourcePath: run.SourcePath, CommitSHA: strings.ToLower(strings.TrimSpace(*run.CommitSHA)),
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return runWorkspace{}, fmt.Errorf("execution: encode workspace marker: %w", err)
	}
	if err := os.WriteFile(filepath.Join(temporary, workspaceMarkerName), encoded, 0o600); err != nil {
		return runWorkspace{}, fmt.Errorf("execution: write workspace marker: %w", err)
	}
	if err := os.Rename(temporary, container); err != nil {
		return runWorkspace{}, fmt.Errorf("execution: publish run workspace: %w", err)
	}
	return runWorkspace{SourcePath: filepath.Join(container, "source")}, nil
}

func (manager *workspaceManager) openExisting(run store.Run, container string) (runWorkspace, error) {
	info, err := os.Lstat(container)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return runWorkspace{}, errors.New("execution: run workspace is not a real directory")
	}
	encoded, err := os.ReadFile(filepath.Join(container, workspaceMarkerName))
	if err != nil {
		return runWorkspace{}, errors.New("execution: existing run workspace has no provenance marker")
	}
	var marker workspaceMarker
	if err := json.Unmarshal(encoded, &marker); err != nil {
		return runWorkspace{}, errors.New("execution: existing run workspace marker is invalid")
	}
	commit := strings.ToLower(strings.TrimSpace(*run.CommitSHA))
	if marker.Version != 1 || marker.RunID != run.ID || marker.ProjectID != run.ProjectID || marker.SourcePath != run.SourcePath || marker.CommitSHA != commit {
		return runWorkspace{}, errors.New("execution: existing run workspace provenance does not match the run")
	}
	source := filepath.Join(container, "source")
	info, err = os.Lstat(source)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return runWorkspace{}, errors.New("execution: existing run workspace source is invalid")
	}
	return runWorkspace{SourcePath: source}, nil
}

func (manager *workspaceManager) Cleanup(runID string) error {
	container, err := manager.containerPath(runID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(container); err != nil {
		return fmt.Errorf("execution: clean run workspace: %w", err)
	}
	return nil
}

func (manager *workspaceManager) CleanupRecovered(ctx context.Context, database *store.Store) error {
	entries, err := os.ReadDir(manager.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gci-tmp-") {
			if err := os.RemoveAll(filepath.Join(manager.root, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		graph, err := database.GetRunGraph(ctx, entry.Name())
		if err != nil {
			var notFound *store.ErrNotFound
			if errors.As(err, &notFound) {
				if err := manager.Cleanup(entry.Name()); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if isTerminalExecutionStatus(graph.Run.Status) {
			if err := manager.Cleanup(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (manager *workspaceManager) SourcePath(runID string) (string, error) {
	container, err := manager.containerPath(runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(container, "source"), nil
}

func (manager *workspaceManager) containerPath(runID string) (string, error) {
	if runID == "" || runID == "." || runID == ".." || filepath.Base(runID) != runID || strings.ContainsAny(runID, `/\\`) || strings.IndexByte(runID, 0) >= 0 {
		return "", errors.New("execution: invalid run ID for workspace")
	}
	return filepath.Join(manager.root, runID), nil
}

func materializeGitArchive(ctx context.Context, sourcePath, commitSHA, destination string) error {
	archiveContext, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(archiveContext, "git", "-c", "safe.directory="+sourcePath, "-C", sourcePath, "archive", "--format=tar", commitSHA)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("execution: open Git archive: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("execution: start Git archive: %w", err)
	}
	extractErr := extractGitArchive(stdout, destination)
	if extractErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = waitErr.Error()
		}
		return fmt.Errorf("execution: create Git archive: %s", detail)
	}
	return nil
}

func extractGitArchive(reader io.Reader, destination string) error {
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("execution: read Git archive: %w", err)
		}
		cleanName, target, err := archiveTarget(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			if err := ensureArchiveDirectory(destination, target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := ensureArchiveParent(destination, filepath.Dir(target)); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("execution: create archived file %q: %w", cleanName, err)
			}
			_, copyErr := io.Copy(file, archive)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("execution: extract archived file %q: %w", cleanName, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("execution: close archived file %q: %w", cleanName, closeErr)
			}
		case tar.TypeSymlink:
			if err := validateArchiveSymlink(cleanName, header.Linkname); err != nil {
				return err
			}
			if err := ensureArchiveParent(destination, filepath.Dir(target)); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("execution: create archived symlink %q: %w", cleanName, err)
			}
		default:
			return fmt.Errorf("execution: unsupported Git archive entry %q", cleanName)
		}
	}
}

func archiveTarget(root, name string) (string, string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00") {
		return "", "", errors.New("execution: invalid Git archive path")
	}
	clean := path.Clean(name)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", fmt.Errorf("execution: Git archive path %q escapes workspace", name)
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	if !pathWithin(root, target) {
		return "", "", fmt.Errorf("execution: Git archive path %q escapes workspace", name)
	}
	return clean, target, nil
}

func ensureArchiveDirectory(root, target string, mode os.FileMode) error {
	if err := ensureArchiveParent(root, filepath.Dir(target)); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(target, mode&0o777)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("execution: archived directory collides with a non-directory")
	}
	return nil
}

func ensureArchiveParent(root, parent string) error {
	relative, err := filepath.Rel(root, parent)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("execution: Git archive parent escapes workspace")
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("execution: Git archive parent is not a real directory")
		}
	}
	return nil
}

func validateArchiveSymlink(name, link string) error {
	if link == "" || path.IsAbs(link) || strings.ContainsAny(link, "\\\x00") {
		return fmt.Errorf("execution: archived symlink %q has an unsafe target", name)
	}
	resolved := path.Clean(path.Join(path.Dir(name), link))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("execution: archived symlink %q escapes workspace", name)
	}
	return nil
}

func containedWorkingDirectory(root, relative string) (string, error) {
	root = filepath.Clean(root)
	if relative == "" {
		return root, nil
	}
	if filepath.IsAbs(relative) {
		return "", errors.New("execution: working directory must be relative")
	}
	target := filepath.Clean(filepath.Join(root, relative))
	if !pathWithin(root, target) {
		return "", errors.New("execution: working directory escapes run workspace")
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("execution: resolve working directory: %w", err)
	}
	if !pathWithin(root, resolved) {
		return "", errors.New("execution: working directory symlink escapes run workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("execution: working directory is not a directory")
	}
	return resolved, nil
}
