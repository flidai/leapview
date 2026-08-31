package postgres

// Catalog-wide DuckLake retention coordination.
//
// Snapshot expiry is intentionally separate from catalog migration.  A
// persisted pool/catalog fence serializes workers, while the operation and
// per-snapshot rows retain phase evidence so a successor can resume after a
// process or connection crash.  The coordinator never derives an expiry set
// from age: it asks the control ledger for explicit retiring/expired rows
// whose durable roots and active query leases are both absent.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxRetentionMaintenanceLease = 24 * time.Hour

var (
	ErrRetentionMaintenanceBusy       = errors.New("DuckLake retention maintenance authority is busy")
	ErrRetentionMaintenanceFenceStale = errors.New("DuckLake retention maintenance fence is stale")
	ErrRetentionMaintenanceExpired    = errors.New("DuckLake retention maintenance fence is expired")
	ErrRetentionMaintenanceNotFound   = errors.New("DuckLake retention maintenance operation not found")
)

type RetentionMaintenanceFence struct {
	PhysicalPoolID string
	CatalogID      string
	OwnerID        string
	FencingEpoch   int64
	LeaseExpiresAt time.Time
}

type AcquireRetentionMaintenanceFenceInput struct {
	PhysicalPoolID string
	CatalogID      string
	OwnerID        string
	LeaseExpiresAt time.Time
}

type RetentionMaintenance struct {
	MaintenanceID     string
	PhysicalPoolID    string
	CatalogID         string
	OwnerID           string
	FencingEpoch      int64
	State             string
	Phase             string
	DryRun            bool
	FileGraceMicros   int64
	SnapshotSetDigest string
	PhaseEvidence     json.RawMessage
	StartedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       time.Time
}

type RetentionMaintenanceSnapshot struct {
	MaintenanceID      string
	PhysicalPoolID     string
	CatalogID          string
	SnapshotID         int64
	Phase              string
	ExpiryEvidence     json.RawMessage
	QuarantineEvidence json.RawMessage
	CleanupEvidence    json.RawMessage
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func retentionFenceLease(now, requested time.Time) (time.Time, error) {
	if requested.IsZero() {
		requested = now.Add(maxRetentionMaintenanceLease)
	}
	requested = requested.UTC().Truncate(time.Microsecond)
	if !requested.After(now) || requested.After(now.Add(maxRetentionMaintenanceLease)) {
		return time.Time{}, ErrInvalid
	}
	return requested, nil
}

func validateRetentionFenceInput(in AcquireRetentionMaintenanceFenceInput) error {
	if !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) {
		return ErrInvalid
	}
	return nil
}

func AcquireRetentionMaintenanceFence(ctx context.Context, tx DBTX, in AcquireRetentionMaintenanceFenceInput) (RetentionMaintenanceFence, error) {
	if tx == nil {
		return RetentionMaintenanceFence{}, ErrInvalid
	}
	if err := validateRetentionFenceInput(in); err != nil {
		return RetentionMaintenanceFence{}, err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return RetentionMaintenanceFence{}, err
	}
	lease, err := retentionFenceLease(now, in.LeaseExpiresAt)
	if err != nil {
		return RetentionMaintenanceFence{}, err
	}
	row, err := querygen(tx).AcquirePoolMaintenanceFence(ctx, dbgen.AcquirePoolMaintenanceFenceParams{
		PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID,
		LeaseExpiresAt: pgtype.Timestamptz{Time: lease, Valid: true},
	})
	if err != nil {
		return RetentionMaintenanceFence{}, mapRetentionAuthorityError(err)
	}
	if row.FencingEpoch <= 0 || row.OwnerID != in.OwnerID {
		return RetentionMaintenanceFence{}, ErrRetentionMaintenanceFenceStale
	}
	return RetentionMaintenanceFence{PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, LeaseExpiresAt: row.LeaseExpiresAt.Time.UTC()}, nil
}

func (r *Repository) AcquireRetentionMaintenanceFence(ctx context.Context, in AcquireRetentionMaintenanceFenceInput) (RetentionMaintenanceFence, error) {
	if r == nil {
		return RetentionMaintenanceFence{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (RetentionMaintenanceFence, error) {
		return AcquireRetentionMaintenanceFence(ctx, tx, in)
	})
}

func validateRetentionFence(f RetentionMaintenanceFence) error {
	if !validID(f.PhysicalPoolID) || !validID(f.CatalogID) || !validID(f.OwnerID) || f.FencingEpoch <= 0 {
		return ErrInvalid
	}
	return nil
}

func ReleaseRetentionMaintenanceFence(ctx context.Context, tx DBTX, fence RetentionMaintenanceFence) error {
	if tx == nil {
		return ErrInvalid
	}
	if err := validateRetentionFence(fence); err != nil {
		return err
	}
	if err := querygen(tx).ReleasePoolMaintenanceFence(ctx, dbgen.ReleasePoolMaintenanceFenceParams{
		PhysicalPoolID: fence.PhysicalPoolID, CatalogID: fence.CatalogID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch,
	}); err != nil {
		return mapRetentionAuthorityError(err)
	}
	return nil
}

func (r *Repository) ReleaseRetentionMaintenanceFence(ctx context.Context, fence RetentionMaintenanceFence) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error { return ReleaseRetentionMaintenanceFence(ctx, tx, fence) })
}

