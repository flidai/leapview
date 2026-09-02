package module

// Native candidate preview runtime preparation is intentionally kept apart
// from the native candidate read resolver. The resolver projects durable
// evidence; this seam rehydrates the exact immutable serving bundle and
// registers a process-local runtime only when a ready preview is requested.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	nativepostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

// nativeCandidateGenerationResolver is optional on NativeDeliveryReader so
// existing read-only test doubles and non-native callers do not gain a new
// obligation. The production PostgreSQL reader implements this exact
// cardinality-checked lookup.
type nativeCandidateGenerationResolver interface {
	ResolveCandidateGeneration(context.Context, string) (nativepostgres.CandidateGenerationResolution, error)
}

// nativeQualificationEvidenceEnvelope mirrors the bounded native seal
// evidence only at the fields needed by preview preparation. The complete
// envelope is decoded with unknown fields rejected so a future writer cannot
// silently widen the authenticated surface. The nested GateEvidence is
// decoded as the release-owned contract and is validated/canonicalized below.
type nativeQualificationEvidenceEnvelope struct {
	SchemaVersion          int                          `json:"schema_version"`
	CandidateID            string                       `json:"candidate_id"`
	AttemptID              string                       `json:"attempt_id"`
	PhysicalPoolID         string                       `json:"physical_pool_id"`
	CatalogID              string                       `json:"catalog_id"`
	SnapshotID             int64                        `json:"snapshot_id"`
	ObjectRoot             string                       `json:"object_root"`
	RelationNamespace      string                       `json:"relation_namespace"`
	RelationManifestDigest string                       `json:"relation_manifest_digest"`
	ClosureDigest          string                       `json:"closure_digest"`
	Runtime                nativePreviewRuntimeEvidence `json:"runtime"`
	Gates                  release.GateEvidence         `json:"gates"`
	Digest                 string                       `json:"digest"`
}

type nativePreviewRuntimeEvidence struct {
	SnapshotID           int64  `json:"snapshot_id"`
	CatalogType          string `json:"catalog_type"`
	DataPath             string `json:"data_path"`
	MetadataSchema       string `json:"metadata_schema"`
	DuckDBRuntime        string `json:"duckdb_runtime"`
	DuckLakeExtension    string `json:"ducklake_extension"`
	CatalogFormat        string `json:"catalog_format"`
	CompatibilityDigest  string `json:"compatibility_digest"`
	CatalogSchemaVersion string `json:"catalog_schema_version"`
}

