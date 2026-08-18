package sqlite

// Durable physical-pool fencing.  A process may use a local mutex as an
// optimisation, but these compare-and-swap operations are the correctness
// boundary shared by every Repository connected to the SQLite database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

var (
	ErrFencingConflict = deployment.ErrDeliveryConflict
	ErrFencingStale    = deployment.ErrDeliveryStale
	ErrFencingInvalid  = deployment.ErrDeliveryInvalid
)

// sqliteBusyError also covers SQLITE_BUSY_SNAPSHOT (517), which is the
// expected result when two deferred transactions read the same root and then
// race to promote their snapshots to writers. The losing fenced operation is
// a delivery conflict, never an untyped storage failure.
func sqliteBusyError(err error) bool {
	if err == nil {
		return false
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() {
		case 5, 261, 517, 773: // SQLITE_BUSY and busy subcodes.
			return true
		}
	}
	return strings.Contains(strings.ToUpper(err.Error()), "SQLITE_BUSY")
}

func fencingBusyConflict(err error) error {
	if !sqliteBusyError(err) {
		return err
	}
	return fmt.Errorf("%w: concurrent fenced delivery transaction: %v", ErrFencingConflict, err)
}

// Fenced races should fail promptly and be classified by the caller. The
// platform store uses a generous timeout for ordinary work, but waiting that
// long on a doomed deferred-transaction upgrade makes concurrent retirement
// and lease acquisition nondeterministic. The owning transaction restores the
// normal timeout before commit (or while rolling back on an error).
func configureFencingTx(ctx context.Context, tx *sql.Tx) error {
	return setBusyTimeout(ctx, tx, 100)
}

func restoreFencingTx(ctx context.Context, tx *sql.Tx) {
	_ = setBusyTimeout(ctx, tx, 5000)
}

// PoolFence is the mutable control state for one physical pool. Epochs are
// never reused, including after expiry or process restart.
type PoolFence struct {
	PhysicalPoolID string
	WriterEpoch    int64
	GCEpoch        int64
	RootRevision   int64
	GCLeaseID      string
	GCHolderID     string
	GCExpires      time.Time
}

