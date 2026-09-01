package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

// nativeCandidateArtifactPhases keeps source evidence and serving artifact
// storage behind neutral ports. It never opens a database or a local
// filesystem: materialization is one immutable object write and hydration is
// one exact object read.
type nativeCandidateArtifactPhases struct {
	reader               project.CandidateSourceObjectReader
	states               ServingStateReader
	provenance           release.ServingStateProvenanceRepository
	artifacts            platformobjectstore.ImmutableStore
	storageDomain        string
	environment          servingstate.Environment
	pins                 ManagedDataPins
	extensionPreparation extension.Preparation
}

var _ candidateArtifactPhases = (*nativeCandidateArtifactPhases)(nil)

const (
	maxNativeInspectArtifactBytes    int64 = 64 << 20
	maxNativeInspectSourceBytes      int64 = 64 << 20
	maxNativeInspectSourceFiles            = 10_000
	maxNativeRecoveryIDBytes               = 255
	nativeServingArtifactContentType       = projectbundle.BundleContentType
	nativeServingArtifactPrefix            = "serving-artifacts/"
	nativeServingArtifactSuffix            = ".tar.gz"
	maxNativeServingDocumentBytes    int64 = 1 << 20
)

func (service *nativeCandidateArtifactPhases) InspectCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest) (release.CandidateArtifactSet, error) {
	if service == nil || service.reader == nil {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if err := validateNativeInspectRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, err
	}

	scope := project.CandidateSourceScope{ProjectID: request.Scope.ProjectID, OwnerID: request.OwnerID}
	artifactBody, err := service.reader.OpenProjectArtifact(ctx, scope, request.Source.ArtifactDigest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	artifactBytes, err := readNativeInspectBody(artifactBody, maxNativeInspectArtifactBytes, -1)
	if err != nil {
		if errors.Is(err, errNativeInspectLimit) {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
	}
	compiledProject, err := projectartifact.Decode(artifactBytes)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if compiledProject.ProjectID() != request.Scope.ProjectID || compiledProject.Digest() != request.Source.ProjectDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("retained project artifact does not match synchronized project"))
	}

	files, err := service.readSourceObjects(ctx, scope, request.Source)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	// The retained source tree and the retained compiler artifact are one
	// synchronized identity. Compile the logical bytes once to reject a reader
	// that serves an unrelated source set under the requested digest.
	authoredProject, err := projectcompiler.CompileProjectFiles(files, request.Source.ProjectFile)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if authoredProject.ProjectID() != compiledProject.ProjectID() || authoredProject.Digest() != compiledProject.Digest() {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("retained source files do not match project artifact"))
	}

	// Resolve the exact active compiler artifact through the serving-state,
	// provenance, and immutable-object authorities. A target with no active
	// generation plans against an empty graph.
	baseIdentity, err := request.Scope.BaseIdentity()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	base, err := service.nativeGenerationBase(ctx, baseIdentity)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	var plan projectcompiler.ProjectPlan
	if base.active {
		plan, err = projectcompiler.PlanProjectFilesAgainstArtifact(files, request.Source.ProjectFile, base.artifact)
	} else {
		plan, err = projectcompiler.PlanProjectFilesAgainstGraph(files, request.Source.ProjectFile, projectgraph.ProjectGraph{})
	}
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	legacy := &candidateArtifactService{pins: service.pins, extensionPreparation: service.extensionPreparation}
	result, err := legacy.inspectCandidateProjectPlan(ctx, request, compiledProject, plan, base)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := retainNativeServingDocuments(&result.Generation, compiledProject); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	// Planning is deliberately read-only, but it still has to bind the exact
	// immutable serving identity that a later materialization must reproduce.
	// Pack the generated serving bundle deterministically without retaining its
	// bytes so the plan can retain its content digest and manifest without
	// writing an object or opening a serving-state transaction. The source
	// artifact digest remains separate provenance evidence on
	// result.Artifact.SourceDigest.
	bundleManifest, bundleDigest, err := projectbundle.PackCompiledProject(compiledProject, plan, io.Discard)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if bundleDigest == "" || platformdigest.ValidateSHA256Identity(bundleDigest) != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("planned native serving artifact identity is invalid"))
	}
	manifestJSON, err := nativeBundleManifestJSON(bundleManifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result.Artifact.ContentDigest = bundleDigest
	result.Generation.BundleManifestJSON = manifestJSON
	result.Generation.ServingArtifactID = nativeServingArtifactID(bundleDigest)
	result.Generation.ArtifactDigest = bundleDigest
	return result, nil
}

