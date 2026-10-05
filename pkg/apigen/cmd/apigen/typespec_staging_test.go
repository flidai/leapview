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

	require.NoError(t, copyTypeSpecProject(src, dst, t.TempDir()))
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

func TestCopyTypeSpecProject_ResolvesSourceRootSymlink(t *testing.T) {
	actualSource := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(actualSource, "main.tsp"), []byte("model Example {}"), 0o600))
	linkedSource := filepath.Join(t.TempDir(), "typespec-source")
	require.NoError(t, os.Symlink(actualSource, linkedSource))
	destination := filepath.Join(t.TempDir(), "project")

	require.NoError(t, copyTypeSpecProject(linkedSource, destination, t.TempDir()))
	stagedRoot, err := os.Lstat(destination)
	require.NoError(t, err)
	require.True(t, stagedRoot.IsDir(), "staged source root must be a copied directory, not a symlink")
	content, err := os.ReadFile(filepath.Join(destination, "main.tsp"))
	require.NoError(t, err)
	require.Equal(t, "model Example {}", string(content))
	require.NoError(t, os.WriteFile(filepath.Join(destination, "main.tsp"), []byte("model Staged {}"), 0o600))
	original, err := os.ReadFile(filepath.Join(actualSource, "main.tsp"))
	require.NoError(t, err)
	require.Equal(t, "model Example {}", string(original), "editing the staged source must not change the original")
}

func TestStageTypeSpecProject_ExcludesWorkspaceNestedInsideSource(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "main.tsp"), []byte("model Example {}"), 0o600))
	packageDir := t.TempDir()
	for _, path := range []string{
		"node_modules/@typespec/http",
		"node_modules/@typespec/openapi",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(packageDir, path), 0o750))
	}

	// A developer or CI runner can point TMPDIR at the TypeSpec source tree.
	// Staging must exclude its own workspace to avoid walking the copy it creates.
	t.Setenv("TMPDIR", source)
	stagedDir, cleanup, err := stageTypeSpecProject(source, typeSpecPackage{Dir: packageDir})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	content, err := os.ReadFile(filepath.Join(stagedDir, "main.tsp"))
	require.NoError(t, err)
	require.Equal(t, "model Example {}", string(content))
	stagingDir := filepath.Dir(stagedDir)
	excludedRel, err := filepath.Rel(source, stagingDir)
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(stagedDir, excludedRel))
}
