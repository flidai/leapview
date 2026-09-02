package deploymentpostgres

// This file contains the value-only hand-off from a native physical build to
// the PostgreSQL generation-admission boundary.  It deliberately performs no
// I/O and has no access to a catalog, object store, or database.  Every value
// that can affect admission is supplied by an authoritative contract or by
// immutable build evidence; this package never invents an identity.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	"github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/extension"
	project "github.com/flidai/leapview/internal/project"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/strictjson"
)

// NativeSealEvidenceAssemblerInput is the complete value-only input for one
// native build hand-off.  The attempt admission result is retained as a
// single value so owner, lease fence, artifact binding, and DuckLake ledger
// evidence cannot accidentally be mixed from different attempts.
//
// TenantDomain, EncryptionDomain, and ObjectNamespace are intentionally
// explicit. They do not exist in the DuckLake runtime evidence and must be
// supplied by the target authority. Artifact root evidence is instead derived
// from the immutable native artifact locator and its content digest.
type NativeSealEvidenceAssemblerInput struct {
	Build            NativePhysicalBuildEvidence
	AttemptAdmission CandidateBuildAttemptAdmissionResult
	PoolContract     *ducklake.PoolContract
	CatalogIdentity  ducklakepostgres.CatalogIdentity
	Compatibility    ducklakepostgres.RuntimeCompatibility
	Plan             deployment.DeliveryPlan
	Artifacts        release.CandidateArtifactSet
	// Bindings and SourceRevision are the exact non-secret evidence observed
	// while preparing this candidate. They are retained in release provenance
	// together with the native qualification gate result.
	Bindings       []deployment.CandidateConnectionEvidence
	SourceRevision *project.CandidateSourceRevision

	// RuntimeVersion is the LeapView runtime identity (not DuckDB's version).
	// DuckDB and DuckLake versions are taken from Compatibility and build seal
	// evidence, respectively.
	RuntimeVersion string

	// DuckLakeSpecVersion is derived from Compatibility.CatalogFormat.  The
	// field is retained as an optional assertion for callers that already carry
	// the normalized value; a non-empty value must match the derived value.
	DuckLakeSpecVersion string

	Qualification NativeQualificationEvidence

	SealID       string
	GenerationID string

	TenantDomain     string
	EncryptionDomain string
	ObjectNamespace  string
}

// NativeSealAssemblerInput and NativePhysicalSealAssemblyInput are concise
// aliases for callers which use the operation name rather than the evidence
// name.
type NativeSealAssemblerInput = NativeSealEvidenceAssemblerInput
type NativePhysicalSealAssemblyInput = NativeSealEvidenceAssemblerInput

// NativeRecoveredSealEvidenceAssemblerInput is the value-only input for a
// recovery hand-off.  It intentionally has the same immutable evidence shape
// as NativeSealEvidenceAssemblerInput, but is a distinct named type and must
// be passed to one of the recovery-specific assembler functions below.  This
// makes it difficult for recovery callers to accidentally select the
// fresh-build assembler (which requires a live lease and running attempts).
//
// The artifact identity in this value is the recovered CandidateArtifactSet's
// Generation identity and NativeArtifact metadata, together with the exact
// value-only binding identity in AttemptAdmission.Artifact.  The recovery
// assembler cross-checks those values before constructing the admission input;
// the later recovery transaction may persist a first binding when none exists.
type NativeRecoveredSealEvidenceAssemblerInput NativeSealEvidenceAssemblerInput

// NativeRecoveredSealAssemblerInput and NativeRecoveredPhysicalSealAssemblyInput
// are concise aliases for callers that name the operation by its recovery or
// physical-seal role.
type NativeRecoveredSealAssemblerInput = NativeRecoveredSealEvidenceAssemblerInput
type NativeRecoveredPhysicalSealAssemblyInput = NativeRecoveredSealEvidenceAssemblerInput

// AssembleNativeGenerationAdmissionInput validates and assembles a complete
// GenerationAdmissionInput from exact native build evidence.  The returned
// value has already passed the same normalization used by CompleteBuildAndAdmit.
func AssembleNativeGenerationAdmissionInput(input NativeSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	assembled, err := assembleNativeSealEvidenceWithPolicy(input, nativeSealAssemblerFresh)
	if err != nil {
		return GenerationAdmissionInput{}, err
	}
	return assembled, nil
}

// AssembleRecoveredNativeGenerationAdmissionInput validates and assembles a
// complete GenerationAdmissionInput from an indeterminate native build.  It
// is deliberately separate from AssembleNativeGenerationAdmissionInput:
// recovery accepts only an exact released target lease and indeterminate
// delivery/DuckLake attempts, while fresh execution continues to require an
// active lease and running attempts.
func AssembleRecoveredNativeGenerationAdmissionInput(input NativeRecoveredSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	assembled, err := assembleNativeSealEvidenceWithPolicy(NativeSealEvidenceAssemblerInput(input), nativeSealAssemblerRecovery)
	if err != nil {
		return GenerationAdmissionInput{}, err
	}
	return assembled, nil
}

