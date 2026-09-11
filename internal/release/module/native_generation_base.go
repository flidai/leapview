package module

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

// nativeGenerationBase loads one exact serving generation from the immutable
// native authorities. Unlike compatibility paths, every byte and identity is
// checked against serving state, provenance, and object-store metadata.
func (service *nativeCandidateArtifactPhases) nativeGenerationBase(ctx context.Context, identity *projectgraph.ServingIdentity) (candidateGenerationBase, error) {
	if identity == nil {
		return candidateGenerationBase{pins: map[string]string{}}, nil
	}
	if service == nil || service.states == nil || service.provenance == nil || service.artifacts == nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(errors.New("native serving-state base authority is unavailable"))
	}
	if err := identity.Validate(); err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	parsedGenerationID, err := validateNativeGenerationID(identity.GenerationID, true)
	if err != nil || parsedGenerationID.String() != identity.GenerationID {
		if err == nil {
			err = errors.New("native base generation identity must be a canonical UUIDv7")
		}
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	state, err := service.states.ByID(ctx, servingstate.ID(identity.GenerationID))
	if errors.Is(err, servingstate.ErrNotFound) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base generation not found"))
	}
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if state.ID != servingstate.ID(identity.GenerationID) || state.ProjectID != identity.ProjectID || state.Environment != servingstate.Environment(identity.Environment) || state.Status != servingstate.StatusActive || state.DuckLakeSnapshotID <= 0 || state.ProjectID.Validate() != nil || servingstate.ValidateEnvironment(state.Environment) != nil || platformdigest.ValidateSHA256Identity(state.ProjectDigest) != nil || platformdigest.ValidateSHA256Identity(state.Digest) != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base generation identity mismatch"))
	}
	baseProvenance, err := service.provenance.ProvenanceForServingState(ctx, *identity)
	if errors.Is(err, release.ErrNotFound) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance not found"))
	}
	if errors.Is(err, release.ErrConflict) || errors.Is(err, release.ErrInvalid) || errors.Is(err, release.ErrProvenanceInvalid) {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if err := baseProvenance.Validate(); err != nil || baseProvenance.Plan.Identity != *identity {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance identity mismatch"))
	}
	artifact, err := service.states.ArtifactByServingState(ctx, state.ID)
	if errors.Is(err, servingstate.ErrNotFound) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving artifact not found"))
	}
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactUnavailable(err)
	}
	if artifact.ServingStateID != state.ID || artifact.ID != nativeServingArtifactID(artifact.Digest) || artifact.Path != "" || artifact.Format != servingstate.ArtifactBundleFormat || platformdigest.ValidateSHA256Identity(artifact.Digest) != nil || artifact.Digest != state.Digest || artifact.ManifestJSON == "" || artifact.ManifestJSON != state.ManifestJSON || artifact.SizeBytes < 1 || artifact.SizeBytes > projectbundle.MaxBundleBytes || artifact.ContentType != nativeServingArtifactContentType || !validNativeStorageDomain(artifact.StorageSecurityDomain) || artifact.StorageSecurityDomain != service.storageDomain || platformdigest.ValidateSHA256Identity(artifact.MetadataDigest) != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving artifact identity or evidence mismatch"))
	}
	locator := nativeServingArtifactKey(artifact.Digest)
	if locator == "" || artifact.Locator != locator || artifact.Locator != strings.TrimSpace(artifact.Locator) {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving artifact locator is not canonical"))
	}
	durableManifestJSON, err := projectbundle.CanonicalManifestJSON(artifact.ManifestJSON)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if baseProvenance.Artifact.ContentDigest != artifact.Digest || baseProvenance.Artifact.ProjectDigest != state.ProjectDigest {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance content identity mismatch"))
	}
	object, err := service.artifacts.Open(ctx, artifact.Locator)
	if err != nil {
		return candidateGenerationBase{}, nativeCandidateObjectError(err)
	}
	if object.Body == nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving artifact object body is nil"))
	}
	defer object.Body.Close()
	expectedMetadata := platformobjectstore.ObjectMetadata{StorageSecurityDomain: artifact.StorageSecurityDomain, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes, ContentType: artifact.ContentType, MetadataDigest: artifact.MetadataDigest}
	if err := validateNativeServingArtifactInfo(object.Info, artifact.Locator, expectedMetadata); err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	validation, compiled, err := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	validatedManifestJSON, err := projectbundle.CanonicalManifestJSON(validation.ManifestJSON)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if validation.Digest != artifact.Digest || validation.BundleDigest != state.ProjectDigest || !bytes.Equal(validatedManifestJSON, durableManifestJSON) || compiled.BundleDigest != state.ProjectDigest {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving artifact content identity mismatch"))
	}
	baseArtifact, err := projectartifact.NewSourceBundle(compiled.Graph, compiled.Manifest)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if baseArtifact.Digest() != state.ProjectDigest {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base source bundle identity mismatch"))
	}
	if baseProvenance.Artifact.CompilerVersion != projectartifact.CompilerVersion || baseProvenance.Artifact.SchemaVersion != baseArtifact.Version() {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance compiler identity mismatch"))
	}
	for _, document := range []struct{ label, persisted string }{
		{label: "access policy", persisted: state.AccessPolicyJSON},
		{label: "dashboard publications", persisted: state.DashboardPublicationsJSON},
		{label: "dashboard appearances", persisted: state.DashboardAppearancesJSON},
	} {
		if err := validateNativeServingDocument(document.persisted, document.label); err != nil {
			return candidateGenerationBase{}, candidateArtifactInvalid(err)
		}
	}
	pins := make(map[string]string, len(baseProvenance.Plan.ManagedDataPins))
	for _, pin := range baseProvenance.Plan.ManagedDataPins {
		connection, revision := pin.ConnectionID, pin.RevisionID
		if connection != strings.TrimSpace(connection) || revision != strings.TrimSpace(revision) || connection == "" || revision == "" {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains noncanonical managed-data pins"))
		}
		if _, exists := pins[connection]; exists {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains duplicate managed-data pins"))
		}
		pins[connection] = revision
	}
	dataRevision := strings.TrimSpace(baseProvenance.Plan.DataRevision)
	if dataRevision == "" && state.DuckLakeSnapshotID > 0 {
		dataRevision = fmt.Sprintf("snapshot:%d", state.DuckLakeSnapshotID)
	}
	baseBindings := make(map[string]string, len(baseProvenance.Plan.Bindings))
	for _, binding := range baseProvenance.Plan.Bindings {
		connectionID, kind := strings.TrimSpace(binding.ConnectionID), strings.TrimSpace(binding.ConnectorKind)
		if connectionID == "" || kind == "" || connectionID != binding.ConnectionID || kind != binding.ConnectorKind {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains noncanonical binding evidence"))
		}
		if existing, ok := baseBindings[connectionID]; ok && existing != kind {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains conflicting binding evidence"))
		}
		if _, exists := baseBindings[connectionID]; exists {
			return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("active generation contains duplicate binding evidence"))
		}
		baseBindings[connectionID] = kind
	}
	if len(baseBindings) == 0 {
		activations, activationErr := baseArtifact.ConnectionActivations()
		if activationErr != nil {
			return candidateGenerationBase{}, candidateArtifactInvalid(activationErr)
		}
		baseBindings = candidateActivationBindings(activations)
	}
	relationContext, err := candidateRelationContexts(pins, baseArtifact, baseBindings)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	return candidateGenerationBase{graph: validation.Graph, artifact: baseArtifact, pins: pins, bindings: baseBindings, snapshotID: state.DuckLakeSnapshotID, dataRevision: dataRevision, relationContext: relationContext, gateEvidence: baseProvenance.Plan.GateEvidence, active: true}, nil
}
