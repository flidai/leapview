package deploymentpostgres

import (
	"context"
	"errors"
	"time"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

// NativeReader is the application-owned adapter for Deployment's bounded
// PostgreSQL read port. Keeping the adapter at composition boundaries makes
// it impossible for the module to infer a native authority from a legacy
// DeliveryReader implementation.
type NativeReader struct {
	repository *nativepostgres.Repository
}

var _ deploymentmodule.NativeDeliveryReader = (*NativeReader)(nil)

// NewNativeReader binds the concrete PostgreSQL authority to the module's
// native read contract. A nil authority is retained as an invalid adapter so
// callers get a deterministic error instead of a panic.
func NewNativeReader(repository *nativepostgres.Repository) *NativeReader {
	return &NativeReader{repository: repository}
}

func (r *NativeReader) authority() (*nativepostgres.Repository, error) {
	if r == nil || r.repository == nil {
		return nil, errors.New("deployment PostgreSQL native reader is not configured")
	}
	return r.repository, nil
}

func (r *NativeReader) Plan(ctx context.Context, id string) (nativepostgres.DeliveryPlan, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPlan{}, err
	}
	return authority.Plan(ctx, id)
}
func (r *NativeReader) LoadPlan(ctx context.Context, id string) (nativepostgres.DeliveryPlan, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPlan{}, err
	}
	return authority.LoadPlan(ctx, id)
}
func (r *NativeReader) BuildAttempt(ctx context.Context, id string) (nativepostgres.DeliveryBuildAttempt, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryBuildAttempt{}, err
	}
	return authority.BuildAttempt(ctx, id)
}
func (r *NativeReader) LoadBuildAttempt(ctx context.Context, id string) (nativepostgres.DeliveryBuildAttempt, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryBuildAttempt{}, err
	}
	return authority.LoadBuildAttempt(ctx, id)
}
func (r *NativeReader) SnapshotSeal(ctx context.Context, id string) (nativepostgres.SnapshotSeal, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.SnapshotSeal{}, err
	}
	return authority.SnapshotSeal(ctx, id)
}
func (r *NativeReader) LoadSnapshotSeal(ctx context.Context, id string) (nativepostgres.SnapshotSeal, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.SnapshotSeal{}, err
	}
	return authority.LoadSnapshotSeal(ctx, id)
}
func (r *NativeReader) Candidate(ctx context.Context, id string) (nativepostgres.DeliveryCandidate, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryCandidate{}, err
	}
	return authority.Candidate(ctx, id)
}
func (r *NativeReader) LoadCandidate(ctx context.Context, id string) (nativepostgres.DeliveryCandidate, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryCandidate{}, err
	}
	return authority.LoadCandidate(ctx, id)
}
func (r *NativeReader) ResolveCandidateGeneration(ctx context.Context, id string) (nativepostgres.CandidateGenerationResolution, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.CandidateGenerationResolution{}, err
	}
	return authority.ResolveCandidateGeneration(ctx, id)
}
func (r *NativeReader) Generation(ctx context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryGeneration{}, err
	}
	return authority.Generation(ctx, id)
}
func (r *NativeReader) LoadGeneration(ctx context.Context, id string) (nativepostgres.DeliveryGeneration, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryGeneration{}, err
	}
	return authority.LoadGeneration(ctx, id)
}
func (r *NativeReader) Publication(ctx context.Context, id string) (nativepostgres.DeliveryPublication, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPublication{}, err
	}
	return authority.Publication(ctx, id)
}
func (r *NativeReader) LoadPublication(ctx context.Context, id string) (nativepostgres.DeliveryPublication, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPublication{}, err
	}
	return authority.LoadPublication(ctx, id)
}
func (r *NativeReader) OperatorSnapshot(ctx context.Context, targetID string) (nativepostgres.DeliveryOperatorSnapshot, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryOperatorSnapshot{}, err
	}
	return authority.OperatorSnapshot(ctx, targetID)
}

// ListPublications forwards the bounded native publication collection through
// the application-owned adapter. The module deliberately receives this
// adapter, rather than the repository itself, so collection reads must remain
// part of the composed native reader surface.
func (r *NativeReader) ListPublications(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (nativepostgres.DeliveryPublicationPage, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPublicationPage{}, err
	}
	return authority.ListPublications(ctx, projectID, targetID, environment, limit, token)
}

// ListRetainedGenerations forwards the bounded native retained-generation
// collection through the application-owned adapter.
func (r *NativeReader) ListRetainedGenerations(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (nativepostgres.DeliveryGenerationPage, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryGenerationPage{}, err
	}
	return authority.ListRetainedGenerations(ctx, projectID, targetID, environment, limit, token)
}

// ListPlans forwards the bounded native plan collection through the
// application-owned adapter.
func (r *NativeReader) ListPlans(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (nativepostgres.DeliveryPlanPage, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPlanPage{}, err
	}
	return authority.ListPlans(ctx, projectID, targetID, environment, limit, token)
}

// ListBuildAttempts forwards the bounded native build-attempt collection
// through the application-owned adapter.
func (r *NativeReader) ListBuildAttempts(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (nativepostgres.DeliveryBuildAttemptPage, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryBuildAttemptPage{}, err
	}
	return authority.ListBuildAttempts(ctx, projectID, targetID, environment, limit, token)
}

// ListCandidates forwards the bounded native candidate collection through
// the application-owned adapter.
func (r *NativeReader) ListCandidates(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (nativepostgres.DeliveryCandidatePage, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryCandidatePage{}, err
	}
	return authority.ListCandidates(ctx, projectID, targetID, environment, limit, token)
}

// ListApprovalRequests forwards the bounded native approval-request collection
// through the application-owned adapter.
func (r *NativeReader) ListApprovalRequests(ctx context.Context, projectID, targetID, environment string, limit int32, token string) (nativepostgres.ApprovalRequestPage, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.ApprovalRequestPage{}, err
	}
	return authority.ListApprovalRequests(ctx, projectID, targetID, environment, limit, token)
}

// HistoricalCommittedPublication forwards the optional lifecycle evidence
// used by the native delivery projection.
func (r *NativeReader) HistoricalCommittedPublication(ctx context.Context, generationID string) (nativepostgres.DeliveryPublication, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryPublication{}, err
	}
	return authority.HistoricalCommittedPublication(ctx, generationID)
}

// GenerationRetentionRoot forwards the optional lifecycle evidence used by
// the native delivery projection.
func (r *NativeReader) GenerationRetentionRoot(ctx context.Context, generationID string) (nativepostgres.DeliveryRetentionRoot, error) {
	authority, err := r.authority()
	if err != nil {
		return nativepostgres.DeliveryRetentionRoot{}, err
	}
	return authority.GenerationRetentionRoot(ctx, generationID)
}

// GenerationRollbackUntil forwards the optional lifecycle evidence used by
// the native delivery projection.
func (r *NativeReader) GenerationRollbackUntil(ctx context.Context, generationID string) (time.Time, error) {
	authority, err := r.authority()
	if err != nil {
		return time.Time{}, err
	}
	return authority.GenerationRollbackUntil(ctx, generationID)
}