// AssembleNativeRecoveredGenerationAdmissionInput is a descriptive alias for
// AssembleRecoveredNativeGenerationAdmissionInput.
func AssembleNativeRecoveredGenerationAdmissionInput(input NativeRecoveredSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return AssembleRecoveredNativeGenerationAdmissionInput(input)
}

// AssembleNativeGenerationAdmissionInputForRecovery is a compatibility alias
// for callers that keep the recovery qualifier at the end of the operation
// name.
func AssembleNativeGenerationAdmissionInputForRecovery(input NativeRecoveredSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return AssembleRecoveredNativeGenerationAdmissionInput(input)
}

// AssembleRecoveredNativeGenerationAdmission is a concise alias for
// AssembleRecoveredNativeGenerationAdmissionInput.
func AssembleRecoveredNativeGenerationAdmission(input NativeRecoveredSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return AssembleRecoveredNativeGenerationAdmissionInput(input)
}

// AssembleNativeGenerationAdmission is a descriptive alias for
// AssembleNativeGenerationAdmissionInput.
func AssembleNativeGenerationAdmission(input NativeSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return AssembleNativeGenerationAdmissionInput(input)
}

// AssembleNativeSealEvidence is retained for callers that describe this
// operation as assembling seal evidence.  It returns the full admission input
// because a seal is not admissible without its generation and artifact proof.
func AssembleNativeSealEvidence(input NativeSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return AssembleNativeGenerationAdmissionInput(input)
}

// AssembleNativeSnapshotSealEvidence assembles and validates only the seal
// projection.  It shares all cross-identity checks with the full admission
// assembler and therefore cannot be used to bypass generation validation.
func AssembleNativeSnapshotSealEvidence(input NativeSealEvidenceAssemblerInput) (SnapshotSealEvidence, error) {
	assembled, err := assembleNativeSealEvidenceWithPolicy(input, nativeSealAssemblerFresh)
	if err != nil {
		return SnapshotSealEvidence{}, err
	}
	return assembled.Seal, nil
}

// NativeSealEvidenceAssembler is a stateless convenience value for callers
// which prefer a method-shaped API.
type NativeSealEvidenceAssembler struct{}

func (NativeSealEvidenceAssembler) Assemble(input NativeSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return AssembleNativeGenerationAdmissionInput(input)
}

func assembleNativeSealEvidence(input NativeSealEvidenceAssemblerInput) (GenerationAdmissionInput, error) {
	return assembleNativeSealEvidenceWithPolicy(input, nativeSealAssemblerFresh)
}

type nativeSealAssemblerPolicy uint8

const (
	nativeSealAssemblerFresh nativeSealAssemblerPolicy = iota
	nativeSealAssemblerRecovery
)

