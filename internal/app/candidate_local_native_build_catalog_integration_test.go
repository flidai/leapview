//go:build integration && duckdb_arrow

package app

import (
	"context"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/app/postgresducklake"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type localNativeBuildCatalogFixture struct {
	config   analyticsducklake.Config
	contract appdeploymentpostgres.NativeBuildContract
}

// The native physical-build port requires a PostgreSQL-backed writer. Use the
// production credential bootstrap and catalog provisioning helpers, with a
// separate metadata writer that cannot create schemas or catalog tables.
// Physical-pool admission is fixture evidence, not a conformance qualification.
func newLocalNativeBuildCatalogFixture(t *testing.T, admission extension.Admission) localNativeBuildCatalogFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	harness := postgrestest.StartTLS(t)
	migrator := harness.EnsureRole(t, postgrestest.Role{Name: "local_native_catalog_migrator", Password: "native-catalog-migrator-secret", Login: true})
	writer := harness.EnsureRole(t, postgrestest.Role{Name: "local_native_catalog_writer", Password: "native-catalog-writer-secret", Login: true})
	database := harness.NewDatabase(t, "local_native_catalog")
	harness.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE", "TEMPORARY")
	harness.GrantDatabase(t, database.Name, writer, "CONNECT")

	admin, err := pgxpool.New(ctx, database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	_, err = admin.Exec(ctx, `REVOKE ALL ON SCHEMA public FROM PUBLIC`)
	require.NoError(t, err)

	poolContract := localNativeBuildPoolContract(t)
	poolID := poolContract.Pool.ID.String()
	dataPath, err := poolContract.Pool.DataPath()
	require.NoError(t, err)
	metadataSchema := analyticsducklake.MetadataSchemaForPool(poolID)
	migratorURL := localNativeBuildTLSURL(t, database.PrivateURL(migrator))
	migratorDB, err := pgxpool.New(ctx, migratorURL)
	require.NoError(t, err)
	t.Cleanup(migratorDB.Close)
	require.NoError(t, ducklakepostgres.EnsureCatalogMetadataSchema(ctx, migratorDB, metadataSchema))
	migratorBootstrap, err := postgresducklake.NewCredentialBootstrap(postgresducklake.CredentialConfig{
		PostgresURL: migratorURL, Contract: poolContract, ExtensionAdmission: admission,
	})
	require.NoError(t, err)
	initialize := analyticsducklake.PostgresCatalogConfig{
		PhysicalPoolID: poolID, DuckLakeSecret: postgresducklake.DuckLakeSecret,
		PostgresSecret: postgresducklake.PostgresSecret, MetadataSchema: metadataSchema,
		DataPath: dataPath, Mode: analyticsducklake.PostgresCatalogInitialize,
	}
	config := analyticsducklake.Config{
		RootDir: t.TempDir(), PoolContract: poolContract, PhysicalPoolID: poolID,
		PostgresCatalog: &initialize, CredentialBootstrap: migratorBootstrap,
		ExtensionAdmission: admission, MaxConnections: 2,
	}
	environment, err := analyticsducklake.Open(ctx, config)
	require.NoError(t, err)
	require.NoError(t, environment.Close())
	require.NoError(t, ducklakepostgres.ProvisionCatalogRuntimePrivileges(ctx, migratorDB, metadataSchema, writer.Name))
	registration, err := ducklakepostgres.ReadCatalogRegistrationEvidence(ctx, migratorDB, metadataSchema)
	require.NoError(t, err)
	catalog, err := ducklakepostgres.DeriveCatalogIdentity(poolID, registration.CatalogDatabase)
	require.NoError(t, err)

	writerURL := localNativeBuildTLSURL(t, database.PrivateURL(writer))
	writerDB, err := pgxpool.New(ctx, writerURL)
	require.NoError(t, err)
	t.Cleanup(writerDB.Close)
	var schemaUsage, schemaCreate, databaseCreate bool
	require.NoError(t, writerDB.QueryRow(ctx, `SELECT has_schema_privilege(current_user, $1, 'USAGE'), has_schema_privilege(current_user, $1, 'CREATE'), has_database_privilege(current_user, current_database(), 'CREATE')`, metadataSchema).Scan(&schemaUsage, &schemaCreate, &databaseCreate))
	require.True(t, schemaUsage)
	require.False(t, schemaCreate)
	require.False(t, databaseCreate)
	writerBootstrap, err := postgresducklake.NewCredentialBootstrap(postgresducklake.CredentialConfig{
		PostgresURL: writerURL, Contract: poolContract, ExtensionAdmission: admission,
	})
	require.NoError(t, err)
	writerCatalog := initialize
	writerCatalog.Mode = analyticsducklake.PostgresCatalogWriter
	writerCatalog.DataPath = ""
	config.PostgresCatalog = &writerCatalog
	config.CredentialBootstrap = writerBootstrap
	compatibility := ducklakepostgres.RuntimeCompatibility{
		RuntimeTuple: ducklakepostgres.RuntimeTuple{
			DuckDBRuntime: poolContract.Tuple.DuckDBRuntime, DuckLakeExtension: poolContract.Tuple.DuckLakeExtension,
			CatalogFormat: poolContract.Tuple.CatalogFormat,
		},
		CompatibilityDigest:  poolContract.Admission.CompatibilityDigest,
		CatalogSchemaVersion: registration.CatalogSchemaVersion,
	}
	return localNativeBuildCatalogFixture{
		config: config,
		contract: appdeploymentpostgres.NativeBuildContract{
			PhysicalPoolID: poolID, CompatibilityDigest: poolContract.Admission.CompatibilityDigest,
			PoolContract: poolContract, Catalog: catalog, Compatibility: compatibility,
			CatalogRuntime: ducklakepostgres.CatalogRuntimeCompatibility{PhysicalPoolID: poolID, CatalogID: catalog.CatalogID, RuntimeCompatibility: compatibility},
			TenantDomain:   poolContract.Pool.Identity.Tenant, EncryptionDomain: poolContract.Pool.Identity.EncryptionDomain,
			ObjectNamespace: poolContract.Pool.Identity.StorageNamespace,
		},
	}
}

func localNativeBuildPoolContract(t *testing.T) *analyticsducklake.PoolContract {
	t.Helper()
	tuple := physicalpool.Compatibility{
		DuckDBRuntime: "duckdb:fixture", DuckLakeExtension: "ducklake:fixture", CatalogFormat: "ducklake:v1",
		StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	pool, err := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{
		StorageLocation: filepath.Join(t.TempDir(), "lake"), StorageNamespace: "objects",
		Region: "fixture", Tenant: "fixture", EncryptionDomain: "fixture",
		IsolationBoundary: "fixture", RetentionAuthority: "fixture", Compatibility: tuple,
	})
	require.NoError(t, err)
	checks := make([]physicalpool.EvidenceCheck, 0, len(analyticsducklake.SharedPoolConformanceChecks))
	for _, name := range analyticsducklake.SharedPoolConformanceChecks {
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: activeResultIdentityDigest('a')})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: tuple, ConformanceVersion: analyticsducklake.SharedPoolConformanceVersion, Checks: checks,
	})
	require.NoError(t, err)
	admitted, err := pool.Admit(evidence)
	require.NoError(t, err)
	pool, err = pool.ApplyAdmission(admitted)
	require.NoError(t, err)
	return &analyticsducklake.PoolContract{Pool: pool, Tuple: tuple, Admission: admitted, Evidence: evidence}
}

func localNativeBuildTLSURL(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("sslmode", "require")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
