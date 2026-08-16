package deployment

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type Repository interface {
	CreateDeployment(context.Context, CreateInput) (Deployment, error)
	DeploymentByID(context.Context, string) (Deployment, error)
	CancelDeployment(context.Context, string) (Deployment, error)
	FailDeployment(context.Context, string, error) error
}
type ActivationUnitOfWork interface {
	ActivateDeployment(context.Context, ActivationInput) (Deployment, error)
}
type ManagedDataResolver interface {
	ResolveManagedData(context.Context, servingstate.ID) (runtimehost.ManagedDataResolution, error)
}
type ServingStateRepository interface {
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
}

type Prepared interface {
	DuckLakeSnapshotID() int64
	Close() error
}
type Runtime interface {
	Prepare(context.Context, runtimehost.ServingStateCandidate) (Prepared, error)
	Verify(context.Context, Prepared) (Verification, error)
	Activate(Prepared, func() error) error
}
type runtimeRegistry interface {
	PrepareServingStateCandidate(context.Context, runtimehost.ServingStateCandidate) (*runtimehost.Prepared, error)
	VerifyPrepared(context.Context, *runtimehost.Prepared) (runtimehost.PreparedVerification, error)
	ActivatePrepared(*runtimehost.Prepared, func() error) error
}
type registryRuntime struct{ registry runtimeRegistry }
type registryPrepared struct{ prepared *runtimehost.Prepared }

func NewRegistryRuntime(registry runtimeRegistry) (Runtime, error) {
	if registry == nil {
		return nil, fmt.Errorf("runtime registry is required")
	}
	return registryRuntime{registry: registry}, nil
}
func (r registryRuntime) Prepare(ctx context.Context, candidate runtimehost.ServingStateCandidate) (Prepared, error) {
	p, err := r.registry.PrepareServingStateCandidate(ctx, candidate)
	if err != nil {
		return nil, err
	}
	return registryPrepared{prepared: p}, nil
}
func (r registryRuntime) Verify(ctx context.Context, prepared Prepared) (Verification, error) {
	p, ok := prepared.(registryPrepared)
	if !ok || p.prepared == nil {
		return Verification{}, fmt.Errorf("prepared runtime belongs to a different coordinator")
	}
	v, err := r.registry.VerifyPrepared(ctx, p.prepared)
	if err != nil {
		return Verification{}, err
	}
	return Verification{Digest: v.Digest}, nil
}
func (r registryRuntime) Activate(prepared Prepared, activate func() error) error {
	p, ok := prepared.(registryPrepared)
	if !ok || p.prepared == nil {
		return fmt.Errorf("prepared runtime belongs to a different coordinator")
	}
	return r.registry.ActivatePrepared(p.prepared, activate)
}
func (p registryPrepared) DuckLakeSnapshotID() int64 { return p.prepared.DuckLakeSnapshotID() }
func (p registryPrepared) Close() error              { return p.prepared.Close() }

type Service struct {
	repository Repository
	activation ActivationUnitOfWork
	states     ServingStateRepository
	runtime    Runtime
	resolver   ManagedDataResolver
}

