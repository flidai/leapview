package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func servingDB(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "serving-runtime", Login: true})
	maintenance := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "serving-maintenance", Login: true})
	db := h.NewDatabase(t, "")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `CREATE SCHEMA IF NOT EXISTS delivery; CREATE TABLE delivery.delivery_target(target_id text PRIMARY KEY,project_id text NOT NULL,environment text NOT NULL,target_revision bigint NOT NULL DEFAULT 1); CREATE TABLE delivery.delivery_snapshot_seal(seal_id uuid PRIMARY KEY,ducklake_snapshot_id bigint NOT NULL,physical_pool_id text,catalog_id text,catalog_database text,catalog_uuid text); CREATE TABLE delivery.delivery_candidate(candidate_id uuid PRIMARY KEY,target_id text NOT NULL REFERENCES delivery.delivery_target,snapshot_seal_id uuid REFERENCES delivery.delivery_snapshot_seal(seal_id)); CREATE TABLE delivery.delivery_generation(generation_id uuid PRIMARY KEY,target_id text NOT NULL REFERENCES delivery.delivery_target,snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),serving_artifact_digest text NOT NULL,compiled_graph_digest text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp()); CREATE TABLE delivery.delivery_active_pointer(target_id text PRIMARY KEY,generation_id uuid NOT NULL REFERENCES delivery.delivery_generation(generation_id),publication_id uuid NOT NULL); CREATE TABLE delivery.delivery_publication(publication_id uuid PRIMARY KEY,generation_id uuid NOT NULL REFERENCES delivery.delivery_generation,target_id text NOT NULL,state text NOT NULL,actor_id text NOT NULL,committed_at timestamptz); CREATE TABLE delivery.delivery_retention_root(root_id uuid PRIMARY KEY,target_id text NOT NULL,generation_id uuid,snapshot_seal_id uuid,candidate_id uuid,root_kind text NOT NULL,state text NOT NULL,expires_at timestamptz,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),retired_at timestamptz,expired_at timestamptz);`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `GRANT USAGE ON SCHEMA delivery TO leapview_control_runtime; GRANT SELECT ON ALL TABLES IN SCHEMA delivery TO leapview_control_runtime; GRANT SELECT ON delivery.delivery_retention_root TO leapview_control_runtime`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	p, err := pgxpool.New(t.Context(), db.URL(runtime))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Ping(t.Context()); err != nil {
		p.Close()
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	m, err := pgxpool.New(t.Context(), db.URL(maintenance))
	if err != nil {
		p.Close()
		t.Fatal(err)
	}
	if err := m.Ping(t.Context()); err != nil {
		m.Close()
		p.Close()
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return admin, p, m
}

func seedGeneration(t *testing.T, admin *pgxpool.Pool, generation, target, publication, attempt, seal, digest, graphDigest string, snapshot int64) {
	seedGenerationEnvironment(t, admin, generation, target, publication, attempt, seal, digest, graphDigest, snapshot, "prod")
}

func seedGenerationEnvironment(t *testing.T, admin *pgxpool.Pool, generation, target, publication, attempt, seal, digest, graphDigest string, snapshot int64, environment string) {
	t.Helper()
	ctx := t.Context()
	_, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_target(target_id,project_id,environment) VALUES($1,'project_demo',$2)`, target, environment)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO delivery.delivery_snapshot_seal(seal_id,ducklake_snapshot_id) VALUES($1::uuid,$2)`, seal, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO delivery.delivery_generation(generation_id,target_id,snapshot_seal_id,serving_artifact_digest,compiled_graph_digest) VALUES($1::uuid,$2,$3::uuid,$4,$5)`, generation, target, seal, digest, graphDigest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO delivery.delivery_retention_root(root_id,target_id,generation_id,snapshot_seal_id,root_kind,state) VALUES($1::uuid,$2,$3::uuid,$4::uuid,'generation','live')`, generation, target, generation, seal)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO delivery.delivery_active_pointer(target_id,generation_id,publication_id) VALUES($1,$2::uuid,$3::uuid)`, target, generation, publication)
	if err != nil {
		t.Fatal(err)
	}
	_, err = admin.Exec(ctx, `INSERT INTO delivery.delivery_publication(publication_id,generation_id,target_id,state,actor_id,committed_at) VALUES($1::uuid,$2::uuid,$3,'committed','test',clock_timestamp())`, publication, generation, target)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetentionInventoryScopesRootsLeasesAndSnapshotEvidence(t *testing.T) {
	admin, runtime, _ := servingDB(t)
	ctx := t.Context()
	digest := "sha256:" + strings.Repeat("a", 64)
	graphDigest := testGraph(t).Digest()
	generation := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seal := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	target := "target_inventory"
	seedGenerationEnvironment(t, admin, generation, target, "cccccccc-cccc-cccc-cccc-cccccccccccc", "dddddddd-dddd-dddd-dddd-dddddddddddd", seal, digest, graphDigest, 41, "prod")
	if _, err := admin.Exec(ctx, `UPDATE delivery.delivery_snapshot_seal SET physical_pool_id='pool_inventory', catalog_id='catalog_inventory', catalog_database='db_inventory', catalog_uuid='catalog-uuid-inventory' WHERE seal_id=$1::uuid`, seal); err != nil {
		t.Fatal(err)
	}
	// A candidate root has no generation or direct seal pointer. The observer
	// must resolve its snapshot through delivery_candidate.snapshot_seal_id.
	candidate := "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	candidateSeal := "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if _, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_snapshot_seal(seal_id,ducklake_snapshot_id,physical_pool_id,catalog_id,catalog_database,catalog_uuid) VALUES($1::uuid,42,'pool_candidate','catalog_candidate','db_candidate','catalog-uuid-candidate')`, candidateSeal); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_candidate(candidate_id,target_id,snapshot_seal_id) VALUES($1::uuid,$2,$3::uuid)`, candidate, target, candidateSeal); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_retention_root(root_id,target_id,candidate_id,root_kind,state) VALUES('11111111-1111-1111-1111-111111111111',$1,$2::uuid,'candidate','live')`, target, candidate); err != nil {
		t.Fatal(err)
	}
	// Add another same-scope root so ordering is observable independently of
	// insertion order.
	if _, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_retention_root(root_id,target_id,generation_id,snapshot_seal_id,root_kind,state) VALUES('00000000-0000-0000-0000-000000000001',$1,$2::uuid,$3::uuid,'rollback','retiring')`, target, generation, seal); err != nil {
		t.Fatal(err)
	}
	// Lease rows are inserted with known IDs to make the deterministic order
	// assertion independent of UUID generation. Their states are intentionally
	// active, expired, and released; all remain visible to the observer.
	for _, id := range []string{"lease-active", "lease-expired", "lease-released"} {
		if _, err := admin.Exec(ctx, `INSERT INTO serving_state.reader_lease(lease_id,generation_id,ducklake_snapshot_id,owner_id,expires_at) VALUES($1,$2::uuid,41,$3,clock_timestamp()+interval '10 minutes')`, id, generation, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `ALTER TABLE serving_state.reader_lease DISABLE TRIGGER reader_lease_mutation; UPDATE serving_state.reader_lease SET acquired_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE lease_id='lease-expired'; ALTER TABLE serving_state.reader_lease ENABLE TRIGGER reader_lease_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE serving_state.reader_lease SET released_at=clock_timestamp() WHERE lease_id='lease-released'`); err != nil {
		t.Fatal(err)
	}
	// A target in another environment must not appear when querying the prod
	// target scope (and the same target with the wrong environment is empty).
	otherGeneration := "22222222-2222-2222-2222-222222222222"
	otherSeal := "55555555-5555-5555-5555-555555555555"
	seedGenerationEnvironment(t, admin, otherGeneration, "target_inventory_other", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444", otherSeal, digest, graphDigest, 43, "staging")
	// A malformed direct root must remain observable to maintenance, but its
	// cross-target seal must never disclose another target's pool or catalog.
	if _, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_retention_root(root_id,target_id,snapshot_seal_id,root_kind,state) VALUES('00000000-0000-0000-0000-000000000002',$1,$2::uuid,'recovery','retiring')`, target, otherSeal); err != nil {
		t.Fatal(err)
	}

	r := New(runtime)
	first, err := r.RetentionInventory(ctx, target, "prod")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.RetentionInventory(ctx, target, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("retention inventory is not stable across reads:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if len(first.Roots) != 4 || len(first.ReaderLeases) != 3 {
		t.Fatalf("inventory cardinality roots=%d leases=%d, want 4/3", len(first.Roots), len(first.ReaderLeases))
	}
	if first.Roots[0].Kind != "candidate" || first.Roots[0].Snapshot == nil || first.Roots[0].Snapshot.SnapshotID != 42 || first.Roots[0].Snapshot.PhysicalPoolID != "pool_candidate" || first.Roots[0].Snapshot.CatalogID != "catalog_candidate" {
		t.Fatalf("candidate root snapshot evidence = %#v", first.Roots[0])
	}
	if first.Roots[1].Kind != "generation" || first.Roots[1].State != "live" || first.Roots[1].Snapshot == nil || first.Roots[1].Snapshot.SnapshotID != 41 || first.Roots[1].Snapshot.PhysicalPoolID != "pool_inventory" || first.Roots[1].Snapshot.CatalogID != "catalog_inventory" {
		t.Fatalf("active generation root = %#v", first.Roots[1])
	}
	if first.Roots[2].Kind != "recovery" || first.Roots[2].State != "retiring" || first.Roots[2].Snapshot != nil {
		t.Fatalf("cross-target seal evidence leaked through malformed root: %#v", first.Roots[2])
	}
	if first.Roots[3].Kind != "rollback" || first.Roots[3].State != "retiring" {
		t.Fatalf("root ordering/state = %#v", first.Roots)
	}
	for i, want := range []struct{ id, state string }{{"lease-active", "active"}, {"lease-expired", "expired"}, {"lease-released", "released"}} {
		if first.ReaderLeases[i].LeaseID != want.id || first.ReaderLeases[i].State != want.state || first.ReaderLeases[i].Snapshot.SnapshotID != 41 || first.ReaderLeases[i].Snapshot.CatalogID != "catalog_inventory" {
			t.Fatalf("lease[%d] = %#v, want id/state=%s/%s and sealed catalog", i, first.ReaderLeases[i], want.id, want.state)
		}
	}
	empty, err := r.RetentionInventory(ctx, target, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Roots) != 0 || len(empty.ReaderLeases) != 0 {
		t.Fatalf("wrong-environment inventory leaked rows: %#v", empty)
	}
	other, err := r.RetentionInventory(ctx, "target_inventory_other", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Roots) != 1 || other.Roots[0].TargetID != "target_inventory_other" || other.Roots[0].Snapshot == nil || other.Roots[0].Snapshot.SnapshotID != 43 {
		t.Fatalf("cross-target inventory = %#v", other)
	}
}

