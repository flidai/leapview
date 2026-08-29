// Package postgres implements the direct PostgreSQL audit append boundary.
//
// The repository is intentionally stateless.  A producer supplies its native
// pgx transaction, so a source mutation and its audit row have exactly one
// commit/rollback boundary.  This package does not materialize an audit
// intent, open a second connection, or commit the caller's transaction.
package postgres

import (
	"context"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed schema.sql
var schemaSQL string

// Tx is the native pgx transaction surface required by RecordAuditEvent.
// pgx.Tx and pgxpool.Tx satisfy it.  Commit and Rollback are deliberately not
// part of this interface: ownership remains with the source capability.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// DBTX is the native pgx surface accepted by access methods. It is deliberately
// small so a pool, connection, or caller-owned transaction can be supplied.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// Repository is the stateful access authority. It always carries a native
// PostgreSQL connection and an explicitly configured fingerprint key.
type Repository struct {
	db             DBTX
	fingerprintKey []byte
}

// AuditRepository is the stateless transaction-bound audit appender.
type AuditRepository struct{}

// New returns the direct immutable audit appender. Source mutations must pass
// their caller-owned pgx transaction to RecordAuditEvent.
func New() *AuditRepository { return &AuditRepository{} }

// FingerprintConfig supplies the HMAC key used to index bearer secrets. A
// missing or short key is rejected; no environment or development fallback is
// consulted.
type FingerprintConfig struct{ Key []byte }

// NewAccess constructs the PostgreSQL access authority. Transactions are
// required for multi-write operations and are obtained from the supplied
// connection pool/connection.
func NewAccess(db DBTX, cfg FingerprintConfig) (*Repository, error) {
	if db == nil {
		return nil, errors.New("access PostgreSQL database is required")
	}
	if len(cfg.Key) < 32 {
		return nil, errors.New("access fingerprint key must be at least 32 bytes")
	}
	return &Repository{db: db, fingerprintKey: append([]byte(nil), cfg.Key...)}, nil
}

// ApplySchema installs the clean access baseline in a caller-owned
// PostgreSQL transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("access PostgreSQL transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

// SchemaSQL returns the standalone access schema for migration runners.
func SchemaSQL() string { return schemaSQL }

// Event is the persisted audit row. IntentDigest is the digest of the
// canonical access.AuditIntent and binds every immutable identity/payload
// field, including the fields added by this capability to the baseline table.
type Event struct {
	AuditID           string
	ScopeID           string
	PrincipalID       string
	Source            string
	Operation         string
	Action            string
	ResourceKind      string
	ResourceID        string
	Capability        access.Capability
	Outcome           string
	RequestID         string
	CorrelationID     string
	AggregateKey      string
	AggregateSequence int64
	MetadataJSON      string
	OccurredAt        time.Time
	IntentDigest      string
}

const (
	// The baseline audit table enforces the same textual boundary. Applying it
	// before SQL keeps oversized payloads fail-closed and deterministic.
	maxAuditMetadataBytes = 32768
	maxAuditReadRows      = 1000
)

// RecordAuditEvent validates and appends one immutable audit row directly in
// tx.  EventID is the retry identity and must be a UUID because the clean
// PostgreSQL baseline stores audit_id as uuid.  Repeating the same EventID
// with the same canonical intent is idempotent; any changed identity or
// payload returns access.ErrAuditIntentConflict.
//
// The method never begins, commits, or rolls back tx.
func (r *AuditRepository) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) (Event, error) {
	if tx == nil {
		return Event{}, errors.New("audit PostgreSQL transaction is nil")
	}
	if ctx == nil {
		return Event{}, errors.New("audit context is nil")
	}

	canonical, err := intent.Canonicalize()
	if err != nil {
		return Event{}, err
	}
	canonical.EventID, err = canonicalUUID("audit event id", canonical.EventID)
	if err != nil {
		return Event{}, err
	}
	if canonical.PrincipalID != "" {
		canonical.PrincipalID, err = canonicalUUID("audit principal id", canonical.PrincipalID)
		if err != nil {
			return Event{}, err
		}
	}
	if canonical.RequestID != "" {
		canonical.RequestID, err = canonicalUUID("audit request id", canonical.RequestID)
		if err != nil {
			return Event{}, err
		}
	}
	if canonical.CorrelationID != "" {
		canonical.CorrelationID, err = canonicalUUID("audit correlation id", canonical.CorrelationID)
		if err != nil {
			return Event{}, err
		}
	}
	if err := validateOutcome(canonical.Outcome); err != nil {
		return Event{}, err
	}
	// PayloadDigest is calculated from the canonical producer intent before
	// persistence.
	digest, err := canonical.PayloadDigest()
	if err != nil {
		return Event{}, err
	}
	metadata := canonical.MetadataJSON
	if len(metadata) > maxAuditMetadataBytes {
		return Event{}, fmt.Errorf("audit metadata exceeds %d bytes", maxAuditMetadataBytes)
	}
	// NULL parameters are used for optional UUID/text values.  occurred_at is
	// intentionally database-owned; a replay therefore compares the complete
	// canonical identity/payload without depending on a newly generated clock
	// value.
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, scope_id, principal_id, source, operation, action,
		     resource_kind, resource_id, capability, outcome, request_id,
		     correlation_id, aggregate_key, aggregate_sequence, intent_digest,
		     metadata)
		VALUES ($1::uuid, NULLIF($2, ''), NULLIF($3, '')::uuid, $4, $5, $6,
		        NULLIF($7, ''), NULLIF($8, ''), $9, $10,
		        NULLIF($11, '')::uuid, NULLIF($12, '')::uuid, $13, $14, $15,
		        $16::jsonb)
		ON CONFLICT (audit_id) DO NOTHING`, canonical.EventID, "", canonical.PrincipalID,
		canonical.Source, canonical.Operation, canonical.Action, canonical.ResourceKind,
		canonical.ResourceID, canonical.Capability.String(), canonical.Outcome,
		canonical.RequestID, canonical.CorrelationID, canonical.AggregateKey,
		canonical.AggregateSequence, digest, metadata); err != nil {
		return Event{}, fmt.Errorf("insert audit event: %w", err)
	}

	var stored Event
	var storedMetadata string
	var metadataEqual bool
	if err := tx.QueryRow(ctx, `
		SELECT audit_id::text, COALESCE(scope_id, ''), COALESCE(principal_id::text, ''),
		       source, operation, action, COALESCE(resource_kind, ''),
		       COALESCE(resource_id, ''), capability, outcome,
		       COALESCE(request_id::text, ''), COALESCE(correlation_id::text, ''),
		       aggregate_key, aggregate_sequence, intent_digest, metadata::text,
		       metadata = $2::jsonb, occurred_at
		FROM audit.audit_event WHERE audit_id = $1::uuid`, canonical.EventID, metadata).
		Scan(&stored.AuditID, &stored.ScopeID, &stored.PrincipalID, &stored.Source,
			&stored.Operation, &stored.Action, &stored.ResourceKind, &stored.ResourceID,
			&stored.Capability, &stored.Outcome, &stored.RequestID, &stored.CorrelationID,
			&stored.AggregateKey, &stored.AggregateSequence, &stored.IntentDigest,
			&storedMetadata, &metadataEqual, &stored.OccurredAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Event{}, fmt.Errorf("audit event %s disappeared: %w", canonical.EventID, err)
		}
		return Event{}, fmt.Errorf("read audit event: %w", err)
	}
	stored.OccurredAt = stored.OccurredAt.UTC()
	stored.MetadataJSON = storedMetadata
	if stored.AuditID != canonical.EventID || stored.ScopeID != "" ||
		stored.PrincipalID != canonical.PrincipalID || stored.Action != canonical.Action ||
		stored.Source != canonical.Source || stored.Operation != canonical.Operation ||
		stored.ResourceKind != canonical.ResourceKind || stored.ResourceID != canonical.ResourceID ||
		stored.Capability != canonical.Capability || stored.Outcome != canonical.Outcome ||
		stored.RequestID != canonical.RequestID || stored.CorrelationID != canonical.CorrelationID ||
		stored.AggregateKey != canonical.AggregateKey || stored.AggregateSequence != canonical.AggregateSequence ||
		stored.IntentDigest != digest || !metadataEqual {
		return Event{}, fmt.Errorf("%w: audit event %s canonical payload differs", access.ErrAuditIntentConflict, canonical.EventID)
	}
	return stored, nil
}

const auditEventColumns = `audit_id::text, COALESCE(scope_id, ''),
 COALESCE(principal_id::text, ''), source, operation, action, COALESCE(resource_kind, ''),
 COALESCE(resource_id, ''), capability, outcome, COALESCE(request_id::text, ''),
 COALESCE(correlation_id::text, ''), aggregate_key, aggregate_sequence,
 intent_digest, metadata::text, occurred_at`

// GetAuditEvent reads one immutable audit row by retry identity.  The query is
// bounded to one row and accepts the same native pgx surface as the append
// method; a pool, connection, or caller-owned transaction may be supplied.
func (r *AuditRepository) GetAuditEvent(ctx context.Context, db Tx, auditID string) (Event, error) {
	if db == nil {
		return Event{}, errors.New("audit PostgreSQL connection is nil")
	}
	if ctx == nil {
		return Event{}, errors.New("audit context is nil")
	}
	canonicalID, err := canonicalUUID("audit event id", auditID)
	if err != nil {
		return Event{}, err
	}
	event, err := scanAuditEvent(db.QueryRow(ctx, `SELECT `+auditEventColumns+` FROM audit.audit_event WHERE audit_id = $1::uuid`, canonicalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, fmt.Errorf("audit event %s not found: %w", canonicalID, err)
	}
	return event, err
}

// ListAuditEvents returns at most limit rows in reverse occurrence order.
// This is a deliberately bounded read/export surface; callers cannot turn it
// into an unbounded audit dump.
func (r *AuditRepository) ListAuditEvents(ctx context.Context, db Tx, limit int) ([]Event, error) {
	if db == nil {
		return nil, errors.New("audit PostgreSQL connection is nil")
	}
	if ctx == nil {
		return nil, errors.New("audit context is nil")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > maxAuditReadRows {
		limit = maxAuditReadRows
	}
	rows, err := db.Query(ctx, `SELECT `+auditEventColumns+` FROM audit.audit_event ORDER BY occurred_at DESC, audit_id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		event, scanErr := scanAuditEvent(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan audit event: %w", scanErr)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	return events, nil
}

