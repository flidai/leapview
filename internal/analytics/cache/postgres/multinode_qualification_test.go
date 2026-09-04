package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgreSQL18MultiNodeCacheQualification uses independent pools to
// represent two cache nodes. The second node takes over expired durable
// fences, rejects the first node's stale publication, and still publishes a
// reachable manifest from the durable coordination state.
func TestPostgreSQL18MultiNodeCacheQualification(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "cache_multinode_qualification")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	poolA, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolA.Close)
	poolB, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(poolB.Close)
	if poolA == poolB {
		t.Fatal("multi-node qualification accidentally reused one PostgreSQL pool")
	}
	tx, err := poolA.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply cache schema: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	repoA := New(poolA)
	repoB := New(poolB)
	if repoA == repoB {
		t.Fatal("multi-node qualification accidentally reused one cache repository")
	}
	key := cacheTestKey()
	cacheKey, err := key.CacheKeyDigest()
	if err != nil {
		t.Fatal(err)
	}
	namespace := cacheTestNamespace()
	storageDomain := cacheTestDigest('d')
	objectDigest := cacheTestDigest('e')
	// Match the production L3 object-key layout: security domain, cache-key
	// digest, then immutable object digest.
	objectKey := "cache/l3/sd/" + storageDomain + "/" + cacheKey + "/" + objectDigest
	const reusedOwner = "node-reused"

	// Keep the active lease long enough for the second pool's first connection
	// to be established on a cold or contended CI runner. Expire both fences
	// explicitly below so this qualification does not depend on scheduler
	// timing (the previous 25ms lease made the active-owner assertion flaky).
	fillA, err := repoA.AcquireFill(ctx, AcquireFillInput{Namespace: namespace, CacheKey: cacheKey, OwnerID: reusedOwner, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repoB.AcquireFill(ctx, AcquireFillInput{Namespace: namespace, CacheKey: cacheKey, OwnerID: "node-b", Lease: time.Minute}); !errors.Is(err, ErrBusy) {
		t.Fatalf("active fill claim from second node = %v, want ErrBusy", err)
	}
	objectA, err := repoA.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{StorageSecurityDomain: storageDomain, ObjectKey: objectKey, OwnerID: reusedOwner, Lease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repoB.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{StorageSecurityDomain: storageDomain, ObjectKey: objectKey, OwnerID: "node-b", Lease: time.Minute}); !errors.Is(err, ErrBusy) {
		t.Fatalf("active object fence claim from second node = %v, want ErrBusy", err)
	}

	// The qualification uses the administrator URL, so forcing the timestamps
	// here exercises the same expired-fence takeover path without sleeping.
	if _, err := poolA.Exec(ctx, `UPDATE cache.cache_fill_lease
		SET acquired_at=clock_timestamp()-interval '2 seconds', expires_at=clock_timestamp()-interval '1 second'
		WHERE lease_id=$1`, fillA.LeaseID); err != nil {
		t.Fatalf("expire fill fence: %v", err)
	}
	if _, err := poolA.Exec(ctx, `UPDATE cache.cache_l3_object_fence
		SET acquired_at=clock_timestamp()-interval '2 seconds', expires_at=clock_timestamp()-interval '1 second'
		WHERE lease_id=$1`, objectA.LeaseID); err != nil {
		t.Fatalf("expire object fence: %v", err)
	}
	fillB, err := repoB.AcquireFill(ctx, AcquireFillInput{Namespace: namespace, CacheKey: cacheKey, OwnerID: reusedOwner, Lease: time.Minute})
	if err != nil {
		t.Fatalf("durable fill takeover: %v", err)
	}
	if fillB.FencingEpoch <= fillA.FencingEpoch {
		t.Fatalf("fill takeover did not advance fencing: first=%#v second=%#v", fillA, fillB)
	}
	objectB, err := repoB.AcquireL3ObjectFence(ctx, AcquireL3ObjectFenceInput{StorageSecurityDomain: storageDomain, ObjectKey: objectKey, OwnerID: reusedOwner, Lease: time.Minute})
	if err != nil {
		t.Fatalf("durable object-fence takeover: %v", err)
	}
	if objectB.FencingEpoch <= objectA.FencingEpoch {
		t.Fatalf("object-fence takeover did not advance fencing: first=%#v second=%#v", objectA, objectB)
	}

	if err := repoA.RenewFill(ctx, fillA, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale fill heartbeat = %v, want ErrStaleFence", err)
	}
	if err := repoA.RenewL3ObjectFence(ctx, objectA, time.Minute); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale object heartbeat = %v, want ErrStaleFence", err)
	}
	staleInput := PublishInput{
		Key: key, OriginSnapshotSealID: cacheTestOriginSeal, StorageSecurityDomain: storageDomain,
		ObjectDigest: objectDigest, ObjectKey: objectKey, ByteSize: 1, Metadata: []byte(`{"rows":1}`),
		Lease: fillA, ObjectFence: objectA,
	}
	if _, err := repoA.Publish(ctx, staleInput); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale node publication = %v, want ErrStaleFence", err)
	}

	validInput := staleInput
	validInput.Lease = fillB
	validInput.ObjectFence = objectB
	manifest, err := repoB.Publish(ctx, validInput)
	if err != nil {
		t.Fatalf("takeover publication: %v", err)
	}
	got, found, err := repoA.Lookup(ctx, key)
	if err != nil || !found {
		t.Fatalf("manifest lookup through first node = %#v, found=%v, err=%v", got, found, err)
	}
	if got.ManifestID != manifest.ManifestID || got.ObjectKey != objectKey || got.ObjectDigest != objectDigest {
		t.Fatalf("durable publication = %#v, want %#v", got, manifest)
	}
}
