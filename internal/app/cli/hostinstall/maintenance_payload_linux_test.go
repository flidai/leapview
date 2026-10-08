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

func TestLegacyPublicMaintenanceStagePreservesSixFilePredecessorAndSupportsNextDeploy(t *testing.T) {
	e := nativeEffectsFixture(t)
	e.id.Predecessor = legacyPublicImage
	e.request.PredecessorImage, e.request.PredecessorRevision = legacyPublicImage, legacyPublicRevision
	log, err := os.Create(filepath.Join(t.TempDir(), "operator.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })
	e.log = log
	fixtures := t.TempDir()
	currentRoot := filepath.Join(fixtures, "candidate")
	writeTestPayload(t, currentRoot)
	compose, err := os.ReadFile("../../../../deploy/compose/compose.yaml")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(currentRoot, "compose.yaml"), compose, 0o600))
	legacyNames := map[string]bool{"leapviewctl": true, "leapviewctl-wrapper": true,
		"compose.yaml": true, "compose.https.yaml": true, "Caddyfile": true, "deployment.env.example": true}
	oldRoot := filepath.Join(fixtures, "predecessor")
	require.NoError(t, os.MkdirAll(oldRoot, 0o700))
	oldGeneration := "sha256-" + strings.Split(legacyPublicImage, "sha256:")[1]
	oldRelease := filepath.Join(e.root, "releases", oldGeneration)
	require.NoError(t, os.MkdirAll(oldRelease, 0o700))
	var oldFiles []payloadFile
	for _, file := range requiredPayloadFiles {
		if !legacyNames[file.Source] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(currentRoot, file.Source))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(oldRoot, file.Source), data, file.Mode))
		require.NoError(t, os.WriteFile(filepath.Join(oldRelease, file.Source), data, file.Mode))
		oldFiles = append(oldFiles, file)
	}
	paths := InstalledPaths(e.root)
	paths.SystemBin = t.TempDir()
	require.NoError(t, activateGeneration(paths, oldGeneration))
	require.NoError(t, ensurePayloadLinksFor(paths, oldFiles))
	// The in-flight maintenance controller must remain selected while adding
	// links for the candidate-owned bootstrap and PostgreSQL payload files.
	require.NoError(t, os.Remove(filepath.Join(e.root, "leapviewctl")))
	require.NoError(t, os.Symlink("guarded-maintenance-controller", filepath.Join(e.root, "leapviewctl")))
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAYLOAD_FIXTURES", fixtures)
	t.Setenv("PREDECESSOR_IMAGE", legacyPublicImage)
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
	entries, err := os.ReadDir(oldRelease)
	require.NoError(t, err)
	require.Len(t, entries, 6, "staging changed the immutable predecessor generation")
	active, err := os.Readlink(filepath.Join(e.root, "current"))
	require.NoError(t, err)
	require.Equal(t, "releases/"+oldGeneration, active)
	controller, err := os.Readlink(filepath.Join(e.root, "leapviewctl"))
	require.NoError(t, err)
	require.Equal(t, "guarded-maintenance-controller", controller)
	firstCandidate := e.id.Candidate
	require.NoError(t, activateGeneration(paths, "sha256-"+strings.Split(firstCandidate, "sha256:")[1]))
	for _, file := range requiredPayloadFiles {
		if legacyNames[file.Source] {
			continue
		}
		want, err := os.ReadFile(filepath.Join(currentRoot, file.Source))
		require.NoError(t, err)
		got, err := os.ReadFile(file.Target(paths))
		require.NoError(t, err, "new payload link is unavailable: %s", file.Source)
		require.Equal(t, want, got)
	}
	// The next ordinary deployment must validate the newly installed adapter
	// files rather than depend on the historical compatibility exception again.
	e.id.Predecessor = firstCandidate
	e.id.Candidate = "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64)
	require.NoError(t, e.stage(t.Context()))
	require.NoError(t, activateGeneration(paths, oldGeneration))
	restored, err := os.ReadFile(filepath.Join(e.root, "compose.yaml"))
	require.NoError(t, err)
	require.Equal(t, compose, restored)
}

func TestLegacyPayloadLinksPreserveOperatorSeedTemplate(t *testing.T) {
	paths := InstalledPaths(t.TempDir())
	seed := filepath.Join(paths.Root, "leapview.env.example")
	operatorDefaults := []byte("operator-owned first-install defaults\n")
	require.NoError(t, os.WriteFile(seed, operatorDefaults, 0o600))
	require.NoError(t, ensureLegacyPayloadLinks(paths))
	actual, err := os.ReadFile(seed)
	require.NoError(t, err)
	require.Equal(t, operatorDefaults, actual)
	info, err := os.Lstat(seed)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink)
	link, err := os.Readlink(filepath.Join(paths.Root, "compose.postgres.yaml"))
	require.NoError(t, err)
	require.Equal(t, "current/compose.postgres.yaml", link)
}