// nativeGenerationBase loads one exact serving generation from the immutable
// native authorities.  Unlike the legacy filesystem path, every byte and
// identity is checked against the serving-state row, its provenance, and the
// object-store metadata before the compiled project can affect planning.
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
	if err := validateNativeBundleManifestJSON(artifact.ManifestJSON); err != nil {
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
	if validation.Digest != artifact.Digest || validation.ProjectID != identity.ProjectID.String() || validation.ProjectDigest != state.ProjectDigest || validation.ManifestJSON != artifact.ManifestJSON || compiled.ProjectID != identity.ProjectID || compiled.ProjectDigest != state.ProjectDigest {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving artifact content identity mismatch"))
	}
	baseArtifact, err := projectartifact.NewProject(compiled.Graph, compiled.Manifest)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if baseArtifact.ProjectID() != identity.ProjectID || baseArtifact.Digest() != state.ProjectDigest {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base project identity mismatch"))
	}
	if baseProvenance.Artifact.CompilerVersion != projectartifact.CompilerVersion || baseProvenance.Artifact.SchemaVersion != baseArtifact.Version() {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base provenance compiler identity mismatch"))
	}
	accessPolicyJSON, publicationsJSON, appearancesJSON, err := nativeServingDocuments(baseArtifact)
	if err != nil {
		return candidateGenerationBase{}, candidateArtifactInvalid(err)
	}
	if state.AccessPolicyJSON != accessPolicyJSON || state.DashboardPublicationsJSON != publicationsJSON || state.DashboardAppearancesJSON != appearancesJSON {
		return candidateGenerationBase{}, candidateArtifactInvalid(errors.New("candidate base serving policy identity mismatch"))
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
		connectionID := strings.TrimSpace(binding.ConnectionID)
		kind := strings.TrimSpace(binding.ConnectorKind)
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

func (service *nativeCandidateArtifactPhases) MaterializeCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) (release.CandidateArtifactSet, error) {
	if service == nil || service.artifacts == nil || service.storageDomain == "" {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if !validNativeStorageDomain(service.storageDomain) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native candidate artifact storage security domain is invalid"))
	}
	if err := validateNativeInspectRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	generationID, err := validateNativeGenerationID(request.GenerationID, true)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if err := validateNativeInspectedEvidence(request, inspected); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	compiledProject := inspected.Compiler.Artifact
	plan := inspected.Compiler.Plan
	var content bytes.Buffer
	manifest, digest, err := projectbundle.PackCompiledProject(compiledProject, plan, &content)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	manifestJSON, err := nativeBundleManifestJSON(manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if digest == "" || platformdigest.ValidateSHA256Identity(digest) != nil || int64(content.Len()) <= 0 || int64(content.Len()) > projectbundle.MaxBundleBytes {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("packed native serving artifact identity is invalid"))
	}
	if inspected.Artifact.ContentDigest != "" && inspected.Artifact.ContentDigest != digest || inspected.Generation.ArtifactDigest != "" && inspected.Generation.ArtifactDigest != digest || inspected.Generation.ServingArtifactID != "" && inspected.Generation.ServingArtifactID != nativeServingArtifactID(digest) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving artifact identity changed"))
	}
	if inspected.Generation.BundleManifestJSON != "" {
		if err := validateNativeBundleManifestJSON(inspected.Generation.BundleManifestJSON); err != nil || inspected.Generation.BundleManifestJSON != manifestJSON {
			if err == nil {
				err = errors.New("inspected native serving artifact bundle manifest changed")
			}
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
		}
	}
	accessPolicyJSON, publicationsJSON, appearancesJSON, err := nativeServingDocuments(compiledProject)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if err := validateNativeServingDocuments(inspected.Generation, accessPolicyJSON, publicationsJSON, appearancesJSON); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	existingIdentity := inspected.Generation.Identity
	if existingIdentity.GenerationID != "" || existingIdentity.ProjectID != "" || existingIdentity.Environment != "" {
		expectedInspectID := "inspect-" + shortCandidateDigest(request.CandidateID)
		if (existingIdentity.GenerationID != expectedInspectID && existingIdentity.GenerationID != generationID.String()) || existingIdentity.ProjectID != request.Scope.ProjectID || existingIdentity.Environment != request.Scope.Environment {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving state identity changed"))
		}
	}
	key := nativeServingArtifactKey(digest)
	metadata := platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: service.storageDomain,
		Digest:                digest,
		SizeBytes:             int64(content.Len()),
		ContentType:           nativeServingArtifactContentType,
		MetadataDigest:        nativeServingArtifactMetadataDigest(),
	}
	info, err := service.putServingArtifact(ctx, key, bytes.NewReader(content.Bytes()), metadata, request.Scope.ProjectID.String())
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := validateNativeServingArtifactInfo(info, key, metadata); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	objectEvidence := nativeArtifactObjectEvidence(info)
	// Inspection computes the serving identity in memory but deliberately has
	// no object metadata.  Accept that empty evidence only for the inspect
	// generation; once a concrete serving generation is supplied, a missing
	// metadata binding is tampering and must fail closed.
	allowInspectOnly := strings.HasPrefix(inspected.Generation.Identity.GenerationID, "inspect-")
	if err := validateNativeArtifactObjectEvidence(inspected.Generation.NativeArtifact, objectEvidence, allowInspectOnly); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	identity, err := projectgraph.NewServingIdentity(request.Scope.ProjectID, request.Scope.Environment, generationID.String())
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result := inspected
	result.Artifact.ContentDigest = digest
	result.Generation.BundleManifestJSON = manifestJSON
	result.Generation.AccessPolicyJSON = accessPolicyJSON
	result.Generation.DashboardPublicationsJSON = publicationsJSON
	result.Generation.DashboardAppearancesJSON = appearancesJSON
	result.Generation.NativeArtifact = objectEvidence
	result.Generation.Identity = identity
	result.Generation.ServingArtifactID = nativeServingArtifactID(digest)
	result.Generation.ArtifactDigest = digest
	if err := result.Generation.Identity.Validate(); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return result, nil
}

