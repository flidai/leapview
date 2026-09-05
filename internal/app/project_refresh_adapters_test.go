package app

import (
	"context"
	"testing"

	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
)

// readOnlyRefreshServingStates models the immutable serving-state reader used
// by canonical refresh composition.
type readOnlyRefreshServingStates struct{}

func (readOnlyRefreshServingStates) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
}

func (readOnlyRefreshServingStates) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return servingstate.State{}, servingstate.ErrNotFound
}

func (readOnlyRefreshServingStates) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return servingstate.Artifact{}, servingstate.ErrNotFound
}

func (readOnlyRefreshServingStates) ListActiveScopes(context.Context) ([]servingstatemodule.ActiveScope, error) {
	return nil, nil
}

func TestProjectRefreshServiceUsesReadOnlyServingState(t *testing.T) {
	reader := readOnlyRefreshServingStates{}
	service, err := projectRefreshService(
		persistenceInputs{servingStateRepo: reader},
		workflowInputs{},
		func() *dashboardmodule.Module { return nil },
	)
	if err != nil {
		t.Fatalf("projectRefreshService() error = %v", err)
	}
	if service.ServingStates != reader {
		t.Fatal("projectRefreshService() did not preserve read-only serving-state authority")
	}
}
