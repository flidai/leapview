package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

func (r *Repository) CreateQueryLease(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, error) {
	lease, err := deployment.NewDeliveryQueryLease(input)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	defer tx.Rollback()
	if lease.CandidateID != "" {
		candidate, readErr := deliveryCandidateByIDTx(ctx, tx, lease.CandidateID)
		if readErr != nil {
			return deployment.DeliveryQueryLease{}, readErr
		}
		if candidate.Status != deployment.DeliveryCandidateReady || candidate.CatalogDigest != lease.CatalogDigest || candidate.PhysicalPoolID != lease.PhysicalPoolID {
			return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: candidate root is not queryable", deployment.ErrDeliveryConflict)
		}
	} else {
		generation, readErr := deliveryGenerationByIDTx(ctx, tx, lease.GenerationID)
		if readErr != nil {
			return deployment.DeliveryQueryLease{}, readErr
		}
		if (generation.Status != deployment.DeliveryGenerationPrepared && generation.Status != deployment.DeliveryGenerationActive) || generation.CatalogDigest != lease.CatalogDigest || generation.PhysicalPoolID != lease.PhysicalPoolID {
			return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: generation root is not queryable", deployment.ErrDeliveryConflict)
		}
	}
	err = deploydb.New(tx).CreateDeliveryQueryLease(ctx, deploydb.CreateDeliveryQueryLeaseParams{ID: lease.ID, HolderID: lease.HolderID, NULLIF: lease.CandidateID, NULLIF_2: lease.GenerationID, CatalogDigest: lease.CatalogDigest, PhysicalPoolID: lease.PhysicalPoolID, ExpiresAt: deliveryTime(lease.ExpiresAt), CreatedAt: deliveryTime(lease.CreatedAt)})
	if err != nil {
		if existing, readErr := deliveryQueryLeaseByIDTx(ctx, tx, lease.ID); readErr == nil && sameQueryLeaseIdentity(existing, lease) {
			if eventErr := appendQueryLeaseEventTx(ctx, tx, existing, "lease_acquired", deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:"+existing.ID)), "accepted", existing.CreatedAt); eventErr != nil {
				return deployment.DeliveryQueryLease{}, eventErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return deployment.DeliveryQueryLease{}, commitErr
			}
			return existing, nil
		}
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease identity conflict", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, lease, "lease_acquired", deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:"+lease.ID)), "accepted", lease.CreatedAt); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return lease, nil
}

func (r *Repository) AcquireQueryLease(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, error) {
	return r.CreateQueryLease(ctx, input)
}

func sameQueryLeaseIdentity(a, b deployment.DeliveryQueryLease) bool {
	return a.ID == b.ID && a.HolderID == b.HolderID && a.CandidateID == b.CandidateID && a.GenerationID == b.GenerationID && a.CatalogDigest == b.CatalogDigest && a.PhysicalPoolID == b.PhysicalPoolID && a.ExpiresAt.Equal(b.ExpiresAt) && a.CreatedAt.Equal(b.CreatedAt)
}

func (r *Repository) DeliveryQueryLeaseByID(ctx context.Context, id string) (deployment.DeliveryQueryLease, error) {
	return deliveryQueryLeaseByIDTx(ctx, r.db, id)
}

func deliveryQueryLeaseByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryQueryLease, error) {
	row, err := deploydb.New(q).GetDeliveryQueryLease(ctx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	l := deployment.DeliveryQueryLease{ID: row.ID, HolderID: row.HolderID, CatalogDigest: row.CatalogDigest, PhysicalPoolID: row.PhysicalPoolID, Status: deployment.DeliveryLeaseStatus(row.Status)}
	if row.CandidateID.Valid {
		l.CandidateID = row.CandidateID.String
	}
	if row.GenerationID.Valid {
		l.GenerationID = row.GenerationID.String
	}
	l.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	l.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	l.ReleasedAt, err = parseNullableDeliveryTime(row.ReleasedAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return l, l.Validate()
}

func (r *Repository) HeartbeatQueryLease(ctx context.Context, id string, now, expiresAt time.Time) (deployment.DeliveryQueryLease, error) {
	l, err := r.DeliveryQueryLeaseByID(ctx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	updated, err := l.Heartbeat(now, expiresAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	res, err := r.queries.RenewDeliveryQueryLease(ctx, deploydb.RenewDeliveryQueryLeaseParams{ExpiresAt: deliveryTime(updated.ExpiresAt), ID: id, ExpiresAt_2: deliveryTime(l.ExpiresAt)})
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease heartbeat CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) ReleaseQueryLease(ctx context.Context, id string, now time.Time) (deployment.DeliveryQueryLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	defer tx.Rollback()
	l, err := deliveryQueryLeaseByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if l.Status == deployment.DeliveryLeaseReleased {
		return l, nil
	}
	updated, err := l.Release(now)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	res, err := deploydb.New(tx).ReleaseDeliveryQueryLease(ctx, deploydb.ReleaseDeliveryQueryLeaseParams{ReleasedAt: presentString(deliveryTime(updated.ReleasedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease release CAS failed", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, updated, "lease_released", deployment.CanonicalDeliveryDigest([]byte("query-lease-released:"+updated.ID)), "accepted", updated.ReleasedAt); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return updated, nil
}
func (r *Repository) ExpireQueryLease(ctx context.Context, id string, now time.Time) (deployment.DeliveryQueryLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	defer tx.Rollback()
	l, err := deliveryQueryLeaseByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if l.Status == deployment.DeliveryLeaseExpired {
		return l, nil
	}
	updated, err := l.Expire(now)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	res, err := deploydb.New(tx).ExpireDeliveryQueryLease(ctx, deploydb.ExpireDeliveryQueryLeaseParams{ReleasedAt: presentString(deliveryTime(updated.ReleasedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease expiry CAS failed", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, updated, "lease_expired", deployment.CanonicalDeliveryDigest([]byte("query-lease-expired:"+updated.ID)), "accepted", updated.ReleasedAt); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return updated, nil
}

func (r *Repository) CreateRetentionException(ctx context.Context, input deployment.DeliveryRetentionException) (deployment.DeliveryRetentionException, error) {
	root, err := deployment.NewDeliveryRetentionException(input)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	if root.CandidateID != "" {
		c, readErr := r.DeliveryCandidateByID(ctx, root.CandidateID)
		if readErr != nil {
			return deployment.DeliveryRetentionException{}, readErr
		}
		if c.CatalogDigest != root.CatalogDigest || c.PhysicalPoolID != root.PhysicalPoolID {
			return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention root differs from candidate", deployment.ErrDeliveryConflict)
		}
	} else {
		g, readErr := r.DeliveryGenerationByID(ctx, root.GenerationID)
		if readErr != nil {
			return deployment.DeliveryRetentionException{}, readErr
		}
		if g.CatalogDigest != root.CatalogDigest || g.PhysicalPoolID != root.PhysicalPoolID {
			return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention root differs from generation", deployment.ErrDeliveryConflict)
		}
	}
	err = r.queries.CreateDeliveryRetentionException(ctx, deploydb.CreateDeliveryRetentionExceptionParams{ID: root.ID, PhysicalPoolID: root.PhysicalPoolID, NULLIF: root.CandidateID, NULLIF_2: root.GenerationID, CatalogDigest: root.CatalogDigest, Reason: root.Reason, ExpiresAt: deliveryTime(root.ExpiresAt), CreatedAt: deliveryTime(root.CreatedAt)})
	if err != nil {
		if existing, readErr := r.DeliveryRetentionExceptionByID(ctx, root.ID); readErr == nil && sameRetentionIdentity(existing, root) {
			return existing, nil
		}
		return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention identity conflict", deployment.ErrDeliveryConflict)
	}
	return root, nil
}
func (r *Repository) CreateRetentionRoot(ctx context.Context, input deployment.DeliveryRetentionException) (deployment.DeliveryRetentionException, error) {
	return r.CreateRetentionException(ctx, input)
}
func sameRetentionIdentity(a, b deployment.DeliveryRetentionException) bool {
	return a.ID == b.ID && a.PhysicalPoolID == b.PhysicalPoolID && a.CandidateID == b.CandidateID && a.GenerationID == b.GenerationID && a.CatalogDigest == b.CatalogDigest && a.Reason == b.Reason && a.ExpiresAt.Equal(b.ExpiresAt) && a.CreatedAt.Equal(b.CreatedAt)
}
func (r *Repository) DeliveryRetentionExceptionByID(ctx context.Context, id string) (deployment.DeliveryRetentionException, error) {
	row, err := r.queries.GetDeliveryRetentionException(ctx, id)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	root := deployment.DeliveryRetentionException{ID: row.ID, PhysicalPoolID: row.PhysicalPoolID, CatalogDigest: row.CatalogDigest, Reason: row.Reason, Status: deployment.DeliveryRetentionExceptionStatus(row.Status)}
	if row.CandidateID.Valid {
		root.CandidateID = row.CandidateID.String
	}
	if row.GenerationID.Valid {
		root.GenerationID = row.GenerationID.String
	}
	root.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	root.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	root.ReleasedAt, err = parseNullableDeliveryTime(row.ReleasedAt)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	return root, root.Validate()
}
func (r *Repository) ReleaseRetentionException(ctx context.Context, id string, now time.Time) (deployment.DeliveryRetentionException, error) {
	root, err := r.DeliveryRetentionExceptionByID(ctx, id)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	if root.Status == deployment.DeliveryRetentionReleased {
		return root, nil
	}
	updated, err := root.Release(now)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	res, err := r.queries.ReleaseDeliveryRetentionException(ctx, deploydb.ReleaseDeliveryRetentionExceptionParams{ReleasedAt: presentString(deliveryTime(updated.ReleasedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention release CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) CreateGCCycle(ctx context.Context, input deployment.DeliveryGCCycle) (deployment.DeliveryGCCycle, error) {
	cycle, err := deployment.NewDeliveryGCCycle(input)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	err = deploydb.New(tx).CreateDeliveryGCCycle(ctx, deploydb.CreateDeliveryGCCycleParams{ID: cycle.ID, ActorID: eventActor(cycle.ActorID), PhysicalPoolID: cycle.PhysicalPoolID, Epoch: cycle.Epoch, RootRevision: cycle.RootRevision, CreatedAt: deliveryTime(cycle.CreatedAt)})
	if err != nil {
		_ = tx.Rollback()
		if existing, readErr := r.DeliveryGCCycleByID(ctx, cycle.ID); readErr == nil && existing.PhysicalPoolID == cycle.PhysicalPoolID && existing.Epoch == cycle.Epoch && existing.RootRevision == cycle.RootRevision {
			return existing, nil
		}
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC cycle identity conflict", deployment.ErrDeliveryConflict)
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return cycle, nil
}
func (r *Repository) DeliveryGCCycleByID(ctx context.Context, id string) (deployment.DeliveryGCCycle, error) {
	return deliveryGCCycleByIDTx(ctx, r.db, id)
}

func deliveryGCCycleByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryGCCycle, error) {
	row, err := deploydb.New(q).GetDeliveryGCCycle(ctx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	c := deployment.DeliveryGCCycle{ID: row.ID, ActorID: row.ActorID, PhysicalPoolID: row.PhysicalPoolID, Epoch: row.Epoch, RootRevision: row.RootRevision, Status: deployment.DeliveryGCStatus(row.Status), AbortReason: row.AbortReason}
	if row.MarkDigest.Valid {
		c.MarkDigest = row.MarkDigest.String
	}
	c.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	c.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return c, c.Validate()
}
func (r *Repository) MarkGCCycle(ctx context.Context, id, markDigest string) (deployment.DeliveryGCCycle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	c, err := deliveryGCCycleByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if (c.Status == deployment.DeliveryGCMarked || c.Status == deployment.DeliveryGCDeleting || c.Status == deployment.DeliveryGCComplete) && c.MarkDigest == markDigest {
		return c, nil
	}
	updated, err := c.Mark(markDigest)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := deploydb.New(tx).MarkDeliveryGCCycle(ctx, deploydb.MarkDeliveryGCCycleParams{MarkDigest: presentString(updated.MarkDigest), ID: id})
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC mark CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-marked:" + updated.ID + ":" + updated.MarkDigest))
	if err := appendGCCycleEventTx(ctx, tx, updated, "gc_marked", updated.ID, requestDigest, "accepted", updated.ActorID, map[string]any{"status": string(updated.Status)}, updated.CreatedAt); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return updated, nil
}
func (r *Repository) BeginGCDelete(ctx context.Context, id string) (deployment.DeliveryGCCycle, error) {
	c, err := r.DeliveryGCCycleByID(ctx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if c.Status == deployment.DeliveryGCDeleting {
		return c, nil
	}
	updated, err := c.BeginDelete()
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := r.queries.BeginDeliveryGCDelete(ctx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC delete CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}
func (r *Repository) CompleteGCCycle(ctx context.Context, id string, now time.Time) (deployment.DeliveryGCCycle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	c, err := deliveryGCCycleByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if c.Status == deployment.DeliveryGCComplete {
		return c, nil
	}
	updated, err := c.Complete(now)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := deploydb.New(tx).CompleteDeliveryGCCycle(ctx, deploydb.CompleteDeliveryGCCycleParams{CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC completion CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-complete:" + updated.ID))
	if err := appendGCCycleEventTx(ctx, tx, updated, "cleanup_completed", updated.ID, requestDigest, "accepted", updated.ActorID, map[string]any{"status": string(updated.Status)}, updated.CompletedAt); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return updated, nil
}
func (r *Repository) AbortGCCycle(ctx context.Context, id, reason string, now time.Time) (deployment.DeliveryGCCycle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	c, err := deliveryGCCycleByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if c.Status == deployment.DeliveryGCAborted && c.AbortReason == reason {
		return c, nil
	}
	updated, err := c.Abort(reason, now)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := deploydb.New(tx).AbortDeliveryGCCycle(ctx, deploydb.AbortDeliveryGCCycleParams{AbortReason: updated.AbortReason, CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC abort CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-aborted:" + updated.ID + ":" + updated.AbortReason))
	if err := appendGCCycleEventTx(ctx, tx, updated, "gc_aborted", updated.ID, requestDigest, "failed", updated.ActorID, map[string]any{"status": string(updated.Status)}, updated.CompletedAt); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return updated, nil
}

func (r *Repository) CreateGCDeleteIntent(ctx context.Context, input deployment.DeliveryGCDeleteIntent) (deployment.DeliveryGCDeleteIntent, error) {
	intent, err := deployment.NewDeliveryGCDeleteIntent(input)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	cycle, err := r.DeliveryGCCycleByID(ctx, intent.CycleID)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if cycle.PhysicalPoolID != intent.PhysicalPoolID || cycle.Status != deployment.DeliveryGCDeleting {
		return deployment.DeliveryGCDeleteIntent{}, fmt.Errorf("%w: GC cycle is not deleting", deployment.ErrDeliveryTransition)
	}
	err = r.queries.CreateDeliveryGCDeleteIntent(ctx, deploydb.CreateDeliveryGCDeleteIntentParams{ID: intent.ID, CycleID: intent.CycleID, PhysicalPoolID: intent.PhysicalPoolID, ObjectKey: intent.ObjectKey, ObjectDigest: intent.ObjectDigest, ObjectVersion: sql.NullString{String: intent.ObjectVersion, Valid: intent.ObjectVersion != ""}, CreatedAt: deliveryTime(intent.CreatedAt)})
	if err != nil {
		if existing, readErr := r.DeliveryGCDeleteIntentByID(ctx, intent.ID); readErr == nil && existing.CycleID == intent.CycleID && existing.ObjectKey == intent.ObjectKey && existing.ObjectDigest == intent.ObjectDigest && existing.ObjectVersion == intent.ObjectVersion {
			return existing, nil
		}
		return deployment.DeliveryGCDeleteIntent{}, fmt.Errorf("%w: delete intent identity conflict", deployment.ErrDeliveryConflict)
	}
	return intent, nil
}
func (r *Repository) DeliveryGCDeleteIntentByID(ctx context.Context, id string) (deployment.DeliveryGCDeleteIntent, error) {
	row, err := r.queries.GetDeliveryGCDeleteIntent(ctx, id)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i := deployment.DeliveryGCDeleteIntent{ID: row.ID, CycleID: row.CycleID, PhysicalPoolID: row.PhysicalPoolID, ObjectKey: row.ObjectKey, ObjectDigest: row.ObjectDigest, ObjectVersion: row.ObjectVersion.String, Status: deployment.DeliveryGCDeleteIntentStatus(row.Status)}
	i.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	return i, i.Validate()
}
func (r *Repository) CompleteGCDeleteIntent(ctx context.Context, id string, status deployment.DeliveryGCDeleteIntentStatus, now time.Time) (deployment.DeliveryGCDeleteIntent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	defer tx.Rollback()
	row, err := deploydb.New(tx).GetDeliveryGCDeleteIntent(ctx, id)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i := deployment.DeliveryGCDeleteIntent{ID: row.ID, CycleID: row.CycleID, PhysicalPoolID: row.PhysicalPoolID, ObjectKey: row.ObjectKey, ObjectDigest: row.ObjectDigest, ObjectVersion: row.ObjectVersion.String, Status: deployment.DeliveryGCDeleteIntentStatus(row.Status)}
	i.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if err := i.Validate(); err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if i.Status == status && status != deployment.DeliveryGCDeletePending {
		return i, nil
	}
	updated, err := i.Complete(status, now)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	res, err := deploydb.New(tx).CompleteDeliveryGCDeleteIntent(ctx, deploydb.CompleteDeliveryGCDeleteIntentParams{Status: string(updated.Status), CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCDeleteIntent{}, fmt.Errorf("%w: delete intent CAS failed", deployment.ErrDeliveryConflict)
	}
	cycle, err := deliveryGCCycleByIDTx(ctx, tx, updated.CycleID)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-deleted:" + updated.ID + ":" + string(updated.Status)))
	if err := appendGCCycleEventTx(ctx, tx, cycle, "gc_deleted", updated.ID, requestDigest, "accepted", cycle.ActorID, map[string]any{"status": string(updated.Status)}, updated.CompletedAt); err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	return updated, nil
}

// Concise aliases mirror the control-contract vocabulary while retaining the
// Delivery prefix on read methods that would otherwise collide with the
// historical release-candidate repository API.
func (r *Repository) DeliveryPlanByID(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
	return r.PlanByID(ctx, id)
}

func (r *Repository) CreateWriterLeaseAndAttempt(ctx context.Context, lease deployment.DeliveryWriterLease, attempt deployment.DeliveryBuildAttempt) (deployment.DeliveryWriterLease, deployment.DeliveryBuildAttempt, error) {
	return r.CreateWriterLeaseAndBuildAttempt(ctx, lease, attempt)
}

func (r *Repository) CASBuildTransition(ctx context.Context, id string, expectedRevision int64, next deployment.DeliveryBuildAttemptStatus, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	return r.TransitionBuildAttempt(ctx, id, expectedRevision, next, now)
}

func (r *Repository) PrepareSeal(ctx context.Context, seal deployment.CatalogSeal) (deployment.CatalogSeal, error) {
	return r.PrepareCatalogSeal(ctx, seal)
}

func (r *Repository) UploadSeal(ctx context.Context, id string) (deployment.CatalogSeal, error) {
	return r.MarkCatalogSealUploaded(ctx, id)
}

func (r *Repository) VerifySeal(ctx context.Context, id, closureDigest, qualificationDigest string, now time.Time) (deployment.CatalogSeal, error) {
	return r.VerifyCatalogSeal(ctx, id, closureDigest, qualificationDigest, now)
}

func (r *Repository) ReadyCandidate(ctx context.Context, candidate deployment.DeliveryCandidate, seal deployment.CatalogSeal, now time.Time) (deployment.DeliveryCandidate, error) {
	return r.CreateCandidateReady(ctx, candidate, seal, now)
}

func (r *Repository) RequestPublication(ctx context.Context, publication deployment.DeliveryPublication, generation ...deployment.DeliveryGeneration) (deployment.DeliveryPublication, error) {
	return r.CreatePublication(ctx, publication, generation...)
}

func (r *Repository) ActivatePublication(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	return r.CommitPublication(ctx, id, now)
}

func (r *Repository) CreateLease(ctx context.Context, lease deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, error) {
	return r.CreateQueryLease(ctx, lease)
}

func (r *Repository) ReleaseLease(ctx context.Context, id string, now time.Time) (deployment.DeliveryQueryLease, error) {
	return r.ReleaseQueryLease(ctx, id, now)
}

func (r *Repository) CreateRetentionRootException(ctx context.Context, root deployment.DeliveryRetentionException) (deployment.DeliveryRetentionException, error) {
	return r.CreateRetentionException(ctx, root)
}

func (r *Repository) CreateGCDelete(ctx context.Context, intent deployment.DeliveryGCDeleteIntent) (deployment.DeliveryGCDeleteIntent, error) {
	return r.CreateGCDeleteIntent(ctx, intent)
}
