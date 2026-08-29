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

	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func upgradeUUID(value string) pgtype.UUID {
	u, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: u, Valid: true}
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
	row, err := querygen(tx).AcquireMigrationFence(ctx, dbgen.AcquireMigrationFenceParams{Scope: string(scope), PhysicalPoolID: pool, OwnerID: in.OwnerID, LeaseExpiresAt: pgtype.Timestamptz{Time: lease, Valid: true}})
	if err != nil {
		return MigrationFence{}, mapAuthorityError(err)
	}
	got := MigrationFence{Scope: MigrationFenceScope(row.Scope), PhysicalPoolID: row.PhysicalPoolID, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, LeaseExpiresAt: row.LeaseExpiresAt.Time.UTC()}
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
	if err := querygen(tx).ReleaseMigrationFence(ctx, dbgen.ReleaseMigrationFenceParams{Scope: string(fence.Scope), PhysicalPoolID: fence.PhysicalPoolID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch}); err != nil {
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
	if err := querygen(tx).RenewMigrationFence(ctx, dbgen.RenewMigrationFenceParams{Scope: string(fence.Scope), PhysicalPoolID: fence.PhysicalPoolID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, LeaseExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true}}); err != nil {
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
	row, err := querygen(tx).GetPoolMigrationFence(ctx, poolID)
	owner, epoch, expiry := row.OwnerID, row.FencingEpoch, row.LeaseExpiresAt
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrMigrationNotFound
	} else if err != nil {
		return time.Time{}, err
	}
	if owner == nil || *owner != fence.OwnerID || epoch != fence.FencingEpoch {
		return time.Time{}, ErrStaleFence
	}
	if !expiry.Valid || !expiry.Time.After(now) {
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
	row, err := querygen(tx).GetGlobalMigrationFence(ctx)
	owner, epoch, expiry := row.OwnerID, row.FencingEpoch, row.LeaseExpiresAt
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrMigrationNotFound
	} else if err != nil {
		return time.Time{}, err
	}
	if owner == nil || *owner != fence.OwnerID || epoch != fence.FencingEpoch {
		return time.Time{}, ErrStaleFence
	}
	if !expiry.Valid || !expiry.Time.After(now) {
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
	if err := querygen(tx).RegisterCatalogRuntimeCompatibility(ctx, dbgen.RegisterCatalogRuntimeCompatibilityParams{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, DuckdbRuntime: in.DuckDBRuntime, DucklakeExtension: in.DuckLakeExtension, CatalogFormat: in.CatalogFormat, CompatibilityDigest: in.CompatibilityDigest, CatalogSchemaVersion: in.CatalogSchemaVersion, OwnerID: poolFence.OwnerID, PoolFencingEpoch: poolFence.FencingEpoch, GlobalFencingEpoch: globalFence.FencingEpoch}); err != nil {
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
	row, err := querygen(db).GetCatalogRuntimeCompatibility(ctx, poolID)
	got := CatalogRuntimeCompatibility{PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, RuntimeCompatibility: RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: row.DuckdbRuntime, DuckLakeExtension: row.DucklakeExtension, CatalogFormat: row.CatalogFormat}, CompatibilityDigest: row.CompatibilityDigest, CatalogSchemaVersion: row.CatalogSchemaVersion}, CurrentMigrationID: row.CurrentMigrationID, UpdatedAt: tsTime(row.UpdatedAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogRuntimeCompatibility{}, ErrNotFound
	}
	if err != nil {
		return CatalogRuntimeCompatibility{}, err
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
	if err := querygen(tx).BeginCatalogMigration(ctx, dbgen.BeginCatalogMigrationParams{MigrationID: upgradeUUID(in.MigrationID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: poolFence.OwnerID, PoolFencingEpoch: poolFence.FencingEpoch, GlobalFencingEpoch: globalFence.FencingEpoch, CurrentDuckdbRuntime: in.Current.DuckDBRuntime, CurrentDucklakeExtension: in.Current.DuckLakeExtension, CurrentCatalogFormat: in.Current.CatalogFormat, CurrentCompatibilityDigest: in.Current.CompatibilityDigest, CurrentCatalogSchemaVersion: in.Current.CatalogSchemaVersion, TargetDuckdbRuntime: in.Target.DuckDBRuntime, TargetDucklakeExtension: in.Target.DuckLakeExtension, TargetCatalogFormat: in.Target.CatalogFormat, TargetCompatibilityDigest: in.Target.CompatibilityDigest, TargetCatalogSchemaVersion: in.Target.CatalogSchemaVersion, BeginEvidence: []byte(beginEvidence)}); err != nil {
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
	if err := querygen(tx).CompleteCatalogMigration(ctx, dbgen.CompleteCatalogMigrationParams{MigrationID: upgradeUUID(in.MigrationID), OwnerID: globalFence.OwnerID, PoolFencingEpoch: poolFence.FencingEpoch, GlobalFencingEpoch: globalFence.FencingEpoch, Evidence: []byte(evidence)}); err != nil {
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
	if err := querygen(tx).FailCatalogMigration(ctx, dbgen.FailCatalogMigrationParams{MigrationID: upgradeUUID(in.MigrationID), OwnerID: poolFence.OwnerID, PoolFencingEpoch: poolFence.FencingEpoch, GlobalFencingEpoch: globalFence.FencingEpoch, FailureEvidence: []byte(failure), RecoveryDecision: in.RecoveryDecision, DecisionEvidence: []byte(decision)}); err != nil {
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
	row, err := querygen(db).GetCatalogMigration(ctx, upgradeUUID(migrationID))
	m := CatalogMigration{MigrationID: row.MigrationID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, GlobalFencingEpoch: row.GlobalFencingEpoch, Current: RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: row.CurrentDuckdbRuntime, DuckLakeExtension: row.CurrentDucklakeExtension, CatalogFormat: row.CurrentCatalogFormat}, CompatibilityDigest: row.CurrentCompatibilityDigest, CatalogSchemaVersion: row.CurrentCatalogSchemaVersion}, Target: RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: row.TargetDuckdbRuntime, DuckLakeExtension: row.TargetDucklakeExtension, CatalogFormat: row.TargetCatalogFormat}, CompatibilityDigest: row.TargetCompatibilityDigest, CatalogSchemaVersion: row.TargetCatalogSchemaVersion}, State: row.State, StartedAt: tsTime(row.StartedAt), TerminalAt: tsTime(row.TerminalAt), BeginEvidence: append(json.RawMessage(nil), row.BeginEvidence...), CompletionEvidence: append(json.RawMessage(nil), row.CompletionEvidence...), FailureEvidence: append(json.RawMessage(nil), row.FailureEvidence...), DecisionEvidence: append(json.RawMessage(nil), row.DecisionEvidence...)}
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogMigration{}, ErrMigrationNotFound
	}
	if err != nil {
		return CatalogMigration{}, err
	}
	if row.RecoveryDecision != nil {
		m.RecoveryDecision = *row.RecoveryDecision
	}
	return m, nil
}

