package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// ListGCDeleteIntents is restart evidence: a collector resumes exact pending
// keys rather than reconstructing a mutable per-file manifest.
func (r *Repository) ListGCDeleteIntents(ctx context.Context, cycleID string) ([]deployment.DeliveryGCDeleteIntent, error) {
	rows, err := r.queries.ListDeliveryGCDeleteIntents(ctx, cycleID)
	if err != nil {
		return nil, err
	}
	var result []deployment.DeliveryGCDeleteIntent
	for _, row := range rows {
		i := deployment.DeliveryGCDeleteIntent{ID: row.ID, CycleID: row.CycleID, PhysicalPoolID: row.PhysicalPoolID, ObjectKey: row.ObjectKey, ObjectDigest: row.ObjectDigest, ObjectVersion: row.ObjectVersion.String, Status: deployment.DeliveryGCDeleteIntentStatus(row.Status)}
		i.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
		if err != nil {
			return nil, err
		}
		i.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
		if err != nil {
			return nil, err
		}
		if err := i.Validate(); err != nil {
			return nil, err
		}
		result = append(result, i)
	}
	return result, nil
}

// QuarantineRoot removes a corrupt root from the queryable lifecycle (or
// records a failed seal) while leaving all physical objects untouched. The
// caller intentionally fails closed if this update itself cannot commit.
func (r *Repository) QuarantineRoot(ctx context.Context, root deployment.DeliveryRoot, reason string, now time.Time) error {
	return r.quarantineRoot(ctx, root, reason, "gc", now)
}

// QuarantineRootWithActor is the operator-facing variant of QuarantineRoot.
// The legacy QuarantineRoot port remains intentionally actorless for the GC
// collector; this narrow extension lets offline repair retain the authenticated
// maintenance principal in the same transaction as the quarantine projection.
func (r *Repository) QuarantineRootWithActor(ctx context.Context, root deployment.DeliveryRoot, reason, actor string, now time.Time) error {
	if actor == "" {
		return fmt.Errorf("quarantine actor is required")
	}
	return r.quarantineRoot(ctx, root, reason, actor, now)
}

func (r *Repository) quarantineRoot(ctx context.Context, root deployment.DeliveryRoot, reason, actor string, now time.Time) error {
	if reason == "" || now.IsZero() || now.Location() != time.UTC {
		return fmt.Errorf("quarantine reason and UTC time are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	code := reason
	if len(code) > 512 {
		code = code[:512]
	}
	// Insert the hold before changing any lifecycle row. This exact catalog
	// identity remains a GC root until an explicit repair removes it. The
	// operation is called after the collector releases its GC fence because
	// normal root-creation triggers reject active roots while sweeping.
	sum := sha256.Sum256([]byte(root.Kind + "\x00" + root.SourceID + "\x00" + root.ObjectKey + "\x00" + root.CatalogDigest))
	quarantineID := "quarantine-" + hex.EncodeToString(sum[:16])
	res, err := deploydb.New(tx).UpdateQuarantineRoot(ctx, deploydb.UpdateQuarantineRootParams{CandidateID: presentString(root.CandidateID), GenerationID: presentString(root.GenerationID), LeaseID: presentString(root.LeaseID), CatalogDigest: root.CatalogDigest, ObjectKey: root.ObjectKey, PhysicalPoolID: root.PhysicalPoolID, SourceID: quarantineID})
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = deploydb.New(tx).CreateDeliveryRootRegistry(ctx, deploydb.CreateDeliveryRootRegistryParams{PhysicalPoolID: root.PhysicalPoolID, RootKind: "quarantined", SourceID: quarantineID, CandidateID: presentString(root.CandidateID), GenerationID: presentString(root.GenerationID), LeaseID: presentString(root.LeaseID), CatalogDigest: root.CatalogDigest, ObjectKey: root.ObjectKey, CreatedAt: deliveryTime(now), ExpiresAt: sql.NullString{}})
		if err != nil {
			return err
		}
	}
	switch root.Kind {
	case "build":
		_, err = deploydb.New(tx).FailDeliveryCatalogSealForPool(ctx, deploydb.FailDeliveryCatalogSealForPoolParams{FailureCode: code, ID: root.SourceID, PhysicalPoolID: root.PhysicalPoolID})
	case "candidate":
		_, err = deploydb.New(tx).FailDeliveryCandidateForPool(ctx, deploydb.FailDeliveryCandidateForPoolParams{FailureCode: code, ID: root.SourceID, PhysicalPoolID: root.PhysicalPoolID})
	case "published", "rollback":
		// Do not mutate the active pointer here. Serving may degrade separately;
		// the quarantined root is the durable protection boundary.
		err = nil
	case "retained", "lease", "quarantined":
		_, err = deploydb.New(tx).RetireDeliveryRootRegistry(ctx, deploydb.RetireDeliveryRootRegistryParams{RetiredAt: presentString(deliveryTime(now)), PhysicalPoolID: root.PhysicalPoolID, RootKind: root.Kind, SourceID: root.SourceID})
	default:
		return fmt.Errorf("unsupported root kind %q", root.Kind)
	}
	if err != nil {
		return err
	}
	// Quarantine is also an auditable GC boundary. Resolve the target scope
	// from the durable candidate/generation row; fresh pools without a target
	// revision retain the projection but intentionally do not fabricate an
	// event with an invalid target foreign key.
	if err := appendRootQuarantineEventTx(ctx, tx, root, quarantineID, code, actor, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}