func New(repository Repository, activation ActivationUnitOfWork, states ServingStateRepository, runtime Runtime, resolver ManagedDataResolver) (*Service, error) {
	if repository == nil || activation == nil || states == nil || runtime == nil || resolver == nil {
		return nil, fmt.Errorf("deployment repository, activation, serving-state, runtime, and managed-data resolver are required")
	}
	return &Service{repository: repository, activation: activation, states: states, runtime: runtime, resolver: resolver}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Deployment, error) {
	rawID, rawDigest, rawCreatedBy := input.ID, input.RequestDigest, input.CreatedBy
	input.ID, input.RequestDigest, input.CreatedBy = strings.TrimSpace(input.ID), strings.TrimSpace(input.RequestDigest), strings.TrimSpace(input.CreatedBy)
	if rawID != input.ID || rawDigest != input.RequestDigest || rawCreatedBy != input.CreatedBy {
		return Deployment{}, fmt.Errorf("deployment identity fields must be canonical")
	}
	if err := ValidateCreate(input); err != nil {
		return Deployment{}, err
	}
	return s.repository.CreateDeployment(ctx, input)
}
func (s *Service) Get(ctx context.Context, scope Scope) (Deployment, error) {
	if scope.ProjectID == "" || scope.DeploymentID == "" || scope.DeploymentID != strings.TrimSpace(scope.DeploymentID) {
		return Deployment{}, fmt.Errorf("project and deployment id are required")
	}
	d, err := s.repository.DeploymentByID(ctx, scope.DeploymentID)
	if err != nil {
		return Deployment{}, err
	}
	if d.ServingIdentity.ProjectID != scope.ProjectID {
		return Deployment{}, ErrNotFound
	}
	return d, nil
}
func (s *Service) Cancel(ctx context.Context, scope Scope) (Deployment, error) {
	d, err := s.Get(ctx, scope)
	if err != nil {
		return Deployment{}, err
	}
	if d.Status == StatusCancelled {
		return d, nil
	}
	if d.Status != StatusPending {
		return Deployment{}, fmt.Errorf("%w: deployment is %s", ErrConflict, d.Status)
	}
	return s.repository.CancelDeployment(ctx, d.ID)
}

func (s *Service) Activate(ctx context.Context, request ActivationRequest) (Deployment, error) {
	if request.ActorID == "" || request.ActorID != strings.TrimSpace(request.ActorID) {
		return Deployment{}, fmt.Errorf("activation principal is required")
	}
	row, err := s.Get(ctx, request.Scope)
	if err != nil {
		return Deployment{}, err
	}
	if row.Status == StatusActive {
		return row, nil
	}
	if row.Status != StatusPending {
		return Deployment{}, fmt.Errorf("%w: deployment is %s", ErrConflict, row.Status)
	}
	if row.ServingIdentity.GenerationID == "" || digest.ValidateSHA256Identity(row.ArtifactDigest) != nil {
		return Deployment{}, fmt.Errorf("%w: deployment identity is incomplete", ErrConflict)
	}
	resolution, err := s.resolver.ResolveManagedData(ctx, servingstate.ID(row.ServingIdentity.GenerationID))
	if err != nil {
		_ = s.repository.FailDeployment(ctx, row.ID, err)
		return Deployment{}, err
	}
	prepared, err := s.runtime.Prepare(ctx, runtimehost.ServingStateCandidate{Identity: row.ServingIdentity, ManagedData: resolution})
	if err != nil {
		_ = s.repository.FailDeployment(ctx, row.ID, err)
		return Deployment{}, err
	}
	defer prepared.Close()
	if id := prepared.DuckLakeSnapshotID(); id > 0 {
		if err := s.states.RecordDuckLakeSnapshot(ctx, servingstate.ID(row.ServingIdentity.GenerationID), id); err != nil {
			_ = s.repository.FailDeployment(ctx, row.ID, err)
			return Deployment{}, err
		}
	}
	verification, err := s.runtime.Verify(ctx, prepared)
	if err != nil {
		_ = s.repository.FailDeployment(ctx, row.ID, err)
		return Deployment{}, err
	}
	if digest.ValidateSHA256Identity(verification.Digest) != nil {
		invalid := fmt.Errorf("runtime verification returned invalid evidence")
		_ = s.repository.FailDeployment(ctx, row.ID, invalid)
		return Deployment{}, invalid
	}
	activationInput := ActivationInput{DeploymentID: row.ID, ServingIdentity: row.ServingIdentity, ArtifactDigest: row.ArtifactDigest, PriorGenerationID: row.PriorGenerationID, ActivationPrincipal: request.ActorID, VerificationDigest: verification.Digest}
	if err := ValidateActivation(activationInput); err != nil {
		_ = s.repository.FailDeployment(ctx, row.ID, err)
		return Deployment{}, err
	}
	var activated Deployment
	if err := s.runtime.Activate(prepared, func() error {
		var e error
		activated, e = s.activation.ActivateDeployment(ctx, activationInput)
		return e
	}); err != nil {
		return Deployment{}, err
	}
	return activated, nil
}
