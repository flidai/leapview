package composectl

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/stretchr/testify/require"
)

func firstInstallOptionsFixture(t *testing.T) FirstInstallOptions {
	t.Helper()
	postgres := qualificationNativeEnvironmentTopologyFixture()
	pool := newQualificationNativePoolFixture(t)
	artifacts, err := adminoffline.UnmarshalQualificationPoolArtifacts(pool.output)
	require.NoError(t, err)
	return FirstInstallOptions{
		Postgres: FirstInstallPostgres{
			ControlURL: postgres.ControlURL, ControlMigratorURL: postgres.ControlMigratorURL,
			ControlMaintenanceURL: postgres.ControlMaintenanceURL,
			DuckLakeURL:           postgres.DuckLakeURL, DuckLakeMaintenanceURL: postgres.DuckLakeMaintenanceURL,
			DuckLakeMigratorURL: postgres.DuckLakeMigratorURL,
		},
		PhysicalPool: FirstInstallPhysicalPool{Pool: artifacts.Pool, Evidence: artifacts.Evidence},
	}
}

func TestPrepareFirstInstallStoresComposeSafeURLWithoutChangingPassword(t *testing.T) {
	options := firstInstallOptionsFixture(t)
	options.Postgres.ControlURL = "postgres://leapview_control_runtime:runtime$'secret@db.example/leapview_control?sslmode=verify-full&options=-capplication_name%3D%24app"
	pool := qualificationNativePoolFixture{
		identity: options.PhysicalPool.Pool, evidence: options.PhysicalPool.Evidence.Evidence,
	}
	dryRun := qualificationNativePoolBootstrapOutput(pool.identity, pool.evidence, false)
	controller, _ := newQualificationNativePoolController(t, dryRun)
	require.NoError(t, controller.PrepareFirstInstall(t.Context(), options))
	contents, err := os.ReadFile(filepath.Join(controller.root, appEnvName))
	require.NoError(t, err)
	values := environmentValues(string(contents))
	value := values["LEAPVIEW_POSTGRES_CONTROL_URL"]
	require.NotContains(t, value, "$")
	parsed, err := url.Parse(value)
	require.NoError(t, err)
	password, present := parsed.User.Password()
	require.True(t, present)
	require.Equal(t, "runtime$'secret", password)
	query, err := url.ParseQuery(parsed.RawQuery)
	require.NoError(t, err)
	require.Equal(t, "-capplication_name=$app", query.Get("options"))
}

func TestFirstInstallRunsCanonicalPoolDryRunAndApplyWithEphemeralMigrators(t *testing.T) {
	options := firstInstallOptionsFixture(t)
	pool := qualificationNativePoolFixture{
		identity: options.PhysicalPool.Pool, evidence: options.PhysicalPool.Evidence.Evidence,
	}
	dryRun := qualificationNativePoolBootstrapOutput(pool.identity, pool.evidence, false)
	apply := qualificationNativePoolBootstrapOutput(pool.identity, pool.evidence, true)
	controller, executor := newQualificationNativePoolController(t, dryRun, apply)
	require.NoError(t, controller.PrepareFirstInstall(t.Context(), options))

	serving, err := os.ReadFile(filepath.Join(controller.root, appEnvName))
	require.NoError(t, err)
	for _, secret := range []string{options.Postgres.ControlMigratorURL, options.Postgres.DuckLakeMigratorURL} {
		require.NotContains(t, string(serving), secret)
	}
	values := environmentValues(string(serving))
	require.Equal(t, options.Postgres.ControlURL, values["LEAPVIEW_POSTGRES_CONTROL_URL"])
	require.NotEmpty(t, values["LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID"])
	require.Len(t, executor.requests, 1)
	require.NotContains(t, strings.Join(executor.requests[0].Environment, "\n"), options.Postgres.ControlMigratorURL)
	require.NotContains(t, strings.Join(executor.requests[0].Environment, "\n"), options.Postgres.DuckLakeMigratorURL)

	require.NoError(t, controller.ApplyFirstInstall(t.Context(), options))
	require.Len(t, executor.requests, 2)
	request := executor.requests[1]
	requestEnvironment := strings.Join(request.Environment, "\n")
	for _, secret := range []string{options.Postgres.ControlMigratorURL, options.Postgres.DuckLakeMigratorURL} {
		require.Contains(t, requestEnvironment, secret)
		require.NotContains(t, strings.Join(request.Arguments, " "), secret)
	}
	require.Contains(t, request.Arguments, "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	require.Contains(t, request.Arguments, "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL")
	serving, err = os.ReadFile(filepath.Join(controller.root, appEnvName))
	require.NoError(t, err)
	require.NotContains(t, string(serving), options.Postgres.ControlMigratorURL)
	require.NotContains(t, string(serving), options.Postgres.DuckLakeMigratorURL)
}

func TestInitializeFirstInstallPassesControlMigratorOnlyToInitializer(t *testing.T) {
	root := t.TempDir()
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("a", 64)
	caddy := "ghcr.io/library/caddy@sha256:" + strings.Repeat("b", 64)
	require.NoError(t, os.WriteFile(filepath.Join(root, deploymentEnvName), []byte(
		"COMPOSE_PROJECT_NAME=host-install-test\nCOMPOSE_HTTPS=1\nCOMPOSE_APP_BIND=127.0.0.1:8080\nLEAPVIEW_IMAGE="+image+"\nCADDY_IMAGE="+caddy+"\nCADDY_DOMAIN=dash.example.com\n",
	), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, appEnvName), []byte(
		"LEAPVIEW_POSTGRES_CONTROL_URL=postgres://leapview_control_runtime:runtime@db.example/leapview_control?sslmode=verify-full\n"+
			"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=\nLEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE=leapview_control_migrator\n"+
			"LEAPVIEW_POSTGRES_DUCKLAKE_URL=postgres://leapview_ducklake_runtime:ducklake@db.example/leapview_ducklake?sslmode=verify-full\n"+
			"LEAPVIEW_POSTGRES_REQUIRE_TLS=true\n",
	), 0o600))
	secret := "postgres://leapview_control_migrator:control-secret@db.example/leapview_control?sslmode=verify-full"
	var calls [][]string
	controller, err := New(Options{Root: root, qualificationExecutor: &recordingQualificationExecutor{}})
	require.NoError(t, err)
	controller.composeOverride = func(_ context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if strings.Contains(strings.Join(args, " "), "admin initialize --format json") {
			_, err := fmt.Fprintln(stdout, `{"email":"admin@example.com","temporaryPassword":"temporary","projectClaimToken":"claim","projectClaimTokenExpiresAt":"2026-10-06T00:00:00Z"}`)
			return err
		}
		return nil
	}
	require.NoError(t, controller.InitializeFirstInstall(t.Context(), InitOptions{
		AdminEmail: "admin@example.com", Domain: "dash.example.com", Environment: "prod", Image: image,
	}, secret))

	joined := strings.Join(flattenArguments(calls), " ")
	require.Contains(t, joined, "--env LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL")
	require.NotContains(t, joined, secret)
	contents, err := os.ReadFile(filepath.Join(root, appEnvName))
	require.NoError(t, err)
	require.NotContains(t, string(contents), "control-secret")
	processEnvironment, err := composeProcessEnvironment(root, map[string]string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL": secret,
	})
	require.NoError(t, err)
	require.Contains(t, strings.Join(processEnvironment, "\n"), "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL="+secret)
}

func flattenArguments(arguments [][]string) []string {
	var flattened []string
	for _, args := range arguments {
		flattened = append(flattened, args...)
	}
	return flattened
}
