package refreshpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

// PostgresCanonicalVerifierAdapter verifies delivery-owned plan, generation,
// snapshot-seal and build-attempt evidence through the refresh transaction.
// The adapter is bound to one deployment target; pool and catalog identities
// are read from the immutable seal and catalog identity rows instead of
// process configuration.
type PostgresCanonicalVerifierAdapter struct {
	Deployment *deploymentpostgres.Repository
	TargetID   string
}

// PostgresPublicationIdentityResolverAdapter resolves the exact physical
// namespace admitted for one deployment target. It intentionally shares the
// complete control-evidence loader with the canonical verifier.
type PostgresPublicationIdentityResolverAdapter struct {
	Deployment *deploymentpostgres.Repository
	TargetID   string
}

var _ refreshpostgres.PostgresPublicationIdentityResolver = (*PostgresPublicationIdentityResolverAdapter)(nil)

func NewPostgresCanonicalVerifierAdapter(deployment *deploymentpostgres.Repository, targetID string) (*PostgresCanonicalVerifierAdapter, error) {
	if err := validateAdapterConfig(deployment, targetID); err != nil {
		return nil, err
	}
	return &PostgresCanonicalVerifierAdapter{Deployment: deployment, TargetID: targetID}, nil
}

func NewPostgresPublicationIdentityResolverAdapter(deployment *deploymentpostgres.Repository, targetID string) (*PostgresPublicationIdentityResolverAdapter, error) {
	if err := validateAdapterConfig(deployment, targetID); err != nil {
		return nil, err
	}
	return &PostgresPublicationIdentityResolverAdapter{Deployment: deployment, TargetID: targetID}, nil
}

func validateAdapterConfig(deployment *deploymentpostgres.Repository, targetID string) error {
	if deployment == nil {
		return errors.New("deployment repository is required")
	}
	if targetID == "" || targetID != strings.TrimSpace(targetID) || len(targetID) > 255 {
		return errors.New("deployment target id must be canonical")
	}
	return nil
}

// ResolvePublicationIdentityTx implements the refresh module's late-bound
// identity resolver contract. Every read is issued through the caller-owned
// transaction, so a successful identity can only accompany the same
// transaction that writes refresh provenance.
func (r *PostgresPublicationIdentityResolverAdapter) ResolvePublicationIdentityTx(ctx context.Context, tx refreshpostgres.Tx, request refreshpostgres.PostgresPublicationIdentityRequest) (refreshpostgres.PostgresPublicationIdentity, error) {
	if r == nil || r.Deployment == nil || tx == nil {
		return refreshpostgres.PostgresPublicationIdentity{}, refreshpostgres.ErrPublicationIdentityUnavailable
	}
	evidence, err := loadCanonicalDeliveryEvidence(ctx, tx, r.Deployment, r.TargetID, request)
	if err != nil {
		return refreshpostgres.PostgresPublicationIdentity{}, err
	}
	return refreshpostgres.PostgresPublicationIdentity{PhysicalPoolID: evidence.Seal.PhysicalPoolID, CatalogID: evidence.Seal.CatalogID}, nil
}

// ResolvePublicationIdentityTx also lets the verifier be supplied directly
// as the resolver when a composition root wants one target-bound adapter.
func (v *PostgresCanonicalVerifierAdapter) ResolvePublicationIdentityTx(ctx context.Context, tx refreshpostgres.Tx, request refreshpostgres.PostgresPublicationIdentityRequest) (refreshpostgres.PostgresPublicationIdentity, error) {
	if v == nil || v.Deployment == nil || tx == nil {
		return refreshpostgres.PostgresPublicationIdentity{}, refreshpostgres.ErrPublicationIdentityUnavailable
	}
	evidence, err := loadCanonicalDeliveryEvidence(ctx, tx, v.Deployment, v.TargetID, request)
	if err != nil {
		return refreshpostgres.PostgresPublicationIdentity{}, err
	}
	return refreshpostgres.PostgresPublicationIdentity{PhysicalPoolID: evidence.Seal.PhysicalPoolID, CatalogID: evidence.Seal.CatalogID}, nil
}

