package managedrecovery

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	deliverypostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/jackc/pgx/v5"
)

// This seeds the real capability schemas as test administrator solely to test
// readback/role/TLS boundaries. It is not a native build/publication journey and
// does not supply application recovery qualification evidence.
func TestNativeManagedReadbackUsesRealSchemasTLSAndRuntimeRoles(t *testing.T) {
	h := postgrestest.StartTLS(t)
	controlRole := h.EnsureRole(t, postgrestest.Role{Name: "managed_control_reader", Password: "control-reader-password", Login: true})
	duckRole := h.EnsureRole(t, postgrestest.Role{Name: "managed_duck_reader", Password: "duck-reader-password", Login: true})
	ownerRole := h.EnsureRole(t, postgrestest.Role{Name: "managed_schema_owner"})
	controlDB, duckDB := h.NewDatabase(t, ""), h.NewDatabase(t, "")
	h.GrantDatabase(t, controlDB.Name, controlRole, "CONNECT")
	h.GrantDatabase(t, duckDB.Name, duckRole, "CONNECT")
	controlAdmin, err := pgx.Connect(t.Context(), controlDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer controlAdmin.Close(context.Background())
	duckAdmin, err := pgx.Connect(t.Context(), duckDB.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer duckAdmin.Close(context.Background())
	for _, schema := range []string{deliverypostgres.SchemaSQL(), ducklakepostgres.SchemaSQL()} {
		if _, err := controlAdmin.Exec(t.Context(), schema); err != nil {
			t.Fatal(err)
		}
	}
	compat := physicalpool.Compatibility{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1"}
	compatDigest, err := compat.Digest()
	if err != nil {
		t.Fatal(err)
	}
	seal, _, _ := managedClosureFixture(t)
	seal.SealID = "018f3f83-7b2f-7b37-9f9e-000000000101"
	seal.PhysicalPoolID, seal.TenantDomain, seal.Region, seal.EncryptionDomain, seal.ObjectNamespace = "pool", "tenant", "region", "encryption", "objects"
	seal.CatalogDatabase, seal.CatalogUUID, seal.CatalogVersion = duckDB.Name, "018f3f83-7b2f-7b37-9f9e-000000000102", 1
	seal.ArtifactRootDigest = digestBytes([]byte("artifact"))
	seal.ArtifactRoot = "serving-artifacts/" + seal.ArtifactRootDigest[7:] + ".tar.gz"
	seal.ServingArtifactID, seal.ServingArtifactDigest = "artifact", seal.ArtifactRootDigest
	seal.CompiledGraphDigest, seal.CompiledConfigDigest, seal.SecurityDomainFingerprint = digestBytes([]byte("graph")), digestBytes([]byte("config")), digestBytes([]byte("security"))
	seal.RequestDigest, seal.PlanDigest, seal.CompatibilityDigest = digestBytes([]byte("request")), digestBytes([]byte("plan")), compatDigest
	seal.DuckDBVersion, seal.RuntimeVersion, seal.DuckLakeExtensionVersion, seal.DuckLakeSpecVersion, seal.CatalogSchemaVersion = "1", "1", "1", "1", "1"
	set := recoveryset.RecoverySet{ID: "018f3f83-7b2f-7b37-9f9e-000000000100", SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "retained-cluster", DatabaseIdentity: controlDB.Name, RecoveryIdentity: "retained-point"}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "retained-cluster", DatabaseIdentity: duckDB.Name, RecoveryIdentity: "retained-point"}},
		Delivery:      recoveryset.DeliveryPointer{TargetID: "target", GenerationID: "018f3f83-7b2f-7b37-9f9e-000000000103", PublicationID: "018f3f83-7b2f-7b37-9f9e-000000000104", TargetRevision: 2}, Serving: seal,
		Catalog:       recoveryset.CatalogCommit{CatalogID: seal.CatalogID, CatalogDatabase: seal.CatalogDatabase, CatalogUUID: seal.CatalogUUID, CatalogVersion: 1, SnapshotID: seal.DuckLakeSnapshotID},
		ObjectRoots:   []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: seal.ObjectRoot, VersionID: strings.Repeat("a", 64), Digest: seal.ObjectRootDigest, ProviderRecoveryFrontier: "restic:" + strings.Repeat("a", 64)}, {Kind: recoveryset.ObjectRootServingArtifact, URI: seal.ArtifactRoot, VersionID: strings.Repeat("b", 64), Digest: seal.ArtifactRootDigest, ProviderRecoveryFrontier: "restic:" + strings.Repeat("b", 64)}},
		Compatibility: compat, FenceEpoch: 1, AuditIdentity: "test", Status: recoveryset.StatusPrepared, CreatedBy: "test", CreatedAt: time.Now().UTC()}
	if err := set.Validate(); err != nil {
		t.Fatal(err)
	}
	const attemptID = "018f3f83-7b2f-7b37-9f9e-000000000105"
	const candidateID = "018f3f83-7b2f-7b37-9f9e-000000000106"
	const planID = "018f3f83-7b2f-7b37-9f9e-000000000107"
	marker := catalogartifact.CommitMarker{SchemaVersion: 1, DeliveryID: "operation", GenerationID: set.Delivery.GenerationID, AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: seal.RequestDigest, PlanDigest: seal.PlanDigest, Project: "project", Environment: "production", PhysicalPoolID: seal.PhysicalPoolID}
	markerString, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	adminTx, err := controlAdmin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer adminTx.Rollback(context.Background())
	// The administrator bypass is isolated fixture construction, not an
	// application publication or a capability granted to either runtime role.
	if _, err := adminTx.Exec(t.Context(), "SET LOCAL session_replication_role=replica"); err != nil {
		t.Fatal(err)
	}
	insert := func(table string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adminTx.Exec(t.Context(), "INSERT INTO "+table+" SELECT (jsonb_populate_record(NULL::"+table+",$1::jsonb)).*", raw); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	insert("delivery.delivery_target", map[string]any{"target_id": "target", "project_id": "project", "environment": "production", "target_revision": 2, "created_at": now, "updated_at": now})
	insert("delivery.delivery_build_attempt", map[string]any{"attempt_id": attemptID, "plan_id": planID, "candidate_id": candidateID, "owner_id": "owner", "physical_pool_id": seal.PhysicalPoolID, "catalog_id": seal.CatalogID, "fencing_epoch": 1, "request_digest": seal.RequestDigest, "plan_digest": seal.PlanDigest, "state": "committed", "namespace": seal.RelationNamespace, "lease_expires_at": now, "session_identity": "session", "snapshot_id": seal.DuckLakeSnapshotID, "commit_marker": marker, "created_at": now, "updated_at": now, "finished_at": now})
	rawSeal, _ := json.Marshal(seal)
	var sealRow map[string]any
	if err := json.Unmarshal(rawSeal, &sealRow); err != nil {
		t.Fatal(err)
	}
	sealRow["attempt_id"], sealRow["candidate_id"], sealRow["qualified_at"], sealRow["qualification_evidence"] = attemptID, candidateID, now, map[string]any{}
	insert("delivery.delivery_snapshot_seal", sealRow)
	insert("delivery.delivery_generation", map[string]any{"generation_id": set.Delivery.GenerationID, "target_id": "target", "candidate_id": candidateID, "snapshot_seal_id": seal.SealID, "plan_id": planID, "plan_digest": seal.PlanDigest, "artifact_root": seal.ArtifactRoot, "artifact_root_digest": seal.ArtifactRootDigest, "serving_artifact_digest": seal.ServingArtifactDigest, "compiled_graph_digest": seal.CompiledGraphDigest, "compiled_config_digest": seal.CompiledConfigDigest, "security_domain_fingerprint": seal.SecurityDomainFingerprint, "generation_revision": 1, "created_at": now})
	insert("delivery.delivery_publication", map[string]any{"publication_id": set.Delivery.PublicationID, "target_id": "target", "generation_id": set.Delivery.GenerationID, "candidate_id": candidateID, "snapshot_seal_id": seal.SealID, "expected_target_revision": 1, "result_target_revision": 2, "actor_id": "actor", "state": "committed", "request_digest": digestBytes([]byte("publication")), "created_at": now, "committed_at": now})
	insert("delivery.delivery_active_pointer", map[string]any{"target_id": "target", "generation_id": set.Delivery.GenerationID, "publication_id": set.Delivery.PublicationID, "changed_at": now})
	insert("ducklake.catalog_identity", map[string]any{"physical_pool_id": seal.PhysicalPoolID, "catalog_database": duckDB.Name, "catalog_id": seal.CatalogID, "catalog_uuid": seal.CatalogUUID, "metadata_schema": "catalog_metadata", "created_at": now})
	if err := adminTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := controlAdmin.Exec(t.Context(), "GRANT USAGE ON SCHEMA delivery,ducklake TO managed_control_reader; GRANT SELECT ON ALL TABLES IN SCHEMA delivery,ducklake TO managed_control_reader"); err != nil {
		t.Fatal(err)
	}
	if _, err := duckAdmin.Exec(t.Context(), `CREATE SCHEMA catalog_metadata;CREATE TABLE catalog_metadata.ducklake_metadata(key text,value text,scope text,scope_id bigint);CREATE TABLE catalog_metadata.ducklake_snapshot(snapshot_id bigint PRIMARY KEY);CREATE TABLE catalog_metadata.ducklake_snapshot_changes(snapshot_id bigint PRIMARY KEY,commit_extra_info text);INSERT INTO catalog_metadata.ducklake_metadata VALUES ('version','1',NULL,NULL);GRANT USAGE ON SCHEMA catalog_metadata TO managed_duck_reader;GRANT SELECT ON ALL TABLES IN SCHEMA catalog_metadata TO managed_duck_reader`); err != nil {
		t.Fatal(err)
	}
	if _, err := duckAdmin.Exec(t.Context(), "INSERT INTO catalog_metadata.ducklake_snapshot VALUES ($1)", seal.DuckLakeSnapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err := duckAdmin.Exec(t.Context(), "INSERT INTO catalog_metadata.ducklake_snapshot_changes VALUES ($1,$2)", seal.DuckLakeSnapshotID, markerString); err != nil {
		t.Fatal(err)
	}
	rootCA, err := os.ReadFile(h.RootCertPath())
	if err != nil {
		t.Fatal(err)
	}
	tlsURL := func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed.RawQuery = "sslmode=verify-full"
		return parsed.String()
	}
	readback := NativePostgresReadback{Set: set, MetadataSchema: "catalog_metadata", ControlURL: tlsURL(controlDB.PrivateURL(controlRole)), DuckLakeURL: tlsURL(duckDB.PrivateURL(duckRole)), RootCA: string(rootCA), Roles: RuntimeRoles{Control: controlRole.Name, DuckLake: duckRole.Name}}
	evidence, err := readback.Verify(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !validContentDigest(evidence.ControlDigest) || !validContentDigest(evidence.DuckLakeDigest) || evidence.Catalog != set.Catalog {
		t.Fatal("incomplete exact native readback")
	}
	t.Run("foreign publication", func(t *testing.T) {
		foreign := readback
		foreign.Set.Delivery.PublicationID = planID
		if _, err := foreign.Verify(t.Context()); err == nil {
			t.Fatal("foreign native publication accepted")
		}
	})
	t.Run("foreign seal field", func(t *testing.T) {
		foreign := readback
		foreign.Set.Serving.CompiledConfigDigest = digestBytes([]byte("foreign"))
		if _, err := foreign.Verify(t.Context()); err == nil {
			t.Fatal("foreign sealed configuration accepted")
		}
	})
	t.Run("foreign metadata schema", func(t *testing.T) {
		foreign := readback
		foreign.MetadataSchema = "other_metadata"
		if _, err := foreign.Verify(t.Context()); err == nil {
			t.Fatal("guessed metadata schema accepted")
		}
	})
	t.Run("wrong TLS authority", func(t *testing.T) {
		foreign := readback
		_, other, _ := managedCredentialFixture(t)
		foreign.RootCA = other.PostgresRootCA
		if _, err := foreign.Verify(t.Context()); err == nil {
			t.Fatal("untrusted server certificate accepted")
		}
	})
	t.Run("foreign catalog marker", func(t *testing.T) {
		other := marker
		other.PlanDigest = digestBytes([]byte("foreign"))
		canonical, _ := other.CanonicalJSON()
		if _, err := duckAdmin.Exec(t.Context(), "UPDATE catalog_metadata.ducklake_snapshot_changes SET commit_extra_info=$1", canonical); err != nil {
			t.Fatal(err)
		}
		defer duckAdmin.Exec(context.Background(), "UPDATE catalog_metadata.ducklake_snapshot_changes SET commit_extra_info=$1", markerString)
		if _, err := readback.Verify(t.Context()); err == nil {
			t.Fatal("foreign DuckLake commit marker accepted")
		}
	})
	t.Run("privileged runtime role", func(t *testing.T) {
		if _, err := controlAdmin.Exec(t.Context(), "ALTER ROLE managed_control_reader CREATEDB"); err != nil {
			t.Fatal(err)
		}
		defer controlAdmin.Exec(context.Background(), "ALTER ROLE managed_control_reader NOCREATEDB")
		if _, err := readback.Verify(t.Context()); err == nil {
			t.Fatal("privileged runtime role accepted")
		}
	})
	t.Run("ordinary schema owner", func(t *testing.T) {
		if _, err := controlAdmin.Exec(t.Context(), "ALTER SCHEMA delivery OWNER TO managed_control_reader"); err != nil {
			t.Fatal(err)
		}
		defer controlAdmin.Exec(context.Background(), "ALTER SCHEMA delivery OWNER TO postgres; GRANT USAGE ON SCHEMA delivery TO managed_control_reader")
		if _, err := readback.Verify(t.Context()); err == nil {
			t.Fatal("ordinary nonsuperuser schema owner accepted as runtime")
		}
	})
	t.Run("ordinary owner membership", func(t *testing.T) {
		if _, err := controlAdmin.Exec(t.Context(), "ALTER SCHEMA delivery OWNER TO "+pgx.Identifier{ownerRole.Name}.Sanitize()+"; GRANT managed_schema_owner TO managed_control_reader"); err != nil {
			t.Fatal(err)
		}
		defer controlAdmin.Exec(context.Background(), "ALTER SCHEMA delivery OWNER TO postgres; REVOKE managed_schema_owner FROM managed_control_reader; GRANT USAGE ON SCHEMA delivery TO managed_control_reader")
		if _, err := readback.Verify(t.Context()); err == nil {
			t.Fatal("ordinary schema-owner membership accepted as runtime")
		}
	})
	t.Run("ordinary protected table owner", func(t *testing.T) {
		if _, err := controlAdmin.Exec(t.Context(), "ALTER TABLE delivery.delivery_target OWNER TO managed_control_reader"); err != nil {
			t.Fatal(err)
		}
		defer controlAdmin.Exec(context.Background(), "ALTER TABLE delivery.delivery_target OWNER TO postgres; GRANT SELECT ON delivery.delivery_target TO managed_control_reader")
		if _, err := readback.Verify(t.Context()); err == nil {
			t.Fatal("ordinary nonsuperuser protected table owner accepted as runtime")
		}
	})
	if _, err := readback.Verify(t.Context()); err != nil {
		t.Fatal("negative checks altered valid native readback")
	}
}
