package postgres

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/http/idempotency"
	"github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "http_idempotency_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return NewStoreWithConfig(p, 100*time.Millisecond, time.Hour), p
}

func TestNewStoreFromRepositoryPreservesOperationIdentity(t *testing.T) {
	repository := postgres.New(nil)
	store := NewStoreFromRepository(repository)
	if !store.Matches(repository) {
		t.Fatal("HTTP idempotency adapter did not retain the supplied operation repository")
	}
	if store.Matches(postgres.New(nil)) {
		t.Fatal("HTTP idempotency adapter accepted a distinct operation repository")
	}
	if NewStoreFromRepository(nil).Matches(repository) {
		t.Fatal("HTTP idempotency adapter matched a nil operation repository")
	}
	var nilStore *Store
	if nilStore.Matches(repository) {
		t.Fatal("nil HTTP idempotency adapter matched an operation repository")
	}
}

func TestClaimReplayAndExactConflict(t *testing.T) {
	s, _ := testStore(t)
	first, execute, err := s.Claim(t.Context(), "principal:path:key", strings.Repeat("a", 64), "owner-1", time.Second, time.Hour)
	if err != nil || !execute || first.State != "pending" {
		t.Fatalf("first claim=%#v execute=%t err=%v", first, execute, err)
	}
	if err := s.Complete(t.Context(), "principal:path:key", strings.Repeat("a", 64), "owner-1", first.LeaseGeneration, http.StatusCreated, http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	replay, execute, err := s.Claim(t.Context(), "principal:path:key", strings.Repeat("a", 64), "owner-2", time.Second, time.Hour)
	if err != nil || execute || replay.Status != http.StatusCreated || string(replay.Body) != `{"ok":true}` {
		t.Fatalf("replay=%#v execute=%t err=%v", replay, execute, err)
	}
	conflict, execute, err := s.Claim(t.Context(), "principal:path:key", strings.Repeat("b", 64), "owner-2", time.Second, time.Hour)
	if err != nil || execute || conflict.Digest == strings.Repeat("b", 64) {
		t.Fatalf("conflict=%#v execute=%t err=%v", conflict, execute, err)
	}
}

func TestExpiredTakeoverFencesOldHTTPOwner(t *testing.T) {
	s, _ := testStore(t)
	first, execute, err := s.Claim(t.Context(), "expiring", strings.Repeat("a", 64), "owner-1", 250*time.Millisecond, time.Hour)
	if err != nil || !execute {
		t.Fatalf("first claim=%#v execute=%t err=%v", first, execute, err)
	}
	beforeExpiry, execute, err := s.Claim(t.Context(), "expiring", strings.Repeat("a", 64), "owner-2", time.Second, time.Hour)
	if err != nil || execute || beforeExpiry.State != "pending" || beforeExpiry.LeaseGeneration != first.LeaseGeneration {
		t.Fatalf("before-expiry claim=%#v execute=%t err=%v", beforeExpiry, execute, err)
	}
	time.Sleep(350 * time.Millisecond)
	next, execute, err := s.Claim(t.Context(), "expiring", strings.Repeat("a", 64), "owner-2", time.Second, time.Hour)
	if err != nil || !execute || next.State != "pending" || next.LeaseGeneration != first.LeaseGeneration+1 {
		t.Fatalf("takeover=%#v execute=%t err=%v", next, execute, err)
	}
	if err := s.Complete(t.Context(), "expiring", strings.Repeat("a", 64), "owner-1", first.LeaseGeneration, http.StatusOK, nil, []byte(`{"old":true}`)); !errors.Is(err, idempotency.ErrLeaseLost) {
		t.Fatalf("stale completion=%v, want lease lost", err)
	}
	if err := s.Complete(t.Context(), "expiring", strings.Repeat("a", 64), "owner-2", next.LeaseGeneration, http.StatusOK, nil, []byte(`{"new":true}`)); err != nil {
		t.Fatalf("new owner completion=%v", err)
	}
}
