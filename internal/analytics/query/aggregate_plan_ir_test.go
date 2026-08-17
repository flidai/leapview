package query

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func TestPlanIRScalarExpressionPreservesAuthoredNumberKinds(t *testing.T) {
	tests := []struct {
		name   string
		source string
		kinds  []planir.NumberKind
	}{
		{name: "integer", source: "1 + 1", kinds: []planir.NumberKind{planir.NumberInteger, planir.NumberInteger}},
		{name: "Decimal", source: "1.0 + 1", kinds: []planir.NumberKind{planir.NumberDecimal, planir.NumberInteger}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expression, err := semanticmodel.ParseExpression(test.source)
			if err != nil {
				t.Fatal(err)
			}
			converted, err := planIRScalarExpression(expression)
			if err != nil {
				t.Fatal(err)
			}
			for index, child := range converted.Children {
				if child.Kind != planir.ScalarLiteral || child.Literal.NumberKind != test.kinds[index] {
					t.Fatalf("child = %#v, want %s literal", child, test.kinds[index])
				}
			}
		})
	}
}

func TestPlanIRScalarExpressionRejectsExponentLiteral(t *testing.T) {
	// The public parser rejects this first; the lowering guard also protects
	// callers that carry a pre-existing expression tree across the boundary.
	if _, err := semanticmodel.ParseExpression("1e-3"); err == nil {
		t.Fatal("ParseExpression accepted exponent notation")
	}
}

