package sqlite

// This file is the bounded durable-audit conformance seam.  It deliberately
// drives the real SQLite/WAL repository through the Access outbox interface,
// while injecting failures only at the dispatcher/store boundary.  Producer
// packages should use the same shape when they add a durable audit handoff.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform"
)

// auditFailureStore is intentionally small: it can fail before materializing
// an intent (a sink outage), or after materialization (a lost acknowledgement).
// The latter is the important at-least-once case: replay must not duplicate the
// final audit event.
type auditFailureStore struct {
	inner access.AuditOutboxStore

	mu                  sync.Mutex
	completeFailures    int
	lostAcknowledgement bool
}

func (s *auditFailureStore) ClaimAuditIntent(ctx context.Context, owner string, lease time.Duration) (access.AuditIntentLease, bool, error) {
	return s.inner.ClaimAuditIntent(ctx, owner, lease)
}

func (s *auditFailureStore) CompleteAuditIntent(ctx context.Context, lease access.AuditIntentLease) error {
	s.mu.Lock()
	if s.completeFailures > 0 {
		s.completeFailures--
		s.mu.Unlock()
		return errors.New("injected audit sink outage")
	}
	lostAck := s.lostAcknowledgement
	s.lostAcknowledgement = false
	s.mu.Unlock()
	if err := s.inner.CompleteAuditIntent(ctx, lease); err != nil {
		return err
	}
	if lostAck {
		return errors.New("injected lost audit acknowledgement")
	}
	return nil
}

func (s *auditFailureStore) RetryAuditIntent(ctx context.Context, lease access.AuditIntentLease, next time.Time, code string) error {
	return s.inner.RetryAuditIntent(ctx, lease, next, code)
}

func (s *auditFailureStore) PoisonAuditIntent(ctx context.Context, lease access.AuditIntentLease, code string) error {
	return s.inner.PoisonAuditIntent(ctx, lease, code)
}

func (s *auditFailureStore) QuarantineAuditIntent(ctx context.Context, lease access.AuditIntentLease, code string) error {
	return s.inner.QuarantineAuditIntent(ctx, lease, code)
}

func (s *auditFailureStore) RequeueAuditIntent(ctx context.Context, eventID string) error {
	return s.inner.RequeueAuditIntent(ctx, eventID)
}

func (s *auditFailureStore) AuditOutboxStats(ctx context.Context, now time.Time) (access.AuditOutboxStats, error) {
	return s.inner.AuditOutboxStats(ctx, now)
}

func openAuditConformanceStore(t *testing.T, ctx context.Context) (*platform.Store, *Repository, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit-conformance.db")
	store, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatalf("open SQLite/WAL audit fixture: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewRepository(store.SQLDB()), path
}

func conformanceDispatcher(t *testing.T, store access.AuditOutboxStore, maxAttempts int) *access.AuditDispatcher {
	t.Helper()
	dispatcher, err := access.NewAuditDispatcher(access.AuditDispatcherConfig{
		Store: store, PollInterval: time.Millisecond, LeaseDuration: time.Minute,
		BaseRetry: time.Second, MaxRetry: time.Second, MaxAttempts: maxAttempts,
		OwnerFactory: func() string { return "conformance-worker" },
		Now:          func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("construct audit dispatcher: %v", err)
	}
	return dispatcher
}

func countAuditRows(t *testing.T, db *sql.DB, table, eventID string) int {
	t.Helper()
	var count int
	column := "event_id"
	if table == "audit_events" {
		column = "id"
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, eventID).Scan(&count); err != nil {
		t.Fatalf("count %s rows: %v", table, err)
	}
	return count
}

func TestAuditConformanceUsesSQLiteWALAndPhysicalRestart(t *testing.T) {
	ctx := t.Context()
	store, repo, path := openAuditConformanceStore(t, ctx)
	var journalMode string
	if err := store.SQLDB().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("SQLite journal mode = %q, want WAL", journalMode)
	}

	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("restart-event", "aggregate:restart", 1))
	oldLease, found, err := repo.ClaimAuditIntent(ctx, "crashed-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim before restart: found=%v err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close pre-restart store: %v", err)
	}

	restarted, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen SQLite/WAL audit fixture: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedRepo := NewRepository(restarted.SQLDB())
	if _, err := restarted.SQLDB().ExecContext(ctx, `UPDATE audit_outbox SET lease_expires_at = datetime('now', '-1 second') WHERE event_id = ?`, oldLease.Intent.EventID); err != nil {
		t.Fatal(err)
	}
	newLease, found, err := restartedRepo.ClaimAuditIntent(ctx, "restarted-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim after restart: found=%v err=%v", found, err)
	}
	if newLease.LeaseGeneration <= oldLease.LeaseGeneration {
		t.Fatalf("lease generation after restart = %d, old = %d", newLease.LeaseGeneration, oldLease.LeaseGeneration)
	}
	if err := restartedRepo.CompleteAuditIntent(ctx, oldLease); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("stale completion after restart = %v, want ErrAuditIntentFence", err)
	}
	if err := restartedRepo.CompleteAuditIntent(ctx, newLease); err != nil {
		t.Fatalf("complete current post-restart lease: %v", err)
	}
	if got := countAuditRows(t, restarted.SQLDB(), "audit_events", oldLease.Intent.EventID); got != 1 {
		t.Fatalf("post-restart materialized events = %d, want 1", got)
	}
}