func assembleNativeSealEvidenceWithPolicy(input NativeSealEvidenceAssemblerInput, policy nativeSealAssemblerPolicy) (GenerationAdmissionInput, error) {
	if err := validateNativeSealAssemblerInputWithPolicy(input, policy); err != nil {
		return GenerationAdmissionInput{}, err
	}

	build := input.Build
	attempt := input.AttemptAdmission.Attempt
	lease := input.AttemptAdmission.Lease
	marker, canonicalMarker, err := canonicalBuildMarker(build)
	if err != nil {
		return GenerationAdmissionInput{}, err
	}
	poolID := input.PoolContract.Pool.ID.String()
	catalogVersion, err := canonicalNumericCatalogVersion(build.Seal.CatalogVersion)
	if err != nil {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: catalog version: %v", deploymentnative.ErrInvalid, err)
	}
	duckLakeSpecVersion := strconv.FormatInt(catalogVersion, 10)

	generationID := input.GenerationID
	if generationID == "" {
		generationID = input.Artifacts.Generation.Identity.GenerationID
	}
	sealID := input.SealID
	qualification, qualificationDigest, err := input.Qualification.Canonical()
	if err != nil {
		return GenerationAdmissionInput{}, fmt.Errorf("%w: canonical qualification evidence: %v", deploymentnative.ErrInvalid, err)
	}
	artifact := input.Artifacts.Generation
	artifactRoot := artifact.NativeArtifact.Locator
	artifactRootDigest := artifact.ArtifactDigest
	projectID := artifact.Identity.ProjectID
	environment := servingstate.Environment(artifact.Identity.Environment)

	assembled := GenerationAdmissionInput{
		Commit: CommitEvidence{
			DeliveryID: marker.DeliveryID, AttemptID: attempt.AttemptID, OwnerID: attempt.OwnerID,
			FencingEpoch: attempt.FencingEpoch, SnapshotID: build.SnapshotID, CommitMarker: canonicalMarker,
		},
		Seal: SnapshotSealEvidence{
			SealID: sealID, AttemptID: attempt.AttemptID, CandidateID: attempt.CandidateID,
			PhysicalPoolID: poolID, TenantDomain: input.TenantDomain, Region: input.PoolContract.Pool.Identity.Region,
			EncryptionDomain: input.EncryptionDomain, ObjectNamespace: input.ObjectNamespace,
			CatalogDatabase: input.CatalogIdentity.CatalogDatabase, CatalogID: build.CatalogID,
			CatalogUUID: input.CatalogIdentity.CatalogUUID, CatalogVersion: catalogVersion,
			DuckLakeSnapshotID: build.SnapshotID, RelationNamespace: attempt.Namespace,
			RelationManifestDigest: build.Closure.RelationManifestDigest, ClosureDigest: build.Closure.ClosureDigest,
			ObjectRoot: build.ObjectRoot, ObjectRootDigest: build.Closure.ObjectRootDigest,
			ArtifactRoot: artifactRoot, ArtifactRootDigest: artifactRootDigest,
			CompiledGraphDigest: input.Artifacts.Compiler.Graph.Digest(), CompiledConfigDigest: input.Plan.Execution.ConfigDigest,
			SecurityDomainFingerprint: input.Artifacts.AuthorizationFingerprint, RequestDigest: attempt.RequestDigest,
			PlanDigest: input.Plan.Digest, CompatibilityDigest: input.Compatibility.CompatibilityDigest,
			ServingArtifactID: artifact.ServingArtifactID, ServingArtifactDigest: artifact.ArtifactDigest,
			DuckDBVersion: input.Compatibility.DuckDBRuntime, RuntimeVersion: input.RuntimeVersion,
			DuckLakeExtensionVersion: input.Compatibility.DuckLakeExtension, DuckLakeSpecVersion: duckLakeSpecVersion,
			CatalogSchemaVersion: input.Compatibility.CatalogSchemaVersion, QualificationEvidence: qualification,
		},
		QualificationDigest: qualificationDigest,
		CandidateExpiresAt:  input.Plan.Governance.ExpiresAt.UTC().Truncate(time.Microsecond),
		Fence:               LeaseFenceEvidence{LeaseID: lease.LeaseID, TargetID: lease.TargetID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch},
		Generation: GenerationEvidence{
			GenerationID: generationID, TargetID: input.Plan.TargetID, CandidateID: attempt.CandidateID, SnapshotSealID: sealID,
			PlanID: input.Plan.ID, PlanDigest: input.Plan.Digest, ArtifactRoot: artifactRoot, ArtifactRootDigest: artifactRootDigest,
			ServingArtifactDigest: artifact.ArtifactDigest, CompiledGraphDigest: input.Artifacts.Compiler.Graph.Digest(),
			CompiledConfigDigest: input.Plan.Execution.ConfigDigest, SecurityDomainFingerprint: input.Artifacts.AuthorizationFingerprint,
		},
		Bundle: BundleEvidenceInput{
			GenerationID: generationID, ProjectID: projectID, Environment: environment,
			Artifact: servingstate.Artifact{
				ID: artifact.ServingArtifactID, ServingStateID: servingstate.ID(generationID), Digest: artifact.ArtifactDigest,
				Format: projectbundle.BundleFormat, ManifestJSON: artifact.BundleManifestJSON, SizeBytes: artifact.NativeArtifact.SizeBytes,
			},
			ArtifactLocator: artifact.NativeArtifact.Locator, StorageSecurityDomain: artifact.NativeArtifact.StorageSecurityDomain,
			ArtifactContentType: artifact.NativeArtifact.ContentType, ArtifactMetadataDigest: artifact.NativeArtifact.MetadataDigest,
			ProjectDigest: input.Artifacts.Artifact.ProjectDigest, AccessPolicyJSON: artifact.AccessPolicyJSON,
			DashboardPublicationsJSON: artifact.DashboardPublicationsJSON, DashboardAppearancesJSON: artifact.DashboardAppearancesJSON,
			CreatedBy: attempt.OwnerID,
		},
		ManagedDataPins: append([]release.ManagedDataPin{}, artifact.ManagedDataPins...),
		Graph:           input.Artifacts.Compiler.Graph,
	}
	// Carry a provenance template through the value-only boundary. Candidate
	// revision is allocated by delivery during CompleteBuildAndAdmitTx and is
	// filled there immediately before the immutable row is retained.
	bindings := make([]release.BindingEvidence, len(input.Bindings))
	for i, binding := range input.Bindings {
		bindings[i] = release.BindingEvidence{
			BindingID: binding.BindingID, ConnectionID: binding.ConnectionID.String(), ConnectorKind: binding.ConnectorKind,
			Revision: binding.Revision, ValidatedVersion: binding.ProviderVersion,
			EndpointConfigHash: binding.EndpointConfigHash, Access: binding.Access,
		}
	}
	var sourceRevision *release.SourceRevisionProvenance
	if input.SourceRevision != nil {
		sourceRevision = &release.SourceRevisionProvenance{Revision: input.SourceRevision.Revision, Repository: input.SourceRevision.Repository, Ref: input.SourceRevision.Ref, ChangeID: input.SourceRevision.ChangeID}
	}
	var baseIdentity *projectgraph.ServingIdentity
	if input.Plan.BaseGenerationID != "" {
		base := projectgraph.ServingIdentity{ProjectID: projectID, Environment: input.Plan.Environment, GenerationID: input.Plan.BaseGenerationID}
		baseIdentity = &base
	}
	gates := input.Qualification.Gates
	assembled.Provenance = release.ProvenanceInput{
		Artifact:       input.Artifacts.Artifact,
		Candidate:      release.CandidateProvenance{ID: attempt.CandidateID, OwnerID: attempt.OwnerID},
		SourceRevision: sourceRevision,
		Plan: release.GenerationPlanProvenance{
			Identity: artifact.Identity, BaseIdentity: baseIdentity, TargetID: input.Plan.TargetID,
			RuntimeVersion: input.RuntimeVersion, PolicyDigest: input.Artifacts.AuthorizationFingerprint,
			DataRevision: artifact.DataRevision, DataMode: artifact.DataMode,
			ManagedDataPins: append([]release.ManagedDataPin(nil), artifact.ManagedDataPins...), Bindings: bindings,
			AuthoredConnections: nativeAuthoredConnectionEvidence(artifact.AuthoredConnections), Extensions: append([]extension.Evidence(nil), input.Artifacts.Extensions...), GateEvidence: &gates,
		},
	}

	// Ensure marker is retained as the exact canonical bytes used by the
	// delivery authority.  The local variable also makes the identity checks
	// below explicit and easy to audit.
	_ = marker
	normalized, err := normalizeInput(assembled)
	if err != nil {
		return GenerationAdmissionInput{}, err
	}
	return normalized, nil
}

