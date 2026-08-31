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
	"sort"
	"strings"
	"time"

	eventsdb "github.com/flidai/leapview/internal/platform/events/postgres/internal/db"
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

// Repository is operationally stateless. The non-zero marker gives each
// allocated authority a distinct Go identity so composition can prove that
// adapters retain the exact canonical authority instead of a sibling object.
type Repository struct{ identity byte }

//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the forward event capability schema. It contains no
// transaction control; callers apply it using their migration transaction.
func SchemaSQL() string { return schemaSQL }

// New returns a stateless event repository.
func New() *Repository { return &Repository{} }

// ErrDeliveryClaimLost reports that a delivery transition no longer owns the
// exact worker/generation fence supplied by the caller.
var ErrDeliveryClaimLost = errors.New("event delivery is not claimed by worker")

// ErrUnsupportedTransactionIsolation reports that a registry-fenced event
// operation was attempted outside PostgreSQL READ COMMITTED. The fan-out
// protocol relies on each SQL command receiving a fresh snapshot after the
// registry lock completes; stronger isolation levels retain the transaction's
// initial snapshot and can therefore produce a mixed enrollment/retirement
// view. Callers must begin a READ COMMITTED transaction instead of retrying
// under a stronger isolation level.
var ErrUnsupportedTransactionIsolation = errors.New("event protocol requires READ COMMITTED transaction isolation")