func RenewRetentionMaintenanceFence(ctx context.Context, tx DBTX, fence RetentionMaintenanceFence, expiresAt time.Time) error {
	if tx == nil {
		return ErrInvalid
	}
	if err := validateRetentionFence(fence); err != nil {
		return err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	lease, err := retentionFenceLease(now, expiresAt)
	if err != nil {
		return err
	}
	if err := querygen(tx).RenewPoolMaintenanceFence(ctx, dbgen.RenewPoolMaintenanceFenceParams{
		PhysicalPoolID: fence.PhysicalPoolID, CatalogID: fence.CatalogID, OwnerID: fence.OwnerID,
		FencingEpoch: fence.FencingEpoch, LeaseExpiresAt: pgtype.Timestamptz{Time: lease, Valid: true},
	}); err != nil {
		return mapRetentionAuthorityError(err)
	}
	return nil
}

func (r *Repository) RenewRetentionMaintenanceFence(ctx context.Context, fence RetentionMaintenanceFence, expiresAt time.Time) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error { return RenewRetentionMaintenanceFence(ctx, tx, fence, expiresAt) })
}

// RenewRetentionMaintenanceFenceFor derives the lease deadline from
// PostgreSQL's clock, avoiding app-clock skew in authority decisions.
func RenewRetentionMaintenanceFenceFor(ctx context.Context, tx DBTX, fence RetentionMaintenanceFence, duration time.Duration) error {
	if tx == nil || duration <= 0 || duration > maxRetentionMaintenanceLease {
		return ErrInvalid
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	return RenewRetentionMaintenanceFence(ctx, tx, fence, now.Add(duration))
}

func (r *Repository) RenewRetentionMaintenanceFenceFor(ctx context.Context, fence RetentionMaintenanceFence, duration time.Duration) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error {
		return RenewRetentionMaintenanceFenceFor(ctx, tx, fence, duration)
	})
}

// CheckRetentionMaintenanceFence re-reads the persisted owner/epoch and
// compares expiry against PostgreSQL's clock.  It is used immediately before
// every native destructive phase (renewal alone is not sufficient evidence).
func CheckRetentionMaintenanceFence(ctx context.Context, tx DBTX, fence RetentionMaintenanceFence) error {
	if tx == nil {
		return ErrInvalid
	}
	if err := validateRetentionFence(fence); err != nil {
		return err
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return err
	}
	row, err := querygen(tx).GetPoolMaintenanceFence(ctx, dbgen.GetPoolMaintenanceFenceParams{PhysicalPoolID: fence.PhysicalPoolID, CatalogID: fence.CatalogID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRetentionMaintenanceNotFound
	}
	if err != nil {
		return err
	}
	if row.OwnerID == nil || *row.OwnerID != fence.OwnerID || row.FencingEpoch != fence.FencingEpoch {
		return ErrRetentionMaintenanceFenceStale
	}
	if !row.LeaseExpiresAt.Valid || !row.LeaseExpiresAt.Time.After(now) {
		return ErrRetentionMaintenanceExpired
	}
	return nil
}

func (r *Repository) CheckRetentionMaintenanceFence(ctx context.Context, fence RetentionMaintenanceFence) error {
	if r == nil {
		return ErrInvalid
	}
	return CheckRetentionMaintenanceFence(ctx, r.db, fence)
}

func mapRetentionAuthorityError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "maintenance fence busy"):
		return ErrRetentionMaintenanceBusy
	case strings.Contains(message, "maintenance fence expired"):
		return ErrRetentionMaintenanceExpired
	case strings.Contains(message, "maintenance fence stale"):
		return ErrRetentionMaintenanceFenceStale
	case strings.Contains(message, "maintenance fence not found"):
		return ErrRetentionMaintenanceNotFound
	case strings.Contains(message, "catalog identity not found"):
		return ErrNotFound
	default:
		return err
	}
}

func canonicalMaintenancePhaseEvidence(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
		return json.RawMessage("{}"), nil
	}
	canonical, err := canonicalOptionalEvidence(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: maintenance phase evidence", ErrInvalid)
	}
	return json.RawMessage(canonical), nil
}

func loadRetentionMaintenance(ctx context.Context, db DBTX, id string) (RetentionMaintenance, error) {
	if db == nil || !validUUID(id) {
		return RetentionMaintenance{}, ErrInvalid
	}
	row, err := querygen(db).GetRetentionMaintenance(ctx, upgradeUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RetentionMaintenance{}, ErrRetentionMaintenanceNotFound
	}
	if err != nil {
		return RetentionMaintenance{}, err
	}
	return RetentionMaintenance{MaintenanceID: row.MaintenanceID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, State: row.State, Phase: row.Phase, DryRun: row.DryRun, FileGraceMicros: row.FileGraceMicros, SnapshotSetDigest: row.SnapshotSetDigest, PhaseEvidence: append(json.RawMessage(nil), row.PhaseEvidence...), StartedAt: row.StartedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), CompletedAt: row.CompletedAt.Time.UTC()}, nil
}

