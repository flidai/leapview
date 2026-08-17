package deployment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var (
	ErrProjectClaimInvalid  = errors.New("project claim is invalid")
	ErrProjectClaimConflict = errors.New("project claim conflicts with the instance binding")
	ErrProjectClaimNotFound = errors.New("project claim not found")
)

// ProjectClaim is the durable, singleton binding for one database instance.
// It intentionally contains no approval or policy state.
type ProjectClaim struct {
	ProjectID   projectgraph.ResourceID
	Environment servingstate.Environment
	ClaimedBy   string
	ClaimedAt   time.Time
}

type ProjectClaimInput struct {
	ProjectID   projectgraph.ResourceID
	Environment servingstate.Environment
	ClaimedBy   string
	ClaimedAt   time.Time
}

func (input ProjectClaimInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil || input.ProjectID.String() != strings.TrimSpace(input.ProjectID.String()) {
		return fmt.Errorf("%w: canonical project id is required", ErrProjectClaimInvalid)
	}
	if err := servingstate.ValidateEnvironment(input.Environment); err != nil || input.Environment != servingstate.Environment(strings.TrimSpace(string(input.Environment))) {
		return fmt.Errorf("%w: canonical environment is required", ErrProjectClaimInvalid)
	}
	if input.ClaimedBy == "" || input.ClaimedBy != strings.TrimSpace(input.ClaimedBy) || len(input.ClaimedBy) > 256 || strings.IndexFunc(input.ClaimedBy, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: canonical actor is required", ErrProjectClaimInvalid)
	}
	if input.ClaimedAt.IsZero() {
		return fmt.Errorf("%w: claim time is required", ErrProjectClaimInvalid)
	}
	return nil
}

func (claim ProjectClaim) Validate() error {
	return (ProjectClaimInput{ProjectID: claim.ProjectID, Environment: claim.Environment, ClaimedBy: claim.ClaimedBy, ClaimedAt: claim.ClaimedAt}).Validate()
}

type ProjectClaimRepository interface {
	ClaimProject(context.Context, ProjectClaimInput) (ProjectClaim, error)
	GetProjectClaim(context.Context) (ProjectClaim, error)
}

type ProjectClaimService struct{ repository ProjectClaimRepository }

func NewProjectClaimService(repository ProjectClaimRepository) (*ProjectClaimService, error) {
	if repository == nil {
		return nil, fmt.Errorf("project claim repository is required")
	}
	return &ProjectClaimService{repository: repository}, nil
}

func (service *ProjectClaimService) Claim(ctx context.Context, input ProjectClaimInput) (ProjectClaim, error) {
	if service == nil || service.repository == nil {
		return ProjectClaim{}, fmt.Errorf("project claim service is unavailable")
	}
	if input.ClaimedAt.IsZero() {
		input.ClaimedAt = time.Now().UTC()
	}
	if err := input.Validate(); err != nil {
		return ProjectClaim{}, err
	}
	return service.repository.ClaimProject(ctx, input)
}

func (service *ProjectClaimService) ClaimProject(ctx context.Context, input ProjectClaimInput) (ProjectClaim, error) {
	return service.Claim(ctx, input)
}

func (service *ProjectClaimService) Get(ctx context.Context) (ProjectClaim, error) {
	if service == nil || service.repository == nil {
		return ProjectClaim{}, fmt.Errorf("project claim service is unavailable")
	}
	return service.repository.GetProjectClaim(ctx)
}

func (service *ProjectClaimService) GetProjectClaim(ctx context.Context) (ProjectClaim, error) {
	return service.Get(ctx)
}
