// Package postgres implements LeapView's bounded PostgreSQL durable event log
// and transactional broadcast fan-out.  Every mutating method accepts the
// caller's pgx transaction; the source mutation and its event therefore share
// exactly one commit or rollback boundary.
package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Tx is the deliberately small native pgx transaction surface used by this
// capability.  pgx.Tx and pgxpool.Tx both satisfy it; no pool or second
// connection is opened by a repository operation.
type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository is stateless.  It exists as a named capability so callers can
// keep the event repository alongside other platform repositories.
type Repository struct{}

//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the forward event capability schema. It contains no
// transaction control; callers apply it using their migration transaction.
func SchemaSQL() string { return schemaSQL }

// New returns a stateless event repository.
func New() *Repository { return &Repository{} }

// EventInput describes one domain event. EventID is optional; when omitted a
// UUIDv7 is generated.
type EventInput struct {
	EventID       string
	ScopeID       string
	AggregateType string
	AggregateID   string
	EventType     string
	SchemaVersion int64
	OccurredAt    time.Time
	CorrelationID string
	Payload       json.RawMessage
}

// Event is the immutable event-log record returned after append.
type Event struct {
	EventID          string
	ScopeID          string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	EventType        string
	SchemaVersion    int64
	OccurredAt       time.Time
	CorrelationID    string
	Payload          json.RawMessage
}

// EventConflictError reports a retry using an existing event identity with a
// different immutable field.
type EventConflictError struct {
	EventID string
	Field   string
}

func (e *EventConflictError) Error() string {
	return fmt.Sprintf("event %s conflicts on %s", e.EventID, e.Field)
}

// ConsumerInput enrolls one durable broadcast consumer.  ReplayFrom must be
// at or after the current retention floor.  A consumer is initially
// backfilling; callers invoke Backfill until it reports Done, at which point it
// is enabled by the repository.
type ConsumerInput struct {
	ConsumerID  string
	ConsumerKey string
	ReplayFrom  time.Time
	Metadata    json.RawMessage
}

