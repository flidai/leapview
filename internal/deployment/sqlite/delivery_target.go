package sqlite

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
)

// ResolveDeliveryTarget exposes the authoritative target fence through the
// generated query layer; no deployment adapter performs ad-hoc SQL.
func (r *Repository) ResolveDeliveryTarget(ctx context.Context, targetID string) (deployment.DeliveryTarget, error) {
	if r == nil || r.queries == nil || targetID == "" {
		return deployment.DeliveryTarget{}, fmt.Errorf("delivery target repository is unavailable")
	}
	row, err := r.queries.GetDeliveryTargetRevision(ctx, targetID)
	if err != nil {
		return deployment.DeliveryTarget{}, err
	}
	return deployment.DeliveryTarget{TargetID: targetID, ProjectID: row.ProjectID, Environment: row.Environment, ActiveGenerationID: row.ActiveGenerationID, TargetRevision: row.TargetRevision}, nil
}

var _ deployment.DeliveryTargetResolver = (*Repository)(nil)