func nativeAuthoredConnectionEvidence(values []release.CandidateAuthoredConnection) []release.AuthoredConnectionEvidence {
	result := make([]release.AuthoredConnectionEvidence, len(values))
	for i, value := range values {
		result[i] = release.AuthoredConnectionEvidence{ConnectionID: value.ConnectionID.String(), ConnectorKind: value.ConnectorKind, Access: value.Access}
	}
	return result
}

func validateNativeSealAssemblerInput(input NativeSealEvidenceAssemblerInput) error {
	return validateNativeSealAssemblerInputWithPolicy(input, nativeSealAssemblerFresh)
}

func validateNativeSealAssemblerInputWithPolicy(input NativeSealEvidenceAssemblerInput, policy nativeSealAssemblerPolicy) error {
	if policy != nativeSealAssemblerFresh && policy != nativeSealAssemblerRecovery {
		return fmt.Errorf("%w: native seal assembler policy is invalid", deploymentnative.ErrInvalid)
	}
	if input.PoolContract == nil {
		return fmt.Errorf("%w: admitted physical-pool contract is required", deploymentnative.ErrInvalid)
	}
	if err := input.PoolContract.Validate(); err != nil {
		return fmt.Errorf("%w: physical-pool contract: %v", deploymentnative.ErrInvalid, err)
	}
	if err := input.Plan.Validate(); err != nil {
		return fmt.Errorf("%w: delivery plan: %v", deploymentnative.ErrInvalid, err)
	}
	if input.Plan.Status != deployment.DeliveryPlanPlanned {
		return fmt.Errorf("%w: delivery plan is not planned", deploymentnative.ErrInvalid)
	}
	if input.Artifacts.Generation.DataMode == release.GenerationDataReuseBase {
		// No native base-catalog evidence crosses this boundary. Retaining a
		// base without an exact sealed snapshot/closure would fabricate reuse.
		return fmt.Errorf("%w: native base catalog reuse is unsupported without exact base evidence", deploymentnative.ErrInvalid)
	}
	if input.Artifacts.Generation.DataMode != release.GenerationDataRefreshSources {
		return fmt.Errorf("%w: unsupported candidate data mode %q", deploymentnative.ErrInvalid, input.Artifacts.Generation.DataMode)
	}
	if err := validateCanonicalRuntimeText(input.RuntimeVersion, "runtime version", 512); err != nil {
		return err
	}
	if input.DuckLakeSpecVersion != "" {
		catalogVersion, err := canonicalNumericCatalogVersion(input.Compatibility.CatalogFormat)
		derived := strconv.FormatInt(catalogVersion, 10)
		if err != nil || input.DuckLakeSpecVersion != derived {
			return conflict("DuckLake specification version differs from admitted catalog format")
		}
	}
	if _, _, err := input.Qualification.Canonical(); err != nil {
		return fmt.Errorf("%w: qualification evidence: %v", deploymentnative.ErrInvalid, err)
	}
	if err := validateNativeQualificationBinding(input); err != nil {
		return err
	}
	if input.SealID == "" {
		return fmt.Errorf("%w: seal id is required", deploymentnative.ErrInvalid)
	}
	if _, err := canonicalUUID(input.SealID, "seal id"); err != nil {
		return err
	}
	if input.GenerationID != "" {
		if _, err := canonicalUUID(input.GenerationID, "generation id"); err != nil {
			return err
		}
	}
	if err := validateCanonicalText(input.TenantDomain, "tenant domain", 512); err != nil {
		return err
	}
	if err := validateCanonicalText(input.EncryptionDomain, "encryption domain", 512); err != nil {
		return err
	}
	if err := validateCanonicalText(input.ObjectNamespace, "object namespace", 512); err != nil {
		return err
	}
	if err := validateNativeBuildValues(input); err != nil {
		return err
	}
	if err := validateNativeCatalogValues(input); err != nil {
		return err
	}
	return validateNativeCandidateValuesWithPolicy(input, policy)
}

