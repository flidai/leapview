package composectl

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type nativePostgresRuntimeFixture struct {
	request        qualificationContainerRequest
	container      *nativePostgresContainerFixture
	startErr       error
	nilStart       bool
	initialExecErr error
}

func (runtime *nativePostgresRuntimeFixture) Start(_ context.Context, request qualificationContainerRequest) (qualificationContainer, error) {
	if runtime.startErr != nil {
		return nil, runtime.startErr
	}
	runtime.request = request
	if runtime.nilStart {
		return nil, nil
	}
	runtime.container = &nativePostgresContainerFixture{execErr: runtime.initialExecErr}
	return runtime.container, nil
}

func (runtime *nativePostgresRuntimeFixture) Existing(string) qualificationContainer {
	return runtime.container
}

type nativePostgresContainerFixture struct {
	probes     []string
	removed    int
	removeErrs []error
	execErr    error
	execErrors []error
	execOutput []byte
}

func (*nativePostgresContainerFixture) Name() string { return "qualification-postgres" }

func (container *nativePostgresContainerFixture) Exec(_ context.Context, _ io.Reader, command ...string) ([]byte, error) {
	container.probes = append(container.probes, strings.Join(command, " "))
	if len(container.execErrors) > 0 {
		err := container.execErrors[0]
		container.execErrors = container.execErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if container.execErr != nil {
		return nil, container.execErr
	}
	if container.execOutput != nil {
		return append([]byte(nil), container.execOutput...), nil
	}
	return []byte("1\n"), nil
}

func TestWaitQualificationNativePostgresTopologyClearsTransientProbeFailure(t *testing.T) {
	container := &nativePostgresContainerFixture{
		execErrors: []error{errors.New("postgres is still starting")},
	}
	credentials := qualificationNativePostgresCredentials{
		controlRuntime:  strings.Repeat("a", 48),
		duckLakeRuntime: strings.Repeat("b", 48),
	}

	require.NoError(t, waitQualificationNativePostgresTopology(t.Context(), container, credentials))
	require.Len(t, container.probes, 3)
}

func TestQualificationNativePostgresBootstrapInvariant(t *testing.T) {
	container := &nativePostgresContainerFixture{execOutput: []byte("0\n")}
	topology := &qualificationNativePostgresTopology{Container: container}
	require.NoError(t, topology.AssertBootstrapOpen(t.Context(), "initialization"))
	require.Len(t, container.probes, 1)
	require.Contains(t, container.probes[0], "LEAPVIEW_POSTGRES_CONTROL_READONLY_PASSWORD")
	require.NotContains(t, container.probes[0], "postgres://")

	container.execOutput = []byte("1\n")
	err := topology.AssertBootstrapOpen(t.Context(), "application startup")
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed before candidate publication")
}

func TestQualificationNativePostgresNativeDeliveryReadInvariant(t *testing.T) {
	container := &nativePostgresContainerFixture{
		execOutput: []byte("leapview_control_runtime|leapview_control|true|true\n"),
	}
	topology := &qualificationNativePostgresTopology{Container: container}
	require.NoError(t, topology.AssertNativeDeliveryReads(t.Context()))
	require.Len(t, container.probes, 1)
	require.Contains(t, container.probes[0], "LEAPVIEW_POSTGRES_CONTROL_RUNTIME_PASSWORD")
	require.Contains(t, container.probes[0], "PGSSLMODE=require")
	require.Contains(t, container.probes[0], "ducklake.catalog_identity")
	require.Contains(t, container.probes[0], "LIMIT 0")
	require.NotContains(t, container.probes[0], "postgres://")

	container.execOutput = []byte("leapview_control_runtime|leapview_control|false|false\n")
	err := topology.AssertNativeDeliveryReads(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "read boundary")

	container.execOutput = []byte("Authorization: Bearer unexpected-secret\n")
	err = topology.AssertNativeDeliveryReads(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "unexpected-secret")
}

func (*nativePostgresContainerFixture) CopyTo(context.Context, string, string) ([]byte, error) {
	return nil, nil
}
func (*nativePostgresContainerFixture) Restart(context.Context) ([]byte, error)      { return nil, nil }
func (*nativePostgresContainerFixture) Kill(context.Context, string) ([]byte, error) { return nil, nil }
func (*nativePostgresContainerFixture) Start(context.Context) ([]byte, error)        { return nil, nil }
func (*nativePostgresContainerFixture) Inspect(context.Context, string) ([]byte, error) {
	return []byte("running"), nil
}
func (*nativePostgresContainerFixture) Logs(context.Context, int) ([]byte, error) { return nil, nil }
func (container *nativePostgresContainerFixture) Remove(context.Context) ([]byte, error) {
	container.removed++
	if len(container.removeErrs) > 0 {
		err := container.removeErrs[0]
		container.removeErrs = container.removeErrs[1:]
		return nil, err
	}
	return nil, nil
}

func qualificationNativePostgresBundleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "qualification")
	require.NoError(t, os.MkdirAll(path, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "postgres-init.sh"), []byte("#!/usr/bin/env bash\n"), 0o700))
	return root
}