// Consumer is the persisted lifecycle record.
type Consumer struct {
	ConsumerID       string
	ConsumerKey      string
	Lifecycle        string
	ReplayFrom       time.Time
	FrontierEventID  string
	FrontierOccurred time.Time
	Metadata         json.RawMessage
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Delivery is a consumer-specific durable work item.
type Delivery struct {
	ConsumerID      string
	EventID         string
	Status          string
	Attempts        int64
	ClaimGeneration int64
	AvailableAt     time.Time
	ClaimedBy       string
	ClaimedUntil    time.Time
	TerminalAt      time.Time
	Evidence        json.RawMessage
}

// BackfillResult describes one bounded, idempotent backfill transaction.
type BackfillResult struct {
	Inserted         int
	Scanned          int
	Done             bool
	FrontierEventID  string
	FrontierOccurred time.Time
}

// ClaimOptions controls an atomic consumer-specific claim.
type ClaimOptions struct {
	ConsumerID string
	WorkerID   string
	Limit      int
	Lease      time.Duration
}

// DeliveryOutcome is one terminal processing result.  dead_letter remains
// visible and blocks event retention until resolved or explicitly waived.
type DeliveryOutcome string

const (
	DeliverySucceeded  DeliveryOutcome = "succeeded"
	DeliveryDeadLetter DeliveryOutcome = "dead_letter"
	DeliveryWaived     DeliveryOutcome = "waived"
)

// MaxDurableConsumers bounds transactional fan-out write amplification. A
// larger or dynamically changing subscriber set belongs behind an external
// dispatcher rather than an event×consumer table.
const MaxDurableConsumers = 32

// NotificationChannel carries only opaque event identities. Notifications are
// commit-time wake hints; durable event and delivery rows remain authoritative.
const NotificationChannel = "leapview_event"

// RetireOptions controls the retirement fence.  Without Waive all existing
// pending, claimed, and dead-letter rows must already be terminal.  A waiver
// marks those rows waived with the supplied audited evidence.
type RetireOptions struct {
	ConsumerID string
	Waive      bool
	Evidence   json.RawMessage
}

// RetryOptions returns a claimed delivery to the pending queue with bounded
// backoff. Once attempts reaches MaxAttempts it is moved to dead_letter and
// remains retention-blocking until explicitly resolved or waived.
type RetryOptions struct {
	ConsumerID      string
	EventID         string
	WorkerID        string
	ClaimGeneration int64
	Delay           time.Duration
	MaxAttempts     int64
	Evidence        json.RawMessage
}

// AppendEvent allocates an aggregate version, writes the event, and fans it
// out to the consumers visible after the registry key-share fence.  The
// registry fence and consumer scan intentionally are separate SQL commands:
// under READ COMMITTED the scan then observes a completed enrollment or
// retirement boundary.
func (r *Repository) AppendEvent(ctx context.Context, tx Tx, in EventInput) (Event, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return Event{}, err
	}
	payload, err := canonicalObject(in.Payload, 65536)
	if err != nil {
		return Event{}, fmt.Errorf("event payload: %w", err)
	}
	scope, err := boundedID("scope", in.ScopeID, 255)
	if err != nil {
		return Event{}, err
	}
	aggregateType, err := boundedID("aggregate type", in.AggregateType, 255)
	if err != nil {
		return Event{}, err
	}
	aggregateID, err := boundedID("aggregate id", in.AggregateID, 255)
	if err != nil {
		return Event{}, err
	}
	eventType, err := boundedID("event type", in.EventType, 255)
	if err != nil {
		return Event{}, err
	}
	if in.SchemaVersion <= 0 {
		return Event{}, errors.New("event schema version must be positive")
	}
	eventID := in.EventID
	if eventID != strings.TrimSpace(eventID) {
		return Event{}, errors.New("event id must not contain surrounding whitespace")
	}
	if eventID == "" {
		eventID, err = uuidv7()
		if err != nil {
			return Event{}, fmt.Errorf("generate event id: %w", err)
		}
	}
	if err := validateUUID("event id", eventID); err != nil {
		return Event{}, err
	}
	correlationID := in.CorrelationID
	if correlationID != strings.TrimSpace(correlationID) {
		return Event{}, errors.New("correlation id must not contain surrounding whitespace")
	}
	if correlationID != "" {
		if err := validateUUID("correlation id", correlationID); err != nil {
			return Event{}, err
		}
	}
	occurredAt := in.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	occurredAt = occurredAt.UTC()
	explicitOccurredAt := !in.OccurredAt.IsZero()

	// Serialize same-identity producers before the idempotency lookup. Without
	// this lock two concurrent transactions can both miss the predecessor and
	// one will consume an aggregate version before failing on the event PK.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, eventID); err != nil {
		return Event{}, fmt.Errorf("lock event identity: %w", err)
	}
	// Explicit identities make producer retries idempotent. A previously
	// committed event already has its fan-out rows because insertion is one
	// transaction; return that immutable record instead of allocating another
	// aggregate version.
	var existing Event
	var existingCorrelation, existingPayload string
	err = tx.QueryRow(ctx, `
		SELECT event_id::text, scope_id, aggregate_type, aggregate_id, aggregate_version,
		       event_type, schema_version, occurred_at, COALESCE(correlation_id::text, ''), payload::text
		FROM event.event_log WHERE event_id = $1::uuid`, eventID).
		Scan(&existing.EventID, &existing.ScopeID, &existing.AggregateType, &existing.AggregateID,
			&existing.AggregateVersion, &existing.EventType, &existing.SchemaVersion,
			&existing.OccurredAt, &existingCorrelation, &existingPayload)
	if err == nil {
		existing.OccurredAt = existing.OccurredAt.UTC()
		existing.CorrelationID = existingCorrelation
		existingPayloadCanonical, canonicalErr := canonicalObject(json.RawMessage(existingPayload), 65536)
		if canonicalErr != nil {
			return Event{}, fmt.Errorf("canonicalize existing event payload: %w", canonicalErr)
		}
		existing.Payload = existingPayloadCanonical
		if existing.ScopeID != scope {
			return Event{}, &EventConflictError{EventID: eventID, Field: "scope_id"}
		}
		if existing.AggregateType != aggregateType {
			return Event{}, &EventConflictError{EventID: eventID, Field: "aggregate_type"}
		}
		if existing.AggregateID != aggregateID {
			return Event{}, &EventConflictError{EventID: eventID, Field: "aggregate_id"}
		}
		if existing.EventType != eventType {
			return Event{}, &EventConflictError{EventID: eventID, Field: "event_type"}
		}
		if existing.SchemaVersion != in.SchemaVersion {
			return Event{}, &EventConflictError{EventID: eventID, Field: "schema_version"}
		}
		if existing.CorrelationID != correlationID {
			return Event{}, &EventConflictError{EventID: eventID, Field: "correlation_id"}
		}
		if !bytes.Equal(existing.Payload, payload) {
			return Event{}, &EventConflictError{EventID: eventID, Field: "payload"}
		}
		if explicitOccurredAt && !existing.OccurredAt.Equal(occurredAt) {
			return Event{}, &EventConflictError{EventID: eventID, Field: "occurred_at"}
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Event{}, fmt.Errorf("check event identity: %w", err)
	}

	// Insert the explicit aggregate row first, then atomically increment it.
	// The UPDATE obtains the row lock and never derives a version from MAX().
	if _, err := tx.Exec(ctx, `
		INSERT INTO event.event_aggregate (scope_id, aggregate_type, aggregate_id, next_version)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (scope_id, aggregate_type, aggregate_id) DO NOTHING`, scope, aggregateType, aggregateID); err != nil {
		return Event{}, fmt.Errorf("ensure event aggregate: %w", err)
	}
	var version int64
	if err := tx.QueryRow(ctx, `
		UPDATE event.event_aggregate
		SET next_version = next_version + 1, updated_at = clock_timestamp()
		WHERE scope_id = $1 AND aggregate_type = $2 AND aggregate_id = $3
		RETURNING next_version - 1`, scope, aggregateType, aggregateID).Scan(&version); err != nil {
		return Event{}, fmt.Errorf("allocate aggregate version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event.event_log
		    (event_id, scope_id, aggregate_type, aggregate_id, aggregate_version,
		     event_type, schema_version, occurred_at, correlation_id, payload)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid, $10::jsonb)`,
		eventID, scope, aggregateType, aggregateID, version, eventType, in.SchemaVersion,
		occurredAt, correlationID, payload); err != nil {
		return Event{}, fmt.Errorf("insert durable event: %w", err)
	}

	// This statement must complete before the consumer scan below.  Do not
	// combine it with the scan in a CTE: READ COMMITTED command snapshots would
	// otherwise permit a mixed pre/post enrollment view.
	var registry bool
	if err := tx.QueryRow(ctx, `
		SELECT registry_id FROM event.event_fanout_registry
		WHERE registry_id = true FOR KEY SHARE`).Scan(&registry); err != nil {
		return Event{}, fmt.Errorf("acquire event fan-out fence: %w", err)
	}
	if !registry {
		return Event{}, errors.New("event fan-out registry row is invalid")
	}
	rows, err := tx.Query(ctx, `
		SELECT consumer_id::text
		FROM event.event_consumer
		WHERE lifecycle IN ('backfilling', 'enabled', 'paused')
		ORDER BY consumer_id`)
	if err != nil {
		return Event{}, fmt.Errorf("scan event consumers: %w", err)
	}
	consumerIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Event{}, fmt.Errorf("scan event consumer: %w", err)
		}
		consumerIDs = append(consumerIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Event{}, fmt.Errorf("scan event consumers: %w", err)
	}
	rows.Close()
	for _, consumerID := range consumerIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO event.event_delivery (consumer_id, event_id, status, available_at)
			VALUES ($1::uuid, $2::uuid, 'pending', clock_timestamp())
			ON CONFLICT (consumer_id, event_id) DO NOTHING`, consumerID, eventID); err != nil {
			return Event{}, fmt.Errorf("insert event delivery: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotificationChannel, eventID); err != nil {
		return Event{}, fmt.Errorf("publish durable event wake hint: %w", err)
	}
	return Event{EventID: eventID, ScopeID: scope, AggregateType: aggregateType,
		AggregateID: aggregateID, AggregateVersion: version, EventType: eventType,
		SchemaVersion: in.SchemaVersion, OccurredAt: occurredAt, CorrelationID: correlationID,
		Payload: payload}, nil
}

// EnrollConsumer creates a backfilling consumer while holding the registry
// FOR UPDATE fence.  It records an exact replay boundary (timestamp plus UUID
// tie-breaker), creates a live retention root, and returns only after the
// caller's transaction can commit that boundary.
func (r *Repository) EnrollConsumer(ctx context.Context, tx Tx, in ConsumerInput) (Consumer, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return Consumer{}, err
	}
	key, err := boundedID("consumer key", in.ConsumerKey, 255)
	if err != nil {
		return Consumer{}, err
	}
	replayFrom := in.ReplayFrom.UTC()
	if in.ReplayFrom.IsZero() {
		replayFrom = time.Unix(0, 0).UTC()
	}
	metadata, err := canonicalObject(in.Metadata, 16384)
	if err != nil {
		return Consumer{}, fmt.Errorf("consumer metadata: %w", err)
	}
	var registry bool
	if err := tx.QueryRow(ctx, `
		SELECT registry_id FROM event.event_fanout_registry
		WHERE registry_id = true FOR UPDATE`).Scan(&registry); err != nil {
		return Consumer{}, fmt.Errorf("acquire enrollment fence: %w", err)
	}
	if !registry {
		return Consumer{}, errors.New("event fan-out registry row is invalid")
	}
	var consumerCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM event.event_consumer WHERE lifecycle <> 'retired'`).Scan(&consumerCount); err != nil {
		return Consumer{}, fmt.Errorf("count durable event consumers: %w", err)
	}
	if consumerCount >= MaxDurableConsumers {
		return Consumer{}, fmt.Errorf("durable consumer limit %d reached", MaxDurableConsumers)
	}
	var floor time.Time
	if err := tx.QueryRow(ctx, `SELECT floor_at FROM event.event_retention_floor WHERE singleton = true FOR SHARE`).Scan(&floor); err != nil {
		return Consumer{}, fmt.Errorf("read event retention floor: %w", err)
	}
	if replayFrom.Before(floor) {
		return Consumer{}, fmt.Errorf("consumer replay_from %s precedes retention floor %s", replayFrom, floor)
	}

	// Capture the latest committed event only after the fence is acquired.
	// Producers blocked on this fence resume after enrollment commit and fan out
	// directly to the newly-created backfilling consumer.
	var replayUntil time.Time
	var replayUntilID string
	err = tx.QueryRow(ctx, `
		SELECT occurred_at, event_id::text
		FROM event.event_log
		ORDER BY occurred_at DESC, event_id DESC
		LIMIT 1`).Scan(&replayUntil, &replayUntilID)
	if errors.Is(err, pgx.ErrNoRows) {
		replayUntil = replayFrom
		replayUntilID = ""
	} else if err != nil {
		return Consumer{}, fmt.Errorf("capture consumer replay boundary: %w", err)
	}
	consumerID := in.ConsumerID
	if consumerID != strings.TrimSpace(consumerID) {
		return Consumer{}, errors.New("consumer id must not contain surrounding whitespace")
	}
	if consumerID == "" {
		consumerID, err = uuidv7()
		if err != nil {
			return Consumer{}, fmt.Errorf("generate consumer id: %w", err)
		}
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return Consumer{}, err
	}
	rootID, err := uuidv7()
	if err != nil {
		return Consumer{}, fmt.Errorf("generate retention root id: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event.event_consumer
		    (consumer_id, consumer_key, lifecycle, replay_from, metadata)
		VALUES ($1::uuid, $2, 'backfilling', $3, $4::jsonb)`, consumerID, key, replayFrom, metadata); err != nil {
		return Consumer{}, fmt.Errorf("enroll event consumer: %w", err)
	}
	var rootUntil any = replayUntil
	var rootUntilID any = nil
	if replayUntilID != "" {
		rootUntilID = replayUntilID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event.event_retention_root
		    (root_id, consumer_id, replay_from, replay_until, replay_until_event_id, state, evidence)
		VALUES ($1::uuid, $2::uuid, $3, $4, NULLIF($5, '')::uuid, 'live', $6::jsonb)`,
		rootID, consumerID, replayFrom, rootUntil, rootUntilID,
		`{"kind":"event_backfill"}`); err != nil {
		return Consumer{}, fmt.Errorf("create event retention root: %w", err)
	}
	return Consumer{ConsumerID: consumerID, ConsumerKey: key,
		Lifecycle: "backfilling", ReplayFrom: replayFrom, Metadata: metadata}, nil
}

// Backfill copies at most limit events into one consumer's delivery set.  It
// advances the frontier in the same transaction as the idempotent inserts.
// Calling it again after a retry is safe because deliveries use ON CONFLICT DO
// NOTHING.  The final short/empty batch enables the consumer and expires its
// replay root.
func (r *Repository) Backfill(ctx context.Context, tx Tx, consumerID string, limit int) (BackfillResult, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return BackfillResult{}, err
	}
	if consumerID != strings.TrimSpace(consumerID) {
		return BackfillResult{}, errors.New("consumer id must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return BackfillResult{}, err
	}
	if limit <= 0 {
		return BackfillResult{}, errors.New("backfill limit must be positive")
	}
	if limit > 1000 {
		return BackfillResult{}, errors.New("backfill limit exceeds 1000")
	}
	var lifecycle string
	if err := tx.QueryRow(ctx, `
		SELECT lifecycle FROM event.event_consumer
		WHERE consumer_id = $1::uuid FOR UPDATE`, consumerID).Scan(&lifecycle); err != nil {
		return BackfillResult{}, fmt.Errorf("lock event consumer: %w", err)
	}
	if lifecycle != "backfilling" {
		return BackfillResult{Done: lifecycle == "enabled"}, nil
	}
	var replayFrom time.Time
	var rootID string
	err := tx.QueryRow(ctx, `
		SELECT root_id::text, replay_from
		FROM event.event_retention_root
		WHERE consumer_id = $1::uuid AND state = 'live'
		ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, consumerID).
		Scan(&rootID, &replayFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return BackfillResult{}, errors.New("event consumer has no live retention root")
	}
	if err != nil {
		return BackfillResult{}, fmt.Errorf("lock event retention root: %w", err)
	}
	var replayUntil, frontierAt time.Time
	var replayUntilID, frontierID string
	var hasUntil, hasFrontier bool
	var replayUntilValue, frontierAtValue pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		SELECT replay_until, COALESCE(replay_until_event_id::text, ''),
		       frontier_occurred_at, COALESCE(frontier_event_id::text, '')
		FROM event.event_retention_root WHERE root_id = $1::uuid`, rootID).
		Scan(&replayUntilValue, &replayUntilID, &frontierAtValue, &frontierID); err != nil {
		return BackfillResult{}, fmt.Errorf("read event replay cursor: %w", err)
	}
	if replayUntilValue.Valid {
		replayUntil = replayUntilValue.Time.UTC()
	}
	if frontierAtValue.Valid {
		frontierAt = frontierAtValue.Time.UTC()
	}
	hasUntil = replayUntilValue.Valid
	hasFrontier = frontierAtValue.Valid && frontierID != ""

	query := `
		SELECT event_id::text, occurred_at
		FROM event.event_log
		WHERE occurred_at >= $1
		  AND ($2::boolean = false OR occurred_at < $3 OR (occurred_at = $3 AND event_id <= NULLIF($4, '')::uuid))
		  AND ($5::boolean = false OR occurred_at > $6 OR (occurred_at = $6 AND event_id > NULLIF($7, '')::uuid))
		ORDER BY occurred_at, event_id
		LIMIT $8`
	rows, err := tx.Query(ctx, query, replayFrom, hasUntil, replayUntil, replayUntilID, hasFrontier, frontierAt, frontierID, limit)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("scan event backfill batch: %w", err)
	}
	type cursor struct {
		id string
		at time.Time
	}
	batch := make([]cursor, 0, limit)
	for rows.Next() {
		var c cursor
		if err := rows.Scan(&c.id, &c.at); err != nil {
			rows.Close()
			return BackfillResult{}, fmt.Errorf("scan event backfill row: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return BackfillResult{}, fmt.Errorf("scan event backfill batch: %w", err)
	}
	rows.Close()
	result := BackfillResult{Scanned: len(batch)}
	for _, c := range batch {
		ct, err := tx.Exec(ctx, `
			INSERT INTO event.event_delivery (consumer_id, event_id, status, available_at)
			VALUES ($1::uuid, $2::uuid, 'pending', clock_timestamp())
			ON CONFLICT (consumer_id, event_id) DO NOTHING`, consumerID, c.id)
		if err != nil {
			return BackfillResult{}, fmt.Errorf("insert backfill delivery: %w", err)
		}
		result.Inserted += int(ct.RowsAffected())
		result.FrontierEventID, result.FrontierOccurred = c.id, c.at
	}
	if len(batch) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE event.event_consumer
			SET frontier_event_id = $2::uuid, frontier_occurred_at = $3, updated_at = clock_timestamp()
			WHERE consumer_id = $1::uuid`, consumerID, result.FrontierEventID, result.FrontierOccurred); err != nil {
			return BackfillResult{}, fmt.Errorf("advance consumer frontier: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE event.event_retention_root
			SET frontier_event_id = $2::uuid, frontier_occurred_at = $3
			WHERE root_id = $1::uuid`, rootID, result.FrontierEventID, result.FrontierOccurred); err != nil {
			return BackfillResult{}, fmt.Errorf("advance retention frontier: %w", err)
		}
	}
	if len(batch) < limit {
		if _, err := tx.Exec(ctx, `
			UPDATE event.event_consumer SET lifecycle = 'enabled', updated_at = clock_timestamp()
			WHERE consumer_id = $1::uuid`, consumerID); err != nil {
			return BackfillResult{}, fmt.Errorf("enable event consumer: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE event.event_retention_root SET state = 'expired' WHERE root_id = $1::uuid`, rootID); err != nil {
			return BackfillResult{}, fmt.Errorf("expire backfill retention root: %w", err)
		}
		result.Done = true
	}
	return result, nil
}