// canonicalDeliveryEvidence is the immutable delivery proof shared by both
// refresh identity resolution and canonical completion verification.
type canonicalDeliveryEvidence struct {
	Target      deploymentpostgres.DeliveryTarget
	Generation  deploymentpostgres.DeliveryGeneration
	Seal        deploymentpostgres.SnapshotSeal
	Attempt     deploymentpostgres.DeliveryBuildAttempt
	Publication deploymentpostgres.DeliveryPublication
	Admission   physicalpool.AdmissionContract
	Catalog     ducklakepostgres.CatalogIdentity
}

func loadCanonicalDeliveryEvidence(ctx context.Context, tx refreshpostgres.Tx, deployment *deploymentpostgres.Repository, targetID string, request refreshpostgres.PostgresPublicationIdentityRequest) (canonicalDeliveryEvidence, error) {
	if deployment == nil || tx == nil {
		return canonicalDeliveryEvidence{}, unavailableError("deployment authority transaction is unavailable")
	}
	if targetID == "" || targetID != strings.TrimSpace(targetID) {
		return canonicalDeliveryEvidence{}, unavailableError("deployment target is not canonical")
	}
	if request.ProjectID == "" || request.ProjectID != strings.TrimSpace(request.ProjectID) || request.Environment == "" || request.Environment != strings.TrimSpace(request.Environment) || request.GenerationID == "" || request.GenerationID != strings.TrimSpace(request.GenerationID) {
		return canonicalDeliveryEvidence{}, unavailableError("publication identity request scope is not canonical")
	}
	if request.Source != "" && request.Source != "publish" && request.Source != "refresh" {
		return canonicalDeliveryEvidence{}, mismatchError("publication identity source is unsupported")
	}

	generation, err := deployment.GenerationTx(ctx, tx, request.GenerationID)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical generation: %v", err)
	}
	if generation.GenerationID != request.GenerationID || generation.TargetID != targetID || generation.CandidateID == "" || generation.SnapshotSealID == "" || generation.PlanID == "" || generation.PlanDigest == "" {
		return canonicalDeliveryEvidence{}, mismatchError("canonical generation is not admitted for target")
	}
	target, err := deployment.TargetForShareTx(ctx, tx, targetID)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical target: %v", err)
	}
	if target.TargetID != targetID || target.ProjectID != request.ProjectID || target.Environment != request.Environment {
		return canonicalDeliveryEvidence{}, mismatchError("canonical delivery target scope differs from publication request")
	}

	seal, err := deployment.SnapshotSealTx(ctx, tx, generation.SnapshotSealID)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical snapshot seal: %v", err)
	}
	if seal.SealID != generation.SnapshotSealID || seal.CandidateID != generation.CandidateID || seal.AttemptID == "" || seal.PlanDigest != generation.PlanDigest || seal.CompatibilityDigest == "" || seal.PhysicalPoolID == "" || seal.CatalogID == "" || seal.CatalogDatabase == "" || seal.CatalogUUID == "" || seal.DuckLakeSnapshotID <= 0 || seal.QualifiedAt.IsZero() {
		return canonicalDeliveryEvidence{}, mismatchError("canonical snapshot seal is not exact generation evidence")
	}
	if request.SnapshotID > 0 && request.SnapshotID != seal.DuckLakeSnapshotID {
		return canonicalDeliveryEvidence{}, mismatchError("publication snapshot differs from canonical seal")
	}

	// Resolve the immutable physical-pool admission by the exact compatibility
	// digest carried by the seal. No latest-admission or process-global pool is
	// accepted.
	admission, err := physicalpoolpostgres.New(tx).LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(seal.PhysicalPoolID), seal.CompatibilityDigest)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical physical-pool admission: %v", err)
	}
	if admission.Pool.ID.String() != seal.PhysicalPoolID || admission.Admission.PoolID.String() != seal.PhysicalPoolID || admission.Admission.CompatibilityDigest != seal.CompatibilityDigest {
		return canonicalDeliveryEvidence{}, mismatchError("canonical physical-pool admission differs from snapshot seal")
	}

	// Catalog identity is a separate immutable authority, but it is read from
	// this same control transaction. This prevents a pool admission from being
	// paired with a catalog database/UUID belonging to another namespace.
	catalog, err := ducklakepostgres.LoadCatalog(ctx, tx, seal.PhysicalPoolID)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical catalog identity: %v", err)
	}
	if catalog.PhysicalPoolID != seal.PhysicalPoolID || catalog.CatalogID != seal.CatalogID || catalog.CatalogDatabase != seal.CatalogDatabase || catalog.CatalogUUID != seal.CatalogUUID {
		return canonicalDeliveryEvidence{}, mismatchError("canonical catalog identity differs from snapshot seal")
	}

	attempt, err := deployment.BuildAttemptTx(ctx, tx, seal.AttemptID)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical build attempt: %v", err)
	}
	if attempt.AttemptID != seal.AttemptID || attempt.State != deploymentpostgres.AttemptCommitted || attempt.SnapshotID != seal.DuckLakeSnapshotID || attempt.PhysicalPoolID != seal.PhysicalPoolID || attempt.PlanID != generation.PlanID || attempt.CandidateID != generation.CandidateID || attempt.PlanDigest != generation.PlanDigest || attempt.FencingEpoch <= 0 || len(attempt.CommitMarker) == 0 {
		return canonicalDeliveryEvidence{}, mismatchError("canonical build attempt is not exact committed evidence")
	}

	publication, err := deployment.HistoricalCommittedPublicationTx(ctx, tx, request.GenerationID)
	if err != nil {
		return canonicalDeliveryEvidence{}, unavailableError("load canonical committed publication: %v", err)
	}
	if publication.State != "committed" || publication.GenerationID != generation.GenerationID || publication.TargetID != targetID || publication.CandidateID != generation.CandidateID || publication.SnapshotSealID != seal.SealID || publication.CommittedAt.IsZero() || publication.ResultTargetRevision <= publication.ExpectedTargetRevision {
		return canonicalDeliveryEvidence{}, mismatchError("canonical delivery publication is not exact generation evidence")
	}
	if request.Source == "refresh" && request.TargetRevision > 0 && (publication.ExpectedTargetRevision != request.TargetRevision || publication.ResultTargetRevision != request.TargetRevision+1) {
		return canonicalDeliveryEvidence{}, mismatchError("canonical publication target revision differs from refresh request")
	}
	return canonicalDeliveryEvidence{Target: target, Generation: generation, Seal: seal, Attempt: attempt, Publication: publication, Admission: admission, Catalog: catalog}, nil
}

