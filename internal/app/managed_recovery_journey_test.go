package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	"github.com/flidai/leapview/internal/app/providerrestore"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// The publication, native seal, materialized rows, customer owner and encrypted
// source credentials come from the real production HTTP journey. No delivery
// or DuckLake metadata is seeded to satisfy the recovery reader.
func TestManagedRecoveryProductionNativeReadback(t *testing.T) {
	f, token := runFirstSourceProductionPublicationJourney(t, false)
	require.NoError(t, f.target.Shutdown(context.Background()))
	f.target = nil
	readback := managedJourneyReadback(t, f)
	proof, err := readback.Verify(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, proof.ControlDigest)
	require.NotEmpty(t, proof.DuckLakeDigest)
	require.Equal(t, readback.Set.Catalog, proof.Catalog)
	wrong := readback
	wrong.Set.Delivery.PublicationID = uuid.NewString()
	_, err = wrong.Verify(t.Context())
	require.Error(t, err, "a foreign publication must fail actual native readback")
	t.Run("independent prepared frontier enrollment", func(t *testing.T) { managedJourneyEnrollment(t, f, readback.Set) })
	t.Run("immutable file backup and restore", func(t *testing.T) { managedJourneyFileRestore(t, f, readback.Set) })
	t.Run("physical backup and confined restore", func(t *testing.T) { managedJourneyPhysicalRestore(t, f, readback) })
	f.start(t)
	_ = f.querySource(t, token, "30")
}

func managedJourneyReadback(t *testing.T, f *sourceCredentialHTTPJourney) managedrecovery.NativePostgresReadback {
	t.Helper()
	admin, err := pgx.Connect(t.Context(), f.control.AdminURL())
	require.NoError(t, err)
	defer admin.Close(context.Background())
	set := recoveryset.RecoverySet{ID: uuid.NewString(), SchemaVersion: recoveryset.SchemaVersion, Status: recoveryset.StatusPrepared, FenceEpoch: 1, AuditIdentity: "managed-production-journey", CreatedBy: "managed-production-journey", CreatedAt: time.Now().UTC()}
	var sealJSON, compatibilityJSON []byte
	var schema, controlDatabase, systemID, lsn string
	require.NoError(t, admin.QueryRow(t.Context(), `SELECT to_jsonb(s),t.target_id,a.generation_id::text,a.publication_id::text,t.target_revision,c.metadata_schema
FROM delivery.delivery_target t
JOIN delivery.delivery_active_pointer a ON a.target_id=t.target_id
JOIN delivery.delivery_generation g ON g.generation_id=a.generation_id
JOIN delivery.delivery_snapshot_seal s ON s.seal_id=g.snapshot_seal_id
JOIN ducklake.catalog_identity c ON c.physical_pool_id=s.physical_pool_id
WHERE t.target_id=$1`, f.instance).Scan(&sealJSON, &set.Delivery.TargetID, &set.Delivery.GenerationID, &set.Delivery.PublicationID, &set.Delivery.TargetRevision, &schema))
	require.NoError(t, json.Unmarshal(sealJSON, &set.Serving))
	require.NoError(t, admin.QueryRow(t.Context(), `SELECT compatibility_json FROM physical_pool.physical_pool_admissions WHERE pool_id=$1 AND compatibility_digest=$2`, set.Serving.PhysicalPoolID, set.Serving.CompatibilityDigest).Scan(&compatibilityJSON))
	require.NoError(t, json.Unmarshal(compatibilityJSON, &set.Compatibility))
	require.NoError(t, admin.QueryRow(t.Context(), `SELECT current_database(),system_identifier::text,pg_current_wal_lsn()::text FROM pg_control_system()`).Scan(&controlDatabase, &systemID, &lsn))
	for _, database := range []struct {
		role recoveryset.DatabaseRole
		name string
	}{{recoveryset.DatabaseControl, controlDatabase}, {recoveryset.DatabaseDuckLake, set.Serving.CatalogDatabase}} {
		set.ClusterPoints = append(set.ClusterPoints, recoveryset.ClusterRecoveryPoint{DatabaseRole: database.role, DatabaseIdentity: database.name, ClusterIdentity: "postgres-system-id:" + systemID, RecoveryIdentity: "lsn:" + lsn})
	}
	s := set.Serving
	set.Catalog = recoveryset.CatalogCommit{CatalogID: s.CatalogID, CatalogDatabase: s.CatalogDatabase, CatalogUUID: s.CatalogUUID, CatalogVersion: s.CatalogVersion, SnapshotID: s.DuckLakeSnapshotID}
	for _, root := range []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: s.ObjectRoot, Digest: s.ObjectRootDigest}, {Kind: recoveryset.ObjectRootServingArtifact, URI: s.ArtifactRoot, Digest: s.ArtifactRootDigest}} {
		root.VersionID = root.Digest
		storageRoot := ""
		if root.Kind == recoveryset.ObjectRootServingArtifact {
			storageRoot = filepath.Join(f.config.ArtifactDir(), "object-store")
		}
		location, err := providerrestore.ManagedLocalRootPath(root, storageRoot)
		require.NoError(t, err)
		if root.Kind == recoveryset.ObjectRootServingArtifact {
			location = filepath.Dir(location)
		}
		manifest, err := managedrecovery.CaptureFiles(location)
		require.NoError(t, err)
		root.VersionID, err = manifest.Digest()
		require.NoError(t, err)
		set.ObjectRoots = append(set.ObjectRoots, root)
	}
	require.NoError(t, set.Validate())
	controlURL, err := url.Parse(f.config.PostgresControlURL)
	require.NoError(t, err)
	ca, err := os.ReadFile(controlURL.Query().Get("sslrootcert"))
	require.NoError(t, err)
	cleanURL := func(raw string) string {
		parsed, e := url.Parse(raw)
		require.NoError(t, e)
		parsed.RawQuery = "sslmode=verify-full"
		return parsed.String()
	}
	keyring, err := os.ReadFile(f.config.CredentialKeyringFile)
	require.NoError(t, err)
	keyDigest := sha256.Sum256(keyring)
	credentials := &managedrecovery.ManagedCredentials{InstanceID: f.instance, KeyringPath: f.config.CredentialKeyringFile, KeyringDigest: "sha256:" + hex.EncodeToString(keyDigest[:])}
	return managedrecovery.NativePostgresReadback{Set: set, MetadataSchema: schema, ControlURL: cleanURL(f.config.PostgresControlURL), DuckLakeURL: cleanURL(f.config.PostgresDuckLakeURL), RootCA: string(ca), Roles: managedrecovery.RuntimeRoles{Control: f.config.PostgresControlRuntimeRole, DuckLake: f.config.PostgresDuckLakeRuntimeRole}, Credentials: credentials}
}
