package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func cacheTestDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func cacheTestEvidence(reason string) json.RawMessage {
	evidence, err := lifecycleEvidence(json.RawMessage(`{"version":1,"reason":"` + reason + `"}`))
	if err != nil {
		panic(err)
	}
	return evidence
}

func TestLifecycleEvidenceValidation(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"missing":   nil,
		"empty":     json.RawMessage(`{}`),
		"version":   json.RawMessage(`{"version":2,"reason":"refresh"}`),
		"reason":    json.RawMessage(`{"version":1,"reason":"   "}`),
		"duplicate": json.RawMessage(`{"version":1,"version":1,"reason":"refresh"}`),
		"oversized": json.RawMessage(strings.Repeat("x", maxEvidenceBytes+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lifecycleEvidence(raw); !errors.Is(err, ErrInvalid) {
				t.Fatalf("evidence error = %v, want invalid", err)
			}
		})
	}
	canonical, err := lifecycleEvidence(json.RawMessage(`{"reason":"refresh","version":1}`))
	if err != nil || !bytes.Equal(canonical, cacheTestEvidence("refresh")) {
		t.Fatalf("canonical evidence = %s, %v", canonical, err)
	}
}

func cacheTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "cache_test")
	p, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
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
	return p
}

func TestManifestKeyIdentityConvergesWithL1CacheKey(t *testing.T) {
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	dependency, err := resultidentity.NewDependency(resultidentity.DependencyInput{SemanticModelID: "semantic_sales", SemanticModelDigest: cacheTestDigest('a'), Relations: []resultidentity.RelationRevision{{RelationID: "orders", RevisionDigest: cacheTestDigest('b')}}, BindingFingerprint: cacheTestDigest('c'), Execution: resultidentity.ExecutionIdentity{PlannerDigest: cacheTestDigest('d'), RuntimeDigest: cacheTestDigest('e'), CapabilityDigest: cacheTestDigest('f'), SettingsDigest: cacheTestDigest('0')}, ResultFormat: resultidentity.ResultFormat{Name: "arrow-ipc", Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	queryDigest := cacheTestDigest('9')
	l1, err := analyticscache.NewKey(analyticscache.KeyInput{Partition: partition, Dependency: dependency, EffectivePolicyFingerprint: cacheTestDigest('1'), CanonicalQueryDigest: queryDigest})
	if err != nil {
		t.Fatal(err)
	}
	durable, err := ManifestKeyFromIdentity(l1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := durable.CacheKeyDigest(); err != nil || got != l1.Digest() {
		t.Fatalf("L1/durable key digest = %q, %v; want %q", got, err, l1.Digest())
	}
}

func cacheTestKey() ManifestKey {
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "project_sales", Environment: "prod"})
	if err != nil {
		panic(err)
	}
	return ManifestKey{PartitionKind: PartitionProduction, ProjectID: partition.ProjectID().String(), Environment: partition.Environment(), PartitionFormatVersion: int64(partition.Version()), DependencyDigest: cacheTestDigest('a'), PolicyFingerprint: cacheTestDigest('b'), CanonicalQueryDigest: cacheTestDigest('c'), KeyFormatVersion: 1}
}

func TestRepositoryFillFencePublishLookupAndDependencyInvalidation(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: fillKey, OwnerID: "node-a", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: fillKey, OwnerID: "node-b", Lease: time.Second}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second fill error = %v, want ErrBusy", err)
	}
	manifest, err := repo.Publish(ctx, PublishInput{Key: key, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/" + cacheTestDigest('e'), ByteSize: 42, Metadata: []byte(`{"rows":1}`), Lease: first})
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.Lookup(ctx, key)
	if err != nil || !found {
		t.Fatalf("Lookup() = %#v, %v, found=%v", got, err, found)
	}
	if got.ManifestID != manifest.ManifestID || got.ByteSize != 42 || string(got.Metadata) != `{"rows": 1}` {
		t.Fatalf("manifest = %#v", got)
	}
	released, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: fillKey, OwnerID: "node-c", Lease: time.Second})
	if err != nil {
		t.Fatalf("published fill was not released: %v", err)
	}
	if err := repo.ReleaseFill(ctx, released); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InvalidateDependency(ctx, key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing invalidation evidence = %v, want invalid", err)
	}
	changed, err := repo.InvalidateDependency(ctx, key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, cacheTestEvidence("dependency-refresh"))
	if err != nil || changed != 1 {
		t.Fatalf("InvalidateDependency() = %d, %v", changed, err)
	}
	if _, found, err := repo.Lookup(ctx, key); err != nil || found {
		t.Fatalf("invalidated manifest lookup = found %v, err %v", found, err)
	}
}

