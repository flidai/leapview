package deploymentpostgres

import (
	"context"
	"fmt"
	"strings"

	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

// nativePublishedDataVersionReader is the least-authority read surface needed
// to establish the freshness baseline. The full native delivery reader
// satisfies this interface, while unrelated plan/build command reads remain
// outside the resolver's authority.
type nativePublishedDataVersionReader interface {
	OperatorSnapshot(context.Context, string) (nativepostgres.DeliveryOperatorSnapshot, error)
	LoadGeneration(context.Context, string) (nativepostgres.DeliveryGeneration, error)
	LoadCandidate(context.Context, string) (nativepostgres.DeliveryCandidate, error)
	LoadSnapshotSeal(context.Context, string) (nativepostgres.SnapshotSeal, error)
	LoadBuildAttempt(context.Context, string) (nativepostgres.DeliveryBuildAttempt, error)
	LoadPublication(context.Context, string) (nativepostgres.DeliveryPublication, error)
}

// NewNativePublishedDataVersionResolver binds refresh freshness to the
// target-owned native publication pointer. The resolver deliberately reads
// every immutable delivery row through the native delivery reader: a serving
// generation, candidate, seal, and build attempt by themselves do not prove
// that the generation was committed and selected for serving.
func NewNativePublishedDataVersionResolver(
	reader nativePublishedDataVersionReader,
	targetID string,
) refreshmodule.PublishedDataVersionResolver {
	return func(ctx context.Context, identity projectgraph.ServingIdentity) (refreshmodule.PublishedDataVersion, bool, error) {
		if reader == nil || targetID == "" || targetID != strings.TrimSpace(targetID) || len(targetID) > 255 {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication data version is unavailable")
		}
		if err := identity.Validate(); err != nil {
			return refreshmodule.PublishedDataVersion{}, false, err
		}

		operator, err := reader.OperatorSnapshot(ctx, targetID)
		if err != nil {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("resolve native publication target: %w", err)
		}
		if operator.TargetID != targetID || operator.ProjectID != identity.ProjectID.String() || operator.Environment != identity.Environment || operator.TargetRevision <= 0 {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication target scope is inconsistent with the active refresh identity")
		}
		if operator.ActiveGenerationID == "" || operator.ActivePublicationID == "" {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication active pointer is unavailable")
		}
		if operator.ActiveGenerationID != identity.GenerationID {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication generation does not match the active refresh identity")
		}

		generation, err := reader.LoadGeneration(ctx, operator.ActiveGenerationID)
		if err != nil {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("resolve native publication generation: %w", err)
		}
		if generation.GenerationID != operator.ActiveGenerationID || generation.TargetID != targetID || generation.CandidateID == "" || generation.SnapshotSealID == "" || generation.PlanID == "" {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication generation evidence is inconsistent")
		}

		candidate, err := reader.LoadCandidate(ctx, generation.CandidateID)
		if err != nil {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("resolve native publication candidate: %w", err)
		}
		// Only qualified/admitted candidates carry a seal in the native schema.
		// Checking status here prevents a malformed/rejected candidate from
		// establishing a freshness baseline despite matching foreign keys.
		if candidate.CandidateID != generation.CandidateID || candidate.TargetID != targetID || candidate.PlanID != generation.PlanID || candidate.SnapshotSealID != generation.SnapshotSealID || candidate.AttemptID == "" || (candidate.Status != "qualified" && candidate.Status != "admitted") {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication candidate evidence is inconsistent")
		}

		seal, err := reader.LoadSnapshotSeal(ctx, generation.SnapshotSealID)
		if err != nil {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("resolve native publication snapshot seal: %w", err)
		}
		if seal.SealID != generation.SnapshotSealID || seal.CandidateID != candidate.CandidateID || seal.AttemptID != candidate.AttemptID || seal.PlanDigest != generation.PlanDigest || seal.DuckLakeSnapshotID <= 0 || seal.QualifiedAt.IsZero() {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication snapshot seal evidence is inconsistent")
		}

		attempt, err := reader.LoadBuildAttempt(ctx, seal.AttemptID)
		if err != nil {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("resolve native publication build attempt: %w", err)
		}
		if attempt.AttemptID != seal.AttemptID || attempt.PlanID != generation.PlanID || attempt.CandidateID != candidate.CandidateID || attempt.State != nativepostgres.AttemptCommitted || attempt.SnapshotID <= 0 || attempt.SnapshotID != seal.DuckLakeSnapshotID || attempt.PlanDigest != generation.PlanDigest {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication build attempt evidence is inconsistent")
		}

		publication, err := reader.LoadPublication(ctx, operator.ActivePublicationID)
		if err != nil {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("resolve native publication: %w", err)
		}
		if publication.PublicationID != operator.ActivePublicationID || publication.TargetID != targetID || publication.GenerationID != generation.GenerationID || publication.CandidateID != candidate.CandidateID || publication.SnapshotSealID != seal.SealID || publication.State != "committed" || publication.ExpectedTargetRevision <= 0 || publication.ResultTargetRevision != operator.TargetRevision || publication.ResultTargetRevision != publication.ExpectedTargetRevision+1 || publication.CommittedAt.IsZero() {
			return refreshmodule.PublishedDataVersion{}, false, fmt.Errorf("native publication evidence is inconsistent")
		}

		return refreshmodule.PublishedDataVersion{
			SnapshotID:  attempt.SnapshotID,
			RefreshedAt: publication.CommittedAt.UTC(),
		}, true, nil
	}
}
