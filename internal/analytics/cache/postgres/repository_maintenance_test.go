package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNamespaceRevisionEpochInvalidationAndReconciliation(t *testing.T) {
	p := cacheTestDB(t)
	defer p.Close()
	repo := New(p)
	ns := Namespace{PartitionKind: PartitionProduction, TargetID: "target_sales", ProjectID: "project_sales", Environment: "prod"}
	if got, err := repo.CurrentEpoch(t.Context(), ns); err != nil || got != 1 {
		t.Fatalf("initial epoch = %d, %v; want 1", got, err)
	}
	digestA, digestB := cacheTestDigest('a'), cacheTestDigest('b')
	first, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestA})
	if err != nil || first.Revision != 1 {
		t.Fatalf("initial dependency revision = %#v, %v", first, err)
	}
	initialReplay, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestA, ExpectedRevision: 0})
	if err != nil || initialReplay.Revision != first.Revision {
		t.Fatalf("initial dependency revision replay = %#v, %v", initialReplay, err)
	}
	if _, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestA, ExpectedRevision: 99}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-digest wrong expected revision = %v, want conflict", err)
	}
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	fence, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: ns, CacheKey: cacheKey, OwnerID: "revision-owner", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cacheTestPublish(t, repo, t.Context(), PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: cacheTestL3ObjectKey(cacheTestDigest('d'), 'a', 'e'), ByteSize: 1, Lease: fence}); err != nil {
		t.Fatal(err)
	}
	second, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestB, ExpectedRevision: 1, Evidence: cacheTestEvidence("source-refresh")})
	if err != nil || second.Revision != 2 || second.RevisionDigest != digestB {
		t.Fatalf("changed dependency revision = %#v, %v", second, err)
	}
	replay, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestB, ExpectedRevision: 1, Evidence: cacheTestEvidence("source-refresh")})
	if err != nil || replay.Revision != second.Revision || replay.RevisionDigest != second.RevisionDigest {
		t.Fatalf("dependency revision lost-ACK replay = %#v, %v", replay, err)
	}
	if _, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: digestB, ExpectedRevision: 1, IdempotencyKey: "different-retry", Evidence: cacheTestEvidence("source-refresh")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("different dependency revision retry = %v, want conflict", err)
	}
	if got, err := repo.CurrentEpoch(t.Context(), ns); err != nil || got != 2 {
		t.Fatalf("changed epoch = %d, %v; want 2", got, err)
	}
	if _, found, err := repo.Lookup(t.Context(), key); err != nil || found {
		t.Fatalf("old dependency manifest found=%v err=%v", found, err)
	}
	events, err := repo.ReconcileInvalidations(t.Context(), ReconcileOptions{AfterEventID: 0, Limit: 10})
	if err != nil || len(events) != 1 || events[0].NamespaceEpoch != 2 || events[0].DependencyID != "orders" {
		t.Fatalf("reconciled invalidations = %#v, %v", events, err)
	}
	if _, err := repo.RecordDependencyRevision(t.Context(), DependencyRevisionInput{Namespace: ns, Kind: DependencySource, DependencyID: "orders", RevisionDigest: cacheTestDigest('c'), ExpectedRevision: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision update = %v, want conflict", err)
	}
	staleKey := cacheTestKey()
	staleKey.CanonicalQueryDigest = cacheTestDigest('9')
	staleDigest, err := staleKey.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	stale, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: ns, CacheKey: staleDigest, OwnerID: "stale-owner", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InvalidateNamespace(t.Context(), NamespaceInvalidationInput{Namespace: ns, Kind: DependencyCustom, DependencyID: "stale-fence", IdempotencyKey: "stale-fence-epoch", Reason: "stale fence"}, cacheTestEvidence("stale-fence")); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheTestPublish(t, repo, t.Context(), PublishInput{Key: staleKey, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: cacheTestL3ObjectKey(cacheTestDigest('d'), 'a', 'e'), ByteSize: 1, Lease: stale}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale namespace publish = %v, want stale fence", err)
	}
	hint, err := ParseNotificationHint(`{"event_id":1,"namespace":"` + ns.Key() + `"}`)
	if err != nil || hint.EventID != 1 || hint.NamespaceKey != ns.Key() {
		t.Fatalf("notification hint = %#v, %v", hint, err)
	}
}