func seedBundle(t *testing.T, admin *pgxpool.Pool, generation, digest, graphDigest string) {
	t.Helper()
	_, err := admin.Exec(t.Context(), `INSERT INTO serving_state.bundle(generation_id,project_id,environment,artifact_id,artifact_digest,compiled_graph_digest,artifact_format,artifact_locator,storage_security_domain,artifact_content_type,artifact_metadata_digest,manifest_json,project_digest,access_policy_json,dashboard_publications_json,dashboard_appearances_json,size_bytes,created_by) VALUES($1::uuid,'project_demo','prod','artifact-'||substr($2,8),$2,$3,'tar.gz','serving-artifacts/'||substr($2,8)||'.tar.gz','runtime','application/gzip',$4,'{}'::jsonb,$5,'{}'::jsonb,'{}'::jsonb,'{}'::jsonb,1,'test')`, generation, digest, graphDigest, "sha256:"+strings.Repeat("9", 64), "sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
}

func testGraph(t *testing.T) projectgraph.ProjectGraph {
	g, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project_demo", Kind: projectgraph.KindProject, Name: "project"}, {ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"}}, []projectgraph.Edge{{From: "project_demo", To: "dashboard", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRetentionMaintenanceRoleBoundary(t *testing.T) {
	admin, runtime, maintenance := servingDB(t)
	ctx := t.Context()

	var runtimeExecute, maintenanceExecute bool
	if err := admin.QueryRow(ctx, `
		SELECT has_function_privilege('leapview_control_runtime', 'serving_state.release_expired_query_snapshot_leases(text, integer)', 'EXECUTE'),
		       has_function_privilege('leapview_control_maintenance', 'serving_state.release_expired_query_snapshot_leases(text, integer)', 'EXECUTE')`).Scan(&runtimeExecute, &maintenanceExecute); err != nil {
		t.Fatal(err)
	}
	if runtimeExecute {
		t.Fatal("runtime role has serving-state retention EXECUTE privilege")
	}
	if !maintenanceExecute {
		t.Fatal("maintenance role is missing serving-state retention EXECUTE privilege")
	}

	if err := New(runtime).ReleaseExpiredQuerySnapshotLeases(ctx, "prod"); err == nil {
		t.Fatal("runtime expired lease reconciliation unexpectedly succeeded")
	}
	if err := New(maintenance).ReleaseExpiredQuerySnapshotLeases(ctx, "prod"); err != nil {
		t.Fatalf("maintenance expired lease reconciliation: %v", err)
	}
}

func TestExpiredLeaseMaintenancePreservesLiveRootAndImmutableEvidence(t *testing.T) {
	admin, runtime, maintenance := servingDB(t)
	ctx := t.Context()
	generation := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	digest := "sha256:" + strings.Repeat("a", 64)
	seal := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	seedGeneration(t, admin, generation, "target_retention", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "dddddddd-dddd-dddd-dddd-dddddddddddd", seal, digest, testGraph(t).Digest(), 41)
	lease, err := New(runtime).CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{
		ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 41,
		OwnerID: "retention-reader", ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	liveLease, err := New(runtime).CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{
		ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 41,
		OwnerID: "live-reader", ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	var generations, seals int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM delivery.delivery_generation), (SELECT count(*) FROM delivery.delivery_snapshot_seal)`).Scan(&generations, &seals); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `ALTER TABLE serving_state.reader_lease DISABLE TRIGGER reader_lease_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE serving_state.reader_lease SET acquired_at=clock_timestamp()-interval '2 minutes', expires_at=clock_timestamp()-interval '1 minute' WHERE lease_id=$1`, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `ALTER TABLE serving_state.reader_lease ENABLE TRIGGER reader_lease_mutation`); err != nil {
		t.Fatal(err)
	}
	if err := New(maintenance).ReleaseExpiredQuerySnapshotLeases(ctx, "prod"); err != nil {
		t.Fatalf("maintenance expired lease reconciliation: %v", err)
	}
	var released bool
	if err := admin.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM serving_state.reader_lease WHERE lease_id=$1`, lease).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("expired reader lease was not released")
	}
	if err := admin.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM serving_state.reader_lease WHERE lease_id=$1`, liveLease).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("live reader lease was released by expired-lease maintenance")
	}
	var rootState string
	if err := admin.QueryRow(ctx, `SELECT state FROM delivery.delivery_retention_root WHERE root_id=$1::uuid`, generation).Scan(&rootState); err != nil {
		t.Fatal(err)
	}
	if rootState != "live" {
		t.Fatalf("maintenance changed live delivery root state to %q", rootState)
	}
	var generationsAfter, sealsAfter int
	if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM delivery.delivery_generation), (SELECT count(*) FROM delivery.delivery_snapshot_seal)`).Scan(&generationsAfter, &sealsAfter); err != nil {
		t.Fatal(err)
	}
	if generationsAfter != generations || sealsAfter != seals {
		t.Fatalf("maintenance mutated immutable delivery evidence: generations=%d/%d seals=%d/%d", generationsAfter, generations, sealsAfter, seals)
	}
}

