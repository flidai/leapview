package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/google/uuid"
)

// ensurePredecessorRollbackWindow turns the displaced generation's immutable
// plan policy into an explicit, database-timed reachability root. The root is
// created in the same transaction as pointer cutover and predecessor
// retirement, so rollback eligibility can never be advertised without the
// snapshot protection that makes it executable.
func (r *Repository) ensurePredecessorRollbackWindow(ctx context.Context, tx Tx, publication DeliveryPublication, predecessor depdb.FindLiveGenerationRootRow) error {
	generation, err := r.GenerationTx(ctx, tx, predecessor.GenerationID)
	if err != nil {
		return err
	}
	plan, err := r.PlanTx(ctx, tx, generation.PlanID)
	if err != nil {
		return err
	}
	rich, err := plan.RichPlan()
	if err != nil {
		return err
	}
	switch rich.Evidence.Rollback.Class {
	case deployment.DeliveryNonReversible:
		return nil
	case deployment.DeliveryRollbackSafe, deployment.DeliveryServingSafe:
	default:
		return fmt.Errorf("%w: predecessor rollback class is invalid", ErrConflict)
	}
	windowText := strings.TrimSpace(rich.Evidence.Rollback.RetentionWindow)
	if windowText == "" {
		// Legacy plans without an explicit window remain truthful: their
		// retiring generation root may drain, but no rollback horizon is
		// synthesized.
		return nil
	}
	window, err := time.ParseDuration(windowText)
	if err != nil || window <= 0 {
		return fmt.Errorf("%w: predecessor rollback retention window is invalid", ErrConflict)
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(map[string]string{
		"purpose":        "retired generation rollback window",
		"publication_id": publication.PublicationID,
	})
	if err != nil {
		return err
	}
	created, err := r.CreateRetentionRootTx(ctx, tx, DeliveryRetentionRoot{
		RootID: rollbackWindowRootID(publication.PublicationID), TargetID: predecessor.TargetID,
		CandidateID: predecessor.CandidateID, GenerationID: predecessor.GenerationID,
		SnapshotSealID: predecessor.SnapshotSealID, RootKind: "rollback", State: "live",
		ExpiresAt: now.Add(window), Evidence: evidence,
	})
	if err != nil {
		return err
	}
	if created.TargetID != predecessor.TargetID || created.CandidateID != predecessor.CandidateID ||
		created.GenerationID != predecessor.GenerationID || created.SnapshotSealID != predecessor.SnapshotSealID ||
		created.RootKind != "rollback" || created.State != "live" || !created.ExpiresAt.After(now) {
		return fmt.Errorf("%w: predecessor rollback retention root identity differs", ErrConflict)
	}
	return nil
}

func rollbackWindowRootID(publicationID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("leapview:delivery:rollback-window-root:"+publicationID)).String()
}