// RetireConsumer closes a consumer's fan-out boundary under the registry
// FOR UPDATE fence. Existing deliveries must be terminal; when Waive is true
// all unresolved rows become audited waived rows in this same transaction.
func (r *Repository) RetireConsumer(ctx context.Context, tx Tx, opts RetireOptions) error {
	if err := validateTxContext(ctx, tx); err != nil {
		return err
	}
	consumerID := opts.ConsumerID
	if consumerID != strings.TrimSpace(consumerID) {
		return errors.New("consumer id must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return err
	}
	evidence, err := canonicalObject(opts.Evidence, 32768)
	if err != nil {
		return fmt.Errorf("retirement evidence: %w", err)
	}
	if opts.Waive {
		if _, err := nonEmptyObject(evidence); err != nil {
			return fmt.Errorf("retirement waiver evidence: %w", err)
		}
	}
	var registry bool
	if err := tx.QueryRow(ctx, `
		SELECT registry_id FROM event.event_fanout_registry
		WHERE registry_id = true FOR UPDATE`).Scan(&registry); err != nil {
		return fmt.Errorf("acquire retirement fence: %w", err)
	}
	if !registry {
		return errors.New("event fan-out registry row is invalid")
	}
	var lifecycle string
	if err := tx.QueryRow(ctx, `
		SELECT lifecycle FROM event.event_consumer
		WHERE consumer_id = $1::uuid FOR UPDATE`, consumerID).Scan(&lifecycle); err != nil {
		return fmt.Errorf("lock event consumer for retirement: %w", err)
	}
	if lifecycle == "retired" {
		return nil
	}
	var unresolved int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM event.event_delivery
		WHERE consumer_id = $1::uuid AND status IN ('pending', 'claimed', 'dead_letter')`, consumerID).Scan(&unresolved); err != nil {
		return fmt.Errorf("count unresolved event deliveries: %w", err)
	}
	if unresolved > 0 && !opts.Waive {
		return fmt.Errorf("consumer has %d unresolved deliveries", unresolved)
	}
	if unresolved > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE event.event_delivery
			SET status = 'waived', terminal_at = clock_timestamp(), claimed_by = NULL,
			    claimed_until = NULL, evidence = $2::jsonb
			WHERE consumer_id = $1::uuid AND status IN ('pending', 'claimed', 'dead_letter')`, consumerID, evidence); err != nil {
			return fmt.Errorf("waive event deliveries: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE event.event_consumer
		SET lifecycle = 'retired', updated_at = clock_timestamp()
		WHERE consumer_id = $1::uuid`, consumerID); err != nil {
		return fmt.Errorf("retire event consumer: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE event.event_retention_root SET state = 'expired', evidence = $2::jsonb
		WHERE consumer_id = $1::uuid AND state <> 'expired'`, consumerID, evidence); err != nil {
		return fmt.Errorf("expire consumer retention root: %w", err)
	}
	return nil
}

// PauseConsumer fences the lifecycle transition so producers either observe
// the consumer before the pause (and enqueue a delivery) or after it. Paused
// consumers continue to receive rows but Claim refuses to process them.
func (r *Repository) PauseConsumer(ctx context.Context, tx Tx, consumerID string) error {
	return r.setLifecycle(ctx, tx, consumerID, "paused")
}

// ResumeConsumer re-enables a paused consumer. Backfilling consumers must use
// Backfill to preserve their replay root and frontier semantics.
func (r *Repository) ResumeConsumer(ctx context.Context, tx Tx, consumerID string) error {
	return r.setLifecycle(ctx, tx, consumerID, "enabled")
}

func (r *Repository) setLifecycle(ctx context.Context, tx Tx, consumerID, lifecycle string) error {
	if err := validateTxContext(ctx, tx); err != nil {
		return err
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return err
	}
	if lifecycle != "paused" && lifecycle != "enabled" {
		return errors.New("invalid consumer lifecycle transition")
	}
	var registry bool
	if err := tx.QueryRow(ctx, `SELECT registry_id FROM event.event_fanout_registry WHERE registry_id = true FOR UPDATE`).Scan(&registry); err != nil {
		return fmt.Errorf("acquire lifecycle fence: %w", err)
	}
	if !registry {
		return errors.New("event fan-out registry row is invalid")
	}
	ct, err := tx.Exec(ctx, `
		UPDATE event.event_consumer SET lifecycle = $2, updated_at = clock_timestamp()
		WHERE consumer_id = $1::uuid AND lifecycle IN ('enabled', 'paused')`, consumerID, lifecycle)
	if err != nil {
		return fmt.Errorf("set event consumer lifecycle: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return errors.New("event consumer is not enabled or paused")
	}
	return nil
}

// Claim atomically claims only this consumer's rows. Expired claims are
// eligible for retry; SKIP LOCKED lets independent workers claim other rows
// without waiting on a slow worker.
func (r *Repository) Claim(ctx context.Context, tx Tx, opts ClaimOptions) ([]Delivery, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return nil, err
	}
	consumerID := opts.ConsumerID
	if consumerID != strings.TrimSpace(consumerID) {
		return nil, errors.New("consumer id must not contain surrounding whitespace")
	}
	workerID, err := boundedID("worker id", opts.WorkerID, 255)
	if err != nil {
		return nil, err
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		return nil, errors.New("claim limit must be positive")
	}
	if limit > 1000 {
		return nil, errors.New("claim limit exceeds 1000")
	}
	lease := opts.Lease
	if lease <= 0 {
		return nil, errors.New("claim lease must be positive")
	}
	if lease > 24*time.Hour {
		return nil, errors.New("claim lease exceeds 24 hours")
	}
	var lifecycle string
	if err := tx.QueryRow(ctx, `SELECT lifecycle FROM event.event_consumer WHERE consumer_id = $1::uuid FOR SHARE`, consumerID).Scan(&lifecycle); err != nil {
		return nil, fmt.Errorf("read event consumer for claim: %w", err)
	}
	if lifecycle != "enabled" {
		return nil, fmt.Errorf("consumer lifecycle %q cannot claim deliveries", lifecycle)
	}
	rows, err := tx.Query(ctx, `
		WITH candidates AS (
			SELECT consumer_id, event_id
			FROM event.event_delivery
			WHERE consumer_id = $1::uuid
			  AND (status = 'pending' OR (status = 'claimed' AND claimed_until < clock_timestamp()))
			  AND available_at <= clock_timestamp()
			ORDER BY available_at, event_id
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		)
		UPDATE event.event_delivery d
		SET status = 'claimed', attempts = d.attempts + 1,
		    claim_generation = d.claim_generation + 1,
		    claimed_by = $2, claimed_until = clock_timestamp() + $4::interval,
		    terminal_at = NULL
		FROM candidates c
		WHERE d.consumer_id = c.consumer_id AND d.event_id = c.event_id
		RETURNING d.event_id::text, d.status, d.attempts, d.claim_generation, d.available_at,
		          d.claimed_by, d.claimed_until, d.terminal_at, d.evidence::text`,
		consumerID, workerID, limit, fmt.Sprintf("%f seconds", lease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("claim event deliveries: %w", err)
	}
	defer rows.Close()
	claimed := make([]Delivery, 0, limit)
	for rows.Next() {
		var d Delivery
		var claimedUntil, terminalAt pgtype.Timestamptz
		var evidence string
		if err := rows.Scan(&d.EventID, &d.Status, &d.Attempts, &d.ClaimGeneration, &d.AvailableAt,
			&d.ClaimedBy, &claimedUntil, &terminalAt, &evidence); err != nil {
			return nil, fmt.Errorf("scan claimed event delivery: %w", err)
		}
		if claimedUntil.Valid {
			d.ClaimedUntil = claimedUntil.Time.UTC()
		}
		if terminalAt.Valid {
			d.TerminalAt = terminalAt.Time.UTC()
		}
		d.ConsumerID = consumerID
		d.Evidence = json.RawMessage(evidence)
		claimed = append(claimed, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan claimed event deliveries: %w", err)
	}
	return claimed, nil
}

// Complete records a terminal delivery outcome and verifies ownership of the
// active claim. A dead letter is intentionally terminal processing state but
// does not satisfy retention until resolved or waived.
func (r *Repository) Complete(ctx context.Context, tx Tx, consumerID, eventID, workerID string, claimGeneration int64, outcome DeliveryOutcome, evidence json.RawMessage) error {
	if err := validateTxContext(ctx, tx); err != nil {
		return err
	}
	if consumerID != strings.TrimSpace(consumerID) || eventID != strings.TrimSpace(eventID) || workerID != strings.TrimSpace(workerID) {
		return errors.New("delivery identities must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return err
	}
	if err := validateUUID("event id", eventID); err != nil {
		return err
	}
	if workerID == "" {
		return errors.New("worker id is required")
	}
	if claimGeneration <= 0 {
		return errors.New("claim generation must be positive")
	}
	if outcome != DeliverySucceeded && outcome != DeliveryDeadLetter && outcome != DeliveryWaived {
		return fmt.Errorf("invalid delivery outcome %q", outcome)
	}
	evidenceJSON, err := canonicalObject(evidence, 32768)
	if err != nil {
		return fmt.Errorf("delivery evidence: %w", err)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE event.event_delivery
		SET status = $5, terminal_at = clock_timestamp(), claimed_by = NULL,
		    claimed_until = NULL, evidence = $6::jsonb
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid
		  AND status = 'claimed' AND claimed_by = $3 AND claim_generation = $4
		  AND claimed_until > clock_timestamp()
		RETURNING event_id`, consumerID, eventID, workerID, claimGeneration, outcome, evidenceJSON)
	if err != nil {
		return fmt.Errorf("complete event delivery: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return errors.New("event delivery is not claimed by worker")
	}
	return nil
}

// Replay marks a terminal delivery pending again by event identity. It is
// useful for explicit poison resolution and is intentionally consumer-scoped.
func (r *Repository) Replay(ctx context.Context, tx Tx, consumerID, eventID string) error {
	if err := validateTxContext(ctx, tx); err != nil {
		return err
	}
	if consumerID != strings.TrimSpace(consumerID) || eventID != strings.TrimSpace(eventID) {
		return errors.New("delivery identities must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return err
	}
	if err := validateUUID("event id", eventID); err != nil {
		return err
	}
	ct, err := tx.Exec(ctx, `
		UPDATE event.event_delivery
		SET status = 'pending', available_at = clock_timestamp(), claimed_by = NULL,
		    claimed_until = NULL, terminal_at = NULL
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid
		  AND status IN ('succeeded', 'dead_letter', 'waived')`, consumerID, eventID)
	if err != nil {
		return fmt.Errorf("replay event delivery: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return errors.New("event delivery is not replayable")
	}
	return nil
}

// Retry atomically records a failed attempt. Delay and MaxAttempts are bounded
// at the repository edge so a malformed caller cannot create an unbounded
// retry storm or interval.
func (r *Repository) Retry(ctx context.Context, tx Tx, opts RetryOptions) error {
	if err := validateTxContext(ctx, tx); err != nil {
		return err
	}
	if opts.ConsumerID != strings.TrimSpace(opts.ConsumerID) || opts.EventID != strings.TrimSpace(opts.EventID) {
		return errors.New("delivery identities must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", opts.ConsumerID); err != nil {
		return err
	}
	if err := validateUUID("event id", opts.EventID); err != nil {
		return err
	}
	if opts.WorkerID != strings.TrimSpace(opts.WorkerID) {
		return errors.New("worker id must not contain surrounding whitespace")
	}
	if strings.TrimSpace(opts.WorkerID) == "" {
		return errors.New("worker id is required")
	}
	if opts.ClaimGeneration <= 0 {
		return errors.New("claim generation must be positive")
	}
	if opts.MaxAttempts <= 0 {
		return errors.New("max attempts must be positive")
	}
	if opts.MaxAttempts > 1000 {
		return errors.New("max attempts exceeds 1000")
	}
	if opts.Delay < 0 {
		return errors.New("retry delay cannot be negative")
	}
	if opts.Delay > 24*time.Hour {
		return errors.New("retry delay exceeds 24 hours")
	}
	evidence, err := canonicalObject(opts.Evidence, 32768)
	if err != nil {
		return fmt.Errorf("retry evidence: %w", err)
	}
	ct, err := tx.Exec(ctx, `
		UPDATE event.event_delivery
		SET status = CASE WHEN attempts >= $6 THEN 'dead_letter' ELSE 'pending' END,
		    available_at = CASE WHEN attempts >= $6 THEN available_at ELSE clock_timestamp() + $5::interval END,
		    terminal_at = CASE WHEN attempts >= $6 THEN clock_timestamp() ELSE NULL END,
		    claimed_by = NULL, claimed_until = NULL, evidence = $7::jsonb
		WHERE consumer_id = $1::uuid AND event_id = $2::uuid
		  AND status = 'claimed' AND claimed_by = $3 AND claim_generation = $4
		  AND claimed_until > clock_timestamp()`, opts.ConsumerID, opts.EventID, opts.WorkerID,
		opts.ClaimGeneration, fmt.Sprintf("%f seconds", opts.Delay.Seconds()), opts.MaxAttempts, evidence)
	if err != nil {
		return fmt.Errorf("retry event delivery: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return errors.New("event delivery is not claimed by worker")
	}
	return nil
}

// Prune removes events older than before only when no live replay root pins
// them and every applicable delivery is terminal succeeded or waived. Dead
// letters deliberately remain visible and block pruning. The durable floor is
// advanced in the same transaction and is capped by the oldest live root.
func (r *Repository) Prune(ctx context.Context, tx Tx, before time.Time) (int64, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return 0, err
	}
	if before.IsZero() {
		return 0, errors.New("prune cutoff is required")
	}
	before = before.UTC()
	var floor time.Time
	if err := tx.QueryRow(ctx, `
		SELECT floor_at FROM event.event_retention_floor
		WHERE singleton = true FOR UPDATE`).Scan(&floor); err != nil {
		return 0, fmt.Errorf("lock event retention floor: %w", err)
	}
	var oldestRoot pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		SELECT min(replay_from) FROM event.event_retention_root
		WHERE state <> 'expired'`).Scan(&oldestRoot); err != nil {
		return 0, fmt.Errorf("read live event retention roots: %w", err)
	}
	target := before
	if oldestRoot.Valid && oldestRoot.Time.Before(target) {
		target = oldestRoot.Time.UTC()
	}
	// An unresolved delivery is itself a retention root. Keep the floor at its
	// timestamp so a newly enrolled consumer can still replay that event while
	// poison handling is visible and being resolved.
	var blocked pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `
		SELECT min(e.occurred_at)
		FROM event.event_log e
		WHERE e.occurred_at < $1
		  AND NOT EXISTS (
			SELECT 1 FROM event.event_retention_root r
			WHERE r.state <> 'expired'
			  AND e.occurred_at >= r.replay_from
			  AND (r.replay_until IS NULL OR e.occurred_at <= r.replay_until)
		  )
		  AND EXISTS (
			SELECT 1 FROM event.event_delivery d
			WHERE d.event_id = e.event_id
			  AND d.status IN ('pending', 'claimed', 'dead_letter')
		  )`, target).Scan(&blocked); err != nil {
		return 0, fmt.Errorf("read unresolved event retention blockers: %w", err)
	}
	if blocked.Valid && blocked.Time.Before(target) {
		target = blocked.Time.UTC()
	}
	if target.After(floor) {
		if _, err := tx.Exec(ctx, `
			UPDATE event.event_retention_floor SET floor_at = $1, updated_at = clock_timestamp()
			WHERE singleton = true`, target); err != nil {
			return 0, fmt.Errorf("advance event retention floor: %w", err)
		}
	}
	const batch = 1000
	var removed int64
	for {
		ct, err := tx.Exec(ctx, `
			WITH doomed AS (
				SELECT e.event_id FROM event.event_log e
				WHERE e.occurred_at < $1
				  AND NOT EXISTS (
				  SELECT 1 FROM event.event_retention_root r
				  WHERE r.state <> 'expired'
				    AND e.occurred_at >= r.replay_from
				    AND (r.replay_until IS NULL OR e.occurred_at <= r.replay_until)
				  )
				  AND NOT EXISTS (
				  SELECT 1 FROM event.event_delivery d
				  WHERE d.event_id = e.event_id
				    AND d.status IN ('pending', 'claimed', 'dead_letter')
				  )
				ORDER BY e.occurred_at, e.event_id
				LIMIT $2
			)
			DELETE FROM event.event_log e USING doomed
			WHERE e.event_id = doomed.event_id`, target, batch)
		if err != nil {
			return removed, fmt.Errorf("prune durable events: %w", err)
		}
		removed += ct.RowsAffected()
		if ct.RowsAffected() < batch {
			break
		}
	}
	return removed, nil
}

// RetentionFloor returns the durable event replay floor.
func (r *Repository) RetentionFloor(ctx context.Context, tx Tx) (time.Time, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return time.Time{}, err
	}
	var floor time.Time
	if err := tx.QueryRow(ctx, `SELECT floor_at FROM event.event_retention_floor WHERE singleton = true`).Scan(&floor); err != nil {
		return time.Time{}, fmt.Errorf("read event retention floor: %w", err)
	}
	return floor.UTC(), nil
}

// ListDeliveries lists one consumer's durable delivery state for operational
// reconciliation. It does not claim rows and can be used after a missed wake
// notification.
func (r *Repository) ListDeliveries(ctx context.Context, tx Tx, consumerID string, limit int) ([]Delivery, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return nil, err
	}
	if consumerID != strings.TrimSpace(consumerID) {
		return nil, errors.New("consumer id must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("delivery list limit must be positive")
	}
	if limit > 10000 {
		return nil, errors.New("delivery list limit exceeds 10000")
	}
	rows, err := tx.Query(ctx, `
		SELECT event_id::text, status, attempts, claim_generation, available_at, COALESCE(claimed_by, ''),
		       claimed_until, terminal_at, evidence::text
		FROM event.event_delivery WHERE consumer_id = $1::uuid
		ORDER BY available_at, event_id LIMIT $2`, consumerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list event deliveries: %w", err)
	}
	defer rows.Close()
	result := make([]Delivery, 0, limit)
	for rows.Next() {
		var d Delivery
		var claimedUntil, terminalAt pgtype.Timestamptz
		var evidence string
		if err := rows.Scan(&d.EventID, &d.Status, &d.Attempts, &d.ClaimGeneration, &d.AvailableAt, &d.ClaimedBy, &claimedUntil, &terminalAt, &evidence); err != nil {
			return nil, fmt.Errorf("scan event delivery: %w", err)
		}
		d.ConsumerID = consumerID
		if claimedUntil.Valid {
			d.ClaimedUntil = claimedUntil.Time.UTC()
		}
		if terminalAt.Valid {
			d.TerminalAt = terminalAt.Time.UTC()
		}
		d.Evidence = json.RawMessage(evidence)
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list event deliveries: %w", err)
	}
	return result, nil
}

// GetEvent reads one durable event by identity.
func (r *Repository) GetEvent(ctx context.Context, tx Tx, eventID string) (Event, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return Event{}, err
	}
	if err := validateUUID("event id", eventID); err != nil {
		return Event{}, err
	}
	var e Event
	var correlation string
	var payload string
	if err := tx.QueryRow(ctx, `
		SELECT event_id::text, scope_id, aggregate_type, aggregate_id, aggregate_version,
		       event_type, schema_version, occurred_at, COALESCE(correlation_id::text, ''), payload::text
		FROM event.event_log WHERE event_id = $1::uuid`, eventID).
		Scan(&e.EventID, &e.ScopeID, &e.AggregateType, &e.AggregateID, &e.AggregateVersion,
			&e.EventType, &e.SchemaVersion, &e.OccurredAt, &correlation, &payload); err != nil {
		return Event{}, fmt.Errorf("get durable event: %w", err)
	}
	e.CorrelationID, e.Payload = correlation, json.RawMessage(payload)
	e.OccurredAt = e.OccurredAt.UTC()
	return e, nil
}

func validateTxContext(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("event PostgreSQL transaction is nil")
	}
	if ctx == nil {
		return errors.New("event context is nil")
	}
	return nil
}

