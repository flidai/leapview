package l3

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func l3PostgresDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "cache_l3_test")
	p, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := cachepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPostgresL3ConcurrentFillAndMissingObjectReconcile(t *testing.T) {
	db := l3PostgresDB(t)
	repo := cachepostgres.New(db)
	store := newMemoryStore()
	ns := testNamespace()
	c, err := New(Config{Authority: repo, Store: store, Namespace: ns, SecurityDomain: testDigest('d'), OriginSnapshotSealID: testOriginSnapshotSealID, Prefix: "objects", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	key := testKey()
	type result struct {
		lease cachepostgres.FillLease
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"owner-a", "owner-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			lease, acquireErr := c.AcquireFill(t.Context(), key, owner, 30_000_000_000)
			results <- result{lease: lease, err: acquireErr}
		}(owner)
	}
	wg.Wait()
	close(results)
	var winner cachepostgres.FillLease
	busy := 0
	for item := range results {
		if item.err == nil {
			winner = item.lease
		} else if errors.Is(item.err, cachepostgres.ErrBusy) {
			busy++
		} else {
			t.Fatalf("concurrent acquire error = %v", item.err)
		}
	}
	if winner.LeaseID == [16]byte{} || busy != 1 {
		t.Fatalf("concurrent fills winner=%v busy=%d", winner.LeaseID, busy)
	}
	store.putErr = ErrObjectAmbiguous
	manifest, err := c.Publish(t.Context(), PublishInput{Key: key, Lease: winner, Body: strings.NewReader("result")})
	if err != nil {
		t.Fatalf("publish after lost PUT acknowledgement: %v", err)
	}
	otherKey := key
	otherKey.DependencyDigest = testDigest('0')
	otherLease, err := c.AcquireFill(t.Context(), otherKey, "other-owner", 30*time.Second)
	if err != nil {
		t.Fatalf("other fill: %v", err)
	}
	otherManifest, err := c.Publish(t.Context(), PublishInput{Key: otherKey, Lease: otherLease, Body: strings.NewReader("other-result")})
	if err != nil {
		t.Fatalf("other publish: %v", err)
	}
	store.putErr = nil
	store.corrupt = true
	corrupt, err := c.Read(t.Context(), manifest, key)
	if err != nil || corrupt.Hit || !corrupt.Reconciled {
		t.Fatalf("corrupt object result=%+v err=%v", corrupt, err)
	}
	store.corrupt = false
	otherRead, err := c.Read(t.Context(), otherManifest, otherKey)
	if err != nil || !otherRead.Hit {
		t.Fatalf("unrelated admitted object was lost: result=%+v err=%v", otherRead, err)
	}

	// Exact retirement leaves the namespace usable and permits a fresh fenced
	// fill without disturbing unrelated admitted manifests.
	newLease, err := c.AcquireFill(t.Context(), key, "owner-retry", 30*time.Second)
	if err != nil {
		t.Fatalf("retry fill after invalidation: %v", err)
	}
	manifest, err = c.Publish(t.Context(), PublishInput{Key: key, Lease: newLease, Body: strings.NewReader("result-2")})
	if err != nil {
		t.Fatalf("republish after invalidation: %v", err)
	}
	delete(store.objects, manifest.ObjectKey)
	read, err := c.Read(context.Background(), manifest, key)
	if err != nil {
		t.Fatalf("missing object read: %v", err)
	}
	if read.Hit || !read.Reconciled {
		t.Fatalf("missing object result = %+v", read)
	}
	if _, found, err := repo.Lookup(t.Context(), key); err != nil || found {
		t.Fatalf("reconciled manifest found=%v err=%v", found, err)
	}

	// An aged orphan is deleted only after the real SQL authority confirms no
	// admitted/retiring manifest or retention root references it.
	orphanDigest := testDigest('f')
	orphanKey, err := c.ObjectKey(key, orphanDigest)
	if err != nil {
		t.Fatal(err)
	}
	store.objects[orphanKey] = memoryObject{info: ObjectInfo{Key: orphanKey, SecurityDomain: testDigest('d'), Digest: orphanDigest, Size: 1, Metadata: []byte(`{}`), CreatedAt: time.Unix(1, 0)}, body: []byte("x")}
	gc, err := c.GC(t.Context())
	if err != nil || gc.Deleted != 1 {
		t.Fatalf("orphan gc result=%+v err=%v", gc, err)
	}
	if _, exists := store.objects[orphanKey]; exists {
		t.Fatal("orphan object survived GC")
	}

	// A snapshot from another security domain is rejected before object access
	// or a PostgreSQL lookup can occur.
	foreign := manifest
	foreign.StorageSecurityDomain = testDigest('e')
	if _, err := c.Read(t.Context(), foreign, key); !errors.Is(err, ErrSecurityDomain) {
		t.Fatalf("cross-domain read error = %v", err)
	}
}