func (service *nativeCandidateArtifactPhases) HydrateCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet, identity release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error) {
	if service == nil || service.artifacts == nil || service.storageDomain == "" {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if !validNativeStorageDomain(service.storageDomain) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native candidate artifact storage security domain is invalid"))
	}
	if err := validateNativeInspectRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	generationID, err := validateNativeGenerationID(request.GenerationID, true)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if err := validateNativeInspectedEvidence(request, inspected); err != nil {
		return release.CandidateArtifactSet{}, err
	}
	if err := validateNativeArtifactIdentity(identity); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if identity.ServingStateID != generationID.String() {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving state identity does not match candidate artifact"))
	}
	if inspected.Artifact.ContentDigest != "" && inspected.Artifact.ContentDigest != identity.ServingArtifactDigest || inspected.Generation.ArtifactDigest != "" && inspected.Generation.ArtifactDigest != identity.ServingArtifactDigest || inspected.Generation.ServingArtifactID != "" && inspected.Generation.ServingArtifactID != identity.ServingArtifactID {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving artifact identity changed"))
	}
	expectedInspectID := "inspect-" + shortCandidateDigest(request.CandidateID)
	if inspected.Generation.Identity.GenerationID != expectedInspectID && inspected.Generation.Identity.GenerationID != identity.ServingStateID {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving state identity changed"))
	}
	key := nativeServingArtifactKey(identity.ServingArtifactDigest)
	metadata := platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: service.storageDomain,
		Digest:                identity.ServingArtifactDigest,
		ContentType:           nativeServingArtifactContentType,
		MetadataDigest:        nativeServingArtifactMetadataDigest(),
	}
	object, err := service.artifacts.Open(ctx, key)
	if err != nil {
		return release.CandidateArtifactSet{}, nativeCandidateObjectError(err)
	}
	if object.Body == nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact object body is nil"))
	}
	defer object.Body.Close()
	if object.Info.SizeBytes <= 0 || object.Info.SizeBytes > projectbundle.MaxBundleBytes {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact object size is invalid"))
	}
	metadata.SizeBytes = object.Info.SizeBytes
	if err := validateNativeServingArtifactInfo(object.Info, key, metadata); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	objectEvidence := nativeArtifactObjectEvidence(object.Info)
	allowInspectOnly := strings.HasPrefix(inspected.Generation.Identity.GenerationID, "inspect-")
	if err := validateNativeArtifactObjectEvidence(inspected.Generation.NativeArtifact, objectEvidence, allowInspectOnly); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	validation, compiled, err := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if err := validateNativeBundleManifestJSON(validation.ManifestJSON); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if inspected.Generation.BundleManifestJSON != "" && inspected.Generation.BundleManifestJSON != validation.ManifestJSON {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("inspected native serving artifact bundle manifest changed"))
	}
	if validation.Digest != identity.ServingArtifactDigest || validation.ProjectID != request.Scope.ProjectID.String() || validation.ProjectDigest != request.Source.ProjectDigest || compiled.ProjectID != request.Scope.ProjectID || compiled.ProjectDigest != request.Source.ProjectDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact compiled identity mismatch"))
	}
	if compiled.GraphDigest != inspected.Compiler.Graph.Digest() || !sameNativeJSON(compiled.Plan, inspected.Compiler.Plan) || !sameNativeJSON(compiled.Manifest, inspected.Compiler.Manifest) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native serving artifact compiler evidence mismatch"))
	}
	accessPolicyJSON, publicationsJSON, appearancesJSON, err := nativeServingDocumentsFromManifest(compiled.Manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if err := validateNativeServingDocuments(inspected.Generation, accessPolicyJSON, publicationsJSON, appearancesJSON); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	canonicalProject, err := projectartifact.NewProject(compiled.Graph, compiled.Manifest)
	if err != nil || canonicalProject.ProjectID() != request.Scope.ProjectID || canonicalProject.Digest() != request.Source.ProjectDigest {
		if err == nil {
			err = errors.New("native serving artifact project digest mismatch")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	var repacked bytes.Buffer
	_, repackedDigest, err := projectbundle.PackCompiledProject(canonicalProject, compiled.Plan, &repacked)
	if err != nil || repackedDigest != identity.ServingArtifactDigest || int64(repacked.Len()) != object.Info.SizeBytes {
		if err == nil {
			err = errors.New("native serving artifact canonical bytes do not match bound object")
		}
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result := inspected
	result.Artifact.ContentDigest = identity.ServingArtifactDigest
	result.Generation.BundleManifestJSON = validation.ManifestJSON
	result.Generation.AccessPolicyJSON = accessPolicyJSON
	result.Generation.DashboardPublicationsJSON = publicationsJSON
	result.Generation.DashboardAppearancesJSON = appearancesJSON
	result.Generation.NativeArtifact = objectEvidence
	result.Compiler.Graph = compiled.Graph
	result.Compiler.Manifest = compiled.Manifest
	result.Compiler.Plan = compiled.Plan
	result.Compiler.Artifact = canonicalProject
	result.Generation.Identity, err = projectgraph.NewServingIdentity(request.Scope.ProjectID, request.Scope.Environment, identity.ServingStateID)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	result.Generation.ServingArtifactID = identity.ServingArtifactID
	result.Generation.ArtifactDigest = identity.ServingArtifactDigest
	if err := result.Generation.Identity.Validate(); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	return result, nil
}

// RecoverCandidateArtifacts reloads one immutable serving bundle for native
// physical-build recovery. It intentionally has no source reader, serving
// state writer, provenance reader, or materialization dependency: the bundle
// and its provider metadata are the sole recovery inputs.
func (service *nativeCandidateArtifactPhases) RecoverCandidateArtifacts(ctx context.Context, request release.CandidateArtifactRecoveryRequest) (release.CandidateArtifactSet, error) {
	if service == nil || service.artifacts == nil || service.storageDomain == "" {
		return release.CandidateArtifactSet{}, release.ErrCandidateArtifactUnavailable
	}
	if !validNativeStorageDomain(service.storageDomain) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native candidate artifact storage security domain is invalid"))
	}
	if err := validateNativeRecoveryRequest(request, service.environment); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}

	digest := request.Artifact.ServingArtifactDigest
	key := nativeServingArtifactKey(digest)
	object, err := service.artifacts.Open(ctx, key)
	if err != nil {
		return release.CandidateArtifactSet{}, nativeCandidateObjectError(err)
	}
	if object.Body == nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered serving artifact object body is nil"))
	}
	defer object.Body.Close()
	if object.Info.SizeBytes <= 0 || object.Info.SizeBytes > projectbundle.MaxBundleBytes {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered serving artifact object size is invalid"))
	}
	expectedMetadata := platformobjectstore.ObjectMetadata{
		StorageSecurityDomain: service.storageDomain,
		Digest:                digest,
		SizeBytes:             object.Info.SizeBytes,
		ContentType:           nativeServingArtifactContentType,
		MetadataDigest:        nativeServingArtifactMetadataDigest(),
	}
	if err := validateNativeServingArtifactInfo(object.Info, key, expectedMetadata); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}

	validation, compiled, err := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if validation.Digest != digest || validation.ProjectID != request.ServingIdentity.ProjectID.String() || validation.ProjectDigest != compiled.ProjectDigest || compiled.ProjectID != request.ServingIdentity.ProjectID || compiled.Graph.ProjectID() != request.ServingIdentity.ProjectID {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered serving artifact content identity mismatch"))
	}
	if err := validateNativeBundleManifestJSON(validation.ManifestJSON); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}

	canonicalProject, err := projectartifact.NewProject(compiled.Graph, compiled.Manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if canonicalProject.ProjectID() != request.ServingIdentity.ProjectID || canonicalProject.Digest() != compiled.ProjectDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered project identity mismatch"))
	}
	var repacked bytes.Buffer
	repackedManifest, repackedDigest, err := projectbundle.PackCompiledProject(canonicalProject, compiled.Plan, &repacked)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	repackedManifestJSON, err := nativeBundleManifestJSON(repackedManifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if repackedDigest != digest || int64(repacked.Len()) != object.Info.SizeBytes || repackedManifestJSON != validation.ManifestJSON {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered serving artifact canonical bytes do not match bound object"))
	}

	activations, err := canonicalProject.ConnectionActivations()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	requirements, managedConnections, authored, err := candidateConnectionRequirements(activations)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	managedPins, err := nativeRecoveryManagedDataPins(request.ManagedDataPins)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if len(managedConnections) != len(managedPins) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered serving artifact managed-data pin set differs from compiled activations"))
	}
	for _, connectionID := range managedConnections {
		if _, ok := managedPins[connectionID]; !ok {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(fmt.Errorf("native recovered serving artifact managed-data pin %q is missing", connectionID))
		}
	}

	authorizationSnapshot, err := projectmanifest.CompileAuthorizationSnapshot(request.ServingIdentity, canonicalProject.Graph(), canonicalProject.Manifest().Access)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	authorizationFingerprint, err := authorizationSnapshot.Digest()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	dataRevision, err := release.CandidateSourcesDataRevision(request.SourceDigest, releaseManagedDataPins(managedPins))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	accessPolicyJSON, publicationsJSON, appearancesJSON, err := nativeServingDocuments(canonicalProject)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	relationContext, err := candidateRelationContexts(managedPins, canonicalProject, candidateActivationBindings(activations))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	relationExecution, err := canonicalProject.RelationExecutionDigestsByContext(relationContext)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}

	return release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{
			SourceDigest: request.SourceDigest, ProjectDigest: canonicalProject.Digest(), ContentDigest: digest,
			CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: canonicalProject.Version(),
		},
		AuthorizationFingerprint: authorizationFingerprint,
		Generation: release.CandidateGenerationArtifact{
			Identity: request.ServingIdentity, ServingArtifactID: request.Artifact.ServingArtifactID,
			ArtifactDigest: digest, BundleManifestJSON: validation.ManifestJSON,
			NativeArtifact:   nativeArtifactObjectEvidence(object.Info),
			AccessPolicyJSON: accessPolicyJSON, DashboardPublicationsJSON: publicationsJSON, DashboardAppearancesJSON: appearancesJSON,
			DataRevision: dataRevision, DataMode: release.GenerationDataRefreshSources,
			Deterministic: compiled.Plan.Deterministic, ManagedDataPins: releaseManagedDataPins(managedPins), Connections: requirements, AuthoredConnections: authored,
			Restrictions: candidateRestrictions(authorizationSnapshot),
		},
		Compiler: release.CandidateCompilerEvidence{
			Graph: canonicalProject.Graph(), Manifest: canonicalProject.Manifest(), Plan: compiled.Plan, Artifact: canonicalProject,
			RelationExecution: relationExecution,
		},
	}, nil
}