// StartRetentionMaintenance creates or exactly replays an operation.  A
// successor worker may claim a running operation with a newer pool fence; its
// existing phase is preserved for crash recovery.
func StartRetentionMaintenance(ctx context.Context, tx DBTX, in RetentionMaintenance) (RetentionMaintenance, error) {
	if tx == nil || !validUUID(in.MaintenanceID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || in.State != "running" || in.FileGraceMicros <= 0 {
		return RetentionMaintenance{}, ErrInvalid
	}
	evidence, err := canonicalMaintenancePhaseEvidence(in.PhaseEvidence)
	if err != nil {
		return RetentionMaintenance{}, err
	}
	if err := querygen(tx).InsertRetentionMaintenance(ctx, dbgen.InsertRetentionMaintenanceParams{MaintenanceID: upgradeUUID(in.MaintenanceID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, DryRun: in.DryRun, FileGraceMicros: in.FileGraceMicros, SnapshotSetDigest: in.SnapshotSetDigest, PhaseEvidence: []byte(evidence)}); err != nil {
		return RetentionMaintenance{}, err
	}
	got, err := loadRetentionMaintenance(ctx, tx, in.MaintenanceID)
	if err != nil {
		return RetentionMaintenance{}, err
	}
	if got.PhysicalPoolID != in.PhysicalPoolID || got.CatalogID != in.CatalogID || got.DryRun != in.DryRun || got.FileGraceMicros != in.FileGraceMicros || (got.SnapshotSetDigest != "" && in.SnapshotSetDigest != "" && got.SnapshotSetDigest != in.SnapshotSetDigest) {
		return RetentionMaintenance{}, ErrConflict
	}
	if got.State == "completed" {
		return got, nil
	}
	if got.State != "running" {
		return RetentionMaintenance{}, ErrConflict
	}
	// Update the active owner/epoch only; preserve phase and evidence from a
	// prior attempt so replay never rewinds destructive progress.
	_, err = querygen(tx).UpdateRetentionMaintenance(ctx, dbgen.UpdateRetentionMaintenanceParams{
		OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, State: got.State, Phase: got.Phase, DryRun: got.DryRun, FileGraceMicros: got.FileGraceMicros, SnapshotSetDigest: got.SnapshotSetDigest,
		PhaseEvidence: []byte(got.PhaseEvidence), CompletedAt: pgtype.Timestamptz{}, MaintenanceID: upgradeUUID(in.MaintenanceID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID,
	})
	if err != nil {
		return RetentionMaintenance{}, err
	}
	return loadRetentionMaintenance(ctx, tx, in.MaintenanceID)
}

func (r *Repository) StartRetentionMaintenance(ctx context.Context, in RetentionMaintenance) (RetentionMaintenance, error) {
	if r == nil {
		return RetentionMaintenance{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (RetentionMaintenance, error) { return StartRetentionMaintenance(ctx, tx, in) })
}

func UpdateRetentionMaintenancePhase(ctx context.Context, tx DBTX, in RetentionMaintenance) error {
	if tx == nil || !validUUID(in.MaintenanceID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || in.FileGraceMicros <= 0 {
		return ErrInvalid
	}
	evidence, err := canonicalMaintenancePhaseEvidence(in.PhaseEvidence)
	if err != nil {
		return err
	}
	completed := pgtype.Timestamptz{}
	if !in.CompletedAt.IsZero() {
		completed = pgtype.Timestamptz{Time: in.CompletedAt.UTC(), Valid: true}
	}
	result, err := querygen(tx).UpdateRetentionMaintenance(ctx, dbgen.UpdateRetentionMaintenanceParams{OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, State: in.State, Phase: in.Phase, DryRun: in.DryRun, FileGraceMicros: in.FileGraceMicros, SnapshotSetDigest: in.SnapshotSetDigest, PhaseEvidence: []byte(evidence), CompletedAt: completed, MaintenanceID: upgradeUUID(in.MaintenanceID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID})
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrRetentionMaintenanceFenceStale
	}
	return nil
}

func (r *Repository) UpdateRetentionMaintenancePhase(ctx context.Context, in RetentionMaintenance) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error { return UpdateRetentionMaintenancePhase(ctx, tx, in) })
}

func ListExpiryEligibleSnapshots(ctx context.Context, db DBTX, physicalPoolID, catalogID string) ([]SnapshotRef, error) {
	if db == nil || !validID(physicalPoolID) || !validID(catalogID) {
		return nil, ErrInvalid
	}
	rows, err := querygen(db).ListExpiryEligibleSnapshots(ctx, dbgen.ListExpiryEligibleSnapshotsParams{PhysicalPoolID: physicalPoolID, CatalogID: catalogID})
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, SnapshotRef{PhysicalPoolID: physicalPoolID, CatalogID: catalogID, SnapshotID: row.SnapshotID})
	}
	return out, nil
}

func (r *Repository) ListExpiryEligibleSnapshots(ctx context.Context, physicalPoolID, catalogID string) ([]SnapshotRef, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return ListExpiryEligibleSnapshots(ctx, r.db, physicalPoolID, catalogID)
}

// prepareRetentionSnapshots locks the eligible retention rows and records the
// exact child set in one control transaction.  Retirement prevents new roots
// or query leases, while the row lock closes the enumeration/claim gap before
// any external DuckLake statement is issued.
func prepareRetentionSnapshots(ctx context.Context, db DBTX, operation RetentionMaintenance) ([]SnapshotRef, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	return inRepositoryTransaction(ctx, db, func(tx DBTX) ([]SnapshotRef, error) {
		var rows []struct {
			SnapshotID int64
			State      string
		}
		var queryErr error
		if operation.DryRun {
			candidates, listErr := querygen(tx).ListRetentionDryRunSnapshots(ctx, dbgen.ListRetentionDryRunSnapshotsParams{PhysicalPoolID: operation.PhysicalPoolID, CatalogID: operation.CatalogID})
			queryErr = listErr
			for _, candidate := range candidates {
				rows = append(rows, struct {
					SnapshotID int64
					State      string
				}{SnapshotID: candidate.SnapshotID, State: candidate.State})
			}
		} else {
			claimed, claimErr := querygen(tx).ClaimRetentionSnapshots(ctx, dbgen.ClaimRetentionSnapshotsParams{
				MaintenanceID: upgradeUUID(operation.MaintenanceID), OwnerID: operation.OwnerID, FencingEpoch: operation.FencingEpoch,
				PhysicalPoolID: operation.PhysicalPoolID, CatalogID: operation.CatalogID,
			})
			queryErr = claimErr
			for _, candidate := range claimed {
				rows = append(rows, struct {
					SnapshotID int64
					State      string
				}{SnapshotID: candidate, State: ""})
			}
		}
		if queryErr != nil {
			return nil, queryErr
		}
		out := make([]SnapshotRef, 0, len(rows))
		for _, row := range rows {
			ref := SnapshotRef{PhysicalPoolID: operation.PhysicalPoolID, CatalogID: operation.CatalogID, SnapshotID: row.SnapshotID}
			if _, err := ensureRetentionMaintenanceSnapshot(ctx, tx, operation.MaintenanceID, ref); err != nil {
				return nil, err
			}
			out = append(out, ref)
		}
		digest := retentionSnapshotSetDigest(out)
		operation.SnapshotSetDigest = digest
		if err := UpdateRetentionMaintenancePhase(ctx, tx, operation); err != nil {
			return nil, err
		}
		return out, nil
	})
}

// startAndPrepareRetentionMaintenance makes operation creation, exact-set
// claiming, child insertion, and digest persistence one control transaction.
// A replay with a non-empty digest never enumerates or adds newly eligible
// snapshots; its durable children are the sole source of truth.
func startAndPrepareRetentionMaintenance(ctx context.Context, db DBTX, in RetentionMaintenance) (RetentionMaintenance, error) {
	return inRepositoryTransaction(ctx, db, func(tx DBTX) (RetentionMaintenance, error) {
		operation, err := StartRetentionMaintenance(ctx, tx, in)
		if err != nil {
			return RetentionMaintenance{}, err
		}
		if operation.State == "completed" || operation.SnapshotSetDigest != "" {
			return operation, nil
		}
		if _, err := prepareRetentionSnapshots(ctx, tx, operation); err != nil {
			return RetentionMaintenance{}, err
		}
		return loadRetentionMaintenance(ctx, tx, operation.MaintenanceID)
	})
}

func ensureRetentionMaintenanceSnapshot(ctx context.Context, tx DBTX, maintenanceID string, ref SnapshotRef) (RetentionMaintenanceSnapshot, error) {
	if tx == nil || !validUUID(maintenanceID) || !validSnapshotRef(ref) {
		return RetentionMaintenanceSnapshot{}, ErrInvalid
	}
	// The owner/epoch are read from the operation row by callers and are bound
	// here so the SQL leaf can prove the active pool fence in the same write.
	maintenance, loadErr := loadRetentionMaintenance(ctx, tx, maintenanceID)
	if loadErr != nil {
		return RetentionMaintenanceSnapshot{}, loadErr
	}
	if err := querygen(tx).InsertRetentionMaintenanceSnapshot(ctx, dbgen.InsertRetentionMaintenanceSnapshotParams{MaintenanceID: upgradeUUID(maintenanceID), PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID, OwnerID: maintenance.OwnerID, FencingEpoch: maintenance.FencingEpoch}); err != nil {
		return RetentionMaintenanceSnapshot{}, err
	}
	row, err := querygen(tx).GetRetentionMaintenanceSnapshot(ctx, dbgen.GetRetentionMaintenanceSnapshotParams{MaintenanceID: upgradeUUID(maintenanceID), PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID})
	if errors.Is(err, pgx.ErrNoRows) {
		return RetentionMaintenanceSnapshot{}, ErrRetentionMaintenanceNotFound
	}
	if err != nil {
		return RetentionMaintenanceSnapshot{}, err
	}
	return RetentionMaintenanceSnapshot{MaintenanceID: row.MaintenanceID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, Phase: row.Phase, ExpiryEvidence: append(json.RawMessage(nil), row.ExpiryEvidence...), QuarantineEvidence: append(json.RawMessage(nil), row.QuarantineEvidence...), CleanupEvidence: append(json.RawMessage(nil), row.CleanupEvidence...), CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()}, nil
}

func ListRetentionMaintenanceSnapshots(ctx context.Context, db DBTX, maintenanceID string) ([]RetentionMaintenanceSnapshot, error) {
	if db == nil || !validUUID(maintenanceID) {
		return nil, ErrInvalid
	}
	rows, err := querygen(db).ListRetentionMaintenanceSnapshots(ctx, upgradeUUID(maintenanceID))
	if err != nil {
		return nil, err
	}
	out := make([]RetentionMaintenanceSnapshot, 0, len(rows))
	for _, row := range rows {
		out = append(out, RetentionMaintenanceSnapshot{MaintenanceID: row.MaintenanceID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, SnapshotID: row.SnapshotID, Phase: row.Phase, ExpiryEvidence: append(json.RawMessage(nil), row.ExpiryEvidence...), QuarantineEvidence: append(json.RawMessage(nil), row.QuarantineEvidence...), CleanupEvidence: append(json.RawMessage(nil), row.CleanupEvidence...), CreatedAt: row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC()})
	}
	return out, nil
}

func (r *Repository) EnsureRetentionMaintenanceSnapshot(ctx context.Context, maintenanceID string, ref SnapshotRef) (RetentionMaintenanceSnapshot, error) {
	if r == nil {
		return RetentionMaintenanceSnapshot{}, ErrInvalid
	}
	return inRepositoryTransaction(ctx, r.db, func(tx DBTX) (RetentionMaintenanceSnapshot, error) {
		return ensureRetentionMaintenanceSnapshot(ctx, tx, maintenanceID, ref)
	})
}

func (r *Repository) ListRetentionMaintenanceSnapshots(ctx context.Context, maintenanceID string) ([]RetentionMaintenanceSnapshot, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return ListRetentionMaintenanceSnapshots(ctx, r.db, maintenanceID)
}

func updateRetentionMaintenanceSnapshot(ctx context.Context, tx DBTX, item RetentionMaintenanceSnapshot, ownerID string, fencingEpoch int64) error {
	if tx == nil || !validUUID(item.MaintenanceID) || !validSnapshotRef(SnapshotRef{item.PhysicalPoolID, item.CatalogID, item.SnapshotID}) {
		return ErrInvalid
	}
	if !validID(ownerID) || fencingEpoch <= 0 {
		return ErrInvalid
	}
	result, err := querygen(tx).UpdateRetentionMaintenanceSnapshot(ctx, dbgen.UpdateRetentionMaintenanceSnapshotParams{Phase: item.Phase, ExpiryEvidence: nullableEvidence(item.ExpiryEvidence), QuarantineEvidence: nullableEvidence(item.QuarantineEvidence), CleanupEvidence: nullableEvidence(item.CleanupEvidence), MaintenanceID: upgradeUUID(item.MaintenanceID), PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID, OwnerID: ownerID, FencingEpoch: fencingEpoch})
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrRetentionMaintenanceFenceStale
	}
	return nil
}