func TestQualificationNativePostgresTopologyStartsPinnedTLSNetworkSidecar(t *testing.T) {
	runtime := &nativePostgresRuntimeFixture{}
	topology, err := newQualificationNativePostgresTopology(t.Context(), runtime, qualificationNativePostgresTopologyOptions{
		ComposeProject: "leapview-qualification",
		ComposeNetwork: "leapview-qualification_default",
		BundleRoot:     qualificationNativePostgresBundleFixture(t),
	})
	require.NoError(t, err)
	require.NotNil(t, topology)
	require.NotNil(t, runtime.container)
	t.Cleanup(func() { require.NoError(t, topology.Remove(context.Background())) })

	require.Equal(t, qualificationPostgreSQL18Image, runtime.request.Image)
	require.Equal(t, "leapview-qualification_default", runtime.request.NetworkMode)
	require.Equal(t, []string{"sh"}, runtime.request.Entrypoint)
	require.True(t, runtime.request.NoHealth)
	require.Len(t, runtime.request.Volumes, 4)
	for _, volume := range runtime.request.Volumes {
		require.True(t, volume.ReadOnly, "all init/TLS mounts must be read-only")
	}
	require.Contains(t, runtime.request.Command[1], "ssl=on")
	require.Contains(t, runtime.request.Command[1], "ssl_ca_file=")
	require.Contains(t, runtime.request.Command[1], "ssl_cert_file=")
	require.Contains(t, runtime.request.Command[1], "ssl_key_file=")
	require.Contains(t, runtime.request.Command[1], "log_line_prefix=")
	require.Contains(t, runtime.request.Command[1], "chmod 0600")
	require.Equal(t, []string{
		"/var/lib/postgresql:rw,exec,nosuid,nodev,size=512m",
		"/tmp:rw,nosuid,nodev,mode=1777,size=64m",
	}, runtime.request.Tmpfs)
	initVolume := runtime.request.Volumes[0]
	require.Equal(t, filepath.Join(filepath.Dir(filepath.Dir(initVolume.Source)), "qualification", "postgres-init.sh"), initVolume.Source)
	initInfo, err := os.Stat(initVolume.Source)
	require.NoError(t, err)
	// Bundles assembled under a restrictive umask can preserve 0700 here.
	// The PostgreSQL image executes the hook as its unprivileged postgres user,
	// so topology startup must normalize the host mount to the reviewed 0755
	// mode before handing it to Docker.
	require.Equal(t, os.FileMode(0o755), initInfo.Mode().Perm())

	for _, probe := range []string{"leapview_control", "leapview_ducklake"} {
		found := false
		for _, command := range runtime.container.probes {
			if strings.Contains(command, "PGSSLMODE=require") && strings.Contains(command, "--dbname "+probe) && strings.Contains(command, "--command 'SELECT 1'") {
				found = true
				break
			}
		}
		require.Truef(t, found, "missing authenticated TLS probe for %s: %v", probe, runtime.container.probes)
	}
	for _, connectionURL := range []string{
		topology.ControlURL, topology.ControlReadonlyURL, topology.ControlMaintenanceURL,
		topology.ControlMigratorURL, topology.ControlUpgradeCoordinatorURL,
		topology.DuckLakeURL, topology.DuckLakeMaintenanceURL, topology.DuckLakeMigratorURL,
	} {
		parsed, parseErr := url.Parse(connectionURL)
		require.NoError(t, parseErr)
		require.Equal(t, "require", parsed.Query().Get("sslmode"))
		require.NotEmpty(t, parsed.User.Username())
		password, present := parsed.User.Password()
		require.True(t, present)
		require.Len(t, password, 48)
	}
	require.Equal(t, qualificationNativePostgresControlRuntimeRole, topology.ControlRuntimeRole)
	require.Equal(t, qualificationNativePostgresControlMigratorRole, topology.ControlMigratorRole)
	require.Equal(t, qualificationNativePostgresDuckLakeRuntimeRole, topology.DuckLakeRuntimeRole)
	require.Equal(t, qualificationNativePostgresDuckLakeMigratorRole, topology.DuckLakeMigratorRole)
}