func TestAuditConformanceRetriesRealSQLiteAfterSinkOutage(t *testing.T) {
	ctx := t.Context()
	store, repo, _ := openAuditConformanceStore(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("retry-event", "aggregate:retry", 1))
	failing := &auditFailureStore{inner: repo, completeFailures: 1}
	dispatcher := conformanceDispatcher(t, failing, 3)
	if delivered, err := dispatcher.DispatchOne(ctx, "worker"); err != nil || !delivered {
		t.Fatalf("first outage dispatch = delivered %v err %v, want handled retry", delivered, err)
	}
	var state, code string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT state, last_error_code FROM audit_outbox WHERE event_id = 'retry-event'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != string(access.AuditIntentRetry) || code != "AUDIT_SINK_UNAVAILABLE" {
		t.Fatalf("retry state/code = %q/%q, want retry/AUDIT_SINK_UNAVAILABLE", state, code)
	}
	// Advance the durable schedule without sleeping in the conformance lane.
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_outbox SET next_attempt_at = CURRENT_TIMESTAMP WHERE event_id = 'retry-event'`); err != nil {
		t.Fatal(err)
	}
	if delivered, err := dispatcher.DispatchOne(ctx, "worker"); err != nil || !delivered {
		t.Fatalf("recovered dispatch = delivered %v err %v, want delivered", delivered, err)
	}
	if got := countAuditRows(t, store.SQLDB(), "audit_events", "retry-event"); got != 1 {
		t.Fatalf("retry materialized events = %d, want 1", got)
	}
}

func TestAuditConformanceLostAcknowledgementIsIdempotent(t *testing.T) {
	ctx := t.Context()
	store, repo, _ := openAuditConformanceStore(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("lost-ack-event", "aggregate:lost-ack", 1))
	failing := &auditFailureStore{inner: repo, lostAcknowledgement: true}
	dispatcher := conformanceDispatcher(t, failing, 3)
	if delivered, err := dispatcher.DispatchOne(ctx, "worker"); delivered || err == nil {
		t.Fatalf("lost acknowledgement dispatch = delivered %v err %v, want recoverable error", delivered, err)
	}
	var state string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT state FROM audit_outbox WHERE event_id = 'lost-ack-event'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(access.AuditIntentDelivered) {
		t.Fatalf("lost acknowledgement state = %q, want delivered", state)
	}
	if got := countAuditRows(t, store.SQLDB(), "audit_events", "lost-ack-event"); got != 1 {
		t.Fatalf("lost acknowledgement materialized events = %d, want exactly 1", got)
	}
	if delivered, err := dispatcher.DispatchOne(ctx, "worker"); err != nil || delivered {
		t.Fatalf("replay after lost acknowledgement = delivered %v err %v, want no work", delivered, err)
	}
}

func TestAuditConformanceAggregatesOrderIndependently(t *testing.T) {
	ctx := t.Context()
	store, repo, _ := openAuditConformanceStore(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("aggregate-a-2", "aggregate:a", 2))
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("aggregate-b-1", "aggregate:b", 1))
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("aggregate-a-1", "aggregate:a", 1))
	// Make the independent aggregate the first candidate without relying on
	// second-resolution created_at ties or event-ID sort order.
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_outbox SET next_attempt_at = datetime('2000-01-01') WHERE event_id = 'aggregate-b-1'`); err != nil {
		t.Fatal(err)
	}

	first, found, err := repo.ClaimAuditIntent(ctx, "worker-a", time.Minute)
	if err != nil || !found || first.Intent.EventID == "aggregate-a-2" {
		t.Fatalf("first independent claim = %#v found=%v err=%v, must not bypass aggregate-a predecessor", first.Intent, found, err)
	}
	if err := repo.CompleteAuditIntent(ctx, first); err != nil {
		t.Fatalf("complete first independent claim: %v", err)
	}
	second, found, err := repo.ClaimAuditIntent(ctx, "worker-b", time.Minute)
	if err != nil || !found || second.Intent.EventID == "aggregate-a-2" {
		t.Fatalf("second independent claim = %#v found=%v err=%v, must not bypass aggregate-a predecessor", second.Intent, found, err)
	}
	if err := repo.CompleteAuditIntent(ctx, second); err != nil {
		t.Fatalf("complete second independent claim: %v", err)
	}
	third, found, err := repo.ClaimAuditIntent(ctx, "worker-a", time.Minute)
	if err != nil || !found || third.Intent.EventID != "aggregate-a-2" {
		t.Fatalf("aggregate-a successor claim = %#v found=%v err=%v, want aggregate-a-2", third.Intent, found, err)
	}
}

