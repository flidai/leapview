// Package postgres owns the application control-plane DuckLake ledger. Its
// schema is installed in the leapview_control baseline; it is not the
// separately provisioned leapview_ducklake catalog database. DuckDB remains
// the local data-plane: this package never opens a DuckDB connection, stores
// catalog bytes, or guesses a snapshot from catalog recency.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native pgx query surface.  A pool, connection, or caller-owned
// transaction may be supplied; transaction variants below preserve atomic
// composition with deployment mutations.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Tx = DBTX

var (
	ErrInvalid           = errors.New("invalid DuckLake PostgreSQL identity")
	ErrConflict          = errors.New("DuckLake PostgreSQL identity conflict")
	ErrNotFound          = errors.New("DuckLake PostgreSQL identity not found")
	ErrNotLive           = errors.New("DuckLake snapshot is not live")
	ErrLeaseExpired      = errors.New("DuckLake snapshot lease is expired")
	ErrStaleFence        = errors.New("DuckLake owner fencing epoch is stale")
	ErrAttemptBusy       = errors.New("DuckLake build attempt is owned by another worker")
	ErrIndeterminate     = errors.New("DuckLake build attempt outcome is indeterminate")
	ErrQuarantined       = errors.New("DuckLake snapshot is quarantined")
	ErrMarkerQuarantined = errors.New("DuckLake physical pool has a marker quarantine")
	ErrCleanupPending    = errors.New("DuckLake snapshot cleanup is pending")
	ErrCleanupBusy       = errors.New("DuckLake snapshot cleanup is owned by another worker")
	ErrClockUnavailable  = errors.New("DuckLake PostgreSQL clock unavailable")
)

const (
	maxID            = 255
	maxSchema        = 128
	maxEvidence      = 32768
	maxAttemptLease  = 24 * time.Hour
	maxSnapshotLease = 24 * time.Hour
)

// CatalogIdentity names the durable catalog binding. Runtime/extension/format
// compatibility is intentionally excluded: upgrades replace the mutable
// CatalogRuntimeCompatibility row while this identity remains stable.
type CatalogIdentity struct {
	PhysicalPoolID  string
	CatalogDatabase string
	CatalogID       string
	CatalogUUID     string
	MetadataSchema  string
	CreatedAt       time.Time
}

type SnapshotRef struct {
	PhysicalPoolID string
	CatalogID      string
	SnapshotID     int64
}

type GenerationBinding struct {
	DeliveryID             string
	GenerationID           string
	AttemptID              string
	PhysicalPoolID         string
	CatalogID              string
	SnapshotID             int64
	RelationManifestDigest string
	CompatibilityDigest    string
	ServingArtifactDigest  string
	RequestDigest          string
	PlanDigest             string
	FencingEpoch           int64
	BoundAt                time.Time
}

type AttemptState string

const (
	AttemptRunning       AttemptState = "running"
	AttemptCommitted     AttemptState = "committed"
	AttemptAborted       AttemptState = "aborted"
	AttemptIndeterminate AttemptState = "indeterminate"
	AttemptFenced        AttemptState = "fenced"
)

// AttemptEvidence is an exact external-attempt ledger. CommitMarker is the
// canonical JSON written by DuckLake before the local transaction commits.
// TerminationEvidence must positively establish abort/termination before a
// retry can use a new attempt.
type AttemptEvidence struct {
	AttemptID           string
	RequestDigest       string
	PlanDigest          string
	PhysicalPoolID      string
	CatalogID           string
	OwnerID             string
	FencingEpoch        int64
	LeaseExpiresAt      time.Time
	SessionIdentity     string
	State               AttemptState
	SnapshotID          int64
	CommitMarker        string
	TerminationEvidence json.RawMessage
	CreatedAt           time.Time
	UpdatedAt           time.Time
	TerminalAt          time.Time
}

// MarkerQuarantineReason identifies why exact external marker reconciliation
// could not safely select one committed snapshot.
type MarkerQuarantineReason string

const (
	MarkerQuarantineDuplicate        MarkerQuarantineReason = "duplicate"
	MarkerQuarantineDigestMismatch   MarkerQuarantineReason = "digest_mismatch"
	MarkerQuarantineIdentityMismatch MarkerQuarantineReason = "identity_mismatch"
)

// MarkerQuarantine is immutable, pool-wide evidence of an external DuckLake
// marker anomaly. It is intentionally separate from AttemptEvidence's
// positive termination evidence: an anomaly does not establish that a
// transaction aborted and therefore gates both successor admission and
// restart recovery.
type MarkerQuarantine struct {
	PhysicalPoolID       string
	CatalogID            string
	AttemptID            string
	RequestDigest        string
	PlanDigest           string
	Reason               MarkerQuarantineReason
	Evidence             json.RawMessage
	ObservedMarkerDigest string
	ObservedSnapshotIDs  []int64
	CreatedAt            time.Time
}

type MarkerQuarantineInput struct {
	PhysicalPoolID       string
	CatalogID            string
	AttemptID            string
	RequestDigest        string
	PlanDigest           string
	Reason               MarkerQuarantineReason
	Evidence             json.RawMessage
	ObservedMarkerDigest string
	ObservedSnapshotIDs  []int64
}

type BeginAttemptInput struct {
	AttemptID       string
	RequestDigest   string
	PlanDigest      string
	PhysicalPoolID  string
	CatalogID       string
	OwnerID         string
	FencingEpoch    int64
	SessionIdentity string
	LeaseExpiresAt  time.Time
}

type CommitAttemptInput struct {
	AttemptID    string
	OwnerID      string
	FencingEpoch int64
	Snapshot     SnapshotRef
	CommitMarker string
	CommittedAt  time.Time
}

type TerminateAttemptInput struct {
	AttemptID    string
	OwnerID      string
	FencingEpoch int64
	Evidence     json.RawMessage
	TerminatedAt time.Time
}

// ReconcileAttemptInput is exact restart evidence for a DuckLake build
// attempt. It intentionally contains the already-resolved snapshot marker;
// marker lookup/resolution belongs to a separate physical adapter. Recovery
// may transition only running or indeterminate rows and never infers an
// outcome from lease expiry alone.
type ReconcileAttemptInput struct {
	AttemptID           string
	OwnerID             string
	FencingEpoch        int64
	Snapshot            SnapshotRef
	CommitMarker        string
	TerminationEvidence json.RawMessage
	SessionTerminated   bool
	SessionIdentity     string
	State               AttemptState
}

type SnapshotLeaseState string

const (
	LeaseActive   SnapshotLeaseState = "active"
	LeaseReleased SnapshotLeaseState = "released"
	LeaseExpired  SnapshotLeaseState = "expired"
)

type SnapshotLease struct {
	LeaseID        string
	DeliveryID     string
	GenerationID   string
	PhysicalPoolID string
	CatalogID      string
	SnapshotID     int64
	OwnerID        string
	FencingEpoch   int64
	State          SnapshotLeaseState
	ExpiresAt      time.Time
	AcquiredAt     time.Time
	ReleasedAt     time.Time
}

type AcquireLeaseInput struct {
	LeaseID        string
	DeliveryID     string
	GenerationID   string
	PhysicalPoolID string
	CatalogID      string
	SnapshotID     int64
	OwnerID        string
	FencingEpoch   int64
	ExpiresAt      time.Time
	AcquiredAt     time.Time
}

type SnapshotRootKind string

const (
	RootCandidate  SnapshotRootKind = "candidate"
	RootGeneration SnapshotRootKind = "generation"
	RootRollback   SnapshotRootKind = "rollback"
	RootRecovery   SnapshotRootKind = "recovery"
	// RootActive is the explicit active-serving reachability kind. Generation
	// remains accepted for compatibility with earlier callers.
	RootActive   SnapshotRootKind = "active"
	RootCache    SnapshotRootKind = "cache"
	RootLineage  SnapshotRootKind = "lineage"
	RootDelivery SnapshotRootKind = "delivery"
)

type SnapshotRoot struct {
	RootID             string
	PhysicalPoolID     string
	CatalogID          string
	SnapshotID         int64
	Kind               SnapshotRootKind
	State              string
	CreatedAt          time.Time
	RetiredAt          time.Time
	ExpiredAt          time.Time
	QuarantinedAt      time.Time
	CleanupCompletedAt time.Time
	Evidence           json.RawMessage
	QuarantineEvidence json.RawMessage
	CleanupEvidence    json.RawMessage
}

type SnapshotRootInput struct {
	RootID         string
	PhysicalPoolID string
	CatalogID      string
	SnapshotID     int64
	Kind           SnapshotRootKind
	CreatedAt      time.Time
	// Evidence is immutable bounded metadata describing why this root exists.
	// Identity remains relational (the fields above), never hidden in JSON.
	Evidence json.RawMessage
}

type SnapshotRetentionState string

const (
	RetentionLive            SnapshotRetentionState = "live"
	RetentionRetiring        SnapshotRetentionState = "retiring"
	RetentionExpiring        SnapshotRetentionState = "expiring"
	RetentionExpired         SnapshotRetentionState = "expired"
	RetentionQuarantined     SnapshotRetentionState = "quarantined"
	RetentionCleanupComplete SnapshotRetentionState = "cleanup-complete"
)

type SnapshotRetention struct {
	PhysicalPoolID        string
	CatalogID             string
	SnapshotID            int64
	State                 SnapshotRetentionState
	ProtectedUntil        time.Time
	RetiredAt             time.Time
	ExpiredAt             time.Time
	CleanupOwnerID        string
	CleanupFencingEpoch   int64
	CleanupLeaseExpiresAt time.Time
	QuarantinedAt         time.Time
	CleanupCompletedAt    time.Time
	QuarantineEvidence    json.RawMessage
	CleanupEvidence       json.RawMessage
	Evidence              json.RawMessage
	CreatedAt             time.Time
}

// ReaderDrain is deliberately small and safe for metrics/logging. It omits
// session credentials and query payloads while retaining the exact lease
// identity needed to investigate a stuck reader.
type ReaderDrain struct {
	LeaseID, DeliveryID, GenerationID string
	PhysicalPoolID, CatalogID         string
	SnapshotID                        int64
	OwnerID                           string
	FencingEpoch                      int64
	State                             SnapshotLeaseState
	AcquiredAt, ExpiresAt             time.Time
	Overdue, NonDraining              bool
}

type RetentionBacklog struct {
	CleanupPending     int64
	Quarantined        int64
	Orphans            int64
	OverdueReaders     int64
	NonDrainingReaders int64
}

type LeaseFence struct {
	LeaseID      string
	OwnerID      string
	FencingEpoch int64
}

// CleanupFence is a persisted worker claim. A successor claim increments the
// epoch, fencing every stale owner even when its process or connection was
// restarted.
type CleanupFence struct {
	OwnerID        string
	FencingEpoch   int64
	LeaseExpiresAt time.Time
}
type SnapshotCleanupFence = CleanupFence

type SnapshotLeaseClaim struct {
	LeaseID      string
	OwnerID      string
	FencingEpoch int64
}

type Repository struct {
	db DBTX
}

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception:schema-ddl -- capability-owned schema DDL.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository { return &Repository{db: db} }

// DB exposes the configured native control-database handle to composition
// checks. It never opens a connection or transfers lifecycle ownership.
func (r *Repository) DB() DBTX {
	if r == nil {
		return nil
	}
	return r.db
}

// Configured reports whether the repository has a native PostgreSQL handle.
// Transaction ownership remains with the application composition layer: the
// Tx methods below deliberately operate on the caller's transaction rather
// than opening a second one.
func (r *Repository) Configured() bool { return r != nil && r.db != nil }

// TransactionCapable reports whether the configured control-database handle
// can begin a caller-owned PostgreSQL transaction. The ledger's Tx methods
// never begin or commit that transaction themselves, allowing delivery and
// serving authorities to share one control-plane transaction.
func (r *Repository) TransactionCapable() bool {
	if r == nil || r.db == nil {
		return false
	}
	_, ok := r.db.(beginner)
	return ok
}

// QuarantineMarkerTx persists immutable marker-anomaly evidence through a
// caller-owned transaction. Exact replay is idempotent; changed evidence for
// the same pool/catalog/attempt key returns ErrConflict.
func (r *Repository) QuarantineMarkerTx(ctx context.Context, tx Tx, in MarkerQuarantineInput) (MarkerQuarantine, error) {
	if r == nil || tx == nil {
		return MarkerQuarantine{}, ErrInvalid
	}
	return QuarantineMarker(ctx, tx, in)
}

