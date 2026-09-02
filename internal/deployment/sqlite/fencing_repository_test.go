package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentgc "github.com/flidai/leapview/internal/deployment/gc"
	"github.com/flidai/leapview/internal/platform"
)

func fencingPool(t *testing.T, store *platform.Store) string {
	t.Helper()
	pool := "sha256:" + strings.Repeat("a", 64)
	_, err := store.SQLDB().ExecContext(context.Background(), `INSERT INTO physical_pools (id,identity_digest,storage_location,storage_namespace,storage_implementation,object_naming_contract,encryption_domain,isolation_boundary,retention_authority,retention_policy_json) VALUES (?,?,?,?,?,?,?,?,?,?)`, pool, pool, "s3://fence", "fence", "s3", "names-v1", "fence", "fence", "fence", `{}`)
	if err != nil {
		t.Fatalf("insert fencing pool: %v", err)
	}
	return pool
}

func TestFencingAllowsConcurrentWritersAndGCExcludesAllWriters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fence.db")
	store1, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store1.Close()
	store2, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	pool := fencingPool(t, store1)
	r1, r2 := NewRepositoryWithHooks(store1.SQLDB(), ActivationHooks{}), NewRepositoryWithHooks(store2.SQLDB(), ActivationHooks{})
	now := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	got := make(chan WriterFence, 2)
	for i, repo := range []*Repository{r1, r2} {
		wg.Add(1)
		go func(i int, repo *Repository) {
			defer wg.Done()
			f, err := repo.AcquireWriterFence(ctx, WriterFenceRequest{ID: "writer-" + string(rune('1'+i)), AttemptID: "attempt-" + string(rune('1'+i)), PhysicalPoolID: pool, HolderID: "holder-" + string(rune('1'+i)), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
				return
			}
			got <- f
		}(i, repo)
	}
	wg.Wait()
	close(got)
	var fences []WriterFence
	for f := range got {
		fences = append(fences, f)
	}
	if len(fences) != 2 || fences[0].Epoch == fences[1].Epoch {
		t.Fatalf("concurrent writer epochs = %#v", fences)
	}
	if _, err := r1.AcquireGCFence(ctx, GCFenceRequest{ID: "gc-1", PhysicalPoolID: pool, HolderID: "collector", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("GC acquired over active writers: %v", err)
	}
	if err := r1.ReleaseWriterFence(ctx, fences[0], now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := r2.ReleaseWriterFence(ctx, fences[1], now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	gc, err := r2.AcquireGCFence(ctx, GCFenceRequest{ID: "gc-2", PhysicalPoolID: pool, HolderID: "collector", CreatedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r1.AcquireWriterFence(ctx, WriterFenceRequest{ID: "writer-3", AttemptID: "attempt-3", PhysicalPoolID: pool, HolderID: "holder-3", CreatedAt: now.Add(3 * time.Minute), ExpiresAt: now.Add(time.Hour)}); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("writer acquired over GC: %v", err)
	}
	if err := r2.ReleaseGCFence(ctx, gc, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestFencingExpiryAndStaleEpochFailClosedAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	store, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	pool := fencingPool(t, store)
	r := NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	old, err := r.AcquireWriterFence(ctx, WriterFenceRequest{ID: "old", AttemptID: "old-attempt", PhysicalPoolID: pool, HolderID: "builder", CreatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	store2, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	r2 := NewRepositoryWithHooks(store2.SQLDB(), ActivationHooks{})
	newFence, err := r2.AcquireWriterFence(ctx, WriterFenceRequest{ID: "new", AttemptID: "new-attempt", PhysicalPoolID: pool, HolderID: "builder-2", CreatedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if newFence.Epoch <= old.Epoch {
		t.Fatalf("epoch did not increase: old=%d new=%d", old.Epoch, newFence.Epoch)
	}
	if err := r2.ReleaseWriterFence(ctx, old, now.Add(3*time.Minute)); !errors.Is(err, deployment.ErrDeliveryStale) {
		t.Fatalf("stale release error = %v", err)
	}
	if ok, err := r2.IsCurrentWriterFence(ctx, old, now.Add(3*time.Minute)); err != nil || ok {
		t.Fatalf("stale fence accepted: ok=%v err=%v", ok, err)
	}
}

func TestRootRegistryNoResurrectionAndStableSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "roots.db")
	store, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pool := fencingPool(t, store)
	r := NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
	now := time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("b", 64)
	root := DeliveryRoot{PhysicalPoolID: pool, Kind: "quarantined", SourceID: "hold-1", CatalogDigest: digest, ObjectKey: "catalogs/hold.ducklake", CreatedAt: now}
	if _, err := r.RegisterRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RetireRoot(ctx, pool, root.Kind, root.SourceID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterRoot(ctx, root); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("retired root resurrected: %v", err)
	}
	set, err := r.EnumerateRoots(ctx, pool, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Roots) != 0 {
		t.Fatalf("retired root remains in active set: %#v", set.Roots)
	}
}

func TestWriterRetryDoesNotAdvanceEpochAndReleasedRetryConflicts(t *testing.T) {
	ctx := context.Background()
	store, repo := openDeliveryRepository(t)
	pool := repoDeliveryDigest('c')
	insertDeliveryPool(t, store, pool)
	now := time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC)
	req := WriterFenceRequest{ID: "retry-writer", AttemptID: "retry-attempt", PhysicalPoolID: pool, HolderID: "retry-holder", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	first, err := repo.AcquireWriterFence(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repo.AcquireWriterFence(ctx, req)
	if err != nil || replayed.Epoch != first.Epoch {
		t.Fatalf("retry=%#v err=%v first=%#v", replayed, err, first)
	}
	fence, err := repo.PoolFence(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if fence.WriterEpoch != first.Epoch {
		t.Fatalf("retry advanced writer epoch to %d from %d", fence.WriterEpoch, first.Epoch)
	}
	if err := repo.ReleaseWriterFence(ctx, first, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireWriterFence(ctx, req); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("released retry error=%v", err)
	}
}

func TestGCRetryPreservesEpoch(t *testing.T) {
	ctx := context.Background()
	store, repo := openDeliveryRepository(t)
	pool := repoDeliveryDigest('d')
	insertDeliveryPool(t, store, pool)
	now := time.Date(2026, 8, 17, 5, 30, 0, 0, time.UTC)
	req := GCFenceRequest{ID: "retry-gc", PhysicalPoolID: pool, HolderID: "collector", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	first, err := repo.AcquireGCFence(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repo.AcquireGCFence(ctx, req)
	if err != nil || replayed.Epoch != first.Epoch {
		t.Fatalf("GC retry=%#v err=%v first=%#v", replayed, err, first)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO delivery_root_registry (physical_pool_id,root_kind,source_id,catalog_digest,object_key,status,created_at) VALUES (?,?,?,?,'catalogs/direct.ducklake','active',?)`, pool, "quarantined", "direct-root", repoDeliveryDigest('e'), now.Add(10*time.Minute).Format(time.RFC3339Nano)); err == nil {
		t.Fatal("direct root insertion bypassed active GC fence")
	}
	if err := repo.ReleaseGCFence(ctx, first, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireGCFence(ctx, req); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("released GC retry error=%v", err)
	}
	second, err := repo.AcquireGCFence(ctx, GCFenceRequest{ID: "retry-gc-b", PhysicalPoolID: pool, HolderID: "collector", CreatedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(3 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseGCFence(ctx, second, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireGCFence(ctx, req); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("A/B/A GC identity reuse error=%v", err)
	}
}

func TestRootObjectKeyValidationHandlesShortUnsafeKeys(t *testing.T) {
	for _, key := range []string{".", "..", "./", "../", "a"} {
		root := DeliveryRoot{PhysicalPoolID: "sha256:" + strings.Repeat("a", 64), Kind: "quarantined", SourceID: "short-key", CatalogDigest: "sha256:" + strings.Repeat("b", 64), ObjectKey: key, CreatedAt: time.Date(2026, 8, 17, 6, 0, 0, 0, time.UTC)}
		err := validateRoot(root)
		if key == "a" && err != nil {
			t.Fatalf("canonical short key rejected: %v", err)
		}
		if key != "a" && err == nil {
			t.Fatalf("unsafe short key %q accepted", key)
		}
	}
}

func TestCreateWriterLeaseAndAttemptAllocatesEpochAndBindsIdentity(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 7, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('f')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "atomic-writer", AttemptID: "atomic-attempt", PhysicalPoolID: pool, OwnerID: "builder", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: "atomic-attempt", PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	storedLease, _, err := repo.CreateWriterLeaseAndBuildAttempt(context.Background(), lease, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if storedLease.Epoch != 1 {
		t.Fatalf("allocated epoch=%d, want 1", storedLease.Epoch)
	}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(context.Background(), lease, attempt); err != nil {
		t.Fatalf("exact transaction retry failed: %v", err)
	}
	changed := attempt
	changed.SourceDigest = repoDeliveryDigest('e')
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(context.Background(), lease, changed); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("full identity retry error=%v", err)
	}
}

// readyEnumerationCandidate builds the smallest fully governed candidate so
// root enumeration can be tested against the real seal/candidate lifecycle.
func readyEnumerationCandidate(t *testing.T, store *platform.Store, repo *Repository, now time.Time) (deployment.DeliveryCandidate, deployment.CatalogSeal) {
	t.Helper()
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('8')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "enum-writer", AttemptID: "enum-attempt", PhysicalPoolID: pool, OwnerID: "builder", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: lease.AttemptID, PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(context.Background(), lease, attempt); err != nil {
		t.Fatal(err)
	}
	for i, next := range []deployment.DeliveryBuildAttemptStatus{deployment.DeliveryBuildNormalizing, deployment.DeliveryBuildValidating, deployment.DeliveryBuildSealing} {
		if _, err := repo.TransitionBuildAttempt(context.Background(), attempt.ID, int64(i+1), next, now.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	seal, err := repo.PrepareCatalogSeal(context.Background(), deployment.CatalogSeal{ID: "enum-seal", AttemptID: attempt.ID, PlanID: plan.ID, PlanDigest: plan.Digest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, CatalogDigest: repoDeliveryDigest('9'), CompatibilityDigest: repoDeliveryDigest('a'), ServingArtifactID: "artifact-enum-1", ServingArtifactDigest: repoDeliveryDigest('d'), ServingStateID: "state-enum", ObjectKey: "catalogs/enum.ducklake", ObjectSize: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkCatalogSealUploaded(context.Background(), seal.ID); err != nil {
		t.Fatal(err)
	}
	seal, err = repo.VerifyCatalogSeal(context.Background(), seal.ID, repoDeliveryDigest('b'), repoDeliveryDigest('c'), now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	candidate := deployment.DeliveryCandidate{ID: "enum-candidate", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseTargetRevision: 0, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-enum", CreatedAt: now, ResolvedInputs: sqliteResolvedInputs(t, plan, "enum-candidate")}
	return candidate, seal
}

func TestEnumerateRootsVerifiedSealStandaloneUntilCandidate(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	candidate, seal := readyEnumerationCandidate(t, store, repo, now)
	set, err := repo.EnumerateRoots(context.Background(), candidate.PhysicalPoolID, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	standalone := 0
	for _, root := range set.Roots {
		if root.Kind == "build" && root.SourceID == seal.ID {
			standalone++
		}
	}
	if standalone != 1 {
		t.Fatalf("verified standalone seal roots=%d, roots=%#v", standalone, set.Roots)
	}
	if _, err := repo.CreateCandidateReady(context.Background(), candidate, seal, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	set, err = repo.EnumerateRoots(context.Background(), candidate.PhysicalPoolID, now.Add(6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range set.Roots {
		if root.Kind == "build" && root.SourceID == seal.ID {
			t.Fatalf("verified seal remained standalone after candidate creation: %#v", set.Roots)
		}
	}
}

func TestEnumerateRootsWithGraceRetainsExpiredReaderUntilDrainWindow(t *testing.T) {
	ctx := context.Background()
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	candidate, seal := readyEnumerationCandidate(t, store, repo, now)
	if _, err := repo.CreateCandidateReady(ctx, candidate, seal, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	lease := deployment.DeliveryQueryLease{
		ID: "grace-expired-reader", HolderID: "reader", CandidateID: candidate.ID,
		CatalogDigest: candidate.CatalogDigest, PhysicalPoolID: candidate.PhysicalPoolID,
		CreatedAt: now.Add(6 * time.Minute), ExpiresAt: now.Add(10 * time.Minute),
	}
	if _, err := repo.AcquireQueryLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ExpireQueryLease(ctx, lease.ID, lease.ExpiresAt); err != nil {
		t.Fatal(err)
	}

	withinGrace, err := repo.EnumerateRootsWithGrace(ctx, candidate.PhysicalPoolID, now.Add(15*time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !hasLeaseRoot(withinGrace, lease.ID) {
		t.Fatalf("expired lease disappeared inside ReaderGrace: %#v", withinGrace.Roots)
	}
	afterGrace, err := repo.EnumerateRootsWithGrace(ctx, candidate.PhysicalPoolID, now.Add(21*time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if hasLeaseRoot(afterGrace, lease.ID) {
		t.Fatalf("expired lease remained after ReaderGrace: %#v", afterGrace.Roots)
	}

	active := lease
	active.ID = "released-reader"
	active.CreatedAt = now.Add(11 * time.Minute)
	active.ExpiresAt = now.Add(time.Hour)
	if _, err := repo.AcquireQueryLease(ctx, active); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReleaseQueryLease(ctx, active.ID, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	afterRelease, err := repo.EnumerateRootsWithGrace(ctx, candidate.PhysicalPoolID, now.Add(12*time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if hasLeaseRoot(afterRelease, active.ID) {
		t.Fatalf("released reader remained a root: %#v", afterRelease.Roots)
	}

	// Run the real collector against the same repository to prove the root
	// protects the catalog's physical closure, not just its control row.
	if _, err := repo.RetireDeliveryCandidate(ctx, candidate.ID, now.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	dataKey := "data/orders.parquet"
	objects := &sealedLeaseGCStore{objects: map[string]deploymentgc.Object{
		candidate.CatalogObjectKey: {Key: candidate.CatalogObjectKey, Digest: candidate.CatalogDigest, CreatedAt: now},
		dataKey:                    {Key: dataKey, Digest: repoDeliveryDigest('e'), CreatedAt: now},
	}}
	// The fixture's build writer is no longer doing work; let its durable
	// expiry pass before GC so the collector can acquire the pool fence.
	if _, err := store.SQLDB().ExecContext(ctx, "UPDATE delivery_writer_leases SET expires_at=? WHERE id=?", deliveryTime(now.Add(12*time.Minute)), "enum-writer"); err != nil {
		t.Fatal(err)
	}
	inspector := sealedLeaseGCInspector{candidate.CatalogObjectKey: {CatalogKey: candidate.CatalogObjectKey, CatalogDigest: candidate.CatalogDigest, DataFiles: []string{dataKey}}}
	withinResult, err := (deploymentgc.Collector{
		Control: repo, Store: objects, Inspector: inspector,
		Config: deploymentgc.Config{PhysicalPoolID: candidate.PhysicalPoolID, HolderID: "grace-gc", CycleID: "grace-gc-cycle", Now: func() time.Time { return now.Add(15 * time.Minute) }, OrphanGrace: time.Minute, ReaderGrace: 10 * time.Minute},
	}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if withinResult.Deleted != 0 || len(objects.deleted) != 0 {
		t.Fatalf("expired lease within grace did not protect closure: result=%#v deleted=%v", withinResult, objects.deleted)
	}
	afterResult, err := (deploymentgc.Collector{
		Control: repo, Store: objects, Inspector: inspector,
		Config: deploymentgc.Config{PhysicalPoolID: candidate.PhysicalPoolID, HolderID: "grace-gc-after", CycleID: "grace-gc-after-cycle", Now: func() time.Time { return now.Add(21 * time.Minute) }, OrphanGrace: time.Minute, ReaderGrace: 10 * time.Minute},
	}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterResult.Deleted != 2 || len(objects.deleted) != 2 {
		t.Fatalf("expired lease closure remained after grace: result=%#v deleted=%v", afterResult, objects.deleted)
	}
}

func hasLeaseRoot(set RootSet, leaseID string) bool {
	for _, root := range set.Roots {
		if root.Kind == "lease" && root.SourceID == leaseID {
			return true
		}
	}
	return false
}

func TestQueryLeaseAndCandidateRetirementRaceHasOneWinner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lease-retire.db")
	store, repo := openDeliveryRepository(t)
	// Reopen the fixture on a shared path so each repository has an independent
	// SQLite connection while observing the same durable fence.
	_ = store.Close()
	store, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo = NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
	candidate, seal := readyEnumerationCandidate(t, store, repo, time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC))
	if _, err := repo.CreateCandidateReady(ctx, candidate, seal, time.Date(2026, 8, 17, 9, 5, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	store2, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	repo2 := NewRepositoryWithHooks(store2.SQLDB(), ActivationHooks{})
	now := time.Date(2026, 8, 17, 9, 6, 0, 0, time.UTC)
	lease := deployment.DeliveryQueryLease{ID: "race-query", HolderID: "reader", CandidateID: candidate.ID, CatalogDigest: candidate.CatalogDigest, PhysicalPoolID: candidate.PhysicalPoolID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	var wg sync.WaitGroup
	queryDone := make(chan error, 1)
	retireDone := make(chan error, 1)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _, err := repo2.AcquireQueryLeaseAgainstRoot(ctx, lease)
		queryDone <- err
	}()
	go func() {
		defer wg.Done()
		_, err := repo.RetireDeliveryCandidate(ctx, candidate.ID, now)
		retireDone <- err
	}()
	wg.Wait()
	queryErr, retireErr := <-queryDone, <-retireDone
	if (queryErr == nil) == (retireErr == nil) {
		t.Fatalf("race winners queryErr=%v retireErr=%v", queryErr, retireErr)
	}
	if queryErr != nil && !errors.Is(queryErr, deployment.ErrDeliveryConflict) {
		t.Fatalf("query loser error=%v", queryErr)
	}
	if retireErr != nil && !errors.Is(retireErr, deployment.ErrDeliveryConflict) {
		t.Fatalf("retirement loser error=%v", retireErr)
	}
}
