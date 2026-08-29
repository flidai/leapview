package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgres18UpgradeAuthorityLifecycleAndRuntimeGate(t *testing.T) {
	h := postgrestest.Start(t)
	migratorRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_migrator", Password: "migrator-secret", Login: true})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	db := h.NewDatabase(t, "ducklake_upgrade_authority_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminRepo := New(admin)
	const poolID, catalogID = "upgrade-pool", "upgrade-catalog"
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "catalog-v1"}); err != nil {
		t.Fatal(err)
	}
	migratorDB, err := pgxpool.New(t.Context(), db.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migratorDB.Close)
	migrator := New(migratorDB)
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	runtime := New(runtimeDB)
	current := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.5", DuckLakeExtension: "ducklake:0.3", CatalogFormat: "ducklake:v1"}, CompatibilityDigest: digest('a'), CatalogSchemaVersion: "catalog-v1"}
	target := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.6", DuckLakeExtension: "ducklake:0.4", CatalogFormat: "ducklake:v2"}, CompatibilityDigest: digest('b'), CatalogSchemaVersion: "catalog-v2"}
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}
	if err := ensureSnapshotLive(t.Context(), admin, ref); err != nil {
		t.Fatal(err)
	}
	globalFence, err := migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "migration-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "migration-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.RegisterCatalogRuntimeCompatibility(t.Context(), CatalogRuntimeCompatibility{PhysicalPoolID: poolID, CatalogID: catalogID, RuntimeCompatibility: current, GlobalFence: globalFence, PoolFence: fence}); err != nil {
		t.Fatal(err)
	}
	migrationID := "0198f2c0-7c7a-7f00-8a11-000000000201"
	beginEvidence := []byte(`{"drain_verified":true,"backup_verified":true}`)
	if _, err := migrator.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: "0198f2c0-7c7a-7f00-8a11-000000000299", PhysicalPoolID: poolID, CatalogID: catalogID, PoolFence: fence, Current: current, Target: target, Evidence: beginEvidence}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pool-only migration authority err=%v", err)
	}
	migration, err := migrator.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: migrationID, PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: globalFence, PoolFence: fence, Current: current, Target: target, Evidence: beginEvidence})
	if err != nil {
		t.Fatal(err)
	}
	if migration.State != "running" || !sameRuntimeCompatibility(migration.Target, target) || !evidenceEqual(migration.BeginEvidence, string(beginEvidence)) {
		t.Fatalf("migration=%#v", migration)
	}
	if _, err := runtime.CheckRuntimeAttachEligibility(t.Context(), RuntimeAttachInput{PhysicalPoolID: poolID, CatalogID: catalogID, Compatibility: current}); !errors.Is(err, ErrRuntimeAttachIneligible) {
		t.Fatalf("runtime attach during migration err=%v", err)
	}
	qualification, err := migrator.RequalifySnapshot(t.Context(), RequalifySnapshotInput{QualificationID: "0198f2c0-7c7a-7f00-8a11-000000000202", PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: ref.SnapshotID, MigrationID: migrationID, GlobalFence: globalFence, PoolFence: fence, Compatibility: target, Evidence: []byte(`{"snapshot":41,"verified":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if qualification.Status != "qualified" || !sameRuntimeCompatibility(qualification.RuntimeCompatibility, target) {
		t.Fatalf("qualification=%#v", qualification)
	}
	completed, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: migrationID, GlobalFence: globalFence, PoolFence: fence, Evidence: []byte(`{"catalog":"migrated"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || !evidenceEqual(completed.CompletionEvidence, `{"catalog":"migrated"}`) {
		t.Fatalf("completed=%#v", completed)
	}
	if replay, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: migrationID, GlobalFence: globalFence, PoolFence: fence, Evidence: []byte(`{"catalog":"migrated"}`)}); err != nil || replay.State != "completed" {
		t.Fatalf("completion replay=%#v err=%v", replay, err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), globalFence); err != nil {
		t.Fatal(err)
	}
	eligible, err := runtime.CheckRuntimeAttachEligibility(t.Context(), RuntimeAttachInput{PhysicalPoolID: poolID, CatalogID: catalogID, Compatibility: target})
	if err != nil || !eligible.Eligible {
		t.Fatalf("eligible=%#v err=%v", eligible, err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `INSERT INTO ducklake.catalog_migration(migration_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,current_duckdb_runtime,current_ducklake_extension,current_catalog_format,current_compatibility_digest,current_catalog_schema_version,target_duckdb_runtime,target_ducklake_extension,target_catalog_format,target_compatibility_digest,target_catalog_schema_version,state,started_at) VALUES ('0198f2c0-7c7a-7f00-8a11-000000000203',$1,$2,'runtime',1,'d','l','f',$3,'v1','d2','l2','f2',$4,'v2','running',clock_timestamp())`, poolID, catalogID, digest('a'), digest('b')); err == nil {
		t.Fatal("runtime role obtained catalog migration write authority")
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE ducklake.catalog_runtime_compatibility SET catalog_format='tampered' WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("runtime role obtained compatibility mutation authority")
	}
	if _, err := migratorDB.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET protected_until=clock_timestamp() WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, ref.SnapshotID); err == nil {
		t.Fatal("migrator role obtained direct retention mutation authority")
	}
	if _, err := migratorDB.Exec(t.Context(), `UPDATE ducklake.catalog_identity SET catalog_id='tampered' WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("migrator role obtained direct catalog identity mutation authority")
	}

	// A failed migration retains an explicit rollback/forward-recovery decision
	// and cannot be rewritten or reopened by a later lifecycle call.
	globalFence2, err := migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "migration-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	fence2, err := migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "migration-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	failureID := "0198f2c0-7c7a-7f00-8a11-000000000204"
	if _, err := migrator.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: failureID, PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: globalFence2, PoolFence: fence2, Current: target, Target: current, Evidence: beginEvidence}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.RequalifySnapshot(t.Context(), RequalifySnapshotInput{QualificationID: "0198f2c0-7c7a-7f00-8a11-000000000205", PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: ref.SnapshotID, MigrationID: failureID, GlobalFence: globalFence2, PoolFence: fence2, Compatibility: current, Status: "rejected", Evidence: []byte(`{"snapshot":41,"reason":"verification_failed"}`)}); err != nil {
		t.Fatal("rejected qualification: ", err)
	}
	if _, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: failureID, GlobalFence: globalFence2, PoolFence: fence2, Evidence: []byte(`{"catalog":"must-not-open"}`)}); !errors.Is(err, ErrQualificationMissing) {
		t.Fatalf("completion with rejected qualification err=%v", err)
	}
	failed, err := migrator.FailCatalogMigration(t.Context(), FailCatalogMigrationInput{MigrationID: failureID, GlobalFence: globalFence2, PoolFence: fence2, Evidence: []byte(`{"error":"checksum"}`), RecoveryDecision: "rollback", DecisionEvidence: []byte(`{"operator":"approved"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "failed" || failed.RecoveryDecision != "rollback" || !evidenceEqual(failed.DecisionEvidence, `{"operator":"approved"}`) {
		t.Fatalf("failed=%#v", failed)
	}
	if replay, err := migrator.FailCatalogMigration(t.Context(), FailCatalogMigrationInput{MigrationID: failureID, GlobalFence: globalFence2, PoolFence: fence2, Evidence: []byte(`{"error":"checksum"}`), RecoveryDecision: "rollback", DecisionEvidence: []byte(`{"operator":"approved"}`)}); err != nil || replay.State != "failed" {
		t.Fatalf("failure replay=%#v err=%v", replay, err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), fence2); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), globalFence2); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres18MigrationFenceConcurrencyAndGlobalExclusion(t *testing.T) {
	h := postgrestest.Start(t)
	migratorRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_migrator", Password: "migrator-secret", Login: true})
	db := h.NewDatabase(t, "ducklake_upgrade_fence_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminRepo := New(admin)
	const poolID, catalogID = "concurrent-pool", "concurrent-catalog"
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	firstDB, err := pgxpool.New(t.Context(), db.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(firstDB.Close)
	secondDB, err := pgxpool.New(t.Context(), db.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondDB.Close)
	first, second := New(firstDB), New(secondDB)
	start := make(chan struct{})
	type result struct {
		fence MigrationFence
		err   error
	}
	results := make(chan result, 2)
	go func() {
		<-start
		f, e := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: "concurrent-pool", OwnerID: "worker-a", LeaseExpiresAt: time.Now().Add(time.Minute)})
		results <- result{f, e}
	}()
	go func() {
		<-start
		f, e := second.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: "concurrent-pool", OwnerID: "worker-b", LeaseExpiresAt: time.Now().Add(time.Minute)})
		results <- result{f, e}
	}()
	close(start)
	var winner MigrationFence
	for range 2 {
		got := <-results
		if got.err == nil {
			if winner.FencingEpoch != 0 {
				t.Fatal("two workers acquired one pool fence")
			}
			winner = got.fence
		} else if !errors.Is(got.err, ErrMigrationBusy) {
			t.Fatalf("losing fence claim err=%v", got.err)
		}
	}
	if winner.FencingEpoch <= 0 {
		t.Fatal("no pool fence winner")
	}
	if err := first.ReleaseMigrationFence(t.Context(), winner); err != nil {
		t.Fatal(err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), winner); err != nil {
		t.Fatal("release replay: ", err)
	}
	if err := first.RenewMigrationFence(t.Context(), winner, time.Now().Add(time.Minute)); !errors.Is(err, ErrMigrationFenceExpired) && !errors.Is(err, ErrStaleFence) {
		t.Fatalf("renew released fence err=%v", err)
	}
	short, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: "concurrent-pool", OwnerID: "worker-short", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RenewMigrationFence(t.Context(), short, time.Now().Add(time.Minute)); err != nil {
		t.Fatal("renew active fence: ", err)
	}
	if err := first.RenewMigrationFence(t.Context(), short, time.Now().Add(time.Minute)); err != nil {
		t.Fatal("renew replay: ", err)
	}
	if err := first.RenewMigrationFence(t.Context(), MigrationFence{Scope: MigrationFencePool, PhysicalPoolID: short.PhysicalPoolID, OwnerID: "stale-worker", FencingEpoch: short.FencingEpoch}, time.Now().Add(time.Minute)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale renewal err=%v", err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), short); err != nil {
		t.Fatal(err)
	}
	expiring, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: "concurrent-pool", OwnerID: "worker-expiring", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE ducklake.migration_fence SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE scope='pool' AND physical_pool_id=$1 AND owner_id=$2 AND fencing_epoch=$3`, expiring.PhysicalPoolID, expiring.OwnerID, expiring.FencingEpoch); err != nil {
		t.Fatal(err)
	}
	if err := first.RenewMigrationFence(t.Context(), expiring, time.Now().Add(time.Minute)); !errors.Is(err, ErrMigrationFenceExpired) {
		t.Fatalf("expired renewal err=%v", err)
	}
	successor, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: "concurrent-pool", OwnerID: "worker-successor", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || successor.FencingEpoch <= expiring.FencingEpoch {
		t.Fatalf("successor epoch=%#v err=%v", successor, err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), successor); err != nil {
		t.Fatal(err)
	}
	current := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1", DuckLakeExtension: "ducklake:1", CatalogFormat: "ducklake:v1"}, CompatibilityDigest: digest('a'), CatalogSchemaVersion: "v1"}
	target := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:2", DuckLakeExtension: "ducklake:2", CatalogFormat: "ducklake:v2"}, CompatibilityDigest: digest('b'), CatalogSchemaVersion: "v2"}
	globalInterrupted, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "worker-interrupted", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	poolInterrupted, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "worker-interrupted", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.RegisterCatalogRuntimeCompatibility(t.Context(), CatalogRuntimeCompatibility{PhysicalPoolID: poolID, CatalogID: catalogID, RuntimeCompatibility: current, GlobalFence: globalInterrupted, PoolFence: poolInterrupted}); err != nil {
		t.Fatal(err)
	}
	interruptedID := "0198f2c0-7c7a-7f00-8a11-000000000206"
	if _, err := first.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: interruptedID, PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: globalInterrupted, PoolFence: poolInterrupted, Current: current, Target: target, Evidence: []byte(`{"drain_verified":true,"backup_verified":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE ducklake.migration_fence SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE (scope='global' AND physical_pool_id='') OR (scope='pool' AND physical_pool_id=$1)`, poolID); err != nil {
		t.Fatal(err)
	}
	globalSuccessor, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "worker-recovery", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	poolSuccessor, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "worker-recovery", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: interruptedID, GlobalFence: globalInterrupted, PoolFence: poolInterrupted, Evidence: []byte(`{"must_not":"complete"}`)}); !errors.Is(err, ErrMigrationFenceExpired) && !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expired interrupted completion err=%v", err)
	}
	if recovered, err := first.FailCatalogMigration(t.Context(), FailCatalogMigrationInput{MigrationID: interruptedID, GlobalFence: globalSuccessor, PoolFence: poolSuccessor, Evidence: []byte(`{"interrupted":true}`), RecoveryDecision: "forward_recovery", DecisionEvidence: []byte(`{"successor":"admitted"}`)}); err != nil || recovered.State != "failed" || recovered.RecoveryDecision != "forward_recovery" {
		t.Fatalf("forward recovery=%#v err=%v", recovered, err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), poolSuccessor); err != nil {
		t.Fatal(err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), globalSuccessor); err != nil {
		t.Fatal(err)
	}
	// Repeat takeover with an explicit rollback decision as well as the
	// forward-recovery path above.
	globalRollback, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "worker-rollback", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	poolRollback, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "worker-rollback", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	rollbackID := "0198f2c0-7c7a-7f00-8a11-000000000207"
	if _, err := first.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: rollbackID, PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: globalRollback, PoolFence: poolRollback, Current: current, Target: target, Evidence: []byte(`{"drain_verified":true,"backup_verified":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `UPDATE ducklake.migration_fence SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE (scope='global' AND physical_pool_id='') OR (scope='pool' AND physical_pool_id=$1)`, poolID); err != nil {
		t.Fatal(err)
	}
	globalRollbackSuccessor, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "worker-rollback-successor", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	poolRollbackSuccessor, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "worker-rollback-successor", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := first.FailCatalogMigration(t.Context(), FailCatalogMigrationInput{MigrationID: rollbackID, GlobalFence: globalRollbackSuccessor, PoolFence: poolRollbackSuccessor, Evidence: []byte(`{"interrupted":true}`), RecoveryDecision: "rollback", DecisionEvidence: []byte(`{"restored":true}`)}); err != nil || recovered.State != "failed" || recovered.RecoveryDecision != "rollback" {
		t.Fatalf("rollback recovery=%#v err=%v", recovered, err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), poolRollbackSuccessor); err != nil {
		t.Fatal(err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), globalRollbackSuccessor); err != nil {
		t.Fatal(err)
	}
	global, err := first.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "global-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: "concurrent-pool", OwnerID: "worker-c", LeaseExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrMigrationBusy) {
		t.Fatalf("pool claim while global fence held err=%v", err)
	}
	if err := first.ReleaseMigrationFence(t.Context(), global); err != nil {
		t.Fatal(err)
	}
}

