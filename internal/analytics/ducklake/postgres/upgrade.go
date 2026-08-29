package postgres

// PostgreSQL-native DuckLake catalog upgrade authority.  This file is the
// only place that may grant migration authority: normal runtime attachments
// use CheckRuntimeAttachEligibility and never opt into DuckLake automatic
// migration.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func inRepositoryTransaction[T any](ctx context.Context, db DBTX, fn func(DBTX) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	beginner, ok := db.(transactionBeginner)
	if !ok {
		return fn(db)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return zero, err
	}
	value, err := fn(tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	return value, nil
}

func inRepositoryExecTransaction(ctx context.Context, db DBTX, fn func(DBTX) error) error {
	_, err := inRepositoryTransaction(ctx, db, func(tx DBTX) (struct{}, error) {
		return struct{}{}, fn(tx)
	})
	return err
}

const maxMigrationLease = 24 * time.Hour

var (
	ErrMigrationBusy             = errors.New("DuckLake migration authority is busy")
	ErrMigrationFenceExpired     = errors.New("DuckLake migration fence is expired")
	ErrMigrationNotFound         = errors.New("DuckLake catalog migration not found")
	ErrMigrationTerminal         = errors.New("DuckLake catalog migration is terminal")
	ErrRuntimeAttachIneligible   = errors.New("DuckLake runtime attach is ineligible")
	ErrQualificationMissing      = errors.New("DuckLake snapshot qualification evidence is missing")
	ErrQualificationRejected     = errors.New("DuckLake snapshot qualification was rejected")
	ErrCompatibilityMismatch     = errors.New("DuckLake runtime compatibility tuple mismatch")
	ErrMigrationEvidenceRequired = errors.New("DuckLake migration evidence is required")
)

type MigrationFenceScope string

const (
	MigrationFenceGlobal MigrationFenceScope = "global"
	MigrationFencePool   MigrationFenceScope = "pool"
)

// RuntimeTuple is the exact execution/storage tuple pinned by a pool.
type RuntimeTuple struct {
	DuckDBRuntime     string
	DuckLakeExtension string
	CatalogFormat     string
}

// RuntimeCompatibility combines the tuple with both compatibility identities
// that are required for a safe attach.
type RuntimeCompatibility struct {
	RuntimeTuple
	CompatibilityDigest  string
	CatalogSchemaVersion string
}

// CatalogRuntimeCompatibility is the immutable/current row stored for a pool.
type CatalogRuntimeCompatibility struct {
	PhysicalPoolID string
	CatalogID      string
	RuntimeCompatibility
	// Registration is a maintenance mutation and therefore must carry the
	// exact active GLOBAL+POOL authority pair. Loaded rows leave these zero.
	GlobalFence        MigrationFence
	PoolFence          MigrationFence
	CurrentMigrationID string
	UpdatedAt          time.Time
}

type MigrationFence struct {
	Scope          MigrationFenceScope
	PhysicalPoolID string
	OwnerID        string
	FencingEpoch   int64
	LeaseExpiresAt time.Time
}

type AcquireMigrationFenceInput struct {
	Scope          MigrationFenceScope
	PhysicalPoolID string
	OwnerID        string
	LeaseExpiresAt time.Time
}

type CatalogMigration struct {
	MigrationID               string
	PhysicalPoolID, CatalogID string
	OwnerID                   string
	FencingEpoch              int64
	GlobalFencingEpoch        int64
	Current                   RuntimeCompatibility
	Target                    RuntimeCompatibility
	State                     string
	StartedAt                 time.Time
	TerminalAt                time.Time
	BeginEvidence             json.RawMessage
	CompletionEvidence        json.RawMessage
	FailureEvidence           json.RawMessage
	RecoveryDecision          string
	DecisionEvidence          json.RawMessage
}

type BeginCatalogMigrationInput struct {
	MigrationID    string
	PhysicalPoolID string
	CatalogID      string
	GlobalFence    MigrationFence
	PoolFence      MigrationFence
	Current        RuntimeCompatibility
	Target         RuntimeCompatibility
	Evidence       json.RawMessage
}

type CompleteCatalogMigrationInput struct {
	MigrationID string
	GlobalFence MigrationFence
	PoolFence   MigrationFence
	Evidence    json.RawMessage
}

type FailCatalogMigrationInput struct {
	MigrationID      string
	GlobalFence      MigrationFence
	PoolFence        MigrationFence
	Evidence         json.RawMessage
	RecoveryDecision string
	DecisionEvidence json.RawMessage
}

type SnapshotQualification struct {
	QualificationID string
	PhysicalPoolID  string
	CatalogID       string
	SnapshotID      int64
	MigrationID     string
	RuntimeCompatibility
	Status      string
	Evidence    json.RawMessage
	QualifiedAt time.Time
}

type RequalifySnapshotInput struct {
	QualificationID string
	PhysicalPoolID  string
	CatalogID       string
	SnapshotID      int64
	MigrationID     string
	GlobalFence     MigrationFence
	PoolFence       MigrationFence
	Compatibility   RuntimeCompatibility
	Evidence        json.RawMessage
	Status          string
}

