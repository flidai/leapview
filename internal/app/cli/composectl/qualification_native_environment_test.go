package composectl

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func qualificationNativeEnvironmentTopologyFixture() *qualificationNativePostgresTopology {
	password := func(role string) string {
		parsed := url.URL{Scheme: "postgres", Host: "postgres", Path: "/" + map[string]string{
			qualificationNativePostgresControlRuntimeRole:      qualificationNativePostgresControlDatabase,
			qualificationNativePostgresControlReadonlyRole:     qualificationNativePostgresControlDatabase,
			qualificationNativePostgresControlMigratorRole:     qualificationNativePostgresControlDatabase,
			qualificationNativePostgresControlUpgradeRole:      qualificationNativePostgresControlDatabase,
			qualificationNativePostgresControlMaintenanceRole:  qualificationNativePostgresControlDatabase,
			qualificationNativePostgresDuckLakeRuntimeRole:     qualificationNativePostgresDuckLakeDatabase,
			qualificationNativePostgresDuckLakeMigratorRole:    qualificationNativePostgresDuckLakeDatabase,
			qualificationNativePostgresDuckLakeMaintenanceRole: qualificationNativePostgresDuckLakeDatabase,
		}[role], RawQuery: "sslmode=require"}
		return parsed.String()
	}
	makeURL := func(role string) string {
		parsed, _ := url.Parse(password(role))
		parsed.User = url.UserPassword(role, "secret-"+strings.ReplaceAll(role, "_", "-"))
		return parsed.String()
	}
	return &qualificationNativePostgresTopology{
		ControlURL:                    makeURL(qualificationNativePostgresControlRuntimeRole),
		ControlReadonlyURL:            makeURL(qualificationNativePostgresControlReadonlyRole),
		ControlMigratorURL:            makeURL(qualificationNativePostgresControlMigratorRole),
		ControlUpgradeCoordinatorURL:  makeURL(qualificationNativePostgresControlUpgradeRole),
		ControlMaintenanceURL:         makeURL(qualificationNativePostgresControlMaintenanceRole),
		DuckLakeURL:                   makeURL(qualificationNativePostgresDuckLakeRuntimeRole),
		DuckLakeMigratorURL:           makeURL(qualificationNativePostgresDuckLakeMigratorRole),
		DuckLakeMaintenanceURL:        makeURL(qualificationNativePostgresDuckLakeMaintenanceRole),
		ControlRuntimeRole:            qualificationNativePostgresControlRuntimeRole,
		ControlReadonlyRole:           qualificationNativePostgresControlReadonlyRole,
		ControlMigratorRole:           qualificationNativePostgresControlMigratorRole,
		ControlUpgradeCoordinatorRole: qualificationNativePostgresControlUpgradeRole,
		ControlMaintenanceRole:        qualificationNativePostgresControlMaintenanceRole,
		DuckLakeRuntimeRole:           qualificationNativePostgresDuckLakeRuntimeRole,
		DuckLakeMigratorRole:          qualificationNativePostgresDuckLakeMigratorRole,
		DuckLakeMaintenanceRole:       qualificationNativePostgresDuckLakeMaintenanceRole,
	}
}

func TestQualificationNativeEnvironmentSeedsPackagedExampleAtomically(t *testing.T) {
	root := t.TempDir()
	seed := "LEAPVIEW_CSRF_KEY=<generated-by-leapviewctl>\nLEAPVIEW_POSTGRES_CONTROL_URL=\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env.example"), []byte(seed), 0o644))
	require.NoError(t, seedQualificationNativeEnvironment(root))
	contents, err := os.ReadFile(filepath.Join(root, appEnvName))
	require.NoError(t, err)
	require.Equal(t, seed, string(contents))
	info, err := os.Stat(filepath.Join(root, appEnvName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, os.Remove(filepath.Join(root, "leapview.env.example")))
	require.Error(t, seedQualificationNativeEnvironment(root))
}