// EventInput describes one domain event. EventID is optional; when omitted a
// UUIDv7 is generated.
type EventInput struct {
	EventID       string
	ScopeID       string
	AggregateType string
	AggregateID   string
	EventType     string
	SchemaVersion int64
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
// is enabled by the repository. AggregateTypes is a required, fail-closed
// allowlist; enrollment canonicalizes it to sorted unique values.
type ConsumerInput struct {
	ConsumerID     string
	ConsumerKey    string
	ReplayFrom     time.Time
	Metadata       json.RawMessage
	AggregateTypes []string
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
	AggregateTypes   []string
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

// ReplayOptions controls an explicit single-delivery replay. Evidence is
// mandatory, must be a non-empty JSON object, and is persisted with the
// delivery transition for auditability.
type ReplayOptions struct {
	ConsumerID string
	EventID    string
	Evidence   json.RawMessage
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
	if err := requireReadCommitted(ctx, tx); err != nil {
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
	if err := validateUUIDv7("event id", eventID); err != nil {
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
	// Serialize same-identity producers before the idempotency lookup. Without
	// this lock two concurrent transactions can both miss the predecessor and
	// one will consume an aggregate version before failing on the event PK.
	if err := eventsdb.New(tx).LockEventIdentity(ctx, eventID); err != nil {
		return Event{}, fmt.Errorf("lock event identity: %w", err)
	}
	// Explicit identities make producer retries idempotent. A previously
	// committed event already has its fan-out rows because insertion is one
	// transaction; return that immutable record instead of allocating another
	// aggregate version.
	existingRow, err := eventsdb.New(tx).GetEventByID(ctx, uuidParam(eventID))
	if err == nil {
		existing := eventFromRow(existingRow)
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
		payloadEqual, payloadErr := eventsdb.New(tx).EventPayloadEqual(ctx, eventsdb.EventPayloadEqualParams{EventID: uuidParam(eventID), Payload: payload})
		if payloadErr != nil {
			return Event{}, fmt.Errorf("compare existing event payload: %w", payloadErr)
		}
		if !payloadEqual {
			return Event{}, &EventConflictError{EventID: eventID, Field: "payload"}
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Event{}, fmt.Errorf("check event identity: %w", err)
	}

	// Insert the explicit aggregate row first, then atomically increment it.
	// The UPDATE obtains the row lock and never derives a version from MAX().
	if err := eventsdb.New(tx).EnsureEventAggregate(ctx, eventsdb.EnsureEventAggregateParams{ScopeID: scope, AggregateType: aggregateType, AggregateID: aggregateID}); err != nil {
		return Event{}, fmt.Errorf("ensure event aggregate: %w", err)
	}
	version, err := eventsdb.New(tx).AllocateAggregateVersion(ctx, eventsdb.AllocateAggregateVersionParams{ScopeID: scope, AggregateType: aggregateType, AggregateID: aggregateID})
	if err != nil {
		return Event{}, fmt.Errorf("allocate aggregate version: %w", err)
	}
	// occurred_at and payload are database-owned. The trigger and this statement
	// ensure the authoritative timestamp comes from PostgreSQL's clock, while
	// returning payload::text keeps the in-memory result identical to what a
	// concurrent reader will observe after jsonb canonicalization.
	correlation := uuidParam(correlationID)
	inserted, err := eventsdb.New(tx).InsertEvent(ctx, eventsdb.InsertEventParams{EventID: uuidParam(eventID), ScopeID: scope, AggregateType: aggregateType, AggregateID: aggregateID, AggregateVersion: int64(version), EventType: eventType, SchemaVersion: in.SchemaVersion, CorrelationID: correlation, Payload: payload})
	if err != nil {
		return Event{}, fmt.Errorf("insert durable event: %w", err)
	}
	if !inserted.OccurredAt.Valid {
		return Event{}, errors.New("insert durable event returned null timestamp")
	}
	if inserted.Payload == "" {
		return Event{}, errors.New("insert durable event returned empty payload")
	}
	occurredAt := inserted.OccurredAt.Time.UTC()

	// This statement must complete before the consumer scan below.  Do not
	// combine it with the scan in a CTE: READ COMMITTED command snapshots would
	// otherwise permit a mixed pre/post enrollment view.
	registry, err := eventsdb.New(tx).LockFanoutRegistryForKeyShare(ctx)
	if err != nil {
		return Event{}, fmt.Errorf("acquire event fan-out fence: %w", err)
	}
	if !registry {
		return Event{}, errors.New("event fan-out registry row is invalid")
	}
	consumerIDs, err := eventsdb.New(tx).ListFanoutConsumers(ctx, aggregateType)
	if err != nil {
		return Event{}, fmt.Errorf("scan event consumers: %w", err)
	}
	for _, consumerID := range consumerIDs {
		if err := eventsdb.New(tx).InsertEventDelivery(ctx, eventsdb.InsertEventDeliveryParams{ConsumerID: uuidParam(consumerID), EventID: uuidParam(eventID)}); err != nil {
			return Event{}, fmt.Errorf("insert event delivery: %w", err)
		}
	}
	if err := eventsdb.New(tx).NotifyEvent(ctx, eventsdb.NotifyEventParams{Channel: NotificationChannel, Payload: eventID}); err != nil {
		return Event{}, fmt.Errorf("publish durable event wake hint: %w", err)
	}
	return Event{EventID: eventID, ScopeID: scope, AggregateType: aggregateType,
		AggregateID: aggregateID, AggregateVersion: int64(version), EventType: eventType,
		SchemaVersion: in.SchemaVersion, OccurredAt: occurredAt, CorrelationID: correlationID,
		Payload: json.RawMessage(inserted.Payload)}, nil
}

// EnrollConsumer creates a backfilling consumer while holding the registry
// FOR UPDATE fence.  It records an exact replay boundary (timestamp plus UUID
// tie-breaker), creates a live retention root, and returns only after the
// caller's transaction can commit that boundary.
func (r *Repository) EnrollConsumer(ctx context.Context, tx Tx, in ConsumerInput) (Consumer, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return Consumer{}, err
	}
	if err := requireReadCommitted(ctx, tx); err != nil {
		return Consumer{}, err
	}
	key, err := boundedID("consumer key", in.ConsumerKey, 255)
	if err != nil {
		return Consumer{}, err
	}
	aggregateTypes, err := canonicalAggregateTypes(in.AggregateTypes)
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
	registry, err := eventsdb.New(tx).LockFanoutRegistryForUpdate(ctx)
	if err != nil {
		return Consumer{}, fmt.Errorf("acquire enrollment fence: %w", err)
	}
	if !registry {
		return Consumer{}, errors.New("event fan-out registry row is invalid")
	}
	consumerCount, err := eventsdb.New(tx).CountActiveConsumers(ctx)
	if err != nil {
		return Consumer{}, fmt.Errorf("count durable event consumers: %w", err)
	}
	if consumerCount >= MaxDurableConsumers {
		return Consumer{}, fmt.Errorf("durable consumer limit %d reached", MaxDurableConsumers)
	}
	floorValue, err := eventsdb.New(tx).GetRetentionFloorForShare(ctx)
	if err != nil {
		return Consumer{}, fmt.Errorf("read event retention floor: %w", err)
	}
	if !floorValue.Valid {
		return Consumer{}, errors.New("event retention floor is null")
	}
	floor := floorValue.Time.UTC()
	if replayFrom.Before(floor) {
		return Consumer{}, fmt.Errorf("consumer replay_from %s precedes retention floor %s", replayFrom, floor)
	}

	// Capture the latest committed event only after the fence is acquired.
	// Producers blocked on this fence resume after enrollment commit and fan out
	// directly to the newly-created backfilling consumer.
	var replayUntil time.Time
	var replayUntilID string
	latest, err := eventsdb.New(tx).GetLatestEventBoundary(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		replayUntil = replayFrom
		replayUntilID = ""
	} else if err != nil {
		return Consumer{}, fmt.Errorf("capture consumer replay boundary: %w", err)
	} else if !latest.OccurredAt.Valid {
		return Consumer{}, errors.New("latest event boundary timestamp is null")
	} else {
		replayUntil, replayUntilID = latest.OccurredAt.Time.UTC(), latest.EventID
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
	if err := eventsdb.New(tx).InsertConsumer(ctx, eventsdb.InsertConsumerParams{ConsumerID: uuidParam(consumerID), ConsumerKey: key, ReplayFrom: timestampParam(replayFrom), Metadata: metadata}); err != nil {
		return Consumer{}, fmt.Errorf("enroll event consumer: %w", err)
	}
	for _, aggregateType := range aggregateTypes {
		if err := eventsdb.New(tx).InsertConsumerAggregate(ctx, eventsdb.InsertConsumerAggregateParams{ConsumerID: uuidParam(consumerID), AggregateType: aggregateType}); err != nil {
			return Consumer{}, fmt.Errorf("persist event consumer aggregate filter: %w", err)
		}
	}
	if err := eventsdb.New(tx).InsertRetentionRoot(ctx, eventsdb.InsertRetentionRootParams{RootID: uuidParam(rootID), ConsumerID: uuidParam(consumerID), ReplayFrom: timestampParam(replayFrom), ReplayUntil: timestampParam(replayUntil), ReplayUntilEventID: uuidParam(replayUntilID), Evidence: []byte(`{"kind":"event_backfill"}`)}); err != nil {
		return Consumer{}, fmt.Errorf("create event retention root: %w", err)
	}
	return Consumer{ConsumerID: consumerID, ConsumerKey: key,
		Lifecycle: "backfilling", ReplayFrom: replayFrom, Metadata: metadata,
		AggregateTypes: append([]string(nil), aggregateTypes...)}, nil
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
	if err := requireReadCommitted(ctx, tx); err != nil {
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
	lifecycle, err := eventsdb.New(tx).GetConsumerLifecycleForUpdate(ctx, uuidParam(consumerID))
	if err != nil {
		return BackfillResult{}, fmt.Errorf("lock event consumer: %w", err)
	}
	if lifecycle != "backfilling" {
		return BackfillResult{Done: lifecycle == "enabled"}, nil
	}
	rootRow, err := eventsdb.New(tx).GetRetentionRootForUpdate(ctx, uuidParam(consumerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return BackfillResult{}, errors.New("event consumer has no live retention root")
	}
	if err != nil {
		return BackfillResult{}, fmt.Errorf("lock event retention root: %w", err)
	}
	rootID := rootRow.RootID
	if !rootRow.ReplayFrom.Valid {
		return BackfillResult{}, errors.New("event retention root replay_from is null")
	}
	replayFrom := rootRow.ReplayFrom.Time.UTC()
	cursor, err := eventsdb.New(tx).GetRetentionReplayCursor(ctx, uuidParam(rootID))
	if err != nil {
		return BackfillResult{}, fmt.Errorf("read event replay cursor: %w", err)
	}
	var replayUntil, frontierAt time.Time
	replayUntilID, frontierID := cursor.ReplayUntilEventID, cursor.FrontierEventID
	if cursor.ReplayUntil.Valid {
		replayUntil = cursor.ReplayUntil.Time.UTC()
	}
	if cursor.FrontierOccurredAt.Valid {
		frontierAt = cursor.FrontierOccurredAt.Time.UTC()
	}
	hasUntil, hasFrontier := cursor.ReplayUntil.Valid, cursor.FrontierOccurredAt.Valid && frontierID != ""
	rows, err := eventsdb.New(tx).ListBackfillEvents(ctx, eventsdb.ListBackfillEventsParams{ConsumerID: uuidParam(consumerID), ReplayFrom: timestampParam(replayFrom), HasUntil: hasUntil, ReplayUntil: timestampParam(replayUntil), ReplayUntilEventID: uuidParam(replayUntilID), HasFrontier: hasFrontier, FrontierAt: timestampParam(frontierAt), FrontierEventID: uuidParam(frontierID), PLimit: int32(limit)})
	if err != nil {
		return BackfillResult{}, fmt.Errorf("scan event backfill batch: %w", err)
	}
	result := BackfillResult{Scanned: len(rows)}
	for _, c := range rows {
		ct, err := eventsdb.New(tx).InsertBackfillDelivery(ctx, eventsdb.InsertBackfillDeliveryParams{ConsumerID: uuidParam(consumerID), EventID: uuidParam(c.EventID)})
		if err != nil {
			return BackfillResult{}, fmt.Errorf("insert backfill delivery: %w", err)
		}
		result.Inserted += int(ct.RowsAffected())
		result.FrontierEventID, result.FrontierOccurred = c.EventID, c.OccurredAt.Time.UTC()
	}
	if len(rows) > 0 {
		if err := eventsdb.New(tx).AdvanceConsumerFrontier(ctx, eventsdb.AdvanceConsumerFrontierParams{ConsumerID: uuidParam(consumerID), EventID: uuidParam(result.FrontierEventID), OccurredAt: timestampParam(result.FrontierOccurred)}); err != nil {
			return BackfillResult{}, fmt.Errorf("advance consumer frontier: %w", err)
		}
		if err := eventsdb.New(tx).AdvanceRetentionFrontier(ctx, eventsdb.AdvanceRetentionFrontierParams{RootID: uuidParam(rootID), EventID: uuidParam(result.FrontierEventID), OccurredAt: timestampParam(result.FrontierOccurred)}); err != nil {
			return BackfillResult{}, fmt.Errorf("advance retention frontier: %w", err)
		}
	}
	if len(rows) < limit {
		if err := eventsdb.New(tx).EnableConsumer(ctx, uuidParam(consumerID)); err != nil {
			return BackfillResult{}, fmt.Errorf("enable event consumer: %w", err)
		}
		if err := eventsdb.New(tx).ExpireRetentionRoot(ctx, uuidParam(rootID)); err != nil {
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
	if err := requireReadCommitted(ctx, tx); err != nil {
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
	registry, err := eventsdb.New(tx).LockFanoutRegistryForUpdate(ctx)
	if err != nil {
		return fmt.Errorf("acquire retirement fence: %w", err)
	}
	if !registry {
		return errors.New("event fan-out registry row is invalid")
	}
	lifecycle, err := eventsdb.New(tx).GetConsumerLifecycleForUpdate(ctx, uuidParam(consumerID))
	if err != nil {
		return fmt.Errorf("lock event consumer for retirement: %w", err)
	}
	if lifecycle == "retired" {
		return nil
	}
	unresolved, err := eventsdb.New(tx).CountUnresolvedDeliveries(ctx, uuidParam(consumerID))
	if err != nil {
		return fmt.Errorf("count unresolved event deliveries: %w", err)
	}
	if unresolved > 0 && !opts.Waive {
		return fmt.Errorf("consumer has %d unresolved deliveries", unresolved)
	}
	if unresolved > 0 {
		if err := eventsdb.New(tx).WaiveDeliveries(ctx, eventsdb.WaiveDeliveriesParams{Evidence: evidence, ConsumerID: uuidParam(consumerID)}); err != nil {
			return fmt.Errorf("waive event deliveries: %w", err)
		}
	}
	if err := eventsdb.New(tx).RetireConsumer(ctx, uuidParam(consumerID)); err != nil {
		return fmt.Errorf("retire event consumer: %w", err)
	}
	if err := eventsdb.New(tx).ExpireConsumerRoots(ctx, eventsdb.ExpireConsumerRootsParams{Evidence: evidence, ConsumerID: uuidParam(consumerID)}); err != nil {
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
	registry, err := eventsdb.New(tx).LockFanoutRegistryForUpdate(ctx)
	if err != nil {
		return fmt.Errorf("acquire lifecycle fence: %w", err)
	}
	if !registry {
		return errors.New("event fan-out registry row is invalid")
	}
	ct, err := eventsdb.New(tx).SetConsumerLifecycle(ctx, eventsdb.SetConsumerLifecycleParams{Lifecycle: lifecycle, ConsumerID: uuidParam(consumerID)})
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
	leaseMicros := durationMicros(lease)
	lifecycle, err := eventsdb.New(tx).GetConsumerLifecycleForShare(ctx, uuidParam(consumerID))
	if err != nil {
		return nil, fmt.Errorf("read event consumer for claim: %w", err)
	}
	if lifecycle != "enabled" {
		return nil, fmt.Errorf("consumer lifecycle %q cannot claim deliveries", lifecycle)
	}
	rows, err := eventsdb.New(tx).ClaimDeliveries(ctx, eventsdb.ClaimDeliveriesParams{
		WorkerID: pgtype.Text{String: workerID, Valid: true}, LeaseMicros: leaseMicros,
		ConsumerID: uuidParam(consumerID), PLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("claim event deliveries: %w", err)
	}
	claimed := make([]Delivery, 0, limit)
	for _, row := range rows {
		claimedBy := ""
		if row.ClaimedBy.Valid {
			claimedBy = row.ClaimedBy.String
		}
		claimedUntil, terminalAt := time.Time{}, time.Time{}
		if row.ClaimedUntil.Valid {
			claimedUntil = row.ClaimedUntil.Time
		}
		if row.TerminalAt.Valid {
			terminalAt = row.TerminalAt.Time
		}
		if !row.AvailableAt.Valid {
			return nil, errors.New("claimed event delivery has null available_at")
		}
		claimed = append(claimed, deliveryFromValues(consumerID, row.DEventID, row.Status, row.Attempts, row.ClaimGeneration, row.AvailableAt.Time, claimedBy, claimedUntil, terminalAt, row.DEvidence))
	}
	return claimed, nil
}

// ConsumerByID returns the durable enrollment and its aggregate filter. It is
// a read-side admission check for runtime capabilities: callers must compare
// the returned identity, lifecycle, and canonical filter with their explicit
// configuration before attempting a claim. Unlike the fan-out mutation
// protocol, this read does not coordinate a registry fence or depend on fresh
// per-command snapshots, so it is valid in the caller's existing isolation
// level (and deliberately does not impose the READ COMMITTED guard).
func (r *Repository) ConsumerByID(ctx context.Context, tx Tx, consumerID string) (Consumer, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return Consumer{}, err
	}
	if consumerID != strings.TrimSpace(consumerID) {
		return Consumer{}, errors.New("consumer id must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return Consumer{}, err
	}
	row, err := eventsdb.New(tx).GetConsumerByID(ctx, uuidParam(consumerID))
	if err != nil {
		return Consumer{}, fmt.Errorf("get event consumer: %w", err)
	}
	aggregates, err := eventsdb.New(tx).ListConsumerAggregates(ctx, uuidParam(consumerID))
	if err != nil {
		return Consumer{}, fmt.Errorf("get event consumer aggregate filter: %w", err)
	}
	// Aggregate allowlists are policy input, not presentation data. Validate
	// and canonicalize the rows on read as a defensive boundary in case a
	// migration or privileged SQL writer ever bypasses the enrollment path.
	// An empty/corrupt allowlist must fail closed instead of becoming an
	// accidental all-events consumer.
	aggregateTypes, err := canonicalAggregateTypes(aggregates)
	if err != nil {
		return Consumer{}, fmt.Errorf("validate event consumer aggregate filter: %w", err)
	}
	if !row.ReplayFrom.Valid || !row.CreatedAt.Valid || !row.UpdatedAt.Valid {
		return Consumer{}, errors.New("event consumer has null timestamp")
	}
	metadata := json.RawMessage(row.Metadata)
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	consumer := Consumer{
		ConsumerID: row.ConsumerID, ConsumerKey: row.ConsumerKey, Lifecycle: row.Lifecycle,
		ReplayFrom: row.ReplayFrom.Time.UTC(), Metadata: metadata, CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(), AggregateTypes: append([]string(nil), aggregateTypes...),
	}
	if row.FrontierEventID != "" {
		consumer.FrontierEventID = row.FrontierEventID
	}
	if row.FrontierOccurredAt.Valid {
		consumer.FrontierOccurred = row.FrontierOccurredAt.Time.UTC()
	}
	return consumer, nil
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
	if outcome == DeliveryWaived {
		if _, err := nonEmptyObject(evidenceJSON); err != nil {
			return fmt.Errorf("delivery waiver evidence: %w", err)
		}
	}
	ct, err := eventsdb.New(tx).CompleteDelivery(ctx, eventsdb.CompleteDeliveryParams{
		Status: string(outcome), Evidence: evidenceJSON, ConsumerID: uuidParam(consumerID),
		EventID: uuidParam(eventID), WorkerID: pgtype.Text{String: workerID, Valid: true},
		ClaimGeneration: claimGeneration,
	})
	if err != nil {
		return fmt.Errorf("complete event delivery: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return ErrDeliveryClaimLost
	}
	return nil
}

// Replay marks a terminal delivery pending again by event identity. It is
// useful for explicit poison resolution and is intentionally consumer-scoped.
// The fan-out registry and consumer lifecycle locks serialize this transition
// with retirement, so a replay can never leave a pending row on a retired
// consumer.
func (r *Repository) Replay(ctx context.Context, tx Tx, opts ReplayOptions) error {
	if err := validateTxContext(ctx, tx); err != nil {
		return err
	}
	if err := requireReadCommitted(ctx, tx); err != nil {
		return err
	}
	consumerID, eventID := opts.ConsumerID, opts.EventID
	if consumerID != strings.TrimSpace(consumerID) || eventID != strings.TrimSpace(eventID) {
		return errors.New("delivery identities must not contain surrounding whitespace")
	}
	if err := validateUUID("consumer id", consumerID); err != nil {
		return err
	}
	if err := validateUUID("event id", eventID); err != nil {
		return err
	}
	evidence, err := canonicalObject(opts.Evidence, 32768)
	if err != nil {
		return fmt.Errorf("replay evidence: %w", err)
	}
	if _, err := nonEmptyObject(evidence); err != nil {
		return fmt.Errorf("replay evidence: %w", err)
	}
	registry, err := eventsdb.New(tx).LockFanoutRegistryForUpdate(ctx)
	if err != nil {
		return fmt.Errorf("acquire replay fence: %w", err)
	}
	if !registry {
		return errors.New("event fan-out registry row is invalid")
	}
	lifecycle, err := eventsdb.New(tx).GetConsumerLifecycleForUpdate(ctx, uuidParam(consumerID))
	if err != nil {
		return fmt.Errorf("lock event consumer for replay: %w", err)
	}
	if lifecycle != "enabled" && lifecycle != "paused" {
		return fmt.Errorf("consumer lifecycle %q cannot replay deliveries", lifecycle)
	}
	ct, err := eventsdb.New(tx).ReplayDelivery(ctx, eventsdb.ReplayDeliveryParams{ConsumerID: uuidParam(consumerID), EventID: uuidParam(eventID), Evidence: evidence})
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
	delayMicros := durationMicros(opts.Delay)
	evidence, err := canonicalObject(opts.Evidence, 32768)
	if err != nil {
		return fmt.Errorf("retry evidence: %w", err)
	}
	ct, err := eventsdb.New(tx).RetryDelivery(ctx, eventsdb.RetryDeliveryParams{
		MaxAttempts: opts.MaxAttempts, DelayMicros: delayMicros, Evidence: evidence,
		ConsumerID: uuidParam(opts.ConsumerID), EventID: uuidParam(opts.EventID),
		WorkerID: pgtype.Text{String: opts.WorkerID, Valid: true}, ClaimGeneration: opts.ClaimGeneration,
	})
	if err != nil {
		return fmt.Errorf("retry event delivery: %w", err)
	}
	if ct.RowsAffected() != 1 {
		return ErrDeliveryClaimLost
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
	// Keep each invocation bounded to one owner-function batch. Callers that
	// need to drain more rows can commit and invoke Prune again; a single
	// transaction must not turn a retention sweep into an unbounded delete.
	const batch = 1000
	removed, err := eventsdb.New(tx).PruneEventLog(ctx, eventsdb.PruneEventLogParams{Before: timestampParam(before), Batch: batch})
	if err != nil {
		return 0, fmt.Errorf("prune durable events: %w", err)
	}
	return removed, nil
}

// RetentionFloor returns the durable event replay floor.
func (r *Repository) RetentionFloor(ctx context.Context, tx Tx) (time.Time, error) {
	if err := validateTxContext(ctx, tx); err != nil {
		return time.Time{}, err
	}
	floorValue, err := eventsdb.New(tx).GetRetentionFloor(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("read event retention floor: %w", err)
	}
	if !floorValue.Valid {
		return time.Time{}, errors.New("event retention floor is null")
	}
	return floorValue.Time.UTC(), nil
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
	rows, err := eventsdb.New(tx).ListDeliveries(ctx, eventsdb.ListDeliveriesParams{ConsumerID: uuidParam(consumerID), PLimit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list event deliveries: %w", err)
	}
	result := make([]Delivery, 0, limit)
	for _, row := range rows {
		if !row.AvailableAt.Valid {
			return nil, errors.New("event delivery has null available_at")
		}
		claimedBy := ""
		if row.ClaimedBy.Valid {
			claimedBy = row.ClaimedBy.String
		}
		claimedUntil, terminalAt := time.Time{}, time.Time{}
		if row.ClaimedUntil.Valid {
			claimedUntil = row.ClaimedUntil.Time
		}
		if row.TerminalAt.Valid {
			terminalAt = row.TerminalAt.Time
		}
		result = append(result, deliveryFromValues(consumerID, row.EventID, row.Status, row.Attempts, row.ClaimGeneration, row.AvailableAt.Time, claimedBy, claimedUntil, terminalAt, row.Evidence))
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
	row, err := eventsdb.New(tx).GetEventByID(ctx, uuidParam(eventID))
	if err != nil {
		return Event{}, fmt.Errorf("get durable event: %w", err)
	}
	if !row.OccurredAt.Valid {
		return Event{}, errors.New("durable event has null occurred_at")
	}
	return eventFromRow(row), nil
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

// requireReadCommitted verifies the caller-owned transaction before any
// protocol query or mutation. This guard intentionally rejects repeatable
// read and serializable transactions rather than attempting to compensate for
// their transaction-wide snapshot semantics.
func requireReadCommitted(ctx context.Context, tx Tx) error {
	isolation, err := eventsdb.New(tx).CurrentTransactionIsolation(ctx)
	if err != nil {
		return fmt.Errorf("%w: read transaction isolation: %v", ErrUnsupportedTransactionIsolation, err)
	}
	if strings.ToLower(strings.TrimSpace(isolation)) != "read committed" {
		return fmt.Errorf("%w: got %q", ErrUnsupportedTransactionIsolation, isolation)
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

// MaxConsumerAggregateTypes bounds per-consumer fan-out filter cardinality.
// It exceeds the largest currently allowlisted topic family (five aggregate
// types) while preventing unbounded filter rows and duplicate-input work.
const MaxConsumerAggregateTypes = 16

// canonicalAggregateTypes validates, deduplicates, and sorts a consumer's
// aggregate-family allowlist. Empty filters are rejected deliberately: an
// omitted or empty policy must never silently become an all-events consumer.
func canonicalAggregateTypes(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("consumer aggregate types are required")
	}
	if len(values) > MaxConsumerAggregateTypes {
		return nil, fmt.Errorf("consumer aggregate types exceed %d", MaxConsumerAggregateTypes)
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for i, value := range values {
		aggregateType, err := boundedID(fmt.Sprintf("consumer aggregate type %d", i), value, 255)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[aggregateType]; exists {
			continue
		}
		seen[aggregateType] = struct{}{}
		result = append(result, aggregateType)
	}
	sort.Strings(result)
	return result, nil
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

// validateUUIDv7 enforces the event authority's textual identity contract.
// UUID values are canonical only when they use the lower-case RFC 9562
// representation, RFC 4122 variant, and version 7. Keep this check at the
// repository boundary because PostgreSQL's uuid type normalizes textual input
// before storage, making the original casing unavailable to a table check.
func validateUUIDv7(label, value string) error {
	if value == "" || value != strings.TrimSpace(value) || value != strings.ToLower(value) {
		return fmt.Errorf("%s must be a canonical lowercase UUIDv7", label)
	}
	if err := validateUUID(label, value); err != nil {
		return fmt.Errorf("%s must be a canonical lowercase UUIDv7", label)
	}
	var raw [16]byte
	if _, err := hex.Decode(raw[:], []byte(strings.ReplaceAll(value, "-", ""))); err != nil {
		return fmt.Errorf("%s must be a canonical lowercase UUIDv7", label)
	}
	if raw[6]>>4 != 7 || raw[8]&0xc0 != 0x80 {
		return fmt.Errorf("%s must be a canonical lowercase UUIDv7", label)
	}
	return nil
}

// uuidParam converts an already-validated textual UUID into pgx's nullable
// UUID value. Empty strings intentionally remain invalid UUIDs so nullable
// SQL parameters (for example correlation_id or replay boundary IDs) are
// encoded as SQL NULL rather than as a malformed value.
func uuidParam(value string) pgtype.UUID {
	if value == "" {
		return pgtype.UUID{}
	}
	var out pgtype.UUID
	if err := out.Scan(value); err != nil {
		// Callers validate UUID text before reaching SQL. Keep this helper
		// allocation-free on the happy path; an invalid value is represented as
		// NULL and the database will reject it if a caller bypasses validation.
		return pgtype.UUID{}
	}
	return out
}

func timestampParam(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

// durationMicros rounds a positive duration up to PostgreSQL's finest
// interval precision.  Passing the integer separately (rather than building
// an interval string) avoids float truncation for sub-second leases and keeps
// all caller values as typed SQL parameters.
func durationMicros(value time.Duration) int64 {
	if value <= 0 {
		return 0
	}
	nanos := int64(value)
	return (nanos + int64(time.Microsecond) - 1) / int64(time.Microsecond)
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
