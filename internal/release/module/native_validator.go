package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

var (
	errImmutableNativeArtifactEvidence = errors.New("immutable native serving artifact evidence is incomplete")
	errImmutableNativeArtifactLocator  = errors.New("immutable native serving artifact locator is not canonical")
)

// immutableArtifactValidator adapts the native serving-state reader to the
// release service's narrow validation contract. Both rows are read from the
// same immutable authority and their identities/digests are checked together;
// no compatibility validation or artifact-store mutation is reachable here.
type immutableArtifactValidator struct {
	reader        ServingStateReader
	storageDomain string
}

var _ release.ArtifactValidator = immutableArtifactValidator{}

func (v immutableArtifactValidator) Validate(ctx context.Context, id servingstate.ID) (servingstate.State, error) {
	if v.reader == nil || id == "" {
		return servingstate.State{}, release.ErrCandidateArtifactUnavailable
	}
	state, err := v.reader.ByID(ctx, id)
	if err != nil {
		return servingstate.State{}, err
	}
	if state.ID != id || state.ProjectID.Validate() != nil || servingstate.ValidateEnvironment(state.Environment) != nil || platformdigest.ValidateSHA256Identity(state.Digest) != nil {
		return servingstate.State{}, fmt.Errorf("%w: immutable serving state identity is incomplete", release.ErrConflict)
	}
	artifact, err := v.reader.ArtifactByServingState(ctx, id)
	if err != nil {
		return servingstate.State{}, err
	}
	if err := validateImmutableNativeArtifact(artifact, state, v.storageDomain); err != nil {
		return servingstate.State{}, fmt.Errorf("%w: immutable serving artifact identity is incomplete", release.ErrConflict)
	}
	return state, nil
}

// validateImmutableNativeArtifact checks the value-only evidence persisted by
// the native serving-state authority. Native bundles have no filesystem Path;
// their digest-derived Locator and object metadata are the admission evidence.
func validateImmutableNativeArtifact(artifact servingstate.Artifact, state servingstate.State, storageDomain string) error {
	if artifact.ServingStateID != state.ID || artifact.Path != "" ||
		platformdigest.ValidateSHA256Identity(artifact.Digest) != nil || artifact.Digest != state.Digest ||
		artifact.ID != nativeServingArtifactID(artifact.Digest) || artifact.Format != servingstate.ArtifactBundleFormat ||
		artifact.ManifestJSON == "" || artifact.ManifestJSON != state.ManifestJSON ||
		artifact.SizeBytes < 1 || artifact.SizeBytes > servingstate.MaxArtifactBundleBytes ||
		artifact.ContentType != servingstate.ArtifactBundleContentType ||
		!validNativeStorageDomain(artifact.StorageSecurityDomain) ||
		!validNativeStorageDomain(storageDomain) || artifact.StorageSecurityDomain != storageDomain ||
		platformdigest.ValidateSHA256Identity(artifact.MetadataDigest) != nil {
		return errImmutableNativeArtifactEvidence
	}
	wantLocator := nativeServingArtifactKey(artifact.Digest)
	if wantLocator == "" || artifact.Locator != wantLocator || artifact.Locator != strings.TrimSpace(artifact.Locator) {
		return errImmutableNativeArtifactLocator
	}
	return nil
}
