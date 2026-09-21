package hostinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractCandidatePayloadUsesExactDigestPinnedImage(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "payload")
	writeTestPayload(t, fixture)
	logPath := filepath.Join(root, "docker.log")
	t.Setenv("LEAPVIEW_TEST_UPGRADE_PAYLOAD", fixture)
	t.Setenv("LEAPVIEW_TEST_UPGRADE_DOCKER_LOG", logPath)
	docker := filepath.Join(root, "docker")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LEAPVIEW_TEST_UPGRADE_DOCKER_LOG"
case "$1" in
  pull) exit 0 ;;
  create) printf 'candidate-container\n'; exit 0 ;;
  cp) cp -R "$LEAPVIEW_TEST_UPGRADE_PAYLOAD/." "$3"; exit $? ;;
  rm) exit 0 ;;
esac
exit 1
`
	require.NoError(t, os.WriteFile(docker, []byte(script), 0o700))
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	payload, err := extractCandidatePayload(t.Context(), docker, image, os.Stderr)
	require.NoError(t, err)
	require.Equal(t, "compose.yaml\n", string(payload["compose.yaml"]))
	log, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Contains(t, string(log), "pull --quiet "+image+"\n")
	require.Contains(t, string(log), "create "+image+"\n")
	require.Contains(t, string(log), "cp candidate-container:/usr/local/share/leapview/deployment/.")
	_, err = extractCandidatePayload(t.Context(), docker, "ghcr.io/flidai/leapview:latest", os.Stderr)
	require.Error(t, err)
	logAfter, err := os.ReadFile(logPath)
	require.NoError(t, err)
	require.Equal(t, string(log), string(logAfter), "mutable references must not reach Docker")
}
