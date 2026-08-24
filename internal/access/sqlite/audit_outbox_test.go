package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

func sqliteAuditIntent(eventID, aggregate string, sequence int64) access.AuditIntent {
	return access.AuditIntent{
		EventID:           eventID,
		Source:            "access",
		Operation:         "principal.mutation",
		PrincipalID:       "audit-principal",
		Action:            "principal.changed",
		ResourceKind:      "principal",
		ResourceID:        "audit-principal",
		Capability:        access.CapabilityResourceManage,
		Outcome:           "success",
		RequestID:         "request-" + eventID,
		CorrelationID:     "correlation-" + eventID,
		AggregateKey:      aggregate,
		AggregateSequence: sequence,
		MetadataJSON:      `{"z":2,"a":1}`,
	}
}

func insertSQLiteAuditIntent(t *testing.T, ctx context.Context, db *sql.DB, repo *Repository, intent access.AuditIntent) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repo.RecordAuditIntent(ctx, tx, intent); err != nil {
		t.Fatalf("record audit intent %s: %v", intent.EventID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertSQLitePrincipal(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, id, id+"@example.test", id); err != nil {
		t.Fatalf("insert principal %s: %v", id, err)
	}
}

func TestRecordAuditIntentParticipatesInCallerTransaction(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('tx-commit', 'tx@example.test', 'Tx')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAuditIntent(ctx, tx, sqliteAuditIntent("tx-commit-event", "principal:tx-commit", 1)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE event_id = 'tx-commit-event'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed outbox rows = %d, want 1", count)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM principals WHERE id = 'tx-commit'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("committed mutation rows = %d, want 1", count)
	}

	tx, err = store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('tx-rollback', 'rollback@example.test', 'Rollback')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAuditIntent(ctx, tx, sqliteAuditIntent("tx-rollback-event", "principal:tx-rollback", 1)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for query, label := range map[string]string{
		`SELECT COUNT(*) FROM audit_outbox WHERE event_id = 'tx-rollback-event'`: "outbox",
		`SELECT COUNT(*) FROM principals WHERE id = 'tx-rollback'`:               "mutation",
	} {
		if err := store.SQLDB().QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rolled-back %s rows = %d, want 0", label, count)
		}
	}
}

func TestRecordAuditIntentSameIDIsIdempotentAndConflictsOnPayloadChange(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	first := sqliteAuditIntent("same-id", "principal:same-id", 1)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, first)

	// Metadata formatting is not part of identity after canonicalization.
	same := first
	same.MetadataJSON = ` { "a": 1, "z": 2 } `
	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAuditIntent(ctx, tx, same); err != nil {
		t.Fatalf("same payload replay: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE event_id = 'same-id'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent row count = %d, want 1", count)
	}

	conflict := first
	conflict.Action = "principal.deleted"
	tx, err = store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = repo.RecordAuditIntent(ctx, tx, conflict)
	if !errors.Is(err, access.ErrAuditIntentConflict) {
		t.Fatalf("payload conflict = %v, want ErrAuditIntentConflict", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var action string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT action FROM audit_outbox WHERE event_id = 'same-id'`).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != first.Action {
		t.Fatalf("stored action = %q, want original %q", action, first.Action)
	}
}

func TestRecordAuditIntentAllocatesReplayStableAggregateSequence(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("allocated-first", "aggregate:allocated", 1))

	allocated := sqliteAuditIntent("allocated-second", "aggregate:allocated", 0)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, allocated)
	// A retry presents the same source intent with its sequence still delegated
	// to Access. It must resolve to the already-persisted value, not allocate a
	// conflicting successor.
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, allocated)

	var sequence, rows int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT aggregate_sequence FROM audit_outbox WHERE event_id = ?`, allocated.EventID).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE aggregate_key = ?`, allocated.AggregateKey).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || rows != 2 {
		t.Fatalf("allocated sequence=%d rows=%d, want sequence=2 rows=2", sequence, rows)
	}

	conflict := allocated
	conflict.AggregateKey = "aggregate:different"
	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repo.RecordAuditIntent(ctx, tx, conflict); !errors.Is(err, access.ErrAuditIntentConflict) {
		t.Fatalf("changed aggregate replay = %v, want ErrAuditIntentConflict", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var storedAggregate string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT aggregate_key FROM audit_outbox WHERE event_id = ?`, allocated.EventID).Scan(&storedAggregate); err != nil {
		t.Fatal(err)
	}
	if storedAggregate != allocated.AggregateKey {
		t.Fatalf("stored aggregate = %q, want original %q", storedAggregate, allocated.AggregateKey)
	}
}