// reconcileAdvancedRetentionEvidence closes the crash window left by older
// coordinators that could advance snapshot_retention without recording the
// matching per-operation child row.  Retention rows are monotonic, so a
// successor can safely project each durable evidence document through the
// child state machine before attempting physical cleanup.  The individual
// updates intentionally use the normal fenced repository path; a crash
// between projections is replayable and cannot skip an evidence requirement.
func (c *RetentionCoordinator) reconcileAdvancedRetentionEvidence(ctx context.Context, item RetentionMaintenanceSnapshot, retention SnapshotRetention, in RetentionMaintenanceRequest, fencingEpoch int64) error {
	if c == nil || c.Control == nil {
		return ErrInvalid
	}
	if in.DryRun || item.Phase == "cleanup-complete" {
		return nil
	}
	advance := func(phase string, expiry, quarantine, cleanup json.RawMessage) error {
		next := item
		next.Phase, next.ExpiryEvidence, next.QuarantineEvidence, next.CleanupEvidence = phase, expiry, quarantine, cleanup
		if err := c.Control.UpdateRetentionMaintenanceSnapshot(ctx, next, in.OwnerID, fencingEpoch); err != nil {
			return err
		}
		item = next
		return nil
	}

	// Expired, quarantined, and cleanup-complete retention rows all carry the
	// immutable expiry evidence.  Project an eligible child to expired first so
	// quarantine/cleanup replay retains the full audit chain.
	if item.Phase == "eligible" && retention.State != RetentionRetiring && retention.State != RetentionExpiring && retention.State != RetentionLive {
		if len(retention.Evidence) == 0 {
			return fmt.Errorf("%w: advanced retention row has no expiry evidence", ErrConflict)
		}
		if err := advance("expired", retention.Evidence, nil, nil); err != nil {
			return err
		}
	}
	if (retention.State == RetentionQuarantined || retention.State == RetentionCleanupComplete) && item.Phase == "expired" {
		if len(retention.QuarantineEvidence) == 0 {
			return fmt.Errorf("%w: quarantined retention row has no quarantine evidence", ErrConflict)
		}
		if err := advance("quarantined", item.ExpiryEvidence, retention.QuarantineEvidence, nil); err != nil {
			return err
		}
	}
	if retention.State == RetentionCleanupComplete && item.Phase == "quarantined" {
		if len(retention.CleanupEvidence) == 0 {
			return fmt.Errorf("%w: cleanup-complete retention row has no cleanup evidence", ErrConflict)
		}
		if err := advance("cleanup-complete", item.ExpiryEvidence, item.QuarantineEvidence, retention.CleanupEvidence); err != nil {
			return err
		}
	}
	return nil
}