func TestQualificationNativePostgresTopologyValidationAndCleanup(t *testing.T) {
	bundleRoot := qualificationNativePostgresBundleFixture(t)
	for name, options := range map[string]qualificationNativePostgresTopologyOptions{
		"missing project": {ComposeNetwork: "network", BundleRoot: bundleRoot},
		"missing network": {ComposeProject: "project", BundleRoot: bundleRoot},
		"missing root":    {ComposeProject: "project", ComposeNetwork: "network"},
		"missing init":    {ComposeProject: "project", ComposeNetwork: "network", BundleRoot: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newQualificationNativePostgresTopology(t.Context(), &nativePostgresRuntimeFixture{}, options)
			require.Error(t, err)
		})
	}

	runtime := &nativePostgresRuntimeFixture{}
	topology, err := newQualificationNativePostgresTopology(t.Context(), runtime, qualificationNativePostgresTopologyOptions{
		ComposeProject: "project",
		ComposeNetwork: "project_default",
		BundleRoot:     bundleRoot,
	})
	require.NoError(t, err)
	secretDir := runtime.request.Volumes[1].Source
	require.DirExists(t, filepath.Dir(secretDir))
	require.NoError(t, topology.Remove(t.Context()))
	require.NoError(t, topology.Remove(t.Context()))
	require.Equal(t, 1, runtime.container.removed)
	_, err = os.Stat(filepath.Dir(secretDir))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestEnsureQualificationNativePostgresInitScriptExecutableRejectsSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "target.sh")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o600))
	link := filepath.Join(t.TempDir(), "postgres-init.sh")
	require.NoError(t, os.Symlink(target, link))

	err := ensureQualificationNativePostgresInitScriptExecutable(link)
	require.ErrorContains(t, err, "is not a regular file")

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestQualificationNativePostgresTopologyPreservesHandlesAfterRemovalFailure(t *testing.T) {
	bundleRoot := qualificationNativePostgresBundleFixture(t)
	removeErr := errors.New("docker removal failed")
	runtime := &nativePostgresRuntimeFixture{}
	topology, err := newQualificationNativePostgresTopology(t.Context(), runtime, qualificationNativePostgresTopologyOptions{
		ComposeProject: "project",
		ComposeNetwork: "project_default",
		BundleRoot:     bundleRoot,
	})
	require.NoError(t, err)
	secretDir := topology.secretDir
	runtime.container.removeErrs = []error{removeErr, nil}

	err = topology.Remove(t.Context())
	require.ErrorIs(t, err, removeErr)
	require.Same(t, runtime.container, topology.Container)
	require.Equal(t, secretDir, topology.secretDir)
	require.DirExists(t, secretDir)

	require.NoError(t, topology.Remove(t.Context()))
	require.Nil(t, topology.Container)
	require.Empty(t, topology.secretDir)
	require.Equal(t, 2, runtime.container.removed)
	_, statErr := os.Stat(secretDir)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestQualificationNativePostgresTopologyCleansUpStartAndReadinessFailures(t *testing.T) {
	bundleRoot := qualificationNativePostgresBundleFixture(t)
	options := qualificationNativePostgresTopologyOptions{
		ComposeProject: "project", ComposeNetwork: "project_default", BundleRoot: bundleRoot,
	}
	startErr := errors.New("start failed")
	_, err := newQualificationNativePostgresTopology(t.Context(), &nativePostgresRuntimeFixture{startErr: startErr}, options)
	require.ErrorIs(t, err, startErr)

	runtime := &nativePostgresRuntimeFixture{nilStart: true}
	_, err = newQualificationNativePostgresTopology(t.Context(), runtime, options)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil container")

	runtime = &nativePostgresRuntimeFixture{initialExecErr: errors.New("psql failed")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, readinessErr := newQualificationNativePostgresTopology(ctx, runtime, options)
	require.Error(t, readinessErr)
	require.Equal(t, 1, runtime.container.removed)
	secretDir := runtime.request.Volumes[1].Source
	_, statErr := os.Stat(filepath.Dir(secretDir))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestQualificationNativePostgresProbeRedactsCredentialDiagnostics(t *testing.T) {
	secret := strings.Repeat("a", 48)
	message := qualificationNativePostgresProbe("leapview_control", qualificationNativePostgresControlRuntimeRole, secret)
	require.Contains(t, message, "PGSSLMODE=require")
	require.Contains(t, message, "PGPASSWORD="+secret)

	redacted := string(redactQualificationBytes([]byte(message)))
	require.NotContains(t, redacted, secret)
	require.Contains(t, redacted, "[REDACTED]")
}