func TestRecordAuditIntentCapacityFailsClosedButAllowsIdempotentReplay(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	repo.auditOutboxCapacity = 1
	first := sqliteAuditIntent("capacity-first", "capacity:first", 1)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, first)

	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repo.RecordAuditIntent(ctx, tx, first); err != nil {
		t.Fatalf("idempotent replay at capacity: %v", err)
	}
	if err := repo.RecordAuditIntent(ctx, tx, sqliteAuditIntent("capacity-second", "capacity:second", 1)); !errors.Is(err, access.ErrAuditOutboxCapacity) {
		t.Fatalf("new intent at capacity = %v, want ErrAuditOutboxCapacity", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox rows after capacity rejection = %d, want 1", count)
	}
}

func TestClaimAuditIntentEnforcesAggregateOrdering(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("aggregate-2", "aggregate:ordered", 2))
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("aggregate-1", "aggregate:ordered", 1))

	first, found, err := repo.ClaimAuditIntent(ctx, "worker-1", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim predecessor: found=%v err=%v", found, err)
	}
	if first.Intent.EventID != "aggregate-1" || first.Intent.AggregateSequence != 1 {
		t.Fatalf("first lease = %#v, want sequence 1", first.Intent)
	}
	if _, found, err := repo.ClaimAuditIntent(ctx, "worker-2", time.Minute); err != nil || found {
		t.Fatalf("claimed successor before predecessor completion: found=%v err=%v", found, err)
	}
	if err := repo.CompleteAuditIntent(ctx, first); err != nil {
		t.Fatalf("complete predecessor: %v", err)
	}
	second, found, err := repo.ClaimAuditIntent(ctx, "worker-2", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim successor: found=%v err=%v", found, err)
	}
	if second.Intent.EventID != "aggregate-2" || second.Intent.AggregateSequence != 2 {
		t.Fatalf("successor lease = %#v, want sequence 2", second.Intent)
	}
}