func TestL3ObjectAndScanFencesAreDurableAndFenced(t *testing.T) {
	db := cacheTestDB(t)
	repo := New(db)
	maintenance := NewMaintenance(db)
	domain := cacheTestDigest('d')
	objectKey := "cache/l3/sd/" + domain + "/" + cacheTestDigest('a') + "/" + cacheTestDigest('b')

	first, err := repo.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: domain, ObjectKey: objectKey, OwnerID: "runtime-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: domain, ObjectKey: objectKey, OwnerID: "gc-a", Lease: time.Minute}); !errors.Is(err, ErrBusy) {
		t.Fatalf("maintenance acquired live runtime fence: %v", err)
	}
	if err := repo.ReleaseL3ObjectFence(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second, err := maintenance.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: domain, ObjectKey: objectKey, OwnerID: "gc-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if second.FencingEpoch <= first.FencingEpoch {
		t.Fatalf("object fencing epoch did not advance: first=%d second=%d", first.FencingEpoch, second.FencingEpoch)
	}
	if err := repo.RenewL3ObjectFence(t.Context(), first, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale object fence renewed: %v", err)
	}
	if err := maintenance.ReleaseL3ObjectFence(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.PrepareL3ObjectGC(t.Context(), second); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("GC preparation with released object fence error = %v, want stale fence", err)
	}

	lease, err := maintenance.AcquireL3GCLease(t.Context(), domain, "node-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if lease.CursorObjectKey != "" || lease.Cycle != 1 {
		t.Fatalf("initial GC lease = %+v", lease)
	}
	if _, err := maintenance.AcquireL3GCLease(t.Context(), domain, "node-b", time.Minute); !errors.Is(err, ErrBusy) {
		t.Fatalf("second GC owner acquired live lease: %v", err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, objectKey, false); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, objectKey, false); err != nil {
		t.Fatalf("idempotent GC cursor replay failed: %v", err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, objectKey[:len(objectKey)-1], false); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("regressing GC cursor error = %v, want stale fence", err)
	}
	if err := maintenance.AdvanceL3GCCursor(t.Context(), lease, "", true); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ReleaseL3GCLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	next, err := maintenance.AcquireL3GCLease(t.Context(), domain, "node-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.CursorObjectKey != "" || next.Cycle != 2 || next.FencingEpoch <= lease.FencingEpoch {
		t.Fatalf("resumed GC lease = %+v", next)
	}
}

