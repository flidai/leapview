package runtimefactory

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/pkg/strictjson"
)

// NewPostgresSealedRootResolver binds one PostgreSQL delivery target to its
// exact immutable generation (or a candidate preview), snapshot seal, build
// attempt, and physical-pool admission. PostgreSQL-backed catalogs are
// relational roots: this resolver deliberately does not accept a control-plane
// database handle, read a serving-state artifact table, or resolve a catalog file.
//
// The serving-state repository has already supplied the artifact to
// RuntimeInput. The resolver binds its durable ID and digest from the
// PostgreSQL snapshot seal, while the factory performs the final
// state/artifact identity check before attach.
func NewPostgresSealedRootResolver(targetID string, delivery *deploymentpostgres.Repository, pools *physicalpoolpostgres.Repository, lineage *lineagepostgres.Repository) SealedRootResolver {
	return func(ctx context.Context, input runtimehost.RuntimeInput) (SealedServingRoot, error) {
		if delivery == nil || pools == nil || lineage == nil || !lineage.Configured() || strings.TrimSpace(targetID) == "" || targetID != strings.TrimSpace(targetID) {
			return SealedServingRoot{}, fmt.Errorf("%w: PostgreSQL durable delivery, physical-pool, or lineage repository is unavailable", ErrSealedRootUnavailable)
		}
		if strings.TrimSpace(string(input.State.ID)) == "" || strings.TrimSpace(input.Artifact.ID) == "" || input.Artifact.ID != strings.TrimSpace(input.Artifact.ID) {
			return SealedServingRoot{}, fmt.Errorf("%w: serving-state and artifact identity are required", ErrSealedRootUnavailable)
		}
		candidateInput := input.Candidate
		if candidateInput == nil {
			candidateInput = input.SealedActivationCandidate
		}
		if candidateInput != nil {
			candidateID := strings.TrimSpace(candidateInput.CandidateID)
			if candidateID == "" || candidateID != candidateInput.CandidateID {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate identity is required", ErrSealedRootUnavailable)
			}
			target, err := delivery.Target(ctx, targetID)
			if err != nil {
				return SealedServingRoot{}, fmt.Errorf("%w: delivery target: %v", ErrSealedRootUnavailable, err)
			}
			if target.TargetID != targetID || target.ProjectID != input.State.ProjectID.String() || target.Environment != string(input.State.Environment) {
				return SealedServingRoot{}, fmt.Errorf("%w: delivery target is not bound to requested project and environment", ErrSealedRootMismatch)
			}
			candidate, err := delivery.Candidate(ctx, candidateID)
			if err != nil {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate: %v", ErrSealedRootUnavailable, err)
			}
			if candidate.SnapshotSealID == "" {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate has no snapshot seal", ErrSealedRootUnavailable)
			}
			seal, err := delivery.SnapshotSeal(ctx, candidate.SnapshotSealID)
			if err != nil {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate snapshot seal: %v", ErrSealedRootUnavailable, err)
			}
			attempt, err := delivery.BuildAttempt(ctx, seal.AttemptID)
			if err != nil {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate build attempt: %v", ErrSealedRootUnavailable, err)
			}
			plan, err := delivery.Plan(ctx, candidate.PlanID)
			if err != nil {
				return SealedServingRoot{}, fmt.Errorf("%w: candidate delivery plan: %v", ErrSealedRootUnavailable, err)
			}
			marker, err := parsePostgresCommitMarker(attempt.CommitMarker)
			if err != nil {
				return SealedServingRoot{}, fmt.Errorf("%w: committed build-attempt marker is invalid", ErrSealedRootMismatch)
			}
			if err := validatePostgresCandidateTuple(targetID, plan, candidate, seal, attempt, marker, input); err != nil {
				return SealedServingRoot{}, err
			}
			if err := validatePostgresLineageBinding(ctx, lineage, targetID, input.State.ProjectID.String(), marker.GenerationID); err != nil {
				return SealedServingRoot{}, err
			}
			contract, err := loadPostgresPoolContract(ctx, pools, seal.PhysicalPoolID, seal.CompatibilityDigest)
			if err != nil {
				return SealedServingRoot{}, err
			}
			return postgresSealedServingRoot(targetID, marker.DeliveryID, marker.GenerationID, candidate, seal, attempt, contract)
		}

		target, err := delivery.Target(ctx, targetID)
		if err != nil {
			return SealedServingRoot{}, fmt.Errorf("%w: delivery target: %v", ErrSealedRootUnavailable, err)
		}
		if target.TargetID != targetID || target.ProjectID != input.State.ProjectID.String() || target.Environment != string(input.State.Environment) {
			return SealedServingRoot{}, fmt.Errorf("%w: delivery target is not bound to requested project and environment", ErrSealedRootMismatch)
		}

		if target.ActiveGenerationID == "" {
			return SealedServingRoot{}, fmt.Errorf("%w: delivery target has no active generation", ErrSealedRootUnavailable)
		}
		if target.ActivePublicationID == "" {
			return SealedServingRoot{}, fmt.Errorf("%w: active delivery pointer has no publication", ErrSealedRootUnavailable)
		}
		publication, err := delivery.Publication(ctx, target.ActivePublicationID)
		if err != nil {
			return SealedServingRoot{}, fmt.Errorf("%w: active delivery publication: %v", ErrSealedRootUnavailable, err)
		}
		if publication.PublicationID != target.ActivePublicationID || publication.State != "committed" || publication.TargetID != targetID || publication.GenerationID != target.ActiveGenerationID || publication.CommittedAt.IsZero() || publication.ResultTargetRevision <= 0 || publication.ResultTargetRevision != target.TargetRevision {
			return SealedServingRoot{}, fmt.Errorf("%w: active delivery pointer is not one committed publication", ErrSealedRootMismatch)
		}
		generation, err := delivery.Generation(ctx, target.ActiveGenerationID)
		if err != nil {
			return SealedServingRoot{}, fmt.Errorf("%w: active delivery generation: %v", ErrSealedRootUnavailable, err)
		}
		if generation.GenerationID != target.ActiveGenerationID || generation.GenerationID != string(input.State.ID) || generation.TargetID != targetID {
			return SealedServingRoot{}, fmt.Errorf("%w: active delivery generation is not bound to requested serving state", ErrSealedRootMismatch)
		}
		candidate, err := delivery.Candidate(ctx, generation.CandidateID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		if candidate.SnapshotSealID == "" || candidate.TargetID != targetID || !postgresCandidateQualified(candidate) {
			return SealedServingRoot{}, fmt.Errorf("%w: active delivery candidate is not qualified", ErrSealedRootMismatch)
		}
		seal, err := delivery.SnapshotSeal(ctx, generation.SnapshotSealID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		attempt, err := delivery.BuildAttempt(ctx, seal.AttemptID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		plan, err := delivery.Plan(ctx, generation.PlanID)
		if err != nil {
			return SealedServingRoot{}, err
		}
		marker, err := parsePostgresCommitMarker(attempt.CommitMarker)
		if err != nil {
			return SealedServingRoot{}, fmt.Errorf("%w: committed build-attempt marker is invalid", ErrSealedRootMismatch)
		}
		if err := validatePostgresDeliveryTuple(publication, generation, plan, candidate, seal, attempt, marker, input); err != nil {
			return SealedServingRoot{}, err
		}
		if err := validatePostgresLineageBinding(ctx, lineage, targetID, input.State.ProjectID.String(), generation.GenerationID); err != nil {
			return SealedServingRoot{}, err
		}
		contract, err := loadPostgresPoolContract(ctx, pools, seal.PhysicalPoolID, seal.CompatibilityDigest)
		if err != nil {
			return SealedServingRoot{}, err
		}
		return postgresSealedServingRoot(targetID, marker.DeliveryID, generation.GenerationID, candidate, seal, attempt, contract)
	}
}

// validatePostgresLineageBinding verifies that the immutable compiler graph
// selected for this project and target is bound to the exact generation being
// served. TargetID is the canonical target scope; marker/root DeliveryID is
// build provenance and is not a serving-scope selector.
func validatePostgresLineageBinding(ctx context.Context, lineage *lineagepostgres.Repository, targetID, projectID, generationID string) error {
	if lineage == nil || !lineage.Configured() {
		return fmt.Errorf("%w: PostgreSQL lineage repository is unavailable", ErrSealedRootUnavailable)
	}
	if _, err := lineage.LoadBoundForProject(ctx, projectID, targetID, generationID); err != nil {
		if errors.Is(err, lineagepostgres.ErrNotFound) || errors.Is(err, lineagepostgres.ErrTampered) || errors.Is(err, lineagepostgres.ErrInvalid) || errors.Is(err, lineagepostgres.ErrConflict) {
			return fmt.Errorf("%w: lineage binding: %w", ErrSealedRootMismatch, err)
		}
		return fmt.Errorf("%w: lineage binding: %w", ErrSealedRootUnavailable, err)
	}
	return nil
}

func postgresCandidateQualified(candidate deploymentpostgres.DeliveryCandidate) bool {
	return candidate.Status == "qualified" || candidate.Status == "admitted"
}

// PostgreSQL stores commit_marker in jsonb, whose textual projection is
// semantically canonical but does not preserve the field order emitted by
// ducklake.CommitMarker.CanonicalJSON. Decode strictly and normalize here;
// unknown fields and malformed values remain fail-closed while avoiding an
// ordering false negative caused by the relational representation.
func parsePostgresCommitMarker(raw []byte) (ducklake.CommitMarker, error) {
	if marker, err := ducklake.ParseCommitMarker(string(raw)); err == nil {
		return marker, nil
	}
	var marker ducklake.CommitMarker
	if err := strictjson.DecodeWithOptions(raw, &marker, strictjson.Options{MaxBytes: ducklake.MaxCommitMarkerBytes}); err != nil {
		return ducklake.CommitMarker{}, err
	}
	return marker.Normalize()
}

// validatePostgresDeliveryTuple rejects a seal that is not backed by the
// exact committed publication, generation, plan, attempt, and candidate
// selected by the delivery authority.
func validatePostgresDeliveryTuple(publication deploymentpostgres.DeliveryPublication, generation deploymentpostgres.DeliveryGeneration, plan deploymentpostgres.DeliveryPlan, candidate deploymentpostgres.DeliveryCandidate, seal deploymentpostgres.SnapshotSeal, attempt deploymentpostgres.DeliveryBuildAttempt, marker ducklake.CommitMarker, input runtimehost.RuntimeInput) error {
	targetID := publication.TargetID
	if publication.PublicationID == "" || publication.State != "committed" || publication.GenerationID != generation.GenerationID || publication.CandidateID != candidate.CandidateID || publication.SnapshotSealID != seal.SealID || publication.ExpectedTargetRevision <= 0 || publication.ResultTargetRevision != publication.ExpectedTargetRevision+1 {
		return fmt.Errorf("%w: active delivery pointer is not one committed publication", ErrSealedRootMismatch)
	}
	if err := validatePostgresCandidateTuple(targetID, plan, candidate, seal, attempt, marker, input); err != nil {
		return err
	}
	if candidate.CandidateID == "" || candidate.TargetID != targetID || !postgresCandidateQualified(candidate) || candidate.SnapshotSealID != seal.SealID || candidate.ArtifactDigest != seal.ServingArtifactDigest || candidate.QualificationDigest == "" {
		return fmt.Errorf("%w: candidate and seal are not one qualified tuple", ErrSealedRootMismatch)
	}
	if generation.TargetID != targetID || generation.CandidateID != candidate.CandidateID || generation.SnapshotSealID != seal.SealID || generation.PlanID != plan.PlanID || generation.PlanID != candidate.PlanID || generation.PlanDigest != plan.PlanDigest || generation.ArtifactRoot != seal.ArtifactRoot || generation.ArtifactRootDigest != seal.ArtifactRootDigest || generation.ServingArtifactDigest != seal.ServingArtifactDigest || generation.CompiledGraphDigest != seal.CompiledGraphDigest || generation.CompiledConfigDigest != seal.CompiledConfigDigest || generation.SecurityDomainFingerprint != seal.SecurityDomainFingerprint {
		return fmt.Errorf("%w: generation candidate and seal identities differ", ErrSealedRootMismatch)
	}
	if plan.TargetID != targetID || plan.PlanDigest != seal.PlanDigest || plan.CompiledGraphDigest != seal.CompiledGraphDigest || plan.CompiledConfigDigest != seal.CompiledConfigDigest || plan.SecurityDomainFingerprint != seal.SecurityDomainFingerprint || plan.ArtifactDigest != seal.ServingArtifactDigest {
		return fmt.Errorf("%w: plan, generation, and seal are not one verified tuple", ErrSealedRootMismatch)
	}
	if marker.GenerationID != generation.GenerationID {
		return fmt.Errorf("%w: committed build-attempt marker is not bound to active generation", ErrSealedRootMismatch)
	}
	return nil
}

func validatePostgresCandidateTuple(targetID string, plan deploymentpostgres.DeliveryPlan, candidate deploymentpostgres.DeliveryCandidate, seal deploymentpostgres.SnapshotSeal, attempt deploymentpostgres.DeliveryBuildAttempt, marker ducklake.CommitMarker, input runtimehost.RuntimeInput) error {
	if marker.DeliveryID == "" || marker.GenerationID == "" || marker.GenerationID != string(input.State.ID) || marker.AttemptID != attempt.AttemptID || marker.Project != input.State.ProjectID.String() || marker.Environment != string(input.State.Environment) || marker.PhysicalPoolID != seal.PhysicalPoolID || marker.RequestDigest != seal.RequestDigest || marker.PlanDigest != seal.PlanDigest || marker.LeaseEpoch != attempt.FencingEpoch {
		return fmt.Errorf("%w: committed build-attempt marker is not bound to requested generation", ErrSealedRootMismatch)
	}
	if candidate.CandidateID == "" || candidate.TargetID != targetID || candidate.PlanID != plan.PlanID || !postgresCandidateQualified(candidate) || candidate.SnapshotSealID != seal.SealID || seal.CandidateID != candidate.CandidateID || candidate.ArtifactDigest != seal.ServingArtifactDigest || candidate.QualificationDigest == "" {
		return fmt.Errorf("%w: candidate and seal are not one qualified tuple", ErrSealedRootMismatch)
	}
	if err := platformdigest.ValidateSHA256Identity(candidate.QualificationDigest); err != nil {
		return fmt.Errorf("%w: qualification digest is invalid: %v", ErrSealedRootUnavailable, err)
	}
	candidateInput := input.Candidate
	if candidateInput == nil {
		candidateInput = input.SealedActivationCandidate
	}
	if candidateInput != nil && candidateInput.RuntimeVersion != "" && candidateInput.RuntimeVersion != seal.RuntimeVersion {
		return fmt.Errorf("%w: candidate runtime version differs from snapshot seal", ErrSealedRootMismatch)
	}
	if plan.PlanID == "" || plan.TargetID != targetID || plan.PlanDigest != seal.PlanDigest || plan.CompiledGraphDigest != seal.CompiledGraphDigest || plan.CompiledConfigDigest != seal.CompiledConfigDigest || plan.SecurityDomainFingerprint != seal.SecurityDomainFingerprint || plan.ArtifactDigest != seal.ServingArtifactDigest {
		return fmt.Errorf("%w: plan, candidate, and seal are not one verified tuple", ErrSealedRootMismatch)
	}
	if err := platformdigest.ValidateSHA256Identity(plan.QualificationDigest); err != nil {
		return fmt.Errorf("%w: plan qualification digest is invalid: %v", ErrSealedRootUnavailable, err)
	}
	if input.Artifact.Digest == "" || seal.ServingArtifactID == "" || seal.ServingArtifactID != input.Artifact.ID || candidate.ArtifactDigest != input.Artifact.Digest || seal.ServingArtifactDigest != input.Artifact.Digest {
		return fmt.Errorf("%w: durable serving artifact digest differs from requested artifact", ErrSealedRootMismatch)
	}
	if attempt.AttemptID == "" || seal.AttemptID != attempt.AttemptID || attempt.CandidateID != candidate.CandidateID || attempt.PlanID != candidate.PlanID || attempt.State != deploymentpostgres.AttemptCommitted || attempt.PhysicalPoolID != seal.PhysicalPoolID || attempt.SnapshotID != seal.DuckLakeSnapshotID || attempt.RequestDigest != seal.RequestDigest || attempt.PlanDigest != seal.PlanDigest || attempt.Namespace != seal.RelationNamespace || attempt.FencingEpoch <= 0 || len(attempt.CommitMarker) == 0 {
		return fmt.Errorf("%w: snapshot seal is not backed by exact committed build-attempt evidence", ErrSealedRootMismatch)
	}
	expectedNamespace, err := deployment.DeriveRelationNamespace(deployment.RelationNamespaceInput{
		CandidateID: candidate.CandidateID, AttemptID: attempt.AttemptID, FencingEpoch: attempt.FencingEpoch,
	})
	if err != nil || seal.RelationNamespace != expectedNamespace {
		return fmt.Errorf("%w: snapshot seal relation namespace differs from candidate attempt fence", ErrSealedRootMismatch)
	}
	if seal.QualifiedAt.IsZero() || seal.PhysicalPoolID == "" || seal.CatalogDatabase == "" || seal.CatalogID == "" || seal.CatalogUUID == "" || seal.RelationNamespace == "" || seal.ObjectRoot == "" || seal.ArtifactRoot == "" || seal.TenantDomain == "" || seal.Region == "" || seal.EncryptionDomain == "" || seal.ObjectNamespace == "" || seal.CatalogVersion <= 0 || seal.DuckLakeSnapshotID <= 0 || seal.ClosureDigest == "" || seal.RuntimeVersion == "" {
		return fmt.Errorf("%w: PostgreSQL snapshot-seal identity is incomplete", ErrSealedRootUnavailable)
	}
	for name, value := range map[string]string{
		"relation manifest digest": seal.RelationManifestDigest, "closure digest": seal.ClosureDigest, "object root digest": seal.ObjectRootDigest,
		"artifact root digest": seal.ArtifactRootDigest, "compiled graph digest": seal.CompiledGraphDigest,
		"compiled config digest": seal.CompiledConfigDigest, "security fingerprint": seal.SecurityDomainFingerprint,
		"request digest": seal.RequestDigest, "plan digest": seal.PlanDigest,
		"compatibility digest": seal.CompatibilityDigest, "serving artifact digest": seal.ServingArtifactDigest,
	} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("%w: %s is invalid: %v", ErrSealedRootUnavailable, name, err)
		}
	}
	for name, value := range map[string]string{
		"DuckDB version": seal.DuckDBVersion, "runtime version": seal.RuntimeVersion, "DuckLake extension version": seal.DuckLakeExtensionVersion,
		"DuckLake specification version": seal.DuckLakeSpecVersion, "catalog schema version": seal.CatalogSchemaVersion,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: %s is unavailable", ErrSealedRootUnavailable, name)
		}
	}
	return nil
}

