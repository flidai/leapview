package compiler

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// SourceFiles resolves the concrete authored files reachable from the project
// manifest. Consumers use this projection instead of scanning the repository.
func SourceFiles(projectPath string) ([]string, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return nil, err
	}
	return sourceFilesFromProject(projectPath, project)
}

func sourceFilesFromProject(projectPath string, project Project) ([]string, error) {
	root, err := filepath.Abs(project.BaseDir)
	if err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project boundary %q: %w", root, err)
	}
	projectPath, err = filepath.Abs(projectPath)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	add := func(path string) error {
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
		boundaryPath, resolveErr := resolvePathForBoundary(path)
		if resolveErr != nil {
			return fmt.Errorf("resolve project source %q: %w", path, resolveErr)
		}
		relative, err := filepath.Rel(resolvedRoot, boundaryPath)
		if err != nil {
			return err
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("resolved project source %q escapes project boundary %q", path, root)
		}
		seen[filepath.Clean(path)] = struct{}{}
		return nil
	}
	if err := add(projectPath); err != nil {
		return nil, err
	}
	addPaths := func(paths map[string]string) error {
		for _, path := range paths {
			if err := add(path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := addPaths(project.ConnectionPaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.SourcePaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.ModelPaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.SemanticModelPaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.DashboardPaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.PipelinePaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.PublicationPaths); err != nil {
		return nil, err
	}
	if err := addPaths(project.AccessPaths); err != nil {
		return nil, err
	}
	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func isMissingPath(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

func resolvePathForBoundary(path string) (string, error) {
	current := path
	remainder := ""
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, remainder), nil
		}
		if !isMissingPath(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}
