// Package projects defines the local filesystem policy for git-ci projects.
package projects

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Registry contains the canonical directories under which local projects may
// be selected. Its roots are immutable after construction.
type Registry struct {
	roots []string
}

// Project is an immediately discovered Git repository. Path is canonical and
// SuggestedSlug is derived only from the directory basename.
type Project struct {
	Path          string `json:"path"`
	SuggestedSlug string `json:"suggestedSlug"`
}

// NewRegistry builds a project registry from configured roots. Every root must
// already exist and be a directory. Roots are made absolute, symlinks are
// resolved, then equal and nested roots are removed. The remaining roots are
// sorted lexicographically so policy decisions do not depend on configuration
// order.
func NewRegistry(configuredRoots []string) (*Registry, error) {
	if len(configuredRoots) == 0 {
		return nil, fmt.Errorf("at least one project root is required")
	}

	canonicalRoots := make([]string, 0, len(configuredRoots))
	for _, root := range configuredRoots {
		if root == "" {
			return nil, fmt.Errorf("project root must not be empty")
		}

		canonical, err := canonicalDirectory(root)
		if err != nil {
			return nil, fmt.Errorf("invalid project root %q: %w", root, err)
		}
		canonicalRoots = append(canonicalRoots, canonical)
	}

	sort.Strings(canonicalRoots)
	roots := make([]string, 0, len(canonicalRoots))
	for _, root := range canonicalRoots {
		contained := false
		for _, existing := range roots {
			if isWithin(existing, root) {
				contained = true
				break
			}
		}
		if !contained {
			roots = append(roots, root)
		}
	}

	return &Registry{roots: roots}, nil
}

// Roots returns a copy of the registry's canonical, non-overlapping roots.
func (r *Registry) Roots() []string {
	if r == nil {
		return nil
	}

	roots := make([]string, len(r.roots))
	copy(roots, r.roots)
	return roots
}

// ValidateLocalPath validates an operator-selected local directory and returns
// its canonical path. Absolute paths must be beneath one configured root.
// Relative paths are evaluated beneath every canonical root in sorted order;
// exactly one existing canonical directory must be valid, otherwise an absent
// or ambiguous selection is rejected. The process working directory is never
// used to resolve a relative selection.
func (r *Registry) ValidateLocalPath(selectedPath string) (string, error) {
	if r == nil || len(r.roots) == 0 {
		return "", fmt.Errorf("project registry has no roots")
	}
	if selectedPath == "" {
		return "", fmt.Errorf("local project path must not be empty")
	}

	if filepath.IsAbs(selectedPath) {
		canonical, err := canonicalDirectory(selectedPath)
		if err != nil {
			return "", fmt.Errorf("invalid local project path %q: %w", selectedPath, err)
		}
		if !r.contains(canonical) {
			return "", fmt.Errorf("local project path %q is outside configured roots", selectedPath)
		}
		return canonical, nil
	}

	matches := make([]string, 0, 1)
	for _, root := range r.roots {
		canonical, err := canonicalDirectory(filepath.Join(root, selectedPath))
		if err != nil || !isWithin(root, canonical) {
			continue
		}
		matches = append(matches, canonical)
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("relative local project path %q does not resolve to a directory beneath a configured root", selectedPath)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("relative local project path %q is ambiguous across configured roots", selectedPath)
	}
}

// Discover finds Git repositories that are immediate children of configured
// roots. It does not walk descendants. Entries whose canonical target escapes
// a root are ignored. Results are sorted by canonical path, then suggested
// slug, and duplicate canonical paths are returned once.
func (r *Registry) Discover() ([]Project, error) {
	if r == nil || len(r.roots) == 0 {
		return nil, fmt.Errorf("project registry has no roots")
	}

	projects := make([]Project, 0)
	seen := make(map[string]struct{})
	for _, root := range r.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("read project root %q: %w", root, err)
		}

		for _, entry := range entries {
			canonical, err := canonicalDirectory(filepath.Join(root, entry.Name()))
			if err != nil || canonical == root || !isWithin(root, canonical) {
				continue
			}
			if !isGitRepository(canonical) {
				continue
			}
			if _, ok := seen[canonical]; ok {
				continue
			}

			seen[canonical] = struct{}{}
			projects = append(projects, Project{
				Path:          canonical,
				SuggestedSlug: SuggestSlug(canonical),
			})
		}
	}

	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Path == projects[j].Path {
			return projects[i].SuggestedSlug < projects[j].SuggestedSlug
		}
		return projects[i].Path < projects[j].Path
	})
	return projects, nil
}

// DiscoverProjects is an explicit name for Discover.
func (r *Registry) DiscoverProjects() ([]Project, error) {
	return r.Discover()
}

// NormalizeSlug converts arbitrary text into lowercase ASCII slug components.
// Runs of unsupported characters become one hyphen, while leading and trailing
// separators are removed. An input with no ASCII letters or digits normalizes
// to the empty string and should be rejected or given a fallback by the caller.
func NormalizeSlug(value string) string {
	var normalized strings.Builder
	pendingHyphen := false

	for i := 0; i < len(value); i++ {
		char := value[i]
		switch {
		case char >= 'A' && char <= 'Z':
			if pendingHyphen && normalized.Len() > 0 {
				normalized.WriteByte('-')
			}
			normalized.WriteByte(char + ('a' - 'A'))
			pendingHyphen = false
		case (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9'):
			if pendingHyphen && normalized.Len() > 0 {
				normalized.WriteByte('-')
			}
			normalized.WriteByte(char)
			pendingHyphen = false
		default:
			if normalized.Len() > 0 {
				pendingHyphen = true
			}
		}
	}

	return normalized.String()
}

// ValidateSlug reports whether slug contains only lowercase ASCII letters,
// digits, and single interior hyphens.
func ValidateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("project slug must not be empty")
	}
	if slug[0] == '-' || slug[len(slug)-1] == '-' {
		return fmt.Errorf("project slug must not start or end with a hyphen")
	}

	previousHyphen := false
	for i := 0; i < len(slug); i++ {
		char := slug[i]
		switch {
		case (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9'):
			previousHyphen = false
		case char == '-':
			if previousHyphen {
				return fmt.Errorf("project slug must not contain consecutive hyphens")
			}
			previousHyphen = true
		default:
			return fmt.Errorf("project slug contains invalid character %q", char)
		}
	}

	return nil
}

// SuggestSlug deterministically derives a valid project slug from path's
// directory basename. It does not inspect existing projects, so suggestions do
// not change when another project with the same basename is discovered.
func SuggestSlug(path string) string {
	slug := NormalizeSlug(filepath.Base(filepath.Clean(path)))
	if slug == "" {
		return "project"
	}
	return slug
}

func (r *Registry) contains(path string) bool {
	for _, root := range r.roots {
		if isWithin(root, path) {
			return true
		}
	}
	return false
}

func canonicalDirectory(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make absolute: %w", err)
	}

	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory")
	}
	return filepath.Clean(canonicalPath), nil
}

func isWithin(root, path string) bool {
	relativePath, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relativePath) {
		return false
	}
	if relativePath == "." {
		return true
	}

	for _, component := range strings.Split(relativePath, string(filepath.Separator)) {
		if component == ".." {
			return false
		}
	}
	return true
}

func isGitRepository(directory string) bool {
	metadata, err := os.Stat(filepath.Join(directory, ".git"))
	if err != nil {
		return false
	}
	return metadata.IsDir() || metadata.Mode().IsRegular()
}