func TestRepositoryConcurrentFillAcquisitionHasOneOwnerAndAdvancesFence(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	key := cacheTestDigest('f')
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan FillLease, 2)
	errs := make(chan error, 2)
	for _, owner := range []string{"node-a", "node-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			lease, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: key, OwnerID: owner, Lease: time.Second})
			results <- lease
			errs <- err
		}(owner)
	}
	wg.Wait()
	close(results)
	close(errs)
	var acquired FillLease
	count := 0
	for err := range errs {
		if err == nil {
			count++
		} else if !errors.Is(err, ErrBusy) {
			t.Fatalf("concurrent acquisition error = %v", err)
		}
	}
	for lease := range results {
		if lease.LeaseID != uuid.Nil {
			acquired = lease
		}
	}
	if count != 1 || acquired.LeaseID == uuid.Nil {
		t.Fatalf("acquisition count=%d lease=%#v", count, acquired)
	}
	if err := repo.ReleaseFill(ctx, acquired); err != nil {
		t.Fatal(err)
	}
	next, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: key, OwnerID: "node-c", Lease: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if next.FencingEpoch <= acquired.FencingEpoch {
		t.Fatalf("fence epoch did not advance: old=%d new=%d", acquired.FencingEpoch, next.FencingEpoch)
	}
	if err := repo.ReleaseFill(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryExpiredOwnerIsFencedBeforePublish(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestDigest('7')
	old, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: key, OwnerID: "node-old", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE cache.cache_fill_lease SET acquired_at=clock_timestamp()-interval '2 seconds', expires_at=clock_timestamp()-interval '1 second' WHERE lease_id=$1`, old.LeaseID); err != nil {
		t.Fatal(err)
	}
	next, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: key, OwnerID: "node-new", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if next.FencingEpoch <= old.FencingEpoch {
		t.Fatalf("fence did not advance: old=%d new=%d", old.FencingEpoch, next.FencingEpoch)
	}
	if err := repo.RenewFill(ctx, old, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale renew error=%v", err)
	}
	if err := repo.ReleaseFill(ctx, next); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRejectsUnrelatedFenceOnPublish(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	other := key
	other.CanonicalQueryDigest = cacheTestDigest('9')
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: fillKey, OwnerID: "node-a", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Publish(ctx, PublishInput{Key: other, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 1, Lease: lease})
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("unrelated fence publish error=%v, want ErrStaleFence", err)
	}
	if err := repo.ReleaseFill(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPublishReplayIsIdempotentAndChangedContentsConflict(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: fillKey, OwnerID: "node-replay", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	in := PublishInput{Key: key, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 7, Metadata: []byte(`{"rows":1}`), Lease: lease}
	first, err := repo.Publish(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.Publish(ctx, in)
	if err != nil {
		t.Fatalf("lost-ACK replay: %v", err)
	}
	if replay.ManifestID != first.ManifestID {
		t.Fatalf("replay manifest = %s, want %s", replay.ManifestID, first.ManifestID)
	}
	changed := in
	changed.ObjectKey = "cache/other-object"
	if _, err := repo.Publish(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}
	if _, err := repo.Publish(ctx, PublishInput{Key: key, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 7, Metadata: []byte(`{"rows":1,"rows":2}`), Lease: lease}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate metadata error = %v, want invalid", err)
	}
}

func TestRepositoryRetentionLifecycleAndManifestImmutability(t *testing.T) {
	p := cacheTestDB(t)
	repo := New(p)
	ctx := context.Background()
	key := cacheTestKey()
	fillKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := repo.AcquireFill(ctx, AcquireFillInput{CacheKey: fillKey, OwnerID: "node-retention", Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := repo.Publish(ctx, PublishInput{Key: key, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/object", ByteSize: 7, Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing root retirement evidence = %v, want invalid", err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, json.RawMessage(strings.Repeat("x", maxEvidenceBytes+1))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized root retirement evidence = %v, want invalid", err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "dashboard"); err != nil {
		t.Fatalf("idempotent root replay: %v", err)
	}
	if err := repo.AddRetentionRoot(ctx, rootID, manifest.ManifestID, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("root identity conflict: %v", err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("dashboard-closed")); err != nil {
		t.Fatal(err)
	}
	var retireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT retire_evidence FROM cache.cache_retention_root WHERE root_id=$1`, rootID).Scan(&retireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedRootEvidence, err := lifecycleEvidence(retireEvidence)
	if err != nil || !bytes.Equal(normalizedRootEvidence, cacheTestEvidence("dashboard-closed")) {
		t.Fatalf("persisted root retirement evidence = %s", retireEvidence)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched root retirement replay = %v, want conflict", err)
	}
	if err := repo.RetireRetentionRoot(ctx, rootID, cacheTestEvidence("dashboard-closed")); err != nil {
		t.Fatalf("idempotent root retirement replay: %v", err)
	}
	if changed, err := repo.InvalidateDependency(ctx, key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, cacheTestEvidence("dependency-refresh")); err != nil || changed != 1 {
		t.Fatalf("invalidate before retention expiry = %d, %v", changed, err)
	}
	var manifestRetireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT retire_evidence FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&manifestRetireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedManifestRetireEvidence, err := lifecycleEvidence(manifestRetireEvidence)
	if err != nil || !bytes.Equal(normalizedManifestRetireEvidence, cacheTestEvidence("dependency-refresh")) {
		t.Fatalf("persisted manifest retirement evidence = %s", manifestRetireEvidence)
	}
	if changed, err := repo.InvalidateDependency(ctx, key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) || changed != 0 {
		t.Fatalf("mismatched manifest retirement replay = %d, %v", changed, err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("manifest-gc")); !errors.Is(err, ErrConflict) {
		t.Fatalf("manifest expired with retiring root: %v", err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing root expiry evidence = %v, want invalid", err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("root-gc")); err != nil {
		t.Fatal(err)
	}
	var rootExpireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT expire_evidence FROM cache.cache_retention_root WHERE root_id=$1`, rootID).Scan(&rootExpireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedRootExpireEvidence, err := lifecycleEvidence(rootExpireEvidence)
	if err != nil || !bytes.Equal(normalizedRootExpireEvidence, cacheTestEvidence("root-gc")) {
		t.Fatalf("persisted root expiry evidence = %s", rootExpireEvidence)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched root expiry replay = %v, want conflict", err)
	}
	if err := repo.ExpireRetentionRoot(ctx, rootID, cacheTestEvidence("root-gc")); err != nil {
		t.Fatalf("idempotent root expiry replay: %v", err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing manifest expiry evidence = %v, want invalid", err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("manifest-gc")); err != nil {
		t.Fatal(err)
	}
	var expireEvidence []byte
	if err := p.QueryRow(ctx, `SELECT expire_evidence FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&expireEvidence); err != nil {
		t.Fatal(err)
	}
	normalizedExpireEvidence, err := lifecycleEvidence(expireEvidence)
	if err != nil || !bytes.Equal(normalizedExpireEvidence, cacheTestEvidence("manifest-gc")) {
		t.Fatalf("persisted manifest expiry evidence = %s", expireEvidence)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched manifest expiry replay = %v", err)
	}
	if err := repo.ExpireManifest(ctx, manifest.ManifestID, cacheTestEvidence("manifest-gc")); err != nil {
		t.Fatalf("idempotent manifest expiry replay: %v", err)
	}
	if changed, err := repo.InvalidateDependency(ctx, key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, cacheTestEvidence("different")); !errors.Is(err, ErrConflict) || changed != 0 {
		t.Fatalf("mismatched invalidation after expiry = %d, %v", changed, err)
	}
	if _, err := p.Exec(ctx, `UPDATE cache.cache_manifest SET object_key='cache/mutated' WHERE manifest_id=$1`, manifest.ManifestID); err == nil {
		t.Fatal("direct manifest object mutation succeeded")
	}
	if _, err := p.Exec(ctx, `DELETE FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID); err == nil {
		t.Fatal("direct manifest deletion succeeded")
	}
}

// TestRetentionRootManifestExpiryRaceLockOrdering exercises the two lock
// orderings that matter for retention safety. Both transactions lock the
// manifest row before inspecting or mutating roots, so a root cannot be added
// after expiry and expiry cannot overtake a root insert.
func TestRetentionRootManifestExpiryRaceLockOrdering(t *testing.T) {
	newFixture := func(t *testing.T) (*pgxpool.Pool, *Repository, Manifest, ManifestKey) {
		t.Helper()
		p := cacheTestDB(t)
		repo := New(p)
		key := cacheTestKey()
		cacheKey, err := key.CacheKeyDigest()
		if err != nil {
			t.Fatal(err)
		}
		lease, err := repo.AcquireFill(t.Context(), AcquireFillInput{CacheKey: cacheKey, OwnerID: "race-owner", Lease: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := repo.Publish(t.Context(), PublishInput{Key: key, StorageSecurityDomain: cacheTestDigest('d'), ObjectDigest: cacheTestDigest('e'), ObjectKey: "cache/race", ByteSize: 1, Lease: lease})
		if err != nil {
			t.Fatal(err)
		}
		return p, repo, manifest, key
	}
	waitBlocked := func(t *testing.T, p *pgxpool.Pool, tx pgx.Tx) {
		t.Helper()
		pid := tx.Conn().PgConn().PID()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			var blocked int
			if err := p.QueryRow(t.Context(), `SELECT cardinality(pg_blocking_pids($1))`, pid).Scan(&blocked); err == nil && blocked > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("transaction did not become lock-blocked")
	}

	t.Run("root lock first", func(t *testing.T) {
		p, repo, manifest, key := newFixture(t)
		defer p.Close()
		rootID, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		rootTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rootTx.Rollback(t.Context())
		var state string
		if err := rootTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != StateAdmitted {
			t.Fatalf("initial manifest state = %q", state)
		}
		expireTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer expireTx.Rollback(t.Context())
		locked := make(chan error, 1)
		go func() {
			var next string
			locked <- expireTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&next)
		}()
		waitBlocked(t, p, expireTx)
		if _, err := rootTx.Exec(t.Context(), `INSERT INTO cache.cache_retention_root (root_id,manifest_id,state,reason) VALUES ($1,$2,'live','race')`, rootID, manifest.ManifestID); err != nil {
			t.Fatal(err)
		}
		if err := rootTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-locked; err != nil {
			t.Fatal(err)
		}
		var roots bool
		if err := expireTx.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM cache.cache_retention_root WHERE manifest_id=$1 AND state IN ('live','retiring'))`, manifest.ManifestID).Scan(&roots); err != nil {
			t.Fatal(err)
		}
		if !roots {
			t.Fatal("expiry transaction missed committed retention root")
		}
		if err := expireTx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if changed, err := repo.InvalidateDependency(t.Context(), key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, cacheTestEvidence("dependency-refresh")); err != nil || changed != 1 {
			t.Fatalf("invalidate after root race = %d, %v", changed, err)
		}
		if err := repo.ExpireManifest(t.Context(), manifest.ManifestID, cacheTestEvidence("manifest-gc")); !errors.Is(err, ErrConflict) {
			t.Fatalf("expiry with committed root = %v, want conflict", err)
		}
	})

	t.Run("expiry lock first", func(t *testing.T) {
		p, repo, manifest, key := newFixture(t)
		defer p.Close()
		if changed, err := repo.InvalidateDependency(t.Context(), key.PartitionKind, key.ProjectID, key.Environment, key.CandidateID, key.DependencyDigest, cacheTestEvidence("dependency-refresh")); err != nil || changed != 1 {
			t.Fatalf("initial invalidate = %d, %v", changed, err)
		}
		expireTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer expireTx.Rollback(t.Context())
		var state string
		if err := expireTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != StateRetiring {
			t.Fatalf("retiring manifest state = %q", state)
		}
		rootID, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		rootTx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer rootTx.Rollback(t.Context())
		locked := make(chan error, 1)
		go func() {
			var next string
			locked <- rootTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifest.ManifestID).Scan(&next)
		}()
		waitBlocked(t, p, rootTx)
		if _, err := expireTx.Exec(t.Context(), `UPDATE cache.cache_manifest SET state='expired', expired_at=clock_timestamp(), expire_evidence=$2::jsonb WHERE manifest_id=$1 AND state='retiring'`, manifest.ManifestID, cacheTestEvidence("race-expire")); err != nil {
			t.Fatal(err)
		}
		if err := expireTx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := <-locked; err != nil {
			t.Fatal(err)
		}
		var next string
		if err := rootTx.QueryRow(t.Context(), `SELECT state FROM cache.cache_manifest WHERE manifest_id=$1`, manifest.ManifestID).Scan(&next); err != nil {
			t.Fatal(err)
		}
		if next != StateExpired {
			t.Fatalf("root transaction observed state = %q, want expired", next)
		}
		if err := rootTx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := repo.AddRetentionRoot(t.Context(), rootID, manifest.ManifestID, "race"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("root after expiry = %v, want not found", err)
		}
	})
}
