package module

import (
	"testing"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/stretchr/testify/require"
)

func TestProjectRuntimeReopensSameGenerationWhilePreviousReaderDrains(t *testing.T) {
	cache, err := resultcache.New(resultcache.Limits{RuntimeEntries: 4, RuntimeBytes: 4096, NodeEntries: 16, NodeBytes: 16384})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	factory := NewSurface(&analyticsducklake.Environment{}, cache).ProjectRuntimeFactoryForEnvironment(nil)
	request := analyticsruntime.ProjectRequest{
		ProjectID: "project:test", Environment: "prod", TargetID: "target:test", ServingStateID: "state:restored",
		Models: map[string]*semanticmodel.Model{"values": {
			Name: "values",
			Tables: map[string]semanticmodel.Table{"values": {
				ModelName: "values", Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS value"},
				GrainEntity: "value", Entities: map[string]semanticmodel.EntityDefinition{"value": {Type: "primary", Fields: []string{"value"}}},
				Columns:    map[string]semanticmodel.ModelColumn{"value": {Name: "value", Datatype: semanticmodel.DataTypeInteger}},
				Dimensions: map[string]semanticmodel.MetricDimension{"value": {Datatype: semanticmodel.DataTypeInteger}},
			}},
			Datasets: map[string]semanticmodel.SemanticDatasetSpec{"values": {Model: "values"}},
		}}, SkipInitialRefresh: true,
	}
	first, err := factory.OpenProject(t.Context(), request)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close()) })
	before := cache.Stats().Scopes
	invalid := request
	invalid.Models = nil
	_, err = factory.OpenProject(t.Context(), invalid)
	require.ErrorContains(t, err, "semantic models are required")
	require.Equal(t, before, cache.Stats().Scopes, "failed retry must close only its own handles")
	second, err := factory.OpenProject(t.Context(), request)
	require.NoError(t, err, "a replay must prepare the exact generation while its old readers remain leased")
	t.Cleanup(func() { require.NoError(t, second.Close()) })
	require.Len(t, cache.Stats().Scopes, 4, "each runtime owns separate result and byte scopes")
	partition, err := projectResultPartition(request)
	require.NoError(t, err)
	sharedResults := 0
	for _, scope := range cache.Stats().Scopes {
		if scope.PartitionID == analyticscache.PartitionIdentity(partition) {
			sharedResults++
		}
	}
	require.Equal(t, 2, sharedResults, "dependency-equivalent results retain their stable partition")
	require.NoError(t, first.Close())
	require.Len(t, cache.Stats().Scopes, 2, "draining old readers must preserve the replacement scopes")
	require.NoError(t, second.Close())
	require.Empty(t, cache.Stats().Scopes)
}
