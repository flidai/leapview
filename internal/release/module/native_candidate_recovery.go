package module

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

// RecoverCandidateArtifacts reloads one immutable serving bundle for native
// physical-build recovery. It intentionally has no source reader, serving
// state writer, provenance reader, or materialization dependency. Runtime
// extensions are re-admitted from requirements derived from that exact bundle
// so a restarted process retains the capability evidence used by result
// identity derivation.
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
	legacyAuthorizationPolicy := request.AuthorizationPolicyRevision == 0 && request.AuthorizationPolicyDigest == ""
	if !legacyAuthorizationPolicy && (request.AuthorizationPolicyRevision <= 0 || platformdigest.ValidateSHA256Identity(request.AuthorizationPolicyDigest) != nil) {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovery target authorization policy evidence is incomplete"))
	}
	if platformdigest.ValidateSHA256Identity(request.AuthorizationFingerprint) != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovery authorization fingerprint is invalid"))
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
	if validation.Digest != digest || validation.BundleDigest != compiled.BundleDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered serving artifact content identity mismatch"))
	}
	if err := validateNativeBundleManifestJSON(validation.ManifestJSON); err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}

	canonicalProject, err := projectartifact.NewSourceBundle(compiled.Graph, compiled.Manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if canonicalProject.Digest() != compiled.BundleDigest {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovered source bundle identity mismatch"))
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
	extensionRequirements, err := requiredExtensionNames(activations, canonicalProject.Manifest())
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	extensions, err := (&candidateArtifactInspector{extensionPreparation: service.extensionPreparation}).collectExtensionEvidence(ctx, extensionRequirements)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactUnavailable(err)
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

	// Reload the exact historical target policy selected by a post-017 plan.
	// Pre-017 plans have no target-policy revision and used the canonical empty
	// target attachment; reconstruct that immutable legacy document instead of
	// consulting a mutable policy head that did not govern the candidate.
	policyIdentity, err := candidatePolicyIdentity(request.ServingIdentity.ProjectID, request.ServingIdentity.Environment)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	var targetPolicy resolvedTargetAuthorizationPolicy
	if legacyAuthorizationPolicy {
		legacyPolicy := projectmanifest.AccessPolicy{}
		legacySnapshot, snapshotErr := projectmanifest.CompileAuthorizationSnapshot(policyIdentity, canonicalProject.Graph(), legacyPolicy)
		if snapshotErr != nil {
			return release.CandidateArtifactSet{}, candidateArtifactInvalid(snapshotErr)
		}
		targetPolicy = resolvedTargetAuthorizationPolicy{canonical: "{}", manifest: legacyPolicy, snapshot: legacySnapshot}
	} else {
		targetPolicy, err = service.resolveTargetAuthorizationPolicy(
			ctx, request.ServingIdentity.ProjectID, request.ServingIdentity.Environment,
			request.AuthorizationPolicyRevision, request.AuthorizationPolicyDigest,
			canonicalProject.Graph(), policyIdentity,
		)
		if err != nil {
			return release.CandidateArtifactSet{}, candidateArtifactUnavailable(fmt.Errorf("recover target authorization policy: %w", err))
		}
	}
	authorizationFingerprint, err := targetPolicy.snapshot.Digest()
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	if authorizationFingerprint != request.AuthorizationFingerprint {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(errors.New("native recovery authorization fingerprint differs from immutable plan evidence"))
	}
	authorizationSnapshot, err := projectmanifest.CompileAuthorizationSnapshot(request.ServingIdentity, canonicalProject.Graph(), targetPolicy.manifest)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	dataRevision, err := release.CandidateSourcesDataRevision(request.SourceDigest, releaseManagedDataPins(managedPins))
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	_, publicationsJSON, appearancesJSON, err := nativeServingDocuments(canonicalProject)
	if err != nil {
		return release.CandidateArtifactSet{}, candidateArtifactInvalid(err)
	}
	accessPolicyJSON := targetPolicy.canonical
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
		Extensions:                  extensions,
		AuthorizationPolicyRevision: targetPolicy.revision,
		AuthorizationPolicyDigest:   targetPolicy.digest,
		AuthorizationFingerprint:    authorizationFingerprint,
		AuthorizationSnapshot:       targetPolicy.snapshot,
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