// EnsureNativeCandidateRuntime lazily prepares the native candidate runtime
// for one authenticated owner. It is a no-op for the explicit legacy SQLite
// path, whose candidate lifecycle prepares runtimes during synchronization.
// Every native input is re-read from durable evidence, making repeated calls
// after a restart safe and preventing callers from supplying runtime state.
func (m *Module) EnsureNativeCandidateRuntime(ctx context.Context, candidateID, principalID string) (resultErr error) {
	defer func() {
		if resultErr == nil {
			return
		}
		m.candidateLogger().ErrorContext(
			ctx,
			"native candidate runtime preparation failed",
			"candidate", strings.TrimSpace(candidateID),
			"error", resultErr,
		)
	}()
	if m == nil {
		return deployment.ErrCandidateUnavailable
	}
	// NativeDeliveryReader is only installed for the clean-slate PostgreSQL
	// path. Do not alter legacy candidate behavior or attempt to prepare a
	// ready candidate through the SQLite service.
	if m.nativeDeliveryReader == nil {
		return nil
	}
	if m.candidateRuntimes == nil || m.candidateArtifactRecovery == nil {
		return deployment.ErrCandidateUnavailable
	}
	if m.candidateAdmission == nil {
		return deployment.ErrCandidateUnavailable
	}
	if m.nativeMetadataSchema == nil {
		return deployment.ErrCandidateUnavailable
	}
	candidateID = strings.TrimSpace(candidateID)
	principalID = strings.TrimSpace(principalID)
	if candidateID == "" || principalID == "" {
		return deployment.ErrCandidateNotFound
	}

	reader := m.nativeDeliveryReader
	candidate, err := reader.Candidate(ctx, candidateID)
	if err != nil {
		return nativeCandidateRuntimeReadError(err)
	}
	if candidate.CandidateID != candidateID || candidate.TargetID == "" || (m.instanceID != "" && candidate.TargetID != m.instanceID) || candidate.PlanID == "" || candidate.SnapshotSealID == "" {
		return deployment.ErrCandidateNotFound
	}
	if candidate.CandidateRevision < 1 || candidate.CreatedAt.IsZero() || candidate.QualifiedAt.IsZero() || !candidate.RetiredAt.IsZero() {
		return nativeCandidateRuntimeUnavailable("candidate lifecycle evidence is incomplete")
	}
	status := strings.ToLower(strings.TrimSpace(candidate.Status))
	if status != "qualified" && status != "ready" {
		return nativeCandidateRuntimeUnavailable("candidate is not qualified")
	}
	if err := validateNativePreviewUUID(candidateID, "candidate id"); err != nil {
		return nativeCandidateRuntimeUnavailable(err.Error())
	}

	seal, err := reader.SnapshotSeal(ctx, candidate.SnapshotSealID)
	if err != nil {
		return nativeCandidateRuntimeReadError(err)
	}
	if seal.SealID != candidate.SnapshotSealID || seal.CandidateID != candidate.CandidateID || seal.AttemptID == "" || (candidate.AttemptID != "" && candidate.AttemptID != seal.AttemptID) {
		return nativeCandidateRuntimeUnavailable("candidate snapshot seal identity is inconsistent")
	}
	attempt, err := reader.BuildAttempt(ctx, seal.AttemptID)
	if err != nil {
		return nativeCandidateRuntimeReadError(err)
	}
	if attempt.AttemptID != seal.AttemptID || attempt.CandidateID != candidate.CandidateID || attempt.PlanID != candidate.PlanID || attempt.State != nativepostgres.AttemptCommitted || attempt.OwnerID == "" || attempt.OwnerID != strings.TrimSpace(attempt.OwnerID) {
		return nativeCandidateRuntimeUnavailable("candidate build attempt identity is incomplete")
	}
	// A foreign candidate remains indistinguishable from a missing candidate.
	if attempt.OwnerID != principalID {
		return deployment.ErrCandidateNotFound
	}

	plan, err := nativeReadPlan(ctx, reader, candidate.PlanID)
	if err != nil {
		return nativeCandidateRuntimeReadError(err)
	}
	if err := validateNativePreviewPlan(m, candidate, attempt, seal, plan); err != nil {
		return nativeCandidateRuntimeUnavailable(err.Error())
	}
	managedPins, err := nativePreviewManagedDataPins(plan)
	if err != nil {
		return nativeCandidateRuntimeUnavailable(err.Error())
	}
	resolver, ok := reader.(nativeCandidateGenerationResolver)
	if !ok {
		return nativeCandidateRuntimeUnavailable("candidate generation resolver is unavailable")
	}
	resolution, err := resolver.ResolveCandidateGeneration(ctx, candidateID)
	if err != nil {
		return nativeCandidateRuntimeReadError(err)
	}
	if resolution.CandidateID != candidate.CandidateID || resolution.TargetID != candidate.TargetID || resolution.PlanID != candidate.PlanID || resolution.SnapshotSealID != seal.SealID || resolution.GenerationCount != 1 || resolution.GenerationID == "" || resolution.Status != candidate.Status || resolution.CandidateRevision != candidate.CandidateRevision || resolution.ArtifactDigest != candidate.ArtifactDigest {
		return nativeCandidateRuntimeUnavailable("candidate generation resolution is inconsistent")
	}
	generation, err := reader.Generation(ctx, resolution.GenerationID)
	if err != nil {
		return nativeCandidateRuntimeReadError(err)
	}
	if err := validateNativePreviewGeneration(m, generation, candidate, attempt, seal, plan); err != nil {
		return nativeCandidateRuntimeUnavailable(err.Error())
	}

	evidence, err := decodeNativePreviewGateEvidence(seal.QualificationEvidence)
	if err != nil {
		return nativeCandidateRuntimeUnavailable(err.Error())
	}
	if evidence.CandidateID != candidate.CandidateID || evidence.AttemptID != attempt.AttemptID || evidence.PhysicalPoolID != seal.PhysicalPoolID || evidence.CatalogID != seal.CatalogID || evidence.SnapshotID != seal.DuckLakeSnapshotID || evidence.ObjectRoot != seal.ObjectRoot || evidence.RelationNamespace != seal.RelationNamespace || evidence.RelationManifestDigest != seal.RelationManifestDigest || evidence.ClosureDigest != seal.ClosureDigest || evidence.Digest != candidate.QualificationDigest || evidence.Runtime.SnapshotID != seal.DuckLakeSnapshotID || evidence.Runtime.CatalogType != "postgres" || evidence.Runtime.DataPath != seal.ObjectRoot || evidence.Runtime.MetadataSchema != m.nativeMetadataSchema(seal.PhysicalPoolID) || evidence.Runtime.CompatibilityDigest != seal.CompatibilityDigest || evidence.Runtime.DuckDBRuntime != seal.DuckDBVersion || evidence.Runtime.DuckLakeExtension != seal.DuckLakeExtensionVersion || evidence.Runtime.CatalogFormat != seal.DuckLakeSpecVersion || evidence.Runtime.CatalogSchemaVersion != seal.CatalogSchemaVersion {
		return nativeCandidateRuntimeUnavailable("candidate qualification evidence identity is inconsistent")
	}
	gate := evidence.Gates
	if gate.CandidateID != candidate.CandidateID || gate.SourceDigest != plan.SourceDigest || gate.RuntimeVersion != seal.RuntimeVersion || (gate.Outcome != release.GateSuccess && gate.Outcome != release.GateWarning) {
		return nativeCandidateRuntimeUnavailable("candidate gate evidence identity is inconsistent")
	}
	admission, err := m.candidateAdmission.AcquireCandidatePreparation(ctx)
	if err != nil || admission == nil {
		return fmt.Errorf("%w: candidate preview preparation admission unavailable", deployment.ErrCandidateUnavailable)
	}
	defer admission.Release()
	if admission.Context() != nil {
		ctx = admission.Context()
	}

	identity, err := projectgraph.NewServingIdentity(plan.ProjectID, plan.Environment, generation.GenerationID)
	if err != nil {
		return nativeCandidateRuntimeUnavailable("candidate serving identity is invalid")
	}
	artifacts, err := m.candidateArtifactRecovery.RecoverCandidateArtifacts(ctx, release.CandidateArtifactRecoveryRequest{
		CandidateID:     candidate.CandidateID,
		ServingIdentity: identity,
		SourceDigest:    plan.SourceDigest,
		ManagedDataPins: managedPins,
		Artifact: release.CandidateArtifactIdentity{
			ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest,
			ServingStateID: generation.GenerationID,
		},
	})
	if err != nil {
		return fmt.Errorf("%w: recover native candidate artifact: %v", deployment.ErrCandidateUnavailable, err)
	}
	if err := validateRecoveredNativePreviewArtifacts(artifacts, identity, candidate, plan, seal, gate); err != nil {
		return nativeCandidateRuntimeUnavailable(err.Error())
	}

	// CandidateRuntimeService owns target connection leasing and runtime-host
	// registration. The durable row is qualified, so use a transient preparing
	// projection solely for this existing preparation contract; no durable
	// status or candidate row is mutated.
	previewCandidate := deployment.Candidate{
		ID: candidate.CandidateID, Key: candidate.CandidateID, TargetID: candidate.TargetID, OwnerID: attempt.OwnerID,
		Scope:          deployment.CandidateScope{ProjectID: plan.ProjectID, Environment: plan.Environment, BaseGenerationID: plan.BaseGenerationID},
		ArtifactDigest: plan.SourceDigest, Status: deployment.CandidatePreparing,
		ExpiresAt: plan.Governance.ExpiresAt.UTC(), CreatedAt: candidate.CreatedAt.UTC(), UpdatedAt: candidate.QualifiedAt.UTC(), Revision: candidate.CandidateRevision,
	}
	_, err = m.candidateRuntimes.Prepare(ctx, deployment.CandidateRuntimeRequest{
		Candidate: previewCandidate, AuthorizationFingerprint: artifacts.AuthorizationFingerprint,
		Generation: deployment.CandidateGenerationRuntime{
			Identity: artifacts.Generation.Identity, ArtifactDigest: artifacts.Generation.ArtifactDigest,
			DataRevision: artifacts.Generation.DataRevision, DataMode: deployment.CandidateDataMode(artifacts.Generation.DataMode),
			Connections:            candidateConnectionRequirements(artifacts.Generation.Connections),
			AuthoredConnections:    candidateAuthoredConnections(artifacts.Generation.AuthoredConnections),
			ManagedDataConnections: candidateManagedDataConnections(artifacts.Generation.ManagedDataPins),
			Extensions:             append([]extension.Evidence(nil), artifacts.Extensions...), Restrictions: candidateRuntimeRestrictions(artifacts.Generation.Restrictions),
			BindingFingerprint: gate.BindingGeneration, GateEvidence: &gate,
		},
	})
	if err != nil {
		return fmt.Errorf("%w: prepare native candidate runtime: %v", deployment.ErrCandidateUnavailable, err)
	}
	return nil
}

