package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
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
	materializer, ok := executor.(*duckDBProjectMaterializer)
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
	materializer := &duckDBProjectMaterializer{module: module}

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

type projectMaterializerConnectionResolver struct{}

func (projectMaterializerConnectionResolver) Resolve(context.Context, string, semanticmodel.Connection) (semanticmodel.Connection, error) {
	return semanticmodel.Connection{}, errors.New("candidate resolver called")
}

var _ analyticsruntime.ConnectionResolver = projectMaterializerConnectionResolver{}

func TestProjectMaterializerResolvesConnectionsAgainstCandidateRuntime(t *testing.T) {
	module := &Module{activeRuntimeBindingEvidence: activeEvidenceSource{}}
	candidate := projectMaterializerConnectionResolver{}
	key := candidateRuntimeBindingKey{candidateID: "candidate_1", projectID: "sales"}
	token := module.candidateRuntimeBindings.register(key, candidate)
	t.Cleanup(func() { module.candidateRuntimeBindings.remove(key, token) })

	resolver := (&duckDBProjectMaterializer{module: module}).connectionResolver(analyticsmaterialization.Request{
		CandidateID: "candidate_1",
		Identity:    projectgraph.ServingIdentity{ProjectID: "sales"},
		// Active evidence must not be consulted for candidate requests.
		ConnectionEvidenceServingStateID: "state_active",
	})
	require.Equal(t, candidate, resolver)
}

func TestProjectMaterializerFailsClosedForMissingCandidateRuntime(t *testing.T) {
	module := &Module{activeRuntimeBindingEvidence: activeEvidenceSource{}}
	resolver := (&duckDBProjectMaterializer{module: module}).connectionResolver(analyticsmaterialization.Request{
		CandidateID: "candidate_1",
		Identity:    projectgraph.ServingIdentity{ProjectID: "sales"},
		// A missing candidate binding must not fall back to this active state.
		ConnectionEvidenceServingStateID: "state_active",
	})
	require.NotNil(t, resolver)
	_, err := resolver.Resolve(t.Context(), "warehouse", semanticmodel.Connection{Kind: "postgres"})
	require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
	_, active := resolver.(*activeRuntimeConnectionResolver)
	require.False(t, active)
}

func TestProjectMaterializerRejectsNonCanonicalCandidateIdentity(t *testing.T) {
	module := &Module{}
	for name, request := range map[string]analyticsmaterialization.Request{
		"candidate": {CandidateID: " candidate_1", Identity: projectgraph.ServingIdentity{ProjectID: "sales"}},
		"project":   {CandidateID: "candidate_1", Identity: projectgraph.ServingIdentity{ProjectID: " sales"}},
	} {
		t.Run(name, func(t *testing.T) {
			resolver := (&duckDBProjectMaterializer{module: module}).connectionResolver(request)
			require.NotNil(t, resolver)
			_, err := resolver.Resolve(t.Context(), "warehouse", semanticmodel.Connection{Kind: "postgres"})
			require.ErrorIs(t, err, connectionbinding.ErrProviderUnavailable)
		})
	}
}

func TestProjectMaterializerLeavesConnectionsUnboundWithoutIdentity(t *testing.T) {
	materializer := &duckDBProjectMaterializer{module: &Module{}}
	require.Nil(t, materializer.connectionResolver(analyticsmaterialization.Request{
		Identity: projectgraph.ServingIdentity{},
	}))
}

func TestProjectMaterializerObservationStateClearsAcrossRunsAndCopiesSchemas(t *testing.T) {
	materializer := &duckDBProjectMaterializer{}
	nullable := true
	first := []analyticsmaterialize.SourceObservation{{ID: "orders", Schema: []semanticmodel.ColumnSchema{{Name: "id", PhysicalType: "BIGINT", Nullable: &nullable}}}}
	run := materializer.beginMaterializationRun()
	materializer.setMaterializationRun(run, first)
	first[0].ID = "mutated"
	first[0].Schema[0].Name = "mutated"
	first[0].Schema[0].Nullable = nil
	got, err := materializer.SourceObservations(t.Context())
	require.NoError(t, err)
	require.Equal(t, "orders", got[0].ID)
	require.Equal(t, "id", got[0].Schema[0].Name)
	require.NotNil(t, got[0].Schema[0].Nullable)

	// A failed subsequent run must not leave the previous run's evidence
	// visible, and a later successful run replaces it completely.
	run = materializer.beginMaterializationRun()
	materializer.clearMaterializationRun(run)
	got, err = materializer.SourceObservations(t.Context())
	require.NoError(t, err)
	require.Empty(t, got)
	second := []analyticsmaterialize.SourceObservation{{ID: "customers"}}
	run = materializer.beginMaterializationRun()
	materializer.setMaterializationRun(run, second)
	got, err = materializer.SourceObservations(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "customers", got[0].ID)
}
