package consumer

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

func mustTestOptimizer(t *testing.T, model *semanticmodel.Model) *Optimizer {
	t.Helper()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	optimizer, err := NewOptimizerFromPlanner(planner)
	if err != nil {
		t.Fatal(err)
	}
	return optimizer
}

func TestNewOptimizerFromPlannerRetainsActivationPlanner(t *testing.T) {
	model := optimizerTestModel()
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	optimizer, err := NewOptimizerFromPlanner(planner)
	if err != nil {
		t.Fatal(err)
	}
	if optimizer.planner != planner {
		t.Fatal("optimizer copied or replaced the activation planner")
	}
}

func TestOptimizerGroupsSemanticConsumersWithoutPresentationShapes(t *testing.T) {
	model := optimizerTestModel()
	scope := []dataquery.Filter{{Field: "segment", Operator: "equals", Values: []any{"consumer"}}}
	plan, err := mustTestOptimizer(t, model).Optimize([]LogicalQuery{
		{
			Target: Target{Kind: KindVisual, ID: "trend"},
			Query:  dataquery.Query{Kind: dataquery.KindSemanticAggregate, Fields: []dataquery.Field{{Field: "customer", Alias: "label"}}, Metrics: []dataquery.Field{{Field: "order_count", Alias: "orders"}, {Field: "tag_count", Alias: "tags"}}, Filters: scope, Limit: 500},
		},
		{
			Target: Target{Kind: KindVisual, ID: "ratio"},
			Query:  dataquery.Query{Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "tags_per_order", Alias: "value"}}, Filters: scope},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Strategy != StrategyBundle {
		t.Fatalf("plan = %#v, want one semantic bundle", plan)
	}
	if got := []string{plan.Jobs[0].Queries[0].Target.ID, plan.Jobs[0].Queries[1].Target.ID}; got[0] != "trend" || got[1] != "ratio" {
		t.Fatalf("authored consumer order = %#v", got)
	}
}

func TestOptimizerKeepsDifferentGovernedScopesSeparate(t *testing.T) {
	model := optimizerTestModel()
	plan, err := mustTestOptimizer(t, model).Optimize([]LogicalQuery{
		{Target: Target{Kind: KindVisual, ID: "consumer"}, Query: dataquery.Query{Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "order_count"}}, Filters: []dataquery.Filter{{Field: "segment", Operator: "equals", Values: []any{"consumer"}}}}},
		{Target: Target{Kind: KindVisual, ID: "business"}, Query: dataquery.Query{Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "order_count"}}, Filters: []dataquery.Filter{{Field: "segment", Operator: "equals", Values: []any{"business"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 2 {
		t.Fatalf("jobs = %#v, want separate governed scopes", plan.Jobs)
	}
}

func TestOptimizerBatchesScalarConsumersAcrossDatasets(t *testing.T) {
	plan, err := mustTestOptimizer(t, optimizerTestModel()).Optimize([]LogicalQuery{
		{Target: Target{Kind: KindVisual, ID: "orders"}, Query: dataquery.Query{Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "order_count"}}}},
		{Target: Target{Kind: KindVisual, ID: "tags"}, Query: dataquery.Query{Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "tag_count"}}}},
		{Target: Target{Kind: KindVisual, ID: "ratio"}, Query: dataquery.Query{Kind: dataquery.KindSemanticAggregate, Metrics: []dataquery.Field{{Field: "tags_per_order"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Strategy != StrategyBatch || len(plan.Jobs[0].Queries) != 3 {
		t.Fatalf("cross-dataset scalar plan = %#v", plan)
	}
}

func TestOptimizerBundlesSameDatasetNonAdditiveScalarWithGroupedConsumers(t *testing.T) {
	plan, err := mustTestOptimizer(t, optimizerTestModel()).Optimize([]LogicalQuery{
		{
			Target: Target{Kind: KindVisual, ID: "orders_by_customer"},
			Query: dataquery.Query{
				Kind:    dataquery.KindSemanticAggregate,
				Fields:  []dataquery.Field{{Field: "customer", Alias: "label"}},
				Metrics: []dataquery.Field{{Field: "order_count", Alias: "value"}},
			},
		},
		{
			Target: Target{Kind: KindVisual, ID: "unique_customers"},
			Query: dataquery.Query{
				Kind:    dataquery.KindSemanticAggregate,
				Metrics: []dataquery.Field{{Field: "unique_customers", Alias: "value"}},
			},
		},
		{
			Target: Target{Kind: KindVisual, ID: "average_order_value"},
			Query: dataquery.Query{
				Kind:    dataquery.KindSemanticAggregate,
				Metrics: []dataquery.Field{{Field: "average_order_value", Alias: "value"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Strategy != StrategyBundle || len(plan.Jobs[0].Queries) != 3 {
		t.Fatalf("same-dataset non-additive plan = %#v, want one exact grouping-set bundle", plan)
	}
}

func TestOptimizerBundlesGroupedConsumersAcrossDatasetSignatures(t *testing.T) {
	queries := []LogicalQuery{
		{
			Target: Target{Kind: KindVisual, ID: "orders_by_customer"},
			Query: dataquery.Query{
				Kind:    dataquery.KindSemanticAggregate,
				Target:  "orders",
				Fields:  []dataquery.Field{{Field: "orders.customer", Alias: "label"}},
				Metrics: []dataquery.Field{{Field: "order_count", Alias: "value"}},
			},
		},
		{
			Target: Target{Kind: KindVisual, ID: "tags_per_order_by_customer"},
			Query: dataquery.Query{
				Kind:    dataquery.KindSemanticAggregate,
				Fields:  []dataquery.Field{{Field: "customer", Alias: "label"}},
				Metrics: []dataquery.Field{{Field: "tags_per_order", Alias: "value"}},
			},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(optimizerTestModel())
	if err != nil {
		t.Fatal(err)
	}
	optimizer, err := NewOptimizerFromPlanner(planner)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := optimizer.OptimizeForConcurrency(queries, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Strategy != StrategyBundle || len(plan.Jobs[0].Queries) != 2 {
		t.Fatalf("heterogeneous dataset plan = %#v, want one shared-scan bundle", plan)
	}
	plan, err = optimizer.OptimizeForConcurrency(queries, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 2 {
		t.Fatalf("concurrent heterogeneous dataset plan = %#v, want independent dataset-signature bundles", plan)
	}
}

func optimizerTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "commerce",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"},
			"tags":   {Model: "tags"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				GrainEntity: "customer",
				Entities: map[string]semanticmodel.EntityDefinition{
					"customer": {Type: "primary", Fields: []string{"customer"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer":    {Field: "orders.customer_id", Table: "orders", Name: "customer", Type: "string", Datatype: semanticmodel.DataTypeString},
					"customer_id": {Field: "orders.customer_id", Table: "orders", Name: "customer_id", Type: "string", Datatype: semanticmodel.DataTypeString},
					"segment":     {Field: "orders.segment", Table: "orders", Name: "segment", Type: "string", Datatype: semanticmodel.DataTypeString},
					"amount":      {Field: "orders.amount", Table: "orders", Name: "amount", Type: "number", Datatype: semanticmodel.DataTypeFloat},
				},
			},
			"tags": {
				ModelName:   "tags",
				GrainEntity: "customer",
				Entities: map[string]semanticmodel.EntityDefinition{
					"customer": {Type: "primary", Fields: []string{"customer"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer":    {Field: "tags.customer_id", Table: "tags", Name: "customer", Type: "string", Datatype: semanticmodel.DataTypeString},
					"customer_id": {Field: "tags.customer_id", Table: "tags", Name: "customer_id", Type: "string", Datatype: semanticmodel.DataTypeString},
					"segment":     {Field: "tags.segment", Table: "tags", Name: "segment", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"customer": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.customer_id"}, "tags": {Field: "tags.customer_id"}}},
			"segment":  {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.segment"}, "tags": {Field: "tags.segment"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":         {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.customer"}, Empty: "zero"},
			"unique_customers":    {Type: "aggregate", Dataset: "orders", Aggregation: "count_distinct", Input: &semanticmodel.MetricInput{Field: "orders.customer_id"}, Empty: "zero"},
			"average_order_value": {Type: "aggregate", Dataset: "orders", Aggregation: "avg", Input: &semanticmodel.MetricInput{Field: "orders.amount"}, Empty: "null"},
			"tag_count":           {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.customer"}, Empty: "zero"},
			"tags_per_order":      {Type: "ratio", Numerator: "tag_count", Denominator: "order_count"},
		},
	}
}
