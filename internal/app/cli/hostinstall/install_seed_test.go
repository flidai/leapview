package hostinstall

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/stretchr/testify/require"
)

type preparingLifecycle struct {
	recordingLifecycle
	controller *composectl.Controller
}

func (l *preparingLifecycle) UpdateImage(image string) error {
	return l.controller.UpdateImage(image)
}

func (l *preparingLifecycle) PrepareFirstInstall(ctx context.Context, options composectl.FirstInstallOptions) error {
	return l.controller.PrepareFirstInstall(ctx, options)
}

// Exercise the real first-install preparation against the generation/link
// layout created by Installer. Only the external Docker boundary is replaced.
func TestInstallSeedsApplicationEnvironmentBeforeRealPoolPreparation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake Docker executable uses a POSIX shell")
	}
	for _, test := range []struct {
		name     string
		existing bool
		symlink  bool
		archive  bool
	}{
		{name: "fresh OCI payload"},
		{name: "fresh rendered archive", archive: true},
		{name: "resume OCI preserves operator values", existing: true},
		{name: "resume archive preserves operator values", existing: true, archive: true},
		{name: "reject redirected application environment", symlink: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := testPaths(t)
			writeTestPayload(t, paths.Payload)
			seed, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "compose", "leapview.env.example"))
			require.NoError(t, err)
			seed = append(seed, []byte("\nLEAPVIEW_TEST_SEED=seed-marker\n")...)
			require.NoError(t, os.WriteFile(filepath.Join(paths.Payload, "leapview.env.example"), seed, 0o600))
			deployment, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "compose", "deployment.env.example"))
			require.NoError(t, err)
			// Standalone Compose archives render the admitted image here; the
			// OCI-extracted payload copies the authored placeholder unchanged.
			if test.archive {
				deployment = bytes.ReplaceAll(deployment, []byte("ghcr.io/flidai/leapview@sha256:<release-digest>"),
					[]byte("ghcr.io/flidai/leapview@sha256:"+strings.Repeat("a", 64)))
			}
			require.NoError(t, os.WriteFile(filepath.Join(paths.Payload, "deployment.env.example"), deployment, 0o600))
			writeConfig(t, paths.Config, Config{
				SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
				Environment: "prod", Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64), HTTPS: boolPointer(true),
			})
			writeOperatorConfig(t, paths.OperatorConfig)
			appPath := filepath.Join(paths.Root, "leapview.env")
			if test.existing || test.symlink {
				require.NoError(t, os.MkdirAll(paths.Root, 0o700))
			}
			if test.existing {
				require.NoError(t, os.WriteFile(appPath, bytes.ReplaceAll(seed, []byte("seed-marker"), []byte("operator-marker")), 0o600))
			}
			outside := filepath.Join(t.TempDir(), "outside.env")
			if test.symlink {
				require.NoError(t, os.WriteFile(outside, seed, 0o600))
				require.NoError(t, os.Symlink(outside, appPath))
			}
			docker := filepath.Join(t.TempDir(), "docker")
			require.NoError(t, os.WriteFile(docker, []byte("#!/bin/sh\ngrep -Fxq 'LEAPVIEW_IMAGE=ghcr.io/flidai/leapview@sha256:"+
				strings.Repeat("a", 64)+"' deployment.env || exit 43\nprintf 'reached-pool-dry-run\\n' >&2\nexit 42\n"), 0o700))
			var lifecycle *preparingLifecycle
			installer, err := New(Options{Paths: paths, LifecycleFactory: func(root string) (Lifecycle, error) {
				controller, err := composectl.New(composectl.Options{Root: root, DockerBin: docker})
				lifecycle = &preparingLifecycle{controller: controller}
				return lifecycle, err
			}})
			require.NoError(t, err)
			err = installer.Install(t.Context())
			if test.symlink {
				require.ErrorContains(t, err, "install application environment")
				require.ErrorContains(t, err, "symbolic link")
				contents, err := os.ReadFile(outside)
				require.NoError(t, err)
				require.Equal(t, seed, contents)
				require.Empty(t, lifecycle.initialize)
				require.Zero(t, lifecycle.starts)
				return
			}
			selectedImage, imageErr := lifecycle.controller.ConfiguredImage()
			require.NoError(t, imageErr)
			require.Equal(t, "ghcr.io/flidai/leapview@sha256:"+strings.Repeat("a", 64), selectedImage,
				"the pool dry-run must use the admitted image before initialization")
			require.ErrorContains(t, err, "dry-run first-install physical-pool bootstrap")
			require.ErrorContains(t, err, "reached-pool-dry-run")
			require.Empty(t, lifecycle.initialize, "failed dry-run must prevent initialization")
			require.Zero(t, lifecycle.starts)
			info, err := os.Lstat(appPath)
			require.NoError(t, err)
			require.True(t, info.Mode().IsRegular())
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			contents, err := os.ReadFile(appPath)
			require.NoError(t, err)
			if test.existing {
				require.Contains(t, string(contents), "LEAPVIEW_TEST_SEED=operator-marker")
				require.NotContains(t, string(contents), "seed-marker")
			} else {
				require.Contains(t, string(contents), "LEAPVIEW_TEST_SEED=seed-marker")
			}
			_, bootstrap, err := readAndValidateOperatorBootstrap(paths.OperatorConfig)
			require.NoError(t, err)
			require.Contains(t, string(contents), bootstrap.Postgres.ControlURL)
			require.NotContains(t, string(contents), bootstrap.Postgres.ControlMigratorURL)
			link, err := os.Readlink(filepath.Join(paths.Root, "leapview.env.example"))
			require.NoError(t, err)
			require.Equal(t, filepath.Join("current", "leapview.env.example"), link)
		})
	}
}
