package installationstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstallationMarkerRequiresExplicitPhaseAndCurrentGeneration(t *testing.T) {
	root := t.TempDir()
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	generation := "sha256-" + strings.Repeat("a", 64)
	releases := filepath.Join(root, "releases")
	require.NoError(t, os.MkdirAll(filepath.Join(releases, generation), 0o700))
	require.NoError(t, os.Symlink(filepath.Join("releases", generation), filepath.Join(root, "current")))

	config := Config{SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com", Environment: "prod", Image: image, HTTPS: boolPointer(true)}
	marker, err := NewMarker(config, PhasePrivate)
	require.NoError(t, err)
	require.NoError(t, WriteMarker(root, marker))
	got, present, err := ReadMarker(root)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, marker, got)
	require.NoError(t, VerifyCurrent(root, got, image))

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	var shape map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &shape))
	require.Contains(t, shape, "image")
	require.Contains(t, shape, "bootstrapPhase")
	require.Contains(t, shape, "generation")
	require.NotContains(t, shape, "config")

	got.Generation = "sha256-" + strings.Repeat("b", 64)
	require.ErrorContains(t, got.Validate(), "generation")
	got.Generation = generation
	got.BootstrapPhase = ""
	require.ErrorContains(t, got.Validate(), "explicit bootstrap phase")
	got.BootstrapPhase = PhasePrivate
	got.HTTPS = nil
	require.ErrorContains(t, got.Validate(), "explicit HTTPS selection")
}

func TestInstallationMarkerRejectsMissingPhaseUnknownFieldsAndPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, MarkerName)
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	generation := "sha256-" + strings.Repeat("a", 64)
	for _, contents := range []string{
		`{"schemaVersion":1,"domain":"dash.example.com","adminEmail":"admin@example.com","environment":"prod","image":"` + image + `","https":true,"generation":"` + generation + `"}`,
		`{"schemaVersion":1,"domain":"dash.example.com","adminEmail":"admin@example.com","environment":"prod","image":"` + image + `","https":true,"bootstrapPhase":"public","generation":"` + generation + `","extra":true}`,
	} {
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
		_, _, err := ReadMarker(root)
		require.Error(t, err)
	}
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":1,"domain":"dash.example.com","adminEmail":"admin@example.com","environment":"prod","image":"`+image+`","https":true,"bootstrapPhase":"public","generation":"`+generation+`"}`), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))
	_, _, err := ReadMarker(root)
	require.ErrorContains(t, err, "private regular file")
}

func boolPointer(value bool) *bool { return &value }