func validateNativeRecoveryRequest(request release.CandidateArtifactRecoveryRequest, environment servingstate.Environment) error {
	if request.CandidateID == "" || request.CandidateID != strings.TrimSpace(request.CandidateID) || len(request.CandidateID) > maxNativeRecoveryIDBytes || strings.ContainsAny(request.CandidateID, "\x00\r\n") {
		return errors.New("native recovered candidate ID is invalid")
	}
	candidateID, err := uuid.Parse(request.CandidateID)
	if err != nil || candidateID == uuid.Nil || candidateID.String() != request.CandidateID || candidateID.Version() != 7 || candidateID.Variant() != uuid.RFC4122 {
		return errors.New("native recovered candidate ID must be a canonical UUIDv7")
	}
	if err := request.ServingIdentity.Validate(); err != nil {
		return fmt.Errorf("native recovered serving identity: %w", err)
	}
	if request.ServingIdentity.Environment != string(environment) {
		return errors.New("native recovered serving identity environment does not match module")
	}
	if _, err := validateNativeGenerationID(request.ServingIdentity.GenerationID, true); err != nil {
		return err
	}
	if request.SourceDigest == "" || request.SourceDigest != strings.TrimSpace(request.SourceDigest) || platformdigest.ValidateSHA256Identity(request.SourceDigest) != nil {
		return errors.New("native recovered source digest is invalid")
	}
	if err := validateNativeArtifactIdentity(request.Artifact); err != nil {
		return err
	}
	if request.Artifact.ServingStateID != request.ServingIdentity.GenerationID {
		return errors.New("native recovered serving artifact state identity differs from serving identity")
	}
	if _, err := nativeRecoveryManagedDataPins(request.ManagedDataPins); err != nil {
		return err
	}
	return nil
}