func nullableEvidence(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func (r *Repository) UpdateRetentionMaintenanceSnapshot(ctx context.Context, item RetentionMaintenanceSnapshot, ownerID string, fencingEpoch int64) error {
	if r == nil {
		return ErrInvalid
	}
	return inRepositoryExecTransaction(ctx, r.db, func(tx DBTX) error {
		return updateRetentionMaintenanceSnapshot(ctx, tx, item, ownerID, fencingEpoch)
	})
}

// RetentionCatalogSession is the narrow native operation boundary.  A
// production implementation should return one pinned
// ducklake.PostgresCatalogMaintenance session, never a runtime *sql.DB pool.
type RetentionCatalogSession interface {
	ExpireSnapshots(context.Context, []int64, bool) error
	CleanupOldFiles(context.Context, time.Duration, bool) error
	DeleteOrphanedFiles(context.Context, time.Duration, bool) error
	Close() error
}

type retentionCatalogSnapshotVerifier interface {
	VerifySnapshotsExpired(context.Context, []int64) error
}

// RetentionCatalogSessionInput binds the native session to the exact control
// identity and fence that authorized this run. Production factories should
// prefer this form so they cannot accidentally attach a neighboring pool or
// omit the live lease/fence callback from the DuckLake contract.
type RetentionCatalogSessionInput struct {
	Request RetentionMaintenanceRequest
	Fence   RetentionMaintenanceFence
}

type RetentionCatalogSessionFactoryFor func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error)

// PostgresRetentionCatalogSession adapts the dedicated parent-package
// maintenance executor and gives callers a convenient concrete factory.
type PostgresRetentionCatalogSession struct {
	session     *ducklake.PostgresCatalogMaintenanceSession
	maintenance *ducklake.PostgresCatalogMaintenance
}

