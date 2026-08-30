package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

// postgresDeliveryStartupAuthority is the read-only portion of the native
// delivery authority needed to prove the active target pointer. Keeping this
// interface local prevents readiness from acquiring a writer or from opening
// a legacy database/sql repository.
// postgresDeliveryStartupServingStates is the immutable serving evidence
// surface rooted at the delivery generation ID. It deliberately excludes
// activation/mutation methods.
type postgresDeliveryStartupServingStates interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

// postgresDeliveryStartupPhysicalPools proves that the exact immutable pool
// tuple referenced by a sealed generation was admitted. The digest comes from
// the seal; no pool is selected from configuration or by recency.
type postgresDeliveryStartupPhysicalPools interface {
	LoadAdmissionContractByCompatibilityDigest(context.Context, physicalpool.PoolID, string) (physicalpool.AdmissionContract, error)
}

type postgresDeliveryStartupCheckConfig struct {
	TargetID    string
	Environment servingstate.Environment
	ReadClaim   func(context.Context) (projectgraph.ResourceID, bool, error)
	Delivery    appdeploymentpostgres.StartupReader
	Serving     postgresDeliveryStartupServingStates
	Physical    postgresDeliveryStartupPhysicalPools
}

// newPostgresDeliveryStartupCheck builds the production readiness callback.
// A target with neither a durable claim nor a target row is the explicitly
// supported fresh-install state: administration may start before first
// bootstrap. Every partial or active state is checked against the exact
// target-owned generation/publication/seal/serving tuple.
func newPostgresDeliveryStartupCheck(config postgresDeliveryStartupCheckConfig) (func(context.Context) error, error) {
	if strings.TrimSpace(config.TargetID) == "" {
		return nil, errors.New("PostgreSQL delivery startup target id is required")
	}
	if err := servingstate.ValidateEnvironment(config.Environment); err != nil {
		return nil, fmt.Errorf("PostgreSQL delivery startup environment: %w", err)
	}
	if config.ReadClaim == nil {
		return nil, errors.New("PostgreSQL delivery startup project claim reader is required")
	}
	if config.Delivery == nil {
		return nil, errors.New("PostgreSQL delivery startup authority is required")
	}
	if config.Serving == nil {
		return nil, errors.New("PostgreSQL delivery startup serving-state authority is required")
	}
	if config.Physical == nil {
		return nil, errors.New("PostgreSQL delivery startup physical-pool authority is required")
	}

	return func(ctx context.Context) error {
		claimedProject, claimFound, err := config.ReadClaim(ctx)
		if err != nil {
			return fmt.Errorf("delivery startup project claim: %w", err)
		}
		if claimFound {
			if err := claimedProject.Validate(); err != nil {
				return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupClaimTargetPartial)
			}
		}

		target, targetErr := config.Delivery.Target(ctx, config.TargetID)
		targetFound := targetErr == nil
		if targetErr != nil && !postgresDeliveryStartupNotFound(targetErr) {
			return fmt.Errorf("delivery startup target: %w", targetErr)
		}
		if !claimFound && !targetFound {
			// No durable scope exists yet. This is the only healthy fresh-target
			// state and must not be confused with a broken partial migration.
			return nil
		}
		if claimFound != targetFound {
			return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupClaimTargetPartial)
		}

		scope := config.TargetID
		if target.TargetID != config.TargetID || target.ProjectID != claimedProject.String() || target.Environment != string(config.Environment) {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupTargetIdentityMismatch)
		}
		if target.TargetRevision <= 0 {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingTargetRevision)
		}

		generationID := strings.TrimSpace(target.ActiveGenerationID)
		publicationID := strings.TrimSpace(target.ActivePublicationID)
		if (generationID == "") != (publicationID == "") {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupActivePointerMismatch)
		}
		if generationID == "" {
			// A claimed target with no active generation is a valid
			// administrable pre-publication state.
			return nil
		}

		generation, err := config.Delivery.Generation(ctx, generationID)
		if err != nil {
			if postgresDeliveryStartupNotFound(err) {
				return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingServingGeneration)
			}
			return fmt.Errorf("delivery startup generation: %w", err)
		}
		if generation.GenerationID != generationID || generation.TargetID != target.TargetID || strings.TrimSpace(generation.CandidateID) == "" || strings.TrimSpace(generation.PlanID) == "" || strings.TrimSpace(generation.SnapshotSealID) == "" || strings.TrimSpace(generation.ServingArtifactDigest) == "" {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingServingGeneration)
		}

		publication, err := config.Delivery.Publication(ctx, publicationID)
		if err != nil {
			if postgresDeliveryStartupNotFound(err) {
				return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingPublication)
			}
			return fmt.Errorf("delivery startup publication: %w", err)
		}
		if publication.PublicationID != publicationID || publication.TargetID != target.TargetID || publication.GenerationID != generationID || publication.CandidateID != generation.CandidateID || publication.SnapshotSealID != generation.SnapshotSealID || publication.State != "committed" || publication.ExpectedTargetRevision <= 0 || publication.ResultTargetRevision != target.TargetRevision || publication.ResultTargetRevision != publication.ExpectedTargetRevision+1 {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupActivePointerMismatch)
		}

		stateID := servingstate.ID(generationID)
		state, err := config.Serving.ByID(ctx, stateID)
		if err != nil {
			if postgresDeliveryStartupNotFound(err) {
				return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingServingState)
			}
			return fmt.Errorf("delivery startup serving state: %w", err)
		}
		artifact, artifactErr := config.Serving.ArtifactByServingState(ctx, stateID)
		if artifactErr != nil {
			if postgresDeliveryStartupNotFound(artifactErr) {
				return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingServingState)
			}
			return fmt.Errorf("delivery startup serving artifact: %w", artifactErr)
		}
		if state.ID != stateID || state.ProjectID != claimedProject || state.Environment != config.Environment || state.Status != servingstate.StatusActive || state.Digest != generation.ServingArtifactDigest || artifact.ServingStateID != stateID || strings.TrimSpace(artifact.ID) == "" || artifact.Digest != generation.ServingArtifactDigest || strings.TrimSpace(artifact.Path) == "" {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupServingEvidenceMismatch)
		}

		seal, err := config.Delivery.SnapshotSeal(ctx, generation.SnapshotSealID)
		if err != nil {
			if postgresDeliveryStartupNotFound(err) {
				return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupMissingSeal)
			}
			return fmt.Errorf("delivery startup snapshot seal: %w", err)
		}
		if !postgresDeliveryStartupSealComplete(seal, generation, artifact, state) {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupSealEvidenceMismatch)
		}
		admission, admissionErr := config.Physical.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(seal.PhysicalPoolID), seal.CompatibilityDigest)
		if admissionErr != nil {
			code := deployment.DeliveryStartupUnadmittedPool
			if errors.Is(admissionErr, physicalpool.ErrPoolNotAdmitted) {
				code = deployment.DeliveryStartupMissingPoolAdmission
			}
			return postgresDeliveryStartupDiagnostics(scope, code)
		}
		if err := admission.Pool.Validate(); err != nil {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupUnadmittedPool)
		}
		if err := admission.Admission.Validate(); err != nil {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupUnadmittedPool)
		}
		if admission.Pool.ID != physicalpool.PoolID(seal.PhysicalPoolID) || !admission.Pool.Admitted || admission.Admission.PoolID != physicalpool.PoolID(seal.PhysicalPoolID) || admission.Admission.CompatibilityDigest != seal.CompatibilityDigest {
			return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupUnadmittedPool)
		}
		return nil
	}, nil
}

