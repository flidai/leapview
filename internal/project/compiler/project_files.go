package compiler

import (
	"fmt"
	"io/fs"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
)

// mapProjectReader is a logical, project-relative source tree. It deliberately
// has no fallback to os.ReadFile: a missing source is an ordinary source error.
type mapProjectReader struct {
	files map[string][]byte
}

func newMapProjectReader(files map[string][]byte) (mapProjectReader, error) {
	if len(files) == 0 {
		return mapProjectReader{}, fmt.Errorf("project source files are required")
	}
	reader := mapProjectReader{files: make(map[string][]byte, len(files))}
	for name, content := range files {
		if name == "" || name == "." {
			return mapProjectReader{}, fmt.Errorf("source path %q is not a file path", name)
		}
		canonical, err := cleanLogicalPath(name)
		if err != nil {
			return mapProjectReader{}, fmt.Errorf("source path %q: %w", name, err)
		}
		if _, exists := reader.files[canonical]; exists {
			return mapProjectReader{}, fmt.Errorf("duplicate source path %q", name)
		}
		reader.files[canonical] = append([]byte(nil), content...)
	}
	return reader, nil
}

func (r mapProjectReader) ReadFile(name string) ([]byte, error) {
	canonical, err := cleanLogicalPath(name)
	if err != nil {
		return nil, err
	}
	content, ok := r.files[canonical]
	if !ok {
		return nil, fmt.Errorf("%s: %w", canonical, fs.ErrNotExist)
	}
	return append([]byte(nil), content...), nil
}

