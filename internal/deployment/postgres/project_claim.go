package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ClaimProject delegates the instance singleton to the platform bootstrap
// authority while preserving deployment's domain contract. The deployment
// capability owns this translation; platform remains independent of sibling
// domain packages.
func (r *Repository) ClaimProject(ctx context.Context, input deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	if r == nil || r.db == nil {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimInvalid
	}
	if err := input.Validate(); err != nil {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimInvalid
	}
	claim, err := platformbootstrap.New(r.db).ClaimProject(ctx, platformbootstrap.ProjectClaimInput{
		ProjectID: input.ProjectID.String(), Environment: string(input.Environment),
		ClaimedBy: input.ClaimedBy, ClaimedAt: input.ClaimedAt,
	})
	if err != nil {
		return deployment.ProjectClaim{}, mapClaimError(err)
	}
	return mapPlatformClaim(claim)
}

func (r *Repository) ClaimProjectTx(ctx context.Context, tx Tx, input deployment.ProjectClaimInput) (deployment.ProjectClaim, error) {
	if tx == nil {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimInvalid
	}
	if err := input.Validate(); err != nil {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimInvalid
	}
	claim, err := platformbootstrap.New(tx).ClaimProject(ctx, platformbootstrap.ProjectClaimInput{
		ProjectID: input.ProjectID.String(), Environment: string(input.Environment),
		ClaimedBy: input.ClaimedBy, ClaimedAt: input.ClaimedAt,
	})
	if err != nil {
		return deployment.ProjectClaim{}, mapClaimError(err)
	}
	return mapPlatformClaim(claim)
}

func (r *Repository) GetProjectClaim(ctx context.Context) (deployment.ProjectClaim, error) {
	if r == nil || r.db == nil {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimNotFound
	}
	claim, err := platformbootstrap.New(r.db).GetProjectClaim(ctx)
	if err != nil {
		return deployment.ProjectClaim{}, mapClaimError(err)
	}
	return mapPlatformClaim(claim)
}

func mapClaimError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, platformbootstrap.ErrNotFound) {
		return deployment.ErrProjectClaimNotFound
	}
	if errors.Is(err, platformbootstrap.ErrConflict) {
		return deployment.ErrProjectClaimConflict
	}
	// Unknown failures are infrastructure or decoding failures, not malformed
	// caller input. Preserve their chain so the API returns its generic 500
	// response and operators retain the underlying diagnostic.
	return fmt.Errorf("project claim authority: %w", err)
}

func mapPlatformClaim(claim platformbootstrap.ProjectClaim) (deployment.ProjectClaim, error) {
	id, err := projectgraph.NewResourceID(claim.ProjectID)
	if err != nil {
		return deployment.ProjectClaim{}, fmt.Errorf("%w: stored project claim project: %v", deployment.ErrProjectClaimInvalid, err)
	}
	value := deployment.ProjectClaim{ProjectID: id, Environment: servingstate.Environment(claim.Environment), ClaimedBy: claim.ClaimedBy, ClaimedAt: claim.ClaimedAt}
	if err := value.Validate(); err != nil {
		return deployment.ProjectClaim{}, deployment.ErrProjectClaimInvalid
	}
	return value, nil
}
