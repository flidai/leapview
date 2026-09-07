package postgres

import (
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/recoveryset"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func recoveryRetentionTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "recovery_retention_lifecycle_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{accesspostgres.SchemaSQL(), eventspostgres.SchemaSQL()} {
		if _, err := tx.Exec(t.Context(), schema); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := recoverysetpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := servingstatepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ducklakepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecoveryRetentionRootMaintenanceRetiresAndExpires(t *testing.T) {
	p := recoveryRetentionTestDB(t)
	ctx := t.Context()
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	const (
		targetID      = "recovery-retention-target"
		planID        = "0198f2c0-7c7a-7f00-8a11-000000006001"
		candidateID   = "0198f2c0-7c7a-7f00-8a11-000000006002"
		attemptID     = "0198f2c0-7c7a-7f00-8a11-000000006003"
		sealID        = "0198f2c0-7c7a-7f00-8a11-000000006004"
		generationID  = "0198f2c0-7c7a-7f00-8a11-000000006005"
		setID         = "0198f2c0-7c7a-7f00-8a11-000000006006"
		publicationID = "0198f2c0-7c7a-7f00-8a11-000000006007"
		rootID        = "0198f2c0-7c7a-7f00-8a11-000000006008"
		catalogUUID   = "0198f2c0-7c7a-7f00-8a11-000000006009"
	)
	if _, err := New(p).CreateTarget(ctx, TargetInput{TargetID: targetID, ProjectID: "recovery-project", Environment: "prod"}); err != nil {
		t.Fatal(err)
	}
	planDigest := digest('a')
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_plan(plan_id,target_id,plan_revision,plan_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,artifact_digest,qualification_digest,qualification_required,approval_required,approval_policy_revision,plan_document) VALUES ($1::uuid,$2,1,$3,$3,$3,$3,$3,$3,false,false,1,'{}'::jsonb)`, planID, targetID, planDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,plan_id,status,candidate_revision,artifact_digest) VALUES ($1::uuid,$2,$3::uuid,'building',1,$4)`, candidateID, targetID, planID, planDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_build_attempt(attempt_id,plan_id,candidate_id,owner_id,physical_pool_id,catalog_id,fencing_epoch,request_digest,plan_digest,state,namespace,lease_expires_at,session_identity,snapshot_id,commit_marker,finished_at) VALUES ($1::uuid,$2::uuid,$3::uuid,'builder','recovery-pool','recovery-catalog',1,$4,$4,'committed','candidate/recovery',clock_timestamp()+interval '1 hour','recovery-session',1,'{"committed":true}'::jsonb,clock_timestamp())`, attemptID, planID, candidateID, planDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_snapshot_seal(seal_id,attempt_id,candidate_id,physical_pool_id,tenant_domain,region,encryption_domain,object_namespace,catalog_database,catalog_id,catalog_uuid,catalog_version,ducklake_snapshot_id,relation_namespace,relation_manifest_digest,closure_digest,object_root,object_root_digest,artifact_root,artifact_root_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,request_digest,plan_digest,compatibility_digest,serving_artifact_id,serving_artifact_digest,duckdb_version,runtime_version,ducklake_extension_version,ducklake_spec_version,catalog_schema_version,qualification_evidence) VALUES ($1::uuid,$2::uuid,$3::uuid,'recovery-pool','tenant','region','encryption','objects/recovery','ducklake','recovery-catalog',$4::uuid,1,1,'candidate/recovery',$5,$6,'objects/recovery',$7,'artifacts/recovery',$8,$9,$10,$11,$12,$13,$14,'artifact-recovery',$15,'1','runtime','1','1','1','{}'::jsonb)`, sealID, attemptID, candidateID, catalogUUID, digest('b'), digest('c'), digest('d'), digest('e'), digest('f'), digest('0'), digest('1'), digest('2'), digest('3'), digest('4'), digest('5')); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_candidate SET snapshot_seal_id=$2::uuid,status='qualified',qualification_digest=$3,qualified_at=clock_timestamp() WHERE candidate_id=$1::uuid`, candidateID, sealID, digest('6')); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO delivery.delivery_generation(generation_id,target_id,candidate_id,snapshot_seal_id,plan_id,plan_digest,artifact_root,artifact_root_digest,serving_artifact_digest,compiled_graph_digest,compiled_config_digest,security_domain_fingerprint,generation_revision) VALUES ($1::uuid,$2,$3::uuid,$4::uuid,$5::uuid,$6,'artifacts/recovery',$7,$8,$9,$10,$11,1)`, generationID, targetID, candidateID, sealID, planID, planDigest, digest('e'), digest('f'), digest('0'), digest('1'), digest('2')); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `INSERT INTO ducklake.catalog_identity(physical_pool_id,catalog_database,catalog_id,catalog_uuid,metadata_schema) VALUES ('recovery-pool','ducklake','recovery-catalog',$1::uuid,'lake')`, catalogUUID); err != nil {
		t.Fatal(err)
	}
	compat := recoveryset.CompatibilityTuple{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1", StorageImplementation: "s3", ObjectNamingContract: "uuidv7:v1"}
	compatDigest, err := compat.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set := recoveryset.RecoverySet{
		ID: setID, SchemaVersion: recoveryset.SchemaVersion,
		ClusterPoints: []recoveryset.ClusterRecoveryPoint{{DatabaseRole: recoveryset.DatabaseControl, ClusterIdentity: "cluster", DatabaseIdentity: "control", RecoveryIdentity: "lsn:0/1"}, {DatabaseRole: recoveryset.DatabaseDuckLake, ClusterIdentity: "cluster", DatabaseIdentity: "ducklake", RecoveryIdentity: "lsn:0/1"}},
		Delivery:      recoveryset.DeliveryPointer{TargetID: targetID, GenerationID: generationID, PublicationID: publicationID, TargetRevision: 1},
		Serving:       recoveryset.SnapshotSeal{SealID: sealID, PhysicalPoolID: "recovery-pool", TenantDomain: "tenant", Region: "region", EncryptionDomain: "encryption", ObjectNamespace: "objects/recovery", CatalogDatabase: "ducklake", CatalogID: "recovery-catalog", CatalogUUID: catalogUUID, CatalogVersion: 1, DuckLakeSnapshotID: 1, RelationNamespace: "candidate/recovery", RelationManifestDigest: digest('b'), ClosureDigest: digest('c'), ObjectRoot: "objects/recovery", ObjectRootDigest: digest('d'), ArtifactRoot: "artifacts/recovery", ArtifactRootDigest: digest('e'), ServingArtifactID: "artifact-recovery", ServingArtifactDigest: digest('f'), CompiledGraphDigest: digest('0'), CompiledConfigDigest: digest('1'), SecurityDomainFingerprint: digest('2'), RequestDigest: digest('3'), PlanDigest: planDigest, CompatibilityDigest: compatDigest, DuckDBVersion: "1", RuntimeVersion: "runtime", DuckLakeExtensionVersion: "1", DuckLakeSpecVersion: "1", CatalogSchemaVersion: "1"},
		Catalog:       recoveryset.CatalogCommit{CatalogID: "recovery-catalog", CatalogDatabase: "ducklake", CatalogUUID: catalogUUID, CatalogVersion: 1, SnapshotID: 1},
		ObjectRoots:   []recoveryset.ObjectRoot{{Kind: recoveryset.ObjectRootDuckLake, URI: "objects/recovery", VersionID: "1", Digest: digest('d')}, {Kind: recoveryset.ObjectRootServingArtifact, URI: "artifacts/recovery", VersionID: "1", Digest: digest('e')}},
		Compatibility: compat, FenceEpoch: 1, AuditIdentity: "audit", Status: recoveryset.StatusPrepared, CreatedBy: "operator", CreatedAt: time.Now().UTC(),
	}
	createdSet, err := recoverysetpostgres.New(p).Create(ctx, set)
	if err != nil {
		t.Fatal(err)
	}
	evidence := []byte(`{"recovery_set_id":"` + setID + `","frontier_digest":"` + createdSet.FrontierDigest + `"}`)
	if _, err := p.Exec(ctx, `SELECT delivery.create_recovery_retention_root($1::uuid,$2,$3::uuid,$4::uuid,NULL,$5::jsonb)`, rootID, targetID, generationID, sealID, evidence); err == nil {
		t.Fatal("recovery retention root accepted an unbounded expiry")
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	root := DeliveryRetentionRoot{RootID: rootID, TargetID: targetID, GenerationID: generationID, SnapshotSealID: sealID, RootKind: "recovery", State: "live", ExpiresAt: expiresAt, Evidence: evidence}
	if _, err := New(p).CreateRetentionRoot(ctx, root); err == nil {
		t.Fatal("recovery retention root synthesized missing physical retention")
	}
	if _, err := p.Exec(ctx, `INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state) VALUES ('recovery-pool','recovery-catalog',1,'live')`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(p).CreateRetentionRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE delivery.delivery_retention_root SET expires_at=clock_timestamp()-interval '1 second' WHERE root_id=$1::uuid`, rootID); err != nil {
		t.Fatal(err)
	}
	drain := NewMaintenance(p)
	result, err := drain.Drain(ctx, "recovery-pool", "recovery-catalog", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Retired != 1 || result.Expired != 1 {
		t.Fatalf("recovery retention drain = %#v, want one retirement and expiry", result)
	}
	var state string
	if err := p.QueryRow(ctx, `SELECT state FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, rootID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "expired" {
		t.Fatalf("recovery root state = %q, want expired", state)
	}
}
