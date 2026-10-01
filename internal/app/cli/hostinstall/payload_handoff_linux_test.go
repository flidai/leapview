//go:build linux

package hostinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostAndDemoReleaseStagingInteroperate(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	paths := testPaths(t)
	require.NoError(t, os.MkdirAll(paths.Root, 0700))
	payload := testPayload("fixture-")
	fixture := t.TempDir()
	for name, data := range payload {
		require.NoError(t, os.WriteFile(filepath.Join(fixture, name), data, 0600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(fixture, "qualification"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "qualification", "browser.mjs"), []byte("qualification helper"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(fixture, "README.md"), []byte("documentation"), 0600))

	first := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	second := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	generation, err := stageGeneration(paths, first, payload)
	require.NoError(t, err)
	require.NoError(t, ensurePayloadLinks(paths))
	require.NoError(t, activateGeneration(paths, generation))

	module, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "scripts", "demo_compose_runtime.py"))
	require.NoError(t, err)
	// Exercise the real Python staging implementation with only Docker extraction
	// substituted by a packaged fixture. Go stages first; Python must accept that
	// generation, then produce a new generation accepted by the Go installer.
	command := exec.CommandContext(t.Context(), "python3", "-c", `
import importlib.util, pathlib, shutil, sys
spec = importlib.util.spec_from_file_location('runtime', sys.argv[1])
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)
runtime.ROOT = pathlib.Path(sys.argv[2])
def run(*args):
    if args[:2] == ('docker', 'cp'):
        shutil.copytree(sys.argv[3], args[-1], dirs_exist_ok=True)
    elif args[:2] != ('docker', 'rm'):
        raise AssertionError('unexpected mutation')
runtime.run = run
runtime.out = lambda *args: 'fixture-container'
runtime.stage_release(sys.argv[4])
runtime.stage_release(sys.argv[5])
`, module, paths.Root, fixture, first, second)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	secondDirectory := filepath.Join(paths.Root, "releases", "sha256-"+strings.Repeat("b", 64))
	require.NoError(t, validateGeneration(secondDirectory, payload))
	_, err = stageGeneration(paths, second, payload)
	require.NoError(t, err, "Go must accept an image-only deployment generation")
	entries, err := os.ReadDir(secondDirectory)
	require.NoError(t, err)
	require.Len(t, entries, len(requiredPayloadFiles))
	active, err := os.Readlink(filepath.Join(paths.Root, "current"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join("releases", generation), active)
}