func validateNativeBuildValues(input NativeSealEvidenceAssemblerInput) error {
	build := input.Build
	if build.AttemptID == "" || build.AttemptID != input.AttemptAdmission.Attempt.AttemptID {
		return conflict("native build and admitted attempt identities differ")
	}
	if build.CatalogID == "" || build.CatalogID != input.CatalogIdentity.CatalogID {
		return conflict("native build and catalog identities differ")
	}
	if build.SnapshotID <= 0 || build.SnapshotID != build.Seal.SnapshotID || build.SnapshotID != build.Closure.SnapshotID {
		return fmt.Errorf("%w: native snapshot evidence is inconsistent", deploymentnative.ErrInvalid)
	}
	if err := ducklake.VerifyNativeSnapshotClosureEvidence(build.Closure); err != nil {
		return fmt.Errorf("%w: native closure evidence: %v", deploymentnative.ErrInvalid, err)
	}
	canonicalRoot, err := ducklake.CanonicalDataPath(build.ObjectRoot)
	if err != nil || canonicalRoot != build.ObjectRoot || build.Closure.ObjectRoot != build.ObjectRoot || build.Seal.DataPath != build.ObjectRoot {
		return conflict("native object-root evidence differs")
	}
	if build.Seal.CatalogType != "postgres" && !strings.EqualFold(strings.TrimSpace(build.Seal.CatalogType), "postgres") {
		return conflict("native DuckLake catalog type is not PostgreSQL")
	}
	if build.Closure.CatalogID != build.CatalogID {
		return conflict("native closure catalog identity differs")
	}
	marker, canonical, err := canonicalBuildMarker(build)
	if err != nil {
		return err
	}
	if marker.AttemptID != build.AttemptID || marker.GenerationID != input.Artifacts.Generation.Identity.GenerationID || marker.PlanDigest != input.Plan.Digest || marker.Project != input.Plan.ProjectID.String() || marker.Environment != input.Plan.Environment || marker.PhysicalPoolID != input.PoolContract.Pool.ID.String() || marker.RequestDigest != input.AttemptAdmission.Attempt.RequestDigest || marker.LeaseEpoch != input.AttemptAdmission.Attempt.FencingEpoch || build.Seal.CommitMarker != string(canonical) {
		return conflict("native commit marker identity differs")
	}
	return nil
}

func validateNativeCatalogValues(input NativeSealEvidenceAssemblerInput) error {
	pool := input.PoolContract.Pool
	identity := input.CatalogIdentity
	compatibility := input.Compatibility
	poolID := pool.ID.String()
	if identity.PhysicalPoolID != poolID || input.Build.Marker.PhysicalPoolID != poolID {
		return conflict("catalog and physical-pool identities differ")
	}
	if identity.CatalogID != input.Build.CatalogID {
		return conflict("catalog identity differs from native build")
	}
	if identity.CatalogUUID == "" {
		return fmt.Errorf("%w: catalog UUID is required", deploymentnative.ErrInvalid)
	}
	if _, err := canonicalUUID(identity.CatalogUUID, "catalog uuid"); err != nil {
		return err
	}
	if err := validateCanonicalText(identity.CatalogDatabase, "catalog database", 255); err != nil {
		return err
	}
	if err := validateCanonicalText(identity.MetadataSchema, "catalog metadata schema", 255); err != nil {
		return err
	}
	if identity.MetadataSchema != input.Build.Seal.MetadataSchema || identity.MetadataSchema != ducklake.MetadataSchemaForPool(poolID) {
		return conflict("catalog metadata schema differs from admitted pool")
	}
	if err := compatibilityValidate(compatibility); err != nil {
		return err
	}
	if input.PoolContract.Admission.CompatibilityDigest != compatibility.CompatibilityDigest {
		return conflict("catalog compatibility digest differs from admitted runtime")
	}
	if input.PoolContract.Tuple.DuckDBRuntime != compatibility.DuckDBRuntime || input.PoolContract.Tuple.DuckLakeExtension != compatibility.DuckLakeExtension || input.PoolContract.Tuple.CatalogFormat != compatibility.CatalogFormat {
		return conflict("pool contract and runtime compatibility tuples differ")
	}
	if err := validateRuntimeAndCatalogVersions(input.Build.Seal.CatalogVersion, input.Build.Seal.ExtensionVersion, compatibility); err != nil {
		return err
	}
	if input.PoolContract.Pool.Identity.Region == "" {
		return fmt.Errorf("%w: admitted physical-pool region is required", deploymentnative.ErrInvalid)
	}
	if input.PoolContract.Pool.Identity.Tenant == "" || input.TenantDomain != input.PoolContract.Pool.Identity.Tenant {
		return conflict("tenant domain differs from admitted physical-pool identity")
	}
	if input.PoolContract.Pool.Identity.Region != strings.TrimSpace(input.PoolContract.Pool.Identity.Region) {
		return fmt.Errorf("%w: admitted physical-pool region is not canonical", deploymentnative.ErrInvalid)
	}
	return nil
}

func validateNativeCandidateValues(input NativeSealEvidenceAssemblerInput) error {
	return validateNativeCandidateValuesWithPolicy(input, nativeSealAssemblerFresh)
}