type WriterFence struct {
	ID             string
	AttemptID      string
	PhysicalPoolID string
	HolderID       string
	Epoch          int64
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type GCFence = deployment.GCFence

type WriterFenceRequest struct {
	ID             string
	AttemptID      string
	PhysicalPoolID string
	HolderID       string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type GCFenceRequest = deployment.GCFenceRequest

func validateFenceRequest(id, attempt, pool, holder string, created, expires time.Time) error {
	for name, value := range map[string]string{"fence": id, "attempt": attempt, "physical pool": pool, "holder": holder} {
		if err := deployment.ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if created.IsZero() || expires.IsZero() || created.Location() != time.UTC || expires.Location() != time.UTC || !expires.After(created) {
		return fmt.Errorf("%w: fence times must be UTC and expiry must be after creation", ErrFencingInvalid)
	}
	return nil
}

func (r *Repository) ensurePoolFenceTx(ctx context.Context, tx *sql.Tx, poolID string) error {
	if err := deployment.ValidateDeliveryID(poolID); err != nil {
		return err
	}
	// Avoid issuing an INSERT for the overwhelmingly common existing-fence
	// path. Even INSERT OR IGNORE takes SQLite's writer reservation briefly,
	// which can turn two otherwise-read transactions into a SQLITE_BUSY race.
	_, err := deploydb.New(tx).GetDeliveryPoolFencePresence(ctx, poolID)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	err = deploydb.New(tx).EnsureDeliveryPoolFence(ctx, poolID)
	return err
}

func (r *Repository) PoolFence(ctx context.Context, poolID string) (PoolFence, error) {
	if err := deployment.ValidateDeliveryID(poolID); err != nil {
		return PoolFence{}, err
	}
	row, err := deploydb.New(r.db).GetDeliveryPoolFence(ctx, poolID)
	if err != nil {
		return PoolFence{}, err
	}
	var parseErr error
	f := PoolFence{PhysicalPoolID: row.PhysicalPoolID, WriterEpoch: row.WriterEpoch, GCEpoch: row.GcLeaseEpoch, RootRevision: row.RootRevision, GCLeaseID: row.GcLeaseID, GCHolderID: row.GcHolderID}
	if row.GcExpiresAt.Valid {
		f.GCExpires, parseErr = parseDeliveryTime(row.GcExpiresAt.String)
		if parseErr != nil {
			return PoolFence{}, parseErr
		}
	}
	return f, nil
}

// ReconcilePoolFence clears only leases whose durable expiry has passed. It
// intentionally does not advance an epoch; the next acquisition does that.
func (r *Repository) ReconcilePoolFence(ctx context.Context, poolID string, now time.Time) (PoolFence, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return PoolFence{}, fmt.Errorf("%w: reconciliation time must be UTC", ErrFencingInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PoolFence{}, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, poolID); err != nil {
		return PoolFence{}, err
	}
	if err := deploydb.New(tx).ExpireDeliveryWriterLeasesExactPool(ctx, deploydb.ExpireDeliveryWriterLeasesExactPoolParams{ReleasedAt: presentString(deliveryTime(now)), PhysicalPoolID: poolID, Julianday: deliveryTime(now)}); err != nil {
		return PoolFence{}, err
	}
	if err := deploydb.New(tx).ExpireDeliveryGCLeasesForPool(ctx, deploydb.ExpireDeliveryGCLeasesForPoolParams{ReleasedAt: presentString(deliveryTime(now)), PhysicalPoolID: poolID, Julianday: deliveryTime(now)}); err != nil {
		return PoolFence{}, err
	}
	if err := deploydb.New(tx).ClearExpiredDeliveryGCLeaseFence(ctx, deploydb.ClearExpiredDeliveryGCLeaseFenceParams{UpdatedAt: deliveryTime(now), PhysicalPoolID: poolID, Julianday: deliveryTime(now)}); err != nil {
		return PoolFence{}, err
	}
	if err := tx.Commit(); err != nil {
		return PoolFence{}, err
	}
	return r.PoolFence(ctx, poolID)
}

// AcquireWriterFence retries the complete deferred transaction when SQLite
// reports SQLITE_BUSY_SNAPSHOT while two independent connections promote the
// same read snapshot. The retry is bounded and preserves the durable epoch
// allocation as the only correctness authority.
func (r *Repository) AcquireWriterFence(ctx context.Context, req WriterFenceRequest) (WriterFence, error) {
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		fence, err := r.acquireWriterFenceOnce(ctx, req)
		if err == nil {
			return fence, nil
		}
		last = err
		if !sqliteBusyError(err) {
			return WriterFence{}, err
		}
		select {
		case <-ctx.Done():
			return WriterFence{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 5 * time.Millisecond):
		}
	}
	return WriterFence{}, fencingBusyConflict(last)
}

func (r *Repository) acquireWriterFenceOnce(ctx context.Context, req WriterFenceRequest) (WriterFence, error) {
	if err := validateFenceRequest(req.ID, req.AttemptID, req.PhysicalPoolID, req.HolderID, req.CreatedAt, req.ExpiresAt); err != nil {
		return WriterFence{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return WriterFence{}, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, req.PhysicalPoolID); err != nil {
		return WriterFence{}, err
	}
	now := deliveryTime(req.CreatedAt)
	row, readErr := deploydb.New(tx).GetDeliveryWriterFenceLease(ctx, req.ID)
	var existing WriterFence
	if readErr == nil {
		existing = WriterFence{ID: row.ID, AttemptID: row.AttemptID, PhysicalPoolID: row.PhysicalPoolID, HolderID: row.OwnerID, Epoch: row.Epoch}
		var parseErr error
		existing.CreatedAt, parseErr = parseDeliveryTime(row.CreatedAt)
		if parseErr != nil {
			return WriterFence{}, parseErr
		}
		existing.ExpiresAt, parseErr = parseDeliveryTime(row.ExpiresAt)
		if parseErr != nil {
			return WriterFence{}, parseErr
		}
		if row.Status == string(deployment.DeliveryLeaseActive) && existing.AttemptID == req.AttemptID && existing.PhysicalPoolID == req.PhysicalPoolID && existing.HolderID == req.HolderID && existing.CreatedAt.Equal(req.CreatedAt) && existing.ExpiresAt.Equal(req.ExpiresAt) && existing.ExpiresAt.After(req.CreatedAt) {
			if err := tx.Commit(); err != nil {
				return WriterFence{}, err
			}
			return existing, nil
		}
		return WriterFence{}, fmt.Errorf("%w: writer lease identity is already bound", ErrFencingConflict)
	} else if readErr != sql.ErrNoRows {
		return WriterFence{}, readErr
	}
	if err := deploydb.New(tx).ExpireDeliveryWriterLeasesExactPool(ctx, deploydb.ExpireDeliveryWriterLeasesExactPoolParams{ReleasedAt: presentString(now), PhysicalPoolID: req.PhysicalPoolID, Julianday: now}); err != nil {
		return WriterFence{}, err
	}
	// Expired owners are cleared as part of the same write that claims the
	// pool. This is the restart/reconciliation fail-closed path.
	res, err := deploydb.New(tx).AdvanceDeliveryPoolWriterEpoch(ctx, deploydb.AdvanceDeliveryPoolWriterEpochParams{UpdatedAt: now, PhysicalPoolID: req.PhysicalPoolID, Julianday: now})
	if err != nil {
		return WriterFence{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return WriterFence{}, fmt.Errorf("%w: physical pool writer is fenced", ErrFencingConflict)
	}
	epoch, err := deploydb.New(tx).GetDeliveryPoolWriterEpoch(ctx, req.PhysicalPoolID)
	if err != nil {
		return WriterFence{}, err
	}
	if err := deploydb.New(tx).CreateDeliveryWriterFenceLease(ctx, deploydb.CreateDeliveryWriterFenceLeaseParams{ID: req.ID, AttemptID: req.AttemptID, PhysicalPoolID: req.PhysicalPoolID, OwnerID: req.HolderID, Epoch: epoch, ExpiresAt: deliveryTime(req.ExpiresAt), CreatedAt: now}); err != nil {
		return WriterFence{}, fmt.Errorf("%w: writer lease identity conflict", ErrFencingConflict)
	}
	if err := tx.Commit(); err != nil {
		return WriterFence{}, err
	}
	return WriterFence{ID: req.ID, AttemptID: req.AttemptID, PhysicalPoolID: req.PhysicalPoolID, HolderID: req.HolderID, Epoch: epoch, CreatedAt: req.CreatedAt, ExpiresAt: req.ExpiresAt}, nil
}

// AcquirePoolWriter is a parameter-oriented alias useful to callers that do
// not need to construct a request value.
func (r *Repository) AcquirePoolWriter(ctx context.Context, poolID, leaseID, holderID string, createdAt, expiresAt time.Time) (WriterFence, error) {
	return r.AcquireWriterFence(ctx, WriterFenceRequest{ID: leaseID, AttemptID: leaseID, PhysicalPoolID: poolID, HolderID: holderID, CreatedAt: createdAt, ExpiresAt: expiresAt})
}

func (r *Repository) AcquireWriter(ctx context.Context, req WriterFenceRequest) (WriterFence, error) {
	return r.AcquireWriterFence(ctx, req)
}

func (r *Repository) HeartbeatWriterFence(ctx context.Context, fence WriterFence, now, expiresAt time.Time) (WriterFence, error) {
	if err := validateFenceRequest(fence.ID, fence.AttemptID, fence.PhysicalPoolID, fence.HolderID, fence.CreatedAt, fence.ExpiresAt); err != nil {
		return WriterFence{}, err
	}
	if now.IsZero() || expiresAt.IsZero() || now.Location() != time.UTC || expiresAt.Location() != time.UTC || !expiresAt.After(now) {
		return WriterFence{}, fmt.Errorf("%w: invalid writer heartbeat time", ErrFencingInvalid)
	}
	res, err := r.queries.RenewDeliveryWriterFenceLease(ctx, deploydb.RenewDeliveryWriterFenceLeaseParams{ExpiresAt: deliveryTime(expiresAt), PhysicalPoolID: fence.PhysicalPoolID, ID: fence.ID, AttemptID: fence.AttemptID, OwnerID: fence.HolderID, Epoch: fence.Epoch, Julianday: deliveryTime(now)})
	if err != nil {
		return WriterFence{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return WriterFence{}, fmt.Errorf("%w: stale writer epoch or expired lease", ErrFencingStale)
	}
	fence.ExpiresAt = expiresAt
	return fence, nil
}

func (r *Repository) ReleaseWriterFence(ctx context.Context, fence WriterFence, now time.Time) error {
	if now.IsZero() || now.Location() != time.UTC {
		return fmt.Errorf("%w: release time must be UTC", ErrFencingInvalid)
	}
	res, err := r.queries.ReleaseDeliveryWriterFenceLease(ctx, deploydb.ReleaseDeliveryWriterFenceLeaseParams{ReleasedAt: presentString(deliveryTime(now)), PhysicalPoolID: fence.PhysicalPoolID, ID: fence.ID, AttemptID: fence.AttemptID, OwnerID: fence.HolderID, Epoch: fence.Epoch, Julianday: deliveryTime(now)})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: stale writer epoch or expired lease", ErrFencingStale)
	}
	return nil
}

func (r *Repository) HeartbeatWriter(ctx context.Context, fence WriterFence, now, expiresAt time.Time) (WriterFence, error) {
	return r.HeartbeatWriterFence(ctx, fence, now, expiresAt)
}

func (r *Repository) ReleaseWriter(ctx context.Context, fence WriterFence, now time.Time) error {
	return r.ReleaseWriterFence(ctx, fence, now)
}

// AcquireWriterLease adapts the existing deployment contract to the pool
// fence. The supplied epoch is ignored: SQLite allocates the next epoch.
func (r *Repository) AcquireWriterLease(ctx context.Context, lease deployment.DeliveryWriterLease) (deployment.DeliveryWriterLease, error) {
	if lease.CreatedAt.IsZero() || lease.ExpiresAt.IsZero() {
		return deployment.DeliveryWriterLease{}, fmt.Errorf("%w: writer lease times are required", ErrFencingInvalid)
	}
	f, err := r.AcquireWriterFence(ctx, WriterFenceRequest{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, HolderID: lease.OwnerID, CreatedAt: lease.CreatedAt.UTC(), ExpiresAt: lease.ExpiresAt.UTC()})
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	lease.Epoch, lease.Status, lease.ReleasedAt = f.Epoch, deployment.DeliveryLeaseActive, time.Time{}
	lease.CreatedAt, lease.ExpiresAt = f.CreatedAt, f.ExpiresAt
	return lease, nil
}

func (r *Repository) ReleaseWriterLeaseFence(ctx context.Context, lease deployment.DeliveryWriterLease, now time.Time) error {
	return r.ReleaseWriterFence(ctx, WriterFence{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, HolderID: lease.OwnerID, Epoch: lease.Epoch, CreatedAt: lease.CreatedAt, ExpiresAt: lease.ExpiresAt}, now)
}

func (r *Repository) AcquireGCFence(ctx context.Context, req GCFenceRequest) (GCFence, error) {
	if err := validateFenceRequest(req.ID, req.ID, req.PhysicalPoolID, req.HolderID, req.CreatedAt, req.ExpiresAt); err != nil {
		return GCFence{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GCFence{}, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, req.PhysicalPoolID); err != nil {
		return GCFence{}, err
	}
	now := deliveryTime(req.CreatedAt)
	row, readErr := deploydb.New(tx).GetDeliveryGCFenceState(ctx, req.PhysicalPoolID)
	existingID, existingHolder, existingExpiry, existingCreated, lastID := row.GcLeaseID, row.GcHolderID, row.GcExpiresAt, row.GcCreatedAt, row.GcLastLeaseID
	existingCaptured := row.GcRootRevision
	existingEpoch, existingRevision := row.GcLeaseEpoch, row.RootRevision
	if readErr != nil {
		return GCFence{}, readErr
	}
	if existingID != "" {
		if existingID == req.ID && existingHolder == req.HolderID && existingExpiry == deliveryTime(req.ExpiresAt) && existingCreated == now {
			if err := tx.Commit(); err != nil {
				return GCFence{}, err
			}
			if existingCaptured.Valid {
				existingRevision = existingCaptured.Int64
			}
			return GCFence{ID: req.ID, PhysicalPoolID: req.PhysicalPoolID, HolderID: req.HolderID, Epoch: existingEpoch, RootRevision: existingRevision, CreatedAt: req.CreatedAt, ExpiresAt: req.ExpiresAt}, nil
		}
		if sqliteTimeAfter(existingExpiry, now) {
			return GCFence{}, fmt.Errorf("%w: GC lease identity is already bound", ErrFencingConflict)
		}
		if existingID == req.ID {
			return GCFence{}, fmt.Errorf("%w: expired GC lease identity cannot be reused", ErrFencingConflict)
		}
	} else if lastID == req.ID {
		return GCFence{}, fmt.Errorf("%w: released GC lease identity cannot be reused", ErrFencingConflict)
	}
	if history, err := deploydb.New(tx).GetDeliveryGCLeaseStatus(ctx, req.ID); err == nil {
		_ = history
		return GCFence{}, fmt.Errorf("%w: historical GC lease identity cannot be reused", ErrFencingConflict)
	} else if err != sql.ErrNoRows {
		return GCFence{}, err
	}
	if err := deploydb.New(tx).ExpireDeliveryWriterLeasesExactPool(ctx, deploydb.ExpireDeliveryWriterLeasesExactPoolParams{ReleasedAt: presentString(now), PhysicalPoolID: req.PhysicalPoolID, Julianday: now}); err != nil {
		return GCFence{}, err
	}
	if err := deploydb.New(tx).ExpireDeliveryGCLeasesForPool(ctx, deploydb.ExpireDeliveryGCLeasesForPoolParams{ReleasedAt: presentString(now), PhysicalPoolID: req.PhysicalPoolID, Julianday: now}); err != nil {
		return GCFence{}, err
	}
	legacyWriters, err := deploydb.New(tx).CountActiveDeliveryWriters(ctx, deploydb.CountActiveDeliveryWritersParams{PhysicalPoolID: req.PhysicalPoolID, Julianday: now})
	if err != nil {
		return GCFence{}, err
	}
	res, err := deploydb.New(tx).AdvanceDeliveryGCFence(ctx, deploydb.AdvanceDeliveryGCFenceParams{GcLeaseID: presentString(req.ID), GcHolderID: presentString(req.HolderID), GcExpiresAt: presentString(deliveryTime(req.ExpiresAt)), GcCreatedAt: presentString(now), GcRootRevision: sql.NullInt64{Int64: existingRevision, Valid: true}, GcLastLeaseID: presentString(req.ID), UpdatedAt: now, PhysicalPoolID: req.PhysicalPoolID, Julianday: now, Column10: int64(legacyWriters)})
	if err != nil {
		return GCFence{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return GCFence{}, fmt.Errorf("%w: physical pool GC is fenced", ErrFencingConflict)
	}
	fenceState, err := deploydb.New(tx).GetDeliveryGCFenceEpochRevision(ctx, req.PhysicalPoolID)
	epoch, revision := fenceState.GcLeaseEpoch, fenceState.GcRootRevision
	if err != nil {
		return GCFence{}, err
	}
	if err := deploydb.New(tx).CreateDeliveryGCLease(ctx, deploydb.CreateDeliveryGCLeaseParams{ID: req.ID, PhysicalPoolID: req.PhysicalPoolID, HolderID: req.HolderID, Epoch: epoch, CreatedAt: now, ExpiresAt: deliveryTime(req.ExpiresAt)}); err != nil {
		return GCFence{}, fmt.Errorf("%w: GC lease history insert: %v", ErrFencingConflict, err)
	}
	if err := tx.Commit(); err != nil {
		return GCFence{}, err
	}
	rootRevision := revision.Int64
	if !revision.Valid {
		rootRevision = existingRevision
	}
	return GCFence{ID: req.ID, PhysicalPoolID: req.PhysicalPoolID, HolderID: req.HolderID, Epoch: epoch, RootRevision: rootRevision, CreatedAt: req.CreatedAt, ExpiresAt: req.ExpiresAt}, nil
}

func (r *Repository) AcquirePoolGC(ctx context.Context, poolID, leaseID, holderID string, createdAt, expiresAt time.Time) (GCFence, error) {
	return r.AcquireGCFence(ctx, GCFenceRequest{ID: leaseID, PhysicalPoolID: poolID, HolderID: holderID, CreatedAt: createdAt, ExpiresAt: expiresAt})
}

func (r *Repository) AcquireGCLease(ctx context.Context, req GCFenceRequest) (GCFence, error) {
	return r.AcquireGCFence(ctx, req)
}

func (r *Repository) HeartbeatGCFence(ctx context.Context, fence GCFence, now, expiresAt time.Time) (GCFence, error) {
	if now.IsZero() || expiresAt.IsZero() || now.Location() != time.UTC || expiresAt.Location() != time.UTC || !expiresAt.After(now) {
		return GCFence{}, fmt.Errorf("%w: invalid GC heartbeat time", ErrFencingInvalid)
	}
	res, err := r.queries.RenewDeliveryGCFence(ctx, deploydb.RenewDeliveryGCFenceParams{GcExpiresAt: presentString(deliveryTime(expiresAt)), UpdatedAt: deliveryTime(now), PhysicalPoolID: fence.PhysicalPoolID, GcLeaseID: presentString(fence.ID), GcHolderID: presentString(fence.HolderID), GcLeaseEpoch: fence.Epoch, Julianday: deliveryTime(now)})
	if err != nil {
		return GCFence{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return GCFence{}, fmt.Errorf("%w: stale GC epoch or expired lease", ErrFencingStale)
	}
	history, err := r.queries.RenewDeliveryGCLeaseHistory(ctx, deploydb.RenewDeliveryGCLeaseHistoryParams{ExpiresAt: deliveryTime(expiresAt), ID: fence.ID, PhysicalPoolID: fence.PhysicalPoolID, HolderID: fence.HolderID, Epoch: fence.Epoch})
	if err != nil {
		return GCFence{}, err
	}
	if n, _ := history.RowsAffected(); n != 1 {
		return GCFence{}, fmt.Errorf("%w: GC lease history is stale", ErrFencingStale)
	}
	fence.ExpiresAt = expiresAt
	return fence, nil
}

func (r *Repository) ReleaseGCFence(ctx context.Context, fence GCFence, now time.Time) error {
	if now.IsZero() || now.Location() != time.UTC {
		return fmt.Errorf("%w: release time must be UTC", ErrFencingInvalid)
	}
	res, err := r.queries.ReleaseDeliveryGCFence(ctx, deploydb.ReleaseDeliveryGCFenceParams{UpdatedAt: deliveryTime(now), PhysicalPoolID: fence.PhysicalPoolID, GcLeaseID: presentString(fence.ID), GcHolderID: presentString(fence.HolderID), GcLeaseEpoch: fence.Epoch, Julianday: deliveryTime(now)})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: stale GC epoch or expired lease", ErrFencingStale)
	}
	history, err := r.queries.ReleaseDeliveryGCLeaseHistory(ctx, deploydb.ReleaseDeliveryGCLeaseHistoryParams{ReleasedAt: presentString(deliveryTime(now)), ID: fence.ID, PhysicalPoolID: fence.PhysicalPoolID, HolderID: fence.HolderID, Epoch: fence.Epoch})
	if err != nil {
		return err
	}
	if n, _ := history.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: GC lease history is stale", ErrFencingStale)
	}
	return nil
}

func (r *Repository) HeartbeatGCLease(ctx context.Context, fence GCFence, now, expiresAt time.Time) (GCFence, error) {
	return r.HeartbeatGCFence(ctx, fence, now, expiresAt)
}

func (r *Repository) ReleaseGCLease(ctx context.Context, fence GCFence, now time.Time) error {
	return r.ReleaseGCFence(ctx, fence, now)
}

// IsCurrentGCFence revalidates both the GC epoch and the root-set revision
// immediately before a destructive sweep. Any root transition invalidates the
// captured mark and forces the collector to restart.
func (r *Repository) IsCurrentGCFence(ctx context.Context, fence GCFence, now time.Time) (bool, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return false, fmt.Errorf("%w: check time must be UTC", ErrFencingInvalid)
	}
	n, err := r.queries.IsCurrentDeliveryGCFence(ctx, deploydb.IsCurrentDeliveryGCFenceParams{PhysicalPoolID: fence.PhysicalPoolID, GcLeaseID: presentString(fence.ID), GcHolderID: presentString(fence.HolderID), GcLeaseEpoch: fence.Epoch, Julianday: deliveryTime(now), RootRevision: fence.RootRevision})
	return n == 1, err
}

// IsCurrentWriterFence is a cheap guard for a caller about to mutate an
// external catalog. It fails closed on expiry and never accepts owner identity
// without the epoch.
func (r *Repository) IsCurrentWriterFence(ctx context.Context, fence WriterFence, now time.Time) (bool, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return false, fmt.Errorf("%w: check time must be UTC", ErrFencingInvalid)
	}
	n, err := r.queries.IsCurrentDeliveryWriterFence(ctx, deploydb.IsCurrentDeliveryWriterFenceParams{PhysicalPoolID: fence.PhysicalPoolID, ID: fence.ID, AttemptID: fence.AttemptID, OwnerID: fence.HolderID, Epoch: fence.Epoch, Julianday: deliveryTime(now)})
	return n == 1, err
}

func writerFenceByIDTx(ctx context.Context, q deploydb.DBTX, id string) (WriterFence, error) {
	row, err := deploydb.New(q).GetDeliveryWriterFence(ctx, id)
	if err != nil {
		return WriterFence{}, err
	}
	f := WriterFence{ID: row.ID, AttemptID: row.AttemptID, PhysicalPoolID: row.PhysicalPoolID, HolderID: row.OwnerID, Epoch: row.Epoch}
	f.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return WriterFence{}, err
	}
	f.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
	return f, err
}

func sameBuildAttemptIdentity(a, b deployment.DeliveryBuildAttempt) bool {
	return a.ID == b.ID && a.PlanID == b.PlanID && a.IdempotencyKey == b.IdempotencyKey && a.PlanDigest == b.PlanDigest &&
		a.SourceDigest == b.SourceDigest && a.ExecutionDigest == b.ExecutionDigest &&
		a.BaseGenerationID == b.BaseGenerationID && a.BaseCatalogDigest == b.BaseCatalogDigest &&
		a.BasePhysicalPoolID == b.BasePhysicalPoolID && a.PhysicalPoolID == b.PhysicalPoolID &&
		a.WriterLeaseID == b.WriterLeaseID
}

func (r *Repository) retireDeliveryCandidateFenced(ctx context.Context, id string, now time.Time) (deployment.DeliveryCandidate, error) {
	candidate, err := r.retireDeliveryCandidateFencedOnce(ctx, id, now)
	if err == nil || !sqliteBusyError(err) {
		return candidate, err
	}
	// Re-read after rolling back the failed transaction. The other winner may
	// have committed either a query lease or retirement; both outcomes are a
	// typed delivery conflict for this loser.
	_, _ = deliveryCandidateByIDTx(ctx, r.db, id)
	return deployment.DeliveryCandidate{}, fencingBusyConflict(err)
}

func (r *Repository) retireDeliveryCandidateFencedOnce(ctx context.Context, id string, now time.Time) (deployment.DeliveryCandidate, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: retirement time must be UTC", ErrFencingInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if err := configureFencingTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return deployment.DeliveryCandidate{}, err
	}
	defer func() {
		restoreFencingTx(ctx, tx)
		_ = tx.Rollback()
	}()
	c, err := deliveryCandidateByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if c.Status == deployment.DeliveryCandidateRetired {
		return c, nil
	}
	if err := r.ensurePoolFenceTx(ctx, tx, c.PhysicalPoolID); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	fence, err := deploydb.New(tx).GetDeliveryPoolFence(ctx, c.PhysicalPoolID)
	gcLease, gcExpiry := fence.GcLeaseID, ""
	if fence.GcExpiresAt.Valid {
		gcExpiry = fence.GcExpiresAt.String
	}
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if gcLease != "" && sqliteTimeAfter(gcExpiry, deliveryTime(now)) {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: GC lease excludes retirement", ErrFencingConflict)
	}
	roots, err := deploydb.New(tx).CountCandidateQueryLeases(ctx, deploydb.CountCandidateQueryLeasesParams{CandidateID: presentString(id), Julianday: deliveryTime(now)})
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if roots > 0 {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: candidate has active query leases", ErrFencingConflict)
	}
	updated, err := c.Retire(now)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	res, err := deploydb.New(tx).RetireDeliveryCandidate(ctx, deploydb.RetireDeliveryCandidateParams{RetiredAt: presentString(deliveryTime(updated.RetiredAt)), ID: id})
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: candidate retirement CAS failed", ErrFencingConflict)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, c.PlanID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("candidate-retired:" + c.ID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(c.TargetID, requestDigest, "candidate_retired", "candidate", c.ID), TargetID: c.TargetID, ProjectID: c.ProjectID.String(), Environment: c.Environment,
		ActorID: eventActor(plan.ActorID), EventKind: "candidate_retired", ObjectKind: "candidate", ObjectID: c.ID, RequestDigest: requestDigest, PlanDigest: c.PlanDigest, Outcome: "accepted", Details: map[string]any{"status": string(updated.Status)}, CreatedAt: updated.RetiredAt,
	}); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	restoreFencingTx(ctx, tx)
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	return updated, nil
}