func validateNativePreviewPlan(m *Module, candidate nativepostgres.DeliveryCandidate, attempt nativepostgres.DeliveryBuildAttempt, seal nativepostgres.SnapshotSeal, plan deployment.DeliveryPlan) error {
	if plan.ID != candidate.PlanID || plan.TargetID != candidate.TargetID || plan.ProjectID.Validate() != nil || plan.Environment == "" || (m.instanceEnvironment != "" && plan.Environment != string(m.instanceEnvironment)) || plan.SourceDigest == "" || plan.SourceDigest != strings.TrimSpace(plan.SourceDigest) || platformdigest.ValidateSHA256Identity(plan.SourceDigest) != nil || plan.Digest != attempt.PlanDigest || plan.Digest != seal.PlanDigest || plan.Execution.ConfigDigest != seal.CompiledConfigDigest || plan.ServingArtifactDigest != seal.ServingArtifactDigest || candidate.ArtifactDigest != seal.ServingArtifactDigest || seal.ServingArtifactDigest == "" || platformdigest.ValidateSHA256Identity(seal.ServingArtifactDigest) != nil || plan.Governance.ExpiresAt.IsZero() || !plan.Governance.ExpiresAt.Equal(plan.Governance.ExpiresAt.UTC()) || !plan.Governance.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("candidate delivery plan identity or expiry is invalid")
	}
	return nil
}

