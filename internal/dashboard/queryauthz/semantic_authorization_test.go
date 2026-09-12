package authz

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func TestSemanticAuthorizationAdapterSharesConsumerPlannerAndProjectionSemantics(t *testing.T) {
	model := semanticAuthorizationTestModel()
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(table string) (string, error) { return table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	adapter := SemanticAuthorizationAdapter{
		Model:    func(string) (*semanticmodel.Model, bool) { return model, true },
		Consumer: func(context.Context, string) (*semanticquery.SemanticAccessConsumer, error) { return consumer, nil },
	}
	ctx, err := adapter.SemanticConsumerForRequest(context.Background(), "sales")
	if err != nil {
		t.Fatal(err)
	}
	if bound, ok := SemanticConsumerFromContext(ctx, "sales"); !ok || bound != consumer {
		t.Fatalf("shared consumer binding = %v, %v", bound, ok)
	}
	if err := adapter.AuthorizeSemanticTarget(ctx, "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatalf("target authorization: %v", err)
	}
	if err := adapter.AuthorizeSemanticField(ctx, "sales", "orders", "orders.status"); err != nil {
		t.Fatalf("field authorization: %v", err)
	}
	if err := adapter.AuthorizeSemanticModelProjection(ctx, "sales"); err != nil {
		t.Fatalf("projection authorization: %v", err)
	}
	resolved, err := adapter.SemanticPlannerForRequest(ctx, "sales")
	if err != nil || resolved == nil || resolved.CompiledModel() == nil {
		t.Fatalf("shared planner resolution = %v, %v", resolved, err)
	}
}

func TestSemanticAuthorizationAdapterPreservesProviderErrorsAndFailClosedFallback(t *testing.T) {
	providerErr := errors.New("authority provider unavailable")
	model := semanticAuthorizationTestModel()
	adapter := SemanticAuthorizationAdapter{
		Model:          func(string) (*semanticmodel.Model, bool) { return model, true },
		ContextPlanner: func(context.Context, string) (*semanticquery.Planner, error) { return nil, providerErr },
	}
	if _, err := adapter.SemanticPlannerForRequest(context.Background(), "sales"); !errors.Is(err, providerErr) {
		t.Fatalf("context planner error = %v, want provider error", err)
	}

	unknown := SemanticAuthorizationAdapter{}
	if err := unknown.AuthorizeSemanticTarget(context.Background(), "missing", semanticquery.SemanticAccessTarget{Dataset: "orders"}); !errors.Is(err, ErrSemanticConsumerAuthorityUnavailable) {
		t.Fatalf("unknown target error = %v, want unavailable", err)
	}
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(table string) (string, error) { return table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	mismatched := SemanticAuthorizationAdapter{Consumer: func(context.Context, string) (*semanticquery.SemanticAccessConsumer, error) { return consumer, nil }}
	if _, err := mismatched.SemanticConsumerForRequest(context.Background(), "sales"); !errors.Is(err, ErrSemanticConsumerAuthorityUnavailable) {
		t.Fatalf("consumer without model evidence error = %v, want unavailable", err)
	}
}

func semanticAuthorizationTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"status"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status": {Field: "orders.status", Table: "orders", Name: "status", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"status": {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}},
		},
	}
}