func (r mapProjectReader) ExpandIncludes(baseDir string, includes []string) ([]string, error) {
	baseDir, err := cleanLogicalPath(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project boundary: %w", err)
	}
	var paths []string
	seen := map[string]struct{}{}
	for _, pattern := range includes {
		if strings.TrimSpace(pattern) == "" {
			return nil, fmt.Errorf("include pattern is required")
		}
		if pattern != strings.TrimSpace(pattern) || strings.Contains(pattern, `\`) {
			return nil, fmt.Errorf("include pattern %q must use canonical slash-separated paths", pattern)
		}
		if strings.HasPrefix(pattern, "/") || isWindowsAbsolutePath(pattern) {
			return nil, fmt.Errorf("include pattern %q must be relative", pattern)
		}
		if strings.Contains(strings.ReplaceAll(pattern, "\\", "/"), "**") {
			return nil, fmt.Errorf("include pattern %q uses unsupported ** glob", pattern)
		}
		clean := pathpkg.Clean(pattern)
		for _, part := range strings.Split(clean, "/") {
			if part == ".." {
				return nil, fmt.Errorf("include pattern %q escapes project boundary", pattern)
			}
		}
		glob := pathpkg.Join(baseDir, clean)
		matches := make([]string, 0)
		for name := range r.files {
			matched, matchErr := pathpkg.Match(glob, name)
			if matchErr != nil {
				return nil, fmt.Errorf("include pattern %q: %w", pattern, matchErr)
			}
			if matched {
				matches = append(matches, name)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("include pattern %q matched no files", pattern)
		}
		sort.Strings(matches)
		for _, match := range matches {
			if ext := strings.ToLower(pathpkg.Ext(match)); ext != ".yaml" && ext != ".yml" {
				return nil, fmt.Errorf("include pattern %q matched non-YAML file %s", pattern, match)
			}
			if _, duplicate := seen[match]; duplicate {
				continue
			}
			seen[match] = struct{}{}
			paths = append(paths, match)
		}
	}
	return paths, nil
}

func cleanLogicalPath(name string) (string, error) {
	if name != strings.TrimSpace(name) {
		return "", fmt.Errorf("path must not contain leading or trailing whitespace")
	}
	if name == "" {
		return ".", nil
	}
	if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || isWindowsAbsolutePath(name) {
		return "", fmt.Errorf("path must be relative")
	}
	clean := pathpkg.Clean(name)
	if clean != name {
		return "", fmt.Errorf("path is not canonical")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes project boundary")
	}
	return clean, nil
}

func isWindowsAbsolutePath(name string) bool {
	return len(name) >= 3 && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z')) && name[1] == ':' && (name[2] == '/' || name[2] == '\\')
}

func (r mapProjectReader) ValidateDashboardPath(projectRoot, dashboardPath string) (string, string, error) {
	root, err := cleanLogicalPath(projectRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve project boundary: %w", err)
	}
	dashboard, err := cleanLogicalPath(dashboardPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve dashboard path: %w", err)
	}
	if _, ok := r.files[dashboard]; !ok {
		return "", "", fmt.Errorf("resolve dashboard path: %s: %w", dashboard, fs.ErrNotExist)
	}
	relative, err := logicalRelative(root, dashboard)
	if err != nil {
		return "", "", fmt.Errorf("dashboard path %q resolves outside project boundary", dashboardPath)
	}
	return pathpkg.Dir(dashboard), relative, nil
}

func (r mapProjectReader) ResolveIncludePaths(projectRoot, dashboardDir, pattern string) ([]string, error) {
	// Fragment include matching has the same safety and ordering contract as
	// project resource includes, but paths are relative to the dashboard.
	if strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("dashboard fragment include pattern is required")
	}
	if strings.HasPrefix(pattern, "/") || isWindowsAbsolutePath(pattern) {
		return nil, fmt.Errorf("dashboard fragment include pattern %q must be relative to the dashboard", pattern)
	}
	if strings.Contains(strings.ReplaceAll(pattern, "\\", "/"), "**") {
		return nil, fmt.Errorf("dashboard fragment include pattern %q uses unsupported ** glob", pattern)
	}
	if pattern != strings.TrimSpace(pattern) || strings.Contains(pattern, `\`) {
		return nil, fmt.Errorf("dashboard fragment include pattern %q must use canonical slash-separated paths", pattern)
	}
	clean := pathpkg.Clean(pattern)
	glob := pathpkg.Join(dashboardDir, clean)
	if _, err := logicalRelative(projectRoot, glob); err != nil {
		return nil, fmt.Errorf("dashboard fragment include pattern %q escapes the project boundary", pattern)
	}
	matches := make([]string, 0)
	for name := range r.files {
		matched, err := pathpkg.Match(glob, name)
		if err != nil {
			return nil, fmt.Errorf("dashboard fragment include pattern %q: %w", pattern, err)
		}
		if matched {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("dashboard fragment include pattern %q matched no files", pattern)
	}
	sort.Strings(matches)
	seen := make(map[string]struct{}, len(matches))
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		if ext := strings.ToLower(pathpkg.Ext(match)); ext != ".yaml" && ext != ".yml" {
			return nil, fmt.Errorf("dashboard fragment include %q matched non-YAML file %s", pattern, match)
		}
		if _, duplicate := seen[match]; duplicate {
			continue
		}
		seen[match] = struct{}{}
		result = append(result, match)
	}
	return result, nil
}

func (r mapProjectReader) CanonicalPath(name string) (string, error) {
	canonical, err := cleanLogicalPath(name)
	if err != nil {
		return "", err
	}
	if _, ok := r.files[canonical]; !ok {
		return "", fs.ErrNotExist
	}
	return canonical, nil
}

func (r mapProjectReader) RelativePath(projectRoot, target string) (string, error) {
	root, err := cleanLogicalPath(projectRoot)
	if err != nil {
		return "", err
	}
	target, err = cleanLogicalPath(target)
	if err != nil {
		return "", err
	}
	relative, err := logicalRelative(root, target)
	if err != nil {
		return "", fmt.Errorf("path resolves outside project boundary")
	}
	return relative, nil
}

func logicalRelative(root, target string) (string, error) {
	if root == "." {
		return target, nil
	}
	if target == root {
		return ".", nil
	}
	prefix := root + "/"
	if !strings.HasPrefix(target, prefix) {
		return "", fmt.Errorf("path resolves outside project boundary")
	}
	return strings.TrimPrefix(target, prefix), nil
}

func (r mapProjectReader) DisplayPath(projectRoot, target string) string {
	if relative, err := r.RelativePath(projectRoot, target); err == nil {
		return relative
	}
	return strings.ReplaceAll(target, "\\", "/")
}

var _ projectFileReader = mapProjectReader{}
var _ document.FragmentReader = mapProjectReader{}
