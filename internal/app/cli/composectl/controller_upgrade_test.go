package composectl

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpgradeImageSelectionPreservesOtherComposeSettings(t *testing.T) {
	root := t.TempDir()
	predecessor := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	candidate := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	path := filepath.Join(root, deploymentEnvName)
	require.NoError(t, os.WriteFile(path, []byte("LEAPVIEW_IMAGE="+predecessor+"\nCADDY_DOMAIN=dash.example.com\n"), 0o600))
	controller, err := New(Options{Root: root})
	require.NoError(t, err)
	configured, err := controller.ConfiguredImage()
	require.NoError(t, err)
	require.Equal(t, predecessor, configured)
	require.Error(t, controller.UpdateImage("ghcr.io/flidai/leapview:latest"))
	require.NoError(t, controller.UpdateImage(candidate))
	configured, err = controller.ConfiguredImage()
	require.NoError(t, err)
	require.Equal(t, candidate, configured)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(contents), "CADDY_DOMAIN=dash.example.com\n")
}

func TestUpgradeRunningImageReadsDockerContainerIdentity(t *testing.T) {
	root := t.TempDir()
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	docker := filepath.Join(root, "fake-docker")
	require.NoError(t, os.WriteFile(docker, []byte("#!/bin/sh\nif [ \"$1\" = inspect ] && [ \"$4\" = candidate-container ]; then\n  printf '%s\\n' '"+image+"'\n  exit 0\nfi\nexit 1\n"), 0o700))
	controller, err := New(Options{Root: root, DockerBin: docker})
	require.NoError(t, err)
	controller.composeOverride = func(_ context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) error {
		require.Equal(t, []string{"ps", "-q", "leapview"}, args)
		_, err := io.WriteString(stdout, "candidate-container\n")
		return err
	}
	running, err := controller.RunningImage(t.Context())
	require.NoError(t, err)
	require.Equal(t, image, running)
}