func TestExpiredLeaseMaintenanceSkipsLockedRows(t *testing.T) {
	admin, runtime, maintenance := servingDB(t)
	ctx := t.Context()
	generation := "12121212-1212-1212-1212-121212121212"
	digest := "sha256:" + strings.Repeat("b", 64)
	seedGeneration(t, admin, generation, "target_retention_locked", "13131313-1313-1313-1313-131313131313", "14141414-1414-1414-1414-141414141414", "15151515-1515-1515-1515-151515151515", digest, testGraph(t).Digest(), 51)
	reader := New(runtime)
	first, err := reader.CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 51, OwnerID: "locked-reader", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.CreateQuerySnapshotLease(ctx, servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 51, OwnerID: "free-reader", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `ALTER TABLE serving_state.reader_lease DISABLE TRIGGER reader_lease_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE serving_state.reader_lease SET acquired_at=clock_timestamp()-interval '2 minutes', expires_at=clock_timestamp()-interval '1 minute' WHERE lease_id IN ($1,$2)`, first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `ALTER TABLE serving_state.reader_lease ENABLE TRIGGER reader_lease_mutation`); err != nil {
		t.Fatal(err)
	}
	lockTx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	if _, err := lockTx.Exec(ctx, `SELECT lease_id FROM serving_state.reader_lease WHERE lease_id=$1 FOR UPDATE`, first); err != nil {
		t.Fatal(err)
	}
	maintenanceCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if err := New(maintenance).ReleaseExpiredQuerySnapshotLeases(maintenanceCtx, "prod"); err != nil {
		t.Fatalf("maintenance blocked on locked lease: %v", err)
	}
	var firstReleased, secondReleased bool
	if err := admin.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM serving_state.reader_lease WHERE lease_id=$1`, first).Scan(&firstReleased); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM serving_state.reader_lease WHERE lease_id=$1`, second).Scan(&secondReleased); err != nil {
		t.Fatal(err)
	}
	if firstReleased {
		t.Fatal("maintenance changed an expired lease held by another transaction")
	}
	if !secondReleased {
		t.Fatal("maintenance did not process an unlocked expired lease")
	}
}

