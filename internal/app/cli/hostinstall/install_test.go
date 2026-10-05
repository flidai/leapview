package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/installationstate"
	"github.com/stretchr/testify/require"
)

type recordingLifecycle struct {
	initialize         []composectl.InitOptions
	operator           []composectl.FirstInstallOptions
	events             []string
	controlURL         string
	prepareErr         error
	initializeErr      error
	applyErr           error
	startErr           error
	starts             int
	beforePrivateStart func()
}

func (l *recordingLifecycle) PrepareFirstInstall(_ context.Context, options composectl.FirstInstallOptions) error {
	l.events = append(l.events, "dry-run")
	l.operator = append(l.operator, options)
	return l.prepareErr
}

func (l *recordingLifecycle) InitializeFirstInstall(_ context.Context, options composectl.InitOptions, controlMigratorURL string) error {
	l.events = append(l.events, "initialize")
	l.initialize = append(l.initialize, options)
	l.controlURL = controlMigratorURL
	return l.initializeErr
}

func (l *recordingLifecycle) ApplyFirstInstall(_ context.Context, options composectl.FirstInstallOptions) error {
	l.events = append(l.events, "apply")
	l.operator = append(l.operator, options)
	return l.applyErr
}

func (l *recordingLifecycle) StartFirstInstallBootstrap(context.Context) error {
	if l.beforePrivateStart != nil {
		l.beforePrivateStart()
	}
	l.events = append(l.events, "start-private")
	l.starts++
	return l.startErr
}

func (l *recordingLifecycle) Start(context.Context) error {
	l.events = append(l.events, "start")
	l.starts++
	return l.startErr
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
		TargetID:      "deployment-target-1",
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	writeOperatorConfig(t, paths.OperatorConfig)
	lifecycle := &recordingLifecycle{beforePrivateStart: func() {
		marker, present, err := installationstate.ReadMarker(paths.Root)
		require.NoError(t, err)
		require.True(t, present, "private phase must be durable before any service starts")
		require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
		require.NoError(t, installationstate.VerifyCurrent(paths.Root, marker, config.Image))
	}}
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
	require.Equal(t, []string{"dry-run", "initialize", "apply", "start-private"}, lifecycle.events)
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
	marker, err := readMarker(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, config.TargetID, marker.TargetID)
	require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
	require.Equal(t, "sha256-"+strings.Repeat("a", 64), marker.Generation)
	markerBytes, err := os.ReadFile(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	operatorContents, err := os.ReadFile(paths.OperatorConfig)
	require.NoError(t, err)
	for _, secret := range []string{"control-migrator-secret", "ducklake-migrator-secret"} {
		require.NotContains(t, string(markerBytes), secret)
		require.Contains(t, string(operatorContents), secret)
	}
	require.Equal(t, "postgres://leapview_control_migrator:control-migrator-secret@db.example/leapview_control?sslmode=verify-full", lifecycle.controlURL)
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
	require.Equal(t, []string{"dry-run", "initialize", "apply", "start-private", "start"}, lifecycle.events)
}

func TestInstallStopsAtEachBootstrapFailureAndKeepsOperatorInputForRetry(t *testing.T) {
	for _, failure := range []string{"dry-run", "initialize", "apply", "start"} {
		t.Run(failure, func(t *testing.T) {
			paths := testPaths(t)
			writeTestPayload(t, paths.Payload)
			config := Config{
				SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
				Environment: "prod", Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
				HTTPS: boolPointer(true),
			}
			writeConfig(t, paths.Config, config)
			writeOperatorConfig(t, paths.OperatorConfig)
			lifecycle := &recordingLifecycle{}
			wantEvents := map[string][]string{
				"dry-run":    {"dry-run"},
				"initialize": {"dry-run", "initialize"},
				"apply":      {"dry-run", "initialize", "apply"},
				"start":      {"dry-run", "initialize", "apply", "start-private"},
			}
			switch failure {
			case "dry-run":
				lifecycle.prepareErr = errors.New("dry-run failed")
			case "initialize":
				lifecycle.initializeErr = errors.New("initialize failed")
			case "apply":
				lifecycle.applyErr = errors.New("apply failed")
			case "start":
				lifecycle.startErr = errors.New("start failed")
			}
			installer, err := New(Options{
				Paths:            paths,
				LifecycleFactory: func(string) (Lifecycle, error) { return lifecycle, nil },
				Run:              func(context.Context, string, ...string) error { return nil },
			})
			require.NoError(t, err)
			require.Error(t, installer.Install(t.Context()))
			require.Equal(t, wantEvents[failure], lifecycle.events)
			_, err = os.Stat(paths.OperatorConfig)
			require.NoError(t, err, "operator input must remain available for a retry")
			_, err = os.Stat(filepath.Join(paths.Root, installMarkerName))
			if failure == "start" {
				require.NoError(t, err, "initialized host must persist its private phase before service startup")
				marker, markerErr := readMarker(filepath.Join(paths.Root, installMarkerName))
				require.NoError(t, markerErr)
				require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
			} else {
				require.True(t, os.IsNotExist(err), "failed database initialization must not write the install marker")
			}
			lifecycle.prepareErr, lifecycle.initializeErr, lifecycle.applyErr, lifecycle.startErr = nil, nil, nil, nil
			require.NoError(t, installer.Install(t.Context()), "retry must recover the failed step without exposing the app")
			marker, markerErr := readMarker(filepath.Join(paths.Root, installMarkerName))
			require.NoError(t, markerErr)
			require.Equal(t, installationstate.PhasePrivate, marker.BootstrapPhase)
			if failure == "start" {
				require.Len(t, lifecycle.initialize, 1, "service retry must not mint bootstrap credentials again")
			}

		})
	}
}

func TestFirstInstallRequiresPrivateOperatorInputBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	writeConfig(t, paths.Config, Config{
		SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS: boolPointer(true),
	})
	installer, err := New(Options{Paths: paths})
	require.NoError(t, err)
	err = installer.Install(t.Context())
	require.ErrorContains(t, err, "operator bootstrap configuration")
	_, statErr := os.Lstat(filepath.Join(paths.Root, "current"))
	require.True(t, os.IsNotExist(statErr))
	_, statErr = os.Lstat(filepath.Join(paths.Root, "releases"))
	require.True(t, os.IsNotExist(statErr))
}

