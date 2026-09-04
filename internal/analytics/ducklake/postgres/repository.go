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
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
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
	ErrInvalid             = errors.New("invalid DuckLake PostgreSQL identity")
	ErrConflict            = errors.New("DuckLake PostgreSQL identity conflict")
	ErrNotFound            = errors.New("DuckLake PostgreSQL identity not found")
	ErrNotLive             = errors.New("DuckLake snapshot is not live")
	ErrCleanupLeaseExpired = errors.New("DuckLake snapshot cleanup lease is expired")
	ErrStaleFence          = errors.New("DuckLake owner fencing epoch is stale")
	ErrMarkerQuarantined   = errors.New("DuckLake physical pool has a marker quarantine")
	ErrCleanupPending      = errors.New("DuckLake snapshot cleanup is pending")
	ErrCleanupBusy         = errors.New("DuckLake snapshot cleanup is owned by another worker")
	ErrClockUnavailable    = errors.New("DuckLake PostgreSQL clock unavailable")
)

const (
	maxID           = 255
	maxSchema       = 128
	maxEvidence     = 32768
	maxCleanupLease = 24 * time.Hour
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

// MarkerQuarantineReason identifies why exact external marker reconciliation
// could not safely select one committed snapshot.
type MarkerQuarantineReason string

const (
	MarkerQuarantineDuplicate        MarkerQuarantineReason = "duplicate"
	MarkerQuarantineDigestMismatch   MarkerQuarantineReason = "digest_mismatch"
	MarkerQuarantineIdentityMismatch MarkerQuarantineReason = "identity_mismatch"
)

// MarkerQuarantine is immutable, pool-wide evidence of an external DuckLake
// marker anomaly. It is intentionally separate from the delivery attempt's
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

// CleanupFence is a persisted worker claim. A successor claim increments the
// epoch, fencing every stale owner even when its process or connection was
// restarted.
type CleanupFence struct {
	OwnerID        string
	FencingEpoch   int64
	LeaseExpiresAt time.Time
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
// must acquire this transaction-scoped pool lock before locking any attempt
// row, preserving the repository-wide quarantine-before-attempt lock order.
// db must be a caller-owned transaction. Repository entrypoints supply one;
// low-level transaction variants require their callers to preserve the same
// boundary.
func lockMarkerQuarantineScope(ctx context.Context, db DBTX, physicalPoolID string) error {
	if db == nil || !validID(physicalPoolID) {
		return ErrInvalid
	}
	queries := querygen(db)
	if err := queries.AcquireMarkerQuarantineScopeLock(ctx, physicalPoolID); err != nil {
		return err
	}
	identity, err := queries.GetCatalogIdentity(ctx, physicalPoolID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if identity.PhysicalPoolID != physicalPoolID {
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

// ValidateBuildAdmissionTx is the narrow physical admission guard used by
// canonical delivery admission. It acquires the migration/maintenance fence
// scope first, then the marker-quarantine pool scope, and finally checks the
// immutable quarantine ledger. It intentionally performs no attempt or
// generation lifecycle writes.
func (r *Repository) ValidateBuildAdmissionTx(ctx context.Context, tx Tx, physicalPoolID, catalogID string) error {
	if r == nil || tx == nil || !r.Configured() {
		return ErrInvalid
	}
	if err := lockAttemptAdmissionScope(ctx, tx, physicalPoolID, catalogID); err != nil {
		return err
	}
	if err := lockMarkerQuarantineScope(ctx, tx, physicalPoolID); err != nil {
		return err
	}
	quarantined, err := markerQuarantineExistsForPool(ctx, tx, physicalPoolID)
	if err != nil {
		return err
	}
	if quarantined {
		return ErrMarkerQuarantined
	}
	return nil
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

// AdmitSnapshotRetentionFromSealTx derives and locks the physical retention
// gate from one canonical delivery snapshot seal.  The caller owns tx; this
// method never begins, commits, or rolls it back.  A missing seal, missing
// catalog identity, or non-live existing row fails closed.
func (r *Repository) AdmitSnapshotRetentionFromSealTx(ctx context.Context, tx Tx, sealID string) error {
	if r == nil || tx == nil || !r.Configured() || !validUUID(sealID) {
		return ErrInvalid
	}
	seal, err := querygen(tx).GetSnapshotRetentionSealIdentity(ctx, pgUUID(sealID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := r.ValidateBuildAdmissionTx(ctx, tx, seal.PhysicalPoolID, seal.CatalogID); err != nil {
		return err
	}
	row, err := querygen(tx).AdmitSnapshotRetentionFromSeal(ctx, pgUUID(sealID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.RetentionState != string(RetentionLive) {
		return ErrNotLive
	}
	return nil
}

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
		leaseExpiresAt = now.Add(maxCleanupLease)
	}
	leaseExpiresAt = leaseExpiresAt.UTC().Truncate(time.Microsecond)
	if !leaseExpiresAt.After(now) || leaseExpiresAt.After(now.Add(maxCleanupLease)) {
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
		return ErrCleanupLeaseExpired
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
		return ErrCleanupLeaseExpired
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

func validMarkerQuarantineInput(in MarkerQuarantineInput) bool {
	if !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validUUID(in.AttemptID) ||
		platformdigest.ValidateSHA256Identity(in.RequestDigest) != nil || platformdigest.ValidateSHA256Identity(in.PlanDigest) != nil || !validMarkerQuarantineReason(in.Reason) ||
		platformdigest.ValidateSHA256Identity(in.ObservedMarkerDigest) != nil || len(in.ObservedSnapshotIDs) > 128 {
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

// validUUID admits UUID-shaped identities from the platform's UUIDv4/v7
// generators. The repository intentionally does not generate IDs: callers
// provide externally durable identities so retries can replay the exact UUID
// across processes and deployments.
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
