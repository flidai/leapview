package main

import (
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// The immutable checkout commit is the baseline, so even building this helper
// cannot hide an earlier dependency mutation. Walk ignored files too: an
// initially absent go.sum or ignored lockfile must not appear unnoticed.
func checkIntegrity(ctx context.Context, root, revision string) error {
	if !commitID.MatchString(revision) {
		return fmt.Errorf("full checkout commit ID is required")
	}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, output)
		}
		return output, nil
	}
	if _, err := git("diff", "--exit-code", "--name-only", revision, "--"); err != nil {
		return fmt.Errorf("SAST changed tracked checkout files: %w", err)
	}
	files, err := git("ls-tree", "-r", "--name-only", "-z", revision)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, path := range strings.Split(string(files), "\x00") {
		if dependencyPath(path) {
			expected[path] = true
		}
	}
	var unexpected []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if rel != "." && ignoredDependencyDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !dependencyPath(rel) {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("dependency file is a symlink: %s", rel)
		}
		if !expected[rel] {
			unexpected = append(unexpected, rel)
		}
		delete(expected, rel)
		return nil
	})
	if err != nil {
		return err
	}
	for path := range expected {
		unexpected = append(unexpected, "missing: "+path)
	}
	if len(unexpected) > 0 {
		slices.Sort(unexpected)
		return fmt.Errorf("SAST dependency file inventory changed: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func ignoredDependencyDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".terraform", ".task", ".cache", ".tmp", "dist", "out", "coverage":
		return true
	}
	return false
}

func dependencyPath(path string) bool {
	parts := strings.Split(path, "/")
	for _, part := range parts[:len(parts)-1] {
		if ignoredDependencyDir(part) {
			return false
		}
	}
	switch parts[len(parts)-1] {
	case "go.mod", "go.sum", "go.work", "go.work.sum", "package.json", "package-lock.json", "npm-shrinkwrap.json", "bun.lock", "bun.lockb", "pnpm-lock.yaml", "yarn.lock":
		return true
	}
	return false
}