// RuntimeAttachInput is deliberately explicit. Automatic migration is a
// caller-visible input so a runtime cannot silently get authority by omission.
type RuntimeAttachInput struct {
	PhysicalPoolID     string
	CatalogID          string
	Compatibility      RuntimeCompatibility
	AutomaticMigration bool
}

type RuntimeAttachEligibility struct {
	Eligible bool
	Reason   string
	Current  CatalogRuntimeCompatibility
}

func (t RuntimeTuple) validate() error {
	if !validID(t.DuckDBRuntime) || !validID(t.DuckLakeExtension) || !validID(t.CatalogFormat) {
		return ErrInvalid
	}
	return nil
}

func (c RuntimeCompatibility) validate() error {
	if err := c.RuntimeTuple.validate(); err != nil {
		return err
	}
	if !validDigest(c.CompatibilityDigest) || !validSchemaVersion(c.CatalogSchemaVersion) {
		return ErrInvalid
	}
	return nil
}

func validSchemaVersion(v string) bool {
	return v != "" && v == stringsTrim(v) && len(v) <= maxSchema
}

// stringsTrim avoids importing strings in all call sites and keeps validation
// identical to the existing repository identity checks.
func stringsTrim(v string) string {
	start, end := 0, len(v)
	for start < end && (v[start] == ' ' || v[start] == '\t' || v[start] == '\n' || v[start] == '\r') {
		start++
	}
	for end > start && (v[end-1] == ' ' || v[end-1] == '\t' || v[end-1] == '\n' || v[end-1] == '\r') {
		end--
	}
	return v[start:end]
}

func sameRuntimeCompatibility(a, b RuntimeCompatibility) bool {
	return a.RuntimeTuple == b.RuntimeTuple && a.CompatibilityDigest == b.CompatibilityDigest && a.CatalogSchemaVersion == b.CatalogSchemaVersion
}

// resolveUpgradeFences accepts the explicit GlobalFence/PoolFence pair;
// lifecycle operations never infer one scope from the other.
func resolveUpgradeFences(global, pool MigrationFence, poolID string) (MigrationFence, MigrationFence, error) {
	if global.Scope != MigrationFenceGlobal || global.PhysicalPoolID != "" || !validID(global.OwnerID) || global.FencingEpoch <= 0 {
		return MigrationFence{}, MigrationFence{}, ErrInvalid
	}
	if pool.Scope != MigrationFencePool || pool.PhysicalPoolID != poolID || !validID(pool.OwnerID) || pool.FencingEpoch <= 0 || pool.OwnerID != global.OwnerID {
		return MigrationFence{}, MigrationFence{}, ErrInvalid
	}
	return global, pool, nil
}

// mapAuthorityError converts errors raised by the SECURITY DEFINER authority
// functions into the repository's stable sentinel errors. PostgreSQL remains
// the final arbiter; this mapping only preserves Go's errors.Is contract.
func mapAuthorityError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "fence busy"):
		return ErrMigrationBusy
	case strings.Contains(message, "fence expired"):
		return ErrMigrationFenceExpired
	case strings.Contains(message, "fence stale"):
		return ErrStaleFence
	case strings.Contains(message, "fence not found"):
		return ErrMigrationNotFound
	case strings.Contains(message, "snapshot qualification missing"):
		return ErrQualificationMissing
	case strings.Contains(message, "runtime compatibility mismatch"):
		return ErrCompatibilityMismatch
	case strings.Contains(message, "qualification conflict"):
		return ErrConflict
	case strings.Contains(message, "migration conflict"):
		return ErrConflict
	case strings.Contains(message, "migration evidence required"):
		return ErrMigrationEvidenceRequired
	case strings.Contains(message, "snapshot not found"):
		return ErrNotFound
	case strings.Contains(message, "catalog migration not found"):
		return ErrMigrationNotFound
	case strings.Contains(message, "catalog migration terminal"):
		return ErrMigrationTerminal
	}
	return err
}

func fenceInput(in AcquireMigrationFenceInput) (MigrationFenceScope, string, error) {
	scope := in.Scope
	if scope != MigrationFenceGlobal && scope != MigrationFencePool {
		return "", "", ErrInvalid
	}
	pool := in.PhysicalPoolID
	if scope == MigrationFenceGlobal {
		if pool != "" {
			return "", "", ErrInvalid
		}
	} else if !validID(pool) {
		return "", "", ErrInvalid
	}
	if !validID(in.OwnerID) {
		return "", "", ErrInvalid
	}
	return scope, pool, nil
}

func normalizeLease(now, requested time.Time) (time.Time, error) {
	if requested.IsZero() {
		requested = now.Add(maxMigrationLease)
	}
	requested = requested.UTC().Truncate(time.Microsecond)
	if !requested.After(now) || requested.After(now.Add(maxMigrationLease)) {
		return time.Time{}, ErrInvalid
	}
	return requested, nil
}