func validateNativeCandidateValuesWithPolicy(input NativeSealEvidenceAssemblerInput, policy nativeSealAssemblerPolicy) error {
	identity := input.Artifacts.Generation.Identity
	if err := identity.Validate(); err != nil {
		return fmt.Errorf("%w: candidate serving identity: %v", deploymentnative.ErrInvalid, err)
	}
	if input.GenerationID != "" && input.GenerationID != identity.GenerationID {
		return conflict("generation identity differs from candidate artifact")
	}
	if identity.GenerationID != input.Build.Marker.GenerationID {
		return conflict("candidate artifact and commit marker generation identities differ")
	}
	if identity.ProjectID != input.Plan.ProjectID || identity.Environment != input.Plan.Environment {
		return conflict("candidate artifact scope differs from delivery plan")
	}
	if input.AttemptAdmission.Attempt.PlanID != input.Plan.ID || input.AttemptAdmission.Attempt.PlanDigest != input.Plan.Digest || input.AttemptAdmission.Attempt.CandidateID == "" {
		return conflict("admitted attempt and delivery plan identities differ")
	}
	lease := input.AttemptAdmission.Lease
	attempt := input.AttemptAdmission.Attempt
	duckAttempt := input.AttemptAdmission.DuckLakeAttempt
	if lease.TargetID != input.Plan.TargetID || lease.OwnerID != attempt.OwnerID || lease.FencingEpoch != attempt.FencingEpoch || lease.LeaseID == "" {
		return conflict("admitted lease fence differs from attempt")
	}
	if policy == nativeSealAssemblerFresh {
		if lease.State != "active" {
			return conflict("admitted lease is not active")
		}
		if attempt.State != deploymentnative.AttemptRunning || duckAttempt.State != ducklakepostgres.AttemptRunning {
			return conflict("admitted attempts are not running")
		}
	} else {
		// Recovery is fenced by both ledgers being indeterminate and by the
		// exact target lease having already been released.  In particular,
		// expired, active, or any other lease state cannot authorize this
		// value-only hand-off.
		if err := validateRecoveredReleasedLease(lease); err != nil {
			return err
		}
		if attempt.State != deploymentnative.AttemptIndeterminate || duckAttempt.State != ducklakepostgres.AttemptIndeterminate {
			return conflict("recovery requires indeterminate delivery and DuckLake attempts")
		}
		if input.AttemptAdmission.Artifact.AttemptID != attempt.AttemptID {
			return conflict("recovered artifact binding differs from indeterminate attempt")
		}
		if !attempt.LeaseExpiresAt.Equal(lease.ExpiresAt) || !duckAttempt.LeaseExpiresAt.Equal(lease.ExpiresAt) {
			return conflict("recovered attempt lease evidence differs from released target lease")
		}
		if err := validateRecoveredAttemptTermination(attempt, duckAttempt); err != nil {
			return err
		}
	}
	expectedDuckState := ducklakepostgres.AttemptRunning
	if policy == nativeSealAssemblerRecovery {
		expectedDuckState = ducklakepostgres.AttemptIndeterminate
	}
	if duckAttempt.State != expectedDuckState || duckAttempt.AttemptID != attempt.AttemptID || duckAttempt.RequestDigest != attempt.RequestDigest || duckAttempt.PlanDigest != input.Plan.Digest || duckAttempt.PhysicalPoolID != input.PoolContract.Pool.ID.String() || duckAttempt.CatalogID != input.Build.CatalogID || duckAttempt.OwnerID != attempt.OwnerID || duckAttempt.FencingEpoch != attempt.FencingEpoch {
		return conflict("DuckLake attempt admission differs from native build")
	}
	if policy == nativeSealAssemblerRecovery && input.Qualification.Digest == "" {
		return fmt.Errorf("%w: recovered qualification digest is required", deploymentnative.ErrInvalid)
	}
	if err := validateDigest(input.Artifacts.Artifact.ProjectDigest, "project digest"); err != nil {
		return err
	}
	if err := validateDigest(input.Artifacts.Artifact.ContentDigest, "artifact content digest"); err != nil {
		return err
	}
	if input.Artifacts.Generation.ArtifactDigest != input.Artifacts.Artifact.ContentDigest || input.Artifacts.Generation.ServingArtifactID != "artifact-"+strings.TrimPrefix(input.Artifacts.Generation.ArtifactDigest, "sha256:") {
		return conflict("candidate artifact content identity differs")
	}
	if input.AttemptAdmission.Artifact.ServingArtifactID != input.Artifacts.Generation.ServingArtifactID || input.AttemptAdmission.Artifact.ServingArtifactDigest != input.Artifacts.Generation.ArtifactDigest || input.AttemptAdmission.Artifact.ServingStateID != identity.GenerationID {
		return conflict("serving artifact admission differs from candidate artifact")
	}
	if input.Artifacts.Compiler.Graph.ProjectID() != identity.ProjectID || input.Artifacts.Compiler.Graph.Digest() == "" {
		return conflict("candidate compiler graph identity differs")
	}
	if input.Plan.Execution.SourceArtifactDigest != input.Artifacts.Artifact.SourceDigest {
		return conflict("delivery plan source and candidate source identities differ")
	}
	if input.Artifacts.AuthorizationFingerprint == "" || input.Artifacts.AuthorizationFingerprint != input.Plan.Governance.AuthorizationDigest {
		return conflict("candidate authorization fingerprint differs from delivery plan")
	}
	if err := validateNativeArtifactObject(input.Artifacts.Generation.NativeArtifact, input.Artifacts.Generation.ArtifactDigest); err != nil {
		return err
	}
	return nil
}