func TestAdmitGenerationBundleAndActiveRead(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "11111111-1111-1111-1111-111111111111"
	digest := "sha256:" + strings.Repeat("a", 64)
	seedGeneration(t, admin, generation, "target_demo", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444", digest, testGraph(t).Digest(), 7)
	r := New(pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := GenerationBundleInput{GenerationID: generation, ProjectID: "project_demo", Environment: "prod", ProjectDigest: "sha256:" + strings.Repeat("b", 64), ArtifactLocator: "serving-artifacts/" + strings.Repeat("a", 64) + ".tar.gz", StorageSecurityDomain: "runtime", ArtifactContentType: "application/gzip", ArtifactMetadataDigest: "sha256:" + strings.Repeat("9", 64), Artifact: servingstate.Artifact{ID: "artifact-" + strings.Repeat("a", 64), ServingStateID: servingstate.ID(generation), Digest: digest, Format: "tar.gz", ManifestJSON: `{"name":"demo"}`, SizeBytes: 1}, AccessPolicyJSON: `{}`, CreatedBy: "test"}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, testGraph(t)); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, artifact, err := r.ActiveArtifact(t.Context(), "project_demo", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if state.ID != servingstate.ID(generation) || artifact.ID != "artifact-"+strings.Repeat("a", 64) || artifact.Path != "" || artifact.Locator != input.ArtifactLocator || artifact.StorageSecurityDomain != "runtime" || artifact.ContentType != "application/gzip" || artifact.MetadataDigest != input.ArtifactMetadataDigest {
		t.Fatalf("active=%#v %#v", state, artifact)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, testGraph(t)); err != nil {
		t.Fatal("exact replay", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Child replay is exact, not merely idempotent: a tampered payload or
	// missing asset is surfaced as a conflict instead of silently accepted.
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.asset DISABLE TRIGGER asset_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE serving_state.asset SET payload_json='{"tampered":true}'::jsonb WHERE generation_id=$1::uuid AND logical_asset_id='dashboard'`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.asset ENABLE TRIGGER asset_immutable`); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, testGraph(t)); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("tampered child replay err=%v", err)
	}
	_ = tx.Rollback(t.Context())
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.asset DISABLE TRIGGER asset_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.asset_edge DISABLE TRIGGER asset_edge_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `DELETE FROM serving_state.asset_edge WHERE generation_id=$1::uuid AND (from_logical_asset_id='dashboard' OR to_logical_asset_id='dashboard')`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.asset_edge ENABLE TRIGGER asset_edge_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `DELETE FROM serving_state.asset WHERE generation_id=$1::uuid AND logical_asset_id='dashboard'`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO serving_state.asset(generation_id,snapshot_id,logical_asset_id,asset_type,asset_key,payload_schema,payload_json,content_hash) VALUES($1::uuid,'extra_snapshot','extra','model','extra','project.graph.v1','{}','sha256:extra')`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE serving_state.asset ENABLE TRIGGER asset_immutable`); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM serving_state.asset WHERE generation_id=$1::uuid AND logical_asset_id='extra'`, generation).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, testGraph(t)); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("extra child replay err=%v", err)
	}
	// Even if a caller elects to commit after observing the conflict, no
	// replay-side child writes are allowed to leak into its transaction.
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM serving_state.asset WHERE generation_id=$1::uuid AND logical_asset_id='extra'`, generation).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("conflicting replay changed extra child count: before=%d after=%d", before, after)
	}
}

