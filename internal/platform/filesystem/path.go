package securefs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalPathWithinRoot resolves root and target and returns the canonical
// target together with its project-relative path. Both paths must resolve to
// the same filesystem boundary; callers can then use the relative value with
// an os.Root descriptor for race-resistant access.
func CanonicalPathWithinRoot(root, target string) (canonical, relative string, err error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(target) == "" {
		return "", "", fmt.Errorf("filesystem path is required")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve filesystem root: %w", err)
	}
	rootAbs, err = filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve filesystem root: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve filesystem path: %w", err)
	}
	canonical, err = filepath.EvalSymlinks(targetAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve filesystem path: %w", err)
	}
	relative, err = filepath.Rel(rootAbs, canonical)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path resolves outside filesystem root")
	}
	return canonical, relative, nil
}

// ReadCanonicalFile reads an existing canonical path through an os.Root
// descriptor. The descriptor prevents a concurrent symlink swap from
// redirecting the read outside the canonical parent directory.
func ReadCanonicalFile(path string) ([]byte, error) {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem path: %w", err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(canonical))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(canonical))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path %q is not a regular file", path)
	}
	return io.ReadAll(file)
}

// ReadCanonicalRegularFile is the strict variant for persisted paths supplied
// by a caller. It rejects lexical traversal and any symlink component before
// reading, so a retained artifact cannot be redirected to another location.
func ReadCanonicalRegularFile(path string) ([]byte, error) {
	if path == "" || filepath.Clean(path) != path {
		return nil, fmt.Errorf("path %q is not canonical", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem path: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	if canonical != abs {
		return nil, fmt.Errorf("path %q contains a symlink", path)
	}
	return ReadCanonicalFile(canonical)
}

// StatCanonicalFile obtains metadata through a descriptor-relative open,
// avoiding an unchecked os.Stat on a caller-derived path.
func StatCanonicalFile(path string) (os.FileInfo, error) {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve filesystem path: %w", err)
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(canonical))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(canonical))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}
