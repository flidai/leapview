package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
)

func TestAdversarialExpiredLeaseIsQuarantinedInsteadOfReexecuted(t *testing.T) {
	ctx := context.Background()
	db, err := platform.Open(ctx, filepath.Join(t.TempDir(), "idempotency.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewStoreWithSession(db.SQLDB(), "server-1")
	first, execute, err := store.Claim(ctx, "scope", "digest", "worker-1", time.Second, time.Hour)
	if err != nil || !execute {
		t.Fatalf("first claim = %#v %v", first, err)
	}
	if _, err := db.SQLDB().ExecContext(ctx, `UPDATE api_idempotency_records SET lease_expires_at = ? WHERE scope = ?`, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), "scope"); err != nil {
		t.Fatal(err)
	}
	pending, execute, err := store.Claim(ctx, "scope", "digest", "worker-2", time.Minute, time.Hour)
	if err != nil || execute || pending.State != "pending" {
		t.Fatalf("same-process retry = %#v execute=%v err=%v", pending, execute, err)
	}
	replacement := NewStoreWithSession(db.SQLDB(), "server-2")
	quarantined, execute, err := replacement.Claim(ctx, "scope", "digest", "worker-3", time.Minute, time.Hour)
	if err != nil || execute || quarantined.State != "completed" || quarantined.Status != 409 {
		t.Fatalf("restart quarantine = %#v execute=%v err=%v", quarantined, execute, err)
	}
	if err := store.Complete(ctx, "scope", "digest", "worker-1", first.LeaseGeneration, 200, nil, []byte("stale")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion error = %v", err)
	}
	if _, err := store.Renew(ctx, "scope", "digest", "worker-1", first.LeaseGeneration, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale renewal error = %v", err)
	}
	if err := store.Abandon(ctx, "scope", "digest", "worker-1", first.LeaseGeneration); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale abandon error = %v", err)
	}
}

func TestLiveLeaseHeldByAnotherSessionRemainsPending(t *testing.T) {
	ctx := context.Background()
	db, err := platform.Open(ctx, filepath.Join(t.TempDir(), "idempotency.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	first := NewStoreWithSession(db.SQLDB(), "server-1")
	if _, execute, err := first.Claim(ctx, "scope", "digest", "worker-1", time.Minute, time.Hour); err != nil || !execute {
		t.Fatalf("first claim execute=%v err=%v", execute, err)
	}
	second := NewStoreWithSession(db.SQLDB(), "server-2")
	retry, execute, err := second.Claim(ctx, "scope", "digest", "worker-2", time.Minute, time.Hour)
	if err != nil || execute || retry.State != "pending" {
		t.Fatalf("live lease claim = %#v execute=%v err=%v", retry, execute, err)
	}
}
