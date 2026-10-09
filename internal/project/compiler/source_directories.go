package compiler

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// SourceDirectories returns the source root and bounded trees beneath the six
// conventional authoring directories without parsing resources. Development
// watchers must remain able to observe repairs
// when a resource is invalid, and new resources in currently empty directories.
// Symlink entries are excluded; resource compilation reports their invalidity.
func SourceDirectories(sourceRoot string) ([]string, error) {
	rootPath, root, err := openAuthoredSourceRoot(sourceRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	directories := make([]string, 0)
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("scan source directories at %q: %w", relative, walkErr)
		}
		if relative != "." {
			entries++
			if entries > maxAuthoredSourceEntries {
				return fmt.Errorf("analytics source exceeds %d filesystem entries", maxAuthoredSourceEntries)
			}
		}
		if entry.IsDir() && relative != "." && !IsAuthoredSourcePath(relative) {
			return fs.SkipDir
		}
		if entry.Type()&os.ModeSymlink == 0 && entry.IsDir() {
			directories = append(directories, filepath.Join(rootPath, filepath.FromSlash(relative)))
		}
		return nil
	})
	return directories, err
}

// IsAuthoredSourcePath reports whether a relative file or directory belongs to
// one of the compiler's conventional authoring directories. It does not parse
// or validate the resource, so invalid edits remain observable to a watcher.
func IsAuthoredSourcePath(relative string) bool {
	cleaned, err := cleanSourceRelativePath(relative)
	if err != nil {
		return false
	}
	for _, owned := range authoredResourceDirectories {
		if cleaned == owned.directory {
			return true
		}
	}
	_, owned := authoredDirectoryForPath(cleaned)
	return owned
}