// Test the database capability boundary independently of the repository's
// happy path. The migrator role may invoke guarded functions, but cannot
// mutate authority rows directly or fabricate qualification evidence.
func TestPostgres18UpgradeAuthorityAdversarialSQLAndTupleCycle(t *testing.T) {
	h := postgrestest.Start(t)
	migratorRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_ducklake_migrator", Password: "migrator-secret", Login: true})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	db := h.NewDatabase(t, "ducklake_upgrade_adversarial_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminRepo := New(admin)
	const poolID, catalogID = "adversarial-pool", "adversarial-catalog"
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "v1"}); err != nil {
		t.Fatal(err)
	}
	migratorDB, err := pgxpool.New(t.Context(), db.URL(migratorRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(migratorDB.Close)
	migrator := New(migratorDB)
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	runtime := New(runtimeDB)
	a := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:a", DuckLakeExtension: "ducklake:a", CatalogFormat: "format:a"}, CompatibilityDigest: digest('a'), CatalogSchemaVersion: "v1"}
	b := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:b", DuckLakeExtension: "ducklake:b", CatalogFormat: "format:b"}, CompatibilityDigest: digest('b'), CatalogSchemaVersion: "v2"}
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 91}
	if err := ensureSnapshotLive(t.Context(), admin, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.Exec(t.Context(), `SELECT ducklake.register_catalog_runtime_compatibility($1,$2,$3,$4,$5,$6,$7,'unfenced',0,0)`, poolID, catalogID, a.DuckDBRuntime, a.DuckLakeExtension, a.CatalogFormat, a.CompatibilityDigest, a.CatalogSchemaVersion); err == nil {
		t.Fatal("unfenced compatibility registration unexpectedly succeeded")
	}
	global, err := migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "guarded-worker", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "guarded-worker", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.RegisterCatalogRuntimeCompatibility(t.Context(), CatalogRuntimeCompatibility{PhysicalPoolID: poolID, CatalogID: catalogID, RuntimeCompatibility: a, GlobalFence: global, PoolFence: pool}); err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.Exec(t.Context(), `SELECT ducklake.register_catalog_runtime_compatibility($1,$2,$3,$4,$5,$6,$7,'forged-owner',$8,$9)`, poolID, catalogID, a.DuckDBRuntime, a.DuckLakeExtension, a.CatalogFormat, a.CompatibilityDigest, a.CatalogSchemaVersion, pool.FencingEpoch, global.FencingEpoch); err == nil {
		t.Fatal("wrong-owner compatibility registration unexpectedly succeeded")
	}
	t.Cleanup(func() {
		_ = migrator.ReleaseMigrationFence(t.Context(), pool)
		_ = migrator.ReleaseMigrationFence(t.Context(), global)
	})
	if _, err := migratorDB.Exec(t.Context(), `UPDATE ducklake.migration_fence SET fencing_epoch=fencing_epoch+1 WHERE scope='global' AND physical_pool_id=''`); err == nil {
		t.Fatal("migrator directly tampered with global fence epoch")
	}
	if _, err := migratorDB.Exec(t.Context(), `UPDATE ducklake.catalog_runtime_compatibility SET catalog_format='forged' WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("migrator directly tampered with compatibility")
	}
	if _, err := migratorDB.Exec(t.Context(), `INSERT INTO ducklake.catalog_migration(migration_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,global_fencing_epoch,current_duckdb_runtime,current_ducklake_extension,current_catalog_format,current_compatibility_digest,current_catalog_schema_version,target_duckdb_runtime,target_ducklake_extension,target_catalog_format,target_compatibility_digest,target_catalog_schema_version,state,started_at,begin_evidence) VALUES ('0198f2c0-7c7a-7f00-8a11-000000000301',$1,$2,'forged',1,1,'a','a','a',$3,'v1','b','b','b',$4,'v2','running',clock_timestamp(),'{}')`, poolID, catalogID, digest('a'), digest('b')); err == nil {
		t.Fatal("migrator directly inserted an unfenced migration")
	}
	if _, err := migratorDB.Exec(t.Context(), `SELECT ducklake.record_snapshot_requalification('0198f2c0-7c7a-7f00-8a11-000000000302',$1,$2,$3,'0198f2c0-7c7a-7f00-8a11-000000000303',$4,$5,$6,$7,$8,'qualified','{"forged":true}',clock_timestamp(),'forged-owner',$9,$10)`, poolID, catalogID, ref.SnapshotID, b.DuckDBRuntime, b.DuckLakeExtension, b.CatalogFormat, b.CompatibilityDigest, b.CatalogSchemaVersion, pool.FencingEpoch, global.FencingEpoch); err == nil {
		t.Fatal("migrator fabricated qualification evidence without an active migration")
	}
	m1, err := migrator.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: "0198f2c0-7c7a-7f00-8a11-000000000303", PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: global, PoolFence: pool, Current: a, Target: b, Evidence: []byte(`{"drain_verified":true,"backup_verified":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.Exec(t.Context(), `SELECT ducklake.begin_catalog_migration($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb)`, m1.MigrationID, poolID, catalogID, pool.OwnerID, pool.FencingEpoch, global.FencingEpoch, a.DuckDBRuntime, a.DuckLakeExtension, a.CatalogFormat, a.CompatibilityDigest, a.CatalogSchemaVersion, "duckdb:conflict", b.DuckLakeExtension, b.CatalogFormat, b.CompatibilityDigest, b.CatalogSchemaVersion, `{"drain_verified":true,"backup_verified":true}`); err == nil {
		t.Fatal("conflicting migration replay unexpectedly succeeded")
	}
	if _, err := runtime.CheckRuntimeAttachEligibility(t.Context(), RuntimeAttachInput{PhysicalPoolID: poolID, CatalogID: catalogID, Compatibility: RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:wrong", DuckLakeExtension: "ducklake:b", CatalogFormat: "format:b"}, CompatibilityDigest: b.CompatibilityDigest, CatalogSchemaVersion: b.CatalogSchemaVersion}}); !errors.Is(err, ErrRuntimeAttachIneligible) {
		t.Fatalf("incompatible runtime attach err=%v", err)
	}
	if _, err := migrator.RequalifySnapshot(t.Context(), RequalifySnapshotInput{QualificationID: "0198f2c0-7c7a-7f00-8a11-000000000304", PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: ref.SnapshotID, MigrationID: m1.MigrationID, GlobalFence: global, PoolFence: pool, Compatibility: b, Status: "rejected", Evidence: []byte(`{"reason":"seal-mismatch"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: m1.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: []byte(`{"complete":true}`)}); !errors.Is(err, ErrQualificationMissing) {
		t.Fatalf("rejected qualification completion err=%v", err)
	}
	if _, err := migrator.FailCatalogMigration(t.Context(), FailCatalogMigrationInput{MigrationID: m1.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: []byte(`{"rollback":true}`), RecoveryDecision: "rollback", DecisionEvidence: []byte(`{"approved":true}`)}); err != nil {
		t.Fatal(err)
	}
	// A fresh migration can move A -> B, then B -> A. The old B
	// qualification is not accepted as evidence for the new migration ID.
	if err := migrator.ReleaseMigrationFence(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), global); err != nil {
		t.Fatal(err)
	}
	global, err = migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "cycle-worker", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err = migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "cycle-worker", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := migrator.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: "0198f2c0-7c7a-7f00-8a11-000000000305", PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: global, PoolFence: pool, Current: a, Target: b, Evidence: []byte(`{"drain_verified":true,"backup_verified":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.RequalifySnapshot(t.Context(), RequalifySnapshotInput{QualificationID: "0198f2c0-7c7a-7f00-8a11-000000000306", PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: ref.SnapshotID, MigrationID: m2.MigrationID, GlobalFence: global, PoolFence: pool, Compatibility: b, Evidence: []byte(`{"seal":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: m2.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: []byte(`{"complete":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), global); err != nil {
		t.Fatal(err)
	}
	global, err = migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFenceGlobal, OwnerID: "cycle-worker-2", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err = migrator.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "cycle-worker-2", LeaseExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	m3, err := migrator.BeginCatalogMigration(t.Context(), BeginCatalogMigrationInput{MigrationID: "0198f2c0-7c7a-7f00-8a11-000000000307", PhysicalPoolID: poolID, CatalogID: catalogID, GlobalFence: global, PoolFence: pool, Current: b, Target: a, Evidence: []byte(`{"drain_verified":true,"backup_verified":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: m3.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: []byte(`{"complete":true}`)}); !errors.Is(err, ErrQualificationMissing) {
		t.Fatalf("stale A->B qualification accepted for B->A completion err=%v", err)
	}
	if _, err := migrator.RequalifySnapshot(t.Context(), RequalifySnapshotInput{QualificationID: "0198f2c0-7c7a-7f00-8a11-000000000308", PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: ref.SnapshotID, MigrationID: m3.MigrationID, GlobalFence: global, PoolFence: pool, Compatibility: a, Evidence: []byte(`{"seal":true}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := migrator.CompleteCatalogMigration(t.Context(), CompleteCatalogMigrationInput{MigrationID: m3.MigrationID, GlobalFence: global, PoolFence: pool, Evidence: []byte(`{"complete":true}`)}); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := migrator.ReleaseMigrationFence(t.Context(), global); err != nil {
		t.Fatal(err)
	}
	eligible, err := runtime.CheckRuntimeAttachEligibility(t.Context(), RuntimeAttachInput{PhysicalPoolID: poolID, CatalogID: catalogID, Compatibility: a})
	if err != nil || !eligible.Eligible {
		t.Fatalf("A->B->A runtime eligibility=%#v err=%v", eligible, err)
	}
}
