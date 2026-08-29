package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// DeliveryTargetRevision returns the durable target fence used by sealed
// publication and rollback request resolvers. It is a read-only control-plane
// capability; no runtime or object-store state is touched.
func (r *Repository) DeliveryTargetRevision(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	if r == nil || r.db == nil || targetID == "" {
		return deployment.DeliveryTarget{}, fmt.Errorf("delivery repository and target are required")
	}
	target, err := deploydb.New(r.db).GetDeliveryTargetRevision(ctx, targetID)
	if err == sql.ErrNoRows {
		return deployment.DeliveryTarget{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.DeliveryTarget{}, err
	}
	return deployment.DeliveryTarget{TargetID: targetID, ProjectID: target.ProjectID, Environment: target.Environment, ActiveGenerationID: target.ActiveGenerationID, TargetRevision: target.TargetRevision}, nil
}

// HasIndeterminateDeliveryPublication reports an external activation outcome
// that still needs durable reconciliation. Startup uses this as a fail-closed
// signal: an unknown publication is never inferred to be committed merely
// because a process was restarted.
func (r *Repository) HasIndeterminateDeliveryPublication(ctx context.Context, targetID string) (bool, error) {
	if r == nil || r.db == nil || targetID == "" {
		return false, fmt.Errorf("delivery repository and target are required")
	}
	exists, err := deploydb.New(r.db).HasIndeterminateDeliveryPublication(ctx, targetID)
	return exists, err
}

// ServingStateIDForArtifact resolves the immutable compiled serving-state
// identity persisted for one serving artifact. Delivery roots use this as a
// cross-store integrity check instead of deriving a state ID from a request or
// generation label.
func (r *Repository) ServingStateIDForArtifact(ctx context.Context, artifactID string) (string, error) {
	if r == nil || r.db == nil || strings.TrimSpace(artifactID) == "" {
		return "", deployment.ErrNotFound
	}
	stateID, err := deploydb.New(r.db).GetServingStateIDForArtifact(ctx, artifactID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", deployment.ErrNotFound
		}
		return "", err
	}
	if strings.TrimSpace(stateID) == "" {
		return "", deployment.ErrNotFound
	}
	return stateID, nil
}

// ActiveDeliveryGenerationForTarget resolves the durable target pointer and
// generation in one read sequence. The caller must still bind the returned
// serving-artifact identity to its graph artifact before attaching a catalog.
func (r *Repository) ActiveDeliveryGenerationForTarget(ctx context.Context, targetID, projectID, environment string) (deployment.DeliveryGeneration, error) {
	if r == nil || r.db == nil || targetID == "" {
		return deployment.DeliveryGeneration{}, fmt.Errorf("delivery repository and target are required")
	}
	target, err := deploydb.New(r.db).GetDeliveryTargetRevision(ctx, targetID)
	if err != nil {
		if err == sql.ErrNoRows {
			return deployment.DeliveryGeneration{}, deployment.ErrNotFound
		}
		return deployment.DeliveryGeneration{}, err
	}
	storedProject, storedEnvironment, active := target.ProjectID, target.Environment, target.ActiveGenerationID
	if storedProject != projectID || storedEnvironment != environment || active == "" {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: target scope or active generation changed", deployment.ErrDeliveryConflict)
	}
	generation, err := r.DeliveryGenerationByID(ctx, active)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if generation.Status != deployment.DeliveryGenerationActive || generation.ProjectID.String() != projectID || generation.Environment != environment {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: active delivery generation is not queryable", deployment.ErrDeliveryConflict)
	}
	return generation, nil
}

// DeliveryGenerationByServingStateID resolves the exact persisted delivery
// generation that owns one serving-state identity. It intentionally does not
// consult the target's active pointer: publication preparation must be able to
// attach a pending generation before the final CAS, while rollback preparation
// must attach a retained generation. The serving-state ID, target, project, and
// environment form an exact lookup tuple; no newest/implicit generation is
// selected.
func (r *Repository) DeliveryGenerationByServingStateID(ctx context.Context, targetID, projectID, environment, servingStateID string) (deployment.DeliveryGeneration, error) {
	if r == nil || r.db == nil || targetID == "" || projectID == "" || environment == "" || servingStateID == "" {
		return deployment.DeliveryGeneration{}, fmt.Errorf("delivery repository and generation identity are required")
	}
	generationIDs, err := deploydb.New(r.db).ListDeliveryGenerationIDsByServingState(ctx, deploydb.ListDeliveryGenerationIDsByServingStateParams{TargetID: targetID, ProjectID: projectID, Environment: environment, ServingStateID: servingStateID})
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if len(generationIDs) == 0 {
		return deployment.DeliveryGeneration{}, deployment.ErrNotFound
	}
	if len(generationIDs) > 1 {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: multiple delivery generations own serving state %q", deployment.ErrDeliveryConflict, servingStateID)
	}
	generationID := generationIDs[0]
	generation, err := r.DeliveryGenerationByID(ctx, generationID)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	if generation.ID == "" || generation.TargetID != targetID || generation.ProjectID.String() != projectID || generation.Environment != environment || generation.ServingStateID != servingStateID {
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: serving-state generation identity changed", deployment.ErrDeliveryConflict)
	}
	switch generation.Status {
	case deployment.DeliveryGenerationPrepared, deployment.DeliveryGenerationActive, deployment.DeliveryGenerationRetired:
		return generation, nil
	default:
		return deployment.DeliveryGeneration{}, fmt.Errorf("%w: serving-state generation status %q is not attachable", deployment.ErrDeliveryConflict, generation.Status)
	}
}
