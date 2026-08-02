package handlers

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	cli "github.com/urfave/cli/v2"
)

func captureStdoutClean(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	data, _ := io.ReadAll(r)
	_ = r.Close()
	return string(data)
}

func newCleanCtx(t *testing.T, args ...string) *cli.Context {
	t.Helper()

	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.Bool("all", false, "")
	fs.Bool("containers", false, "")
	fs.Bool("images", false, "")
	fs.Bool("cache", false, "")
	fs.Bool("force", false, "")

	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse args %v: %v", args, err)
	}
	return cli.NewContext(nil, fs, nil)
}

func TestCmdClean_NoTargetsPrintsGuide(t *testing.T) {
	ctx := newCleanCtx(t, "--all=false", "--cache=false", "--containers=false", "--images=false")

	out := captureStdoutClean(t, func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if strings.TrimSpace(out) != "Nothing to clean. Use --all or specify what to clean." {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestCmdClean_CacheOnlyRemovesKnownDirectories(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Chdir(root)

	cacheDirs := []string{
		".git-ci-cache",
		".git-ci",
		filepath.Join("tmp", "git-ci"),
		filepath.Join(home, ".cache", "git-ci"),
		filepath.Join(home, ".git-ci"),
	}

	for _, dir := range cacheDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	ctx := newCleanCtx(t, "--cache")
	out := captureStdoutClean(t, func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if !strings.Contains(out, "Cleaning up resources...") {
		t.Fatalf("expected cache clean banner, got:\n%s", out)
	}
	if !strings.Contains(out, "Removed 5 cache director(ies)") {
		t.Fatalf("expected cache removal count, got:\n%s", out)
	}

	for _, dir := range cacheDirs {
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("expected %s removed, got exists", dir)
		}
	}
}

type fakeCleanDockerClient struct {
	labelContainers []container.Summary
	allContainers   []container.Summary
	images          []image.Summary
	failLabelList   bool

	containerStopCalls   []string
	containerRemoveCalls []string
	imageRemoveCalls     []string
	imagesPruned         bool
	closed               bool
}

func (f *fakeCleanDockerClient) ContainerList(_ context.Context, options container.ListOptions) ([]container.Summary, error) {
	if len(options.Filters.Get("label")) > 0 {
		if f.failLabelList {
			return nil, assertError{"simulated label query failure"}
		}
		return f.labelContainers, nil
	}
	return f.allContainers, nil
}

func (f *fakeCleanDockerClient) ContainerStop(_ context.Context, containerID string, _ container.StopOptions) error {
	f.containerStopCalls = append(f.containerStopCalls, containerID)
	return nil
}

func (f *fakeCleanDockerClient) ContainerRemove(_ context.Context, containerID string, _ container.RemoveOptions) error {
	f.containerRemoveCalls = append(f.containerRemoveCalls, containerID)
	return nil
}

func (f *fakeCleanDockerClient) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	return f.images, nil
}

func (f *fakeCleanDockerClient) ImageRemove(_ context.Context, imageID string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.imageRemoveCalls = append(f.imageRemoveCalls, imageID)
	return nil, nil
}

func (f *fakeCleanDockerClient) ImagesPrune(_ context.Context, _ filters.Args) (image.PruneReport, error) {
	f.imagesPruned = true
	return image.PruneReport{
		ImagesDeleted: []image.DeleteResponse{
			{Deleted: "img1"},
			{Deleted: "img2"},
		},
	}, nil
}

func (f *fakeCleanDockerClient) Close() error {
	f.closed = true
	return nil
}

type assertError struct {
	msg string
}

func (e assertError) Error() string { return e.msg }

func withStdoutAndStdin(t *testing.T, input string, fn func()) string {
	t.Helper()

	origIn := os.Stdin
	origOut := os.Stdout

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdin: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		t.Fatalf("pipe stdout: %v", err)
	}

	os.Stdin = inR
	os.Stdout = outW
	defer func() {
		os.Stdin = origIn
		os.Stdout = origOut
	}()

	if _, err := inW.WriteString(input); err != nil {
		t.Fatalf("write stdin input: %v", err)
	}
	_ = inW.Close()

	fn()

	_ = outW.Close()
	output, _ := io.ReadAll(outR)
	_ = outR.Close()

	return string(output)
}

func TestClean_AllUsesDockerAndCachePaths(t *testing.T) {
	fake := &fakeCleanDockerClient{
		labelContainers: []container.Summary{
			{
				ID:    "container-keep-1",
				Names: []string{"/git-ci-test-container"},
				State: "exited",
			},
		},
		allContainers: nil,
		images: []image.Summary{
			{ID: "sha256:image-1", RepoTags: []string{"ghcr.io/example/git-ci:latest"}},
			{ID: "sha256:image-2", RepoTags: []string{"other:tag"}},
		},
	}

	orig := newCleanDockerClient
	newCleanDockerClient = func() (cleanDockerClient, error) {
		return fake, nil
	}
	t.Cleanup(func() {
		newCleanDockerClient = orig
	})

	ctx := newCleanCtx(t, "--all", "--force")
	out := captureStdoutClean(t, func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if !strings.Contains(out, "Cleaning up resources...") {
		t.Fatalf("expected clean banner, got:\n%s", out)
	}
	if !strings.Contains(out, "✓ Cleanup completed") {
		t.Fatalf("expected successful completion, got:\n%s", out)
	}
	if len(fake.containerRemoveCalls) != 1 {
		t.Fatalf("expected 1 container removed, got %d", len(fake.containerRemoveCalls))
	}
	if len(fake.imageRemoveCalls) != 1 {
		t.Fatalf("expected 1 image removed, got %d", len(fake.imageRemoveCalls))
	}
	if !fake.closed {
		t.Fatalf("expected docker client to be closed")
	}
}

func TestClean_ContainersFallsBackToNamePrefixFilter_WhenLabelQueryFails(t *testing.T) {
	fake := &fakeCleanDockerClient{
		failLabelList: true,
		allContainers: []container.Summary{
			{ID: "container-1", Names: []string{"/git-ci-test-container"}, State: "exited"},
			{ID: "container-2", Names: []string{"/not-matched-container"}, State: "exited"},
		},
	}

	orig := newCleanDockerClient
	newCleanDockerClient = func() (cleanDockerClient, error) {
		return fake, nil
	}
	t.Cleanup(func() { newCleanDockerClient = orig })

	ctx := newCleanCtx(t, "--containers", "--force")
	out := captureStdoutClean(t, func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if !strings.Contains(out, "Removed 1 container(s)") {
		t.Fatalf("expected only prefixed container to be removed, got:\n%s", out)
	}
	if len(fake.containerRemoveCalls) != 1 {
		t.Fatalf("expected 1 container removal, got %d", len(fake.containerRemoveCalls))
	}
	if fake.containerRemoveCalls[0] != "container-1" {
		t.Fatalf("expected git-ci container to be removed, got %+v", fake.containerRemoveCalls)
	}
}

func TestClean_InteractiveContainerPromptHonorsUserNo(t *testing.T) {
	fake := &fakeCleanDockerClient{
		failLabelList: false,
		labelContainers: []container.Summary{
			{ID: "container-1", Names: []string{"/git-ci-container"}, State: "exited"},
		},
	}

	orig := newCleanDockerClient
	newCleanDockerClient = func() (cleanDockerClient, error) {
		return fake, nil
	}
	t.Cleanup(func() { newCleanDockerClient = orig })

	ctx := newCleanCtx(t, "--containers")
	out := withStdoutAndStdin(t, "n\n", func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if !strings.Contains(out, "Remove container git-ci-container?") {
		t.Fatalf("expected interactive prompt, got:\n%s", out)
	}
	if len(fake.containerRemoveCalls) != 0 {
		t.Fatalf("expected user decline to skip removal, got %d removals", len(fake.containerRemoveCalls))
	}
}

func TestClean_InteractiveContainerPromptHonorsUserYes(t *testing.T) {
	fake := &fakeCleanDockerClient{
		failLabelList: false,
		labelContainers: []container.Summary{
			{ID: "container-1", Names: []string{"/git-ci-container"}, State: "exited"},
		},
	}

	orig := newCleanDockerClient
	newCleanDockerClient = func() (cleanDockerClient, error) {
		return fake, nil
	}
	t.Cleanup(func() { newCleanDockerClient = orig })

	ctx := newCleanCtx(t, "--containers")
	out := withStdoutAndStdin(t, "y\n", func() {
		if err := CmdClean(ctx); err != nil {
			t.Fatalf("CmdClean: %v", err)
		}
	})

	if !strings.Contains(out, "Removed 1 container(s)") {
		t.Fatalf("expected container to be removed, got:\n%s", out)
	}
	if len(fake.containerRemoveCalls) != 1 {
		t.Fatalf("expected 1 removal, got %d", len(fake.containerRemoveCalls))
	}
}
