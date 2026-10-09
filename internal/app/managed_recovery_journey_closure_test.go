package app

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/stretchr/testify/require"
)

func managedJourneyNativeClosure(t *testing.T, f *sourceCredentialHTTPJourney, seal recoveryset.SnapshotSeal) ducklake.NativeSnapshotClosureEvidence {
	t.Helper()
	authority, err := deploymentpostgres.NewNativeBuildContractAuthority(deploymentpostgres.NativeBuildContractAuthorityConfig{PhysicalPool: f.graph.PhysicalPool, Catalog: f.graph.DuckLakeControlLedger, Runtime: f.graph.DuckLakeControlLedger})
	require.NoError(t, err)
	contract, err := authority.Resolve(t.Context(), deploymentpostgres.NativeBuildContractRequest{PhysicalPoolID: seal.PhysicalPoolID, CompatibilityDigest: seal.CompatibilityDigest})
	require.NoError(t, err)
	supply, err := loadExtensionSupply(t.Context(), f.config)
	require.NoError(t, err)
	bootstrap, err := newPostgresDuckLakeCredentialBootstrap(f.config, contract.PoolContract, supply)
	require.NoError(t, err)
	environment, err := ducklake.Open(t.Context(), ducklake.Config{
		RootDir: f.config.RuntimeDir(), ReadOnly: true, PhysicalPoolID: seal.PhysicalPoolID, SharedPool: true,
		Compatibility: contract.PoolContract.Tuple, PoolContract: contract.PoolContract, CredentialBootstrap: bootstrap, ExtensionAdmission: supply,
		MaxConnections: 1, MaxThreads: 2, MemoryMaxBytes: f.config.DuckDBNodeMemoryMaxBytes, TempMaxBytes: f.config.DuckDBNodeTempMaxBytes, TempDir: f.config.DuckDBTempDirPath(),
		PostgresCatalog: &ducklake.PostgresCatalogConfig{PhysicalPoolID: seal.PhysicalPoolID, DuckLakeSecret: postgresDuckLakeSecret, PostgresSecret: postgresConnectionSecret, MetadataSchema: ducklake.MetadataSchemaForPool(seal.PhysicalPoolID), Mode: ducklake.PostgresCatalogServing, SnapshotVersion: seal.DuckLakeSnapshotID},
	})
	require.NoError(t, err)
	defer environment.Close()
	closure, err := environment.NativeSnapshotClosureEvidence(t.Context(), ducklake.NativeSnapshotClosureRequest{CatalogID: seal.CatalogID, SnapshotID: seal.DuckLakeSnapshotID, ObjectRoot: seal.ObjectRoot, RelationNamespace: seal.RelationNamespace})
	require.NoError(t, err)
	require.NotEmpty(t, closure.Relations)
	require.Equal(t, seal.ClosureDigest, closure.ClosureDigest)
	return closure
}
