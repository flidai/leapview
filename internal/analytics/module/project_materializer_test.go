package module

import (
	"context"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/stretchr/testify/require"
)

type projectMaterializerCredentialResolver struct{}

func (*projectMaterializerCredentialResolver) Resolve(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
	return nil, nil
}

func TestProjectMaterializerForEnvironmentUsesExplicitEnvironmentAndModulePolicy(t *testing.T) {
	processEnvironment := &analyticsducklake.Environment{}
	targetEnvironment := &analyticsducklake.Environment{}
	credentials := &projectMaterializerCredentialResolver{}
	module := &Module{environment: processEnvironment, credentials: credentials}

	executor, err := module.ProjectMaterializerForEnvironment(targetEnvironment)
	require.NoError(t, err)
	materializer, ok := executor.(duckDBProjectMaterializer)
	require.True(t, ok)
	require.Same(t, targetEnvironment, materializer.environment)
	require.Same(t, credentials, materializer.credentials)
	require.Same(t, module, materializer.module)
	require.NotSame(t, processEnvironment, materializer.environment)
}

func TestProjectMaterializerForEnvironmentFailsClosedForMissingEnvironmentOrConfig(t *testing.T) {
	targetEnvironment := &analyticsducklake.Environment{}

	tests := []struct {
		name   string
		module *Module
		env    *analyticsducklake.Environment
	}{
		{name: "nil module", module: nil, env: targetEnvironment},
		{name: "missing credential resolver", module: &Module{}, env: targetEnvironment},
		{name: "missing explicit environment", module: &Module{credentials: &projectMaterializerCredentialResolver{}}, env: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, err := test.module.ProjectMaterializerForEnvironment(test.env)
			require.Error(t, err)
			require.Nil(t, executor)
		})
	}
}

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
