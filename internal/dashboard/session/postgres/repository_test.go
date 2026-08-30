package postgres

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/session"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
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
	store, err := NewWithTTL(db, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testKey() session.Key {
	return session.Key{ProjectID: "project:session", PrincipalOrClient: "principal:viewer", DashboardID: "dashboard:overview", ServingStateID: "state:1", StreamInstanceID: "stream:1"}
}

func testState() session.State {
	return session.NewState("overview", filter.NewMachine(filter.ApplicationImmediate, map[string]filter.BindingSpec{}).Snapshot())
}

func TestStorePostgreSQL18TTLAndCAS(t *testing.T) {
	store := testStore(t)
	key, state := testKey(), testState()
	record, err := store.Create(t.Context(), key, state)
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != 1 {
		t.Fatalf("initial version = %d", record.Version)
	}
	loaded, err := store.Load(t.Context(), key)
	if err != nil || loaded.Version != 1 {
		t.Fatalf("load = %#v (%v)", loaded, err)
	}
	next := state
	next.ActivePage = "details"
	updated, err := store.CompareAndSwap(t.Context(), key, record.Version, next)
	if err != nil || updated.Version != 2 || updated.State.ActivePage != "details" {
		t.Fatalf("CAS = %#v (%v)", updated, err)
	}
	if _, err := store.CompareAndSwap(t.Context(), key, record.Version, state); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
	store.clock = func() time.Time { return time.Now().Add(10 * time.Minute) }
	if _, err := store.Load(t.Context(), key); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("expired load = %v", err)
	}
}

func TestStorePostgreSQL18ConcurrentCAS(t *testing.T) {
	store := testStore(t)
	key, state := testKey(), testState()
	record, err := store.Create(t.Context(), key, state)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := state
			next.ActivePage = "page"
			_, err := store.CompareAndSwap(t.Context(), key, record.Version, next)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, session.ErrConflict) {
			t.Fatalf("concurrent CAS = %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("CAS successes = %d, want 1", successes)
	}
}

func TestStorePostgreSQL18ExpiredCleanupIsBounded(t *testing.T) {
	store := testStore(t)
	base := time.Now().UTC()
	store.clock = func() time.Time { return base }
	for _, dashboardID := range []string{"dashboard:expired-a", "dashboard:expired-b", "dashboard:expired-c"} {
		key := testKey()
		key.DashboardID = projectgraph.ResourceID(dashboardID)
		if _, err := store.Create(t.Context(), key, testState()); err != nil {
			t.Fatal(err)
		}
	}
	store.clock = func() time.Time { return base.Add(10 * time.Minute) }
	maintenance := NewMaintenance(store.db)
	maintenance.clock = store.clock
	if deleted, err := maintenance.DeleteExpiredBatch(t.Context(), 2); err != nil || deleted != 2 {
		t.Fatalf("bounded expiry batch = %d (%v), want 2", deleted, err)
	}
	if deleted, err := maintenance.DeleteExpiredBatch(t.Context(), 2); err != nil || deleted != 1 {
		t.Fatalf("second expiry batch = %d (%v), want 1", deleted, err)
	}
	if deleted, err := maintenance.DeleteExpiredBatch(t.Context(), 2); err != nil || deleted != 0 {
		t.Fatalf("empty expiry batch = %d (%v), want 0", deleted, err)
	}
	if _, err := maintenance.DeleteExpiredBatch(t.Context(), 0); err == nil {
		t.Fatal("unbounded expiry batch was accepted")
	}
	if _, err := maintenance.DeleteExpiredBatch(t.Context(), 1001); err == nil {
		t.Fatal("oversized expiry batch was accepted")
	}
}
