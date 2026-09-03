package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

// immutableArtifactValidator adapts the native serving-state reader to the
// release service's narrow validation contract. Both rows are read from the
// same immutable authority and their identities/digests are checked together;
// no compatibility validation or artifact-store mutation is reachable here.
type immutableArtifactValidator struct {
	reader ServingStateReader
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
	if state.ID != id || state.ProjectID.Validate() != nil || servingstate.ValidateEnvironment(state.Environment) != nil || state.Digest == "" {
		return servingstate.State{}, fmt.Errorf("%w: immutable serving state identity is incomplete", release.ErrConflict)
	}
	artifact, err := v.reader.ArtifactByServingState(ctx, id)
	if err != nil {
		return servingstate.State{}, err
	}
	if artifact.ID == "" || artifact.ServingStateID != id || artifact.Digest == "" || artifact.Path == "" {
		return servingstate.State{}, fmt.Errorf("%w: immutable serving artifact identity is incomplete", release.ErrConflict)
	}
	if artifact.Digest != state.Digest {
		return servingstate.State{}, fmt.Errorf("%w: immutable serving artifact digest mismatch", release.ErrConflict)
	}
	return state, nil
}
