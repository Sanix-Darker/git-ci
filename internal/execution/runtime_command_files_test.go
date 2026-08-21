package execution

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitHubRuntimeFilesParseEnvironmentAndPathContracts(t *testing.T) {
	root := t.TempDir()
	environmentPath := filepath.Join(root, "env")
	pathPath := filepath.Join(root, "path")
	writeRuntimeFile(t, environmentPath, "VERSION=1\nNOTES<<END\nline one\nline two\nEND\nVERSION=2\n")
	writeRuntimeFile(t, pathPath, "/tools/one\n/tools/two\n/tools/one\n")
	environment, err := parseGitHubEnvironmentFile(environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if environment["VERSION"] != "2" || environment["NOTES"] != "line one\nline two" {
		t.Fatalf("environment = %#v", environment)
	}
	paths, err := parseGitHubPathFile(pathPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, "|") != "/tools/one|/tools/two" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestGitHubRuntimeFilesRejectUnsafeEnvironmentAndFiles(t *testing.T) {
	for _, command := range []string{"bad name=value\n", "GITHUB_TOKEN=value\n", "runner_temp=value\n", "NODE_OPTIONS=value\n", "VALUE<<END\nunterminated\n"} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "env")
			writeRuntimeFile(t, path, command)
			if _, err := parseGitHubEnvironmentFile(path); err == nil {
				t.Fatalf("command %q was accepted", command)
			}
		})
	}
	t.Run("entry limit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "env")
		var contents strings.Builder
		for index := 0; index <= maxGitHubRuntimeEntries; index++ {
			_, _ = contents.WriteString("VALUE_" + strings.Repeat("X", index%3) + "=item\n")
		}
		writeRuntimeFile(t, path, contents.String())
		if _, err := parseGitHubEnvironmentFile(path); err == nil || !strings.Contains(err.Error(), "entries") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "path")
		writeRuntimeFile(t, path, strings.Repeat("x", maxGitHubRuntimeFileBytes+1))
		if _, err := parseGitHubPathFile(path); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("invalid UTF-8 and null", func(t *testing.T) {
		for _, contents := range [][]byte{{0xff}, {'a', 0, 'b'}} {
			path := filepath.Join(t.TempDir(), "path")
			if err := os.WriteFile(path, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := parseGitHubPathFile(path); err == nil || !strings.Contains(err.Error(), "UTF-8") {
				t.Fatalf("error = %v", err)
			}
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink permissions differ on Windows")
		}
		root := t.TempDir()
		target, link := filepath.Join(root, "target"), filepath.Join(root, "path")
		writeRuntimeFile(t, target, "/unsafe\n")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := parseGitHubPathFile(link); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPrepareGitHubRuntimeFilesMapsContainedContainerPaths(t *testing.T) {
	root := t.TempDir()
	files, err := prepareGitHubRuntimeFiles(root, "step-id", true)
	if err != nil {
		t.Fatal(err)
	}
	defer files.cleanup()
	seen := make(map[string]struct{})
	for _, file := range []githubCommandFile{files.output, files.environment, files.path} {
		if !strings.HasPrefix(file.hostPath, filepath.Join(root, ".gci", "command-files")+string(filepath.Separator)) {
			t.Fatalf("host path escapes workspace: %q", file.hostPath)
		}
		if !strings.HasPrefix(file.runtimePath, "/workspace/.gci/command-files/") {
			t.Fatalf("runtime path = %q", file.runtimePath)
		}
		if _, duplicate := seen[file.hostPath]; duplicate {
			t.Fatalf("duplicate command path %q", file.hostPath)
		}
		seen[file.hostPath] = struct{}{}
	}
}

func writeRuntimeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
