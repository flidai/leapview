package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/stretchr/testify/require"
)

type recordingLifecycle struct {
	initialize []composectl.InitOptions
	starts     int
}

func (l *recordingLifecycle) Initialize(_ context.Context, options composectl.InitOptions) error {
	l.initialize = append(l.initialize, options)
	return nil
}

func (l *recordingLifecycle) Start(context.Context) error {
	l.starts++
	return nil
}

func TestInstallWritesCanonicalHostPayloadAndIsIdempotent(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	lifecycle := &recordingLifecycle{}
	var commands [][]string
	installer, err := New(Options{
		Paths: paths,
		LifecycleFactory: func(string) (Lifecycle, error) {
			return lifecycle, nil
		},
		Run: func(_ context.Context, name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			return nil
		},
	})
	require.NoError(t, err)

	require.NoError(t, installer.Install(t.Context()))
	require.Len(t, lifecycle.initialize, 1)
	require.Equal(t, composectl.InitOptions{
		AdminEmail:  config.AdminEmail,
		Domain:      config.Domain,
		Environment: config.Environment,
		Image:       config.Image,
	}, lifecycle.initialize[0])
	require.Equal(t, 1, lifecycle.starts)
	require.Equal(t, [][]string{
		{paths.Systemctl, "daemon-reload"},
		{paths.Systemctl, "enable", "--now", "leapview-backup.timer"},
	}, commands)

	for _, target := range []string{
		filepath.Join(paths.Root, "leapviewctl"),
		filepath.Join(paths.Root, "compose.yaml"),
		filepath.Join(paths.Root, "compose.https.yaml"),
		filepath.Join(paths.Root, "Caddyfile"),
		filepath.Join(paths.Root, "deployment.env.example"),
		filepath.Join(paths.Root, "deployment.env"),
		filepath.Join(paths.SystemBin, "leapviewctl"),
		filepath.Join(paths.SystemBin, "leapview-backup-hook"),
		filepath.Join(paths.Systemd, "leapview-backup.service"),
		filepath.Join(paths.Systemd, "leapview-backup.timer"),
		filepath.Join(paths.Root, installMarkerName),
	} {
		info, statErr := os.Stat(target)
		require.NoError(t, statErr, target)
		require.False(t, info.IsDir(), target)
	}

	// A repeated bootstrap repairs/verifies the files but must never initialize
	// the instance or mint first-login credentials a second time.
	require.NoError(t, installer.Install(t.Context()))
	require.Len(t, lifecycle.initialize, 1)
	require.Equal(t, 2, lifecycle.starts)
}

func TestInstallRejectsInvalidConfigurationBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	require.NoError(t, os.WriteFile(paths.Config, []byte(`{"schemaVersion":1,"domain":"bad/domain"}`), 0o600))
	installer, err := New(Options{Paths: paths})
	require.NoError(t, err)

	err = installer.Install(t.Context())
	require.ErrorContains(t, err, "configuration")
	_, statErr := os.Stat(paths.Root)
	require.True(t, os.IsNotExist(statErr))
}

func TestInstallRejectsPayloadFromAnotherImageBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	installer, err := New(Options{
		Paths:         paths,
		ExpectedImage: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64),
	})
	require.NoError(t, err)

	err = installer.Install(t.Context())
	require.ErrorContains(t, err, "does not match the extracted deployment payload image")
	_, statErr := os.Stat(paths.Root)
	require.True(t, os.IsNotExist(statErr))
}

func TestInstallRejectsChangedConfigurationAfterInitialization(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	lifecycle := &recordingLifecycle{}
	installer, err := New(Options{
		Paths: paths,
		LifecycleFactory: func(string) (Lifecycle, error) {
			return lifecycle, nil
		},
		Run: func(context.Context, string, ...string) error { return nil },
	})
	require.NoError(t, err)
	require.NoError(t, installer.Install(t.Context()))

	config.Domain = "other.example.com"
	writeConfig(t, paths.Config, config)
	err = installer.Install(t.Context())
	require.ErrorContains(t, err, "does not match the installed instance")
	require.Len(t, lifecycle.initialize, 1)
	require.Equal(t, 1, lifecycle.starts)
}

func TestInstallDoesNotInitializeAgainAfterPostStartSystemdFailure(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	lifecycle := &recordingLifecycle{}
	failSystemd := true
	installer, err := New(Options{
		Paths: paths,
		LifecycleFactory: func(string) (Lifecycle, error) {
			return lifecycle, nil
		},
		Run: func(context.Context, string, ...string) error {
			if failSystemd {
				return errors.New("systemd unavailable")
			}
			return nil
		},
	})
	require.NoError(t, err)

	require.ErrorContains(t, installer.Install(t.Context()), "reload systemd")
	require.Len(t, lifecycle.initialize, 1)
	require.Equal(t, 1, lifecycle.starts)
	_, statErr := os.Stat(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, statErr)

	failSystemd = false
	require.NoError(t, installer.Install(t.Context()))
	require.Len(t, lifecycle.initialize, 1)
	require.Equal(t, 2, lifecycle.starts)
}