func TestAuditConformanceMalformedSecretPayloadRollsBackMutation(t *testing.T) {
	ctx := t.Context()
	store, repo, _ := openAuditConformanceStore(t, ctx)
	secret := "do-not-persist-this-secret"
	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('malformed-audit', 'malformed@example.test', 'Malformed')`); err != nil {
		t.Fatal(err)
	}
	intent := sqliteAuditIntent("malformed-event", "aggregate:malformed", 1)
	intent.MetadataJSON = fmt.Sprintf(`{"password":"%s"}`, secret)
	err = repo.RecordAuditIntent(ctx, tx, intent)
	if err == nil {
		t.Fatal("secret-bearing payload was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("payload secret leaked into validation error")
	}
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	var mutationCount, outboxCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id = 'malformed-audit'`).Scan(&mutationCount); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE event_id = 'malformed-event'`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if mutationCount != 0 || outboxCount != 0 {
		t.Fatalf("malformed payload rollback mutation/outbox = %d/%d, want 0/0", mutationCount, outboxCount)
	}
}

func TestAuditConformancePoisonAndQuarantineRemainVisible(t *testing.T) {
	ctx := t.Context()
	store, repo, _ := openAuditConformanceStore(t, ctx)

	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("poison-event", "aggregate:poison", 1))
	poisonDispatcher := conformanceDispatcher(t, &auditFailureStore{inner: repo, completeFailures: 1}, 1)
	if delivered, err := poisonDispatcher.DispatchOne(ctx, "poison-worker"); err != nil || !delivered {
		t.Fatalf("poison dispatch = delivered %v err %v, want handled", delivered, err)
	}
	var poisonState, poisonCode string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT state, last_error_code FROM audit_outbox WHERE event_id = 'poison-event'`).Scan(&poisonState, &poisonCode); err != nil {
		t.Fatal(err)
	}
	if poisonState != string(access.AuditIntentPoison) || poisonCode != "AUDIT_SINK_UNAVAILABLE" {
		t.Fatalf("poison state/code = %q/%q, want poison/AUDIT_SINK_UNAVAILABLE", poisonState, poisonCode)
	}

	quarantineIntent := sqliteAuditIntent("quarantine-event", "aggregate:quarantine", 1)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, quarantineIntent)
	// An existing row with the same ID but different payload is an integrity
	// conflict.  It must be quarantined, never silently overwritten.
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO audit_events (id, action, resource_kind, resource_id, capability, status, metadata_json) VALUES (?, 'tampered', 'principal', 'tampered', '', 'success', '{}')`, quarantineIntent.EventID); err != nil {
		t.Fatal(err)
	}
	quarantineDispatcher := conformanceDispatcher(t, repo, 3)
	if delivered, err := quarantineDispatcher.DispatchOne(ctx, "quarantine-worker"); err != nil || !delivered {
		t.Fatalf("quarantine dispatch = delivered %v err %v, want handled", delivered, err)
	}
	var quarantineState, quarantineCode string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT state, last_error_code FROM audit_outbox WHERE event_id = 'quarantine-event'`).Scan(&quarantineState, &quarantineCode); err != nil {
		t.Fatal(err)
	}
	if quarantineState != string(access.AuditIntentQuarantined) || quarantineCode != "AUDIT_INTENT_CONFLICT" {
		t.Fatalf("quarantine state/code = %q/%q, want quarantined/AUDIT_INTENT_CONFLICT", quarantineState, quarantineCode)
	}
}