func lockCatalogMigration(ctx context.Context, db DBTX, migrationID string) (CatalogMigration, error) {
	if db == nil || !validUUID(migrationID) {
		return CatalogMigration{}, ErrInvalid
	}
	if _, err := querygen(db).LockCatalogMigration(ctx, upgradeUUID(migrationID)); errors.Is(err, pgx.ErrNoRows) {
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
	err = querygen(tx).RecordSnapshotRequalification(ctx, dbgen.RecordSnapshotRequalificationParams{QualificationID: upgradeUUID(in.QualificationID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: in.SnapshotID, MigrationID: upgradeUUID(in.MigrationID), DuckdbRuntime: in.Compatibility.DuckDBRuntime, DucklakeExtension: in.Compatibility.DuckLakeExtension, CatalogFormat: in.Compatibility.CatalogFormat, CompatibilityDigest: in.Compatibility.CompatibilityDigest, CatalogSchemaVersion: in.Compatibility.CatalogSchemaVersion, Status: status, Evidence: []byte(evidence), QualifiedAt: pgtype.Timestamptz{Time: now, Valid: true}, OwnerID: poolFence.OwnerID, PoolFencingEpoch: poolFence.FencingEpoch, GlobalFencingEpoch: globalFence.FencingEpoch})
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
	row, err := querygen(db).GetSnapshotRequalification(ctx, upgradeUUID(qualificationID))
	q := SnapshotQualification{QualificationID: row.QualificationID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, MigrationID: row.MigrationID, RuntimeCompatibility: RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: row.DuckdbRuntime, DuckLakeExtension: row.DucklakeExtension, CatalogFormat: row.CatalogFormat}, CompatibilityDigest: row.CompatibilityDigest, CatalogSchemaVersion: row.CatalogSchemaVersion}, Status: row.Status, Evidence: append(json.RawMessage(nil), row.Evidence...), QualifiedAt: tsTime(row.QualifiedAt)}
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotQualification{}, ErrNotFound
	}
	if err != nil {
		return SnapshotQualification{}, err
	}
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
	activeMigration64, err := querygen(db).CountRunningMigrations(ctx, dbgen.CountRunningMigrationsParams{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID})
	activeMigration := int(activeMigration64)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	if activeMigration != 0 {
		return out, fmt.Errorf("%w: migration in progress", ErrRuntimeAttachIneligible)
	}
	activeFence64, err := querygen(db).CountActiveMigrationFences(ctx, dbgen.CountActiveMigrationFencesParams{PhysicalPoolID: in.PhysicalPoolID, LeaseExpiresAt: pgtype.Timestamptz{Time: now, Valid: true}})
	activeFence := int(activeFence64)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrRuntimeAttachIneligible, err)
	}
	if activeFence != 0 {
		return out, fmt.Errorf("%w: migration fence held", ErrRuntimeAttachIneligible)
	}
	missing64, err := querygen(db).CountMissingSnapshotQualifications(ctx, dbgen.CountMissingSnapshotQualificationsParams{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, MigrationID: upgradeUUID(current.CurrentMigrationID), CompatibilityDigest: in.Compatibility.CompatibilityDigest, CatalogSchemaVersion: in.Compatibility.CatalogSchemaVersion, DuckdbRuntime: in.Compatibility.DuckDBRuntime, DucklakeExtension: in.Compatibility.DuckLakeExtension, CatalogFormat: in.Compatibility.CatalogFormat})
	missing := int(missing64)
	if err != nil {
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
