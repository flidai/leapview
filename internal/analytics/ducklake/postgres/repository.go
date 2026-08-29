// Package postgres owns the control-plane identity and lifecycle records for
// a PostgreSQL-backed DuckLake catalog.  DuckDB remains the local data-plane:
// this package never opens a DuckDB connection, stores catalog bytes, or
// guesses a snapshot from catalog recency.
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
	"github.com/flidai/leapview/pkg/strictjson"
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

type Tx = DBTX

var (
	ErrInvalid       = errors.New("invalid DuckLake PostgreSQL identity")
	ErrConflict      = errors.New("DuckLake PostgreSQL identity conflict")
	ErrNotFound      = errors.New("DuckLake PostgreSQL identity not found")
	ErrNotLive       = errors.New("DuckLake snapshot is not live")
	ErrLeaseExpired  = errors.New("DuckLake snapshot lease is expired")
	ErrStaleFence    = errors.New("DuckLake owner fencing epoch is stale")
	ErrAttemptBusy   = errors.New("DuckLake build attempt is owned by another worker")
	ErrIndeterminate = errors.New("DuckLake build attempt outcome is indeterminate")
)

const (
	maxID            = 255
	maxSchema        = 128
	maxEvidence      = 32768
	maxAttemptLease  = 24 * time.Hour
	maxSnapshotLease = 24 * time.Hour
)

type CatalogIdentity struct {
	PhysicalPoolID       string
	CatalogID            string
	MetadataSchema       string
	CompatibilityDigest  string
	CatalogSchemaVersion string
	CreatedAt            time.Time
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
)

type SnapshotRoot struct {
	RootID         string
	PhysicalPoolID string
	CatalogID      string
	SnapshotID     int64
	Kind           SnapshotRootKind
	State          string
	CreatedAt      time.Time
	RetiredAt      time.Time
	ExpiredAt      time.Time
}

type SnapshotRootInput struct {
	RootID         string
	PhysicalPoolID string
	CatalogID      string
	SnapshotID     int64
	Kind           SnapshotRootKind
	CreatedAt      time.Time
}

type LeaseFence struct {
	LeaseID      string
	OwnerID      string
	FencingEpoch int64
}