// nativeRecoveryManagedDataPins validates the durable pin ledger and lowers it
// to the relation-context map consumed by compiler projections. The caller is
// expected to compare the resulting key set to compiled managed activations.
func nativeRecoveryManagedDataPins(values []release.ManagedDataPin) (map[string]string, error) {
	pins := make(map[string]string, len(values))
	for _, pin := range values {
		connectionID, revisionID := strings.TrimSpace(pin.ConnectionID), strings.TrimSpace(pin.RevisionID)
		if connectionID == "" || connectionID != pin.ConnectionID || revisionID == "" || revisionID != pin.RevisionID {
			return nil, errors.New("native recovered managed-data pin identity is invalid")
		}
		if _, exists := pins[connectionID]; exists {
			return nil, fmt.Errorf("native recovered managed-data pin %q is duplicated", connectionID)
		}
		pins[connectionID] = revisionID
	}
	return pins, nil
}

func releaseManagedDataPins(values map[string]string) []release.ManagedDataPin {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]release.ManagedDataPin, 0, len(keys))
	for _, key := range keys {
		result = append(result, release.ManagedDataPin{ConnectionID: key, RevisionID: values[key]})
	}
	return result
}

func sameNativeJSON(left, right any) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

// retainNativeServingDocuments derives serving policy snapshots from the
// immutable compiled artifact. It deliberately does not inspect source paths
// or any mutable authoring state.
func retainNativeServingDocuments(generation *release.CandidateGenerationArtifact, compiled projectartifact.Project) error {
	if generation == nil {
		return errors.New("native serving generation evidence is nil")
	}
	accessPolicyJSON, publicationsJSON, appearancesJSON, err := nativeServingDocuments(compiled)
	if err != nil {
		return err
	}
	generation.AccessPolicyJSON = accessPolicyJSON
	generation.DashboardPublicationsJSON = publicationsJSON
	generation.DashboardAppearancesJSON = appearancesJSON
	return nil
}

func nativeServingDocuments(compiled projectartifact.Project) (string, string, string, error) {
	return nativeServingDocumentsFromManifest(compiled.Manifest())
}

func nativeServingDocumentsFromManifest(manifest projectmanifest.Project) (string, string, string, error) {
	access := manifest.Access
	publications := manifest.Publications
	accessJSON, err := canonicalNativeServingDocument(access, "access policy")
	if err != nil {
		return "", "", "", err
	}
	publicationsJSON, err := canonicalNativeServingDocument(publications, "dashboard publications")
	if err != nil {
		return "", "", "", err
	}
	// Dashboard appearances currently have no authored source in the compiled
	// manifest. Persist the canonical empty object until that source exists.
	appearancesJSON := "{}"
	return accessJSON, publicationsJSON, appearancesJSON, nil
}

