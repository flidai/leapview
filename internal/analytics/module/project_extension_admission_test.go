package module

import (
	"context"
	"path/filepath"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/extension"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

type deniedProjectExtensionAdmission struct{ calls []string }

func (a *deniedProjectExtensionAdmission) AdmitExtension(_ context.Context, name string) (extension.AdmittedExtension, error) {
	a.calls = append(a.calls, name)
	return extension.AdmittedExtension{}, extension.ErrExtensionUnapproved
}

func TestProjectExecutionPreservesConfiguredExtensionAdmission(t *testing.T) {
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(controller.Close)
	environment, err := analyticsducklake.Open(t.Context(), analyticsducklake.Config{
		RootDir: t.TempDir(), MaxConnections: 2, ExtensionAdmission: newModuleTestExtensionAdmission(t, "ducklake"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Close()) })
	denied := &deniedProjectExtensionAdmission{}
	m, err := Build(t.Context(), Config{
		DisableProcessEnvironment: true, CredentialMode: CredentialModeNonSecret,
		ExtensionAdmission:  denied,
		RuntimeCacheEntries: 1, RuntimeCacheBytes: 1024, NodeCacheEntries: 1, NodeCacheBytes: 1024,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	models := map[string]*semanticmodel.Model{"semantic:orders": {
		Name: "orders", DefaultConnection: "warehouse",
		Connections: map[string]semanticmodel.Connection{"warehouse": {Kind: "sqlite", RuntimeOptions: semanticmodel.ConnectionRuntimeOptions{Path: filepath.Join(t.TempDir(), "source.sqlite")}}},
		Sources:     map[string]semanticmodel.Source{"orders": {Connection: "warehouse", Object: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", SourceDependencies: []string{"orders"},
			Execution:           semanticmodel.ExecutionDefinition{SQL: "SELECT id FROM source.orders"},
			SQLAnalysisEvidence: &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: []string{"orders"}},
			Dimensions:          map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeString}},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}}
	materializer, err := m.ProjectMaterializerForEnvironment(environment)
	require.NoError(t, err)
	request := analyticsmaterialization.Request{
		Models: models, Identity: projectgraph.ServingIdentity{ProjectID: "project:test", Environment: "prod", GenerationID: "state:test"},
		Tables: []string{"orders"},
	}
	for _, path := range []string{"materialize", "materialize-with-writer", "serving-project"} {
		t.Run(path, func(t *testing.T) {
			denied.calls = nil
			lease, acquireErr := controller.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "extension-propagation", EstimatedMemoryBytes: 1})
			require.NoError(t, acquireErr)
			t.Cleanup(lease.Release)
			ctx := lease.Context()
			var err error
			switch path {
			case "materialize":
				_, _, err = materializer.(analyticsmaterialization.ObservationExecutor).MaterializeWithObservations(ctx, request)
			case "materialize-with-writer":
				_, _, err = materializer.(analyticsmaterialization.ObservationWriterExecutor).MaterializeWithObservationWriter(ctx, request, func(context.Context, []analyticsmaterialize.SourceObservation) error {
					t.Fatal("denied source admission reached the commit writer")
					return nil
				})
			case "serving-project":
				_, err = m.ProjectRuntimeFactoryForEnvironment(environment).OpenProject(ctx, analyticsruntime.ProjectRequest{
					Models: models, ProjectID: "project:test", Environment: "prod", TargetID: "target:test", ServingStateID: "state:test",
				})
			}
			require.ErrorIs(t, err, extension.ErrExtensionUnapproved, "configured source admission must reject execution instead of being dropped")
			require.Equal(t, []string{"sqlite"}, denied.calls)
		})
	}
}
