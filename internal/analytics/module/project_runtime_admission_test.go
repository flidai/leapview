package module

import (
	"context"
	"errors"
	"testing"

	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

func TestProjectRuntimeForwardsConfiguredSourceExtensionAdmission(t *testing.T) {
	ctx := t.Context()
	workloads, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(func() { workloads.Close() })
	lease, err := workloads.Acquire(ctx, workload.Request{
		Class: workload.Refresh, PrincipalID: "test", Operation: "project-runtime-extension-admission", EstimatedMemoryBytes: 1,
	})
	require.NoError(t, err)
	t.Cleanup(lease.Release)

	ducklakeAdmission := newModuleTestExtensionAdmission(t, "ducklake")
	environment, err := analyticsducklake.Open(lease.Context(), analyticsducklake.Config{
		RootDir: t.TempDir(), MaxConnections: 2, ExtensionAdmission: ducklakeAdmission,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Close()) })

	postgresAdmission := &recordingDeniedExtensionAdmission{}
	module, err := Build(lease.Context(), Config{
		DisableProcessEnvironment: true,
		ExtensionAdmission:        postgresAdmission,
		RuntimeCacheEntries:       4,
		RuntimeCacheBytes:         1 << 20,
		NodeCacheEntries:          8,
		NodeCacheBytes:            2 << 20,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, module.Close()) })

	factory := module.ProjectRuntimeFactoryForEnvironment(environment)
	project, err := factory.OpenProject(lease.Context(), analyticsruntime.ProjectRequest{
		Models:   map[string]*semanticmodel.Model{"semantic-model:orders": projectRuntimePostgresModel()},
		TargetID: "target:prod", ProjectID: "sales", Environment: "prod",
	})
	require.Error(t, err, "the configured admission intentionally denies postgres")
	require.Nil(t, project)
	require.Equal(t, []string{"postgres"}, postgresAdmission.calls,
		"the project runtime must use the module's configured source-extension admission")
	require.NotContains(t, err.Error(), "requires extension admission",
		"a missing-admission error would mean the configured authority was dropped")
}

type recordingDeniedExtensionAdmission struct {
	calls []string
}

func (admission *recordingDeniedExtensionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	admission.calls = append(admission.calls, name)
	return extension.AdmittedExtension{}, errors.New("intentional test denial")
}

func (admission *recordingDeniedExtensionAdmission) PrepareExtensions(ctx context.Context, names []string) ([]extension.Evidence, error) {
	for _, name := range names {
		if _, err := admission.AdmitExtension(ctx, name); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func projectRuntimePostgresModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:              "orders",
		DefaultConnection: "warehouse",
		Connections: map[string]semanticmodel.Connection{
			"warehouse": {Kind: "postgres"},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Connection: "warehouse", Object: "public.orders"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Execution: semanticmodel.ExecutionDefinition{Source: "orders"},
				Entities: map[string]semanticmodel.EntityDefinition{
					"id": {Type: "primary", Fields: []string{"id"}},
				},
				GrainEntity: "id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"id": {Datatype: semanticmodel.DataTypeInteger},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"},
		},
	}
}