func canonicalNativeServingDocument(value any, label string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode native serving %s: %w", label, err)
	}
	if bytes.Equal(encoded, []byte("null")) {
		encoded = []byte("{}")
	}
	if len(encoded) == 0 || int64(len(encoded)) > maxNativeServingDocumentBytes {
		return "", fmt.Errorf("native serving %s exceeds bounded document size", label)
	}
	canonical, err := canonicalNativeServingObject(encoded, label)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func canonicalNativeServingObject(encoded []byte, label string) ([]byte, error) {
	var object map[string]any
	if err := strictjson.DecodeWithOptions(encoded, &object, strictjson.Options{MaxBytes: maxNativeServingDocumentBytes, MaxDepth: 32, DuplicateKeys: strictjson.CaseSensitiveKeys, AllowUnknownFields: true}); err != nil {
		return nil, fmt.Errorf("native serving %s: %w", label, err)
	}
	if object == nil {
		return nil, fmt.Errorf("native serving %s must be a JSON object", label)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("canonicalize native serving %s: %w", label, err)
	}
	if int64(len(canonical)) > maxNativeServingDocumentBytes {
		return nil, fmt.Errorf("native serving %s exceeds bounded document size", label)
	}
	return canonical, nil
}

func validateNativeServingDocument(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("native serving %s must be canonical JSON", label)
	}
	canonical, err := canonicalNativeServingObject([]byte(value), label)
	if err != nil {
		return err
	}
	if !bytes.Equal([]byte(value), canonical) {
		return fmt.Errorf("native serving %s is not canonical JSON", label)
	}
	return nil
}

func validateNativeServingDocuments(generation release.CandidateGenerationArtifact, accessPolicyJSON, publicationsJSON, appearancesJSON string) error {
	if err := validateNativeServingDocument(generation.AccessPolicyJSON, "access policy"); err != nil {
		return err
	}
	if err := validateNativeServingDocument(generation.DashboardPublicationsJSON, "dashboard publications"); err != nil {
		return err
	}
	if err := validateNativeServingDocument(generation.DashboardAppearancesJSON, "dashboard appearances"); err != nil {
		return err
	}
	if generation.AccessPolicyJSON != accessPolicyJSON || generation.DashboardPublicationsJSON != publicationsJSON || generation.DashboardAppearancesJSON != appearancesJSON {
		return errors.New("inspected native serving policy evidence changed")
	}
	return nil
}

func nativeArtifactObjectEvidence(info platformobjectstore.ObjectInfo) release.NativeArtifactObjectEvidence {
	return release.NativeArtifactObjectEvidence{Locator: info.Key, StorageSecurityDomain: info.StorageSecurityDomain, ContentType: info.ContentType, MetadataDigest: info.MetadataDigest, SizeBytes: info.SizeBytes}
}

func validateNativeArtifactObjectEvidence(inspected, observed release.NativeArtifactObjectEvidence, allowEmpty bool) error {
	if inspected == (release.NativeArtifactObjectEvidence{}) {
		// The read-only inspect phase has no serving-object metadata yet;
		// materialize/hydrate fill it from exact put/open responses. Once a
		// serving identity is already present, a missing value is tampering.
		if !allowEmpty {
			return errors.New("inspected native serving artifact object evidence is missing")
		}
		return nil
	}
	if inspected != observed {
		return errors.New("inspected native serving artifact object evidence changed")
	}
	return nil
}