func postgresDeliveryStartupSealComplete(seal appdeploymentpostgres.StartupSnapshotSeal, generation appdeploymentpostgres.StartupGeneration, artifact servingstate.Artifact, state servingstate.State) bool {
	return seal.SealID == generation.SnapshotSealID &&
		seal.CandidateID == generation.CandidateID &&
		strings.TrimSpace(seal.PhysicalPoolID) != "" &&
		strings.TrimSpace(seal.CompatibilityDigest) != "" &&
		seal.DuckLakeSnapshotID > 0 &&
		seal.DuckLakeSnapshotID == state.DuckLakeSnapshotID &&
		strings.TrimSpace(seal.PlanDigest) != "" &&
		seal.PlanDigest == generation.PlanDigest &&
		strings.TrimSpace(seal.ServingArtifactDigest) != "" &&
		seal.ServingArtifactDigest == generation.ServingArtifactDigest &&
		strings.TrimSpace(seal.ServingArtifactID) != "" &&
		seal.ServingArtifactID == artifact.ID &&
		strings.TrimSpace(seal.CompiledGraphDigest) != "" &&
		seal.CompiledGraphDigest == generation.CompiledGraphDigest &&
		strings.TrimSpace(seal.CompiledConfigDigest) != "" &&
		seal.CompiledConfigDigest == generation.CompiledConfigDigest &&
		strings.TrimSpace(seal.SecurityDomainFingerprint) != "" &&
		seal.SecurityDomainFingerprint == generation.SecurityDomainFingerprint &&
		strings.TrimSpace(seal.ArtifactRoot) != "" &&
		seal.ArtifactRoot == generation.ArtifactRoot &&
		strings.TrimSpace(seal.ArtifactRootDigest) != "" &&
		seal.ArtifactRootDigest == generation.ArtifactRootDigest
}

func postgresDeliveryStartupDiagnostics(scope string, codes ...deployment.DeliveryStartupDiagnosticCode) error {
	diagnostics := make([]deployment.DeliveryStartupDiagnostic, 0, len(codes))
	for _, code := range codes {
		if code == "" {
			continue
		}
		diagnostics = append(diagnostics, deployment.DeliveryStartupDiagnostic{Code: code, Scope: scope})
	}
	return &deployment.DeliveryStartupDiagnosticsError{Diagnostics: diagnostics}
}

func postgresDeliveryStartupNotFound(err error) bool {
	return errors.Is(err, deployment.ErrNotFound) || errors.Is(err, servingstate.ErrNotFound) || errors.Is(err, deployment.ErrProjectClaimNotFound)
}
