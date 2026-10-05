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
		}[role], RawQuery: "sslmode=verify-full&sslrootcert=" + url.QueryEscape(qualificationNativePostgresRootCertPath)}
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
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_URL=",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE=placeholder",
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
	readonlyURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "control readonly", value: topology.ControlReadonlyURL,
		role: qualificationNativePostgresControlReadonlyRole, database: qualificationNativePostgresControlDatabase,
	})
	require.NoError(t, err)
	require.Equal(t, readonlyURL, values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"])
	require.Equal(t, qualificationNativePostgresControlReadonlyRole, values["LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"])
	for _, key := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
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
	require.Len(t, operation, 4)
	require.Equal(t, topology.ControlMigratorURL, operation["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL"])
	require.Equal(t, topology.DuckLakeMigratorURL, operation["LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL"])
	require.Equal(t, qualificationNativePostgresControlMigratorRole, operation["LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_ROLE"])
	require.Equal(t, qualificationNativePostgresDuckLakeMigratorRole, operation["LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_ROLE"])
}

func TestQualificationNativeEnvironmentWritesShippedTemplateWithReadonlyTopology(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "compose", "leapview.env.example"))
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env.example"), template, 0o644))
	require.NoError(t, seedQualificationNativeEnvironment(root))

	topology := qualificationNativeEnvironmentTopologyFixture()
	require.NotEmpty(t, topology.ControlReadonlyURL)
	require.NoError(t, writeQualificationNativePostgresEnvironment(filepath.Join(root, appEnvName), topology))

	contents, err := os.ReadFile(filepath.Join(root, appEnvName))
	require.NoError(t, err)
	values := environmentValues(string(contents))
	serving, err := qualificationNativePostgresServingEnvironment(topology)
	require.NoError(t, err)
	for key, want := range serving {
		require.Equal(t, want, values[key], key)
	}
	readonlyURL, err := canonicalPostgresConnectionURL(postgresConnection{
		name: "control readonly", value: topology.ControlReadonlyURL,
		role: qualificationNativePostgresControlReadonlyRole, database: qualificationNativePostgresControlDatabase,
	})
	require.NoError(t, err)
	require.Equal(t, readonlyURL, values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"])
	require.Equal(t, qualificationNativePostgresControlReadonlyRole, values["LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"])
	require.Equal(t, "<generated-by-leapviewctl>", values["LEAPVIEW_CSRF_KEY"])
	require.NoError(t, assertQualificationNativeServingCredentialBoundary(filepath.Join(root, appEnvName)))
	for _, key := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_DUCKLAKE_MIGRATOR_URL",
		"LEAPVIEW_POSTGRES_CONTROL_UPGRADE_COORDINATOR_URL",
	} {
		_, present := values[key]
		require.False(t, present, key)
	}
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
			parsed.RawQuery = "foo=bar&sslmode=verify-full&sslrootcert=" + url.QueryEscape(qualificationNativePostgresRootCertPath)
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

func TestAssertQualificationNativeServingCredentialBoundaryValidatesOptionalReadonlyPair(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, appEnvName)
	topology := qualificationNativeEnvironmentTopologyFixture()
	values, err := qualificationNativePostgresServingEnvironment(topology)
	require.NoError(t, err)
	writeValues := func(t *testing.T, values map[string]string) {
		t.Helper()
		lines := make([]string, 0, len(values))
		for key, value := range values {
			lines = append(lines, key+"="+value)
		}
		require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	}
	cloneValues := func(values map[string]string) map[string]string {
		cloned := make(map[string]string, len(values))
		for key, value := range values {
			cloned[key] = value
		}
		return cloned
	}
	writeValues(t, values)
	require.NoError(t, assertQualificationNativeServingCredentialBoundary(path))

	t.Run("readonly pair absent", func(t *testing.T) {
		withoutReadonly := cloneValues(values)
		delete(withoutReadonly, "LEAPVIEW_POSTGRES_CONTROL_READONLY_URL")
		delete(withoutReadonly, "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE")
		writeValues(t, withoutReadonly)
		require.NoError(t, assertQualificationNativeServingCredentialBoundary(path))
	})

	for name, mutate := range map[string]func(map[string]string){
		"readonly URL missing": func(values map[string]string) {
			delete(values, "LEAPVIEW_POSTGRES_CONTROL_READONLY_URL")
		},
		"readonly role missing": func(values map[string]string) {
			delete(values, "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE")
		},
		"readonly role is invalid": func(values map[string]string) {
			values["LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"] = "custom_readonly"
		},
		"readonly URL has the wrong role": func(values map[string]string) {
			parsed, parseErr := url.Parse(values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"])
			require.NoError(t, parseErr)
			password, present := parsed.User.Password()
			require.True(t, present)
			parsed.User = url.UserPassword(qualificationNativePostgresControlRuntimeRole, password)
			values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"] = parsed.String()
		},
		"readonly URL is invalid": func(values map[string]string) {
			parsed, parseErr := url.Parse(values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"])
			require.NoError(t, parseErr)
			parsed.RawQuery = "sslmode=disable"
			values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"] = parsed.String()
		},
		"readonly credential aliases runtime": func(values map[string]string) {
			runtimeURL, parseErr := url.Parse(values["LEAPVIEW_POSTGRES_CONTROL_URL"])
			require.NoError(t, parseErr)
			password, present := runtimeURL.User.Password()
			require.True(t, present)
			readonlyURL, parseErr := url.Parse(values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"])
			require.NoError(t, parseErr)
			readonlyURL.User = url.UserPassword(readonlyURL.User.Username(), password)
			values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"] = readonlyURL.String()
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := cloneValues(values)
			mutate(mutated)
			writeValues(t, mutated)
			require.Error(t, assertQualificationNativeServingCredentialBoundary(path))
		})
	}

	for _, key := range []string{
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_URL",
		"LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE",
	} {
		t.Run("duplicate "+key, func(t *testing.T) {
			writeValues(t, values)
			contents, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.NoError(t, os.WriteFile(path, append(contents, []byte(key+"=duplicate\n")...), 0o600))
			require.Error(t, assertQualificationNativeServingCredentialBoundary(path))
		})
	}
}

func TestQualificationNativeEnvironmentClearsReadonlyWhenTopologyOmitsIt(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "compose", "leapview.env.example"))
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "leapview.env.example"), template, 0o644))
	require.NoError(t, seedQualificationNativeEnvironment(root))
	environmentPath := filepath.Join(root, appEnvName)
	require.NoError(t, appendOrReplaceEnvFile(environmentPath, "LEAPVIEW_POSTGRES_CONTROL_READONLY_URL", "stale-readonly-url"))
	require.NoError(t, appendOrReplaceEnvFile(environmentPath, "LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE", qualificationNativePostgresControlReadonlyRole))

	topology := qualificationNativeEnvironmentTopologyFixture()
	topology.ControlReadonlyURL = ""
	topology.ControlReadonlyRole = ""
	require.NoError(t, writeQualificationNativePostgresEnvironment(environmentPath, topology))

	contents, err := os.ReadFile(environmentPath)
	require.NoError(t, err)
	values := environmentValues(string(contents))
	_, hasReadonlyURL := values["LEAPVIEW_POSTGRES_CONTROL_READONLY_URL"]
	_, hasReadonlyRole := values["LEAPVIEW_POSTGRES_CONTROL_READONLY_ROLE"]
	require.False(t, hasReadonlyURL)
	require.False(t, hasReadonlyRole)
	require.NoError(t, assertQualificationNativeServingCredentialBoundary(environmentPath))
}