func TestOperatorBootstrapRejectsDuplicateOrUnboundedInputWithoutEchoingSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator-bootstrap.json")
	for name, contents := range map[string]string{
		"duplicate keys":         `{"schemaVersion":1,"schemaVersion":1,"controlMigratorUrl":"control-migrator-secret"}`,
		"malformed secret input": `{"schemaVersion":1,"controlMigratorUrl":"control-migrator-secret`,
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			_, _, err := readAndValidateOperatorBootstrap(path)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "control-migrator-secret")
		})
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(" ", maxOperatorBootstrapBytes+1)), 0o600))
	_, _, err := readAndValidateOperatorBootstrap(path)
	require.ErrorContains(t, err, "size limit")
}

func TestOperatorBootstrapRejectsCaseFoldedDuplicateKeys(t *testing.T) {
	for name, duplicate := range map[string]struct {
		canonical string
		alias     string
	}{
		"ASCII case alias": {
			canonical: `"controlMigratorUrl":"postgres://leapview_control_migrator:control-migrator-secret@db.example/leapview_control?sslmode=verify-full",`,
			alias:     `"ControlMigratorUrl":"postgres://leapview_control_migrator:control-migrator-secret@db.example/leapview_control?sslmode=verify-full",`,
		},
		"Unicode simple-fold alias": {
			canonical: `"schemaVersion":1,`,
			alias:     `"ſchemaVersion":1,`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator-bootstrap.json")
			writeOperatorConfig(t, path)
			contents, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(contents), duplicate.canonical)
			contents = []byte(strings.Replace(string(contents), duplicate.canonical, duplicate.canonical+duplicate.alias, 1))
			require.NoError(t, os.WriteFile(path, contents, 0o600))

			_, _, err = readAndValidateOperatorBootstrap(path)
			require.ErrorContains(t, err, "not strict JSON")
		})
	}
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
	writeOperatorConfig(t, paths.OperatorConfig)
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
	writeOperatorConfig(t, paths.OperatorConfig)
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
		Payload:        filepath.Join(base, "payload"),
		Config:         filepath.Join(base, "bootstrap.json"),
		OperatorConfig: filepath.Join(base, "operator-bootstrap.json"),
		Root:           filepath.Join(base, "opt", "leapview"),
		ConfigDir:      filepath.Join(base, "etc", "leapview"),
		SystemBin:      filepath.Join(base, "usr", "local", "sbin"),
		Systemd:        filepath.Join(base, "etc", "systemd", "system"),
		Systemctl:      filepath.Join(base, "bin", "systemctl"),
	}
}

func writeOperatorConfig(t *testing.T, path string) {
	t.Helper()
	compatibility := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:test", DuckLakeExtension: "ducklake:test", CatalogFormat: "ducklake-catalog:v1",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: compatibility, ConformanceVersion: "test/v1",
		Checks: []physicalpool.EvidenceCheck{{ID: "shared_pool_check", Passed: true, ObservationDigest: "sha256:" + strings.Repeat("0", 64)}},
	})
	require.NoError(t, err)
	operator := OperatorBootstrap{
		SchemaVersion: 1,
		Postgres: composectl.FirstInstallPostgres{
			ControlURL:             "postgres://leapview_control_runtime:control-runtime-secret@db.example/leapview_control?sslmode=verify-full",
			ControlMigratorURL:     "postgres://leapview_control_migrator:control-migrator-secret@db.example/leapview_control?sslmode=verify-full",
			ControlMaintenanceURL:  "postgres://leapview_control_maintenance:control-maintenance-secret@db.example/leapview_control?sslmode=verify-full",
			DuckLakeURL:            "postgres://leapview_ducklake_runtime:ducklake-runtime-secret@db.example/leapview_ducklake?sslmode=verify-full",
			DuckLakeMaintenanceURL: "postgres://leapview_ducklake_maintenance:ducklake-maintenance-secret@db.example/leapview_ducklake?sslmode=verify-full",
			DuckLakeMigratorURL:    "postgres://leapview_ducklake_migrator:ducklake-migrator-secret@db.example/leapview_ducklake?sslmode=verify-full",
		},
		PhysicalPool: composectl.FirstInstallPhysicalPool{
			Pool: physicalpool.PoolIdentity{
				StorageLocation: "/var/lib/leapview/home/data", StorageNamespace: "delivery",
				EncryptionDomain: "production", IsolationBoundary: "tenant", RetentionAuthority: "operator",
				RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600},
				Compatibility:   compatibility,
			},
			Evidence: physicalpool.EvidenceArtifact{SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion, Evidence: evidence},
		},
	}
	encoded, err := json.Marshal(operator)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
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

func writeInstallationMarker(t *testing.T, root string, config Config, phase string) {
	t.Helper()
	marker, err := installationstate.NewMarker(config, phase)
	require.NoError(t, err)
	require.NoError(t, installationstate.WriteMarker(root, marker))
}
