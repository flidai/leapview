package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyTypeSpecProject_ExcludesInFlightGenerationOutputs(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "project")
	inputs := map[string]string{
		"main.tsp":       "import \"./helpers.js\";",
		"helpers.js":     "export const helper = {};",
		"package.json":   `{"type":"module"}`,
		"tspconfig.yaml": "emit: []\n",
		".shared.tsp":    "model Shared {}",
		"notes.tmp":      "ordinary project file",
		"gen/ir.json":    `{"previous":"complete"}`,
	}
	for path, content := range inputs {
		path = filepath.Join(src, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}

	// Other compilers may be writing these outputs while this project is staged.
	// Their pending files can disappear at any time when atomically renamed.
	var pendingOutputs []string
	for _, output := range []string{"gen/ir.json", "gen/openapi.yaml"} {
		pending, err := tempOutputPath(filepath.Join(src, output))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(pending, []byte("incomplete output"), 0o600))
		pendingOutputs = append(pendingOutputs, pending)
	}

	require.NoError(t, copyTypeSpecProject(src, dst))
	for _, pending := range pendingOutputs {
		rel, err := filepath.Rel(src, pending)
		require.NoError(t, err)
		require.NoFileExists(t, filepath.Join(dst, rel))
	}
	for path, content := range inputs {
		staged, err := os.ReadFile(filepath.Join(dst, path))
		require.NoError(t, err)
		require.Equal(t, content, string(staged))
	}
}