func TestAuditIntentLeaseExpiryReclaimsAndFencesStaleWorker(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("lease-event", "aggregate:lease", 1))

	oldLease, found, err := repo.ClaimAuditIntent(ctx, "old-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim old lease: found=%v err=%v", found, err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_outbox SET lease_expires_at = datetime('now', '-1 second') WHERE event_id = 'lease-event'`); err != nil {
		t.Fatal(err)
	}
	newLease, found, err := repo.ClaimAuditIntent(ctx, "new-worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("reclaim expired lease: found=%v err=%v", found, err)
	}
	if newLease.LeaseGeneration <= oldLease.LeaseGeneration || newLease.AttemptCount <= oldLease.AttemptCount {
		t.Fatalf("reclaimed lease generation/attempt = %d/%d, old = %d/%d", newLease.LeaseGeneration, newLease.AttemptCount, oldLease.LeaseGeneration, oldLease.AttemptCount)
	}
	if err := repo.CompleteAuditIntent(ctx, oldLease); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("stale completion = %v, want ErrAuditIntentFence", err)
	}
	if err := repo.RetryAuditIntent(ctx, oldLease, time.Now().Add(time.Minute), "stale"); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("stale retry = %v, want ErrAuditIntentFence", err)
	}
	if err := repo.PoisonAuditIntent(ctx, oldLease, "stale"); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("stale poison = %v, want ErrAuditIntentFence", err)
	}
	if err := repo.QuarantineAuditIntent(ctx, oldLease, "stale"); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("stale quarantine = %v, want ErrAuditIntentFence", err)
	}
	if err := repo.CompleteAuditIntent(ctx, newLease); err != nil {
		t.Fatalf("complete current lease: %v", err)
	}
}

func TestCompleteAuditIntentMaterializesExactlyOnceAcrossRepositoryRestart(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLitePrincipal(t, ctx, store.SQLDB(), "audit-principal")
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("materialize-once", "aggregate:materialize", 1))
	lease, found, err := repo.ClaimAuditIntent(ctx, "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim materialization: found=%v err=%v", found, err)
	}

	restarted := NewRepository(store.SQLDB())
	if err := restarted.CompleteAuditIntent(ctx, lease); err != nil {
		t.Fatalf("complete after repository restart: %v", err)
	}
	if err := repo.CompleteAuditIntent(ctx, lease); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("replayed completion = %v, want ErrAuditIntentFence", err)
	}
	var outboxCount, eventCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE event_id = 'materialize-once'`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE id = 'materialize-once'`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 || eventCount != 1 {
		t.Fatalf("materialized outbox/events = %d/%d, want 1/1", outboxCount, eventCount)
	}
	var intentCreatedAt, eventCreatedAt string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT created_at FROM audit_outbox WHERE event_id = 'materialize-once'`).Scan(&intentCreatedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT created_at FROM audit_events WHERE id = 'materialize-once'`).Scan(&eventCreatedAt); err != nil {
		t.Fatal(err)
	}
	if eventCreatedAt != intentCreatedAt {
		t.Fatalf("materialized event time = %q, want mutation time %q", eventCreatedAt, intentCreatedAt)
	}
}

func TestCompleteAuditIntentHandlesPrincipalDeletionAndMissingPrincipal(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLitePrincipal(t, ctx, store.SQLDB(), "audit-principal")
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("deleted-principal", "aggregate:deleted", 1))
	lease, found, err := repo.ClaimAuditIntent(ctx, "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim deleted principal event: found=%v err=%v", found, err)
	}
	if err := repo.CompleteAuditIntent(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `DELETE FROM principals WHERE id = 'audit-principal'`); err != nil {
		t.Fatal(err)
	}
	var principal sql.NullString
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT principal_id FROM audit_events WHERE id = 'deleted-principal'`).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	if principal.Valid {
		t.Fatalf("deleted principal retained in audit event: %q", principal.String)
	}
	var outboxPrincipal string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT principal_id FROM audit_outbox WHERE event_id = 'deleted-principal'`).Scan(&outboxPrincipal); err != nil {
		t.Fatal(err)
	}
	if outboxPrincipal != "audit-principal" {
		t.Fatalf("outbox principal = %q, want durable original identity", outboxPrincipal)
	}

	missing := sqliteAuditIntent("missing-principal", "aggregate:missing", 1)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, missing)
	missingLease, found, err := repo.ClaimAuditIntent(ctx, "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim missing principal event: found=%v err=%v", found, err)
	}
	if missingLease.Intent.EventID != missing.EventID {
		t.Fatalf("claimed event = %q, want %q", missingLease.Intent.EventID, missing.EventID)
	}
	if err := repo.CompleteAuditIntent(ctx, missingLease); err != nil {
		t.Fatalf("complete missing principal event: %v", err)
	}
	var eventPrincipal sql.NullString
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT principal_id FROM audit_events WHERE id = 'missing-principal'`).Scan(&eventPrincipal); err != nil {
		t.Fatal(err)
	}
	if eventPrincipal.Valid {
		t.Fatalf("missing principal retained in audit event: %q", eventPrincipal.String)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT principal_id FROM audit_outbox WHERE event_id = 'missing-principal'`).Scan(&outboxPrincipal); err != nil {
		t.Fatal(err)
	}
	if outboxPrincipal != "audit-principal" {
		t.Fatalf("missing-principal outbox principal = %q, want durable original identity", outboxPrincipal)
	}
	var state string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT state FROM audit_outbox WHERE event_id = 'missing-principal'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(access.AuditIntentDelivered) {
		t.Fatalf("missing-principal outbox state = %q, want delivered", state)
	}
	if _, found, err := repo.ClaimAuditIntent(ctx, "replay-worker", time.Minute); err != nil || found {
		t.Fatalf("reclaimed delivered principal event: found=%v err=%v", found, err)
	}
}

func TestAuditOutboxStatsCountsStatesAndOldestUndeliveredAge(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLitePrincipal(t, ctx, store.SQLDB(), "audit-principal")
	for _, eventID := range []string{"stats-pending", "stats-retry", "stats-leased", "stats-delivered", "stats-poison", "stats-quarantined"} {
		insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent(eventID, "aggregate:"+eventID, 1))
	}
	// Give each intended claim a deterministic order independent of SQLite's
	// second-resolution created_at timestamps.
	for sequence, eventID := range []string{"stats-retry", "stats-leased", "stats-delivered", "stats-poison", "stats-quarantined", "stats-pending"} {
		if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_outbox SET next_attempt_at = datetime('2000-01-01', ?) WHERE event_id = ?`, fmt.Sprintf("+%d seconds", sequence), eventID); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(eventID, owner string) access.AuditIntentLease {
		t.Helper()
		lease, found, err := repo.ClaimAuditIntent(ctx, owner, time.Minute)
		if err != nil || !found || lease.Intent.EventID != eventID {
			t.Fatalf("claim %s: lease=%#v found=%v err=%v", eventID, lease, found, err)
		}
		return lease
	}
	if err := repo.RetryAuditIntent(ctx, claim("stats-retry", "retry-worker"), time.Now().Add(time.Hour), "SINK_RETRY"); err != nil {
		t.Fatal(err)
	}
	leased := claim("stats-leased", "leased-worker")
	if leased.State != access.AuditIntentLeased {
		t.Fatalf("leased state = %q, want leased", leased.State)
	}
	if err := repo.CompleteAuditIntent(ctx, claim("stats-delivered", "complete-worker")); err != nil {
		t.Fatal(err)
	}
	if err := repo.PoisonAuditIntent(ctx, claim("stats-poison", "poison-worker"), "POISON"); err != nil {
		t.Fatal(err)
	}
	quarantine := claim("stats-quarantined", "quarantine-worker")
	if err := repo.QuarantineAuditIntent(ctx, quarantine, "QUARANTINED"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_outbox SET created_at = '2026-08-20 00:00:00' WHERE event_id = 'stats-pending'`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	stats, err := repo.AuditOutboxStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Retry != 1 || stats.Leased != 1 || stats.Delivered != 1 || stats.Poison != 1 || stats.Quarantined != 1 {
		t.Fatalf("stats = %#v, want one row in every state", stats)
	}
	wantAge := 3*24*time.Hour + 12*time.Hour
	if stats.OldestUndeliveredAge != wantAge {
		t.Fatalf("oldest undelivered age = %s, want %s", stats.OldestUndeliveredAge, wantAge)
	}
}

func TestRequeueAuditIntentResetsTerminalStateAndRecordsRecoveryAudit(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("requeue-event", "aggregate:requeue", 1))
	lease, found, err := repo.ClaimAuditIntent(ctx, "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if err := repo.PoisonAuditIntent(ctx, lease, "SINK_POISON"); err != nil {
		t.Fatal(err)
	}
	if err := repo.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{EventID: "requeue-event"}); err != nil {
		t.Fatalf("requeue: %v", err)
	}
	var state, owner, errorCode string
	var attempts int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT state, attempt_count, lease_owner, last_error_code FROM audit_outbox WHERE event_id = 'requeue-event'`).Scan(&state, &attempts, &owner, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != string(access.AuditIntentRetry) || attempts != 0 || owner != "" || errorCode != "" {
		t.Fatalf("requeued state = %q attempts=%d owner=%q code=%q, want retry/0/empty/empty", state, attempts, owner, errorCode)
	}
	var recoveryCount int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'audit.outbox.requeued' AND resource_kind = 'audit_outbox_intent' AND resource_id = 'requeue-event'`).Scan(&recoveryCount); err != nil {
		t.Fatal(err)
	}
	if recoveryCount != 1 {
		t.Fatalf("requeue recovery audit rows = %d, want 1", recoveryCount)
	}
	if err := repo.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{EventID: "requeue-event"}); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("requeue nonterminal event = %v, want ErrAuditIntentFence", err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'audit.outbox.requeued' AND resource_id = 'requeue-event'`).Scan(&recoveryCount); err != nil {
		t.Fatal(err)
	}
	if recoveryCount != 1 {
		t.Fatalf("duplicate requeue recovery audit rows = %d, want 1", recoveryCount)
	}
}