type rowScanner interface{ Scan(...any) error }

func scanAuditEvent(row rowScanner) (Event, error) {
	var event Event
	if err := row.Scan(&event.AuditID, &event.ScopeID, &event.PrincipalID,
		&event.Source, &event.Operation, &event.Action, &event.ResourceKind,
		&event.ResourceID, &event.Capability, &event.Outcome, &event.RequestID,
		&event.CorrelationID, &event.AggregateKey, &event.AggregateSequence,
		&event.IntentDigest, &event.MetadataJSON, &event.OccurredAt); err != nil {
		return Event{}, err
	}
	event.OccurredAt = event.OccurredAt.UTC()
	return event, nil
}

func validateOutcome(value string) error {
	switch value {
	case "success", "failure", "denied":
		return nil
	default:
		return fmt.Errorf("audit outcome %q is not supported", value)
	}
}

func canonicalUUID(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", fmt.Errorf("%s must be a UUID", label)
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || len(decoded) != 16 {
		if err == nil {
			err = errors.New("invalid UUID length")
		}
		return "", fmt.Errorf("%s must be a UUID: %w", label, err)
	}
	// Format the canonical UUID directly from the decoded bytes.
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		decoded[0], decoded[1], decoded[2], decoded[3], decoded[4], decoded[5],
		decoded[6], decoded[7], decoded[8], decoded[9], decoded[10], decoded[11],
		decoded[12], decoded[13], decoded[14], decoded[15]), nil
}
