package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	physicalpool "github.com/flidai/leapview/internal/analytics/physicalpool"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
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

// postgresDeliveryStartupRecoverySets is deliberately exact-ID and read-only.
// Recovery selection never means latest/current and readiness cannot mutate
// validation evidence.
type postgresDeliveryStartupRecoverySets interface {
	ReadExact(context.Context, string) (recoveryset.RecoverySet, error)
	ValidationAttempt(context.Context, string) (recoveryset.ValidationAttempt, error)
	ValidationResult(context.Context, string) (recoveryset.ValidationResult, error)
}

type postgresDeliveryStartupCheckConfig struct {
	TargetID      string
	Environment   servingstate.Environment
	RecoverySetID string
	ReadClaim     func(context.Context) (projectgraph.ResourceID, bool, error)
	Delivery      appdeploymentpostgres.StartupReader
	Recovery      postgresDeliveryStartupRecoverySets
	Serving       postgresDeliveryStartupServingStates
	Physical      postgresDeliveryStartupPhysicalPools
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
	recoverySetID := config.RecoverySetID
	if recoverySetID != "" && recoverySetID != strings.TrimSpace(recoverySetID) {
		return nil, errors.New("PostgreSQL delivery startup recovery set id must be a canonical UUID without surrounding whitespace")
	}
	if recoverySetID != "" {
		parsed, err := uuid.Parse(recoverySetID)
		if err != nil || parsed.String() != recoverySetID {
			return nil, errors.New("PostgreSQL delivery startup recovery set id must be a canonical UUID")
		}
	}
	if recoverySetID != "" && config.Recovery == nil {
		return nil, errors.New("PostgreSQL delivery startup recovery-set authority is required when recovery set id is configured")
	}

	return func(ctx context.Context) error {
		var selectedRecovery *recoveryset.RecoverySet
		if recoverySetID != "" {
			set, err := config.Recovery.ReadExact(ctx, recoverySetID)
			if err != nil {
				if postgresDeliveryStartupNotFound(err) || errors.Is(err, recoveryset.ErrNotFound) {
					return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetMissing)
				}
				if errors.Is(err, recoveryset.ErrInvalid) {
					return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetInvalid)
				}
				return fmt.Errorf("delivery startup recovery set: %w", err)
			}
			if err := set.Validate(); err != nil {
				return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetInvalid)
			}
			if set.ID != recoverySetID {
				return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetPointerMismatch)
			}
			if set.Status == recoveryset.StatusInvalid {
				return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetInvalid)
			}
			if set.Status != recoveryset.StatusPublished {
				return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetNotPublished)
			}
			selectedRecovery = &set
		}
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
			if selectedRecovery != nil {
				return postgresDeliveryStartupDiagnostics(config.TargetID, deployment.DeliveryStartupRecoverySetPointerMismatch)
			}
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
			if selectedRecovery != nil {
				return postgresDeliveryStartupDiagnostics(scope, deployment.DeliveryStartupRecoverySetPointerMismatch)
			}
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
		if state.ID != stateID || state.ProjectID != claimedProject || state.Environment != config.Environment || state.Status != servingstate.StatusActive || state.Digest != generation.ServingArtifactDigest || artifact.ServingStateID != stateID || artifact.Digest != generation.ServingArtifactDigest || !postgresDeliveryStartupNativeArtifactComplete(artifact) {
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
		if selectedRecovery != nil {
			if code := postgresDeliveryStartupRecoveryMismatch(*selectedRecovery, target, generation, publication, seal, state, artifact, admission); code != "" {
				return postgresDeliveryStartupDiagnostics(scope, code)
			}
			code, validationErr := postgresDeliveryStartupRecoveryValidation(ctx, config.Recovery, *selectedRecovery)
			if validationErr != nil {
				return fmt.Errorf("delivery startup recovery validation: %w", validationErr)
			}
			if code != "" {
				return postgresDeliveryStartupDiagnostics(scope, code)
			}
		}
		return nil
	}, nil
}

