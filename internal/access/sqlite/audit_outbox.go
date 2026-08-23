package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/transaction"
)

const sqliteTimestampLayout = "2006-01-02 15:04:05"

func (r *Repository) RecordAuditIntent(ctx context.Context, tx transaction.Transaction, intent access.AuditIntent) error {
	if r == nil {
		return fmt.Errorf("access repository is required")
	}
	if tx == nil {
		return fmt.Errorf("audit intent transaction is required")
	}
	// A zero sequence delegates aggregate ordering to Access. This keeps
	// producer repositories from reading the Access-owned outbox while making
	// retries stable by recovering the sequence already assigned to EventID.
	if intent.AggregateSequence == 0 {
		var storedKey string
		var storedSequence int64
		err := tx.QueryRowContext(ctx, `SELECT aggregate_key, aggregate_sequence FROM audit_outbox WHERE event_id = ?`, intent.EventID).Scan(&storedKey, &storedSequence)
		switch {
		case err == nil:
			intent.AggregateKey, intent.AggregateSequence = storedKey, storedSequence
		case errors.Is(err, sql.ErrNoRows):
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(aggregate_sequence), 0) + 1 FROM audit_outbox WHERE aggregate_key = ?`, intent.AggregateKey).Scan(&intent.AggregateSequence); err != nil {
				return fmt.Errorf("allocate audit intent aggregate sequence: %w", err)
			}
		default:
			return fmt.Errorf("resolve audit intent aggregate sequence: %w", err)
		}
	}
	canonical, err := intent.Canonicalize()
	if err != nil {
		return err
	}
	digest, err := canonical.PayloadDigest()
	if err != nil {
		return err
	}
	capacity := r.auditOutboxCapacity
	if capacity <= 0 {
		capacity = access.MaxUndeliveredAuditIntents
	}
	var storedDigest string
	err = tx.QueryRowContext(ctx, `
INSERT INTO audit_outbox
 (event_id, source, operation, principal_id, action, resource_kind, resource_id, capability, outcome,
  request_id, correlation_id, aggregate_key, aggregate_sequence, metadata_json, payload_digest)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE EXISTS (SELECT 1 FROM audit_outbox WHERE event_id = ?)
   OR (SELECT COUNT(*) FROM (
         SELECT 1 FROM audit_outbox WHERE state <> 'delivered' LIMIT ?
       )) < ?
ON CONFLICT(event_id) DO UPDATE SET event_id = excluded.event_id
RETURNING payload_digest`, canonical.EventID, canonical.Source, canonical.Operation, canonical.PrincipalID, canonical.Action,
		canonical.ResourceKind, canonical.ResourceID, canonical.Capability.String(), canonical.Outcome,
		canonical.RequestID, canonical.CorrelationID, canonical.AggregateKey, canonical.AggregateSequence,
		canonical.MetadataJSON, digest, canonical.EventID, capacity, capacity).Scan(&storedDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return access.ErrAuditOutboxCapacity
	}
	if err != nil {
		return fmt.Errorf("record audit intent: %w", err)
	}
	if storedDigest != digest {
		return fmt.Errorf("%w: event %s stored %s received %s", access.ErrAuditIntentConflict, canonical.EventID, storedDigest, digest)
	}
	return nil
}

func (r *Repository) ClaimAuditIntent(ctx context.Context, owner string, lease time.Duration) (access.AuditIntentLease, bool, error) {
	if r == nil || r.root == nil {
		return access.AuditIntentLease{}, false, fmt.Errorf("access repository database is required")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || len(owner) > 256 || lease <= 0 {
		return access.AuditIntentLease{}, false, fmt.Errorf("audit intent owner and positive lease are required")
	}
	modifier := fmt.Sprintf("+%d seconds", max(1, int(lease.Seconds())))
	row := r.root.QueryRowContext(ctx, `
UPDATE audit_outbox
SET state = 'leased', lease_owner = ?, lease_generation = lease_generation + 1,
    lease_expires_at = datetime('now', ?), attempt_count = attempt_count + 1
WHERE event_id = (
  SELECT candidate.event_id
  FROM audit_outbox candidate
  WHERE ((candidate.state IN ('pending', 'retry') AND candidate.next_attempt_at <= CURRENT_TIMESTAMP)
      OR (candidate.state = 'leased' AND (candidate.lease_expires_at IS NULL OR candidate.lease_expires_at <= CURRENT_TIMESTAMP)))
    AND NOT EXISTS (
      SELECT 1 FROM audit_outbox predecessor
      WHERE predecessor.aggregate_key = candidate.aggregate_key
        AND predecessor.aggregate_sequence < candidate.aggregate_sequence
        AND predecessor.state <> 'delivered'
    )
  ORDER BY candidate.next_attempt_at, candidate.created_at, candidate.event_id
  LIMIT 1
)
RETURNING event_id, source, operation, principal_id, action, resource_kind, resource_id, capability, outcome,
 request_id, correlation_id, aggregate_key, aggregate_sequence, metadata_json, state, attempt_count,
 lease_owner, lease_generation, lease_expires_at, created_at`, owner, modifier)
	leaseValue, err := scanAuditIntentLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return access.AuditIntentLease{}, false, nil
	}
	if err != nil {
		return access.AuditIntentLease{}, false, err
	}
	return leaseValue, true, nil
}

func (r *Repository) CompleteAuditIntent(ctx context.Context, lease access.AuditIntentLease) error {
	if r == nil || r.root == nil {
		return fmt.Errorf("access repository database is required")
	}
	canonical, err := lease.Intent.Canonicalize()
	if err != nil {
		return err
	}
	if lease.CreatedAt.IsZero() {
		return fmt.Errorf("audit intent creation time is required")
	}
	createdAt := lease.CreatedAt.UTC().Format(sqliteTimestampLayout)
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `
INSERT INTO audit_events
 (id, principal_id, action, resource_kind, resource_id, capability, status, request_id, correlation_id, metadata_json, created_at)
VALUES (?, CASE WHEN EXISTS (SELECT 1 FROM principals WHERE id = ?) THEN ? ELSE NULL END, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`, canonical.EventID, canonical.PrincipalID, canonical.PrincipalID, canonical.Action,
		canonical.ResourceKind, canonical.ResourceID, canonical.Capability.String(), canonical.Outcome,
		canonical.RequestID, canonical.CorrelationID, canonical.MetadataJSON, createdAt)
	if err != nil {
		return fmt.Errorf("materialize audit intent: %w", err)
	}
	var principal sql.NullString
	var action, kind, resource, capability, outcome, requestID, correlationID, metadata, storedCreatedAt string
	var principalExists bool
	if err := tx.QueryRowContext(ctx, `
SELECT principal_id, action, resource_kind, resource_id, capability, status, request_id, correlation_id, metadata_json, created_at,
       EXISTS (SELECT 1 FROM principals WHERE id = ?)
FROM audit_events WHERE id = ?`, canonical.PrincipalID, canonical.EventID).Scan(&principal, &action, &kind, &resource, &capability, &outcome, &requestID, &correlationID, &metadata, &storedCreatedAt, &principalExists); err != nil {
		return err
	}
	expectedPrincipal := ""
	if principalExists {
		expectedPrincipal = canonical.PrincipalID
	}
	principalMatches := principal.String == expectedPrincipal
	if !principalMatches || action != canonical.Action || kind != canonical.ResourceKind ||
		resource != canonical.ResourceID || capability != canonical.Capability.String() || outcome != canonical.Outcome ||
		requestID != canonical.RequestID || correlationID != canonical.CorrelationID || metadata != canonical.MetadataJSON || storedCreatedAt != createdAt {
		return access.ErrAuditIntentConflict
	}
	result, err := tx.ExecContext(ctx, `
UPDATE audit_outbox
SET state = 'delivered', delivered_at = CURRENT_TIMESTAMP, lease_owner = '', lease_expires_at = NULL, last_error_code = ''
WHERE event_id = ? AND state = 'leased' AND lease_owner = ? AND lease_generation = ?
  AND lease_expires_at > CURRENT_TIMESTAMP`, canonical.EventID, lease.LeaseOwner, lease.LeaseGeneration)
	if err != nil {
		return err
	}
	if err := requireAuditIntentChange(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RetryAuditIntent(ctx context.Context, lease access.AuditIntentLease, next time.Time, code string) error {
	if next.IsZero() {
		return fmt.Errorf("audit intent retry time is required")
	}
	return r.transitionAuditIntent(ctx, lease, access.AuditIntentRetry, next.UTC().Format(sqliteTimestampLayout), code)
}

func (r *Repository) PoisonAuditIntent(ctx context.Context, lease access.AuditIntentLease, code string) error {
	return r.transitionAuditIntent(ctx, lease, access.AuditIntentPoison, time.Now().UTC().Format(sqliteTimestampLayout), code)
}

func (r *Repository) QuarantineAuditIntent(ctx context.Context, lease access.AuditIntentLease, code string) error {
	return r.transitionAuditIntent(ctx, lease, access.AuditIntentQuarantined, time.Now().UTC().Format(sqliteTimestampLayout), code)
}

func (r *Repository) RequeueAuditIntent(ctx context.Context, eventID string) error {
	err := r.RequeueAuditIntentExact(ctx, access.AuditOutboxRequeueRequest{EventID: eventID})
	if errors.Is(err, access.ErrAuditOutboxNotFound) {
		// Preserve the historical state-protection contract for worker/admin
		// callers that only supplied an event identity.
		return access.ErrAuditIntentFence
	}
	return err
}

// RequeueAuditIntentExact performs one terminal-state compare-and-swap. The
// optional expected values let an operator prove that the inspected terminal
// row is still the row being recovered; no payload column is ever writable.
func (r *Repository) RequeueAuditIntentExact(ctx context.Context, request access.AuditOutboxRequeueRequest) error {
	if r == nil || r.root == nil {
		return fmt.Errorf("access repository database is required")
	}
	eventID := strings.TrimSpace(request.EventID)
	if !canonicalAuditIntentEventIDForSQLite(eventID) {
		return fmt.Errorf("audit intent event id is not canonical")
	}
	if request.ExpectedState != "" && request.ExpectedState != access.AuditIntentPoison && request.ExpectedState != access.AuditIntentQuarantined {
		return fmt.Errorf("audit intent expected state must be poison or quarantined")
	}
	if request.ExpectedAttempts != nil && *request.ExpectedAttempts < 0 {
		return fmt.Errorf("audit intent expected attempts cannot be negative")
	}
	if !canonicalAuditIntentFailureCode(request.ExpectedFailureCode) && request.ExpectedFailureCode != "" {
		return fmt.Errorf("audit intent expected failure code is not canonical")
	}
	if request.ExpectedPayloadDigest != "" && !canonicalAuditIntentPayloadDigest(request.ExpectedPayloadDigest) {
		return fmt.Errorf("audit intent expected payload digest is not canonical")
	}
	auditID, err := newID("audit")
	if err != nil {
		return err
	}
	tx, err := r.root.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var previousState, previousFailureCode, previousPayloadDigest string
	var previousAttempts int
	if err := tx.QueryRowContext(ctx, `
SELECT state, attempt_count, last_error_code, payload_digest
FROM audit_outbox WHERE event_id = ?`, eventID).Scan(&previousState, &previousAttempts, &previousFailureCode, &previousPayloadDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.ErrAuditOutboxNotFound
		}
		return err
	}
	expectedAttempts := -1
	if request.ExpectedAttempts != nil {
		expectedAttempts = *request.ExpectedAttempts
	}
	result, err := tx.ExecContext(ctx, `
UPDATE audit_outbox
SET state = 'retry', attempt_count = 0, next_attempt_at = CURRENT_TIMESTAMP,
    lease_owner = '', lease_expires_at = NULL, last_error_code = ''
WHERE event_id = ? AND state IN ('poison', 'quarantined')
  AND (? = '' OR state = ?)
  AND (? < 0 OR attempt_count = ?)
  AND (? = '' OR last_error_code = ?)
  AND (? = '' OR payload_digest = ?)`, eventID,
		string(request.ExpectedState), string(request.ExpectedState), expectedAttempts, expectedAttempts,
		request.ExpectedFailureCode, request.ExpectedFailureCode, request.ExpectedPayloadDigest, request.ExpectedPayloadDigest)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM audit_outbox WHERE event_id = ?`, eventID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return access.ErrAuditOutboxNotFound
		}
		if err != nil {
			return err
		}
		if state != string(access.AuditIntentPoison) && state != string(access.AuditIntentQuarantined) {
			return access.ErrAuditIntentFence
		}
		return access.ErrAuditIntentConflict
	}
	metadata, err := json.Marshal(map[string]any{
		"event_id": eventID, "previous_state": previousState,
		"previous_attempts": previousAttempts, "failure_code": previousFailureCode,
		"payload_digest": previousPayloadDigest,
	})
	if err != nil {
		return fmt.Errorf("encode audit intent recovery: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_events
	 (id, principal_id, action, resource_kind, resource_id, capability, status, metadata_json)
VALUES (?, NULL, 'audit.outbox.requeued', 'audit_outbox_intent', ?, '', 'success', ?)`, auditID, eventID, string(metadata)); err != nil {
		return fmt.Errorf("record audit intent recovery: %w", err)
	}
	return tx.Commit()
}

func (r *Repository) transitionAuditIntent(ctx context.Context, lease access.AuditIntentLease, state access.AuditIntentState, next, code string) error {
	if r == nil || r.root == nil {
		return fmt.Errorf("access repository database is required")
	}
	code = strings.TrimSpace(code)
	if !canonicalAuditIntentFailureCode(code) {
		return fmt.Errorf("audit intent failure code is required")
	}
	result, err := r.root.ExecContext(ctx, `
UPDATE audit_outbox
SET state = ?, next_attempt_at = ?, lease_owner = '', lease_expires_at = NULL, last_error_code = ?
WHERE event_id = ? AND state = 'leased' AND lease_owner = ? AND lease_generation = ?
  AND lease_expires_at > CURRENT_TIMESTAMP`, string(state), next, code, lease.Intent.EventID, lease.LeaseOwner, lease.LeaseGeneration)
	if err != nil {
		return err
	}
	return requireAuditIntentChange(result)
}

func (r *Repository) AuditOutboxStats(ctx context.Context, now time.Time) (access.AuditOutboxStats, error) {
	if r == nil || r.root == nil {
		return access.AuditOutboxStats{}, fmt.Errorf("access repository database is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stats := access.AuditOutboxStats{}
	var oldest string
	err := r.root.QueryRowContext(ctx, `
SELECT
 COALESCE(SUM(state = 'pending'), 0), COALESCE(SUM(state = 'retry'), 0),
 COALESCE(SUM(state = 'leased'), 0), COALESCE(SUM(state = 'delivered'), 0),
 COALESCE(SUM(state = 'poison'), 0), COALESCE(SUM(state = 'quarantined'), 0),
 COALESCE(SUM(attempt_count), 0),
 COALESCE(MIN(CASE WHEN state <> 'delivered' THEN created_at END), '')
FROM audit_outbox`).Scan(&stats.Pending, &stats.Retry, &stats.Leased, &stats.Delivered, &stats.Poison, &stats.Quarantined, &stats.AttemptCount, &oldest)
	if err != nil {
		return access.AuditOutboxStats{}, err
	}
	if oldest != "" {
		created, parseErr := time.ParseInLocation(sqliteTimestampLayout, oldest, time.UTC)
		if parseErr != nil {
			return access.AuditOutboxStats{}, parseErr
		}
		if now.After(created) {
			stats.OldestUndeliveredAge = now.Sub(created)
		}
	}
	stats.Capacity = int64(r.auditOutboxCapacity)
	if stats.Capacity <= 0 {
		stats.Capacity = access.MaxUndeliveredAuditIntents
	}
	undelivered := stats.Pending + stats.Retry + stats.Leased + stats.Poison + stats.Quarantined
	stats.CapacityRemaining = stats.Capacity - undelivered
	if stats.CapacityRemaining < 0 {
		stats.CapacityRemaining = 0
	}
	return stats, nil
}

// InspectAuditOutbox returns aggregate state and a bounded terminal index.
// It is intentionally read-only and never selects metadata_json or other
// payload-bearing columns.
func (r *Repository) InspectAuditOutbox(ctx context.Context, now time.Time, limit int) (access.AuditOutboxInspection, error) {
	if r == nil || r.root == nil {
		return access.AuditOutboxInspection{}, fmt.Errorf("access repository database is required")
	}
	if limit <= 0 || limit > access.MaxAuditOutboxInspectionRows {
		limit = access.MaxAuditOutboxInspectionRows
	}
	stats, err := r.AuditOutboxStats(ctx, now)
	if err != nil {
		return access.AuditOutboxInspection{}, err
	}
	rows, err := r.root.QueryContext(ctx, `
SELECT event_id, state, attempt_count, last_error_code, payload_digest,
       aggregate_key, aggregate_sequence, lease_generation, created_at
FROM audit_outbox
WHERE state IN ('poison', 'quarantined')
ORDER BY created_at, event_id
LIMIT ?`, limit+1)
	if err != nil {
		return access.AuditOutboxInspection{}, err
	}
	defer rows.Close()
	inspection := access.AuditOutboxInspection{Stats: stats}
	for rows.Next() {
		var item access.AuditOutboxTerminalIntent
		var state, createdAt string
		if err := rows.Scan(&item.EventID, &state, &item.AttemptCount, &item.LastErrorCode, &item.PayloadDigest,
			&item.AggregateKey, &item.AggregateSequence, &item.LeaseGeneration, &createdAt); err != nil {
			return access.AuditOutboxInspection{}, err
		}
		item.State = access.AuditIntentState(state)
		item.CreatedAt, err = time.ParseInLocation(sqliteTimestampLayout, createdAt, time.UTC)
		if err != nil {
			return access.AuditOutboxInspection{}, err
		}
		if len(inspection.Terminals) == limit {
			inspection.Truncated = true
			break
		}
		inspection.Terminals = append(inspection.Terminals, item)
	}
	if err := rows.Err(); err != nil {
		return access.AuditOutboxInspection{}, err
	}
	return inspection, nil
}

type auditIntentScanner interface{ Scan(...any) error }

func scanAuditIntentLease(row auditIntentScanner) (access.AuditIntentLease, error) {
	var lease access.AuditIntentLease
	var capability, state, leaseExpiresAt, createdAt string
	err := row.Scan(&lease.Intent.EventID, &lease.Intent.Source, &lease.Intent.Operation, &lease.Intent.PrincipalID,
		&lease.Intent.Action, &lease.Intent.ResourceKind, &lease.Intent.ResourceID, &capability, &lease.Intent.Outcome,
		&lease.Intent.RequestID, &lease.Intent.CorrelationID, &lease.Intent.AggregateKey, &lease.Intent.AggregateSequence,
		&lease.Intent.MetadataJSON, &state, &lease.AttemptCount, &lease.LeaseOwner, &lease.LeaseGeneration,
		&leaseExpiresAt, &createdAt)
	if err != nil {
		return access.AuditIntentLease{}, err
	}
	lease.Intent.Capability = access.Capability(capability)
	lease.State = access.AuditIntentState(state)
	lease.LeaseExpiresAt, err = time.ParseInLocation(sqliteTimestampLayout, leaseExpiresAt, time.UTC)
	if err != nil {
		return access.AuditIntentLease{}, err
	}
	lease.CreatedAt, err = time.ParseInLocation(sqliteTimestampLayout, createdAt, time.UTC)
	return lease, err
}

func requireAuditIntentChange(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return access.ErrAuditIntentFence
	}
	return nil
}

func canonicalAuditIntentEventIDForSQLite(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:/-", char) {
			continue
		}
		return false
	}
	return true
}

func canonicalAuditIntentPayloadDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range value[len("sha256:"):] {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return false
	}
	return true
}

func canonicalAuditIntentFailureCode(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("._:-", char) {
			continue
		}
		return false
	}
	return true
}
