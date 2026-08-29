package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func servingDB(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	runtime := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "serving-runtime", Login: true})
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
	if _, err := tx.Exec(t.Context(), `CREATE SCHEMA IF NOT EXISTS delivery; CREATE TABLE delivery.delivery_target(target_id text PRIMARY KEY,project_id text NOT NULL,environment text NOT NULL,target_revision bigint NOT NULL DEFAULT 1); CREATE TABLE delivery.delivery_snapshot_seal(seal_id uuid PRIMARY KEY,ducklake_snapshot_id bigint NOT NULL); CREATE TABLE delivery.delivery_generation(generation_id uuid PRIMARY KEY,target_id text NOT NULL REFERENCES delivery.delivery_target,snapshot_seal_id uuid NOT NULL REFERENCES delivery.delivery_snapshot_seal(seal_id),serving_artifact_digest text NOT NULL,compiled_graph_digest text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp()); CREATE TABLE delivery.delivery_active_pointer(target_id text PRIMARY KEY,generation_id uuid NOT NULL REFERENCES delivery.delivery_generation(generation_id),publication_id uuid NOT NULL); CREATE TABLE delivery.delivery_publication(publication_id uuid PRIMARY KEY,generation_id uuid NOT NULL REFERENCES delivery.delivery_generation,target_id text NOT NULL,state text NOT NULL,actor_id text NOT NULL,committed_at timestamptz); CREATE TABLE delivery.delivery_retention_root(root_id uuid PRIMARY KEY,target_id text NOT NULL,generation_id uuid,snapshot_seal_id uuid,root_kind text NOT NULL,state text NOT NULL,expires_at timestamptz);`); err != nil {
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
	return admin, p
}

func seedGeneration(t *testing.T, admin *pgxpool.Pool, generation, target, publication, attempt, seal, digest, graphDigest string, snapshot int64) {
	t.Helper()
	ctx := t.Context()
	_, err := admin.Exec(ctx, `INSERT INTO delivery.delivery_target(target_id,project_id,environment) VALUES($1,'project_demo','prod')`, target)
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

func testGraph(t *testing.T) projectgraph.ProjectGraph {
	g, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "project_demo", Kind: projectgraph.KindProject, Name: "project"}, {ID: "dashboard", Kind: projectgraph.KindDashboard, Name: "dashboard"}}, []projectgraph.Edge{{From: "project_demo", To: "dashboard", Relation: "contains"}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestAdmitGenerationBundleAndActiveRead(t *testing.T) {
	admin, pool := servingDB(t)
	generation := "11111111-1111-1111-1111-111111111111"
	digest := "sha256:" + strings.Repeat("a", 64)
	seedGeneration(t, admin, generation, "target_demo", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444", digest, testGraph(t).Digest(), 7)
	r := New(pool)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := GenerationBundleInput{GenerationID: generation, ProjectID: "project_demo", Environment: "prod", ProjectDigest: "sha256:" + strings.Repeat("b", 64), ArtifactLocator: "objects/artifact.tar.gz", Artifact: servingstate.Artifact{ID: "artifact_demo", ServingStateID: servingstate.ID(generation), Digest: digest, Format: "tar.gz", ManifestJSON: `{"name":"demo"}`}, AccessPolicyJSON: `{}`, CreatedBy: "test"}
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
	if state.ID != servingstate.ID(generation) || artifact.ID != "artifact_demo" {
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

func TestSchemaHasNoMutableServingAuthority(t *testing.T) {
	admin, pool := servingDB(t)
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
	admin, pool := servingDB(t)
	generation := "16161616-1616-1616-1616-161616161616"
	digest := "sha256:" + strings.Repeat("b", 64)
	stored := testGraph(t)
	seedGeneration(t, admin, generation, "target_demo", "17171717-1717-1717-1717-171717171717", "18181818-1818-1818-1818-181818181818", "19191919-1919-1919-1919-191919191919", digest, stored.Digest(), 19)
	input := GenerationBundleInput{GenerationID: generation, ProjectID: "project_demo", Environment: "prod", ProjectDigest: "sha256:" + strings.Repeat("c", 64), ArtifactLocator: "objects/conflict.tar.gz", Artifact: servingstate.Artifact{ID: "artifact_conflict", ServingStateID: servingstate.ID(generation), Digest: digest, Format: "tar.gz", ManifestJSON: `{}`}, AccessPolicyJSON: `{}`, CreatedBy: "test"}
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
	admin, pool := servingDB(t)
	generation := "55555555-5555-5555-5555-555555555555"
	digest := "sha256:" + strings.Repeat("c", 64)
	graph := testGraph(t)
	seedGeneration(t, admin, generation, "target_demo", "66666666-6666-6666-6666-666666666666", "77777777-7777-7777-7777-777777777777", "88888888-8888-8888-8888-888888888888", digest, graph.Digest(), 9)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := GenerationBundleInput{GenerationID: generation, ProjectID: "project_demo", Environment: "prod", ProjectDigest: "sha256:" + strings.Repeat("d", 64), ArtifactLocator: "objects/artifact.tar.gz", Artifact: servingstate.Artifact{ID: "artifact_rollback", ServingStateID: servingstate.ID(generation), Digest: digest, Format: "tar.gz", ManifestJSON: `{}`}, AccessPolicyJSON: `{}`, CreatedBy: "test"}
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
	admin, pool := servingDB(t)
	generation := "12121212-1212-1212-1212-121212121212"
	seedGeneration(t, admin, generation, "target_demo", "13131313-1313-1313-1313-131313131313", "14141414-1414-1414-1414-141414141414", "15151515-1515-1515-1515-151515151515", "sha256:"+strings.Repeat("a", 64), testGraph(t).Digest(), 17)
	if _, ok := any(pool).(Tx); ok {
		t.Fatal("pool unexpectedly satisfies retention transaction surface")
	}
}

func TestRetentionGuardSerializesRetirement(t *testing.T) {
	admin, pool := servingDB(t)
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
	admin, pool := servingDB(t)
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
	admin, pool := servingDB(t)
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