func TestRecordDuckLakeSnapshotVerifiesImmutableSealedEvidence(t *testing.T) {
	admin, pool, _ := servingDB(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	graphDigest := testGraph(t).Digest()
	generation := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seedGeneration(t, admin, generation, "target_snapshot", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "cccccccc-cccc-cccc-cccc-cccccccccccc", "dddddddd-dddd-dddd-dddd-dddddddddddd", digest, graphDigest, 37)
	seedBundle(t, admin, generation, digest, graphDigest)
	r := New(pool)
	if err := r.RecordDuckLakeSnapshot(t.Context(), servingstate.ID(generation), 37); err != nil {
		t.Fatalf("exact sealed snapshot replay: %v", err)
	}
	if err := r.RecordDuckLakeSnapshot(t.Context(), servingstate.ID(generation), 0); err == nil {
		t.Fatal("zero requested snapshot unexpectedly succeeded")
	}
	if err := r.RecordDuckLakeSnapshot(t.Context(), servingstate.ID(generation), 38); err == nil {
		t.Fatal("mismatched requested snapshot unexpectedly succeeded")
	}
	if err := r.RecordDuckLakeSnapshot(t.Context(), servingstate.ID("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"), 37); !errors.Is(err, servingstate.ErrNotFound) {
		t.Fatalf("missing sealed state error = %v, want servingstate.ErrNotFound", err)
	}

	zeroGeneration := "11111111-1111-1111-1111-111111111111"
	seedGeneration(t, admin, zeroGeneration, "target_snapshot_zero", "12121212-1212-1212-1212-121212121212", "13131313-1313-1313-1313-131313131313", "14141414-1414-1414-1414-141414141414", digest, graphDigest, 0)
	seedBundle(t, admin, zeroGeneration, digest, graphDigest)
	if err := r.RecordDuckLakeSnapshot(t.Context(), servingstate.ID(zeroGeneration), 1); err == nil {
		t.Fatal("state with zero persisted snapshot unexpectedly succeeded")
	}
}

func TestServingBundleArtifactCanBeReusedAcrossGenerations(t *testing.T) {
	admin, _, _ := servingDB(t)
	digest := "sha256:" + strings.Repeat("a", 64)
	graphDigest := testGraph(t).Digest()
	first := "21212121-2121-2121-2121-212121212121"
	second := "22222222-2222-2222-2222-222222222222"
	seedGeneration(t, admin, first, "target_reuse_first", "23232323-2323-2323-2323-232323232323", "24242424-2424-2424-2424-242424242424", "25252525-2525-2525-2525-252525252525", digest, graphDigest, 21)
	seedGeneration(t, admin, second, "target_reuse_second", "26262626-2626-2626-2626-262626262626", "27272727-2727-2727-2727-272727272727", "28282828-2828-2828-2828-282828282828", digest, graphDigest, 22)
	seedBundle(t, admin, first, digest, graphDigest)
	seedBundle(t, admin, second, digest, graphDigest)
	var count int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM serving_state.bundle WHERE artifact_id='artifact-'||substr($1,8)`, digest).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("content-addressed artifact generation references = %d, want 2", count)
	}
}

func TestSchemaHasNoMutableServingAuthority(t *testing.T) {
	admin, pool, _ := servingDB(t)
	var count int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='serving_state' AND table_name IN ('state','active_pointer','query_snapshot_lease')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy mutable serving tables exist: %d", count)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE serving_state.bundle SET artifact_id='tampered'`); err == nil {
		t.Fatal("runtime role mutated immutable serving evidence")
	}
}