// postgresDeliveryStartupNativeArtifactComplete validates the immutable
// object-backed serving artifact persisted by the native PostgreSQL serving
// state repository. Native admission intentionally leaves Path empty; the
// digest-derived Locator and its storage metadata are the serving authority.
func postgresDeliveryStartupNativeArtifactComplete(artifact servingstate.Artifact) bool {
	if artifact.Path != "" || !postgresDeliveryStartupCanonicalDigest(artifact.Digest) {
		return false
	}
	wantID := "artifact-" + strings.TrimPrefix(artifact.Digest, "sha256:")
	if artifact.ID != wantID || artifact.ID != strings.TrimSpace(artifact.ID) {
		return false
	}
	if artifact.Format != servingstate.ArtifactBundleFormat || artifact.SizeBytes < 1 || artifact.SizeBytes > servingstate.MaxArtifactBundleBytes {
		return false
	}
	wantLocator := "serving-artifacts/" + strings.TrimPrefix(artifact.Digest, "sha256:") + ".tar.gz"
	if artifact.Locator != wantLocator || artifact.Locator != strings.TrimSpace(artifact.Locator) {
		return false
	}
	if artifact.StorageSecurityDomain == "" || !utf8.ValidString(artifact.StorageSecurityDomain) || artifact.StorageSecurityDomain != strings.TrimSpace(artifact.StorageSecurityDomain) || len(artifact.StorageSecurityDomain) > 512 || strings.IndexFunc(artifact.StorageSecurityDomain, unicode.IsControl) >= 0 {
		return false
	}
	if artifact.ContentType != servingstate.ArtifactBundleContentType || !postgresDeliveryStartupCanonicalDigest(artifact.MetadataDigest) {
		return false
	}
	return true
}

func postgresDeliveryStartupCanonicalDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	hexPart := value[len("sha256:"):]
	if _, err := hex.DecodeString(hexPart); err != nil {
		return false
	}
	return strings.ToLower(hexPart) == hexPart
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

// postgresDeliveryStartupRecoveryValidation proves that the published
// frontier points at one exact, immutable validation attempt and its matching
// result. Both rows are read by the attempt ID persisted on the frontier; no
// latest/current selector is ever consulted. Validation evidence is treated as
// control-plane data, so only stable diagnostic codes cross the readiness
// boundary.
func postgresDeliveryStartupRecoveryValidation(
	ctx context.Context,
	authority postgresDeliveryStartupRecoverySets,
	set recoveryset.RecoverySet,
) (deployment.DeliveryStartupDiagnosticCode, error) {
	attemptID := strings.TrimSpace(set.PublishedValidationAttemptID)
	if attemptID == "" {
		return deployment.DeliveryStartupRecoverySetValidationMissing, nil
	}
	attempt, err := authority.ValidationAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, recoveryset.ErrNotFound) || postgresDeliveryStartupNotFound(err) {
			return deployment.DeliveryStartupRecoverySetValidationMissing, nil
		}
		if errors.Is(err, recoveryset.ErrInvalid) {
			return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
		}
		return "", err
	}
	if attempt.AttemptID != attemptID || attempt.SetID != set.ID || attempt.FenceEpoch != set.FenceEpoch {
		return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
	}
	if attempt.Status != recoveryset.ValidationPassed {
		return deployment.DeliveryStartupRecoverySetValidationNotPassed, nil
	}
	if err := attempt.Validate(); err != nil {
		return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
	}
	if attempt.ResultDigest == "" {
		return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
	}
	result, err := authority.ValidationResult(ctx, attemptID)
	if err != nil {
		if errors.Is(err, recoveryset.ErrNotFound) || postgresDeliveryStartupNotFound(err) {
			return deployment.DeliveryStartupRecoverySetValidationMissing, nil
		}
		if errors.Is(err, recoveryset.ErrInvalid) {
			return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
		}
		return "", err
	}
	if result.AttemptID != attemptID || result.ResultDigest != attempt.ResultDigest {
		return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
	}
	if err := result.Validate(); err != nil {
		return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
	}
	envelope, err := recoveryset.ParseValidationEvidenceEnvelope(result.Evidence)
	if err != nil || envelope.ValidateFor(set, attemptID) != nil {
		return deployment.DeliveryStartupRecoverySetValidationMismatch, nil
	}
	return "", nil
}

