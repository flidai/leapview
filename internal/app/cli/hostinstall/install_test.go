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
	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/stretchr/testify/require"
)

type recordingLifecycle struct {
	initialize []composectl.InitOptions
	starts     int
	qualifies  int
}

func (l *recordingLifecycle) Initialize(_ context.Context, options composectl.InitOptions) error {
	l.initialize = append(l.initialize, options)
	return nil
}

func (l *recordingLifecycle) Start(context.Context) error {
	l.starts++
	return nil
}

func (l *recordingLifecycle) RunScheduledRecoveryQualification(context.Context) error {
	l.qualifies++
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
	require.Equal(t, 1, lifecycle.qualifies)
	require.Equal(t, [][]string{
		{paths.Systemctl, "daemon-reload"},
		{paths.Systemctl, "enable", "--now", "leapview-backup.timer"},
		{paths.Systemctl, "enable", "--now", "leapview-backup-maintenance.timer"},
		{paths.Systemctl, "enable", "--now", "leapview-recovery-qualification.timer"},
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
		filepath.Join(paths.Systemd, "leapview-recovery-qualification.service"),
		filepath.Join(paths.Systemd, "leapview-recovery-qualification.timer"),
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
	require.Equal(t, 2, lifecycle.qualifies)
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

func TestInstallRejectsLegacyPayloadWithoutQualificationBeforeMutation(t *testing.T) {
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	for _, file := range qualificationPayloadFiles {
		require.NoError(t, os.Remove(filepath.Join(paths.Payload, file.Source)))
	}
	config := Config{
		SchemaVersion: 1,
		Domain:        "dash.example.com",
		AdminEmail:    "admin@example.com",
		Environment:   "prod",
		Image:         "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64),
		HTTPS:         boolPointer(true),
	}
	writeConfig(t, paths.Config, config)
	installer, err := New(Options{Paths: paths})
	require.NoError(t, err)

	err = installer.Install(t.Context())
	require.ErrorContains(t, err, "complete recovery qualification bundle is required")
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
	require.ErrorContains(t, err, "unexpected symbolic link")
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
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, "leapview.env"), []byte("LEAPVIEW_PRODUCTION=1\n"), 0o600))
	nextPayload := map[string][]byte{}
	for _, file := range requiredPayloadFiles {
		nextPayload[file.Source] = []byte("next-" + file.Source + "\n")
	}
	nextPayload["deployment.env.example"] = []byte(
		"LEAPVIEW_IMAGE=example.invalid/leapview@sha256:" + strings.Repeat("e", 64) +
			"\nCADDY_IMAGE=" + nextCaddy + "\nCOMPOSE_HTTPS=1\n",
	)
	nextPolicy := testPolicyDocument(t, next)
	nextPayload["release-transition-policy.json"] = nextPolicy
	manager, err := NewDeploymentPayloadManager(DeploymentPayloadManagerOptions{
		Paths: paths,
		Load: func(context.Context, string) (map[string][]byte, error) {
			return nextPayload, nil
		},
		Run:         func(context.Context, string, ...string) error { return nil },
		UnitEnabled: func(context.Context, string) (bool, error) { return true, nil },
	})
	require.NoError(t, err)
	update, err := manager.Prepare(t.Context(), current, next, "linux/amd64", nextPolicy)
	require.NoError(t, err)
	require.NotNil(t, update)
	t.Cleanup(func() { require.NoError(t, update.Close()) })

	require.NoError(t, update.Apply())
	active, err := os.Readlink(filepath.Join(paths.Root, "current"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join("releases", "sha256-"+strings.Repeat("b", 64)), active)
	contents, err := os.ReadFile(filepath.Join(paths.Root, "compose.yaml"))
	require.NoError(t, err)
	require.Equal(t, "next-compose.yaml\n", string(contents))
	environment, err := os.ReadFile(filepath.Join(paths.Root, "deployment.env"))
	require.NoError(t, err)
	require.Equal(t, "LEAPVIEW_IMAGE="+next+"\nCADDY_IMAGE="+nextCaddy+"\nCOMPOSE_HTTPS=1\n", string(environment))
	installed, err := readMarker(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.Equal(t, next, installed.Image)
	contents, err = os.ReadFile(filepath.Join(paths.Root, "release-transition-policy.json"))
	require.NoError(t, err)
	require.Equal(t, nextPolicy, contents)

	require.NoError(t, update.Rollback())
	active, err = os.Readlink(filepath.Join(paths.Root, "current"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join("releases", "sha256-"+strings.Repeat("a", 64)), active)
	contents, err = os.ReadFile(filepath.Join(paths.Root, "compose.yaml"))
	require.NoError(t, err)
	require.Equal(t, "compose.yaml\n", string(contents))
	environment, err = os.ReadFile(filepath.Join(paths.Root, "deployment.env"))
	require.NoError(t, err)
	require.Equal(t, currentEnvironment, string(environment))
	installed, err = readMarker(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.Equal(t, current, installed.Image)
	contents, err = os.ReadFile(filepath.Join(paths.Root, "release-transition-policy.json"))
	require.NoError(t, err)
	require.Equal(t, testPolicyDocument(t, current), contents)
}

func TestFAI515PayloadActivationMigratesQualificationAndRollbackRestoresLegacyState(t *testing.T) {
	paths := testPaths(t)
	current := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	next := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64)
	currentPayload := legacyPayload(testPayload("current-"))
	currentPayload["release-transition-policy.json"] = testPolicyDocument(t, current)
	currentPayload["deployment.env.example"] = []byte(
		"LEAPVIEW_IMAGE=" + current + "\nCADDY_IMAGE=caddy@sha256:" + strings.Repeat("c", 64) + "\nCOMPOSE_HTTPS=1\n",
	)
	nextPayload := testPayload("next-")
	nextPayload["release-transition-policy.json"] = testPolicyDocument(t, next)
	nextPayload["deployment.env.example"] = []byte(
		"LEAPVIEW_IMAGE=" + next + "\nCADDY_IMAGE=caddy@sha256:" + strings.Repeat("d", 64) + "\nCOMPOSE_HTTPS=1\n",
	)

	// Provision the exact payload layout known by the PR #368 controller.
	currentGeneration, err := stageGeneration(paths, current, currentPayload)
	require.NoError(t, err)
	require.NoError(t, ensurePayloadLinksFor(paths, legacyPayloadFiles))
	require.NoError(t, activateGeneration(paths, currentGeneration))
	currentDeploymentEnvironment := "LEAPVIEW_IMAGE=" + current + "\nCADDY_IMAGE=caddy@sha256:" + strings.Repeat("c", 64) + "\nCOMPOSE_HTTPS=1\n"
	currentApplicationEnvironment := "LEAPVIEW_PRODUCTION=1\nLEAPVIEW_ENVIRONMENT=prod\n"
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, "deployment.env"), []byte(currentDeploymentEnvironment), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, "leapview.env"), []byte(currentApplicationEnvironment), 0o600))
	marker, err := json.Marshal(Config{
		SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: current, HTTPS: boolPointer(true),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, installMarkerName), marker, 0o600))

	// Reproduce the old controller's activation of only its compiled file list.
	partialNextGeneration, err := stageGeneration(paths, next, legacyPayload(nextPayload))
	require.NoError(t, err)
	require.NoError(t, activateGeneration(paths, partialNextGeneration))
	nextMarker := Config{
		SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: next, HTTPS: boolPointer(true),
	}
	marker, err = json.Marshal(nextMarker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(paths.Root, installMarkerName), marker, 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(paths.Root, "deployment.env"),
		[]byte("LEAPVIEW_IMAGE="+next+"\nCADDY_IMAGE=caddy@sha256:"+strings.Repeat("d", 64)+"\nCOMPOSE_HTTPS=1\n"),
		0o600,
	))

	dockerPath := filepath.Join(t.TempDir(), "docker")
	require.NoError(t, os.WriteFile(dockerPath, []byte("#!/bin/sh\nprintf 'linux/amd64\\n'\n"), 0o700))
	unitEnabled := false
	var commands [][]string
	manager, err := NewDeploymentPayloadManager(DeploymentPayloadManagerOptions{
		Paths: paths, DockerBin: dockerPath,
		Load: func(_ context.Context, image string) (map[string][]byte, error) {
			if image == current {
				return currentPayload, nil
			}
			return nextPayload, nil
		},
		Run: func(_ context.Context, name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			if len(args) >= 3 && args[0] == "enable" && args[2] == "leapview-recovery-qualification.timer" {
				unitEnabled = true
			}
			if len(args) >= 3 && args[0] == "disable" && args[2] == "leapview-recovery-qualification.timer" {
				unitEnabled = false
			}
			return nil
		},
		UnitEnabled: func(context.Context, string) (bool, error) { return unitEnabled, nil },
	})
	require.NoError(t, err)

	require.NoError(t, manager.ReconcileCurrent(t.Context()))
	active, err := activeGeneration(paths)
	require.NoError(t, err)
	require.Equal(t, "sha256-"+strings.Repeat("b", 64)+"-qualification-v1", active)
	for _, file := range qualificationPayloadFiles {
		_, statErr := os.Stat(file.Target(paths))
		require.NoError(t, statErr, file.Source)
	}
	applicationEnvironment, err := os.ReadFile(filepath.Join(paths.Root, "leapview.env"))
	require.NoError(t, err)
	require.Contains(t, string(applicationEnvironment), "LEAPVIEW_RECOVERY_QUALIFICATION_ENABLED=true\n")
	require.Contains(t, string(applicationEnvironment), "LEAPVIEW_RECOVERY_QUALIFICATION_EXECUTION_ENVIRONMENT=host\n")
	require.True(t, unitEnabled)
	require.Contains(t, commands, []string{paths.Systemctl, "start", "leapview-recovery-qualification.service"})

	// The new controller can load the historical image, disable/remove FAI-516
	// host state, and restore the exact pre-migration application configuration.
	update, err := manager.Prepare(t.Context(), next, current, "linux/amd64", currentPayload["release-transition-policy.json"])
	require.NoError(t, err)
	require.NotNil(t, update)
	t.Cleanup(func() { require.NoError(t, update.Close()) })
	require.NoError(t, update.Apply())
	active, err = activeGeneration(paths)
	require.NoError(t, err)
	require.Equal(t, currentGeneration, active)
	applicationEnvironment, err = os.ReadFile(filepath.Join(paths.Root, "leapview.env"))
	require.NoError(t, err)
	require.Equal(t, currentApplicationEnvironment, string(applicationEnvironment))
	require.False(t, unitEnabled)
	for _, file := range qualificationPayloadFiles {
		_, statErr := os.Lstat(file.Target(paths))
		require.True(t, os.IsNotExist(statErr), file.Source)
	}
}

