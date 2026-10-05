//go:build linux

package hostinstall

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/platform/ociref"
	"github.com/stretchr/testify/require"
)

func TestNativeUpgradeStagesSeedTemplateWithoutChangingInstalledConfiguration(t *testing.T) {
	for _, scenario := range []string{"absent seed", "different seed", "changed topology"} {
		t.Run(scenario, func(t *testing.T) {
			effects := nativeEffectsFixture(t)
			effects.log = os.Stderr
			fixture := filepath.Join(t.TempDir(), "payload")
			writeTestPayload(t, fixture)
			for _, name := range []string{"compose.yaml", "compose.https.yaml", "Caddyfile", "deployment.env.example"} {
				contents, err := os.ReadFile(filepath.Join(fixture, name))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(effects.root, name), contents, 0o600))
			}
			if scenario == "different seed" {
				require.NoError(t, os.WriteFile(filepath.Join(effects.root, "leapview.env.example"), []byte("different first-install defaults\n"), 0o600))
			}
			if scenario == "changed topology" {
				require.NoError(t, os.WriteFile(filepath.Join(effects.root, "compose.yaml"), []byte("different topology\n"), 0o600))
			}
			before, err := os.ReadFile(filepath.Join(effects.root, "leapview.env"))
			require.NoError(t, err)
			bin := t.TempDir()
			t.Setenv("LEAPVIEW_TEST_UPGRADE_PAYLOAD", fixture)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
case "$1" in
 pull) exit 0 ;;
 create) printf 'candidate-container\n' ;;
 cp) cp -R "$LEAPVIEW_TEST_UPGRADE_PAYLOAD/." "$3" ;;
 rm) exit 0 ;;
 *) exit 1 ;;
esac
`), 0o700))
			err = effects.stage(t.Context())
			if scenario == "changed topology" {
				require.ErrorContains(t, err, "deployment topology changed: compose.yaml")
				return
			}
			require.NoError(t, err)
			reference, err := ociref.ParseImmutable(effects.id.Candidate)
			require.NoError(t, err)
			staged, err := os.ReadFile(filepath.Join(effects.root, "releases", reference.Generation, "leapview.env.example"))
			require.NoError(t, err)
			expected, err := os.ReadFile(filepath.Join(fixture, "leapview.env.example"))
			require.NoError(t, err)
			require.Equal(t, expected, staged)
			after, err := os.ReadFile(filepath.Join(effects.root, "leapview.env"))
			require.NoError(t, err)
			require.Equal(t, before, after, "candidate defaults must not rewrite operator configuration")
		})
	}
}