func boundedID(label, value string, max int) (string, error) {
	trimmed := strings.TrimSpace(value)
	if value != trimmed {
		return "", fmt.Errorf("%s must not contain surrounding whitespace", label)
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if len(value) > max {
		return "", fmt.Errorf("%s exceeds %d bytes", label, max)
	}
	return value, nil
}

func validateUUID(label, value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return fmt.Errorf("%s must be a UUID", label)
	}
	var raw [16]byte
	if _, err := hex.Decode(raw[:], []byte(strings.ReplaceAll(value, "-", ""))); err != nil {
		return fmt.Errorf("%s must be a UUID: %w", label, err)
	}
	return nil
}

func canonicalObject(raw json.RawMessage, maxBytes int64) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var validated json.RawMessage
	if err := strictjson.DecodeWithOptions(raw, &validated, strictjson.Options{
		MaxBytes:           maxBytes,
		MaxDepth:           100,
		DuplicateKeys:      strictjson.CaseSensitiveKeys,
		AllowUnknownFields: true,
	}); err != nil {
		return nil, err
	}
	// Preserve JSON numbers while normalizing object key order. Decoding into
	// float64 would make large integer event values lossy during replay checks.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("must be a JSON object")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	if int64(len(canonical)) > maxBytes {
		return nil, fmt.Errorf("exceeds %d bytes after canonicalization", maxBytes)
	}
	return canonical, nil
}

func nonEmptyObject(raw json.RawMessage) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if len(object) == 0 {
		return nil, errors.New("must contain at least one evidence field")
	}
	return raw, nil
}

// uuidv7 creates a sortable UUIDv7 without an additional dependency. The
// timestamp occupies the RFC 9562 48-bit millisecond prefix; random bits fill
// the remaining fields and set the version/variant bits.
func uuidv7() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	ms := uint64(time.Now().UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:]), nil
}
