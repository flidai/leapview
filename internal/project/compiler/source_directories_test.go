package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceDirectoriesWatchInvalidAndEmptyAuthoredTreesOnly(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"connections/team/empty", "models", "node_modules/unrelated", ".git/objects", "notes"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, directory), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "connections/team/invalid.yaml"), []byte("broken: ["), 0o600))
	_, compileErr := SourceFiles(root)
	require.Error(t, compileErr, "fixture must remain invalid")
	external := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(external, "private"), 0o700))
	require.NoError(t, os.Symlink(external, filepath.Join(root, "connections", "external")))

	directories, err := SourceDirectories(root)
	require.NoError(t, err)
	require.Equal(t, []string{
		root,
		filepath.Join(root, "connections"),
		filepath.Join(root, "connections", "team"),
		filepath.Join(root, "connections", "team", "empty"),
		filepath.Join(root, "models"),
	}, directories)
}

func TestIsAuthoredSourcePathUsesCompilerOwnershipAndRejectsEscapes(t *testing.T) {
	for _, directory := range authoredResourceDirectories {
		require.True(t, IsAuthoredSourcePath(directory.directory))
		require.True(t, IsAuthoredSourcePath(directory.directory+"/nested/resource.yaml"))
	}
	for _, path := range []string{"", ".", "notes/connection.yaml", "connections-other/resource.yaml", "../connections/resource.yaml", "connections/../../outside", "/connections/resource.yaml", "C:/connections/resource.yaml"} {
		require.False(t, IsAuthoredSourcePath(path), path)
	}
}
