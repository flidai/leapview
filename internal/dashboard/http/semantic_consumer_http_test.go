package http

import (
	"context"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type dashboardSemanticCapabilityMetrics struct {
	fakeMetrics
	model        *semanticmodel.Model
	cacheAllowed bool
	consumer     *semanticquery.SemanticAccessConsumer
}

func (m dashboardSemanticCapabilityMetrics) SemanticModel(string) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil
}

func (m dashboardSemanticCapabilityMetrics) SemanticConsumerCacheAllowed(string) bool {
	return m.cacheAllowed
}

func (m dashboardSemanticCapabilityMetrics) SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error) {
	return m.consumer, nil
}

func TestDashboardSemanticTargetFailsClosedWithoutModelMetadata(t *testing.T) {
	metrics := dashboardSemanticCapabilityMetrics{}
	if err := authorizeDashboardSemanticTarget(context.Background(), metrics, "missing", semanticquery.SemanticAccessTarget{Dataset: "orders"}); !dashboardSemanticUnavailable(err) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestDashboardProtectedCachePathRequiresExplicitCapability(t *testing.T) {
	metrics := dashboardSemanticCapabilityMetrics{
		model: &semanticmodel.Model{
			Name: "sales",
			AccessPolicy: semanticmodel.SemanticAccessPolicy{
				AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
					"sales": {UserAttribute: "tenant"},
				},
			},
		},
	}
	if dashboardSemanticCacheAllowed(metrics, "sales") {
		t.Fatal("protected cache path was allowed without lifecycle evidence")
	}
}

func TestDashboardPublicFilterWithoutDatasetUsesWholeModelProjection(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"status"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Field: "orders.status", Table: "orders", Name: "status", Type: "string", Datatype: semanticmodel.DataTypeString},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"status": {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(table string) (string, error) { return table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	metrics := dashboardSemanticCapabilityMetrics{model: model, consumer: consumer}
	if err := authorizeDashboardFilterField(context.Background(), metrics, "dashboard", "", "orders.status"); err != nil {
		t.Fatalf("dataset-less public filter authorization: %v", err)
	}
}
