package projects

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateLocalPathAllowsRootAndChild(t *testing.T) {
	root := t.TempDir()
	child := makeDirectory(t, filepath.Join(root, "child"))
	registry := mustRegistry(t, root)

	for _, selectedPath := range []string{root, ".", child, "child"} {
		got, err := registry.ValidateLocalPath(selectedPath)
		if err != nil {
			t.Fatalf("ValidateLocalPath(%q): %v", selectedPath, err)
		}

		want := root
		if selectedPath == child || selectedPath == "child" {
			want = child
		}
		if got != want {
			t.Fatalf("ValidateLocalPath(%q) = %q, want %q", selectedPath, got, want)
		}
	}
}

func TestValidateLocalPathRejectsTraversal(t *testing.T) {
	parent := t.TempDir()
	root := makeDirectory(t, filepath.Join(parent, "root"))
	outside := makeDirectory(t, filepath.Join(parent, "outside"))
	registry := mustRegistry(t, root)

	for _, selectedPath := range []string{"../outside", outside} {
		if _, err := registry.ValidateLocalPath(selectedPath); err == nil {
			t.Fatalf("ValidateLocalPath(%q) unexpectedly succeeded", selectedPath)
		}
	}
}

func TestValidateLocalPathRejectsPrefixConfusion(t *testing.T) {
	parent := t.TempDir()
	root := makeDirectory(t, filepath.Join(parent, "projects"))
	prefixSibling := makeDirectory(t, filepath.Join(parent, "projects-other"))
	registry := mustRegistry(t, root)

	if _, err := registry.ValidateLocalPath(prefixSibling); err == nil {
		t.Fatalf("ValidateLocalPath(%q) unexpectedly succeeded", prefixSibling)
	}
}

func TestValidateLocalPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	registry := mustRegistry(t, root)

	if _, err := registry.ValidateLocalPath("escape"); err == nil {
		t.Fatal("ValidateLocalPath accepted a symlink escape")
	}
}

func TestValidateLocalPathRejectsMissingAndFilePaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "project-file")
	writeFile(t, file, "not a directory")
	registry := mustRegistry(t, root)

	for _, selectedPath := range []string{filepath.Join(root, "missing"), file, "missing", "project-file"} {
		if _, err := registry.ValidateLocalPath(selectedPath); err == nil {
			t.Fatalf("ValidateLocalPath(%q) unexpectedly succeeded", selectedPath)
		}
	}
}

func TestNewRegistryDeduplicatesNestedAndEqualRoots(t *testing.T) {
	root := t.TempDir()
	nested := makeDirectory(t, filepath.Join(root, "nested"))
	registry := mustRegistry(t, nested, root, root)

	if got, want := registry.Roots(), []string{root}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Roots() = %#v, want %#v", got, want)
	}
}

func TestValidateLocalPathRejectsRelativeAmbiguity(t *testing.T) {
	parent := t.TempDir()
	rootA := makeDirectory(t, filepath.Join(parent, "a"))
	rootB := makeDirectory(t, filepath.Join(parent, "b"))
	makeDirectory(t, filepath.Join(rootA, "checkout"))
	makeDirectory(t, filepath.Join(rootB, "checkout"))
	registry := mustRegistry(t, rootB, rootA)

	if _, err := registry.ValidateLocalPath("checkout"); err == nil {
		t.Fatal("ValidateLocalPath accepted an ambiguous relative path")
	}
}

func TestDiscoverReturnsSortedImmediateRepositories(t *testing.T) {
	root := t.TempDir()
	alpha := makeGitDirectory(t, filepath.Join(root, "alpha"))
	zeta := makeGitDirectory(t, filepath.Join(root, "zeta"))
	makeGitDirectory(t, filepath.Join(alpha, "nested"))
	makeDirectory(t, filepath.Join(root, "not-a-repository"))
	registry := mustRegistry(t, root)

	got, err := registry.Discover()
	if err != nil {
		t.Fatalf("Discover(): %v", err)
	}
	want := []Project{
		{Path: alpha, SuggestedSlug: "alpha"},
		{Path: zeta, SuggestedSlug: "zeta"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %#v, want %#v", got, want)
	}
}

func TestDiscoverRecognizesGitFileWorktree(t *testing.T) {
	root := t.TempDir()
	worktree := makeDirectory(t, filepath.Join(root, "linked-worktree"))
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: ../repository/.git/worktrees/linked-worktree\n")
	registry := mustRegistry(t, root)

	got, err := registry.DiscoverProjects()
	if err != nil {
		t.Fatalf("DiscoverProjects(): %v", err)
	}
	want := []Project{{Path: worktree, SuggestedSlug: "linked-worktree"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverProjects() = %#v, want %#v", got, want)
	}
}

func TestSlugNormalizationValidationAndSuggestion(t *testing.T) {
	normalizationCases := map[string]string{
		"My Project__2026":   "my-project-2026",
		"--Already--Clean--": "already-clean",
		"caf\u00e9":          "caf",
		"!!!":                "",
	}
	for input, want := range normalizationCases {
		if got := NormalizeSlug(input); got != want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", input, got, want)
		}
	}

	for _, slug := range []string{"a", "a-1", "project-2026"} {
		if err := ValidateSlug(slug); err != nil {
			t.Errorf("ValidateSlug(%q): %v", slug, err)
		}
	}
	for _, slug := range []string{"", "-project", "project-", "project--copy", "Project", "project_name", "caf\u00e9"} {
		if err := ValidateSlug(slug); err == nil {
			t.Errorf("ValidateSlug(%q) unexpectedly succeeded", slug)
		}
	}

	if got, want := SuggestSlug(filepath.Join("/tmp", "My Project")), "my-project"; got != want {
		t.Errorf("SuggestSlug() = %q, want %q", got, want)
	}
	if got, want := SuggestSlug("---"), "project"; got != want {
		t.Errorf("SuggestSlug() = %q, want %q", got, want)
	}
	if first, second := SuggestSlug(filepath.Join("/one", "Same Name")), SuggestSlug(filepath.Join("/two", "Same Name")); first != second {
		t.Errorf("suggestions for equal basenames differ: %q and %q", first, second)
	}
}

func mustRegistry(t *testing.T, roots ...string) *Registry {
	t.Helper()
	registry, err := NewRegistry(roots)
	if err != nil {
		t.Fatalf("NewRegistry(%#v): %v", roots, err)
	}
	return registry
}

func makeDirectory(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
	return path
}

func makeGitDirectory(t *testing.T, path string) string {
	t.Helper()
	makeDirectory(t, filepath.Join(path, ".git"))
	return path
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