func TestPruneReclaimsExpiredL3ObjectFences(t *testing.T) {
	db := cacheTestDB(t)
	maintenance := NewMaintenance(db)
	domain := cacheTestDigest('d')
	objectKey := cacheTestL3ObjectKey(cacheTestDigest('d'), 'a', 'e')
	fence, err := maintenance.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{
		StorageSecurityDomain: domain,
		ObjectKey:             objectKey,
		OwnerID:               "prune-owner",
		Lease:                 time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ReleaseL3ObjectFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	stats, err := maintenance.Prune(t.Context(), PruneOptions{Before: time.Now().Add(time.Minute), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ExpiredLeases != 1 {
		t.Fatalf("pruned expired L3 object fences = %d, want 1", stats.ExpiredLeases)
	}
}

func TestManifestAdmissionRequiresCurrentExactObjectFence(t *testing.T) {
	db := cacheTestDB(t)
	repo := New(db)
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(t.Context(), AcquireFillInput{Namespace: cacheTestNamespace(), CacheKey: cacheKey, OwnerID: "publisher", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	objectKey := "cache/l3/sd/" + cacheTestDigest('d') + "/" + cacheTestDigest('a') + "/" + cacheTestDigest('b')
	fence, err := repo.AcquireL3ObjectFence(t.Context(), AcquireL3ObjectFenceInput{StorageSecurityDomain: cacheTestDigest('d'), ObjectKey: objectKey, OwnerID: lease.OwnerID, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseL3ObjectFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	_, err = repo.Publish(t.Context(), PublishInput{Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: objectKey, ByteSize: 1, Lease: lease, ObjectFence: fence})
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("publish with released object fence error = %v, want stale fence", err)
	}
}

func TestCacheRoleConformance(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "maintenance-secret", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	database := h.NewDatabase(t, "cache_roles")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
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
	// Simulate a developer database prepared with the pre-object-fence
	// overload. Reapplying the clean-slate schema must remove both the function
	// and its role-specific grant rather than leaving a callable bypass.
	const staleAdmissionSignature = `cache.admit_manifest(uuid,uuid,text,text,bigint,text,bigint,text,text,text,text,text,bigint,text,text,text,bigint,text,text,text,bigint,jsonb,uuid,timestamptz)`
	if _, err := admin.Exec(t.Context(), `CREATE FUNCTION `+staleAdmissionSignature+` RETURNS uuid LANGUAGE sql AS 'SELECT NULL::uuid'; GRANT EXECUTE ON FUNCTION `+staleAdmissionSignature+` TO leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	tx, err = admin.Begin(t.Context())
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
	var staleAdmissionExists bool
	if err := admin.QueryRow(t.Context(), `SELECT to_regprocedure($1) IS NOT NULL`, staleAdmissionSignature).Scan(&staleAdmissionExists); err != nil {
		t.Fatal(err)
	}
	if staleAdmissionExists {
		t.Fatal("pre-object-fence manifest admission overload survived schema reapplication")
	}
	var runtimeSchema, runtimeDelete, runtimePrune, runtimeInvalidate, runtimeRetire, runtimeObjectFence, runtimeGCLease, readonlyInsert, maintenancePrune, maintenanceObjectFence, maintenanceGCLease, maintenanceReachability, maintenanceStateDML bool
	if err := admin.QueryRow(t.Context(), `SELECT has_schema_privilege($1,'cache','USAGE'),has_table_privilege($1,'cache.cache_invalidation','DELETE'),has_function_privilege($1,'cache.prune_coordination(timestamptz,integer)','EXECUTE'),has_function_privilege($1,'cache.invalidate_namespace(uuid,text,text,text,text,bigint,text,text,jsonb)','EXECUTE'),has_function_privilege($1,'cache.retire_manifest(uuid,jsonb)','EXECUTE'),has_function_privilege($1,'cache.acquire_l3_object_fence(uuid,text,text,text,interval)','EXECUTE'),has_function_privilege($1,'cache.acquire_l3_gc_lease(uuid,text,text,interval)','EXECUTE'),has_table_privilege($2,'cache.cache_namespace_epoch','INSERT'),has_function_privilege($3,'cache.prune_coordination(timestamptz,integer)','EXECUTE'),has_function_privilege($3,'cache.acquire_l3_object_fence(uuid,text,text,text,interval)','EXECUTE'),has_function_privilege($3,'cache.acquire_l3_gc_lease(uuid,text,text,interval)','EXECUTE'),has_function_privilege($3,'cache.prepare_l3_object_gc(uuid,text,text,text,bigint)','EXECUTE'),has_table_privilege($3,'cache.cache_l3_gc_state','UPDATE')`, runtimeRole.Name, readonlyRole.Name, maintenanceRole.Name).Scan(&runtimeSchema, &runtimeDelete, &runtimePrune, &runtimeInvalidate, &runtimeRetire, &runtimeObjectFence, &runtimeGCLease, &readonlyInsert, &maintenancePrune, &maintenanceObjectFence, &maintenanceGCLease, &maintenanceReachability, &maintenanceStateDML); err != nil {
		t.Fatal(err)
	}
	if !runtimeSchema || runtimeDelete || runtimePrune || !runtimeInvalidate || !runtimeRetire || !runtimeObjectFence || runtimeGCLease || readonlyInsert || !maintenancePrune || !maintenanceObjectFence || !maintenanceGCLease || !maintenanceReachability || maintenanceStateDML {
		t.Fatalf("cache role grants schema=%v runtime_delete=%v runtime_prune=%v runtime_invalidate=%v runtime_retire=%v runtime_object_fence=%v runtime_gc=%v maintenance_prune=%v maintenance_object_fence=%v maintenance_gc=%v maintenance_reachability=%v maintenance_state_dml=%v readonly_insert=%v", runtimeSchema, runtimeDelete, runtimePrune, runtimeInvalidate, runtimeRetire, runtimeObjectFence, runtimeGCLease, maintenancePrune, maintenanceObjectFence, maintenanceGCLease, maintenanceReachability, maintenanceStateDML, readonlyInsert)
	}
	runtime, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.Exec(t.Context(), `DELETE FROM cache.cache_invalidation`); err == nil {
		t.Fatal("runtime DELETE on durable invalidation history succeeded")
	}
	if _, err := runtime.Exec(t.Context(), `UPDATE cache.cache_invalidation SET reason='forged'`); err == nil {
		t.Fatal("runtime UPDATE on durable invalidation history succeeded")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT * FROM cache.prune_coordination(clock_timestamp(),1)`); err == nil {
		t.Fatal("runtime prune capability unexpectedly granted")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT cache.prepare_l3_object_gc($1,$2,'cache/object','runtime',1)`, uuid.New(), cacheTestDigest('d')); err == nil {
		t.Fatal("runtime L3 object-GC preparation capability unexpectedly granted")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT * FROM cache.acquire_l3_object_fence($1,$2,$3,'runtime',interval '1 minute')`, uuid.New(), cacheTestDigest('d'), "cache/l3/sd/"+cacheTestDigest('e')+"/"+cacheTestDigest('a')+"/"+cacheTestDigest('b')); err == nil {
		t.Fatal("runtime acquired an L3 object fence outside its storage-security-domain path")
	}
	if _, err := runtime.Exec(t.Context(), `INSERT INTO cache.cache_namespace_epoch(namespace_key,partition_kind,target_id,project_id,environment,epoch) VALUES ('forged','production','target_sales','project_sales','prod',1)`); err == nil {
		t.Fatal("runtime direct namespace fabrication succeeded")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT cache.advance_dependency_revision('forged','custom','orders',$1,0,NULL::jsonb,NULL)`, cacheTestDigest('a')); err == nil {
		t.Fatal("runtime intermediate dependency revision capability unexpectedly granted")
	}
	runtimeRepo := New(runtime)
	if _, err := runtimeRepo.InvalidateNamespace(t.Context(), NamespaceInvalidationInput{Namespace: Namespace{PartitionKind: PartitionProduction, TargetID: "runtime_target", ProjectID: "runtime_project", Environment: "prod"}, Kind: DependencyCustom, DependencyID: "runtime", IdempotencyKey: "runtime-invalidate", Reason: "runtime test"}, cacheTestEvidence("runtime")); err != nil {
		t.Fatalf("runtime invalidation capability failed: %v", err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO cache.cache_namespace_epoch(namespace_key,partition_kind,target_id,project_id,environment,epoch) VALUES ('forged','production','target_sales','project_sales','prod',1)`); err == nil {
		t.Fatal("forged namespace identity unexpectedly accepted by database guard")
	}
	maintenance, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	if _, err := maintenance.Exec(t.Context(), `SELECT * FROM cache.prune_coordination(clock_timestamp(),1)`); err != nil {
		t.Fatalf("maintenance bounded prune function failed: %v", err)
	}
}
