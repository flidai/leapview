package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// BumpTargetRevision records one result-affecting target mutation and returns
// the new monotonic revision. The component row is explanatory evidence; the
// target_revision row remains the only CAS authority.
func (r *Repository) BumpTargetRevision(ctx context.Context, targetID, componentKind, componentID, componentDigest string, now time.Time) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("delivery repository is not open")
	}
	if err := deployment.ValidateDeliveryID(targetID); err != nil {
		return 0, err
	}
	for name, value := range map[string]string{"component kind": componentKind, "component id": componentID} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return 0, fmt.Errorf("%w: %s is not canonical", deployment.ErrDeliveryInvalid, name)
		}
	}
	if componentDigest != "" {
		if err := deployment.ValidateDeliveryDigest(componentDigest); err != nil {
			return 0, err
		}
	}
	if now.IsZero() || now.Location() != time.UTC {
		return 0, fmt.Errorf("%w: target revision time must be UTC", deployment.ErrDeliveryInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := deploydb.New(tx).BumpDeliveryTargetRevision(ctx, deploydb.BumpDeliveryTargetRevisionParams{UpdatedAt: deliveryTime(now), TargetID: targetID})
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, sql.ErrNoRows
	}
	target, err := deploydb.New(tx).GetDeliveryTargetRevision(ctx, targetID)
	if err != nil {
		return 0, err
	}
	if err := deploydb.New(tx).CreateDeliveryTargetRevisionComponent(ctx, deploydb.CreateDeliveryTargetRevisionComponentParams{
		TargetID: targetID, TargetRevision: target.TargetRevision, ProjectID: target.ProjectID, Environment: target.Environment,
		ComponentKind: componentKind, ComponentID: componentID, ComponentDigest: componentDigest, ChangedAt: deliveryTime(now),
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return target.TargetRevision, nil
}

// AdvanceTargetRevision and RecordTargetMutation are descriptive aliases for
// adapters that treat the operation as a CAS-fence invalidation boundary.
func (r *Repository) AdvanceTargetRevision(ctx context.Context, targetID, componentKind, componentID, componentDigest string, now time.Time) (int64, error) {
	return r.BumpTargetRevision(ctx, targetID, componentKind, componentID, componentDigest, now)
}

func (r *Repository) RecordTargetMutation(ctx context.Context, targetID, componentKind, componentID, componentDigest string, now time.Time) (int64, error) {
	return r.BumpTargetRevision(ctx, targetID, componentKind, componentID, componentDigest, now)
}
