package execution

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxArchiveFiles int64 = 20_000
	maxArchiveBytes int64 = 500 << 20
)

var errArchiveNoFiles = errors.New("execution: archive pattern matched no files")

type archiveManager struct {
	root string
}

type archiveObject struct {
	Key, SHA256 string
	SizeBytes   int64
	FileCount   int
}

type archiveInput struct {
	absolute, relative string
	info               fs.FileInfo
}

func newArchiveManager(root string) (*archiveManager, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = filepath.Join(os.TempDir(), fmt.Sprintf("git-ci-data-%d", os.Getpid()))
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("execution: resolve data root: %w", err)
	}
	for _, directory := range []string{absolute, filepath.Join(absolute, "tmp"), filepath.Join(absolute, "artifacts"), filepath.Join(absolute, "cache")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("execution: create archive directory: %w", err)
		}
	}
	return &archiveManager{root: filepath.Clean(absolute)}, nil
}

func (m *archiveManager) CreateZip(workspace string, includes, excludes []string) (archiveObject, error) {
	files, err := collectArchiveFiles(workspace, includes, excludes)
	if err != nil {
		return archiveObject{}, err
	}
	temporary, err := os.CreateTemp(filepath.Join(m.root, "tmp"), "artifact-*.zip")
	if err != nil {
		return archiveObject{}, err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	writer := zip.NewWriter(io.MultiWriter(temporary, hash))
	for _, input := range files {
		header, err := zip.FileInfoHeader(input.info)
		if err != nil {
			return archiveObject{}, err
		}
		header.Name = input.relative
		header.Method = zip.Deflate
		header.SetMode(input.info.Mode().Perm())
		output, err := writer.CreateHeader(header)
		if err != nil {
			return archiveObject{}, err
		}
		if err := copyArchiveFile(output, input); err != nil {
			return archiveObject{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return archiveObject{}, err
	}
	if err := temporary.Sync(); err != nil {
		return archiveObject{}, err
	}
	if err := temporary.Close(); err != nil {
		return archiveObject{}, err
	}
	info, err := os.Stat(temporaryPath)
	if err != nil {
		return archiveObject{}, err
	}
	key, finalPath, err := m.nextObjectPath("artifacts", ".zip")
	if err != nil {
		return archiveObject{}, err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return archiveObject{}, err
	}
	keep = true
	return archiveObject{Key: key, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: info.Size(), FileCount: len(files)}, nil
}

func (m *archiveManager) CreateTarGz(workspace string, includes, excludes []string) (archiveObject, error) {
	files, err := collectArchiveFiles(workspace, includes, excludes)
	if err != nil {
		return archiveObject{}, err
	}
	temporary, err := os.CreateTemp(filepath.Join(m.root, "tmp"), "cache-*.tar.gz")
	if err != nil {
		return archiveObject{}, err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	compressed := gzip.NewWriter(io.MultiWriter(temporary, hash))
	writer := tar.NewWriter(compressed)
	for _, input := range files {
		header, err := tar.FileInfoHeader(input.info, "")
		if err != nil {
			return archiveObject{}, err
		}
		header.Name = input.relative
		header.Mode = int64(input.info.Mode().Perm())
		if err := writer.WriteHeader(header); err != nil {
			return archiveObject{}, err
		}
		if err := copyArchiveFile(writer, input); err != nil {
			return archiveObject{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return archiveObject{}, err
	}
	if err := compressed.Close(); err != nil {
		return archiveObject{}, err
	}
	if err := temporary.Sync(); err != nil {
		return archiveObject{}, err
	}
	if err := temporary.Close(); err != nil {
		return archiveObject{}, err
	}
	info, err := os.Stat(temporaryPath)
	if err != nil {
		return archiveObject{}, err
	}
	key, finalPath, err := m.nextObjectPath("cache", ".tar.gz")
	if err != nil {
		return archiveObject{}, err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return archiveObject{}, err
	}
	keep = true
	return archiveObject{Key: key, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: info.Size(), FileCount: len(files)}, nil
}

func (m *archiveManager) ExtractZip(key, destination string) error {
	file, err := m.openObject(key, "artifacts")
	if err != nil {
		return err
	}
	filename := file.Name()
	_ = file.Close()
	reader, err := zip.OpenReader(filename)
	if err != nil {
		return fmt.Errorf("execution: open artifact ZIP: %w", err)
	}
	defer reader.Close()
	if int64(len(reader.File)) > maxArchiveFiles {
		return fmt.Errorf("execution: archive exceeds %d files", maxArchiveFiles)
	}
	var total int64
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		total += int64(entry.UncompressedSize64)
		if total > maxArchiveBytes {
			return fmt.Errorf("execution: archive exceeds %d bytes", maxArchiveBytes)
		}
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("execution: artifact entry %q is not a regular file", entry.Name)
		}
		target, err := safeArchiveTarget(destination, entry.Name)
		if err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		if err := writeExtractedFile(destination, target, entry.Mode().Perm(), source, int64(entry.UncompressedSize64)); err != nil {
			_ = source.Close()
			return err
		}
		if err := source.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (m *archiveManager) ExtractTarGz(key, destination string) error {
	file, err := m.openObject(key, "cache")
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("execution: open cache archive: %w", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var count, total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		count++
		total += header.Size
		if count > maxArchiveFiles || total > maxArchiveBytes {
			return errors.New("execution: cache archive exceeds extraction limits")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("execution: cache entry %q is not a regular file", header.Name)
		}
		target, err := safeArchiveTarget(destination, header.Name)
		if err != nil {
			return err
		}
		if err := writeExtractedFile(destination, target, fs.FileMode(header.Mode).Perm(), reader, header.Size); err != nil {
			return err
		}
	}
}

func (m *archiveManager) OpenArtifact(key string) (*os.File, error) {
	return m.openObject(key, "artifacts")
}

func (m *archiveManager) Remove(key string) {
	if target, err := m.objectPath(key); err == nil {
		_ = os.Remove(target)
	}
}

func (m *archiveManager) nextObjectPath(directory, suffix string) (string, string, error) {
	id, err := randomArchiveID()
	if err != nil {
		return "", "", err
	}
	key := path.Join(directory, id+suffix)
	target, err := m.objectPath(key)
	return key, target, err
}

func randomArchiveID() (string, error) {
	var value [20]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (m *archiveManager) openObject(key, expectedDirectory string) (*os.File, error) {
	clean := path.Clean(strings.TrimSpace(key))
	if !strings.HasPrefix(clean, expectedDirectory+"/") {
		return nil, errors.New("execution: invalid archive storage key")
	}
	target, err := m.objectPath(clean)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("execution: archive body is not a regular file")
	}
	return file, nil
}

func (m *archiveManager) objectPath(key string) (string, error) {
	clean := path.Clean(strings.TrimSpace(key))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("execution: invalid archive storage key")
	}
	target := filepath.Join(m.root, filepath.FromSlash(clean))
	if !pathWithin(m.root, target) {
		return "", errors.New("execution: archive storage key escapes data root")
	}
	return target, nil
}

func collectArchiveFiles(workspace string, includes, excludes []string) ([]archiveInput, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	included, excluded, err := normalizeArchivePatterns(root, includes, excludes)
	if err != nil {
		return nil, err
	}
	if len(included) == 0 {
		return nil, invalidArchivePattern("at least one path is required")
	}
	var files []archiveInput
	var total int64
	err = filepath.WalkDir(root, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if candidate == root {
			return nil
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if !matchesArchivePatterns(name, included) || matchesArchivePatterns(name, excluded) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("execution: archive path %q is not a regular file", name)
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !pathWithin(root, resolved) {
			return fmt.Errorf("execution: archive path %q escapes workspace", name)
		}
		total += info.Size()
		if int64(len(files)+1) > maxArchiveFiles || total > maxArchiveBytes {
			return errors.New("execution: archive exceeds file or byte limit")
		}
		files = append(files, archiveInput{absolute: candidate, relative: name, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errArchiveNoFiles
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return files, nil
}

func normalizeArchivePatterns(root string, includes, excludes []string) ([]string, []string, error) {
	var normalizedIncludes, normalizedExcludes []string
	for _, raw := range includes {
		for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' }) {
			line = strings.TrimSpace(line)
			target := &normalizedIncludes
			if strings.HasPrefix(line, "!") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
				target = &normalizedExcludes
			}
			value, err := normalizeArchivePattern(root, line)
			if err != nil {
				return nil, nil, err
			}
			*target = append(*target, value)
		}
	}
	for _, raw := range excludes {
		for _, line := range strings.FieldsFunc(raw, func(r rune) bool { return r == '\n' || r == '\r' }) {
			value, err := normalizeArchivePattern(root, strings.TrimSpace(line))
			if err != nil {
				return nil, nil, err
			}
			normalizedExcludes = append(normalizedExcludes, value)
		}
	}
	return normalizedIncludes, normalizedExcludes, nil
}

func normalizeArchivePattern(root, value string) (string, error) {
	if value == "" {
		return "", invalidArchivePattern("path must not be empty")
	}
	if filepath.IsAbs(value) {
		relative, err := filepath.Rel(root, filepath.Clean(value))
		if err != nil || !isSafeRelativePath(relative) {
			return "", invalidArchivePattern("absolute path must remain inside workspace")
		}
		value = relative
	}
	value = filepath.ToSlash(filepath.Clean(value))
	value = strings.TrimPrefix(value, "./")
	if value == "." {
		value = "**"
	}
	if path.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../") {
		return "", invalidArchivePattern("path escapes workspace")
	}
	return value, nil
}

func matchesArchivePatterns(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if archivePatternMatches(pattern, name) {
			return true
		}
	}
	return false
}

func archivePatternMatches(pattern, name string) bool {
	if !strings.ContainsAny(pattern, "*?[") {
		return name == pattern || strings.HasPrefix(name, strings.TrimSuffix(pattern, "/")+"/")
	}
	return matchPathSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchPathSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		return matchPathSegments(pattern[1:], name) || (len(name) > 0 && matchPathSegments(pattern, name[1:]))
	}
	if len(name) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], name[0])
	return err == nil && matched && matchPathSegments(pattern[1:], name[1:])
}

func copyArchiveFile(output io.Writer, input archiveInput) error {
	file, err := os.Open(input.absolute)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("execution: archive source %q changed type", input.relative)
	}
	written, err := io.Copy(output, io.LimitReader(file, input.info.Size()+1))
	if err == nil && written != input.info.Size() {
		return fmt.Errorf("execution: archive source %q changed size", input.relative)
	}
	return err
}

func safeArchiveTarget(destination, name string) (string, error) {
	clean := path.Clean(strings.TrimSpace(name))
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("execution: archive entry %q escapes destination", name)
	}
	root, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	if !pathWithin(root, target) {
		return "", fmt.Errorf("execution: archive entry %q escapes destination", name)
	}
	return target, nil
}

func writeExtractedFile(root, target string, mode fs.FileMode, source io.Reader, size int64) error {
	if size < 0 || size > maxArchiveBytes {
		return errors.New("execution: invalid archive entry size")
	}
	if err := secureArchiveParents(root, filepath.Dir(target)); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("execution: archive target %q is a symlink", target)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()|0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if written != size {
		return fmt.Errorf("execution: archive entry size mismatch for %q", target)
	}
	return closeErr
}

func secureArchiveParents(root, directory string) error {
	relative, err := filepath.Rel(root, directory)
	if err != nil || !isSafeRelativePath(relative) {
		return errors.New("execution: archive parent escapes destination")
	}
	current := root
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if info, err := os.Lstat(current); err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("execution: archive parent %q is unsafe", current)
			}
		} else if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func invalidArchivePattern(reason string) error {
	return fmt.Errorf("execution: invalid archive pattern: %s", reason)
}
