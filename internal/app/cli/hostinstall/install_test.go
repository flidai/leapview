package hostinstall

import (
	"context"
	"encoding/json"
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
	require.Empty(t, commands)

	for _, target := range []string{
		filepath.Join(paths.Root, "leapviewctl"),
		filepath.Join(paths.Root, "compose.yaml"),
		filepath.Join(paths.Root, "compose.https.yaml"),
		filepath.Join(paths.Root, "Caddyfile"),
		filepath.Join(paths.Root, "deployment.env.example"),
		filepath.Join(paths.Root, "deployment.env"),
		filepath.Join(paths.SystemBin, "leapviewctl"),
		filepath.Join(paths.Root, installMarkerName),
	} {
		info, statErr := os.Stat(target)
		require.NoError(t, statErr, target)
		require.False(t, info.IsDir(), target)
	}
	current, err := os.Readlink(filepath.Join(paths.Root, "current"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join("releases", "sha256-"+strings.Repeat("a", 64)), current)
	for _, file := range requiredPayloadFiles {
		target := file.Target(paths)
		link, linkErr := os.Readlink(target)
		require.NoError(t, linkErr, target)
		require.Contains(t, link, filepath.Join("current", file.Source), target)
	}

	// A repeated bootstrap verifies the generation but must never initialize
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
	require.ErrorContains(t, err, "unexpected symbolic link")
	contents, readErr := os.ReadFile(outside)
	require.NoError(t, readErr)
	require.Equal(t, "preserve", string(contents))
}

func TestStagedGenerationDoesNotChangeActivePayload(t *testing.T) {
	paths := testPaths(t)
	require.NoError(t, os.MkdirAll(paths.Root, 0o700))
	first := testPayload("first-")
	second := testPayload("second-")
	firstImage := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	secondImage := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)

	firstGeneration, err := stageGeneration(paths, firstImage, first)
	require.NoError(t, err)
	require.NoError(t, ensurePayloadLinks(paths))
	require.NoError(t, activateGeneration(paths, firstGeneration))
	_, err = stageGeneration(paths, secondImage, second)
	require.NoError(t, err)

	contents, err := os.ReadFile(filepath.Join(paths.Root, "compose.yaml"))
	require.NoError(t, err)
	require.Equal(t, "first-compose.yaml\n", string(contents))
	active, err := os.Readlink(filepath.Join(paths.Root, "current"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join("releases", firstGeneration), active)
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
		path := filepath.Join(directory, file.Source)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(file.Source+"\n"), mode))
	}
}

func testPayload(prefix string) map[string][]byte {
	payload := make(map[string][]byte, len(requiredPayloadFiles))
	for _, file := range requiredPayloadFiles {
		payload[file.Source] = []byte(prefix + file.Source + "\n")
	}
	return payload
}

func writeConfig(t *testing.T, path string, config Config) {
	t.Helper()
	contents, err := json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, contents, 0o600))
}

func boolPointer(value bool) *bool { return &value }