func TestConflictingReplayCommitsWithoutWritingIncomingChildren(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "16161616-1616-1616-1616-161616161616"
	digest := "sha256:" + strings.Repeat("b", 64)
	stored := testGraph(t)
	seedGeneration(t, admin, generation, "target_demo", "17171717-1717-1717-1717-171717171717", "18181818-1818-1818-1818-181818181818", "19191919-1919-1919-1919-191919191919", digest, stored.Digest(), 19)
	input := GenerationBundleInput{GenerationID: generation, ProjectID: "project_demo", Environment: "prod", ProjectDigest: "sha256:" + strings.Repeat("c", 64), ArtifactLocator: "serving-artifacts/" + strings.Repeat("b", 64) + ".tar.gz", StorageSecurityDomain: "runtime", ArtifactContentType: "application/gzip", ArtifactMetadataDigest: "sha256:" + strings.Repeat("9", 64), Artifact: servingstate.Artifact{ID: "artifact-" + strings.Repeat("b", 64), ServingStateID: servingstate.ID(generation), Digest: digest, Format: "tar.gz", ManifestJSON: `{}`, SizeBytes: 1}, AccessPolicyJSON: `{}`, CreatedBy: "test"}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, stored); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	extra, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project_demo", Kind: projectgraph.KindProject, Name: "project"}, {ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"}, {ID: "model_extra", Kind: projectgraph.KindModel, Name: "extra"}}, []projectgraph.Edge{{From: "project_demo", To: "dashboard", Relation: "contains"}, {From: "project_demo", To: "model_extra", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE delivery.delivery_generation SET compiled_graph_digest=$2 WHERE generation_id=$1::uuid`, generation, extra.Digest()); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, extra); !errors.Is(err, ErrConflict) {
		_ = tx.Rollback(t.Context())
		t.Fatalf("conflicting graph replay err=%v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM serving_state.asset WHERE generation_id=$1::uuid AND logical_asset_id='model_extra'`, generation).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("conflicting replay wrote incoming extra child: %d", count)
	}
}