func unavailableError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", refreshpostgres.ErrPublicationIdentityUnavailable, fmt.Sprintf(format, args...))
}

func mismatchError(format string, args ...any) error {
	return fmt.Errorf("%w: %w: %s", refreshpostgres.ErrConflict, refreshpostgres.ErrPublicationIdentityMismatch, fmt.Sprintf(format, args...))
}

func (v *PostgresCanonicalVerifierAdapter) VerifyCanonicalRefreshTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) (refreshpostgres.PublicationInput, error) {
	if v == nil || v.Deployment == nil || tx == nil {
		return refreshpostgres.PublicationInput{}, unavailableError("canonical deployment verifier is unavailable")
	}
	if err := job.Validate(); err != nil || job.PipelinePlan == nil || result.PlanID == "" || result.ServingStateID == "" || result.SnapshotID <= 0 {
		return refreshpostgres.PublicationInput{}, refreshrun.ErrLeaseLost
	}
	evidence, err := loadCanonicalDeliveryEvidence(ctx, tx, v.Deployment, v.TargetID, refreshpostgres.PostgresPublicationIdentityRequest{
		ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment,
		GenerationID: result.ServingStateID, SemanticModelID: job.SemanticModelID.String(),
		PipelineID: job.PipelineID.String(), RunID: job.RunID, SnapshotID: result.SnapshotID,
		Source: "refresh", TargetRevision: job.TargetRevision,
	})
	if err != nil {
		return refreshpostgres.PublicationInput{}, err
	}
	if evidence.Generation.GenerationID != result.ServingStateID || evidence.Generation.PlanID != result.PlanID || evidence.Generation.ServingArtifactDigest != job.PipelinePlan.ArtifactDigest {
		return refreshpostgres.PublicationInput{}, mismatchError("canonical generation evidence differs from refresh job")
	}
	plan, err := v.Deployment.PlanTx(ctx, tx, evidence.Generation.PlanID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, unavailableError("load canonical plan: %v", err)
	}
	if plan.TargetID != v.TargetID || plan.PlanDigest != evidence.Generation.PlanDigest || plan.ArtifactDigest != job.PipelinePlan.ArtifactDigest {
		return refreshpostgres.PublicationInput{}, mismatchError("canonical plan evidence differs from refresh job")
	}
	richPlan, err := plan.RichPlan()
	if err != nil {
		return refreshpostgres.PublicationInput{}, unavailableError("load canonical rich plan: %v", err)
	}
	boundPipeline := richPlan.PipelinePlan
	if richPlan.Digest != plan.PlanDigest || richPlan.BaseGenerationID != job.Identity.GenerationID || boundPipeline == nil ||
		boundPipeline.Digest != job.PipelinePlan.Digest || boundPipeline.ID != job.PipelinePlan.ID ||
		boundPipeline.ProjectID != job.Identity.ProjectID.String() || boundPipeline.Environment != job.Identity.Environment ||
		boundPipeline.PipelineID != job.PipelineID.String() || boundPipeline.SemanticModelID != job.SemanticModelID.String() ||
		boundPipeline.ServingGenerationID != job.Identity.GenerationID || boundPipeline.ArtifactDigest != job.PipelinePlan.ArtifactDigest {
		return refreshpostgres.PublicationInput{}, mismatchError("embedded pipeline plan evidence differs from refresh job")
	}
	if evidence.Seal.DuckLakeSnapshotID != result.SnapshotID || evidence.Seal.PlanDigest != plan.PlanDigest || evidence.Seal.ServingArtifactDigest != job.PipelinePlan.ArtifactDigest {
		return refreshpostgres.PublicationInput{}, mismatchError("canonical snapshot seal evidence differs from refresh job")
	}
	if evidence.Publication.ExpectedBaseGenerationID != job.Identity.GenerationID || evidence.Publication.ExpectedTargetRevision != job.TargetRevision || evidence.Publication.ResultTargetRevision != evidence.Publication.ExpectedTargetRevision+1 {
		return refreshpostgres.PublicationInput{}, mismatchError("canonical delivery publication is not current exact evidence")
	}
	activePublication, err := v.Deployment.CommittedPublicationTx(ctx, tx, result.ServingStateID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, unavailableError("load current canonical publication: %v", err)
	}
	if activePublication.PublicationID != evidence.Publication.PublicationID || evidence.Target.ActiveGenerationID != activePublication.GenerationID || evidence.Target.ActivePublicationID != activePublication.PublicationID || evidence.Target.TargetRevision != activePublication.ResultTargetRevision {
		return refreshpostgres.PublicationInput{}, mismatchError("canonical delivery target pointer differs from committed publication")
	}
	return refreshpostgres.PublicationInput{
		RunID: job.RunID, BaseGenerationID: job.Identity.GenerationID, ResultGenerationID: result.ServingStateID,
		ExpectedTargetRevision: evidence.Publication.ExpectedTargetRevision, ResultTargetRevision: evidence.Publication.ResultTargetRevision,
		PhysicalPoolID: evidence.Seal.PhysicalPoolID, CatalogID: evidence.Seal.CatalogID,
	}, nil
}
