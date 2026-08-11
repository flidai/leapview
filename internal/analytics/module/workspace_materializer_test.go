package module

import (
	"testing"

	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceMaterializerResolvesConnectionsAgainstActiveReleaseState(t *testing.T) {
	module := &Module{activeRuntimeBindingEvidence: activeEvidenceSource{}}
	materializer := duckDBWorkspaceMaterializer{module: module}

	resolver := materializer.connectionResolver(analyticsmaterialization.WorkspaceRequest{
		ConnectionEvidenceServingStateID: "state_active",
		WorkspaceID:                      "sales",
		Environment:                      servingstate.Environment("prod"),
	})

	active, ok := resolver.(*activeRuntimeConnectionResolver)
	require.True(t, ok)
	require.Same(t, module, active.module)
	require.Equal(t, "state_active", active.servingStateID)
	require.Equal(t, "sales", active.workspaceID)
	require.Equal(t, "prod", active.environment)
}

func TestWorkspaceMaterializerLeavesAuthoredConnectionsUnboundWithoutReleaseEvidence(t *testing.T) {
	materializer := duckDBWorkspaceMaterializer{module: &Module{}}
	require.Nil(t, materializer.connectionResolver(analyticsmaterialization.WorkspaceRequest{
		ConnectionEvidenceServingStateID: "state_active",
	}))
}