func TestAdmissionIsCallerOwnedAndRollbackable(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "55555555-5555-5555-5555-555555555555"
	digest := "sha256:" + strings.Repeat("c", 64)
	graph := testGraph(t)
	seedGeneration(t, admin, generation, "target_demo", "66666666-6666-6666-6666-666666666666", "77777777-7777-7777-7777-777777777777", "88888888-8888-8888-8888-888888888888", digest, graph.Digest(), 9)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := GenerationBundleInput{GenerationID: generation, ProjectID: "project_demo", Environment: "prod", ProjectDigest: "sha256:" + strings.Repeat("d", 64), ArtifactLocator: "serving-artifacts/" + strings.Repeat("c", 64) + ".tar.gz", StorageSecurityDomain: "runtime", ArtifactContentType: "application/gzip", ArtifactMetadataDigest: "sha256:" + strings.Repeat("9", 64), Artifact: servingstate.Artifact{ID: "artifact-" + strings.Repeat("c", 64), ServingStateID: servingstate.ID(generation), Digest: digest, Format: "tar.gz", ManifestJSON: `{}`, SizeBytes: 1}, AccessPolicyJSON: `{}`, CreatedBy: "test"}
	if _, ok := any(pool).(Tx); ok {
		t.Fatal("pool unexpectedly satisfies caller-owned transaction surface")
	}
	if _, err := AdmitGenerationBundleTx(t.Context(), tx, input, graph); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM serving_state.bundle WHERE generation_id=$1::uuid`, generation).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("caller rollback left serving bundle rows: %d", count)
	}
}

func TestRetentionGuardRequiresCallerTransaction(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "12121212-1212-1212-1212-121212121212"
	seedGeneration(t, admin, generation, "target_demo", "13131313-1313-1313-1313-131313131313", "14141414-1414-1414-1414-141414141414", "15151515-1515-1515-1515-151515151515", "sha256:"+strings.Repeat("a", 64), testGraph(t).Digest(), 17)
	if _, ok := any(pool).(Tx); ok {
		t.Fatal("pool unexpectedly satisfies retention transaction surface")
	}
}

func TestRetentionGuardSerializesRetirement(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "99999999-9999-9999-9999-999999999999"
	digest := "sha256:" + strings.Repeat("e", 64)
	seedGeneration(t, admin, generation, "target_demo", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "cccccccc-cccc-cccc-cccc-cccccccccccc", digest, testGraph(t).Digest(), 11)
	tx1, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(t.Context())
	if _, err := New(pool).CreateQuerySnapshotLeaseTx(t.Context(), tx1, servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 11, OwnerID: "reader", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	tx2, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := tx2.Exec(ctx, `UPDATE delivery.delivery_retention_root SET state='expired' WHERE generation_id=$1::uuid`, generation); err == nil {
		_ = tx2.Rollback(t.Context())
		t.Fatal("retirement did not wait on the reader retention lock")
	}
	_ = tx2.Rollback(t.Context())
	if err := tx1.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE delivery.delivery_retention_root SET state='expired' WHERE generation_id=$1::uuid`, generation); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseExtensionSerializesRetirement(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "abababab-abab-abab-abab-abababababab"
	digest := "sha256:" + strings.Repeat("f", 64)
	seedGeneration(t, admin, generation, "target_demo", "acacacac-acac-acac-acac-acacacacacac", "adadadad-adad-adad-adad-adadadadadad", "aeaeaeae-aeae-aeae-aeae-aeaeaeaeaeae", digest, testGraph(t).Digest(), 13)
	r := New(pool)
	lease, err := r.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 13, OwnerID: "reader", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	tx1, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ExtendQuerySnapshotLeaseTx(t.Context(), tx1, lease, time.Now().Add(2*time.Minute)); err != nil {
		_ = tx1.Rollback(t.Context())
		t.Fatal(err)
	}
	tx2, err := admin.Begin(t.Context())
	if err != nil {
		_ = tx1.Rollback(t.Context())
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := tx2.Exec(ctx, `UPDATE delivery.delivery_retention_root SET state='expired' WHERE generation_id=$1::uuid`, generation); err == nil {
		_ = tx2.Rollback(t.Context())
		_ = tx1.Rollback(t.Context())
		t.Fatal("retirement did not wait on extension retention lock")
	}
	_ = tx2.Rollback(t.Context())
	if err := tx1.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestReaderLeaseBindsSnapshotAndDBClock(t *testing.T) {
	admin, pool, _ := servingDB(t)
	generation := "11111111-1111-1111-1111-111111111111"
	digest := "sha256:" + strings.Repeat("a", 64)
	seedGeneration(t, admin, generation, "target_demo", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444", digest, testGraph(t).Digest(), 7)
	r := New(pool)
	if _, err := r.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 7, OwnerID: "reader", ExpiresAt: time.Now().Add(25 * time.Hour)}); err == nil {
		t.Fatal("lease exceeded the 24-hour DB-clock bound")
	}
	if _, err := r.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 7, OwnerID: "reader", ExpiresAt: time.Now().Add(-time.Minute)}); err == nil {
		t.Fatal("lease with an expired DB-clock bound succeeded")
	}
	if _, err := r.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 8, OwnerID: "reader", ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
		t.Fatal("mismatched snapshot lease succeeded")
	}
	lease, err := r.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 7, OwnerID: "reader", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseQuerySnapshotLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE delivery.delivery_retention_root SET state='expired' WHERE generation_id=$1::uuid`, generation); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateQuerySnapshotLease(t.Context(), servingstate.SnapshotLeaseInput{ServingStateID: servingstate.ID(generation), DuckLakeSnapshotID: 7, OwnerID: "reader", ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
		t.Fatal("lease acquired without a live delivery retention root")
	}
}