func (r *Repository) RetireDeliveryGeneration(ctx context.Context, id string, now time.Time) (deployment.DeliveryGeneration, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: retirement time must be UTC", ErrFencingInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	defer tx.Rollback()
	g, err := deliveryGenerationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if g.Status == deployment.DeliveryGenerationRetired {
		return g, nil
	}
	if err := r.ensurePoolFenceTx(ctx, tx, g.PhysicalPoolID); err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	fence, err := deploydb.New(tx).GetDeliveryPoolFence(ctx, g.PhysicalPoolID)
	gcLease, gcExpiry := fence.GcLeaseID, ""
	if fence.GcExpiresAt.Valid {
		gcExpiry = fence.GcExpiresAt.String
	}
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if gcLease != "" && sqliteTimeAfter(gcExpiry, deliveryTime(now)) {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: GC lease excludes retirement", ErrFencingConflict)
	}
	roots, err := deploydb.New(tx).CountGenerationQueryLeases(ctx, deploydb.CountGenerationQueryLeasesParams{GenerationID: presentString(id), Julianday: deliveryTime(now)})
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if roots > 0 {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: generation has active query leases", ErrFencingConflict)
	}
	updated, err := g.Retire(now)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	res, err := deploydb.New(tx).RetireDeliveryGenerationActive(ctx, deploydb.RetireDeliveryGenerationActiveParams{RetiredAt: presentString(deliveryTime(updated.RetiredAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: generation retirement CAS failed", ErrFencingConflict)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, g.PlanID)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("generation-retired:" + g.ID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(g.TargetID, requestDigest, "retirement_committed", "generation", g.ID), TargetID: g.TargetID, ProjectID: g.ProjectID.String(), Environment: g.Environment,
		ActorID: eventActor(plan.ActorID), EventKind: "retirement_committed", ObjectKind: "generation", ObjectID: g.ID, RequestDigest: requestDigest, PlanDigest: g.PlanDigest, Outcome: "accepted", Details: map[string]any{"status": string(updated.Status)}, CreatedAt: updated.RetiredAt,
	}); err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	return updated, nil
}