func (s *PostgresRetentionCatalogSession) ExpireSnapshots(ctx context.Context, ids []int64, dryRun bool) error {
	return s.maintenance.ExpireSnapshots(ctx, ids, dryRun)
}
func (s *PostgresRetentionCatalogSession) VerifySnapshotsExpired(ctx context.Context, ids []int64) error {
	return s.maintenance.VerifySnapshotsExpired(ctx, ids)
}
func (s *PostgresRetentionCatalogSession) CleanupOldFiles(ctx context.Context, grace time.Duration, dryRun bool) error {
	return s.maintenance.CleanupOldFiles(ctx, grace, dryRun)
}
func (s *PostgresRetentionCatalogSession) DeleteOrphanedFiles(ctx context.Context, grace time.Duration, dryRun bool) error {
	return s.maintenance.DeleteOrphanedFiles(ctx, grace, dryRun)
}
func (s *PostgresRetentionCatalogSession) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

// OpenPostgresRetentionCatalogSession opens exactly one dedicated DuckDB
// connection.  The contract's lease/fence callback is checked by the parent
// physical-maintenance executor before each native phase.
func OpenPostgresRetentionCatalogSession(ctx context.Context, config ducklake.PostgresCatalogMaintenanceSessionConfig, contract ducklake.PostgresCatalogMaintenanceContract) (*PostgresRetentionCatalogSession, error) {
	session, err := ducklake.OpenPostgresCatalogMaintenanceSession(ctx, config)
	if err != nil {
		return nil, err
	}
	maintenance, err := ducklake.NewPostgresCatalogMaintenance(session.Conn(), contract)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return &PostgresRetentionCatalogSession{session: session, maintenance: maintenance}, nil
}

type RetentionMaintenanceRequest struct {
	MaintenanceID  string
	PhysicalPoolID string
	CatalogID      string
	OwnerID        string
	LeaseExpiresAt time.Time
	FileGrace      time.Duration
	DryRun         bool
	Evidence       json.RawMessage
}

type RetentionMaintenanceResult struct {
	Maintenance RetentionMaintenance
	Fence       RetentionMaintenanceFence
	Snapshots   []RetentionMaintenanceSnapshot
	DryRun      bool
}

type RetentionCoordinator struct {
	Control        *Repository
	OpenSessionFor RetentionCatalogSessionFactoryFor
}

