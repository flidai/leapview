package postgres

import (
	"context"
	"errors"
	"fmt"
)

func isRollbackPublication(ctx context.Context, db DBTX, publication DeliveryPublication) (bool, error) {
	rootID, err := uuidID(publication.PublicationID, "publication id", false)
	if err != nil {
		return false, err
	}
	root, err := loadRetentionRoot(ctx, db, rootID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if root.TargetID != publication.TargetID || root.GenerationID != publication.GenerationID || root.CandidateID != publication.CandidateID || root.SnapshotSealID != publication.SnapshotSealID || root.RootKind != "rollback" || root.State != "live" {
		return false, fmt.Errorf("%w: rollback publication root identity differs", ErrConflict)
	}
	return true, nil
}

func (r *Repository) IsRollbackPublication(ctx context.Context, publication DeliveryPublication) (bool, error) {
	if r == nil || r.db == nil {
		return false, ErrInvalid
	}
	return isRollbackPublication(ctx, r.db, publication)
}