// postgresDeliveryStartupRecoveryMismatch compares an explicitly selected
// immutable recovery frontier with every active native projection available
// to readiness. The returned code is stable and deliberately coarse; values
// from the frontier are never included in diagnostics.
func postgresDeliveryStartupRecoveryMismatch(
	set recoveryset.RecoverySet,
	target appdeploymentpostgres.StartupTarget,
	generation appdeploymentpostgres.StartupGeneration,
	publication appdeploymentpostgres.StartupPublication,
	seal appdeploymentpostgres.StartupSnapshotSeal,
	state servingstate.State,
	artifact servingstate.Artifact,
	admission physicalpool.AdmissionContract,
) deployment.DeliveryStartupDiagnosticCode {
	// The delivery pointer and all generation/publication identity bindings are
	// checked together so a selected frontier can never silently follow a
	// different active target revision.
	if set.Delivery.TargetID != target.TargetID ||
		set.Delivery.GenerationID != target.ActiveGenerationID ||
		set.Delivery.PublicationID != target.ActivePublicationID ||
		set.Delivery.TargetRevision != target.TargetRevision ||
		set.Delivery.GenerationID != generation.GenerationID ||
		set.Delivery.TargetID != generation.TargetID ||
		set.Serving.SealID != generation.SnapshotSealID ||
		set.Serving.PlanDigest != generation.PlanDigest ||
		set.Serving.ServingArtifactDigest != generation.ServingArtifactDigest ||
		set.Serving.CompiledGraphDigest != generation.CompiledGraphDigest ||
		set.Serving.CompiledConfigDigest != generation.CompiledConfigDigest ||
		set.Serving.SecurityDomainFingerprint != generation.SecurityDomainFingerprint ||
		set.Serving.ArtifactRoot != generation.ArtifactRoot ||
		set.Serving.ArtifactRootDigest != generation.ArtifactRootDigest ||
		set.Delivery.PublicationID != publication.PublicationID ||
		set.Delivery.TargetID != publication.TargetID ||
		set.Delivery.GenerationID != publication.GenerationID ||
		publication.CandidateID != generation.CandidateID ||
		publication.SnapshotSealID != generation.SnapshotSealID ||
		publication.State != "committed" ||
		publication.ResultTargetRevision != set.Delivery.TargetRevision {
		return deployment.DeliveryStartupRecoverySetPointerMismatch
	}

	// Artifact identity is separately diagnosed because it is the final
	// immutable serving object bound to both the seal and serving state.
	if set.Serving.ServingArtifactID != seal.ServingArtifactID ||
		set.Serving.ServingArtifactDigest != seal.ServingArtifactDigest ||
		set.Serving.ServingArtifactID != artifact.ID ||
		set.Serving.ServingArtifactDigest != artifact.Digest ||
		artifact.ServingStateID != state.ID {
		return deployment.DeliveryStartupRecoverySetArtifactMismatch
	}

	if set.Catalog.CatalogID != seal.CatalogID ||
		set.Catalog.CatalogDatabase != seal.CatalogDatabase ||
		set.Catalog.CatalogUUID != seal.CatalogUUID ||
		set.Catalog.CatalogVersion != seal.CatalogVersion ||
		set.Catalog.SnapshotID != seal.DuckLakeSnapshotID ||
		set.Serving.CatalogID != seal.CatalogID ||
		set.Serving.CatalogDatabase != seal.CatalogDatabase ||
		set.Serving.CatalogUUID != seal.CatalogUUID ||
		set.Serving.CatalogVersion != seal.CatalogVersion ||
		set.Serving.DuckLakeSnapshotID != seal.DuckLakeSnapshotID ||
		set.Serving.DuckLakeSnapshotID != state.DuckLakeSnapshotID {
		return deployment.DeliveryStartupRecoverySetCatalogMismatch
	}

	compatibilityDigest, err := set.Compatibility.Digest()
	if err != nil || set.Serving.CompatibilityDigest != seal.CompatibilityDigest ||
		set.Serving.CompatibilityDigest != compatibilityDigest ||
		!set.Compatibility.Equal(admission.Admission.Compatibility) {
		return deployment.DeliveryStartupRecoverySetCompatibilityMismatch
	}
	poolDataPath, err := admission.Pool.DataPath()
	if err != nil || set.Serving.TenantDomain != admission.Pool.Identity.Tenant ||
		set.Serving.Region != admission.Pool.Identity.Region ||
		set.Serving.EncryptionDomain != admission.Pool.Identity.EncryptionDomain ||
		set.Serving.ObjectNamespace != admission.Pool.Identity.StorageNamespace ||
		set.Serving.ObjectRoot != poolDataPath {
		return deployment.DeliveryStartupRecoverySetSealMismatch
	}
	if !postgresDeliveryStartupRecoveryRootsMatch(set.ObjectRoots, seal) {
		return deployment.DeliveryStartupRecoverySetSealMismatch
	}

	// Compare every remaining durable seal field, including provider-neutral
	// object roots and runtime compatibility versions. Catalog and compatibility
	// fields are checked above with their dedicated diagnostics.
	if set.Serving.SealID != seal.SealID ||
		set.Serving.PhysicalPoolID != seal.PhysicalPoolID ||
		set.Serving.TenantDomain != seal.TenantDomain ||
		set.Serving.Region != seal.Region ||
		set.Serving.EncryptionDomain != seal.EncryptionDomain ||
		set.Serving.ObjectNamespace != seal.ObjectNamespace ||
		set.Serving.RelationNamespace != seal.RelationNamespace ||
		set.Serving.RelationManifestDigest != seal.RelationManifestDigest ||
		set.Serving.ClosureDigest != seal.ClosureDigest ||
		set.Serving.ObjectRoot != seal.ObjectRoot ||
		set.Serving.ObjectRootDigest != seal.ObjectRootDigest ||
		set.Serving.ArtifactRoot != seal.ArtifactRoot ||
		set.Serving.ArtifactRootDigest != seal.ArtifactRootDigest ||
		set.Serving.CompiledGraphDigest != seal.CompiledGraphDigest ||
		set.Serving.CompiledConfigDigest != seal.CompiledConfigDigest ||
		set.Serving.SecurityDomainFingerprint != seal.SecurityDomainFingerprint ||
		set.Serving.RequestDigest != seal.RequestDigest ||
		set.Serving.PlanDigest != seal.PlanDigest ||
		set.Serving.DuckDBVersion != seal.DuckDBVersion ||
		set.Serving.RuntimeVersion != seal.RuntimeVersion ||
		set.Serving.DuckLakeExtensionVersion != seal.DuckLakeExtensionVersion ||
		set.Serving.DuckLakeSpecVersion != seal.DuckLakeSpecVersion ||
		set.Serving.CatalogSchemaVersion != seal.CatalogSchemaVersion {
		return deployment.DeliveryStartupRecoverySetSealMismatch
	}
	return ""
}

// postgresDeliveryStartupRecoveryRootsMatch consumes every selected root.
// The active native seal has exactly two provider recovery boundaries: the
// DuckLake object root and the immutable serving-artifact root. Provider
// version/frontier values remain part of the signed recovery-set identity;
// the recovery drill probes those values against the provider before publish.
func postgresDeliveryStartupRecoveryRootsMatch(roots []recoveryset.ObjectRoot, seal appdeploymentpostgres.StartupSnapshotSeal) bool {
	if len(roots) != 2 {
		return false
	}
	objectRoot, artifactRoot := false, false
	for _, root := range roots {
		switch root.Kind {
		case recoveryset.ObjectRootDuckLake:
			if root.URI != seal.ObjectRoot || root.Digest != seal.ObjectRootDigest || objectRoot {
				return false
			}
			objectRoot = true
		case recoveryset.ObjectRootServingArtifact:
			if root.URI != seal.ArtifactRoot || root.Digest != seal.ArtifactRootDigest || artifactRoot {
				return false
			}
			artifactRoot = true
		default:
			return false
		}
	}
	return objectRoot && artifactRoot
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