func nativePreviewManagedDataPins(plan deployment.DeliveryPlan) ([]release.ManagedDataPin, error) {
	result := make([]release.ManagedDataPin, 0, len(plan.Execution.DataInputs))
	seen := make(map[string]struct{}, len(plan.Execution.DataInputs))
	for _, input := range plan.Execution.DataInputs {
		if input.ID == "source-artifact" || input.Mode != deployment.DeliveryDataPinned {
			continue
		}
		if input.ID == "" || input.ID != strings.TrimSpace(input.ID) || input.Revision == "" || input.Revision != strings.TrimSpace(input.Revision) {
			return nil, errors.New("native preview managed-data plan input is invalid")
		}
		if _, exists := seen[input.ID]; exists {
			return nil, errors.New("native preview managed-data plan input is duplicated")
		}
		seen[input.ID] = struct{}{}
		result = append(result, release.ManagedDataPin{ConnectionID: input.ID, RevisionID: input.Revision})
	}
	return result, nil
}

func validateNativePreviewGeneration(m *Module, generation nativepostgres.DeliveryGeneration, candidate nativepostgres.DeliveryCandidate, attempt nativepostgres.DeliveryBuildAttempt, seal nativepostgres.SnapshotSeal, plan deployment.DeliveryPlan) error {
	if generation.GenerationID == "" || generation.GenerationID != strings.TrimSpace(generation.GenerationID) || generation.TargetID != candidate.TargetID || generation.CandidateID != candidate.CandidateID || generation.SnapshotSealID != seal.SealID || generation.PlanID != plan.ID || generation.PlanDigest != plan.Digest || generation.ServingArtifactDigest != seal.ServingArtifactDigest || generation.ArtifactRoot != seal.ArtifactRoot || generation.ArtifactRootDigest != seal.ArtifactRootDigest || generation.CompiledGraphDigest != seal.CompiledGraphDigest || generation.CompiledConfigDigest != seal.CompiledConfigDigest || generation.SecurityDomainFingerprint != seal.SecurityDomainFingerprint || generation.GenerationRevision < 1 || generation.CreatedAt.IsZero() {
		return errors.New("candidate generation identity is inconsistent")
	}
	if err := validateNativePreviewUUID(generation.GenerationID, "generation id"); err != nil {
		return err
	}
	if m.instanceID != "" && generation.TargetID != m.instanceID {
		return errors.New("candidate generation target is outside this instance")
	}
	if attempt.PlanID != generation.PlanID {
		return errors.New("candidate generation attempt binding is inconsistent")
	}
	return nil
}

