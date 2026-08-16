package module

import (
	"testing"

	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestProjectMaterializerResolvesConnectionsAgainstActiveReleaseState(t *testing.T) {
	module := &Module{activeRuntimeBindingEvidence: activeEvidenceSource{}}
	materializer := duckDBProjectMaterializer{module: module}

	resolver := materializer.connectionResolver(analyticsmaterialization.Request{
		ConnectionEvidenceServingStateID: "state_active",
		Identity:                         projectgraph.ServingIdentity{ProjectID: "sales", Environment: "prod", GenerationID: "candidate"},
		Environment:                      servingstate.Environment("prod"),
	})

	active, ok := resolver.(*activeRuntimeConnectionResolver)
	require.True(t, ok)
	require.Same(t, module, active.module)
	require.Equal(t, "state_active", active.servingStateID)
	require.Equal(t, "sales", active.projectID.String())
	require.Equal(t, "prod", active.environment)
}

func TestProjectMaterializerLeavesAuthoredConnectionsUnboundWithoutReleaseEvidence(t *testing.T) {
	materializer := duckDBProjectMaterializer{module: &Module{}}
	require.Nil(t, materializer.connectionResolver(analyticsmaterialization.Request{
		ConnectionEvidenceServingStateID: "state_active",
	}))
}
