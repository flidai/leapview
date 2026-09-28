package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/stretchr/testify/require"
)

type predecessorBootstrapLifecycle struct {
	events    []string
	applyErr  error
	verifyErr error
}

func (l *predecessorBootstrapLifecycle) PrepareRevision019(_ context.Context, _, _, image string) (composectl.Revision019BootstrapHandle, error) {
	if image != revision019PredecessorImage {
		return nil, errors.New("wrong predecessor image")
	}
	l.events = append(l.events, "tls-postgres-and-pool-dry-run")
	return predecessorBootstrapAdmission{l}, nil
}

func (l *predecessorBootstrapLifecycle) Initialize(context.Context, composectl.InitOptions) error {
	l.events = append(l.events, "ordinary-initialize")
	return nil
}

func (l *predecessorBootstrapLifecycle) InitializeRevision019FromPayload(context.Context, string, composectl.InitOptions) error {
	l.events = append(l.events, "predecessor-owned-initialize")
	return nil
}

func (l *predecessorBootstrapLifecycle) Start(context.Context) error {
	l.events = append(l.events, "ordinary-start")
	return nil
}

func (l *predecessorBootstrapLifecycle) StartRevision019Bootstrap(context.Context, bool) error {
	l.events = append(l.events, "predecessor-liveness-start")
	return nil
}

func (l *predecessorBootstrapLifecycle) VerifyRevision019Prerequisites(context.Context, string) error {
	l.events = append(l.events, "verify-provider-revision-and-pool")
	return l.verifyErr
}

type predecessorBootstrapAdmission struct {
	lifecycle *predecessorBootstrapLifecycle
}

func (a predecessorBootstrapAdmission) Apply(context.Context) error {
	a.lifecycle.events = append(a.lifecycle.events, "pool-admission")
	return a.lifecycle.applyErr
}

func newPredecessorBootstrapInstaller(t *testing.T, lifecycle *predecessorBootstrapLifecycle, bind bool) (*Installer, Paths) {
	t.Helper()
	paths := testPaths(t)
	writeTestPayload(t, paths.Payload)
	config := Config{SchemaVersion: 1, Domain: "dash.example.com", AdminEmail: "admin@example.com",
		Environment: "prod", Image: revision019PredecessorImage, HTTPS: boolPointer(true)}
	writeConfig(t, paths.Config, config)
	require.NoError(t, os.MkdirAll(paths.Root, 0o700))
	if bind {
		binding, err := json.Marshal(revision019Binding{SchemaVersion: 1, Image: revision019PredecessorImage,
			TargetID: "provisioned-target-019", LegacyConfig: config})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(paths.Root, revision019BindingName), binding, 0o600))
	}
	initScript := filepath.Join(t.TempDir(), "postgres-init.sh")
	require.NoError(t, os.WriteFile(initScript, []byte("#!/bin/sh\n"), 0o700))
	installer, err := New(Options{Paths: paths, ExpectedImage: revision019PredecessorImage,
		Revision019InitScript: initScript,
		LifecycleFactory:      func(string) (Lifecycle, error) { return lifecycle, nil }})
	require.NoError(t, err)
	return installer, paths
}

func TestPredecessorBootstrapOrdersProviderAdmissionBeforeStart(t *testing.T) {
	lifecycle := &predecessorBootstrapLifecycle{}
	installer, paths := newPredecessorBootstrapInstaller(t, lifecycle, true)
	require.NoError(t, installer.Install(t.Context()))
	require.Equal(t, []string{"tls-postgres-and-pool-dry-run", "predecessor-owned-initialize", "pool-admission", "verify-provider-revision-and-pool", "predecessor-liveness-start"}, lifecycle.events)
	installed, err := readMarker(filepath.Join(paths.Root, installMarkerName))
	require.NoError(t, err)
	require.NotNil(t, installed)
	require.Empty(t, installed.TargetID)
	bound, _, err := readUpgradeInstallation(paths.Root)
	require.NoError(t, err)
	require.Equal(t, "provisioned-target-019", bound.TargetID)
	require.NoError(t, installer.Install(t.Context()))
	require.Equal(t, []string{"tls-postgres-and-pool-dry-run", "predecessor-owned-initialize", "pool-admission",
		"verify-provider-revision-and-pool", "predecessor-liveness-start",
		"verify-provider-revision-and-pool", "predecessor-liveness-start"}, lifecycle.events)
}

func TestPredecessorBootstrapFailsClosedWithoutBindingOrAdmission(t *testing.T) {
	t.Run("missing provisioner binding", func(t *testing.T) {
		lifecycle := &predecessorBootstrapLifecycle{}
		installer, paths := newPredecessorBootstrapInstaller(t, lifecycle, false)
		err := installer.Install(t.Context())
		require.ErrorContains(t, err, "target binding")
		require.Empty(t, lifecycle.events)
		_, err = os.Stat(filepath.Join(paths.Root, installMarkerName))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("pool admission failure", func(t *testing.T) {
		lifecycle := &predecessorBootstrapLifecycle{applyErr: errors.New("admission rejected")}
		installer, paths := newPredecessorBootstrapInstaller(t, lifecycle, true)
		err := installer.Install(t.Context())
		require.ErrorContains(t, err, "admission rejected")
		require.Equal(t, []string{"tls-postgres-and-pool-dry-run", "predecessor-owned-initialize", "pool-admission"}, lifecycle.events)
		_, err = os.Stat(filepath.Join(paths.Root, installMarkerName))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("provider disappears before start", func(t *testing.T) {
		lifecycle := &predecessorBootstrapLifecycle{verifyErr: errors.New("provider unavailable")}
		installer, paths := newPredecessorBootstrapInstaller(t, lifecycle, true)
		err := installer.Install(t.Context())
		require.ErrorContains(t, err, "provider unavailable")
		require.Equal(t, []string{"tls-postgres-and-pool-dry-run", "predecessor-owned-initialize", "pool-admission",
			"verify-provider-revision-and-pool"}, lifecycle.events)
		_, err = os.Stat(filepath.Join(paths.Root, installMarkerName))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}
