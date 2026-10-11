package composectl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/stretchr/testify/require"
)

// This exercises the installed image, real Compose executor, PostgreSQL roles,
// pool probe, production configuration validation, and control initialization.
// Raw diagnostics remain in a private file because initialization issues secrets.
func TestBundledPostgresDockerInitializesFirstInstall(t *testing.T) {
	if os.Getenv("LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER") != "1" {
		t.Skip("set LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1 and LEAPVIEW_TEST_BUNDLED_POSTGRES_IMAGE to run real first-install initialization")
	}
	image := os.Getenv("LEAPVIEW_TEST_BUNDLED_POSTGRES_IMAGE")
	if image == "" {
		t.Skip("an explicit immutable released application image is required")
	}
	require.NoError(t, requireDigest(image), "the explicitly supplied application image must be immutable")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	project := fmt.Sprintf("lvpginit-%x", time.Now().UnixNano())
	diagnostics, err := os.CreateTemp("/tmp", "leapview-bundled-initialize-private-*.log")
	require.NoError(t, err)
	require.NoError(t, diagnostics.Chmod(0o600))
	t.Cleanup(func() { _ = diagnostics.Close() })
	root, _, controller := newBundledPostgresDockerProjectWithDiagnostics(t, repository, project, diagnostics)
	controller.stdout, controller.stderr = diagnostics, diagnostics
	check := func(stage string, err error) {
		t.Helper()
		if err != nil {
			_, _ = fmt.Fprintf(diagnostics, "stage=%s error=%v\n", stage, err)
			_ = diagnostics.Sync()
			t.Fatalf("real bundled first-install failed at %s; private diagnostics: %s", stage, diagnostics.Name())
		}
	}
	deployment, err := os.ReadFile(filepath.Join(repository, "deploy", "compose", "deployment.env.example"))
	check("deployment-template", err)
	check("deployment-template", os.WriteFile(filepath.Join(root, deploymentEnvName), deployment, 0o600))
	for key, value := range map[string]string{"COMPOSE_PROJECT_NAME": project, "COMPOSE_HTTPS": "0", "LEAPVIEW_IMAGE": image} {
		check("deployment-environment", appendOrReplaceEnvFile(filepath.Join(root, deploymentEnvName), key, value))
	}
	app, err := os.ReadFile(filepath.Join(repository, "deploy", "compose", "leapview.env.example"))
	check("application-template", err)
	check("application-template", os.WriteFile(filepath.Join(root, appEnvName), app, 0o600))
	postgres, err := controller.ensureBundledPostgres(ctx)
	check("postgres-provision", err)
	serving, err := postgresServingEnvironment(postgres.ControlURL, postgres.ControlMaintenanceURL, postgres.DuckLakeURL, postgres.DuckLakeMaintenanceURL)
	check("postgres-serving", err)
	check("postgres-serving", writePostgresServingEnvironment(filepath.Join(root, appEnvName), serving,
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL", "LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"))
	artifacts, err := controller.prepareQualificationNativePhysicalPool(ctx, filepath.Join(root, ".real-pool-probe"))
	check("physical-pool-probe", err)
	bootstrap := FirstInstallOptions{Profile: FirstInstallPostgresBundled, PhysicalPool: FirstInstallPhysicalPool{
		Pool: artifacts.Pool, Evidence: physicalpool.EvidenceArtifact{SchemaVersion: physicalpool.EvidenceArtifactSchemaVersion, Evidence: artifacts.Evidence},
	}}
	check("first-install-prepare", controller.PrepareFirstInstall(ctx, bootstrap))
	check("first-install-initialize", controller.InitializeFirstInstall(ctx, InitOptions{
		AdminEmail: "qualification@example.com", Domain: "qualification.example.com", Environment: "qualification", Image: image, NoHTTPS: true,
	}, bootstrap))
	require.FileExists(t, filepath.Join(root, credentialsName))
	roles, err := bundledPostgresQuery(ctx, root, "leapview_control", `
		SELECT count(*) FROM pg_roles WHERE rolname IN ('leapview_control_readonly', 'leapview_control_backup')
		AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit`)
	check("restricted-control-roles", err)
	require.Equal(t, "2", roles, "baseline authorities must remain non-login roles without elevated privileges")
	check("first-install-retry", controller.InitializeFirstInstall(ctx, InitOptions{
		AdminEmail: "qualification@example.com", Domain: "qualification.example.com", Environment: "qualification", Image: image, NoHTTPS: true,
	}, bootstrap))
}
