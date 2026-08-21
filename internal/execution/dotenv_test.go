package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sanix-darker/git-ci/internal/store"
	"github.com/sanix-darker/git-ci/pkg/types"
)

func TestParseGitLabDotenvContract(t *testing.T) {
	variables, err := parseGitLabDotenv([]byte("VERSION = 1.0\nQUOTED='literal'\nVERSION=1.1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if variables["VERSION"] != "1.1" || variables["QUOTED"] != "'literal'" {
		t.Fatalf("variables = %#v", variables)
	}
	var tooMany strings.Builder
	for index := 0; index <= maxGitLabDotenvVariables; index++ {
		fmt.Fprintf(&tooMany, "V%d=1\n", index)
	}
	for name, contents := range map[string][]byte{
		"comment":      []byte("# no\n"),
		"empty line":   []byte("A=1\n\nB=2\n"),
		"invalid name": []byte("BAD-NAME=value\n"),
		"too many":     []byte(tooMany.String()),
		"oversized":    []byte("A=" + strings.Repeat("x", maxGitLabDotenvBytes)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitLabDotenv(contents); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestE2EGitLabDotenvInheritanceAndArtifact(t *testing.T) {
	ctx, database, manager, project, root := newManagerTestFixture(t)
	inherited := filepath.Join(t.TempDir(), "inherited")
	blocked := filepath.Join(t.TempDir(), "blocked")
	isolated := filepath.Join(t.TempDir(), "isolated")
	writeManagerWorkflow(t, filepath.Join(root, ".gitlab-ci.yml"), strings.Join([]string{
		"stages: [build, test, release]",
		"build:",
		"  stage: build",
		"  script:",
		"    - printf 'GCI_DOTENV_RELEASE=1.2.3\\nDUP=first\\nDUP=last\\n' > build.env",
		"  artifacts:",
		"    reports:",
		"      dotenv: build.env",
		"consumer:",
		"  stage: test",
		"  variables:",
		"    GCI_DOTENV_RELEASE: job",
		"  script:",
		"    - printf '%s|%s' \"$GCI_DOTENV_RELEASE\" \"$DUP\" > " + shellTestPath(inherited),
		"blocked:",
		"  stage: test",
		"  needs:",
		"    - job: build",
		"      artifacts: false",
		"  script:",
		"    - printf '%s' \"${GCI_DOTENV_RELEASE-unset}\" > " + shellTestPath(blocked),
		"isolated:",
		"  stage: release",
		"  dependencies: []",
		"  script:",
		"    - printf '%s' \"${GCI_DOTENV_RELEASE-unset}\" > " + shellTestPath(isolated),
	}, "\n"))
	workflow := syncManagerWorkflow(t, ctx, manager, project.ID)
	run := enqueueAndProcess(t, ctx, manager, workflow.ID)
	graph, err := database.GetRunGraph(ctx, run.ID)
	if err != nil || graph.Run.Status != store.StatusSucceeded {
		t.Fatalf("run graph = %#v, error = %v", graph, err)
	}
	assertOutputFile(t, inherited, "1.2.3|last")
	assertOutputFile(t, blocked, "unset")
	assertOutputFile(t, isolated, "unset")
	artifacts, err := database.ListRunArtifacts(ctx, run.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
}

func TestLoadGitLabDotenvRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions differ on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.env")
	link := filepath.Join(root, "report.env")
	if err := os.WriteFile(target, []byte("SAFE=no\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadGitLabDotenvReport(root, &types.ArtifactConfig{Reports: map[string]string{"dotenv": "report.env"}})
	if err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("error = %v", err)
	}
}
