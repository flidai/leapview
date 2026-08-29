package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

// PostgresCanonicalVerifierAdapter verifies delivery-owned plan, generation,
// snapshot-seal and build-attempt evidence through the refresh transaction.
// No snapshot id is trusted from the refresh worker alone.
type PostgresCanonicalVerifierAdapter struct {
	Deployment      *deploymentpostgres.Repository
	PhysicalPoolID  string
	CatalogID       string
	CatalogDatabase string
	CatalogUUID     string
}

func NewPostgresCanonicalVerifierAdapter(deployment *deploymentpostgres.Repository, physicalPoolID, catalogID, catalogDatabase, catalogUUID string) (*PostgresCanonicalVerifierAdapter, error) {
	if deployment == nil || strings.TrimSpace(physicalPoolID) == "" || strings.TrimSpace(catalogID) == "" || strings.TrimSpace(catalogDatabase) == "" || strings.TrimSpace(catalogUUID) == "" {
		return nil, errors.New("deployment repository, physical pool, catalog ID, catalog database and catalog UUID are required")
	}
	return &PostgresCanonicalVerifierAdapter{Deployment: deployment, PhysicalPoolID: physicalPoolID, CatalogID: catalogID, CatalogDatabase: catalogDatabase, CatalogUUID: catalogUUID}, nil
}

var _ PostgresCanonicalVerifier = (*PostgresCanonicalVerifierAdapter)(nil)

func (v *PostgresCanonicalVerifierAdapter) VerifyCanonicalRefreshTx(ctx context.Context, tx refreshpostgres.Tx, job refreshrun.JobRecord, result refreshrun.CanonicalRefreshResult) (refreshpostgres.PublicationInput, error) {
	if v == nil || v.Deployment == nil || tx == nil {
		return refreshpostgres.PublicationInput{}, errors.New("canonical deployment verifier is unavailable")
	}
	if err := job.Validate(); err != nil || job.PipelinePlan == nil || result.PlanID == "" || result.ServingStateID == "" || result.SnapshotID <= 0 {
		return refreshpostgres.PublicationInput{}, refreshrun.ErrLeaseLost
	}
	generation, err := v.Deployment.GenerationTx(ctx, tx, result.ServingStateID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("load canonical generation: %w", err)
	}
	if generation.GenerationID != result.ServingStateID || generation.PlanID != result.PlanID || generation.PlanDigest != job.PipelinePlan.Digest || generation.ServingArtifactDigest != job.PipelinePlan.ArtifactDigest || generation.CandidateID == "" || generation.SnapshotSealID == "" {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical generation evidence differs from refresh job")
	}
	target, err := v.Deployment.TargetForShareTx(ctx, tx, generation.TargetID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("load canonical target: %w", err)
	}
	if target.ProjectID != job.Identity.ProjectID.String() || target.Environment != job.Identity.Environment {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical delivery target scope differs from refresh job")
	}
	plan, err := v.Deployment.PlanTx(ctx, tx, generation.PlanID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("load canonical plan: %w", err)
	}
	if plan.TargetID != generation.TargetID || plan.PlanDigest != job.PipelinePlan.Digest || plan.ArtifactDigest != job.PipelinePlan.ArtifactDigest || job.PipelinePlan.ID != generation.PlanID || job.PipelinePlan.ProjectID != job.Identity.ProjectID.String() || job.PipelinePlan.Environment != job.Identity.Environment || job.PipelinePlan.PipelineID != job.PipelineID.String() || job.PipelinePlan.ServingGenerationID != job.Identity.GenerationID {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical plan evidence differs from refresh job")
	}
	seal, err := v.Deployment.SnapshotSealTx(ctx, tx, generation.SnapshotSealID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("load canonical snapshot seal: %w", err)
	}
	if seal.DuckLakeSnapshotID != result.SnapshotID || seal.PhysicalPoolID != v.PhysicalPoolID || seal.CatalogID != v.CatalogID || seal.CatalogDatabase != v.CatalogDatabase || seal.CatalogUUID != v.CatalogUUID || seal.PlanDigest != job.PipelinePlan.Digest || seal.ServingArtifactDigest != job.PipelinePlan.ArtifactDigest || seal.CandidateID != generation.CandidateID || seal.QualifiedAt.IsZero() {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical snapshot seal evidence differs from refresh job")
	}
	if seal.AttemptID == "" {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical snapshot seal has no build attempt")
	}
	attempt, err := v.Deployment.BuildAttemptTx(ctx, tx, seal.AttemptID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("load canonical build attempt: %w", err)
	}
	if attempt.State != deploymentpostgres.AttemptCommitted || attempt.SnapshotID != result.SnapshotID || attempt.PhysicalPoolID != v.PhysicalPoolID || attempt.PlanID != generation.PlanID || attempt.CandidateID != generation.CandidateID || attempt.PlanDigest != job.PipelinePlan.Digest || attempt.FencingEpoch <= 0 || len(attempt.CommitMarker) == 0 {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical build attempt is not exact committed evidence")
	}
	publication, err := v.Deployment.CommittedPublicationTx(ctx, tx, result.ServingStateID)
	if err != nil {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("load current committed delivery publication: %w", err)
	}
	if publication.State != "committed" || publication.GenerationID != result.ServingStateID || publication.ExpectedBaseGenerationID != job.Identity.GenerationID || publication.TargetID != generation.TargetID || publication.CandidateID != generation.CandidateID || publication.SnapshotSealID != generation.SnapshotSealID || publication.ExpectedTargetRevision != job.TargetRevision || publication.ResultTargetRevision != publication.ExpectedTargetRevision+1 || publication.CommittedAt.IsZero() {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical delivery publication is not current exact evidence")
	}
	if target.ActiveGenerationID != publication.GenerationID || target.ActivePublicationID != publication.PublicationID || target.TargetRevision != publication.ResultTargetRevision {
		return refreshpostgres.PublicationInput{}, fmt.Errorf("canonical delivery target pointer differs from committed publication (target=%s/%s/%d publication=%s/%s/%d)", target.ActiveGenerationID, target.ActivePublicationID, target.TargetRevision, publication.GenerationID, publication.PublicationID, publication.ResultTargetRevision)
	}
	return refreshpostgres.PublicationInput{RunID: job.RunID, BaseGenerationID: job.Identity.GenerationID, ResultGenerationID: result.ServingStateID, ExpectedTargetRevision: publication.ExpectedTargetRevision, ResultTargetRevision: publication.ResultTargetRevision, PhysicalPoolID: v.PhysicalPoolID, CatalogID: v.CatalogID}, nil
}