func TestInstallRejectsSymlinkTargets(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	require.NoError(t, os.MkdirAll(paths.Root, 0o700))
	outside := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(outside, []byte("preserve"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(paths.Root, "compose.yaml")))
	installer, err := New(Options{Paths: paths})
	require.NoError(t, err)

	err = installer.Install(t.Context())
	require.ErrorContains(t, err, "symbolic link")
	contents, readErr := os.ReadFile(outside)
	require.NoError(t, readErr)
	require.Equal(t, "preserve", string(contents))
}

func TestDeploymentPayloadUpdateTracksImageAndRollsBack(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         current,
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	lifecycle := &recordingLifecycle{}
	installer, err := New(Options{
		Paths: paths,
		LifecycleFactory: func(string) (Lifecycle, error) {
			return lifecycle, nil
		},
		Run: func(context.Context, string, ...string) error { return nil },
	})
	require.NoError(t, err)
	require.NoError(t, installer.Install(t.Context()))

	currentCaddy := "caddy@sha256:" + strings.Repeat("c", 64)
	nextCaddy := "caddy@sha256:" + strings.Repeat("d", 64)
	currentEnvironment := "LEAPVIEW_IMAGE=" + current + "\nCADDY_IMAGE=" + currentCaddy + "\nCOMPOSE_HTTPS=1\n"
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, "deployment.env"), []byte(currentEnvironment), 0o600))
	nextPayload := map[string][]byte{}
	for _, file := range requiredPayloadFiles {
		nextPayload[file.Source] = []byte("next-" + file.Source + "\n")
	}
	nextPayload["deployment.env.example"] = []byte(
		"LEAPVIEW_IMAGE=example.invalid/leapview@sha256:" + strings.Repeat("e", 64) +
			"\nCADDY_IMAGE=" + nextCaddy + "\nCOMPOSE_HTTPS=1\n",
	)
	manager, err := NewDeploymentPayloadManager(DeploymentPayloadManagerOptions{
		Paths: paths,
		Load: func(context.Context, string) (map[string][]byte, error) {
			return nextPayload, nil
		},
		Run: func(context.Context, string, ...string) error { return nil },
	})
	require.NoError(t, err)
	update, err := manager.Prepare(t.Context(), current, next)
	require.NoError(t, err)
	require.NotNil(t, update)
	t.Cleanup(func() { require.NoError(t, update.Close()) })

	require.NoError(t, update.Apply())
	contents, err := os.ReadFile(filepath.Join(paths.Root, "compose.yaml"))
	require.NoError(t, err)
	require.Equal(t, "next-compose.yaml\n", string(contents))
	environment, err := os.ReadFile(filepath.Join(paths.Root, "deployment.env"))
	require.NoError(t, err)
	require.Equal(t, "LEAPVIEW_IMAGE="+next+"\nCADDY_IMAGE="+nextCaddy+"\nCOMPOSE_HTTPS=1\n", string(environment))
	installed, err := readMarker(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.Equal(t, next, installed.Image)

	require.NoError(t, update.Rollback())
	contents, err = os.ReadFile(filepath.Join(paths.Root, "compose.yaml"))
	require.NoError(t, err)
	require.Equal(t, "compose.yaml\n", string(contents))
	environment, err = os.ReadFile(filepath.Join(paths.Root, "deployment.env"))
	require.NoError(t, err)
	require.Equal(t, currentEnvironment, string(environment))
	installed, err = readMarker(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.Equal(t, current, installed.Image)
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	base := t.TempDir()
	return Paths{
		Payload:   filepath.Join(base, "payload"),
		Config:    filepath.Join(base, "bootstrap.json"),
		Root:      filepath.Join(base, "opt", "leapview"),
		ConfigDir: filepath.Join(base, "etc", "leapview"),
		SystemBin: filepath.Join(base, "usr", "local", "sbin"),
		Systemd:   filepath.Join(base, "etc", "systemd", "system"),
		Systemctl: filepath.Join(base, "bin", "systemctl"),
	}
}

func writeTestPayload(t *testing.T, directory string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(directory, 0o700))
	for _, file := range requiredPayloadFiles {
		mode := os.FileMode(0o600)
		if file.Source == "leapviewctl" || strings.HasSuffix(file.Source, "wrapper") || strings.HasSuffix(file.Source, "hook") {
			mode = 0o700
		}
		require.NoError(t, os.WriteFile(filepath.Join(directory, file.Source), []byte(file.Source+"\n"), mode))
	}
}

func writeConfig(t *testing.T, path string, config Config) {
	t.Helper()
	contents, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, contents, 0o600))
}

func boolPointer(value bool) *bool { return &value }
