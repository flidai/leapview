package module

import (
	"context"
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	"github.com/flidai/leapview/internal/servingstate"
)

type immutableReaderFixture struct {
	state    servingstate.State
	artifact servingstate.Artifact
	stateErr error
	artErr   error
}

func (r immutableReaderFixture) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return r.state, r.stateErr
}
func (r immutableReaderFixture) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return r.artifact, r.artErr
}

func TestImmutableArtifactValidatorChecksStateAndArtifactEvidence(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project_sales")
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{ID: "generation-1", ProjectID: projectID, Environment: "dev", Digest: digestForTest("a")}
	artifact := servingstate.Artifact{ID: "artifact-1", ServingStateID: state.ID, Digest: state.Digest, Path: "s3://bucket/artifact"}
	validator := immutableArtifactValidator{reader: immutableReaderFixture{state: state, artifact: artifact}}
	got, err := validator.Validate(t.Context(), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != state {
		t.Fatalf("validated state = %#v, want %#v", got, state)
	}

	artifact.Digest = digestForTest("b")
	_, err = (immutableArtifactValidator{reader: immutableReaderFixture{state: state, artifact: artifact}}).Validate(t.Context(), state.ID)
	if !errors.Is(err, release.ErrConflict) {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestImmutableArtifactValidatorRejectsIncompleteEvidence(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project_sales")
	if err != nil {
		t.Fatal(err)
	}
	state := servingstate.State{ID: "generation-1", ProjectID: projectID, Environment: "dev", Digest: digestForTest("a")}
	_, err = (immutableArtifactValidator{reader: immutableReaderFixture{
		state:    state,
		artifact: servingstate.Artifact{ID: "artifact-1", ServingStateID: state.ID, Digest: state.Digest},
	}}).Validate(t.Context(), state.ID)
	if !errors.Is(err, release.ErrConflict) {
		t.Fatalf("incomplete artifact error = %v", err)
	}
}