func TestAuditConformanceDispatcherShutdownIsCancellationSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store, repo, _ := openAuditConformanceStore(t, t.Context())
	insertSQLiteAuditIntent(t, t.Context(), store.SQLDB(), repo, sqliteAuditIntent("shutdown-event", "aggregate:shutdown", 1))
	dispatcher := conformanceDispatcher(t, repo, 3)
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	stopCtx, stopCancel := context.WithTimeout(t.Context(), time.Second)
	defer stopCancel()
	if err := dispatcher.Stop(stopCtx); err != nil {
		t.Fatalf("dispatcher shutdown: %v", err)
	}
	if err := dispatcher.Stop(stopCtx); err != nil {
		t.Fatalf("repeated dispatcher shutdown: %v", err)
	}
}

// brokenPostCommitFixture intentionally commits the mutation before the
// handoff.  The fixture is expected to report an invariant violation; keeping
// this executable makes the forbidden ordering visible in review without
// weakening the production transaction contract.
func brokenPostCommitFixture(ctx context.Context, db *sql.DB, repo *Repository) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('broken-order', 'broken@example.test', 'Broken')`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// The producer crashed between these two commits.  A conformance check,
	// rather than production code, diagnoses the orphaned mutation.
	var outboxCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE event_id = 'broken-order-event'`).Scan(&outboxCount); err != nil {
		return err
	}
	if outboxCount == 0 {
		return errors.New("audit invariant violated: committed mutation has no durable intent")
	}
	return repo.RecordAuditIntent(ctx, db, sqliteAuditIntent("broken-order-event", "aggregate:broken", 1))
}

func TestBrokenPostCommitFixtureDemonstratesInvariantFailure(t *testing.T) {
	ctx := t.Context()
	store, repo, _ := openAuditConformanceStore(t, ctx)
	err := brokenPostCommitFixture(ctx, store.SQLDB(), repo)
	if err == nil || !strings.Contains(err.Error(), "audit invariant violated") {
		t.Fatalf("broken post-commit fixture error = %v, want invariant failure", err)
	}
	var mutationCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id = 'broken-order'`).Scan(&mutationCount); err != nil {
		t.Fatal(err)
	}
	if mutationCount != 1 {
		t.Fatalf("broken fixture mutation count = %d, want 1 to prove post-commit gap", mutationCount)
	}
}