func nilRetentionSession(session RetentionCatalogSession) bool {
	if session == nil {
		return true
	}
	v := reflect.ValueOf(session)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (c *RetentionCoordinator) validate(in RetentionMaintenanceRequest) error {
	if c == nil || c.Control == nil || c.OpenSessionFor == nil || !validUUID(in.MaintenanceID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.FileGrace < time.Microsecond {
		return ErrInvalid
	}
	return nil
}

func maintenanceEvidence(in RetentionMaintenanceRequest, phase string, extra map[string]any) json.RawMessage {
	values := map[string]any{"maintenance_id": in.MaintenanceID, "physical_pool_id": in.PhysicalPoolID, "catalog_id": in.CatalogID, "phase": phase}
	for key, value := range extra {
		values[key] = value
	}
	encoded, _ := json.Marshal(values)
	return encoded
}

// Run executes the resumable catalog-wide sequence.  Control mutations are
// persisted around every native phase; a successor can replay the same
// operation ID and skip completed child phases after a crash.
func (c *RetentionCoordinator) Run(ctx context.Context, in RetentionMaintenanceRequest) (result RetentionMaintenanceResult, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.validate(in); err != nil {
		return result, err
	}
	fence, err := c.Control.AcquireRetentionMaintenanceFence(ctx, AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID, LeaseExpiresAt: in.LeaseExpiresAt})
	if err != nil {
		return result, err
	}
	result.Fence = fence
	result.DryRun = in.DryRun
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := c.Control.ReleaseRetentionMaintenanceFence(cleanupCtx, fence); err != nil && runErr == nil {
			runErr = err
		}
	}()
	operation, err := startAndPrepareRetentionMaintenance(ctx, c.Control.db, RetentionMaintenance{MaintenanceID: in.MaintenanceID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID, FencingEpoch: fence.FencingEpoch, State: "running", Phase: "expiry", DryRun: in.DryRun, FileGraceMicros: in.FileGrace.Microseconds(), PhaseEvidence: in.Evidence})
	if err != nil {
		return result, err
	}
	result.Maintenance = operation
	if operation.State == "completed" {
		result.Snapshots, _ = c.Control.ListRetentionMaintenanceSnapshots(ctx, in.MaintenanceID)
		return result, nil
	}
	items, err := c.Control.ListRetentionMaintenanceSnapshots(ctx, in.MaintenanceID)
	if err != nil {
		return result, err
	}
	result.Snapshots = items
	refs := make([]SnapshotRef, 0, len(items))
	for _, item := range items {
		refs = append(refs, SnapshotRef{PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID})
	}
	if operation.SnapshotSetDigest != retentionSnapshotSetDigest(refs) {
		return result, fmt.Errorf("%w: persisted maintenance snapshot set changed", ErrConflict)
	}
	if operation.SnapshotSetDigest == "" {
		return result, fmt.Errorf("%w: maintenance snapshot set is not frozen", ErrConflict)
	}

	session, err := c.OpenSessionFor(ctx, RetentionCatalogSessionInput{Request: in, Fence: fence})
	if err != nil {
		return result, err
	}
	if nilRetentionSession(session) {
		return result, fmt.Errorf("%w: catalog session factory returned nil", ErrInvalid)
	}
	defer func() { runErr = errors.Join(runErr, session.Close()) }()

	// Explicit versions expiry is one catalog-wide destructive phase.  Only
	// retiring child rows are sent; already-expired rows are crash-replay work.
	var retiring []int64
	var alreadyExpired []struct {
		ref      SnapshotRef
		evidence json.RawMessage
	}
	if operation.Phase == "expiry" {
		for _, item := range items {
			if item.Phase != "eligible" {
				continue
			}
			retention, loadErr := c.Control.LoadSnapshotRetention(ctx, SnapshotRef{PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID})
			if loadErr != nil {
				return result, loadErr
			}
			if retention.State == RetentionRetiring || retention.State == RetentionExpiring {
				retiring = append(retiring, item.SnapshotID)
			} else if retention.State == RetentionExpired {
				alreadyExpired = append(alreadyExpired, struct {
					ref      SnapshotRef
					evidence json.RawMessage
				}{SnapshotRef{PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID}, retention.Evidence})
			}
		}
	}
	if len(retiring) > 0 {
		if err := c.beforeNativePhase(ctx, &fence); err != nil {
			return result, err
		}
		if err := session.ExpireSnapshots(ctx, retiring, in.DryRun); err != nil {
			return result, fmt.Errorf("expire DuckLake snapshots: %w", err)
		}
		if !in.DryRun {
			verifier, ok := session.(retentionCatalogSnapshotVerifier)
			if !ok {
				return result, fmt.Errorf("%w: catalog session cannot verify explicit snapshot expiry", ErrInvalid)
			}
			if err := verifier.VerifySnapshotsExpired(ctx, retiring); err != nil {
				return result, err
			}
		}
		for _, snapshotID := range retiring {
			ref := SnapshotRef{PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, SnapshotID: snapshotID}
			evidence := maintenanceEvidence(in, "expiry", map[string]any{"snapshot_id": snapshotID, "dry_run": in.DryRun})
			if !in.DryRun {
				if err := c.Control.ExpireSnapshotUnderMaintenanceFence(ctx, ref, evidence, time.Time{}, in.MaintenanceID, fence); err != nil {
					return result, err
				}
			}
		}
	}
	// A crash in an older coordinator could commit snapshot_retention=expired
	// before its child evidence. Reconcile that durable mismatch without
	// re-issuing a native expiry statement.
	if !in.DryRun {
		for _, item := range alreadyExpired {
			if len(item.evidence) == 0 {
				return result, fmt.Errorf("%w: expired snapshot evidence missing", ErrConflict)
			}
			if err := c.Control.ExpireSnapshotUnderMaintenanceFence(ctx, item.ref, item.evidence, time.Time{}, in.MaintenanceID, fence); err != nil {
				return result, err
			}
		}
	}
	// Re-read child phases after the native expiry and control updates. A
	// crash/replay may have advanced some rows while the initial enumeration is
	// still in memory; quarantine must act on the durable phase evidence.
	items, err = c.Control.ListRetentionMaintenanceSnapshots(ctx, in.MaintenanceID)
	if err != nil {
		return result, err
	}
	result.Snapshots = items
	// Reconcile any durable retention rows that are already beyond the child
	// evidence phase. This covers legacy crash windows (for example, an older
	// worker committed quarantine before its operation child update) before the
	// cleanup claim and physical phases inspect the child state.
	if !in.DryRun {
		for _, item := range items {
			ref := SnapshotRef{PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID}
			retention, loadErr := c.Control.LoadSnapshotRetention(ctx, ref)
			if loadErr != nil {
				return result, loadErr
			}
			if err := c.reconcileAdvancedRetentionEvidence(ctx, item, retention, in, fence.FencingEpoch); err != nil {
				return result, err
			}
		}
		items, err = c.Control.ListRetentionMaintenanceSnapshots(ctx, in.MaintenanceID)
		if err != nil {
			return result, err
		}
		result.Snapshots = items
	}
	// Quarantine is the fail-closed handoff: no physical file phase runs until
	// every exact expired snapshot has a persisted per-snapshot cleanup claim.
	if !in.DryRun {
		for i := range items {
			item := items[i]
			if item.Phase == "cleanup-complete" || item.Phase == "quarantined" {
				continue
			}
			ref := SnapshotRef{PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID}
			retention, loadErr := c.Control.LoadSnapshotRetention(ctx, ref)
			if loadErr != nil {
				return result, loadErr
			}
			if retention.State != RetentionExpired && retention.State != RetentionQuarantined {
				continue
			}
			quarantineEvidence := maintenanceEvidence(in, "quarantine", map[string]any{"snapshot_id": item.SnapshotID})
			if retention.State == RetentionQuarantined && len(retention.QuarantineEvidence) != 0 {
				quarantineEvidence = retention.QuarantineEvidence
			}
			cleanupFence, claimErr := c.Control.ClaimSnapshotCleanupUnderMaintenanceFence(ctx, ref, in.OwnerID, fence.LeaseExpiresAt, in.MaintenanceID, fence)
			if claimErr != nil {
				return result, claimErr
			}
			if retention.State == RetentionExpired {
				if err := c.Control.QuarantineSnapshotUnderMaintenanceFence(ctx, ref, quarantineEvidence, cleanupFence, in.MaintenanceID, fence); err != nil {
					return result, err
				}
			}
		}
	}
	items, err = c.Control.ListRetentionMaintenanceSnapshots(ctx, in.MaintenanceID)
	if err != nil {
		return result, err
	}
	result.Snapshots = items
	if operation.Phase == "expiry" || operation.Phase == "old-files" {
		operation.Phase, operation.PhaseEvidence = "old-files", maintenanceEvidence(in, "old-files", map[string]any{"snapshot_count": len(items), "dry_run": in.DryRun})
		operation.DryRun, operation.FileGraceMicros = in.DryRun, in.FileGrace.Microseconds()
		if err := c.Control.UpdateRetentionMaintenancePhase(ctx, operation); err != nil {
			return result, err
		}
		if err := c.beforeNativePhase(ctx, &fence); err != nil {
			return result, err
		}
		if err := session.CleanupOldFiles(ctx, in.FileGrace, in.DryRun); err != nil {
			return result, fmt.Errorf("cleanup DuckLake old files: %w", err)
		}
	}
	if operation.Phase != "completed" {
		operation.Phase, operation.PhaseEvidence = "orphans", maintenanceEvidence(in, "orphans", map[string]any{"file_grace": in.FileGrace.String(), "dry_run": in.DryRun})
		operation.DryRun, operation.FileGraceMicros = in.DryRun, in.FileGrace.Microseconds()
		if err := c.Control.UpdateRetentionMaintenancePhase(ctx, operation); err != nil {
			return result, err
		}
		if err := c.beforeNativePhase(ctx, &fence); err != nil {
			return result, err
		}
		if err := session.DeleteOrphanedFiles(ctx, in.FileGrace, in.DryRun); err != nil {
			return result, fmt.Errorf("delete DuckLake orphaned files: %w", err)
		}
	}

	if !in.DryRun {
		for _, item := range items {
			if item.Phase == "cleanup-complete" {
				continue
			}
			ref := SnapshotRef{PhysicalPoolID: item.PhysicalPoolID, CatalogID: item.CatalogID, SnapshotID: item.SnapshotID}
			retention, loadErr := c.Control.LoadSnapshotRetention(ctx, ref)
			if loadErr != nil {
				return result, loadErr
			}
			if retention.State != RetentionQuarantined && retention.State != RetentionExpired && retention.State != RetentionCleanupComplete {
				continue
			}
			cleanupFence := CleanupFence{OwnerID: in.OwnerID, FencingEpoch: retention.CleanupFencingEpoch, LeaseExpiresAt: retention.CleanupLeaseExpiresAt}
			if retention.State != RetentionCleanupComplete {
				var claimErr error
				cleanupFence, claimErr = c.Control.ClaimSnapshotCleanupUnderMaintenanceFence(ctx, ref, in.OwnerID, fence.LeaseExpiresAt, in.MaintenanceID, fence)
				if claimErr != nil {
					return result, claimErr
				}
			}
			if cleanupFence.FencingEpoch <= 0 || cleanupFence.OwnerID != in.OwnerID {
				return result, ErrRetentionMaintenanceFenceStale
			}
			cleanupEvidence := maintenanceEvidence(in, "cleanup-complete", map[string]any{"snapshot_id": item.SnapshotID, "file_grace": in.FileGrace.String()})
			if retention.State == RetentionCleanupComplete && len(retention.CleanupEvidence) != 0 {
				cleanupEvidence = retention.CleanupEvidence
			}
			if err := c.Control.CompleteSnapshotCleanupUnderMaintenanceFence(ctx, ref, cleanupEvidence, cleanupFence, in.MaintenanceID, fence); err != nil {
				return result, err
			}
		}
	}
	completedAt, err := databaseClock(ctx, c.Control.db)
	if err != nil {
		return result, err
	}
	operation.State, operation.Phase, operation.CompletedAt, operation.PhaseEvidence = "completed", "completed", completedAt, maintenanceEvidence(in, "completed", map[string]any{"snapshot_count": len(items), "dry_run": in.DryRun})
	operation.DryRun, operation.FileGraceMicros = in.DryRun, in.FileGrace.Microseconds()
	if err := c.Control.UpdateRetentionMaintenancePhase(ctx, operation); err != nil {
		return result, err
	}
	result.Maintenance = operation
	result.Fence = fence
	result.Snapshots, _ = c.Control.ListRetentionMaintenanceSnapshots(ctx, in.MaintenanceID)
	return result, nil
}