type Repository struct {
	db  DBTX
	now func() time.Time
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
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository { return &Repository{db: db, now: time.Now} }

func (r *Repository) clock() time.Time {
	if r == nil || r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
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
	_, err := tx.Exec(ctx, `INSERT INTO ducklake.catalog_identity
(physical_pool_id,catalog_id,metadata_schema,compatibility_digest,catalog_schema_version)
VALUES ($1,$2,$3,$4,$5) ON CONFLICT (physical_pool_id) DO NOTHING`, identity.PhysicalPoolID, identity.CatalogID, identity.MetadataSchema, identity.CompatibilityDigest, identity.CatalogSchemaVersion)
	if err != nil {
		return CatalogIdentity{}, err
	}
	var got CatalogIdentity
	err = tx.QueryRow(ctx, `SELECT physical_pool_id,catalog_id,metadata_schema,compatibility_digest,catalog_schema_version,created_at
FROM ducklake.catalog_identity WHERE physical_pool_id=$1`, identity.PhysicalPoolID).Scan(&got.PhysicalPoolID, &got.CatalogID, &got.MetadataSchema, &got.CompatibilityDigest, &got.CatalogSchemaVersion, &got.CreatedAt)
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
	var got CatalogIdentity
	err := db.QueryRow(ctx, `SELECT physical_pool_id,catalog_id,metadata_schema,compatibility_digest,catalog_schema_version,created_at
FROM ducklake.catalog_identity WHERE physical_pool_id=$1`, poolID).Scan(&got.PhysicalPoolID, &got.CatalogID, &got.MetadataSchema, &got.CompatibilityDigest, &got.CatalogSchemaVersion, &got.CreatedAt)
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
	_, err := tx.Exec(ctx, `INSERT INTO ducklake.snapshot_retention (physical_pool_id,catalog_id,snapshot_id,state)
VALUES ($1,$2,$3,'live') ON CONFLICT (physical_pool_id,catalog_id,snapshot_id) DO NOTHING`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID)
	if err != nil {
		return err
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 FOR UPDATE`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&state); err != nil {
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
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 FOR UPDATE`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&state)
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
	var state string
	var req, plan, pool, catalog, marker string
	var fence int64
	var snap int64
	err := tx.QueryRow(ctx, `SELECT state,request_digest,plan_digest,physical_pool_id,catalog_id,fencing_epoch,snapshot_id,COALESCE(commit_marker::text,'')
FROM ducklake.attempt_evidence WHERE attempt_id=$1 FOR UPDATE`, in.AttemptID).Scan(&state, &req, &plan, &pool, &catalog, &fence, &snap, &marker)
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
	_, err = tx.Exec(ctx, `INSERT INTO ducklake.generation_binding
(delivery_id,generation_id,attempt_id,physical_pool_id,catalog_id,snapshot_id,relation_manifest_digest,compatibility_digest,serving_artifact_digest,request_digest,plan_digest,fencing_epoch)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (delivery_id,generation_id) DO NOTHING`, in.DeliveryID, in.GenerationID, in.AttemptID, in.PhysicalPoolID, in.CatalogID, in.SnapshotID, in.RelationManifestDigest, in.CompatibilityDigest, in.ServingArtifactDigest, in.RequestDigest, in.PlanDigest, in.FencingEpoch)
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
	if in.Kind != RootCandidate && in.Kind != RootGeneration && in.Kind != RootRollback && in.Kind != RootRecovery {
		return ErrInvalid
	}
	created := in.CreatedAt.UTC()
	if created.IsZero() {
		created = time.Now().UTC()
	}
	if err := requireSnapshotLive(ctx, tx, SnapshotRef{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO ducklake.snapshot_root (root_id,physical_pool_id,catalog_id,snapshot_id,root_kind,state,created_at)
VALUES ($1,$2,$3,$4,$5,'live',$6) ON CONFLICT (root_id) DO NOTHING`, in.RootID, in.PhysicalPoolID, in.CatalogID, in.SnapshotID, string(in.Kind), created)
	if err != nil {
		return err
	}
	var pool, catalog, kind, state string
	var snapshot int64
	if err := tx.QueryRow(ctx, `SELECT physical_pool_id,catalog_id,snapshot_id,root_kind,state FROM ducklake.snapshot_root WHERE root_id=$1`, in.RootID).Scan(&pool, &catalog, &snapshot, &kind, &state); err != nil {
		return err
	}
	if pool != in.PhysicalPoolID || catalog != in.CatalogID || snapshot != in.SnapshotID || kind != string(in.Kind) || state != "live" {
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
	result, err := tx.Exec(ctx, `UPDATE ducklake.snapshot_root SET state='expired',expired_at=$2 WHERE root_id=$1 AND state IN ('live','retiring')`, rootID, releasedAt.UTC())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_root WHERE root_id=$1`, rootID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
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
	var got GenerationBinding
	err := db.QueryRow(ctx, `SELECT delivery_id,generation_id,attempt_id::text,physical_pool_id,catalog_id,snapshot_id,relation_manifest_digest,compatibility_digest,serving_artifact_digest,request_digest,plan_digest,fencing_epoch,bound_at
FROM ducklake.generation_binding WHERE delivery_id=$1 AND generation_id=$2`, deliveryID, generationID).Scan(&got.DeliveryID, &got.GenerationID, &got.AttemptID, &got.PhysicalPoolID, &got.CatalogID, &got.SnapshotID, &got.RelationManifestDigest, &got.CompatibilityDigest, &got.ServingArtifactDigest, &got.RequestDigest, &got.PlanDigest, &got.FencingEpoch, &got.BoundAt)
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
	return beginAttemptAt(ctx, tx, in, time.Now().UTC())
}

func beginAttemptAt(ctx context.Context, tx DBTX, in BeginAttemptInput, now time.Time) (AttemptEvidence, error) {
	if tx == nil {
		return AttemptEvidence{}, ErrInvalid
	}
	if err := validateBeginAt(in, now); err != nil {
		return AttemptEvidence{}, err
	}
	in.LeaseExpiresAt = in.LeaseExpiresAt.UTC().Truncate(time.Microsecond)
	leaseExpires := in.LeaseExpiresAt.UTC()
	_, err := tx.Exec(ctx, `INSERT INTO ducklake.attempt_evidence
(attempt_id,request_digest,plan_digest,physical_pool_id,catalog_id,owner_id,fencing_epoch,lease_expires_at,session_identity,state)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'running') ON CONFLICT (attempt_id) DO NOTHING`, in.AttemptID, in.RequestDigest, in.PlanDigest, in.PhysicalPoolID, in.CatalogID, in.OwnerID, in.FencingEpoch, leaseExpires, in.SessionIdentity)
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
	return beginAttemptAt(ctx, r.db, in, r.clock())
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
	now := in.CommittedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	canonical, _ := marker.CanonicalJSON()
	existing, err := loadAttemptForUpdate(ctx, tx, in.AttemptID)
	if err != nil {
		return AttemptEvidence{}, err
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
	_, err = tx.Exec(ctx, `UPDATE ducklake.attempt_evidence
SET state='committed',snapshot_id=$4,commit_marker=$5::jsonb,updated_at=$6,terminal_at=$6
WHERE attempt_id=$1 AND state='running' AND owner_id=$2 AND fencing_epoch=$3`, in.AttemptID, in.OwnerID, in.FencingEpoch, in.Snapshot.SnapshotID, canonical, now)
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
	return CommitAttempt(ctx, r.db, in)
}

func TerminateAttempt(ctx context.Context, tx DBTX, in TerminateAttemptInput, state AttemptState) (AttemptEvidence, error) {
	if tx == nil || !validUUID(in.AttemptID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || (state != AttemptAborted && state != AttemptIndeterminate && state != AttemptFenced) {
		return AttemptEvidence{}, ErrInvalid
	}
	evidence, err := canonicalEvidence(in.Evidence)
	if err != nil {
		return AttemptEvidence{}, err
	}
	now := in.TerminatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err = tx.Exec(ctx, `UPDATE ducklake.attempt_evidence SET state=$4,termination_evidence=$5::jsonb,updated_at=$6,terminal_at=$6
WHERE attempt_id=$1 AND state='running' AND owner_id=$2 AND fencing_epoch=$3`, in.AttemptID, in.OwnerID, in.FencingEpoch, string(state), evidence, now)
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

func LoadAttempt(ctx context.Context, db DBTX, id string) (AttemptEvidence, error) {
	if db == nil || !validUUID(id) {
		return AttemptEvidence{}, ErrInvalid
	}
	var a AttemptEvidence
	var marker, termination []byte
	var snapshot pgtype.Int8
	var terminal *time.Time
	err := db.QueryRow(ctx, `SELECT attempt_id::text,request_digest,plan_digest,physical_pool_id,catalog_id,owner_id,fencing_epoch,lease_expires_at,session_identity,state,snapshot_id,commit_marker,termination_evidence,created_at,updated_at,terminal_at
		FROM ducklake.attempt_evidence WHERE attempt_id=$1`, id).Scan(&a.AttemptID, &a.RequestDigest, &a.PlanDigest, &a.PhysicalPoolID, &a.CatalogID, &a.OwnerID, &a.FencingEpoch, &a.LeaseExpiresAt, &a.SessionIdentity, &a.State, &snapshot, &marker, &termination, &a.CreatedAt, &a.UpdatedAt, &terminal)
	if errors.Is(err, pgx.ErrNoRows) {
		return AttemptEvidence{}, ErrNotFound
	}
	if err != nil {
		return AttemptEvidence{}, err
	}
	a.CommitMarker = string(marker)
	if snapshot.Valid {
		a.SnapshotID = snapshot.Int64
	}
	a.TerminationEvidence = append(json.RawMessage(nil), termination...)
	if terminal != nil {
		a.TerminalAt = terminal.UTC()
	}
	return a, nil
}

func loadAttemptForUpdate(ctx context.Context, db DBTX, id string) (AttemptEvidence, error) {
	var locked string
	if err := db.QueryRow(ctx, `SELECT attempt_id::text FROM ducklake.attempt_evidence WHERE attempt_id=$1 FOR UPDATE`, id).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
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
	var pool, catalog string
	var snapshot, fence int64
	err := tx.QueryRow(ctx, `SELECT physical_pool_id,catalog_id,snapshot_id,fencing_epoch FROM ducklake.generation_binding WHERE delivery_id=$1 AND generation_id=$2`, in.DeliveryID, in.GenerationID).Scan(&pool, &catalog, &snapshot, &fence)
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	}
	if err != nil {
		return SnapshotLease{}, err
	}
	if pool != in.PhysicalPoolID || catalog != in.CatalogID || snapshot != in.SnapshotID || fence != in.FencingEpoch {
		return SnapshotLease{}, fmt.Errorf("%w: generation binding differs", ErrConflict)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 FOR UPDATE`, in.PhysicalPoolID, in.CatalogID, in.SnapshotID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	} else if err != nil {
		return SnapshotLease{}, err
	} else if state != "live" {
		return SnapshotLease{}, ErrNotLive
	}
	if _, err := tx.Exec(ctx, `UPDATE ducklake.snapshot_retention SET protected_until=GREATEST(COALESCE(protected_until,$4),$4) WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, in.PhysicalPoolID, in.CatalogID, in.SnapshotID, in.ExpiresAt.UTC()); err != nil {
		return SnapshotLease{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ducklake.snapshot_lease (lease_id,delivery_id,generation_id,physical_pool_id,catalog_id,snapshot_id,owner_id,fencing_epoch,state,expires_at,acquired_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active',$9,$10) ON CONFLICT (lease_id) DO NOTHING`, in.LeaseID, in.DeliveryID, in.GenerationID, in.PhysicalPoolID, in.CatalogID, in.SnapshotID, in.OwnerID, in.FencingEpoch, in.ExpiresAt.UTC(), acquired)
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
	return acquireSnapshotLeaseAt(ctx, r.db, in, r.clock())
}

func RenewSnapshotLease(ctx context.Context, tx DBTX, fence LeaseFence, expiresAt, now time.Time) error {
	if tx == nil || !validUUID(fence.LeaseID) || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
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
	result, err := tx.Exec(ctx, `WITH renewed AS (
    UPDATE ducklake.snapshot_lease
       SET expires_at=$4
     WHERE lease_id=$1 AND owner_id=$2 AND fencing_epoch=$3
       AND state='active' AND expires_at > $5
     RETURNING physical_pool_id,catalog_id,snapshot_id
)
UPDATE ducklake.snapshot_retention AS r
   SET protected_until=GREATEST(COALESCE(r.protected_until,$4),$4)
  FROM renewed
 WHERE r.physical_pool_id=renewed.physical_pool_id
   AND r.catalog_id=renewed.catalog_id
   AND r.snapshot_id=renewed.snapshot_id`, fence.LeaseID, fence.OwnerID, fence.FencingEpoch, expiresAt, now)
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
	return RenewSnapshotLease(ctx, r.db, fence, expiresAt, r.clock())
}

func ReleaseSnapshotLease(ctx context.Context, tx DBTX, fence LeaseFence, releasedAt time.Time) error {
	if tx == nil || !validUUID(fence.LeaseID) || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	if releasedAt.IsZero() {
		releasedAt = time.Now().UTC()
	}
	result, err := tx.Exec(ctx, `UPDATE ducklake.snapshot_lease SET state='released',released_at=$4
WHERE lease_id=$1 AND owner_id=$2 AND fencing_epoch=$3 AND state='active'`, fence.LeaseID, fence.OwnerID, fence.FencingEpoch, releasedAt.UTC())
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
	return ReleaseSnapshotLease(ctx, r.db, fence, r.clock())
}

func ExpireSnapshotLeases(ctx context.Context, tx DBTX, now time.Time) error {
	if tx == nil {
		return ErrInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := tx.Exec(ctx, `UPDATE ducklake.snapshot_lease SET state='expired',released_at=$1 WHERE state='active' AND expires_at <= $1`, now.UTC())
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
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 FOR UPDATE`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	} else if state == "expired" {
		return ErrNotLive
	} else if state == "retiring" {
		return nil
	}
	var roots int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ducklake.snapshot_root WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state IN ('live','retiring')`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&roots); err != nil {
		return err
	}
	if roots != 0 {
		return fmt.Errorf("%w: durable snapshot roots remain", ErrConflict)
	}
	_, err := tx.Exec(ctx, `UPDATE ducklake.snapshot_retention SET state='retiring',retired_at=$4 WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state='live'`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID, retiredAt.UTC())
	return err
}

func (r *Repository) RetireSnapshot(ctx context.Context, ref SnapshotRef, retiredAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return RetireSnapshot(ctx, r.db, ref, retiredAt)
}

func ExpireSnapshot(ctx context.Context, tx DBTX, ref SnapshotRef, evidence json.RawMessage, expiredAt time.Time) error {
	if tx == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	if expiredAt.IsZero() {
		expiredAt = time.Now().UTC()
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: expiration evidence is required", ErrInvalid)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 FOR UPDATE`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	} else if state == "expired" {
		return nil
	} else if state != "retiring" {
		return ErrConflict
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ducklake.snapshot_lease WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state='active'`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("%w: active query leases remain", ErrConflict)
	}
	var roots int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ducklake.snapshot_root WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state IN ('live','retiring')`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&roots); err != nil {
		return err
	}
	if roots != 0 {
		return fmt.Errorf("%w: durable snapshot roots remain", ErrConflict)
	}
	_, err = tx.Exec(ctx, `UPDATE ducklake.snapshot_retention SET state='expired',expired_at=$4,evidence=$5::jsonb WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state='retiring'`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID, expiredAt.UTC(), canonical)
	return err
}

func (r *Repository) ExpireSnapshot(ctx context.Context, ref SnapshotRef, evidence json.RawMessage, expiredAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return ExpireSnapshot(ctx, r.db, ref, evidence, expiredAt)
}

func LoadLease(ctx context.Context, db DBTX, id string) (SnapshotLease, error) {
	if db == nil || !validUUID(id) {
		return SnapshotLease{}, ErrInvalid
	}
	var l SnapshotLease
	var released *time.Time
	err := db.QueryRow(ctx, `SELECT lease_id::text,delivery_id,generation_id,physical_pool_id,catalog_id,snapshot_id,owner_id,fencing_epoch,state,expires_at,acquired_at,released_at
FROM ducklake.snapshot_lease WHERE lease_id=$1`, id).Scan(&l.LeaseID, &l.DeliveryID, &l.GenerationID, &l.PhysicalPoolID, &l.CatalogID, &l.SnapshotID, &l.OwnerID, &l.FencingEpoch, &l.State, &l.ExpiresAt, &l.AcquiredAt, &released)
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotLease{}, ErrNotFound
	}
	if err != nil {
		return SnapshotLease{}, err
	}
	if released != nil {
		l.ReleasedAt = released.UTC()
	}
	return l, nil
}

func (r *Repository) LoadLease(ctx context.Context, id string) (SnapshotLease, error) {
	if r == nil {
		return SnapshotLease{}, ErrInvalid
	}
	return LoadLease(ctx, r.db, id)
}

func validateCatalog(c CatalogIdentity) error {
	for _, value := range []string{c.PhysicalPoolID, c.CatalogID} {
		if !validID(value) {
			return ErrInvalid
		}
	}
	if !validSchema(c.MetadataSchema) || !validID(c.CatalogSchemaVersion) || !validDigest(c.CompatibilityDigest) {
		return ErrInvalid
	}
	return nil
}

func validSnapshotRef(ref SnapshotRef) bool {
	return validID(ref.PhysicalPoolID) && validID(ref.CatalogID) && ref.SnapshotID > 0
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

func validID(value string) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= maxID && !strings.ContainsRune(value, '\x00')
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

func sameCatalog(a, b CatalogIdentity) bool {
	return a.PhysicalPoolID == b.PhysicalPoolID && a.CatalogID == b.CatalogID && a.MetadataSchema == b.MetadataSchema && a.CompatibilityDigest == b.CompatibilityDigest && a.CatalogSchemaVersion == b.CatalogSchemaVersion
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
	return err == nil && canonical == b
}