// nativeBundleManifestJSON is the release-side representation of the
// canonical container manifest emitted by projectbundle.PackCompiledProject
// and returned by projectbundle.ValidateArtifactReader. Keep the bytes as a
// JSON object string: downstream serving admission stores this exact value,
// so it must never be reconstructed from the compiler's semantic manifest.
func nativeBundleManifestJSON(manifest projectbundle.Manifest) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode native serving artifact bundle manifest: %w", err)
	}
	value := string(data)
	if err := validateNativeBundleManifestJSON(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateNativeBundleManifestJSON(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("native serving artifact bundle manifest must be a canonical JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var manifest projectbundle.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return fmt.Errorf("native serving artifact bundle manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("native serving artifact bundle manifest contains trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("native serving artifact bundle manifest trailing data: %w", err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("canonicalize native serving artifact bundle manifest: %w", err)
	}
	if string(canonical) != value {
		return errors.New("native serving artifact bundle manifest is not canonical JSON")
	}
	if manifest.Version != 1 || manifest.ProjectID == "" || manifest.ProjectDigest == "" || manifest.GraphDigest == "" || manifest.CatalogPath == "" || manifest.CompiledPath == "" || manifest.CompiledSHA256 == "" {
		return errors.New("native serving artifact bundle manifest is incomplete")
	}
	if _, err := projectgraph.NewResourceID(manifest.ProjectID); err != nil {
		return fmt.Errorf("native serving artifact bundle manifest project id: %w", err)
	}
	for field, value := range map[string]string{"project digest": manifest.ProjectDigest, "graph digest": manifest.GraphDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return fmt.Errorf("native serving artifact bundle manifest %s: %w", field, err)
		}
	}
	if len(manifest.CompiledSHA256) != sha256.Size*2 {
		return errors.New("native serving artifact bundle manifest compiled digest is invalid")
	}
	if _, err := hex.DecodeString(manifest.CompiledSHA256); err != nil {
		return fmt.Errorf("native serving artifact bundle manifest compiled digest: %w", err)
	}
	if manifest.CompiledSHA256 != strings.ToLower(manifest.CompiledSHA256) {
		return errors.New("native serving artifact bundle manifest compiled digest must be lowercase")
	}
	if manifest.CatalogPath != projectbundle.ProjectFile && manifest.CatalogPath != projectbundle.CompiledProjectFile {
		return errors.New("native serving artifact bundle manifest catalog path is invalid")
	}
	if manifest.CompiledPath != projectbundle.CompiledProjectFile {
		return errors.New("native serving artifact bundle manifest compiled path is invalid")
	}
	return nil
}

func validateNativeInspectedEvidence(request release.CandidateArtifactRequest, inspected release.CandidateArtifactSet) error {
	if inspected.Artifact.SourceDigest != request.ArtifactDigest || inspected.Artifact.ProjectDigest != request.Source.ProjectDigest || inspected.Compiler.Artifact.ProjectID() != request.Scope.ProjectID || inspected.Compiler.Artifact.Digest() != request.Source.ProjectDigest || inspected.Compiler.Graph.ProjectID() != request.Scope.ProjectID || inspected.Compiler.Plan.Project != request.Scope.ProjectID.String() {
		return candidateArtifactInvalid(errors.New("inspected native compiler evidence does not match request"))
	}
	if !sameNativeJSON(inspected.Compiler.Manifest, inspected.Compiler.Artifact.Manifest()) {
		return candidateArtifactInvalid(errors.New("inspected native compiler manifest differs from immutable artifact"))
	}
	if inspected.Generation.Identity.ProjectID != request.Scope.ProjectID || inspected.Generation.Identity.Environment != request.Scope.Environment || inspected.Generation.Identity.Validate() != nil {
		return candidateArtifactInvalid(errors.New("inspected native serving scope does not match request"))
	}
	if err := inspected.Compiler.Graph.Validate(); err != nil {
		return candidateArtifactInvalid(err)
	}
	return nil
}

func validateNativeArtifactIdentity(identity release.CandidateArtifactIdentity) error {
	if identity.ServingArtifactID != nativeServingArtifactID(identity.ServingArtifactDigest) || platformdigest.ValidateSHA256Identity(identity.ServingArtifactDigest) != nil || identity.ServingStateID == "" || identity.ServingStateID != strings.TrimSpace(identity.ServingStateID) {
		return errors.New("native serving artifact identity is incomplete")
	}
	parsed, err := uuid.Parse(identity.ServingStateID)
	if err != nil || parsed == uuid.Nil {
		return errors.New("native serving state identity must be a UUID")
	}
	return nil
}

func (service *nativeCandidateArtifactPhases) putServingArtifact(ctx context.Context, key string, body io.Reader, metadata platformobjectstore.ObjectMetadata, expectedProjectID string) (platformobjectstore.ObjectInfo, error) {
	info, err := service.artifacts.PutImmutable(ctx, key, body, metadata)
	if err == nil {
		if validateErr := validateNativeServingArtifactInfo(info, key, metadata); validateErr != nil {
			return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(validateErr)
		}
		return info, nil
	}
	if !errors.Is(err, platformobjectstore.ErrAmbiguous) {
		return platformobjectstore.ObjectInfo{}, nativeCandidateObjectError(err)
	}
	// The provider may have committed the object before losing its response.
	// Reconcile by exact-key read; only the original immutable identity is a
	// successful replay. A missing or unavailable object remains unavailable.
	object, openErr := service.artifacts.Open(ctx, key)
	if openErr != nil {
		return platformobjectstore.ObjectInfo{}, nativeCandidateObjectError(openErr)
	}
	if object.Body == nil {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(errors.New("native serving artifact replay body is nil"))
	}
	defer object.Body.Close()
	if validateErr := validateNativeServingArtifactInfo(object.Info, key, metadata); validateErr != nil {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(validateErr)
	}
	validation, _, validateErr := projectbundle.ValidateArtifactReader(object.Body, object.Info.SizeBytes)
	if validateErr != nil {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(validateErr)
	}
	if validation.Digest != metadata.Digest || validation.ProjectID != expectedProjectID {
		return platformobjectstore.ObjectInfo{}, candidateArtifactInvalid(errors.New("native serving artifact replay content identity mismatch"))
	}
	return object.Info, nil
}

func nativeCandidateObjectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, platformobjectstore.ErrInvalid) || errors.Is(err, platformobjectstore.ErrInvalidKey) || errors.Is(err, platformobjectstore.ErrConflict) || errors.Is(err, platformobjectstore.ErrDomainMismatch) || errors.Is(err, platformobjectstore.ErrCorrupt) {
		return candidateArtifactInvalid(err)
	}
	return candidateArtifactUnavailable(err)
}

func validateNativeServingArtifactInfo(info platformobjectstore.ObjectInfo, key string, expected platformobjectstore.ObjectMetadata) error {
	if info.Key != key || info.StorageSecurityDomain != expected.StorageSecurityDomain || info.Digest != expected.Digest || info.SizeBytes != expected.SizeBytes || info.ContentType != expected.ContentType || info.MetadataDigest != expected.MetadataDigest {
		return errors.New("native serving artifact object metadata mismatch")
	}
	return nil
}

func nativeServingArtifactKey(digest string) string {
	if platformdigest.ValidateSHA256Identity(digest) != nil {
		return ""
	}
	return nativeServingArtifactPrefix + strings.TrimPrefix(digest, "sha256:") + nativeServingArtifactSuffix
}