func TestInspectAuditOutboxProvidesBoundedTerminalFactsAndExactRecoveryCAS(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	intent := sqliteAuditIntent("inspect-terminal", "aggregate:inspect", 1)
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, intent)
	lease, found, err := repo.ClaimAuditIntent(ctx, "worker", time.Minute)
	if err != nil || !found {
		t.Fatalf("claim: found=%v err=%v", found, err)
	}
	if err := repo.PoisonAuditIntent(ctx, lease, "AUDIT_SINK_UNAVAILABLE"); err != nil {
		t.Fatal(err)
	}
	inspection, err := repo.InspectAuditOutbox(ctx, time.Now().UTC(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Terminals) != 1 || inspection.Truncated {
		t.Fatalf("terminal inspection = %#v truncated=%v", inspection.Terminals, inspection.Truncated)
	}
	terminal := inspection.Terminals[0]
	digest, err := intent.PayloadDigest()
	if err != nil {
		t.Fatal(err)
	}
	if terminal.EventID != intent.EventID || terminal.State != access.AuditIntentPoison ||
		terminal.AttemptCount != lease.AttemptCount || terminal.LastErrorCode != "AUDIT_SINK_UNAVAILABLE" ||
		terminal.PayloadDigest != digest {
		t.Fatalf("terminal facts = %#v", terminal)
	}
	wrongAttempts := terminal.AttemptCount + 1
	if err := repo.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{
		EventID: intent.EventID, ExpectedState: access.AuditIntentPoison,
		ExpectedAttempts: &wrongAttempts, ExpectedFailureCode: terminal.LastErrorCode,
		ExpectedPayloadDigest: terminal.PayloadDigest,
	}); !errors.Is(err, access.ErrAuditIntentConflict) {
		t.Fatalf("mismatched exact recovery = %v, want conflict", err)
	}
	attempts := terminal.AttemptCount
	if err := repo.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{
		EventID: intent.EventID, ExpectedState: access.AuditIntentPoison,
		ExpectedAttempts: &attempts, ExpectedFailureCode: terminal.LastErrorCode,
		ExpectedPayloadDigest: terminal.PayloadDigest,
	}); err != nil {
		t.Fatalf("exact recovery: %v", err)
	}
	if err := repo.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{EventID: intent.EventID, ExpectedState: access.AuditIntentPoison}); !errors.Is(err, access.ErrAuditIntentFence) {
		t.Fatalf("repeated exact recovery = %v, want stale fence", err)
	}
	var metadata string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT metadata_json FROM audit_events WHERE action = 'audit.outbox.requeued' AND resource_id = ?`, intent.EventID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"AUDIT_SINK_UNAVAILABLE", digest, "inspect-terminal"} {
		if !strings.Contains(metadata, fragment) {
			t.Fatalf("recovery metadata %q missing %q", metadata, fragment)
		}
	}
}

func TestAuditOutboxRejectsInvalidClaimsAndTransitions(t *testing.T) {
	ctx := t.Context()
	store, repo := openAccessRepo(t, ctx)
	if _, found, err := repo.ClaimAuditIntent(ctx, "", time.Minute); err == nil || found {
		t.Fatalf("empty owner claim = found %v err %v, want validation error", found, err)
	}
	if _, found, err := repo.ClaimAuditIntent(ctx, "worker", 0); err == nil || found {
		t.Fatalf("zero lease claim = found %v err %v, want validation error", found, err)
	}
	insertSQLiteAuditIntent(t, ctx, store.SQLDB(), repo, sqliteAuditIntent("transition-validation", "aggregate:transition", 1))
	lease, found, err := repo.ClaimAuditIntent(ctx, "worker", time.Minute)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err := repo.RetryAuditIntent(ctx, lease, time.Time{}, "code"); err == nil {
		t.Fatal("zero retry time accepted")
	}
	if err := repo.PoisonAuditIntent(ctx, lease, " "); err == nil {
		t.Fatal("empty poison code accepted")
	}
}