func TestReconcileCurrentRejectsMissingInstallationMetadata(t *testing.T) {
	paths := testPaths(t)
	require.NoError(t, os.MkdirAll(paths.Root, 0o700))
	manager, err := NewDeploymentPayloadManager(DeploymentPayloadManagerOptions{
		Paths: paths,
		Run:   func(context.Context, string, ...string) error { return nil },
	})
	require.NoError(t, err)

	err = manager.ReconcileCurrent(t.Context())
	require.ErrorContains(t, err, "host installation metadata is missing")
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
	payload := filepath.Join(filepath.Dir(path), "payload")
	if info, statErr := os.Stat(payload); statErr == nil && info.IsDir() {
		require.NoError(t, os.WriteFile(
			filepath.Join(payload, "release-transition-policy.json"),
			testPolicyDocument(t, config.Image),
			0o600,
		))
	}
}

func testPolicyDocument(t *testing.T, image string) []byte {
	t.Helper()
	denied := compatibility.Rule{
		ReasonCode:   compatibility.ReasonDeniedNoExplicitRule,
		Remediation:  "use an explicit transition",
		Requirements: []string{},
	}
	policy := &compatibility.Policy{
		SchemaVersion:    compatibility.CurrentSchemaVersion,
		PolicyVersion:    "test/host-policy-v1",
		CandidateRelease: "v1.0.0",
		Releases: []compatibility.Release{{
			ID: "v1.0.0", Version: "1.0.0", SourceRevision: strings.Repeat("a", 40), Distribution: "test",
			Artifacts: []compatibility.Artifact{{Platform: "linux/amd64", Image: image}}, LegacyMarkers: []string{},
			Defaults: compatibility.ReleaseDefaults{
				FreshInstall: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedFreshInstall, Requirements: []string{}},
				Upgrade:      denied, Rollback: denied,
			},
		}},
		Transitions: []compatibility.Transition{},
	}
	document, err := compatibility.MarshalPolicy(policy)
	require.NoError(t, err)
	return document
}

func boolPointer(value bool) *bool { return &value }