func validateRecoveredReleasedLease(lease deploymentnative.DeliveryLease) error {
	if lease.State != "released" || lease.LeaseID == "" || lease.TargetID == "" || lease.OwnerID == "" || lease.FencingEpoch <= 0 {
		return conflict("recovery requires the exact released target lease")
	}
	for label, value := range map[string]time.Time{
		"lease acquired": lease.AcquiredAt,
		"lease expiry":   lease.ExpiresAt,
		"lease released": lease.ReleasedAt,
	} {
		if value.IsZero() || value.Location() != time.UTC || !value.Equal(value.UTC()) {
			return fmt.Errorf("%w: recovered %s timestamp is invalid", deploymentnative.ErrInvalid, label)
		}
	}
	if !lease.ExpiresAt.After(lease.AcquiredAt) || !lease.ReleasedAt.After(lease.AcquiredAt) {
		return fmt.Errorf("%w: recovered lease timestamps are not chronological", deploymentnative.ErrInvalid)
	}
	return nil
}

func validateRecoveredAttemptTermination(delivery deploymentnative.DeliveryBuildAttempt, ducklake ducklakepostgres.AttemptEvidence) error {
	termination := AttemptTerminationInput{
		AttemptID: delivery.AttemptID, OwnerID: delivery.OwnerID,
		FencingEpoch: delivery.FencingEpoch, Evidence: delivery.TerminationEvidence,
	}
	normalized, evidence, err := normalizeAttemptTerminationInput(termination)
	if err != nil {
		return fmt.Errorf("%w: recovered attempt termination evidence is invalid: %v", deploymentnative.ErrConflict, err)
	}
	if err := verifyDeliveryTermination(delivery, normalized, evidence, deploymentnative.AttemptIndeterminate); err != nil {
		return err
	}
	if err := verifyDuckLakeTermination(ducklake, normalized, evidence, ducklakepostgres.AttemptIndeterminate); err != nil {
		return err
	}
	if err := verifyTerminationLedgerAgreement(delivery, ducklake, evidence); err != nil {
		return err
	}
	return nil
}

func validateNativeQualificationBinding(input NativeSealEvidenceAssemblerInput) error {
	qualification := input.Qualification
	build := input.Build
	attempt := input.AttemptAdmission.Attempt
	if qualification.CandidateID != attempt.CandidateID || qualification.AttemptID != attempt.AttemptID || qualification.PhysicalPoolID != attempt.PhysicalPoolID || qualification.CatalogID != build.CatalogID || qualification.SnapshotID != build.SnapshotID {
		return conflict("qualification identity differs from admitted native build")
	}
	if qualification.ObjectRoot != build.ObjectRoot || qualification.RelationNamespace != attempt.Namespace || qualification.RelationManifestDigest != build.Closure.RelationManifestDigest || qualification.ClosureDigest != build.Closure.ClosureDigest {
		return conflict("qualification closure differs from native build")
	}
	compatibility := input.Compatibility
	catalogFormat, err := canonicalCatalogVersion(compatibility.CatalogFormat)
	if err != nil {
		return fmt.Errorf("%w: admitted catalog format: %v", deploymentnative.ErrInvalid, err)
	}
	if qualification.Runtime.SnapshotID != build.SnapshotID || qualification.Runtime.CatalogType != "postgres" || qualification.Runtime.DataPath != build.ObjectRoot || qualification.Runtime.MetadataSchema != input.CatalogIdentity.MetadataSchema || qualification.Runtime.DuckDBRuntime != compatibility.DuckDBRuntime || qualification.Runtime.DuckLakeExtension != compatibility.DuckLakeExtension || qualification.Runtime.CatalogFormat != catalogFormat || qualification.Runtime.CompatibilityDigest != compatibility.CompatibilityDigest || qualification.Runtime.CatalogSchemaVersion != compatibility.CatalogSchemaVersion {
		return conflict("qualification runtime differs from admitted native runtime")
	}
	gates := qualification.Gates
	if gates.CandidateID != attempt.CandidateID || gates.SourceDigest != input.Artifacts.Artifact.SourceDigest || gates.BindingGeneration != input.Plan.Execution.BindingDigest || gates.RuntimeVersion != input.RuntimeVersion || gates.DuckDBVersion != qualification.Runtime.DuckDBRuntime {
		return conflict("qualification gate identity differs from candidate inputs")
	}
	return nil
}