func retentionSnapshotSetDigest(refs []SnapshotRef) string {
	refs = append([]SnapshotRef(nil), refs...)
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].PhysicalPoolID != refs[j].PhysicalPoolID {
			return refs[i].PhysicalPoolID < refs[j].PhysicalPoolID
		}
		if refs[i].CatalogID != refs[j].CatalogID {
			return refs[i].CatalogID < refs[j].CatalogID
		}
		return refs[i].SnapshotID < refs[j].SnapshotID
	})
	h := sha256.New()
	for _, ref := range refs {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00", ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func (c *RetentionCoordinator) beforeNativePhase(ctx context.Context, fence *RetentionMaintenanceFence) error {
	if fence == nil {
		return ErrInvalid
	}
	if err := c.Control.RenewRetentionMaintenanceFenceFor(ctx, *fence, maxRetentionMaintenanceLease-time.Minute); err != nil {
		return err
	}
	row, err := querygen(c.Control.db).GetPoolMaintenanceFence(ctx, dbgen.GetPoolMaintenanceFenceParams{PhysicalPoolID: fence.PhysicalPoolID, CatalogID: fence.CatalogID})
	if err != nil {
		return mapRetentionAuthorityError(err)
	}
	if row.OwnerID == nil || *row.OwnerID != fence.OwnerID || row.FencingEpoch != fence.FencingEpoch {
		return ErrRetentionMaintenanceFenceStale
	}
	fence.LeaseExpiresAt = row.LeaseExpiresAt.Time.UTC()
	return c.Control.CheckRetentionMaintenanceFence(ctx, *fence)
}
