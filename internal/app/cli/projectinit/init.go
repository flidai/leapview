// Package projectinit materializes the versioned LeapView analytics starter.
package projectinit

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:template
var template embed.FS

// Initialize atomically creates a new analytics project. Existing filesystem
// entries are never adopted or overwritten.
func Initialize(destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return "", errors.New("project directory is required")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", fmt.Errorf("resolve project directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	if absolute == filepath.Dir(absolute) {
		return "", fmt.Errorf("refusing to initialize filesystem root %s", absolute)
	}
	parent := filepath.Dir(absolute)
	initialParentInfo, err := os.Lstat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect project parent: %w", err)
	}
	if initialParentInfo.Mode()&os.ModeSymlink != 0 || !initialParentInfo.IsDir() {
		return "", fmt.Errorf("project parent must be a real directory, not a symlink: %s", parent)
	}
	canonicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve project parent: %w", err)
	}
	resolvedParentInfo, err := os.Stat(canonicalParent)
	if err != nil || !os.SameFile(initialParentInfo, resolvedParentInfo) {
		return "", errors.New("project parent changed while resolving")
	}
	absolute = filepath.Join(canonicalParent, filepath.Base(absolute))
	parentFile, err := os.Open(canonicalParent)
	if err != nil {
		return "", fmt.Errorf("open project parent: %w", err)
	}
	defer parentFile.Close()
	parentInfo, err := parentFile.Stat()
	if err != nil || !parentInfo.IsDir() || !os.SameFile(initialParentInfo, parentInfo) {
		return "", fmt.Errorf("project parent is not a directory: %s", canonicalParent)
	}
	parentRoot, err := os.OpenRoot(canonicalParent)
	if err != nil {
		return "", fmt.Errorf("open project parent root: %w", err)
	}
	defer parentRoot.Close()
	rootInfo, err := parentRoot.Stat(".")
	if err != nil || !os.SameFile(parentInfo, rootInfo) {
		return "", errors.New("project parent changed while opening")
	}
	destinationName := filepath.Base(absolute)
	if _, err := parentRoot.Lstat(destinationName); err == nil {
		return "", fmt.Errorf("project destination already exists: %s", absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect project destination: %w", err)
	}
	stagingName, err := createStaging(parentRoot)
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_ = parentRoot.RemoveAll(stagingName)
		}
	}()
	stagingRoot, err := parentRoot.OpenRoot(stagingName)
	if err != nil {
		return "", fmt.Errorf("open project staging directory: %w", err)
	}
	defer stagingRoot.Close()
	if err := materialize(stagingRoot); err != nil {
		return "", err
	}
	if err := stagingRoot.Chmod(".", 0o755); err != nil {
		return "", fmt.Errorf("set project directory permissions: %w", err)
	}
	currentParent, err := os.Stat(canonicalParent)
	if err != nil || !os.SameFile(parentInfo, currentParent) {
		return "", errors.New("project parent changed before commit")
	}
	if err := renameNoReplace(parentFile.Fd(), stagingName, destinationName); err != nil {
		return "", fmt.Errorf("commit project without overwrite: %w", err)
	}
	committed = true
	return absolute, nil
}

func createStaging(parent *os.Root) (string, error) {
	for range 16 {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate project staging identity: %w", err)
		}
		name := ".leapview-init-" + hex.EncodeToString(random[:])
		if err := parent.Mkdir(name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create project staging directory: %w", err)
		}
	}
	return "", errors.New("create project staging directory: identity collision")
}

func materialize(destination *os.Root) error {
	return fs.WalkDir(template, "template", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "template" {
			return nil
		}
		relative, err := filepath.Rel("template", path)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
			return fmt.Errorf("invalid embedded project path %q", path)
		}
		target := filepath.FromSlash(relative)
		if entry.IsDir() {
			if err := destination.Mkdir(target, 0o755); err != nil {
				return fmt.Errorf("create project directory %s: %w", relative, err)
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("embedded project path %s is not a regular file", relative)
		}
		content, err := template.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := destination.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("create project file %s: %w", relative, err)
		}
		if _, err := file.Write(content); err != nil {
			_ = file.Close()
			return fmt.Errorf("write project file %s: %w", relative, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close project file %s: %w", relative, err)
		}
		return nil
	})
}