func validateNativeArtifactObject(artifact release.NativeArtifactObjectEvidence, digest string) error {
	if artifact.Locator != "serving-artifacts/"+strings.TrimPrefix(digest, "sha256:")+".tar.gz" || artifact.Locator != strings.TrimSpace(artifact.Locator) {
		return fmt.Errorf("%w: native serving artifact locator is invalid", deploymentnative.ErrInvalid)
	}
	if err := validateCanonicalText(artifact.StorageSecurityDomain, "artifact storage security domain", 512); err != nil {
		return err
	}
	if artifact.ContentType != projectbundle.BundleContentType {
		return fmt.Errorf("%w: native serving artifact content type must be %q", deploymentnative.ErrInvalid, projectbundle.BundleContentType)
	}
	if err := validateDigest(artifact.MetadataDigest, "artifact metadata digest"); err != nil {
		return err
	}
	if artifact.SizeBytes <= 0 || artifact.SizeBytes > projectbundle.MaxBundleBytes {
		return fmt.Errorf("%w: native serving artifact size is invalid", deploymentnative.ErrInvalid)
	}
	return nil
}

func canonicalBuildMarker(build NativePhysicalBuildEvidence) (catalogartifact.CommitMarker, json.RawMessage, error) {
	marker, err := build.Marker.Normalize()
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("%w: commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil || len(build.CanonicalMarkerJSON) == 0 || !bytes.Equal(build.CanonicalMarkerJSON, []byte(canonical)) {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("%w: native canonical commit marker evidence differs", deploymentnative.ErrConflict)
	}
	if parsed, err := catalogartifact.ParseCommitMarker(build.Seal.CommitMarker); err != nil || parsed != marker {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("%w: DuckLake seal commit marker differs", deploymentnative.ErrConflict)
	}
	return marker, json.RawMessage(canonical), nil
}

func validateRuntimeAndCatalogVersions(catalogVersion, extensionVersion string, compatibility ducklakepostgres.RuntimeCompatibility) error {
	gotCatalog, err := canonicalNumericCatalogVersion(catalogVersion)
	if err != nil {
		return fmt.Errorf("%w: catalog version: %v", deploymentnative.ErrInvalid, err)
	}
	wantCatalog, err := canonicalNumericCatalogVersion(compatibility.CatalogFormat)
	if err != nil || gotCatalog != wantCatalog {
		return conflict("DuckLake catalog version differs from runtime compatibility")
	}
	wantExtension, err := canonicalRuntimeComponent("ducklake", compatibility.DuckLakeExtension)
	if err != nil {
		return fmt.Errorf("%w: DuckLake runtime extension: %v", deploymentnative.ErrInvalid, err)
	}
	gotExtension, err := canonicalRuntimeComponent("ducklake", extensionVersion)
	if err != nil || gotExtension != wantExtension {
		return conflict("DuckLake extension version differs from runtime compatibility")
	}
	return nil
}

func compatibilityValidate(value ducklakepostgres.RuntimeCompatibility) error {
	duckDBRuntime, err := canonicalRuntimeComponent("duckdb", value.DuckDBRuntime)
	if err != nil {
		return fmt.Errorf("%w: DuckDB runtime: %v", deploymentnative.ErrInvalid, err)
	}
	if duckDBRuntime != value.DuckDBRuntime {
		return fmt.Errorf("%w: DuckDB runtime is not canonical", deploymentnative.ErrInvalid)
	}
	duckLakeExtension, err := canonicalRuntimeComponent("ducklake", value.DuckLakeExtension)
	if err != nil {
		return fmt.Errorf("%w: DuckLake extension: %v", deploymentnative.ErrInvalid, err)
	}
	if duckLakeExtension != value.DuckLakeExtension {
		return fmt.Errorf("%w: DuckLake extension is not canonical", deploymentnative.ErrInvalid)
	}
	if _, err := canonicalNumericCatalogVersion(value.CatalogFormat); err != nil {
		return fmt.Errorf("%w: DuckLake catalog format: %v", deploymentnative.ErrInvalid, err)
	}
	if err := validateDigest(value.CompatibilityDigest, "compatibility digest"); err != nil {
		return err
	}
	if err := validateCanonicalText(value.CatalogSchemaVersion, "catalog schema version", 128); err != nil {
		return err
	}
	return nil
}

func canonicalNumericCatalogVersion(value string) (int64, error) {
	parsed, err := ducklake.CatalogVersionNumber(value)
	if err != nil {
		return 0, errors.New("catalog version must be a canonical positive major version")
	}
	return parsed, nil
}

func validateCanonicalRuntimeText(value, label string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: %s is invalid", deploymentnative.ErrInvalid, label)
	}
	return nil
}

func validateCanonicalText(value, label string, max int) error {
	return validateCanonicalRuntimeText(value, label, max)
}

func validateCanonicalObject(raw json.RawMessage, label string) error {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return fmt.Errorf("%w: %s is required", deploymentnative.ErrInvalid, label)
	}
	var decoded any
	if err := strictjson.DecodeWithOptions(raw, &decoded, strictjson.Options{}); err != nil {
		return fmt.Errorf("%w: %s: %v", deploymentnative.ErrInvalid, label, err)
	}
	object, ok := decoded.(map[string]any)
	if !ok || len(object) == 0 {
		return fmt.Errorf("%w: %s must be a non-empty JSON object", deploymentnative.ErrInvalid, label)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal([]byte(value), canonical) {
		return fmt.Errorf("%w: %s must be canonical JSON", deploymentnative.ErrInvalid, label)
	}
	return nil
}