func canonicalBeginEvidence(raw json.RawMessage) (string, error) {
	canonical, err := canonicalEvidence(raw)
	if err != nil {
		return "", fmt.Errorf("%w: begin evidence", ErrMigrationEvidenceRequired)
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(canonical), &object); err != nil {
		return "", fmt.Errorf("%w: begin evidence", ErrMigrationEvidenceRequired)
	}
	verified := func(names ...string) bool {
		for _, name := range names {
			if value, ok := object[name].(bool); ok && value {
				return true
			}
		}
		return false
	}
	if !verified("drain_verified", "drained", "readers_drained") || !verified("backup_verified", "backup_verification", "backup") {
		return "", fmt.Errorf("%w: begin evidence must prove drain and backup verification", ErrMigrationEvidenceRequired)
	}
	return canonical, nil
}

// AcquireMigrationFence atomically claims the global or one pool fence. Pool
// claims lock the global row first, so a global claim and a pool claim cannot
// pass each other without observing the other's active lease.
func AcquireMigrationFence(ctx context.Context, tx DBTX, in AcquireMigrationFenceInput) (MigrationFence, error) {
	if tx == nil {
		return MigrationFence{}, ErrInvalid
	}
	scope, pool, err := fenceInput(in)
	if err != nil {
		return MigrationFence{}, err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return MigrationFence{}, err
	}
	lease, err := normalizeLease(now, in.LeaseExpiresAt)
	if err != nil {
		return MigrationFence{}, err
	}
	var gotScope string
	var got MigrationFence
	err = tx.QueryRow(ctx, `SELECT scope,physical_pool_id,owner_id,fencing_epoch,lease_expires_at FROM ducklake.acquire_migration_fence($1,$2,$3,$4)`, scope, pool, in.OwnerID, lease).Scan(&gotScope, &got.PhysicalPoolID, &got.OwnerID, &got.FencingEpoch, &got.LeaseExpiresAt)
	if err != nil {
		return MigrationFence{}, mapAuthorityError(err)
	}
	got.Scope = MigrationFenceScope(gotScope)
	return got, nil
}