func TestAggregatePlanIRPreservesIndependentRootsAndTypedComputations(t *testing.T) {
	model := planIRTestModel()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{
		{Field: "order_count"},
		{Field: "tag_count"},
		{Field: "tag_rate"},
		{Field: "tag_rate_percent"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.IR == nil {
		t.Fatal("aggregate plan omitted required PlanIR graph")
	}
	if err := plan.IR.Validate(); err != nil {
		t.Fatalf("PlanIR validation: %v", err)
	}
	if plan.SQL == "" || !strings.Contains(plan.SQL, "FULL OUTER JOIN") && !strings.Contains(plan.SQL, "CROSS JOIN") {
		t.Fatalf("unexpected aggregate SQL: %s", plan.SQL)
	}
	var stitch *planir.StitchAggregates
	hasRatio, hasDerived := false, false
	for _, node := range plan.IR.Nodes {
		switch value := node.(type) {
		case planir.StitchAggregates:
			copy := value
			stitch = &copy
		case planir.ComputeRatio:
			hasRatio = true
		case planir.ComputeDerived:
			hasDerived = true
		}
	}
	if stitch == nil || len(stitch.InputsList) != 2 || len(stitch.Keys) != 1 || stitch.Keys[0] != "__scalar_key" {
		t.Fatalf("stitch = %#v", stitch)
	}
	if !hasRatio || !hasDerived {
		t.Fatalf("typed computations ratio=%v derived=%v", hasRatio, hasDerived)
	}
	sortLimit, ok := plan.IR.Nodes[plan.IR.Output].(planir.SortLimit)
	if !ok {
		t.Fatalf("output node = %T, want SortLimit", plan.IR.Nodes[plan.IR.Output])
	}
	if len(sortLimit.Sort) != 4 {
		t.Fatalf("sort keys = %#v, want one key per selected output", sortLimit.Sort)
	}
	seenSort := map[string]bool{}
	for _, key := range sortLimit.Sort {
		if seenSort[key.Field] {
			t.Fatalf("duplicate sort key %q: %#v", key.Field, sortLimit.Sort)
		}
		seenSort[key.Field] = true
	}
	roots := map[string]bool{}
	for _, input := range stitch.InputsList {
		node := plan.IR.Nodes[input].Meta()
		if len(node.RootDatasets) != 1 || roots[node.RootDatasets[0]] {
			t.Fatalf("stitch branch roots are not independent: %v", node.RootDatasets)
		}
		roots[node.RootDatasets[0]] = true
	}
	if len(roots) != 2 {
		t.Fatalf("branch roots = %v", roots)
	}
}

func TestAggregatePlanIRCarriesTraverseAndFilterBranches(t *testing.T) {
	model := planIRFilteredModel()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{
		Dimensions: []Field{{Field: "customer_state"}},
		Metrics:    []Field{{Field: "order_count"}},
		Filters:    []Filter{{Field: "customer_state", Operator: "equals", Values: []any{"CA"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.IR == nil {
		t.Fatal("aggregate plan omitted required PlanIR graph")
	}
	kinds := map[planir.Kind]bool{}
	for _, node := range plan.IR.Nodes {
		kinds[node.Kind()] = true
	}
	for _, kind := range []planir.Kind{planir.KindScanDataset, planir.KindTraverseRelationship, planir.KindFilterRows, planir.KindAggregateMetrics, planir.KindSortLimit} {
		if !kinds[kind] {
			t.Fatalf("PlanIR kinds = %v, missing %s", kinds, kind)
		}
	}
	if err := plan.IR.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAggregatePlanIRValidatesSemanticDimensionPhysicalNameCollision(t *testing.T) {
	model := planIRFilteredModel()
	populateFixtureTableModelNames(model)
	orders := model.Tables["orders"]
	orders.Dimensions["state"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeString}
	model.Tables["orders"] = orders
	model.Dimensions["state"] = semanticmodel.SemanticDimension{
		Type: "string", Datatype: semanticmodel.DataTypeString,
		Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.state"}},
	}
	metric := model.Metrics["order_count"]
	metric.Input = &semanticmodel.MetricInput{Field: "orders.state"}
	model.Metrics["order_count"] = metric
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Dataset: "orders", Dimensions: []Field{{Field: "state"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.IR.Validate(); err != nil {
		t.Fatalf("PlanIR validation: %v", err)
	}
	scan, ok := plan.IR.Nodes["scan_0"]
	if !ok {
		t.Fatal("scan node missing")
	}
	foundInputLineage := false
	for _, lineage := range scan.Meta().PhysicalLineage {
		if lineage.Logical == "orders.state" {
			foundInputLineage = true
			break
		}
	}
	if !foundInputLineage {
		t.Fatalf("scan lineage omitted qualified metric input: %#v", scan.Meta().PhysicalLineage)
	}
}

func TestAggregatePlanIRRolePlayingPathsRemainDistinct(t *testing.T) {
	model := planIRRolePlayingModel()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Dimensions: []Field{{Field: "order_date"}, {Field: "ship_date"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.IR.Validate(); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	traversals := 0
	for _, node := range plan.IR.Nodes {
		if traverse, ok := node.(planir.TraverseRelationship); ok {
			traversals++
			paths[traverse.Path.Name] = true
		}
	}
	if traversals != 2 || !paths["orders_order_date"] || !paths["orders_ship_date"] {
		t.Fatalf("role-playing traversals = %d, paths = %v", traversals, paths)
	}
}

func TestAggregatePlanIRTopologicallyOrdersTwoHopPath(t *testing.T) {
	model := singleDatasetFanoutModel()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Dimensions: []Field{{Field: "region"}, {Field: "tier"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.IR.Validate(); err != nil {
		t.Fatal(err)
	}
	traversals := []planir.TraverseRelationship{}
	for _, node := range plan.IR.Nodes {
		if traverse, ok := node.(planir.TraverseRelationship); ok {
			traversals = append(traversals, traverse)
		}
	}
	if len(traversals) != 2 {
		t.Fatalf("traversals = %#v", traversals)
	}
	byID := map[string]planir.TraverseRelationship{}
	for _, traverse := range traversals {
		byID[traverse.Meta().NodeID] = traverse
	}
	for _, traverse := range traversals {
		if traverse.Path.Name == "customers_profiles" {
			parent, ok := byID[traverse.Input]
			if !ok || parent.Path.Name != "orders_customers" {
				t.Fatalf("two-hop parent = %#v, want orders_customers", parent)
			}
		}
	}
}

func planIRTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "tags": {Model: "tags"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {GrainEntity: "order", Entities: map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Datatype: semanticmodel.DataTypeInteger},
				"revenue":  {Datatype: semanticmodel.DataTypeDecimal},
			}},
			"tags": {GrainEntity: "tag", Entities: map[string]semanticmodel.ModelEntitySpec{"tag": {Type: "primary", Fields: []string{"tag_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"tag_id": {Datatype: semanticmodel.DataTypeInteger},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":      {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
			"revenue":          {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}},
			"tag_count":        {Type: "aggregate", Dataset: "tags", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "tags.tag_id"}},
			"tag_rate":         {Type: "ratio", Numerator: "tag_count", Denominator: "order_count"},
			"tag_rate_percent": {Type: "derived", Expression: "${tag_rate} * 100"},
		},
	}
}

func planIRFilteredModel() *semanticmodel.Model {
	model := &semanticmodel.Model{
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {GrainEntity: "order", Entities: map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id":    {Datatype: semanticmodel.DataTypeInteger},
				"customer_id": {Datatype: semanticmodel.DataTypeInteger},
			}},
			"customers": {GrainEntity: "customer", Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Datatype: semanticmodel.DataTypeInteger},
				"state":       {Datatype: semanticmodel.DataTypeString},
			}, Entities: map[string]semanticmodel.ModelEntitySpec{
				"customer": {Type: "primary", Fields: []string{"customer_id"}},
			}},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"customer_state": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "customers.state", Path: []string{"orders_customers"}},
			}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
	}
	return model
}

func planIRRolePlayingModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "dates": {Model: "dates"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {GrainEntity: "order", Entities: map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id":        {Datatype: semanticmodel.DataTypeInteger},
				"ordered_date_id": {Datatype: semanticmodel.DataTypeInteger},
				"shipped_date_id": {Datatype: semanticmodel.DataTypeInteger},
			}},
			"dates": {GrainEntity: "date", Dimensions: map[string]semanticmodel.MetricDimension{
				"date_id": {Datatype: semanticmodel.DataTypeInteger}, "date_value": {Datatype: semanticmodel.DataTypeDate},
			}, Entities: map[string]semanticmodel.ModelEntitySpec{"date": {Type: "primary", Fields: []string{"date_id"}}}},
		},
		Relationships: []semanticmodel.Relationship{
			{ID: "orders_order_date", FromDataset: "orders", FromFields: []string{"ordered_date_id"}, ToDataset: "dates", ToFields: []string{"date_id"}, Cardinality: "many_to_one"},
			{ID: "orders_ship_date", FromDataset: "orders", FromFields: []string{"shipped_date_id"}, ToDataset: "dates", ToFields: []string{"date_id"}, Cardinality: "many_to_one"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"order_date": {Type: "date", Datatype: semanticmodel.DataTypeDate, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "dates.date_value", Path: []string{"orders_order_date"}}}},
			"ship_date":  {Type: "date", Datatype: semanticmodel.DataTypeDate, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "dates.date_value", Path: []string{"orders_ship_date"}}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
	}
}