func nativeServingArtifactID(digest string) string {
	if platformdigest.ValidateSHA256Identity(digest) != nil {
		return ""
	}
	return "artifact-" + strings.TrimPrefix(digest, "sha256:")
}

func nativeServingArtifactMetadataDigest() string {
	sum := sha256.Sum256(nil)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validNativeStorageDomain(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

var errNativeInspectLimit = errors.New("native candidate inspect input exceeds bounded limit")
var errNativeInspectSize = errors.New("native candidate inspect object size mismatch")

func readNativeInspectBody(body io.ReadCloser, limit, expected int64) ([]byte, error) {
	if body == nil {
		return nil, errors.New("source reader returned nil body")
	}
	defer body.Close()
	if limit <= 0 {
		return nil, errNativeInspectLimit
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errNativeInspectLimit
	}
	if expected >= 0 && int64(len(data)) != expected {
		return nil, fmt.Errorf("%w: got %d, want %d", errNativeInspectSize, len(data), expected)
	}
	return data, nil
}

func validateNativeInspectRequest(request release.CandidateArtifactRequest, environment servingstate.Environment) error {
	if request.CandidateID == "" || request.CandidateID != strings.TrimSpace(request.CandidateID) || request.OwnerID == "" || request.OwnerID != strings.TrimSpace(request.OwnerID) || request.ArtifactDigest == "" || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) {
		return release.ErrCandidateArtifactInvalid
	}
	if request.Scope.Validate() != nil || request.Scope.Environment != string(environment) || request.Source.ProjectID.Validate() != nil || request.Source.ProjectID != request.Scope.ProjectID || request.Source.ArtifactDigest != request.ArtifactDigest || platformdigest.ValidateSHA256Identity(request.ArtifactDigest) != nil || request.Source.ProjectDigest == "" || request.Source.ProjectDigest != strings.TrimSpace(request.Source.ProjectDigest) || platformdigest.ValidateSHA256Identity(request.Source.ProjectDigest) != nil {
		return release.ErrCandidateArtifactInvalid
	}
	if !nativeInspectLogicalPath(request.Source.ProjectFile) {
		return candidateArtifactInvalid(errors.New("retained source project file is not canonical"))
	}
	if request.GenerationID != "" {
		if _, err := validateNativeGenerationID(request.GenerationID, false); err != nil {
			return candidateArtifactInvalid(err)
		}
	}
	return nil
}

func validateNativeGenerationID(value string, required bool) (uuid.UUID, error) {
	if value == "" {
		if required {
			return uuid.Nil, errors.New("native generation identity is required")
		}
		return uuid.Nil, nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		return uuid.Nil, errors.New("native generation identity must be a canonical UUIDv7")
	}
	return parsed, nil
}

func (service *nativeCandidateArtifactPhases) readSourceObjects(ctx context.Context, scope project.CandidateSourceScope, source project.CandidateSourceSnapshot) (map[string][]byte, error) {
	refs, err := service.reader.SourceObjectRefs(ctx, scope, source.ArtifactDigest)
	if err != nil {
		return nil, candidateArtifactUnavailable(err)
	}
	if len(refs) == 0 || len(refs) > maxNativeInspectSourceFiles {
		return nil, candidateArtifactInvalid(errors.New("retained source object set is empty or exceeds file limit"))
	}
	seen := make(map[string]struct{}, len(refs))
	files := make(map[string][]byte, len(refs))
	var total int64
	projectFileFound := false
	for _, ref := range refs {
		if !nativeInspectLogicalPath(ref.Path) || ref.SizeBytes < 0 || ref.SizeBytes > maxNativeInspectSourceBytes {
			return nil, candidateArtifactInvalid(errors.New("retained source object reference is invalid"))
		}
		if _, exists := seen[ref.Path]; exists {
			return nil, candidateArtifactInvalid(errors.New("retained source object set contains duplicate paths"))
		}
		seen[ref.Path] = struct{}{}
		if total > maxNativeInspectSourceBytes-ref.SizeBytes {
			return nil, candidateArtifactInvalid(errNativeInspectLimit)
		}
		total += ref.SizeBytes
		body, openErr := service.reader.OpenSourceObject(ctx, scope, ref)
		if openErr != nil {
			return nil, candidateArtifactUnavailable(openErr)
		}
		data, readErr := readNativeInspectBody(body, ref.SizeBytes, ref.SizeBytes)
		if readErr != nil {
			if errors.Is(readErr, errNativeInspectLimit) || errors.Is(readErr, errNativeInspectSize) {
				return nil, candidateArtifactInvalid(readErr)
			}
			return nil, candidateArtifactUnavailable(readErr)
		}
		actual := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
		if platformdigest.ValidateSHA256Identity(ref.Digest) != nil || actual != ref.Digest {
			return nil, candidateArtifactInvalid(errors.New("retained source object digest mismatch"))
		}
		files[ref.Path] = data
		if ref.Path == source.ProjectFile {
			projectFileFound = true
		}
	}
	if !projectFileFound {
		return nil, candidateArtifactInvalid(errors.New("retained source project file is missing"))
	}
	return files, nil
}

func nativeInspectLogicalPath(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 1024 || !utf8.ValidString(value) || strings.Contains(value, `\`) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) >= 2 && segment[1] == ':' {
			return false
		}
	}
	return true
}