func (r *Repository) AcquireMigrationFence(ctx context.Context, in AcquireMigrationFenceInput) (MigrationFence, error) {
	if r == nil {
		return MigrationFence{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (MigrationFence, error) { return AcquireMigrationFence(ctx, tx, in) })
}

func ReleaseMigrationFence(ctx context.Context, tx DBTX, fence MigrationFence) error {
	if tx == nil || (fence.Scope != MigrationFenceGlobal && fence.Scope != MigrationFencePool) || (fence.Scope == MigrationFenceGlobal && fence.PhysicalPoolID != "") || (fence.Scope == MigrationFencePool && !validID(fence.PhysicalPoolID)) || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT ducklake.release_migration_fence($1,$2,$3,$4)`, fence.Scope, fence.PhysicalPoolID, fence.OwnerID, fence.FencingEpoch); err != nil {
		return mapAuthorityError(err)
	}
	return nil
}

func (r *Repository) ReleaseMigrationFence(ctx context.Context, fence MigrationFence) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error { return ReleaseMigrationFence(ctx, tx, fence) })
}

// RenewMigrationFence extends one bounded lease only for the exact owner and
// epoch. A successor can never renew a predecessor's lease, and every time is
// compared with PostgreSQL's clock.
func RenewMigrationFence(ctx context.Context, tx DBTX, fence MigrationFence, expiresAt time.Time) error {
	if tx == nil || (fence.Scope != MigrationFenceGlobal && fence.Scope != MigrationFencePool) || (fence.Scope == MigrationFenceGlobal && fence.PhysicalPoolID != "") || (fence.Scope == MigrationFencePool && !validID(fence.PhysicalPoolID)) || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return ErrInvalid
	}
	if _, err := tx.Exec(ctx, `SELECT ducklake.renew_migration_fence($1,$2,$3,$4,$5)`, fence.Scope, fence.PhysicalPoolID, fence.OwnerID, fence.FencingEpoch, expiresAt); err != nil {
		return mapAuthorityError(err)
	}
	return nil
}

func (r *Repository) RenewMigrationFence(ctx context.Context, fence MigrationFence, expiresAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error { return RenewMigrationFence(ctx, tx, fence, expiresAt) })
}

func RenewUpgradeFences(ctx context.Context, tx DBTX, global, pool MigrationFence, expiresAt time.Time) error {
	if global.OwnerID != pool.OwnerID {
		return ErrInvalid
	}
	if _, err := ensureGlobalMigrationFence(ctx, tx, global); err != nil {
		return err
	}
	if err := RenewMigrationFence(ctx, tx, global, expiresAt); err != nil {
		return err
	}
	return RenewMigrationFence(ctx, tx, pool, expiresAt)
}

func (r *Repository) RenewUpgradeFences(ctx context.Context, global, pool MigrationFence, expiresAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error { return RenewUpgradeFences(ctx, tx, global, pool, expiresAt) })
}

func ensureMigrationFence(ctx context.Context, tx DBTX, fence MigrationFence, poolID string) (time.Time, error) {
	if fence.Scope != MigrationFencePool || fence.PhysicalPoolID != poolID || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return time.Time{}, ErrInvalid
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	var owner *string
	var epoch int64
	var expiry *time.Time
	if err := tx.QueryRow(ctx, `SELECT owner_id,fencing_epoch,lease_expires_at FROM ducklake.migration_fence WHERE scope='pool' AND physical_pool_id=$1`, poolID).Scan(&owner, &epoch, &expiry); errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrMigrationNotFound
	} else if err != nil {
		return time.Time{}, err
	}
	if owner == nil || *owner != fence.OwnerID || epoch != fence.FencingEpoch {
		return time.Time{}, ErrStaleFence
	}
	if expiry == nil || !expiry.After(now) {
		return time.Time{}, ErrMigrationFenceExpired
	}
	return now, nil
}

func ensureGlobalMigrationFence(ctx context.Context, tx DBTX, fence MigrationFence) (time.Time, error) {
	if fence.Scope != MigrationFenceGlobal || fence.PhysicalPoolID != "" || !validID(fence.OwnerID) || fence.FencingEpoch <= 0 {
		return time.Time{}, ErrInvalid
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	var owner *string
	var epoch int64
	var expiry *time.Time
	if err := tx.QueryRow(ctx, `SELECT owner_id,fencing_epoch,lease_expires_at FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id=''`).Scan(&owner, &epoch, &expiry); errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrMigrationNotFound
	} else if err != nil {
		return time.Time{}, err
	}
	if owner == nil || *owner != fence.OwnerID || epoch != fence.FencingEpoch {
		return time.Time{}, ErrStaleFence
	}
	if expiry == nil || !expiry.After(now) {
		return time.Time{}, ErrMigrationFenceExpired
	}
	return now, nil
}

func ensureUpgradeFences(ctx context.Context, tx DBTX, global, pool MigrationFence, poolID string) (time.Time, error) {
	if global.OwnerID != pool.OwnerID {
		return time.Time{}, ErrInvalid
	}
	_, err := ensureGlobalMigrationFence(ctx, tx, global)
	if err != nil {
		return time.Time{}, err
	}
	return ensureMigrationFence(ctx, tx, pool, poolID)
}

func RegisterCatalogRuntimeCompatibility(ctx context.Context, tx DBTX, in CatalogRuntimeCompatibility) (CatalogRuntimeCompatibility, error) {
	if tx == nil || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) {
		return CatalogRuntimeCompatibility{}, ErrInvalid
	}
	if err := in.RuntimeCompatibility.validate(); err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	globalFence, poolFence, err := resolveUpgradeFences(in.GlobalFence, in.PoolFence, in.PhysicalPoolID)
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if _, err := ensureUpgradeFences(ctx, tx, globalFence, poolFence, in.PhysicalPoolID); err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT ducklake.register_catalog_runtime_compatibility($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, in.PhysicalPoolID, in.CatalogID, in.DuckDBRuntime, in.DuckLakeExtension, in.CatalogFormat, in.CompatibilityDigest, in.CatalogSchemaVersion, poolFence.OwnerID, poolFence.FencingEpoch, globalFence.FencingEpoch); err != nil {
		return CatalogRuntimeCompatibility{}, mapAuthorityError(err)
	}
	got, err := LoadCatalogRuntimeCompatibility(ctx, tx, in.PhysicalPoolID)
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if got.CatalogID != in.CatalogID || !sameRuntimeCompatibility(got.RuntimeCompatibility, in.RuntimeCompatibility) {
		return CatalogRuntimeCompatibility{}, fmt.Errorf("%w: runtime compatibility", ErrConflict)
	}
	return got, nil
}

func (r *Repository) RegisterCatalogRuntimeCompatibility(ctx context.Context, in CatalogRuntimeCompatibility) (CatalogRuntimeCompatibility, error) {
	if r == nil {
		return CatalogRuntimeCompatibility{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (CatalogRuntimeCompatibility, error) {
		return RegisterCatalogRuntimeCompatibility(ctx, tx, in)
	})
}

func LoadCatalogRuntimeCompatibility(ctx context.Context, db DBTX, poolID string) (CatalogRuntimeCompatibility, error) {
	if db == nil || !validID(poolID) {
		return CatalogRuntimeCompatibility{}, ErrInvalid
	}
	var got CatalogRuntimeCompatibility
	var currentMigrationID *string
	err := db.QueryRow(ctx, `SELECT physical_pool_id,catalog_id,duckdb_runtime,ducklake_extension,catalog_format,compatibility_digest,catalog_schema_version,current_migration_id::text,updated_at FROM ducklake.catalog_runtime_compatibility WHERE physical_pool_id=$1`, poolID).Scan(&got.PhysicalPoolID, &got.CatalogID, &got.DuckDBRuntime, &got.DuckLakeExtension, &got.CatalogFormat, &got.CompatibilityDigest, &got.CatalogSchemaVersion, &currentMigrationID, &got.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogRuntimeCompatibility{}, ErrNotFound
	}
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
	}
	if currentMigrationID != nil {
		got.CurrentMigrationID = *currentMigrationID
	}
	return got, nil
}

func (r *Repository) LoadCatalogRuntimeCompatibility(ctx context.Context, poolID string) (CatalogRuntimeCompatibility, error) {
	if r == nil {
		return CatalogRuntimeCompatibility{}, ErrInvalid
	}
	return LoadCatalogRuntimeCompatibility(ctx, r.db, poolID)
}

func BeginCatalogMigration(ctx context.Context, tx DBTX, in BeginCatalogMigrationInput) (CatalogMigration, error) {
	if tx == nil || !validUUID(in.MigrationID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) {
		return CatalogMigration{}, ErrInvalid
	}
	globalFence, poolFence, err := resolveUpgradeFences(in.GlobalFence, in.PoolFence, in.PhysicalPoolID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if in.Current.validate() != nil || in.Target.validate() != nil {
		return CatalogMigration{}, ErrInvalid
	}
	beginEvidence, err := canonicalBeginEvidence(in.Evidence)
	if err != nil {
		return CatalogMigration{}, err
	}
	_, err = ensureUpgradeFences(ctx, tx, globalFence, poolFence, in.PhysicalPoolID)
	if err != nil {
		return CatalogMigration{}, err
	}
	// A compatibility row is established once, then every migration must match
	// the exact current tuple persisted by the previous successful migration.
	if _, err := RegisterCatalogRuntimeCompatibility(ctx, tx, CatalogRuntimeCompatibility{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, RuntimeCompatibility: in.Current, GlobalFence: globalFence, PoolFence: poolFence}); err != nil {
		return CatalogMigration{}, err
	}
	current, err := LoadCatalogRuntimeCompatibility(ctx, tx, in.PhysicalPoolID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if current.CatalogID != in.CatalogID || !sameRuntimeCompatibility(current.RuntimeCompatibility, in.Current) {
		return CatalogMigration{}, fmt.Errorf("%w: current runtime tuple", ErrCompatibilityMismatch)
	}
	if _, err := tx.Exec(ctx, `SELECT ducklake.begin_catalog_migration($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb)`, in.MigrationID, in.PhysicalPoolID, in.CatalogID, poolFence.OwnerID, poolFence.FencingEpoch, globalFence.FencingEpoch, in.Current.DuckDBRuntime, in.Current.DuckLakeExtension, in.Current.CatalogFormat, in.Current.CompatibilityDigest, in.Current.CatalogSchemaVersion, in.Target.DuckDBRuntime, in.Target.DuckLakeExtension, in.Target.CatalogFormat, in.Target.CompatibilityDigest, in.Target.CatalogSchemaVersion, beginEvidence); err != nil {
		return CatalogMigration{}, mapAuthorityError(err)
	}
	got, err := LoadCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if got.PhysicalPoolID != in.PhysicalPoolID || got.CatalogID != in.CatalogID || got.OwnerID != poolFence.OwnerID || got.FencingEpoch != poolFence.FencingEpoch || got.GlobalFencingEpoch != globalFence.FencingEpoch || !sameRuntimeCompatibility(got.Current, in.Current) || !sameRuntimeCompatibility(got.Target, in.Target) || !evidenceEqual(got.BeginEvidence, beginEvidence) {
		return CatalogMigration{}, fmt.Errorf("%w: migration identity", ErrConflict)
	}
	return got, nil
}

func (r *Repository) BeginCatalogMigration(ctx context.Context, in BeginCatalogMigrationInput) (CatalogMigration, error) {
	if r == nil {
		return CatalogMigration{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (CatalogMigration, error) { return BeginCatalogMigration(ctx, tx, in) })
}

func CompleteCatalogMigration(ctx context.Context, tx DBTX, in CompleteCatalogMigrationInput) (CatalogMigration, error) {
	if tx == nil || !validUUID(in.MigrationID) {
		return CatalogMigration{}, ErrInvalid
	}
	evidence, err := canonicalEvidence(in.Evidence)
	if err != nil {
		return CatalogMigration{}, fmt.Errorf("%w: %v", ErrMigrationEvidenceRequired, err)
	}
	migration, err := LoadCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return CatalogMigration{}, err
	}
	globalFence, poolFence, err := resolveUpgradeFences(in.GlobalFence, in.PoolFence, migration.PhysicalPoolID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if _, err := ensureUpgradeFences(ctx, tx, globalFence, poolFence, migration.PhysicalPoolID); err != nil {
		return CatalogMigration{}, err
	}
	migration, err = lockCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if migration.FencingEpoch != poolFence.FencingEpoch || migration.GlobalFencingEpoch != globalFence.FencingEpoch {
		return CatalogMigration{}, ErrStaleFence
	}
	if migration.State == "completed" {
		if !evidenceEqual(migration.CompletionEvidence, evidence) {
			return CatalogMigration{}, ErrConflict
		}
		return migration, nil
	}
	if migration.State != "running" {
		return CatalogMigration{}, ErrMigrationTerminal
	}
	if _, err := tx.Exec(ctx, `SELECT ducklake.complete_catalog_migration($1,$2,$3,$4,$5::jsonb)`, in.MigrationID, globalFence.OwnerID, poolFence.FencingEpoch, globalFence.FencingEpoch, evidence); err != nil {
		return CatalogMigration{}, mapAuthorityError(err)
	}
	return LoadCatalogMigration(ctx, tx, in.MigrationID)
}

func (r *Repository) CompleteCatalogMigration(ctx context.Context, in CompleteCatalogMigrationInput) (CatalogMigration, error) {
	if r == nil {
		return CatalogMigration{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (CatalogMigration, error) { return CompleteCatalogMigration(ctx, tx, in) })
}

func FailCatalogMigration(ctx context.Context, tx DBTX, in FailCatalogMigrationInput) (CatalogMigration, error) {
	if tx == nil || !validUUID(in.MigrationID) || (in.RecoveryDecision != "rollback" && in.RecoveryDecision != "forward_recovery") {
		return CatalogMigration{}, ErrInvalid
	}
	failure, err := canonicalEvidence(in.Evidence)
	if err != nil {
		return CatalogMigration{}, fmt.Errorf("%w: %v", ErrMigrationEvidenceRequired, err)
	}
	decision, err := canonicalEvidence(in.DecisionEvidence)
	if err != nil {
		return CatalogMigration{}, fmt.Errorf("%w: decision evidence", ErrMigrationEvidenceRequired)
	}
	migration, err := LoadCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return CatalogMigration{}, err
	}
	globalFence, poolFence, err := resolveUpgradeFences(in.GlobalFence, in.PoolFence, migration.PhysicalPoolID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if _, err := ensureUpgradeFences(ctx, tx, globalFence, poolFence, migration.PhysicalPoolID); err != nil {
		return CatalogMigration{}, err
	}
	migration, err = lockCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return CatalogMigration{}, err
	}
	if migration.State == "failed" {
		if !evidenceEqual(migration.FailureEvidence, failure) || migration.RecoveryDecision != in.RecoveryDecision || !evidenceEqual(migration.DecisionEvidence, decision) {
			return CatalogMigration{}, ErrConflict
		}
		return migration, nil
	}
	if migration.State != "running" {
		return CatalogMigration{}, ErrMigrationTerminal
	}
	// A successor fence may terminalize an interrupted migration. The original
	// owner/epochs remain immutable evidence; only the current fence grants the
	// authority to record a failure decision.
	if _, err := tx.Exec(ctx, `SELECT ducklake.fail_catalog_migration($1,$2,$3,$4,$5::jsonb,$6,$7::jsonb)`, in.MigrationID, poolFence.OwnerID, poolFence.FencingEpoch, globalFence.FencingEpoch, failure, in.RecoveryDecision, decision); err != nil {
		return CatalogMigration{}, mapAuthorityError(err)
	}
	return LoadCatalogMigration(ctx, tx, in.MigrationID)
}

func (r *Repository) FailCatalogMigration(ctx context.Context, in FailCatalogMigrationInput) (CatalogMigration, error) {
	if r == nil {
		return CatalogMigration{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (CatalogMigration, error) { return FailCatalogMigration(ctx, tx, in) })
}

func LoadCatalogMigration(ctx context.Context, db DBTX, migrationID string) (CatalogMigration, error) {
	if db == nil || !validUUID(migrationID) {
		return CatalogMigration{}, ErrInvalid
	}
	var m CatalogMigration
	var terminal *time.Time
	var begin, completion, failure, decision []byte
	var recoveryDecision *string
	err := db.QueryRow(ctx, `SELECT migration_id::text,physical_pool_id,catalog_id,owner_id,fencing_epoch,global_fencing_epoch,current_duckdb_runtime,current_ducklake_extension,current_catalog_format,current_compatibility_digest,current_catalog_schema_version,target_duckdb_runtime,target_ducklake_extension,target_catalog_format,target_compatibility_digest,target_catalog_schema_version,state,started_at,terminal_at,begin_evidence,completion_evidence,failure_evidence,recovery_decision,decision_evidence FROM ducklake.catalog_migration WHERE migration_id=$1`, migrationID).Scan(&m.MigrationID, &m.PhysicalPoolID, &m.CatalogID, &m.OwnerID, &m.FencingEpoch, &m.GlobalFencingEpoch, &m.Current.DuckDBRuntime, &m.Current.DuckLakeExtension, &m.Current.CatalogFormat, &m.Current.CompatibilityDigest, &m.Current.CatalogSchemaVersion, &m.Target.DuckDBRuntime, &m.Target.DuckLakeExtension, &m.Target.CatalogFormat, &m.Target.CompatibilityDigest, &m.Target.CatalogSchemaVersion, &m.State, &m.StartedAt, &terminal, &begin, &completion, &failure, &recoveryDecision, &decision)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogMigration{}, ErrMigrationNotFound
	}
	if err != nil {
		return CatalogMigration{}, err
	}
	if terminal != nil {
		m.TerminalAt = terminal.UTC()
	}
	m.BeginEvidence = append(json.RawMessage(nil), begin...)
	m.CompletionEvidence = append(json.RawMessage(nil), completion...)
	m.FailureEvidence = append(json.RawMessage(nil), failure...)
	if recoveryDecision != nil {
		m.RecoveryDecision = *recoveryDecision
	}
	m.DecisionEvidence = append(json.RawMessage(nil), decision...)
	return m, nil
}

func lockCatalogMigration(ctx context.Context, db DBTX, migrationID string) (CatalogMigration, error) {
	if db == nil || !validUUID(migrationID) {
		return CatalogMigration{}, ErrInvalid
	}
	var id string
	if err := db.QueryRow(ctx, `SELECT migration_id::text FROM ducklake.catalog_migration WHERE migration_id=$1`, migrationID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return CatalogMigration{}, ErrMigrationNotFound
	} else if err != nil {
		return CatalogMigration{}, err
	}
	return LoadCatalogMigration(ctx, db, migrationID)
}

func (r *Repository) LoadCatalogMigration(ctx context.Context, migrationID string) (CatalogMigration, error) {
	if r == nil {
		return CatalogMigration{}, ErrInvalid
	}
	return LoadCatalogMigration(ctx, r.db, migrationID)
}

func RequalifySnapshot(ctx context.Context, tx DBTX, in RequalifySnapshotInput) (SnapshotQualification, error) {
	if tx == nil || !validUUID(in.QualificationID) || !validUUID(in.MigrationID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || in.SnapshotID <= 0 {
		return SnapshotQualification{}, ErrInvalid
	}
	if !validID(in.PoolFence.OwnerID) || in.PoolFence.FencingEpoch <= 0 {
		return SnapshotQualification{}, ErrInvalid
	}
	if err := in.Compatibility.validate(); err != nil {
		return SnapshotQualification{}, err
	}
	status := in.Status
	if status == "" {
		status = "qualified"
	}
	if status != "qualified" && status != "rejected" {
		return SnapshotQualification{}, ErrInvalid
	}
	evidence, err := canonicalEvidence(in.Evidence)
	if err != nil {
		return SnapshotQualification{}, err
	}
	migration, err := LoadCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return SnapshotQualification{}, err
	}
	if migration.PhysicalPoolID != in.PhysicalPoolID || migration.CatalogID != in.CatalogID || migration.State != "running" || !sameRuntimeCompatibility(migration.Target, in.Compatibility) {
		return SnapshotQualification{}, fmt.Errorf("%w: qualification target", ErrConflict)
	}
	globalFence, poolFence, err := resolveUpgradeFences(in.GlobalFence, in.PoolFence, in.PhysicalPoolID)
	if err != nil {
		return SnapshotQualification{}, err
	}
	now, err := ensureUpgradeFences(ctx, tx, globalFence, poolFence, in.PhysicalPoolID)
	if err != nil {
		return SnapshotQualification{}, err
	}
	migration, err = lockCatalogMigration(ctx, tx, in.MigrationID)
	if err != nil {
		return SnapshotQualification{}, err
	}
	if migration.State != "running" {
		return SnapshotQualification{}, ErrMigrationTerminal
	}
	if migration.FencingEpoch != poolFence.FencingEpoch || migration.GlobalFencingEpoch != globalFence.FencingEpoch {
		return SnapshotQualification{}, ErrStaleFence
	}
	_, err = tx.Exec(ctx, `SELECT ducklake.record_snapshot_requalification($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,$16)`, in.QualificationID, in.PhysicalPoolID, in.CatalogID, in.SnapshotID, in.MigrationID, in.Compatibility.DuckDBRuntime, in.Compatibility.DuckLakeExtension, in.Compatibility.CatalogFormat, in.Compatibility.CompatibilityDigest, in.Compatibility.CatalogSchemaVersion, status, evidence, now, poolFence.OwnerID, poolFence.FencingEpoch, globalFence.FencingEpoch)
	if err != nil {
		return SnapshotQualification{}, mapAuthorityError(err)
	}
	got, err := LoadSnapshotQualification(ctx, tx, in.QualificationID)
	if err != nil {
		return SnapshotQualification{}, err
	}
	if got.PhysicalPoolID != in.PhysicalPoolID || got.CatalogID != in.CatalogID || got.SnapshotID != in.SnapshotID || got.MigrationID != in.MigrationID || !sameRuntimeCompatibility(got.RuntimeCompatibility, in.Compatibility) || got.Status != status || !evidenceEqual(got.Evidence, evidence) {
		return SnapshotQualification{}, fmt.Errorf("%w: snapshot qualification identity", ErrConflict)
	}
	return got, nil
}

func (r *Repository) RequalifySnapshot(ctx context.Context, in RequalifySnapshotInput) (SnapshotQualification, error) {
	if r == nil {
		return SnapshotQualification{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (SnapshotQualification, error) { return RequalifySnapshot(ctx, tx, in) })
}

func LoadSnapshotQualification(ctx context.Context, db DBTX, qualificationID string) (SnapshotQualification, error) {
	if db == nil || !validUUID(qualificationID) {
		return SnapshotQualification{}, ErrInvalid
	}
	var q SnapshotQualification
	var evidence []byte
	err := db.QueryRow(ctx, `SELECT qualification_id::text,physical_pool_id,catalog_id,snapshot_id,migration_id::text,duckdb_runtime,ducklake_extension,catalog_format,compatibility_digest,catalog_schema_version,status,evidence,qualified_at FROM ducklake.snapshot_requalification WHERE qualification_id=$1`, qualificationID).Scan(&q.QualificationID, &q.PhysicalPoolID, &q.CatalogID, &q.SnapshotID, &q.MigrationID, &q.DuckDBRuntime, &q.DuckLakeExtension, &q.CatalogFormat, &q.CompatibilityDigest, &q.CatalogSchemaVersion, &q.Status, &evidence, &q.QualifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotQualification{}, ErrNotFound
	}
	if err != nil {
		return SnapshotQualification{}, err
	}
	q.Evidence = append(json.RawMessage(nil), evidence...)
	return q, nil
}

func (r *Repository) LoadSnapshotQualification(ctx context.Context, qualificationID string) (SnapshotQualification, error) {
	if r == nil {
		return SnapshotQualification{}, ErrInvalid
	}
	return LoadSnapshotQualification(ctx, r.db, qualificationID)
}

// CheckRuntimeAttachEligibility is the fail-closed gate used immediately
// before a DuckLake ATTACH. It does not execute migration SQL and treats any
// missing evidence, active migration, or active fence as ineligible.
func CheckRuntimeAttachEligibility(ctx context.Context, db DBTX, in RuntimeAttachInput) (RuntimeAttachEligibility, error) {
	out := RuntimeAttachEligibility{}
	if db == nil || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || in.Compatibility.validate() != nil {
		return out, ErrRuntimeAttachIneligible
	}
	if in.AutomaticMigration {
		return out, fmt.Errorf("%w: automatic migration is disabled", ErrRuntimeAttachIneligible)
	}
	current, err := LoadCatalogRuntimeCompatibility(ctx, db, in.PhysicalPoolID)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	out.Current = current
	if current.CatalogID != in.CatalogID || !sameRuntimeCompatibility(current.RuntimeCompatibility, in.Compatibility) {
		return out, fmt.Errorf("%w: current tuple", ErrRuntimeAttachIneligible)
	}
	if !validUUID(current.CurrentMigrationID) {
		return out, fmt.Errorf("%w: no completed qualification epoch", ErrRuntimeAttachIneligible)
	}
	now, err := databaseClock(ctx, db)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	var activeMigration int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ducklake.catalog_migration WHERE physical_pool_id=$1 AND catalog_id=$2 AND state='running'`, in.PhysicalPoolID, in.CatalogID).Scan(&activeMigration); err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	if activeMigration != 0 {
		return out, fmt.Errorf("%w: migration in progress", ErrRuntimeAttachIneligible)
	}
	var activeFence int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ducklake.migration_fence WHERE (scope='global' OR (scope='pool' AND physical_pool_id=$1)) AND owner_id IS NOT NULL AND lease_expires_at > $2`, in.PhysicalPoolID, now).Scan(&activeFence); err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	if activeFence != 0 {
		return out, fmt.Errorf("%w: migration fence held", ErrRuntimeAttachIneligible)
	}
	var missing int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM ducklake.snapshot_retention r WHERE r.physical_pool_id=$1 AND r.catalog_id=$2 AND r.state IN ('live','retiring') AND NOT EXISTS (SELECT 1 FROM ducklake.snapshot_requalification q WHERE q.physical_pool_id=r.physical_pool_id AND q.catalog_id=r.catalog_id AND q.snapshot_id=r.snapshot_id AND q.migration_id=$3 AND q.status='qualified' AND q.compatibility_digest=$4 AND q.catalog_schema_version=$5 AND q.duckdb_runtime=$6 AND q.ducklake_extension=$7 AND q.catalog_format=$8 AND EXISTS (SELECT 1 FROM ducklake.catalog_migration m WHERE m.migration_id=q.migration_id AND m.state='completed' AND m.target_compatibility_digest=$4 AND m.target_catalog_schema_version=$5 AND m.target_duckdb_runtime=$6 AND m.target_ducklake_extension=$7 AND m.target_catalog_format=$8))`, in.PhysicalPoolID, in.CatalogID, current.CurrentMigrationID, in.Compatibility.CompatibilityDigest, in.Compatibility.CatalogSchemaVersion, in.Compatibility.DuckDBRuntime, in.Compatibility.DuckLakeExtension, in.Compatibility.CatalogFormat).Scan(&missing); err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	if missing != 0 {
		return out, fmt.Errorf("%w: %d retained snapshots are not qualified", ErrRuntimeAttachIneligible, missing)
	}
	out.Eligible = true
	out.Reason = "qualified"
	return out, nil
}

func (r *Repository) CheckRuntimeAttachEligibility(ctx context.Context, in RuntimeAttachInput) (RuntimeAttachEligibility, error) {
	if r == nil {
		return RuntimeAttachEligibility{}, ErrRuntimeAttachIneligible
	}
	return CheckRuntimeAttachEligibility(ctx, r.db, in)
}

func sameIDOrZero(value, expected string) bool { return value == "" || value == expected }