// QuarantineMarker records an anomaly observed while reconciling one external
// attempt. The operation never opens or commits a transaction itself.
func QuarantineMarker(ctx context.Context, tx DBTX, in MarkerQuarantineInput) (MarkerQuarantine, error) {
	if tx == nil || !validMarkerQuarantineInput(in) {
		return MarkerQuarantine{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	evidence, err := canonicalEvidence(in.Evidence)
	if err != nil {
		return MarkerQuarantine{}, fmt.Errorf("%w: marker quarantine evidence is required", ErrInvalid)
	}
	if err := lockMarkerQuarantineScope(ctx, tx, in.PhysicalPoolID); err != nil {
		return MarkerQuarantine{}, err
	}
	observedIDs := append([]int64(nil), in.ObservedSnapshotIDs...)
	if observedIDs == nil {
		observedIDs = []int64{}
	}
	if err := querygen(tx).InsertMarkerQuarantine(ctx, dbgen.InsertMarkerQuarantineParams{
		PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, AttemptID: pgUUID(in.AttemptID),
		RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, Reason: string(in.Reason),
		Evidence: []byte(evidence), ObservedMarkerDigest: in.ObservedMarkerDigest, ObservedSnapshotIds: observedIDs,
	}); err != nil {
		return MarkerQuarantine{}, err
	}
	got, err := LoadMarkerQuarantine(ctx, tx, in.PhysicalPoolID, in.CatalogID, in.AttemptID)
	if errors.Is(err, ErrNotFound) {
		return MarkerQuarantine{}, ErrNotFound
	}
	if err != nil {
		return MarkerQuarantine{}, err
	}
	if !sameMarkerQuarantine(got, in, evidence) {
		return MarkerQuarantine{}, fmt.Errorf("%w: marker quarantine %q", ErrConflict, in.AttemptID)
	}
	return got, nil
}

func (r *Repository) QuarantineMarker(ctx context.Context, in MarkerQuarantineInput) (MarkerQuarantine, error) {
	if r == nil {
		return MarkerQuarantine{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (MarkerQuarantine, error) {
		return QuarantineMarker(ctx, tx, in)
	})
}

func LoadMarkerQuarantine(ctx context.Context, db DBTX, physicalPoolID, catalogID, attemptID string) (MarkerQuarantine, error) {
	if db == nil || !validID(physicalPoolID) || !validID(catalogID) || !validUUID(attemptID) {
		return MarkerQuarantine{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	row, err := querygen(db).GetMarkerQuarantine(ctx, dbgen.GetMarkerQuarantineParams{PhysicalPoolID: physicalPoolID, CatalogID: catalogID, AttemptID: pgUUID(attemptID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return MarkerQuarantine{}, ErrNotFound
	}
	if err != nil {
		return MarkerQuarantine{}, err
	}
	return MarkerQuarantine{
		PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, AttemptID: row.AttemptID,
		RequestDigest: row.RequestDigest, PlanDigest: row.PlanDigest, Reason: MarkerQuarantineReason(row.Reason),
		Evidence: append(json.RawMessage(nil), row.Evidence...), ObservedMarkerDigest: row.ObservedMarkerDigest,
		ObservedSnapshotIDs: append([]int64(nil), row.ObservedSnapshotIds...), CreatedAt: tsTime(row.CreatedAt),
	}, nil
}

func (r *Repository) LoadMarkerQuarantine(ctx context.Context, physicalPoolID, catalogID, attemptID string) (MarkerQuarantine, error) {
	if r == nil {
		return MarkerQuarantine{}, ErrInvalid
	}
	return LoadMarkerQuarantine(ctx, r.db, physicalPoolID, catalogID, attemptID)
}

func markerQuarantineExistsForPool(ctx context.Context, db DBTX, physicalPoolID string) (bool, error) {
	if db == nil || !validID(physicalPoolID) {
		return false, ErrInvalid
	}
	quarantined, err := querygen(db).HasMarkerQuarantineForPool(ctx, physicalPoolID)
	if err != nil {
		return false, err
	}
	return quarantined, nil
}

// lockMarkerQuarantineScope serializes every operation that can admit or
// reconcile a build attempt with insertion of a pool quarantine row. Callers
// must acquire this catalog-identity row lock before locking any attempt row,
// preserving the repository-wide catalog-before-attempt lock order.
func lockMarkerQuarantineScope(ctx context.Context, db DBTX, physicalPoolID string) error {
	if db == nil || !validID(physicalPoolID) {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	locked, err := querygen(db).LockCatalogQuarantineScope(ctx, physicalPoolID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if locked != physicalPoolID {
		return fmt.Errorf("%w: marker quarantine catalog identity differs", ErrConflict)
	}
	return nil
}

// lockAttemptAdmissionScope serializes build admission against the existing
// migration and catalog-retention authorities.  All callers that can claim a
// fence use the same global -> pool -> maintenance row order; keeping that
// order here avoids a cross-authority deadlock while the row locks make the
// lease-owner check atomic with the subsequent attempt insert.
func lockAttemptAdmissionScope(ctx context.Context, db DBTX, physicalPoolID, catalogID string) error {
	if db == nil || !validID(physicalPoolID) || !validID(catalogID) {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := querygen(db).AssertAttemptAdmissionFence(ctx, dbgen.AssertAttemptAdmissionFenceParams{PhysicalPoolID: physicalPoolID, CatalogID: catalogID})
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "pool maintenance fence busy"):
		return ErrRetentionMaintenanceBusy
	case strings.Contains(message, "migration fence busy"):
		return ErrMigrationBusy
	case strings.Contains(message, "catalog identity not found"):
		return ErrNotFound
	case strings.Contains(message, "invalid attempt admission fence scope"):
		return ErrInvalid
	default:
		return err
	}
}

// BeginAttemptTx persists an exact running-attempt identity through a
// caller-owned transaction. It never begins, commits, or rolls back tx; app
// composition uses it to share one control-plane transaction with delivery
// admission.
func (r *Repository) BeginAttemptTx(ctx context.Context, tx Tx, in BeginAttemptInput) (AttemptEvidence, error) {
	if r == nil || tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return BeginAttempt(ctx, tx, in)
}

// CommitAttemptTx records the exact external DuckLake commit in a
// caller-owned PostgreSQL transaction. It is the transaction-aware adapter
// used by app composition when delivery, serving-state, and DuckLake control
// evidence must commit atomically.
func (r *Repository) CommitAttemptTx(ctx context.Context, tx Tx, in CommitAttemptInput) (AttemptEvidence, error) {
	if r == nil || tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return CommitAttempt(ctx, tx, in)
}

// AbortAttemptTx records positive no-commit/terminated evidence through a
// caller-owned control-plane transaction. It never begins, commits, or rolls
// back tx; application composition uses it alongside the delivery ledger so
// both schemas commit atomically in leapview_control.
func (r *Repository) AbortAttemptTx(ctx context.Context, tx Tx, in TerminateAttemptInput) (AttemptEvidence, error) {
	if r == nil || tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return TerminateAttempt(ctx, tx, in, AttemptAborted)
}

// MarkAttemptIndeterminateTx records bounded evidence that an attempt's
// external outcome cannot be established. It operates on the caller-owned
// transaction and therefore composes with the delivery ledger without ever
// touching the external DuckLake catalog database.
func (r *Repository) MarkAttemptIndeterminateTx(ctx context.Context, tx Tx, in TerminateAttemptInput) (AttemptEvidence, error) {
	if r == nil || tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return TerminateAttempt(ctx, tx, in, AttemptIndeterminate)
}

// RenewAttemptLeaseTx extends a running DuckLake build-attempt lease on the
// caller-owned control transaction. Identity columns remain fenced by owner
// and epoch; only the expiry and updated timestamp advance monotonically.
func (r *Repository) RenewAttemptLeaseTx(ctx context.Context, tx Tx, attemptID, ownerID string, fencingEpoch int64, expiresAt time.Time) (AttemptEvidence, error) {
	if r == nil || tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !validUUID(attemptID) || !validID(ownerID) || fencingEpoch <= 0 {
		return AttemptEvidence{}, ErrInvalid
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return AttemptEvidence{}, err
	}
	exp := expiresAt.UTC().Truncate(time.Microsecond)
	if !exp.After(now) || exp.After(now.Add(maxAttemptLease)) {
		return AttemptEvidence{}, ErrInvalid
	}
	command, err := querygen(tx).RenewAttemptLease(ctx, dbgen.RenewAttemptLeaseParams{AttemptID: pgUUID(attemptID), OwnerID: ownerID, FencingEpoch: fencingEpoch, ExpiresAt: pgtype.Timestamptz{Time: exp, Valid: true}})
	if err != nil {
		return AttemptEvidence{}, err
	}
	if command.RowsAffected() != 1 {
		current, loadErr := LoadAttempt(ctx, tx, attemptID)
		if loadErr != nil {
			return AttemptEvidence{}, loadErr
		}
		if current.OwnerID != ownerID || current.FencingEpoch != fencingEpoch {
			return AttemptEvidence{}, ErrStaleFence
		}
		if current.State != AttemptRunning {
			return AttemptEvidence{}, fmt.Errorf("%w: attempt is %s", ErrConflict, current.State)
		}
		if current.LeaseExpiresAt.After(exp) {
			return AttemptEvidence{}, ErrConflict
		}
		return AttemptEvidence{}, ErrLeaseExpired
	}
	return LoadAttempt(ctx, tx, attemptID)
}

// BindGenerationTx records the immutable generation binding and its
// retention root through a caller-owned transaction. It never commits or
// rolls back tx.
func (r *Repository) BindGenerationTx(ctx context.Context, tx Tx, in GenerationBinding) (GenerationBinding, error) {
	if r == nil || tx == nil {
		return GenerationBinding{}, ErrInvalid
	}
	return BindGeneration(ctx, tx, in)
}

func querygen(db DBTX) *dbgen.Queries { return dbgen.New(db) }

func pgUUID(value string) pgtype.UUID {
	u, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
}

func tsTime(v pgtype.Timestamptz) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return v.Time.UTC()
}

// databaseClock is authoritative for lease/retention decisions. A node's
// wall clock can be skewed or deliberately forged; PostgreSQL's clock is the
// shared ordering source for all workers. A failure to read it fails closed;
// callers must never persist a node-local timestamp as a lifecycle decision.
func databaseClock(ctx context.Context, db DBTX) (time.Time, error) {
	if db == nil {
		return time.Time{}, ErrClockUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	value, err := querygen(db).DatabaseClock(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrClockUnavailable, err)
	}
	if !value.Valid || value.Time.IsZero() {
		return time.Time{}, ErrClockUnavailable
	}
	return value.Time.UTC(), nil
}

func RegisterCatalog(ctx context.Context, tx DBTX, identity CatalogIdentity) (CatalogIdentity, error) {
	if tx == nil {
		return CatalogIdentity{}, ErrInvalid
	}
	if err := validateCatalog(identity); err != nil {
		return CatalogIdentity{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := querygen(tx).InsertCatalogIdentity(ctx, dbgen.InsertCatalogIdentityParams{PhysicalPoolID: identity.PhysicalPoolID, CatalogDatabase: identity.CatalogDatabase, CatalogID: identity.CatalogID, CatalogUuid: identity.CatalogUUID, MetadataSchema: identity.MetadataSchema})
	if err != nil {
		return CatalogIdentity{}, err
	}
	var got CatalogIdentity
	row, err := querygen(tx).GetCatalogIdentity(ctx, identity.PhysicalPoolID)
	got = CatalogIdentity{PhysicalPoolID: row.PhysicalPoolID, CatalogDatabase: row.CatalogDatabase, CatalogID: row.CatalogID, CatalogUUID: row.CatalogUuid, MetadataSchema: row.MetadataSchema, CreatedAt: tsTime(row.CreatedAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogIdentity{}, ErrNotFound
	}
	if err != nil {
		return CatalogIdentity{}, err
	}
	if !sameCatalog(got, identity) {
		return CatalogIdentity{}, fmt.Errorf("%w: physical pool %q", ErrConflict, identity.PhysicalPoolID)
	}
	return got, nil
}

func (r *Repository) RegisterCatalog(ctx context.Context, identity CatalogIdentity) (CatalogIdentity, error) {
	if r == nil {
		return CatalogIdentity{}, ErrInvalid
	}
	return RegisterCatalog(ctx, r.db, identity)
}

func LoadCatalog(ctx context.Context, db DBTX, poolID string) (CatalogIdentity, error) {
	if db == nil || !validID(poolID) {
		return CatalogIdentity{}, ErrInvalid
	}
	row, err := querygen(db).GetCatalogIdentity(ctx, poolID)
	got := CatalogIdentity{PhysicalPoolID: row.PhysicalPoolID, CatalogDatabase: row.CatalogDatabase, CatalogID: row.CatalogID, CatalogUUID: row.CatalogUuid, MetadataSchema: row.MetadataSchema, CreatedAt: tsTime(row.CreatedAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogIdentity{}, ErrNotFound
	}
	if err != nil {
		return CatalogIdentity{}, err
	}
	return got, nil
}

func (r *Repository) LoadCatalog(ctx context.Context, poolID string) (CatalogIdentity, error) {
	if r == nil {
		return CatalogIdentity{}, ErrInvalid
	}
	return LoadCatalog(ctx, r.db, poolID)
}

// ensureSnapshotLive creates the retention gate for a newly committed
// snapshot. It is intentionally private: callers cannot mark an arbitrary
// catalog snapshot live without first recording qualified commit evidence.
func ensureSnapshotLive(ctx context.Context, tx DBTX, ref SnapshotRef) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	err := querygen(tx).InsertSnapshotRetentionLive(ctx, dbgen.InsertSnapshotRetentionLiveParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if err != nil {
		return err
	}
	state, err := querygen(tx).LockSnapshotRetentionState(ctx, dbgen.LockSnapshotRetentionStateParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if err != nil {
		return err
	}
	if state != "live" {
		return ErrNotLive
	}
	return nil
}

// requireSnapshotLive verifies an already-qualified retention row while
// holding its lock. Unlike ensureSnapshotLive, it never creates a row: root
// callers therefore cannot make an arbitrary catalog snapshot live without a
// committed attempt (which is the only path that establishes retention).
func requireSnapshotLive(ctx context.Context, tx DBTX, ref SnapshotRef) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	state, err := querygen(tx).LockSnapshotRetentionState(ctx, dbgen.LockSnapshotRetentionStateParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state != "live" {
		return ErrNotLive
	}
	return nil
}

// BindGeneration records one immutable serving selection. It requires the
// committed attempt row and exact persistent marker evidence; a control lease
// alone cannot manufacture a generation binding.
func BindGeneration(ctx context.Context, tx DBTX, in GenerationBinding) (GenerationBinding, error) {
	if tx == nil {
		return GenerationBinding{}, ErrInvalid
	}
	if err := validateBinding(in); err != nil {
		return GenerationBinding{}, err
	}
	row, err := querygen(tx).GetAttemptForBinding(ctx, pgUUID(in.AttemptID))
	state, req, plan, pool, catalog, fence, marker := row.State, row.RequestDigest, row.PlanDigest, row.PhysicalPoolID, row.CatalogID, row.FencingEpoch, row.CommitMarker
	snap := int64(0)
	if row.SnapshotID != nil {
		snap = *row.SnapshotID
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationBinding{}, ErrNotFound
	}
	if err != nil {
		return GenerationBinding{}, err
	}
	if state != string(AttemptCommitted) || req != in.RequestDigest || plan != in.PlanDigest || pool != in.PhysicalPoolID || catalog != in.CatalogID || fence != in.FencingEpoch || snap != in.SnapshotID || marker == "" {
		return GenerationBinding{}, fmt.Errorf("%w: attempt evidence does not match generation", ErrConflict)
	}
	var commitMarker ducklake.CommitMarker
	if err := strictjson.Decode([]byte(marker), &commitMarker); err != nil {
		return GenerationBinding{}, fmt.Errorf("%w: committed marker is invalid", ErrConflict)
	}
	if commitMarker.AttemptID != in.AttemptID || commitMarker.DeliveryID != in.DeliveryID || commitMarker.GenerationID != in.GenerationID || commitMarker.RequestDigest != in.RequestDigest || commitMarker.PlanDigest != in.PlanDigest || commitMarker.PhysicalPoolID != in.PhysicalPoolID || commitMarker.LeaseEpoch != in.FencingEpoch {
		return GenerationBinding{}, fmt.Errorf("%w: committed marker identity does not match generation", ErrConflict)
	}
	if err := ensureSnapshotLive(ctx, tx, SnapshotRef{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID}); err != nil {
		return GenerationBinding{}, err
	}
	err = querygen(tx).InsertGenerationBinding(ctx, dbgen.InsertGenerationBindingParams{DeliveryID: in.DeliveryID, GenerationID: in.GenerationID, AttemptID: pgUUID(in.AttemptID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID, RelationManifestDigest: in.RelationManifestDigest, CompatibilityDigest: in.CompatibilityDigest, ServingArtifactDigest: in.ServingArtifactDigest, RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, FencingEpoch: in.FencingEpoch})
	if err != nil {
		return GenerationBinding{}, err
	}
	got, err := loadBinding(ctx, tx, in.DeliveryID, in.GenerationID)
	if err != nil {
		return GenerationBinding{}, err
	}
	if !sameBinding(got, in) {
		return GenerationBinding{}, fmt.Errorf("%w: generation %q", ErrConflict, in.GenerationID)
	}
	// A generation binding is itself a durable retention root. Replays are
	// idempotent because the root ID is the immutable attempt UUID.
	if err := createSnapshotRoot(ctx, tx, SnapshotRootInput{RootID: in.AttemptID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID, Kind: RootGeneration, CreatedAt: in.BoundAt}); err != nil {
		return GenerationBinding{}, err
	}
	return got, nil
}

func createSnapshotRoot(ctx context.Context, tx DBTX, in SnapshotRootInput) error {
	if tx == nil || !validUUID(in.RootID) || !validSnapshotRef(SnapshotRef{in.PhysicalPoolID, in.CatalogID, in.SnapshotID}) {
		return ErrInvalid
	}
	if !validRootKind(in.Kind) {
		return ErrInvalid
	}
	created := in.CreatedAt.UTC()
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if dbNow, err := databaseClock(ctx, tx); err != nil {
		return err
	} else {
		created = dbNow
	}
	if err := requireSnapshotLive(ctx, tx, SnapshotRef{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID}); err != nil {
		return err
	}
	evidence, err := canonicalOptionalEvidence(in.Evidence)
	if err != nil {
		return err
	}
	err = querygen(tx).InsertSnapshotRoot(ctx, dbgen.InsertSnapshotRootParams{RootID: pgUUID(in.RootID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID, RootKind: string(in.Kind), CreatedAt: pgtype.Timestamptz{Time: created, Valid: true}, Evidence: []byte(evidence)})
	if err != nil {
		return err
	}
	row, err := querygen(tx).GetSnapshotRootForCheck(ctx, pgUUID(in.RootID))
	if err != nil {
		return err
	}
	pool, catalog, snapshot, kind, state, persistedEvidence := row.PhysicalPoolID, row.CatalogID, row.SnapshotID, row.RootKind, row.State, row.Evidence
	if pool != in.PhysicalPoolID || catalog != in.CatalogID || snapshot != in.SnapshotID || kind != string(in.Kind) || state != "live" || string(persistedEvidence) != evidence {
		return fmt.Errorf("%w: snapshot root %q", ErrConflict, in.RootID)
	}
	return nil
}

func (r *Repository) CreateSnapshotRoot(ctx context.Context, in SnapshotRootInput) error {
	if r == nil {
		return ErrInvalid
	}
	return createSnapshotRoot(ctx, r.db, in)
}

// ReleaseSnapshotRoot removes one durable non-query protection. It is
// idempotent and intentionally fenced by the root identity rather than a
// process-local lease.
func ReleaseSnapshotRoot(ctx context.Context, tx DBTX, rootID string, releasedAt time.Time) error {
	if tx == nil || !validUUID(rootID) {
		return ErrInvalid
	}
	if releasedAt.IsZero() {
		releasedAt = time.Now().UTC()
	}
	if dbNow, err := databaseClock(ctx, tx); err != nil {
		return err
	} else {
		releasedAt = dbNow
	}
	result, err := querygen(tx).ExpireSnapshotRoot(ctx, dbgen.ExpireSnapshotRootParams{RootID: pgUUID(rootID), ExpiredAt: pgtype.Timestamptz{Time: releasedAt.UTC(), Valid: true}})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	state, err := querygen(tx).GetSnapshotRootState(ctx, pgUUID(rootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if state == "expired" {
		return nil
	}
	return ErrConflict
}

func (r *Repository) ReleaseSnapshotRoot(ctx context.Context, rootID string, releasedAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return ReleaseSnapshotRoot(ctx, r.db, rootID, releasedAt)
}

func QuarantineSnapshotRoot(ctx context.Context, tx DBTX, rootID string, evidence json.RawMessage) error {
	if tx == nil || !validUUID(rootID) {
		return ErrInvalid
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: quarantine evidence is required", ErrInvalid)
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	result, err := querygen(tx).QuarantineSnapshotRoot(ctx, dbgen.QuarantineSnapshotRootParams{RootID: pgUUID(rootID), QuarantineEvidence: []byte(canonical), QuarantinedAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	row, err := querygen(tx).GetSnapshotRootQuarantine(ctx, pgUUID(rootID))
	state, raw := row.State, row.QuarantineEvidence
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if state == "quarantined" && evidenceEqual(raw, canonical) {
		return nil
	}
	return ErrConflict
}

func (r *Repository) QuarantineSnapshotRoot(ctx context.Context, rootID string, evidence json.RawMessage) error {
	if r == nil {
		return ErrInvalid
	}
	return QuarantineSnapshotRoot(ctx, r.db, rootID, evidence)
}

func CompleteSnapshotRootCleanup(ctx context.Context, tx DBTX, rootID string, evidence json.RawMessage) error {
	if tx == nil || !validUUID(rootID) {
		return ErrInvalid
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: cleanup evidence is required", ErrInvalid)
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	result, err := querygen(tx).CompleteSnapshotRootCleanup(ctx, dbgen.CompleteSnapshotRootCleanupParams{RootID: pgUUID(rootID), CleanupEvidence: []byte(canonical), CleanupCompletedAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	row, err := querygen(tx).GetSnapshotRootCleanup(ctx, pgUUID(rootID))
	state, raw := row.State, row.CleanupEvidence
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if state == "cleanup-complete" && evidenceEqual(raw, canonical) {
		return nil
	}
	return ErrConflict
}

func (r *Repository) CompleteSnapshotRootCleanup(ctx context.Context, rootID string, evidence json.RawMessage) error {
	if r == nil {
		return ErrInvalid
	}
	return CompleteSnapshotRootCleanup(ctx, r.db, rootID, evidence)
}

func LoadSnapshotRoot(ctx context.Context, db DBTX, rootID string) (SnapshotRoot, error) {
	if db == nil || !validUUID(rootID) {
		return SnapshotRoot{}, ErrInvalid
	}
	row, err := querygen(db).GetSnapshotRoot(ctx, pgUUID(rootID))
	var out SnapshotRoot
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotRoot{}, ErrNotFound
	}
	if err != nil {
		return SnapshotRoot{}, err
	}
	out.RootID, out.PhysicalPoolID, out.CatalogID, out.SnapshotID, out.Kind, out.State, out.CreatedAt = row.RootID, row.PhysicalPoolID, row.CatalogID, row.SnapshotID, SnapshotRootKind(row.RootKind), row.State, tsTime(row.CreatedAt)
	out.Evidence = append(json.RawMessage(nil), row.Evidence...)
	if len(row.QuarantineEvidence) != 0 {
		out.QuarantineEvidence = append(json.RawMessage(nil), row.QuarantineEvidence...)
	}
	if len(row.CleanupEvidence) != 0 {
		out.CleanupEvidence = append(json.RawMessage(nil), row.CleanupEvidence...)
	}
	out.RetiredAt, out.ExpiredAt, out.QuarantinedAt, out.CleanupCompletedAt = tsTime(row.RetiredAt), tsTime(row.ExpiredAt), tsTime(row.QuarantinedAt), tsTime(row.CleanupCompletedAt)
	return out, nil
}

func (r *Repository) LoadSnapshotRoot(ctx context.Context, rootID string) (SnapshotRoot, error) {
	if r == nil {
		return SnapshotRoot{}, ErrInvalid
	}
	return LoadSnapshotRoot(ctx, r.db, rootID)
}

func (r *Repository) BindGeneration(ctx context.Context, in GenerationBinding) (GenerationBinding, error) {
	if r == nil {
		return GenerationBinding{}, ErrInvalid
	}
	return BindGeneration(ctx, r.db, in)
}

func LoadBinding(ctx context.Context, db DBTX, deliveryID, generationID string) (GenerationBinding, error) {
	if db == nil || !validID(deliveryID) || !validID(generationID) {
		return GenerationBinding{}, ErrInvalid
	}
	return loadBinding(ctx, db, deliveryID, generationID)
}

func (r *Repository) LoadBinding(ctx context.Context, deliveryID, generationID string) (GenerationBinding, error) {
	if r == nil {
		return GenerationBinding{}, ErrInvalid
	}
	return LoadBinding(ctx, r.db, deliveryID, generationID)
}

func loadBinding(ctx context.Context, db DBTX, deliveryID, generationID string) (GenerationBinding, error) {
	row, err := querygen(db).GetGenerationBinding(ctx, dbgen.GetGenerationBindingParams{DeliveryID: deliveryID, GenerationID: generationID})
	got := GenerationBinding{DeliveryID: row.DeliveryID, GenerationID: row.GenerationID, AttemptID: row.AttemptID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, RelationManifestDigest: row.RelationManifestDigest, CompatibilityDigest: row.CompatibilityDigest, ServingArtifactDigest: row.ServingArtifactDigest, RequestDigest: row.RequestDigest, PlanDigest: row.PlanDigest, FencingEpoch: row.FencingEpoch, BoundAt: tsTime(row.BoundAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return GenerationBinding{}, ErrNotFound
	}
	if err != nil {
		return GenerationBinding{}, err
	}
	return got, nil
}

// BeginAttempt persists identity before any DuckLake mutation. Replay of the
// exact identity returns the existing row; any identity drift is a conflict.
func BeginAttempt(ctx context.Context, tx DBTX, in BeginAttemptInput) (AttemptEvidence, error) {
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return AttemptEvidence{}, err
	}
	return beginAttemptAt(ctx, tx, in, now)
}

func beginAttemptAt(ctx context.Context, tx DBTX, in BeginAttemptInput, now time.Time) (AttemptEvidence, error) {
	if tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	if err := validateBeginAt(in, now); err != nil {
		return AttemptEvidence{}, err
	}
	// Admission shares the migration/retention authority rows with every
	// maintenance and migration claim.  Even an exact identity replay must
	// pass this fence: replay grants permission to resume physical work, so an
	// active maintenance owner cannot be bypassed by the no-op insert.
	// Lock in the repository-wide order (global migration, pool migration, pool
	// maintenance) before checking leases so an active owner cannot pass a
	// check-then-act race.  The locks execute in a SECURITY DEFINER database
	// capability because normal runtime admission has no direct fence-table
	// UPDATE privilege.
	if err := lockAttemptAdmissionScope(ctx, tx, in.PhysicalPoolID, in.CatalogID); err != nil {
		return AttemptEvidence{}, err
	}
	// Catalog quarantine is the final admission scope lock.  Keeping it after
	// the authority rows gives this path the same global -> pool -> maintenance
	// -> catalog/attempt order as every fence claim and avoids a catalog↔fence
	// lock inversion with concurrent marker reconciliation.
	if err := lockMarkerQuarantineScope(ctx, tx, in.PhysicalPoolID); err != nil {
		return AttemptEvidence{}, err
	}
	if quarantined, err := markerQuarantineExistsForPool(ctx, tx, in.PhysicalPoolID); err != nil {
		return AttemptEvidence{}, err
	} else if quarantined {
		return AttemptEvidence{}, ErrMarkerQuarantined
	}
	in.LeaseExpiresAt = in.LeaseExpiresAt.UTC().Truncate(time.Microsecond)
	leaseExpires := in.LeaseExpiresAt.UTC()
	err := querygen(tx).InsertAttemptEvidence(ctx, dbgen.InsertAttemptEvidenceParams{AttemptID: pgUUID(in.AttemptID), RequestDigest: in.RequestDigest, PlanDigest: in.PlanDigest, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, LeaseExpiresAt: pgtype.Timestamptz{Time: leaseExpires, Valid: true}, SessionIdentity: in.SessionIdentity})
	if err != nil {
		return AttemptEvidence{}, err
	}
	got, err := LoadAttempt(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if got.RequestDigest != in.RequestDigest || got.PlanDigest != in.PlanDigest || got.PhysicalPoolID != in.PhysicalPoolID || got.CatalogID != in.CatalogID || got.OwnerID != in.OwnerID || got.FencingEpoch != in.FencingEpoch || got.SessionIdentity != in.SessionIdentity || !got.LeaseExpiresAt.Equal(in.LeaseExpiresAt) {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt %q", ErrConflict, in.AttemptID)
	}
	return got, nil
}

func (r *Repository) BeginAttempt(ctx context.Context, in BeginAttemptInput) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (AttemptEvidence, error) {
		now, err := databaseClock(ctx, tx)
		if err != nil {
			return AttemptEvidence{}, err
		}
		return beginAttemptAt(ctx, tx, in, now)
	})
}

func CommitAttempt(ctx context.Context, tx DBTX, in CommitAttemptInput) (AttemptEvidence, error) {
	if tx == nil || !validUUID(in.AttemptID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || !validSnapshotRef(in.Snapshot) {
		return AttemptEvidence{}, ErrInvalid
	}
	marker, err := ducklake.ParseCommitMarker(in.CommitMarker)
	if err != nil {
		return AttemptEvidence{}, fmt.Errorf("%w: invalid DuckLake commit marker: %v", ErrInvalid, err)
	}
	if marker.AttemptID != in.AttemptID || marker.PhysicalPoolID != in.Snapshot.PhysicalPoolID || marker.LeaseEpoch != in.FencingEpoch {
		return AttemptEvidence{}, fmt.Errorf("%w: commit marker identity mismatch", ErrConflict)
	}
	if err := lockMarkerQuarantineScope(ctx, tx, in.Snapshot.PhysicalPoolID); err != nil {
		return AttemptEvidence{}, err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return AttemptEvidence{}, err
	}
	canonical, _ := marker.CanonicalJSON()
	existing, err := loadAttemptForUpdate(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if quarantined, err := markerQuarantineExistsForPool(ctx, tx, existing.PhysicalPoolID); err != nil {
		return AttemptEvidence{}, err
	} else if quarantined {
		return AttemptEvidence{}, ErrMarkerQuarantined
	}
	if existing.OwnerID != in.OwnerID || existing.FencingEpoch != in.FencingEpoch {
		return AttemptEvidence{}, ErrStaleFence
	}
	if existing.PhysicalPoolID != in.Snapshot.PhysicalPoolID || existing.CatalogID != in.Snapshot.CatalogID {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt catalog identity differs", ErrConflict)
	}
	if existing.State == AttemptCommitted {
		if existing.SnapshotID != in.Snapshot.SnapshotID || !markersEqual(existing.CommitMarker, canonical) {
			return AttemptEvidence{}, fmt.Errorf("%w: committed attempt evidence differs", ErrConflict)
		}
		return existing, nil
	}
	if existing.State != AttemptRunning {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt is %s", ErrConflict, existing.State)
	}
	// Do not reject a matching external commit merely because the control
	// lease expired before its acknowledgement arrived. Lease expiry is not
	// termination evidence; the exact persistent DuckLake marker decides the
	// external outcome. The successor fence controls whether it may bind.
	if existing.PlanDigest != marker.PlanDigest || existing.RequestDigest != marker.RequestDigest {
		return AttemptEvidence{}, fmt.Errorf("%w: commit marker request or plan digest differs", ErrConflict)
	}
	if err := ensureSnapshotLive(ctx, tx, in.Snapshot); err != nil {
		return AttemptEvidence{}, err
	}
	err = querygen(tx).UpdateAttemptCommitted(ctx, dbgen.UpdateAttemptCommittedParams{AttemptID: pgUUID(in.AttemptID), OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, SnapshotID: &in.Snapshot.SnapshotID, CommitMarker: []byte(canonical), UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return AttemptEvidence{}, err
	}
	got, err := LoadAttempt(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if got.State != AttemptCommitted {
		if got.OwnerID != in.OwnerID || got.FencingEpoch != in.FencingEpoch {
			return AttemptEvidence{}, ErrStaleFence
		}
		return AttemptEvidence{}, fmt.Errorf("%w: attempt is %s", ErrConflict, got.State)
	}
	if got.SnapshotID != in.Snapshot.SnapshotID || !markersEqual(got.CommitMarker, canonical) {
		return AttemptEvidence{}, fmt.Errorf("%w: committed attempt evidence differs", ErrConflict)
	}
	return got, nil
}

func (r *Repository) CommitAttempt(ctx context.Context, in CommitAttemptInput) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (AttemptEvidence, error) {
		return CommitAttempt(ctx, tx, in)
	})
}

func TerminateAttempt(ctx context.Context, tx DBTX, in TerminateAttemptInput, state AttemptState) (AttemptEvidence, error) {
	if tx == nil || !validUUID(in.AttemptID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || (state != AttemptAborted && state != AttemptIndeterminate && state != AttemptFenced) {
		return AttemptEvidence{}, ErrInvalid
	}
	evidence, err := canonicalEvidence(in.Evidence)
	if err != nil {
		return AttemptEvidence{}, err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return AttemptEvidence{}, err
	}
	err = querygen(tx).UpdateAttemptTerminal(ctx, dbgen.UpdateAttemptTerminalParams{AttemptID: pgUUID(in.AttemptID), OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, State: string(state), TerminationEvidence: []byte(evidence), UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return AttemptEvidence{}, err
	}
	got, err := LoadAttempt(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if got.OwnerID != in.OwnerID || got.FencingEpoch != in.FencingEpoch {
		return AttemptEvidence{}, ErrStaleFence
	}
	if got.State == state {
		if !evidenceEqual(got.TerminationEvidence, evidence) {
			return AttemptEvidence{}, fmt.Errorf("%w: terminal evidence differs", ErrConflict)
		}
		return got, nil
	}
	if got.State != state {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt is %s", ErrConflict, got.State)
	}
	return got, nil
}

func (r *Repository) AbortAttempt(ctx context.Context, in TerminateAttemptInput) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return TerminateAttempt(ctx, r.db, in, AttemptAborted)
}

func (r *Repository) MarkAttemptIndeterminate(ctx context.Context, in TerminateAttemptInput) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return TerminateAttempt(ctx, r.db, in, AttemptIndeterminate)
}

// ReconcileAttempt applies an explicit exact-marker or positive
// session-termination recovery decision. It owns a short transaction.
func (r *Repository) ReconcileAttempt(ctx context.Context, in ReconcileAttemptInput) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if b, ok := r.db.(beginner); ok {
		tx, err := b.Begin(ctx)
		if err != nil {
			return AttemptEvidence{}, err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		got, err := r.ReconcileAttemptTx(ctx, tx, in)
		if err != nil {
			return AttemptEvidence{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return AttemptEvidence{}, err
		}
		committed = true
		return got, nil
	}
	return reconcileAttemptTx(ctx, r.db, in)
}

// ReconcileAttemptTx applies exact recovery evidence in a caller-owned
// transaction. It never begins, commits, or rolls back tx.
func (r *Repository) ReconcileAttemptTx(ctx context.Context, tx Tx, in ReconcileAttemptInput) (AttemptEvidence, error) {
	if r == nil || tx == nil || !r.Configured() {
		return AttemptEvidence{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return reconcileAttemptTx(ctx, tx, in)
}

func reconcileAttemptTx(ctx context.Context, tx DBTX, in ReconcileAttemptInput) (AttemptEvidence, error) {
	if tx == nil || !validUUID(in.AttemptID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 {
		return AttemptEvidence{}, ErrInvalid
	}
	state := in.State
	if state != AttemptCommitted && state != AttemptAborted {
		return AttemptEvidence{}, fmt.Errorf("%w: reconciliation outcome must be committed or aborted", ErrInvalid)
	}
	var marker canonicalDuckLakeRecoveryMarker
	var markerCanonical string
	var evidence string
	if state == AttemptCommitted {
		if !validSnapshotRef(in.Snapshot) || in.CommitMarker == "" || len(in.TerminationEvidence) != 0 || in.SessionTerminated {
			return AttemptEvidence{}, fmt.Errorf("%w: committed recovery requires an exact marker", ErrInvalid)
		}
		parsed, err := ducklake.ParseCommitMarker(in.CommitMarker)
		if err != nil {
			return AttemptEvidence{}, fmt.Errorf("%w: invalid recovery commit marker: %v", ErrInvalid, err)
		}
		canonical, err := parsed.CanonicalJSON()
		if err != nil {
			return AttemptEvidence{}, fmt.Errorf("%w: invalid recovery commit marker: %v", ErrInvalid, err)
		}
		marker = canonicalDuckLakeRecoveryMarker{AttemptID: parsed.AttemptID, PhysicalPoolID: parsed.PhysicalPoolID, RequestDigest: parsed.RequestDigest, PlanDigest: parsed.PlanDigest, LeaseEpoch: parsed.LeaseEpoch}
		markerCanonical = canonical
	} else {
		if in.Snapshot != (SnapshotRef{}) || in.CommitMarker != "" || !in.SessionTerminated {
			return AttemptEvidence{}, fmt.Errorf("%w: aborted recovery requires positive session-termination evidence", ErrInvalid)
		}
		if !validID(in.SessionIdentity) {
			return AttemptEvidence{}, fmt.Errorf("%w: session identity is invalid", ErrInvalid)
		}
		if err := validateSessionTerminationEvidence(in.TerminationEvidence, in.AttemptID, in.OwnerID, in.SessionIdentity, in.FencingEpoch); err != nil {
			return AttemptEvidence{}, err
		}
	}

	// Attempt identity is immutable, so an unlocked read is sufficient to
	// discover the pool scope. Acquire the catalog scope before taking the
	// attempt row lock, matching Begin/Commit/Quarantine lock order.
	identity, err := LoadAttempt(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if err := lockMarkerQuarantineScope(ctx, tx, identity.PhysicalPoolID); err != nil {
		return AttemptEvidence{}, err
	}
	existing, err := loadAttemptForUpdate(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if quarantined, err := markerQuarantineExistsForPool(ctx, tx, existing.PhysicalPoolID); err != nil {
		return AttemptEvidence{}, err
	} else if quarantined {
		return AttemptEvidence{}, ErrMarkerQuarantined
	}
	if existing.OwnerID != in.OwnerID || existing.FencingEpoch != in.FencingEpoch {
		return AttemptEvidence{}, ErrStaleFence
	}
	if state == AttemptAborted && existing.SessionIdentity != in.SessionIdentity {
		return AttemptEvidence{}, fmt.Errorf("%w: session-termination evidence session differs", ErrConflict)
	}
	if state == AttemptCommitted && (existing.PhysicalPoolID != in.Snapshot.PhysicalPoolID || existing.CatalogID != in.Snapshot.CatalogID) {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt catalog identity differs", ErrConflict)
	}
	if state == AttemptCommitted {
		if marker.AttemptID != in.AttemptID || marker.PhysicalPoolID != existing.PhysicalPoolID || marker.RequestDigest != existing.RequestDigest || marker.PlanDigest != existing.PlanDigest || marker.LeaseEpoch != existing.FencingEpoch {
			return AttemptEvidence{}, fmt.Errorf("%w: recovery commit marker identity mismatch", ErrConflict)
		}
		if existing.State == AttemptCommitted {
			if existing.SnapshotID == in.Snapshot.SnapshotID && markersEqual(existing.CommitMarker, markerCanonical) {
				return existing, nil
			}
			return AttemptEvidence{}, fmt.Errorf("%w: committed recovery evidence differs", ErrConflict)
		}
	} else if existing.State == AttemptAborted {
		var evidenceErr error
		evidence, evidenceErr = canonicalEvidence(in.TerminationEvidence)
		if evidenceErr == nil && evidenceEqual(existing.TerminationEvidence, evidence) {
			return existing, nil
		}
		return AttemptEvidence{}, fmt.Errorf("%w: aborted recovery evidence differs", ErrConflict)
	}
	if existing.State != AttemptRunning && existing.State != AttemptIndeterminate {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt is %s", ErrConflict, existing.State)
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if state == AttemptCommitted {
		if err := ensureSnapshotLive(ctx, tx, in.Snapshot); err != nil {
			return AttemptEvidence{}, err
		}
		rows, err := querygen(tx).ReconcileAttemptCommitted(ctx, dbgen.ReconcileAttemptCommittedParams{AttemptID: pgUUID(in.AttemptID), OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, SnapshotID: &in.Snapshot.SnapshotID, CommitMarker: []byte(markerCanonical), UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}})
		if err != nil {
			return AttemptEvidence{}, err
		}
		if rows != 1 {
			return AttemptEvidence{}, ErrConflict
		}
	} else {
		evidence, err = canonicalEvidence(in.TerminationEvidence)
		if err != nil {
			return AttemptEvidence{}, err
		}
		rows, err := querygen(tx).ReconcileAttemptTerminal(ctx, dbgen.ReconcileAttemptTerminalParams{AttemptID: pgUUID(in.AttemptID), OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, State: string(state), TerminationEvidence: []byte(evidence), UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true}})
		if err != nil {
			return AttemptEvidence{}, err
		}
		if rows != 1 {
			return AttemptEvidence{}, ErrConflict
		}
	}
	got, err := LoadAttempt(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
	}
	if got.State != state {
		return AttemptEvidence{}, fmt.Errorf("%w: attempt is %s", ErrConflict, got.State)
	}
	if state == AttemptCommitted {
		if got.SnapshotID != in.Snapshot.SnapshotID || !markersEqual(got.CommitMarker, markerCanonical) {
			return AttemptEvidence{}, fmt.Errorf("%w: committed recovery evidence differs", ErrConflict)
		}
	} else if !evidenceEqual(got.TerminationEvidence, string(evidence)) {
		return AttemptEvidence{}, fmt.Errorf("%w: aborted recovery evidence differs", ErrConflict)
	}
	return got, nil
}

type canonicalDuckLakeRecoveryMarker struct {
	AttemptID, PhysicalPoolID, RequestDigest, PlanDigest string
	LeaseEpoch                                           int64
}

type sessionTerminationEvidenceDocument struct {
	SchemaVersion     int    `json:"schema_version"`
	AttemptID         string `json:"attempt_id"`
	OwnerID           string `json:"owner_id"`
	FencingEpoch      int64  `json:"fencing_epoch"`
	SessionIdentity   string `json:"session_identity"`
	SessionTerminated bool   `json:"session_terminated"`
}

func validateSessionTerminationEvidence(raw json.RawMessage, attemptID, ownerID, sessionIdentity string, fencingEpoch int64) error {
	evidence, err := canonicalEvidence(raw)
	if err != nil {
		return fmt.Errorf("%w: positive session-termination evidence is required", ErrInvalid)
	}
	var document sessionTerminationEvidenceDocument
	if err := strictjson.Decode([]byte(evidence), &document); err != nil {
		return fmt.Errorf("%w: positive session-termination evidence is invalid", ErrInvalid)
	}
	if document.SchemaVersion != 1 || document.AttemptID != attemptID || document.OwnerID != ownerID || document.FencingEpoch != fencingEpoch || document.SessionIdentity != sessionIdentity || !document.SessionTerminated {
		return fmt.Errorf("%w: positive session-termination evidence identity differs", ErrInvalid)
	}
	return nil
}

func LoadAttempt(ctx context.Context, db DBTX, id string) (AttemptEvidence, error) {
	if db == nil || !validUUID(id) {
		return AttemptEvidence{}, ErrInvalid
	}
	row, err := querygen(db).GetAttemptEvidence(ctx, pgUUID(id))
	var a AttemptEvidence
	if errors.Is(err, pgx.ErrNoRows) {
		return AttemptEvidence{}, ErrNotFound
	}
	if err != nil {
		return AttemptEvidence{}, err
	}
	a.AttemptID, a.RequestDigest, a.PlanDigest, a.PhysicalPoolID, a.CatalogID, a.OwnerID, a.FencingEpoch, a.LeaseExpiresAt, a.SessionIdentity, a.State = row.AttemptID, row.RequestDigest, row.PlanDigest, row.PhysicalPoolID, row.CatalogID, row.OwnerID, row.FencingEpoch, tsTime(row.LeaseExpiresAt), row.SessionIdentity, AttemptState(row.State)
	if row.SnapshotID != nil {
		a.SnapshotID = *row.SnapshotID
	}
	a.CommitMarker = string(row.CommitMarker)
	a.TerminationEvidence = append(json.RawMessage(nil), row.TerminationEvidence...)
	a.CreatedAt, a.UpdatedAt, a.TerminalAt = tsTime(row.CreatedAt), tsTime(row.UpdatedAt), tsTime(row.TerminalAt)
	return a, nil
}

func loadAttemptForUpdate(ctx context.Context, db DBTX, id string) (AttemptEvidence, error) {
	if _, err := querygen(db).LockAttempt(ctx, pgUUID(id)); errors.Is(err, pgx.ErrNoRows) {
		return AttemptEvidence{}, ErrNotFound
	} else if err != nil {
		return AttemptEvidence{}, err
	}
	return LoadAttempt(ctx, db, id)
}

func (r *Repository) LoadAttempt(ctx context.Context, id string) (AttemptEvidence, error) {
	if r == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	return LoadAttempt(ctx, r.db, id)
}

func AcquireSnapshotLease(ctx context.Context, tx DBTX, in AcquireLeaseInput) (SnapshotLease, error) {
	return acquireSnapshotLeaseAt(ctx, tx, in, time.Now().UTC())
}

func acquireSnapshotLeaseAt(ctx context.Context, tx DBTX, in AcquireLeaseInput, now time.Time) (SnapshotLease, error) {
	if tx == nil || !validUUID(in.LeaseID) || !validID(in.DeliveryID) || !validID(in.GenerationID) || !validSnapshotRef(SnapshotRef{in.PhysicalPoolID, in.CatalogID, in.SnapshotID}) || !validID(in.OwnerID) || in.FencingEpoch <= 0 {
		return SnapshotLease{}, ErrInvalid
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	dbNow, err := databaseClock(ctx, tx)
	if err != nil {
		return SnapshotLease{}, err
	}
	now = dbNow
	acquired := in.AcquiredAt.UTC()
	if acquired.IsZero() {
		acquired = now
	}
	// PostgreSQL timestamptz stores microsecond precision. Normalize before
	// inserting so an idempotent retry compares the exact persisted identity.
	acquired = acquired.Truncate(time.Microsecond)
	in.AcquiredAt = acquired
	in.ExpiresAt = in.ExpiresAt.UTC().Truncate(time.Microsecond)
	// An already-expired lease must never enter the active state. Compare with
	// the repository clock as well as acquired_at: callers may replay an old
	// acquired timestamp, and PostgreSQL's retention gate must reject that
	// lease before it can protect a snapshot.
	if !in.ExpiresAt.After(acquired) || !in.ExpiresAt.After(now) {
		return SnapshotLease{}, ErrInvalid
	}
	if in.ExpiresAt.After(now.Add(maxSnapshotLease)) {
		return SnapshotLease{}, ErrInvalid
	}
	rowBinding, err := querygen(tx).GetGenerationBindingSnapshot(ctx, dbgen.GetGenerationBindingSnapshotParams{DeliveryID: in.DeliveryID, GenerationID: in.GenerationID})
	pool, catalog, snapshot, fence := rowBinding.PhysicalPoolID, rowBinding.CatalogID, rowBinding.SnapshotID, rowBinding.FencingEpoch
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	}
	if err != nil {
		return SnapshotLease{}, err
	}
	if pool != in.PhysicalPoolID || catalog != in.CatalogID || snapshot != in.SnapshotID || fence != in.FencingEpoch {
		return SnapshotLease{}, fmt.Errorf("%w: generation binding differs", ErrConflict)
	}
	state, err := querygen(tx).LockSnapshotRetentionState(ctx, dbgen.LockSnapshotRetentionStateParams{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	} else if err != nil {
		return SnapshotLease{}, err
	} else if state != "live" {
		return SnapshotLease{}, ErrNotLive
	}
	if err := querygen(tx).UpdateRetentionProtection(ctx, dbgen.UpdateRetentionProtectionParams{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID, ProtectedUntil: pgtype.Timestamptz{Time: in.ExpiresAt.UTC(), Valid: true}}); err != nil {
		return SnapshotLease{}, err
	}
	err = querygen(tx).InsertSnapshotLease(ctx, dbgen.InsertSnapshotLeaseParams{LeaseID: pgUUID(in.LeaseID), DeliveryID: in.DeliveryID, GenerationID: in.GenerationID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, ExpiresAt: pgtype.Timestamptz{Time: in.ExpiresAt.UTC(), Valid: true}, AcquiredAt: pgtype.Timestamptz{Time: acquired, Valid: true}})
	if err != nil {
		return SnapshotLease{}, err
	}
	got, err := LoadLease(ctx, tx, in.LeaseID)
	if err != nil {
		return SnapshotLease{}, err
	}
	if !sameLease(got, in) {
		return SnapshotLease{}, fmt.Errorf("%w: lease %q", ErrConflict, in.LeaseID)
	}
	return got, nil
}

func (r *Repository) AcquireSnapshotLease(ctx context.Context, in AcquireLeaseInput) (SnapshotLease, error) {
	if r == nil {
		return SnapshotLease{}, ErrInvalid
	}
	now, err := databaseClock(ctx, r.db)
	if err != nil {
		return SnapshotLease{}, err
	}
	return acquireSnapshotLeaseAt(ctx, r.db, in, now)
}

// ClaimSnapshotLease is a restart-safe fence check for a worker that already
// has a persisted lease identity. It never transfers ownership or extends a
// deadline: only the exact owner and epoch may claim an unexpired active row.
func ClaimSnapshotLease(ctx context.Context, tx DBTX, claim SnapshotLeaseClaim) (SnapshotLease, error) {
	if tx == nil || !validUUID(claim.LeaseID) || !validID(claim.OwnerID) || claim.FencingEpoch <= 0 {
		return SnapshotLease{}, ErrInvalid
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return SnapshotLease{}, err
	}
	now = now.Truncate(time.Microsecond)
	claimRow, err := querygen(tx).GetSnapshotLeaseClaim(ctx, pgUUID(claim.LeaseID))
	state, owner, epoch, expires := claimRow.State, claimRow.OwnerID, claimRow.FencingEpoch, tsTime(claimRow.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	}
	if err != nil {
		return SnapshotLease{}, err
	}
	if owner != claim.OwnerID || epoch != claim.FencingEpoch {
		return SnapshotLease{}, ErrStaleFence
	}
	if state != string(LeaseActive) || !expires.After(now) {
		if state == string(LeaseActive) {
			return SnapshotLease{}, ErrLeaseExpired
		}
		return SnapshotLease{}, ErrConflict
	}
	return LoadLease(ctx, tx, claim.LeaseID)
}

func (r *Repository) ClaimSnapshotLease(ctx context.Context, claim SnapshotLeaseClaim) (SnapshotLease, error) {
	if r == nil {
		return SnapshotLease{}, ErrInvalid
	}
	return ClaimSnapshotLease(ctx, r.db, claim)
}

func RenewSnapshotLease(ctx context.Context, tx DBTX, fence LeaseFence, expiresAt, now time.Time) error {
	if tx == nil || !validUUID(fence.LeaseID) || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	dbNow, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	if now.Before(dbNow) {
		now = dbNow
	} else {
		now = now.UTC()
	}
	now = now.Truncate(time.Microsecond)
	expiresAt = expiresAt.UTC().Truncate(time.Microsecond)
	if !expiresAt.After(now) {
		return ErrInvalid
	}
	if expiresAt.After(now.Add(maxSnapshotLease)) {
		return ErrInvalid
	}
	// Renew the lease and carry its protection horizon forward in one
	// statement. A caller-owned transaction can therefore not expose a
	// renewed lease whose retention gate still carries the old expiry.
	result, err := querygen(tx).RenewSnapshotLease(ctx, dbgen.RenewSnapshotLeaseParams{LeaseID: pgUUID(fence.LeaseID), OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}, Now: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		got, loadErr := LoadLease(ctx, tx, fence.LeaseID)
		if loadErr != nil {
			return loadErr
		}
		if got.OwnerID != fence.OwnerID || got.FencingEpoch != fence.FencingEpoch {
			return ErrStaleFence
		}
		if got.State == LeaseActive && !got.ExpiresAt.After(now) {
			return ErrLeaseExpired
		}
		return ErrStaleFence
	}
	return nil
}

func (r *Repository) RenewSnapshotLease(ctx context.Context, fence LeaseFence, expiresAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return RenewSnapshotLease(ctx, r.db, fence, expiresAt, time.Time{})
}

func ReleaseSnapshotLease(ctx context.Context, tx DBTX, fence LeaseFence, releasedAt time.Time) error {
	if tx == nil || !validUUID(fence.LeaseID) || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	var clockErr error
	releasedAt, clockErr = databaseClock(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	result, err := querygen(tx).ReleaseSnapshotLease(ctx, dbgen.ReleaseSnapshotLeaseParams{LeaseID: pgUUID(fence.LeaseID), OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, ReleasedAt: pgtype.Timestamptz{Time: releasedAt.UTC(), Valid: true}})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	got, loadErr := LoadLease(ctx, tx, fence.LeaseID)
	if loadErr != nil {
		return loadErr
	}
	if got.OwnerID != fence.OwnerID || got.FencingEpoch != fence.FencingEpoch {
		return ErrStaleFence
	}
	if got.State == LeaseReleased || got.State == LeaseExpired {
		return nil
	}
	return ErrConflict
}

func (r *Repository) ReleaseSnapshotLease(ctx context.Context, fence LeaseFence) error {
	if r == nil {
		return ErrInvalid
	}
	return ReleaseSnapshotLease(ctx, r.db, fence, time.Time{})
}

func ExpireSnapshotLeases(ctx context.Context, tx DBTX, now time.Time) error {
	if tx == nil {
		return ErrInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var clockErr error
	now, clockErr = databaseClock(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	err := querygen(tx).ExpireSnapshotLeases(ctx, pgtype.Timestamptz{Time: now.UTC(), Valid: true})
	return err
}

func (r *Repository) ExpireSnapshotLeases(ctx context.Context, now time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return ExpireSnapshotLeases(ctx, r.db, now)
}

func RetireSnapshot(ctx context.Context, tx DBTX, ref SnapshotRef, retiredAt time.Time) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	if retiredAt.IsZero() {
		retiredAt = time.Now().UTC()
	}
	var clockErr error
	retiredAt, clockErr = databaseClock(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	state, err := querygen(tx).LockSnapshotRetentionForState(ctx, dbgen.LockSnapshotRetentionForStateParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	} else if state == "expired" {
		return ErrNotLive
	} else if state == "quarantined" {
		return ErrQuarantined
	} else if state == "cleanup-complete" {
		return ErrConflict
	} else if state == "retiring" {
		return nil
	}
	roots64, err := querygen(tx).CountSnapshotRootsLiveRetiring(ctx, dbgen.CountSnapshotRootsLiveRetiringParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	roots := int(roots64)
	if err != nil {
		return err
	}
	if roots != 0 {
		return fmt.Errorf("%w: durable snapshot roots remain", ErrConflict)
	}
	result, err := querygen(tx).RetireSnapshot(ctx, dbgen.RetireSnapshotParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID, RetiredAt: pgtype.Timestamptz{Time: retiredAt.UTC(), Valid: true}})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	return ErrConflict
}

func (r *Repository) RetireSnapshot(ctx context.Context, ref SnapshotRef, retiredAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return RetireSnapshot(ctx, r.db, ref, retiredAt)
}

// ExpireSnapshotUnderMaintenanceFence performs the control-ledger transition
// with the pool maintenance fence in the same SQL UPDATE predicate. The
// explicit root/lease checks make a stale worker fail closed even if it was
// preempted between a fence read and this write.
func ExpireSnapshotUnderMaintenanceFence(ctx context.Context, tx DBTX, ref SnapshotRef, evidence json.RawMessage, expiredAt time.Time, maintenanceID string, maintenance RetentionMaintenanceFence) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	if !validUUID(maintenanceID) {
		return ErrInvalid
	}
	if err := validateRetentionFence(maintenance); err != nil {
		return err
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: expiration evidence is required", ErrInvalid)
	}
	if err := CheckRetentionMaintenanceFence(ctx, tx, maintenance); err != nil {
		return err
	}
	if expiredAt.IsZero() {
		expiredAt, err = databaseClock(ctx, tx)
		if err != nil {
			return err
		}
	}
	result, err := querygen(tx).ExpireSnapshotUnderMaintenanceFence(ctx, dbgen.ExpireSnapshotUnderMaintenanceFenceParams{
		ExpiredAt: pgtype.Timestamptz{Time: expiredAt.UTC(), Valid: true}, Evidence: []byte(canonical),
		PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID,
		MaintenanceID: upgradeUUID(maintenanceID), MaintenanceOwnerID: maintenance.OwnerID, MaintenanceFencingEpoch: maintenance.FencingEpoch,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	return ErrConflict
}

func (r *Repository) ExpireSnapshotUnderMaintenanceFence(ctx context.Context, ref SnapshotRef, evidence json.RawMessage, expiredAt time.Time, maintenanceID string, maintenance RetentionMaintenanceFence) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error {
		return ExpireSnapshotUnderMaintenanceFence(ctx, tx, ref, evidence, expiredAt, maintenanceID, maintenance)
	})
}

// ClaimSnapshotCleanupUnderMaintenanceFence binds the cleanup claim to the
// active pool fence. The SQL UPDATE repeats the fence owner/epoch/clock check.
func ClaimSnapshotCleanupUnderMaintenanceFence(ctx context.Context, tx DBTX, ref SnapshotRef, ownerID string, leaseExpiresAt time.Time, maintenanceID string, maintenance RetentionMaintenanceFence) (CleanupFence, error) {
	if tx == nil || !validSnapshotRef(ref) || !validID(ownerID) {
		return CleanupFence{}, ErrInvalid
	}
	if !validUUID(maintenanceID) {
		return CleanupFence{}, ErrInvalid
	}
	if err := validateRetentionFence(maintenance); err != nil {
		return CleanupFence{}, err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return CleanupFence{}, err
	}
	if err := CheckRetentionMaintenanceFence(ctx, tx, maintenance); err != nil {
		return CleanupFence{}, err
	}
	row, err := querygen(tx).LockSnapshotRetentionCleanup(ctx, dbgen.LockSnapshotRetentionCleanupParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return CleanupFence{}, ErrNotFound
	}
	if err != nil {
		return CleanupFence{}, err
	}
	state, currentOwner, currentEpoch, currentExpiry := row.State, row.CleanupOwnerID, row.CleanupFencingEpoch, row.CleanupLeaseExpiresAt
	if state == string(RetentionCleanupComplete) {
		return CleanupFence{}, ErrConflict
	}
	if state != string(RetentionExpired) && state != string(RetentionQuarantined) {
		return CleanupFence{}, ErrCleanupPending
	}
	if currentOwner != nil && currentExpiry.Valid && currentExpiry.Time.After(now) {
		if *currentOwner == ownerID {
			return CleanupFence{OwnerID: ownerID, FencingEpoch: currentEpoch, LeaseExpiresAt: currentExpiry.Time.UTC()}, nil
		}
		return CleanupFence{}, ErrCleanupBusy
	}
	if leaseExpiresAt.IsZero() {
		leaseExpiresAt = now.Add(maxSnapshotLease)
	}
	leaseExpiresAt = leaseExpiresAt.UTC().Truncate(time.Microsecond)
	if !leaseExpiresAt.After(now) || leaseExpiresAt.After(now.Add(maxSnapshotLease)) {
		return CleanupFence{}, ErrInvalid
	}
	epoch, err := querygen(tx).ClaimSnapshotCleanupUnderMaintenanceFence(ctx, dbgen.ClaimSnapshotCleanupUnderMaintenanceFenceParams{
		CleanupOwnerID: ownerID, CleanupLeaseExpiresAt: pgtype.Timestamptz{Time: leaseExpiresAt, Valid: true},
		PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID,
		MaintenanceID: upgradeUUID(maintenanceID), MaintenanceOwnerID: maintenance.OwnerID, MaintenanceFencingEpoch: maintenance.FencingEpoch,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CleanupFence{}, ErrRetentionMaintenanceFenceStale
		}
		return CleanupFence{}, err
	}
	if epoch <= 0 {
		return CleanupFence{}, ErrConflict
	}
	return CleanupFence{OwnerID: ownerID, FencingEpoch: epoch, LeaseExpiresAt: leaseExpiresAt}, nil
}

func (r *Repository) ClaimSnapshotCleanupUnderMaintenanceFence(ctx context.Context, ref SnapshotRef, ownerID string, leaseExpiresAt time.Time, maintenanceID string, maintenance RetentionMaintenanceFence) (CleanupFence, error) {
	if r == nil {
		return CleanupFence{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (CleanupFence, error) {
		return ClaimSnapshotCleanupUnderMaintenanceFence(ctx, tx, ref, ownerID, leaseExpiresAt, maintenanceID, maintenance)
	})
}

func QuarantineSnapshotUnderMaintenanceFence(ctx context.Context, tx DBTX, ref SnapshotRef, evidence json.RawMessage, fence CleanupFence, maintenanceID string, maintenance RetentionMaintenanceFence) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	if !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	if !validUUID(maintenanceID) {
		return ErrInvalid
	}
	if err := validateRetentionFence(maintenance); err != nil {
		return err
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: quarantine evidence is required", ErrInvalid)
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	if err := CheckRetentionMaintenanceFence(ctx, tx, maintenance); err != nil {
		return err
	}
	row, err := querygen(tx).LockSnapshotRetentionQuarantine(ctx, dbgen.LockSnapshotRetentionQuarantineParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.CleanupOwnerID == nil || *row.CleanupOwnerID != fence.OwnerID || row.CleanupFencingEpoch != fence.FencingEpoch {
		return ErrStaleFence
	}
	if row.State == string(RetentionCleanupComplete) {
		return ErrConflict
	}
	if row.State == string(RetentionQuarantined) {
		if !evidenceEqual(row.QuarantineEvidence, canonical) {
			return fmt.Errorf("%w: quarantine evidence differs", ErrConflict)
		}
		return nil
	}
	if !row.CleanupLeaseExpiresAt.Valid || !row.CleanupLeaseExpiresAt.Time.After(now) {
		return ErrLeaseExpired
	}
	if row.State != string(RetentionExpired) {
		return fmt.Errorf("%w: snapshot must be expired before quarantine", ErrConflict)
	}
	cleanupOwner := fence.OwnerID
	result, err := querygen(tx).QuarantineSnapshotUnderMaintenanceFence(ctx, dbgen.QuarantineSnapshotUnderMaintenanceFenceParams{
		QuarantineEvidence: []byte(canonical), QuarantinedAt: pgtype.Timestamptz{Time: now, Valid: true},
		PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID,
		CleanupOwnerID: cleanupOwner, CleanupFencingEpoch: fence.FencingEpoch,
		MaintenanceID: upgradeUUID(maintenanceID), MaintenanceOwnerID: maintenance.OwnerID, MaintenanceFencingEpoch: maintenance.FencingEpoch,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrRetentionMaintenanceFenceStale
	}
	return nil
}

func (r *Repository) QuarantineSnapshotUnderMaintenanceFence(ctx context.Context, ref SnapshotRef, evidence json.RawMessage, fence CleanupFence, maintenanceID string, maintenance RetentionMaintenanceFence) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error {
		return QuarantineSnapshotUnderMaintenanceFence(ctx, tx, ref, evidence, fence, maintenanceID, maintenance)
	})
}

// CompleteSnapshotCleanup records a successful physical cleanup only after a
// snapshot has passed through quarantine. The transition is idempotent for an
// exact evidence replay and cannot reopen or rewrite a terminal row.
func CompleteSnapshotCleanupUnderMaintenanceFence(ctx context.Context, tx DBTX, ref SnapshotRef, evidence json.RawMessage, fence CleanupFence, maintenanceID string, maintenance RetentionMaintenanceFence) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	if !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	if !validUUID(maintenanceID) {
		return ErrInvalid
	}
	if err := validateRetentionFence(maintenance); err != nil {
		return err
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: cleanup evidence is required", ErrInvalid)
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	if err := CheckRetentionMaintenanceFence(ctx, tx, maintenance); err != nil {
		return err
	}
	row, err := querygen(tx).LockSnapshotRetentionComplete(ctx, dbgen.LockSnapshotRetentionCompleteParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.CleanupOwnerID == nil || *row.CleanupOwnerID != fence.OwnerID || row.CleanupFencingEpoch != fence.FencingEpoch {
		return ErrStaleFence
	}
	if row.State == string(RetentionCleanupComplete) {
		if !evidenceEqual(row.CleanupEvidence, canonical) {
			return fmt.Errorf("%w: cleanup evidence differs", ErrConflict)
		}
		return nil
	}
	if row.State != string(RetentionQuarantined) {
		return fmt.Errorf("%w: snapshot must be quarantined before cleanup-complete", ErrConflict)
	}
	if !row.CleanupLeaseExpiresAt.Valid || !row.CleanupLeaseExpiresAt.Time.After(now) {
		return ErrLeaseExpired
	}
	cleanupOwner := fence.OwnerID
	result, err := querygen(tx).CompleteSnapshotCleanupUnderMaintenanceFence(ctx, dbgen.CompleteSnapshotCleanupUnderMaintenanceFenceParams{
		CleanupEvidence: []byte(canonical), CleanupCompletedAt: pgtype.Timestamptz{Time: now, Valid: true},
		PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID,
		CleanupOwnerID: cleanupOwner, CleanupFencingEpoch: fence.FencingEpoch,
		MaintenanceID: upgradeUUID(maintenanceID), MaintenanceOwnerID: maintenance.OwnerID, MaintenanceFencingEpoch: maintenance.FencingEpoch,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrRetentionMaintenanceFenceStale
	}
	return nil
}

func (r *Repository) CompleteSnapshotCleanupUnderMaintenanceFence(ctx context.Context, ref SnapshotRef, evidence json.RawMessage, fence CleanupFence, maintenanceID string, maintenance RetentionMaintenanceFence) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error {
		return CompleteSnapshotCleanupUnderMaintenanceFence(ctx, tx, ref, evidence, fence, maintenanceID, maintenance)
	})
}

func LoadLease(ctx context.Context, db DBTX, id string) (SnapshotLease, error) {
	if db == nil || !validUUID(id) {
		return SnapshotLease{}, ErrInvalid
	}
	row, err := querygen(db).GetSnapshotLease(ctx, pgUUID(id))
	l := SnapshotLease{LeaseID: row.LeaseID, DeliveryID: row.DeliveryID, GenerationID: row.GenerationID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, State: SnapshotLeaseState(row.State), ExpiresAt: tsTime(row.ExpiresAt), AcquiredAt: tsTime(row.AcquiredAt), ReleasedAt: tsTime(row.ReleasedAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	}
	if err != nil {
		return SnapshotLease{}, err
	}
	return l, nil
}

func (r *Repository) LoadLease(ctx context.Context, id string) (SnapshotLease, error) {
	if r == nil {
		return SnapshotLease{}, ErrInvalid
	}
	return LoadLease(ctx, r.db, id)
}

func loadSnapshotRetention(ctx context.Context, db DBTX, ref SnapshotRef) (SnapshotRetention, error) {
	if db == nil || !validSnapshotRef(ref) {
		return SnapshotRetention{}, ErrInvalid
	}
	row, err := querygen(db).GetSnapshotRetention(ctx, dbgen.GetSnapshotRetentionParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	out := SnapshotRetention{PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, State: SnapshotRetentionState(row.State), CleanupOwnerID: "", CleanupFencingEpoch: row.CleanupFencingEpoch, CreatedAt: tsTime(row.CreatedAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotRetention{}, ErrNotFound
	}
	if err != nil {
		return SnapshotRetention{}, err
	}
	if row.CleanupOwnerID != nil {
		out.CleanupOwnerID = *row.CleanupOwnerID
	}
	out.ProtectedUntil, out.RetiredAt, out.ExpiredAt, out.CleanupLeaseExpiresAt, out.QuarantinedAt, out.CleanupCompletedAt = tsTime(row.ProtectedUntil), tsTime(row.RetiredAt), tsTime(row.ExpiredAt), tsTime(row.CleanupLeaseExpiresAt), tsTime(row.QuarantinedAt), tsTime(row.CleanupCompletedAt)
	out.Evidence = append(json.RawMessage(nil), row.Evidence...)
	out.QuarantineEvidence = append(json.RawMessage(nil), row.QuarantineEvidence...)
	out.CleanupEvidence = append(json.RawMessage(nil), row.CleanupEvidence...)
	return out, nil
}

func LoadSnapshotRetention(ctx context.Context, db DBTX, ref SnapshotRef) (SnapshotRetention, error) {
	return loadSnapshotRetention(ctx, db, ref)
}

func (r *Repository) LoadSnapshotRetention(ctx context.Context, ref SnapshotRef) (SnapshotRetention, error) {
	if r == nil {
		return SnapshotRetention{}, ErrInvalid
	}
	return loadSnapshotRetention(ctx, r.db, ref)
}

// ListRetainedSnapshots returns every snapshot that remains reachable for a
// pool. Upgrade authority uses this exact relational set when requalifying a
// catalog; callers cannot accidentally omit a retiring snapshot by supplying
// a hand-maintained list.
func ListRetainedSnapshots(ctx context.Context, db DBTX, physicalPoolID, catalogID string) ([]SnapshotRef, error) {
	if db == nil || !validID(physicalPoolID) || !validID(catalogID) {
		return nil, ErrInvalid
	}
	ids, err := querygen(db).ListRetainedSnapshotIDs(ctx, dbgen.ListRetainedSnapshotIDsParams{PhysicalPoolID: physicalPoolID, CatalogID: catalogID})
	if err != nil {
		return nil, err
	}
	var out []SnapshotRef
	for _, snapshotID := range ids {
		out = append(out, SnapshotRef{PhysicalPoolID: physicalPoolID, CatalogID: catalogID, SnapshotID: snapshotID})
	}
	return out, nil
}

func (r *Repository) ListRetainedSnapshots(ctx context.Context, physicalPoolID, catalogID string) ([]SnapshotRef, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return ListRetainedSnapshots(ctx, r.db, physicalPoolID, catalogID)
}

func listSnapshotReaders(ctx context.Context, db DBTX, ref SnapshotRef, overdueOnly bool) ([]ReaderDrain, error) {
	if db == nil || (ref != (SnapshotRef{}) && !validSnapshotRef(ref)) {
		return nil, ErrInvalid
	}
	var rows []dbgen.ListSnapshotReadersAllRow
	var err error
	if ref != (SnapshotRef{}) {
		r, e := querygen(db).ListSnapshotReadersByRef(ctx, dbgen.ListSnapshotReadersByRefParams{PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
		err = e
		for _, x := range r {
			rows = append(rows, dbgen.ListSnapshotReadersAllRow{LeaseID: x.LeaseID, DeliveryID: x.DeliveryID, GenerationID: x.GenerationID, PhysicalPoolID: x.PhysicalPoolID, CatalogID: x.CatalogID, SnapshotID: x.SnapshotID, OwnerID: x.OwnerID, FencingEpoch: x.FencingEpoch, State: x.State, AcquiredAt: x.AcquiredAt, ExpiresAt: x.ExpiresAt, Overdue: x.Overdue, NonDraining: x.NonDraining})
		}
	} else if overdueOnly {
		r, e := querygen(db).ListOverdueSnapshotReaders(ctx)
		err = e
		for _, x := range r {
			rows = append(rows, dbgen.ListSnapshotReadersAllRow{LeaseID: x.LeaseID, DeliveryID: x.DeliveryID, GenerationID: x.GenerationID, PhysicalPoolID: x.PhysicalPoolID, CatalogID: x.CatalogID, SnapshotID: x.SnapshotID, OwnerID: x.OwnerID, FencingEpoch: x.FencingEpoch, State: x.State, AcquiredAt: x.AcquiredAt, ExpiresAt: x.ExpiresAt, Overdue: x.Overdue, NonDraining: x.NonDraining})
		}
	} else {
		rows, err = querygen(db).ListSnapshotReadersAll(ctx)
	}
	if err != nil {
		return nil, err
	}
	var out []ReaderDrain
	for _, row := range rows {
		var d ReaderDrain
		d.LeaseID, d.DeliveryID, d.GenerationID, d.PhysicalPoolID, d.CatalogID, d.SnapshotID, d.OwnerID, d.FencingEpoch, d.State, d.AcquiredAt, d.ExpiresAt = row.LeaseID, row.DeliveryID, row.GenerationID, row.PhysicalPoolID, row.CatalogID, row.SnapshotID, row.OwnerID, row.FencingEpoch, SnapshotLeaseState(row.State), tsTime(row.AcquiredAt), tsTime(row.ExpiresAt)
		if row.Overdue != nil {
			d.Overdue = *row.Overdue
		}
		if row.NonDraining != nil {
			d.NonDraining = *row.NonDraining
		}
		out = append(out, d)
	}
	return out, nil
}

func ListSnapshotReaders(ctx context.Context, db DBTX, ref SnapshotRef) ([]ReaderDrain, error) {
	return listSnapshotReaders(ctx, db, ref, false)
}

func (r *Repository) ListSnapshotReaders(ctx context.Context, ref SnapshotRef) ([]ReaderDrain, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return listSnapshotReaders(ctx, r.db, ref, false)
}

func ListOverdueSnapshotReaders(ctx context.Context, db DBTX) ([]ReaderDrain, error) {
	return listSnapshotReaders(ctx, db, SnapshotRef{}, true)
}

func (r *Repository) ListOverdueSnapshotReaders(ctx context.Context) ([]ReaderDrain, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return listSnapshotReaders(ctx, r.db, SnapshotRef{}, true)
}

func ReadRetentionBacklog(ctx context.Context, db DBTX) (RetentionBacklog, error) {
	if db == nil {
		return RetentionBacklog{}, ErrInvalid
	}
	row, err := querygen(db).ReadRetentionBacklog(ctx)
	out := RetentionBacklog{CleanupPending: row.CleanupPending, Quarantined: row.Quarantined, Orphans: row.Orphans, OverdueReaders: row.OverdueReaders, NonDrainingReaders: row.NonDrainingReaders}
	return out, err
}

func (r *Repository) ReadRetentionBacklog(ctx context.Context) (RetentionBacklog, error) {
	if r == nil {
		return RetentionBacklog{}, ErrInvalid
	}
	return ReadRetentionBacklog(ctx, r.db)
}

func listRetentionByState(ctx context.Context, db DBTX, states ...SnapshotRetentionState) ([]SnapshotRetention, error) {
	if db == nil || len(states) == 0 {
		return nil, ErrInvalid
	}
	values := make([]string, len(states))
	for i, state := range states {
		values[i] = string(state)
	}
	rows, err := querygen(db).ListRetentionByState(ctx, values)
	if err != nil {
		return nil, err
	}
	var out []SnapshotRetention
	for _, row := range rows {
		item := SnapshotRetention{PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, State: SnapshotRetentionState(row.State), CleanupFencingEpoch: row.CleanupFencingEpoch, CreatedAt: tsTime(row.CreatedAt)}
		if row.CleanupOwnerID != nil {
			item.CleanupOwnerID = *row.CleanupOwnerID
		}
		item.Evidence = append(json.RawMessage(nil), row.Evidence...)
		item.QuarantineEvidence = append(json.RawMessage(nil), row.QuarantineEvidence...)
		item.CleanupEvidence = append(json.RawMessage(nil), row.CleanupEvidence...)
		item.ProtectedUntil, item.RetiredAt, item.ExpiredAt, item.CleanupLeaseExpiresAt, item.QuarantinedAt, item.CleanupCompletedAt = tsTime(row.ProtectedUntil), tsTime(row.RetiredAt), tsTime(row.ExpiredAt), tsTime(row.CleanupLeaseExpiresAt), tsTime(row.QuarantinedAt), tsTime(row.CleanupCompletedAt)
		out = append(out, item)
	}
	return out, nil
}

func ListQuarantineBacklog(ctx context.Context, db DBTX) ([]SnapshotRetention, error) {
	return listRetentionByState(ctx, db, RetentionQuarantined)
}

func (r *Repository) ListQuarantineBacklog(ctx context.Context) ([]SnapshotRetention, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return ListQuarantineBacklog(ctx, r.db)
}

func ListCleanupBacklog(ctx context.Context, db DBTX) ([]SnapshotRetention, error) {
	return listRetentionByState(ctx, db, RetentionExpired, RetentionQuarantined)
}

func (r *Repository) ListCleanupBacklog(ctx context.Context) ([]SnapshotRetention, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return ListCleanupBacklog(ctx, r.db)
}

// RetentionMetrics is an alias retained for metrics collectors that use the
// conventional naming rather than backlog terminology.
type RetentionMetrics = RetentionBacklog

func RetentionMetricsSnapshot(ctx context.Context, db DBTX) (RetentionMetrics, error) {
	return ReadRetentionBacklog(ctx, db)
}

func validateCatalog(c CatalogIdentity) error {
	for _, value := range []string{c.PhysicalPoolID, c.CatalogID} {
		if !validID(value) {
			return ErrInvalid
		}
	}
	if !validCatalogDatabase(c.CatalogDatabase) || !validCatalogUUID(c.CatalogUUID) || !validSchema(c.MetadataSchema) {
		return ErrInvalid
	}
	return nil
}

func validSnapshotRef(ref SnapshotRef) bool {
	return validID(ref.PhysicalPoolID) && validID(ref.CatalogID) && ref.SnapshotID > 0
}

func validRootKind(kind SnapshotRootKind) bool {
	switch kind {
	case RootCandidate, RootGeneration, RootRollback, RootRecovery,
		RootActive, RootCache, RootLineage, RootDelivery:
		return true
	default:
		return false
	}
}

func validateBinding(in GenerationBinding) error {
	if !validID(in.DeliveryID) || !validID(in.GenerationID) || !validUUID(in.AttemptID) || !validSnapshotRef(SnapshotRef{in.PhysicalPoolID, in.CatalogID, in.SnapshotID}) || in.FencingEpoch <= 0 {
		return ErrInvalid
	}
	for _, digest := range []string{in.RelationManifestDigest, in.CompatibilityDigest, in.ServingArtifactDigest, in.RequestDigest, in.PlanDigest} {
		if !validDigest(digest) {
			return ErrInvalid
		}
	}
	return nil
}

func validateBegin(in BeginAttemptInput) error {
	return validateBeginAt(in, time.Now().UTC())
}

func validateBeginAt(in BeginAttemptInput, now time.Time) error {
	if !validUUID(in.AttemptID) || !validID(in.OwnerID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validDigest(in.RequestDigest) || !validDigest(in.PlanDigest) || in.FencingEpoch <= 0 || !validID(in.SessionIdentity) {
		return ErrInvalid
	}
	lease := in.LeaseExpiresAt.UTC()
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if lease.IsZero() || !lease.After(now) || lease.After(now.Add(maxAttemptLease)) {
		return ErrInvalid
	}
	return nil
}

func validMarkerQuarantineInput(in MarkerQuarantineInput) bool {
	if !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validUUID(in.AttemptID) ||
		!validDigest(in.RequestDigest) || !validDigest(in.PlanDigest) || !validMarkerQuarantineReason(in.Reason) ||
		!validDigest(in.ObservedMarkerDigest) || len(in.ObservedSnapshotIDs) > 128 {
		return false
	}
	for _, id := range in.ObservedSnapshotIDs {
		if id <= 0 {
			return false
		}
	}
	return true
}

func validMarkerQuarantineReason(reason MarkerQuarantineReason) bool {
	switch reason {
	case MarkerQuarantineDuplicate, MarkerQuarantineDigestMismatch, MarkerQuarantineIdentityMismatch:
		return true
	default:
		return false
	}
}

func sameMarkerQuarantine(got MarkerQuarantine, in MarkerQuarantineInput, canonicalEvidenceJSON string) bool {
	if got.PhysicalPoolID != in.PhysicalPoolID || got.CatalogID != in.CatalogID || got.AttemptID != in.AttemptID ||
		got.RequestDigest != in.RequestDigest || got.PlanDigest != in.PlanDigest || got.Reason != in.Reason ||
		got.ObservedMarkerDigest != in.ObservedMarkerDigest || len(got.ObservedSnapshotIDs) != len(in.ObservedSnapshotIDs) {
		return false
	}
	for i := range got.ObservedSnapshotIDs {
		if got.ObservedSnapshotIDs[i] != in.ObservedSnapshotIDs[i] {
			return false
		}
	}
	return evidenceEqual(got.Evidence, canonicalEvidenceJSON)
}

func validID(value string) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= maxID && !strings.ContainsRune(value, '\x00')
}

func validCatalogDatabase(value string) bool {
	return validID(value) && !strings.ContainsAny(value, "\r\n")
}

func validCatalogUUID(value string) bool {
	u, err := uuid.Parse(value)
	return err == nil && u.String() == value
}

func validSchema(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxSchema {
		return false
	}
	for i, r := range value {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// validUUID admits UUID-shaped identities from the platform's UUIDv4/v7
// generators. The repository intentionally does not generate IDs: callers
// provide the externally durable attempt, root, and lease identity so retries
// can replay the exact UUID across processes and deployments.
func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func canonicalEvidence(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || len(raw) > maxEvidence {
		return "", ErrInvalid
	}
	var object map[string]any
	if err := strictjson.Decode(raw, &object); err != nil || len(object) == 0 {
		return "", ErrInvalid
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func canonicalOptionalEvidence(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	return canonicalEvidence(raw)
}

func sameCatalog(a, b CatalogIdentity) bool {
	return a.PhysicalPoolID == b.PhysicalPoolID && a.CatalogDatabase == b.CatalogDatabase && a.CatalogID == b.CatalogID && a.CatalogUUID == b.CatalogUUID && a.MetadataSchema == b.MetadataSchema
}

func sameBinding(a, b GenerationBinding) bool {
	return a.DeliveryID == b.DeliveryID && a.GenerationID == b.GenerationID && a.AttemptID == b.AttemptID && a.PhysicalPoolID == b.PhysicalPoolID && a.CatalogID == b.CatalogID && a.SnapshotID == b.SnapshotID && a.RelationManifestDigest == b.RelationManifestDigest && a.CompatibilityDigest == b.CompatibilityDigest && a.ServingArtifactDigest == b.ServingArtifactDigest && a.RequestDigest == b.RequestDigest && a.PlanDigest == b.PlanDigest && a.FencingEpoch == b.FencingEpoch
}

func sameLease(got SnapshotLease, in AcquireLeaseInput) bool {
	return got.LeaseID == in.LeaseID && got.DeliveryID == in.DeliveryID && got.GenerationID == in.GenerationID && got.PhysicalPoolID == in.PhysicalPoolID && got.CatalogID == in.CatalogID && got.SnapshotID == in.SnapshotID && got.OwnerID == in.OwnerID && got.FencingEpoch == in.FencingEpoch && got.State == LeaseActive && got.ExpiresAt.Equal(in.ExpiresAt.UTC()) && got.AcquiredAt.Equal(in.AcquiredAt.UTC())
}

func markersEqual(a, b string) bool {
	if a == b {
		return true
	}
	var am, bm ducklake.CommitMarker
	if err := strictjson.Decode([]byte(a), &am); err != nil {
		return false
	}
	if err := strictjson.Decode([]byte(b), &bm); err != nil {
		return false
	}
	ac, err := am.CanonicalJSON()
	if err != nil {
		return false
	}
	bc, err := bm.CanonicalJSON()
	return err == nil && ac == bc
}

func evidenceEqual(a json.RawMessage, b string) bool {
	canonical, err := canonicalEvidence(a)
	if err != nil {
		return false
	}
	expected, err := canonicalEvidence([]byte(b))
	return err == nil && canonical == expected
}