func loadPostgresPoolContract(ctx context.Context, pools *physicalpoolpostgres.Repository, poolID, compatibilityDigest string) (*ducklake.PoolContract, error) {
	if pools == nil {
		return nil, fmt.Errorf("%w: physical-pool admission repository is unavailable", ErrSealedRootUnavailable)
	}
	if strings.TrimSpace(poolID) == "" || strings.TrimSpace(compatibilityDigest) == "" {
		return nil, fmt.Errorf("%w: physical-pool admission identity is incomplete", ErrSealedRootUnavailable)
	}
	admission, err := pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(poolID), compatibilityDigest)
	if err != nil {
		return nil, fmt.Errorf("%w: physical-pool admission: %v", ErrSealedRootUnavailable, err)
	}
	contract := &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
	if contract.Pool.ID.String() != poolID || contract.Tuple != admission.Pool.Compatibility {
		return nil, fmt.Errorf("%w: physical-pool admission identity differs from snapshot seal", ErrSealedRootMismatch)
	}
	return contract, nil
}

func postgresSealedServingRoot(targetID, deliveryID, servingStateID string, candidate deploymentpostgres.DeliveryCandidate, seal deploymentpostgres.SnapshotSeal, attempt deploymentpostgres.DeliveryBuildAttempt, contract *ducklake.PoolContract) (SealedServingRoot, error) {
	dataPath, err := contract.Pool.DataPath()
	if err != nil {
		return SealedServingRoot{}, fmt.Errorf("%w: physical-pool DATA_PATH: %v", ErrSealedRootUnavailable, err)
	}
	metadataSchema := ducklake.MetadataSchemaForPool(seal.PhysicalPoolID)
	return SealedServingRoot{
		TargetID: targetID, GenerationID: servingStateID, CandidateID: candidate.CandidateID, AttemptID: attempt.AttemptID, SealID: seal.SealID,
		ClosureDigest: seal.ClosureDigest, QualificationDigest: candidate.QualificationDigest,
		PhysicalPoolID: seal.PhysicalPoolID, Compatibility: contract.Tuple, PoolContract: contract,
		ServingStateID: servingStateID, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest,
		CatalogDatabase: seal.CatalogDatabase, CatalogID: seal.CatalogID, CatalogUUID: seal.CatalogUUID,
		CatalogMetadataSchema: metadataSchema, CatalogSnapshotID: seal.DuckLakeSnapshotID, DataPath: dataPath,
		CatalogVersion: strconv.FormatInt(seal.CatalogVersion, 10), CatalogVersionNumber: seal.CatalogVersion,
		DuckDBVersion: seal.DuckDBVersion, DuckLakeExtensionVersion: seal.DuckLakeExtensionVersion,
		DuckLakeSpecVersion: seal.DuckLakeSpecVersion, CatalogSchemaVersion: seal.CatalogSchemaVersion,
		RelationNamespace: seal.RelationNamespace, RelationManifestDigest: seal.RelationManifestDigest,
		ObjectRoot: seal.ObjectRoot, ObjectRootDigest: seal.ObjectRootDigest, ArtifactRoot: seal.ArtifactRoot,
		ArtifactRootDigest: seal.ArtifactRootDigest, CompiledGraphDigest: seal.CompiledGraphDigest,
		CompiledConfigDigest: seal.CompiledConfigDigest, RequestDigest: seal.RequestDigest, PlanDigest: seal.PlanDigest,
		TenantDomain: seal.TenantDomain, Region: seal.Region, EncryptionDomain: seal.EncryptionDomain,
		ObjectNamespace: seal.ObjectNamespace, DeliveryID: deliveryID, FencingEpoch: attempt.FencingEpoch,
		CompatibilityDigest: seal.CompatibilityDigest, RuntimeVersion: seal.RuntimeVersion,
		SecurityDomainFingerprint: seal.SecurityDomainFingerprint,
	}, nil
}