func TestQualificationNativeEnvironmentPersistsServingKeysOnly(t *testing.T) {
	root := t.TempDir()
	example := strings.Join([]string{
		"LEAPVIEW_CSRF_KEY=<generated-by-leapviewctl>",
		"LEAPVIEW_METRICS_BEARER_TOKEN=<generated-by-leapviewctl>",
		"LEAPVIEW_POSTGRES_CONTROL_URL=",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL=",
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE=placeholder",
		"LEAPVIEW_POSTGRES_CONTROL_RUNTIME_ROLE=placeholder",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL=",
		"LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_ROLE=placeholder",
		"LEAPVIEW_POSTGRES_DUCKLAKE_URL=",
		"LEAPVIEW_POSTGRES_DUCKLAKE_RUNTIME_ROLE=placeholder",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_URL=",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MAINTENANCE_ROLE=placeholder",
		"LEAPVIEW_POSTGRES_REQUIRE_TLS=false",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL=stale-owner-url",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL=stale-coordinator-url",
		"LEAPVIEW_OTHER_SETTING=preserved",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env.example"), []byte(example), 0o644))
	require.NoError(t, seedQualificationNativeEnvironment(root))
	topology := qualificationNativeEnvironmentTopologyFixture()
	require.NoError(t, writeQualificationNativePostgresEnvironment(filepath.Join(root, appEnvName), topology))
	contents, err := os.ReadFile(filepath.Join(root, appEnvName))
	require.NoError(t, err)
	values := environmentValues(string(contents))
	serving, err := qualificationNativePostgresServingEnvironment(topology)
	require.NoError(t, err)
	for key, want := range serving {
		require.Equal(t, want, values[key], key)
	}
	for _, key := range []string{
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
	} {
		_, present := values[key]
		require.False(t, present, key)
	}
	require.Equal(t, "<generated-by-leapviewctl>", values["LEAPVIEW_CSRF_KEY"])
	require.Equal(t, "<generated-by-leapviewctl>", values["LEAPVIEW_METRICS_BEARER_TOKEN"])
	require.Equal(t, "preserved", values["LEAPVIEW_OTHER_SETTING"])

	operation, err := qualificationNativePostgresOperationEnvironment(topology)
	require.NoError(t, err)
	require.Len(t, operation, 2)
	require.Equal(t, topology.DuckLakeMigratorURL, operation["LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"])
	require.Equal(t, qualificationNativePostgresDuckLakeMigratorRole, operation["LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE"])
}

func TestQualificationNativeEnvironmentRejectsAliasURLsAndRoles(t *testing.T) {
	base := qualificationNativeEnvironmentTopologyFixture()
	for name, mutate := range map[string]func(*qualificationNativePostgresTopology){
		"URL alias": func(topology *qualificationNativePostgresTopology) {
			topology.ControlMigratorURL = topology.ControlURL
		},
		"semantic URL alias": func(topology *qualificationNativePostgresTopology) {
			parsed, err := url.Parse(topology.ControlURL)
			require.NoError(t, err)
			parsed.Scheme = "postgresql"
			parsed.Host = "POSTGRES:5432"
			parsed.RawQuery = "foo=bar&sslmode=require"
			topology.ControlMigratorURL = parsed.String()
		},
		"role alias": func(topology *qualificationNativePostgresTopology) {
			topology.ControlMigratorRole = topology.ControlRuntimeRole
		},
		"credential alias": func(topology *qualificationNativePostgresTopology) {
			runtimeURL, err := url.Parse(topology.ControlURL)
			require.NoError(t, err)
			password, present := runtimeURL.User.Password()
			require.True(t, present)
			migratorURL, err := url.Parse(topology.ControlMigratorURL)
			require.NoError(t, err)
			migratorURL.User = url.UserPassword(migratorURL.User.Username(), password)
			topology.ControlMigratorURL = migratorURL.String()
		},
		"malformed sslmode": func(topology *qualificationNativePostgresTopology) {
			parsed, err := url.Parse(topology.ControlURL)
			require.NoError(t, err)
			parsed.RawQuery = "sslmode=disable"
			topology.ControlURL = parsed.String()
		},
		"malformed role identity": func(topology *qualificationNativePostgresTopology) {
			topology.DuckLakeMaintenanceRole = "not a role"
		},
	} {
		t.Run(name, func(t *testing.T) {
			topology := *base
			mutate(&topology)
			_, err := qualificationNativePostgresServingEnvironment(&topology)
			require.Error(t, err)
		})
	}
	_, err := qualificationNativePostgresServingEnvironment(nil)
	require.Error(t, err)
}

func TestAssertQualificationNativeServingCredentialBoundary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, appEnvName)
	topology := qualificationNativeEnvironmentTopologyFixture()
	values, err := qualificationNativePostgresServingEnvironment(topology)
	require.NoError(t, err)
	lines := make([]string, 0, len(values))
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	require.NoError(t, assertQualificationNativeServingCredentialBoundary(path))

	mutate := func(t *testing.T, key, value string) {
		t.Helper()
		contents, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		env := environmentValues(string(contents))
		env[key] = value
		updated := make([]string, 0, len(env))
		for name, setting := range env {
			updated = append(updated, name+"="+setting)
		}
		require.NoError(t, os.WriteFile(path, []byte(strings.Join(updated, "\n")+"\n"), 0o600))
	}

	t.Run("missing serving URL", func(t *testing.T) {
		mutate(t, "LEAPVIEW_POSTGRES_CONTROL_URL", "")
		require.Error(t, assertQualificationNativeServingCredentialBoundary(path))
	})
	// Restore a valid environment before the alias and operation-only checks.
	values, err = qualificationNativePostgresServingEnvironment(topology)
	require.NoError(t, err)
	lines = lines[:0]
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	t.Run("aliased credential", func(t *testing.T) {
		mutate(t, "LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL", topology.ControlURL)
		require.Error(t, assertQualificationNativeServingCredentialBoundary(path))
	})
	values, err = qualificationNativePostgresServingEnvironment(topology)
	require.NoError(t, err)
	values["LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"] = topology.DuckLakeMigratorURL
	lines = lines[:0]
	for key, value := range values {
		lines = append(lines, key+"="+value)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	require.Error(t, assertQualificationNativeServingCredentialBoundary(path))
}
