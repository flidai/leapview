//go:build linux

package hostinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaintenanceStageRetainsPredecessorHealthcheckForRecovery(t *testing.T) {
	e := nativeEffectsFixture(t)
	log, err := os.Create(filepath.Join(t.TempDir(), "operator.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })
	e.log = log
	current, err := os.ReadFile("../../../../deploy/compose/compose.yaml")
	require.NoError(t, err)
	previous := []byte(strings.Replace(string(current), "[CMD, /usr/local/bin/leapview, healthcheck]", "[CMD, leapview, healthcheck]", 1))
	fixtures := t.TempDir()
	for name, compose := range map[string][]byte{"predecessor": previous, "candidate": current} {
		root := filepath.Join(fixtures, name)
		writeTestPayload(t, root)
		require.NoError(t, os.WriteFile(filepath.Join(root, "compose.yaml"), compose, 0o600))
	}
	predecessor, err := readPayload(filepath.Join(fixtures, "predecessor"))
	require.NoError(t, err)
	paths := InstalledPaths(e.root)
	paths.SystemBin = t.TempDir()
	oldGeneration, err := stageGeneration(paths, e.id.Predecessor, predecessor)
	require.NoError(t, err)
	require.NoError(t, activateGeneration(paths, oldGeneration))
	require.NoError(t, ensurePayloadLinks(paths))
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAYLOAD_FIXTURES", fixtures)
	t.Setenv("PREDECESSOR_IMAGE", e.id.Predecessor)
	script := `#!/bin/sh
set -eu
case "$1" in
  pull|rm) exit 0 ;;
  create) if [ "$2" = "$PREDECESSOR_IMAGE" ]; then echo predecessor; else echo candidate; fi ;;
  cp) name=${2%%:*}; cp -R "$PAYLOAD_FIXTURES/$name/." "$3" ;;
  *) exit 1 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o700))
	require.NoError(t, e.stage(t.Context()))
	readCompose := func() []byte {
		data, err := os.ReadFile(filepath.Join(e.root, "compose.yaml"))
		require.NoError(t, err)
		return data
	}
	require.Equal(t, previous, readCompose(), "staging must not change the active generation")
	require.NoError(t, activateGeneration(paths, "sha256-"+strings.Split(e.id.Candidate, "@sha256:")[1]))
	require.Equal(t, current, readCompose())
	require.NoError(t, activateGeneration(paths, oldGeneration))
	require.Equal(t, previous, readCompose(), "recovery must restore the original command")
}