func validateRecoveredNativePreviewArtifacts(artifacts release.CandidateArtifactSet, identity projectgraph.ServingIdentity, candidate nativepostgres.DeliveryCandidate, plan deployment.DeliveryPlan, seal nativepostgres.SnapshotSeal, gate release.GateEvidence) error {
	if artifacts.AuthorizationFingerprint == "" || artifacts.AuthorizationFingerprint != strings.TrimSpace(artifacts.AuthorizationFingerprint) || platformdigest.ValidateSHA256Identity(artifacts.AuthorizationFingerprint) != nil || artifacts.AuthorizationFingerprint != seal.SecurityDomainFingerprint {
		return errors.New("recovered candidate authorization evidence is invalid")
	}
	if artifacts.Artifact.SourceDigest != plan.SourceDigest || artifacts.Artifact.ContentDigest != seal.ServingArtifactDigest || artifacts.Generation.Identity != identity || artifacts.Generation.ServingArtifactID != seal.ServingArtifactID || artifacts.Generation.ArtifactDigest != seal.ServingArtifactDigest || artifacts.Generation.DataRevision == "" || artifacts.Generation.DataRevision != strings.TrimSpace(artifacts.Generation.DataRevision) || artifacts.Compiler.Graph.Digest() != seal.CompiledGraphDigest || seal.ArtifactRoot != "" && artifacts.Generation.NativeArtifact.Locator != seal.ArtifactRoot || seal.ArtifactRootDigest != "" && artifacts.Generation.ArtifactDigest != seal.ArtifactRootDigest {
		return errors.New("recovered candidate artifact identity is inconsistent")
	}
	if artifacts.Generation.Identity.ProjectID != plan.ProjectID || artifacts.Generation.Identity.Environment != plan.Environment || candidate.ArtifactDigest != seal.ServingArtifactDigest || gate.SourceDigest != plan.SourceDigest {
		return errors.New("recovered candidate project or source identity differs")
	}
	return nil
}

func decodeNativePreviewGateEvidence(raw json.RawMessage) (nativeQualificationEvidenceEnvelope, error) {
	if len(raw) == 0 {
		return nativeQualificationEvidenceEnvelope{}, errors.New("candidate qualification evidence is empty")
	}
	var envelope nativeQualificationEvidenceEnvelope
	if err := strictjson.DecodeWithOptions(raw, &envelope, strictjson.Options{MaxBytes: 128 << 10, MaxDepth: 32, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: false}); err != nil {
		return nativeQualificationEvidenceEnvelope{}, fmt.Errorf("decode candidate qualification evidence: %w", err)
	}
	canonical, err := envelope.Gates.Canonical()
	if err != nil {
		return nativeQualificationEvidenceEnvelope{}, fmt.Errorf("validate candidate gate evidence: %w", err)
	}
	envelope.Gates = canonical
	if envelope.SchemaVersion != 1 || envelope.CandidateID == "" || envelope.AttemptID == "" || envelope.PhysicalPoolID == "" || envelope.CatalogID == "" || envelope.SnapshotID <= 0 || envelope.ObjectRoot == "" || envelope.RelationNamespace == "" || envelope.RelationManifestDigest == "" || envelope.ClosureDigest == "" || envelope.Digest == "" {
		return nativeQualificationEvidenceEnvelope{}, errors.New("candidate qualification evidence identity is incomplete")
	}
	suppliedDigest := envelope.Digest
	envelope.Digest = ""
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nativeQualificationEvidenceEnvelope{}, fmt.Errorf("canonicalize candidate qualification evidence: %w", err)
	}
	digest := sha256.Sum256(encoded)
	if suppliedDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return nativeQualificationEvidenceEnvelope{}, errors.New("candidate qualification evidence digest mismatch")
	}
	envelope.Digest = suppliedDigest
	return envelope, nil
}

func nativeCandidateRuntimeReadError(err error) error {
	if errors.Is(err, nativepostgres.ErrNotFound) || errors.Is(err, deployment.ErrNotFound) {
		return fmt.Errorf("%w: native candidate evidence not found", deployment.ErrCandidateNotFound)
	}
	return fmt.Errorf("%w: native candidate evidence read failed: %v", deployment.ErrCandidateUnavailable, err)
}

func nativeCandidateRuntimeUnavailable(detail string) error {
	return fmt.Errorf("%w: %s", deployment.ErrCandidateUnavailable, detail)
}

func validateNativePreviewUUID(value, label string) error {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value || id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		return fmt.Errorf("%s must be a canonical UUIDv7", label)
	}
	return nil
}
