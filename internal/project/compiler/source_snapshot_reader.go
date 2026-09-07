package compiler

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// snapshotSourceReader serves the immutable YAML snapshot captured during
// descriptor-bound discovery. It implements the compiler and dashboard
// fragment reader contracts without reopening the mutable source checkout.
type snapshotSourceReader struct {
	root  string
	files map[string][]byte
	paths map[string]struct{}
}

var _ sourceFileReader = (*snapshotSourceReader)(nil)

func (r *snapshotSourceReader) ReadFile(path string) ([]byte, error) {
	relative, err := r.relativePath(path)
	if err != nil {
		return nil, err
	}
	content, ok := r.files[relative]
	if !ok {
		return nil, fmt.Errorf("source file %q was not present in the validated source snapshot", relative)
	}
	return append([]byte(nil), content...), nil
}

func (r *snapshotSourceReader) ValidateDashboardPath(projectRoot, dashboardPath string) (string, string, error) {
	if err := r.validateRoot(projectRoot); err != nil {
		return "", "", err
	}
	relative, err := r.relativePath(dashboardPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve dashboard path: %w", err)
	}
	if _, ok := r.files[relative]; !ok {
		return "", "", fmt.Errorf("resolve dashboard path: source file %q was not present in the validated source snapshot", relative)
	}
	absolute := r.absolutePath(relative)
	return filepath.Dir(absolute), relative, nil
}

func (r *snapshotSourceReader) ResolveIncludePaths(projectRoot, dashboardDir, pattern string) ([]string, error) {
	if err := r.validateRoot(projectRoot); err != nil {
		return nil, err
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("dashboard fragment include pattern is required")
	}
	if filepath.IsAbs(pattern) || isWindowsAbsolute(pattern) {
		return nil, fmt.Errorf("dashboard fragment include pattern %q must be relative to the dashboard", pattern)
	}
	if strings.Contains(filepath.ToSlash(pattern), "**") {
		return nil, fmt.Errorf("dashboard fragment include pattern %q uses unsupported ** glob", pattern)
	}
	directory, err := r.relativePath(dashboardDir)
	if err != nil {
		return nil, fmt.Errorf("dashboard fragment include pattern %q escapes the project boundary", pattern)
	}
	joined := filepath.Join(filepath.FromSlash(directory), filepath.FromSlash(pattern))
	clean, err := cleanSourceRelativePath(joined)
	if err != nil {
		return nil, fmt.Errorf("dashboard fragment include pattern %q escapes the project boundary", pattern)
	}

	matchedPaths := make([]string, 0)
	nativePattern := filepath.FromSlash(clean)
	for relative := range r.paths {
		matched, matchErr := filepath.Match(nativePattern, filepath.FromSlash(relative))
		if matchErr != nil {
			return nil, fmt.Errorf("dashboard fragment include pattern %q: %w", pattern, matchErr)
		}
		if matched {
			matchedPaths = append(matchedPaths, relative)
		}
	}
	if len(matchedPaths) == 0 {
		return nil, fmt.Errorf("dashboard fragment include pattern %q matched no files", pattern)
	}
	sort.Strings(matchedPaths)
	matches := make([]string, 0, len(matchedPaths))
	for _, relative := range matchedPaths {
		ext := strings.ToLower(filepath.Ext(relative))
		if ext != ".yaml" && ext != ".yml" {
			return nil, fmt.Errorf("dashboard fragment include %q matched non-YAML file %s", pattern, r.absolutePath(relative))
		}
		matches = append(matches, r.absolutePath(relative))
	}
	return matches, nil
}

func (r *snapshotSourceReader) CanonicalPath(path string) (string, error) {
	relative, err := r.relativePath(path)
	if err != nil {
		return "", err
	}
	if _, ok := r.files[relative]; !ok {
		return "", fmt.Errorf("source file %q was not present in the validated source snapshot", relative)
	}
	return r.absolutePath(relative), nil
}

func (r *snapshotSourceReader) RelativePath(projectRoot, target string) (string, error) {
	if err := r.validateRoot(projectRoot); err != nil {
		return "", err
	}
	return r.relativePath(target)
}

func (r *snapshotSourceReader) DisplayPath(projectRoot, target string) string {
	if err := r.validateRoot(projectRoot); err == nil {
		if relative, relativeErr := r.relativePath(target); relativeErr == nil {
			return relative
		}
	}
	return filepath.ToSlash(filepath.Base(target))
}

func (r *snapshotSourceReader) validateRoot(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve project boundary: %w", err)
	}
	if filepath.Clean(absolute) != filepath.Clean(r.root) {
		return fmt.Errorf("project boundary does not match the validated source snapshot")
	}
	return nil
}

func (r *snapshotSourceReader) relativePath(path string) (string, error) {
	return relativePathWithinRoot(r.root, path)
}

func relativePathWithinRoot(root, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("source path is required")
	}
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(root, filepath.Clean(path))
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path resolves outside source root")
		}
		return filepath.ToSlash(relative), nil
	}
	return cleanSourceRelativePath(path)
}

func (r *snapshotSourceReader) absolutePath(relative string) string {
	return filepath.Join(r.root, filepath.FromSlash(relative))
}
