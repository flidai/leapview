package query

import (
	"bytes"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func aliasCollisionSemanticAccessPlanner(t *testing.T) *Planner {
	t.Helper()
	model := semanticAccessTestModel(t)
	model.AccessPolicy.Datasets = map[string]semanticmodel.SemanticDatasetAccessSpec{
		"orders": {RequiredAccessGrants: []string{"canViewSales"}},
	}
	model.AccessPolicy.Dimensions = nil
	model.AccessPolicy.Metrics = map[string][]string{
		"orderCount": {"canViewSales"},
		"revenue":    {"canViewAccount"},
	}
	policy := compileSemanticAccessPlannerPolicy(t, model)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	filtered := attributes[:0]
	for _, attribute := range attributes {
		if attribute.DefinitionName != "accountIds" {
			filtered = append(filtered, attribute)
		}
	}
	snapshot, authority := semanticAccessSnapshot(t, filtered)
	return semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)
}

func TestSemanticAccessPlannerSelectedAliasesUseSemanticMetricIdentity(t *testing.T) {
	planner := aliasCollisionSemanticAccessPlanner(t)

	if _, err := planner.Plan(Request{
		Dataset: "orders",
		Metrics: []Field{{Field: "orderCount", Alias: "revenue"}},
		Sort:    []Sort{{Field: "revenue", Direction: "desc"}},
	}); err != nil {
		t.Fatalf("allowed metric alias was denied: %v", err)
	}
	if _, err := planner.Plan(Request{
		Dataset: "orders",
		Metrics: []Field{{Field: "revenue", Alias: "orderCount"}},
		Sort:    []Sort{{Field: "orderCount", Direction: "desc"}},
	}); err == nil || !strings.Contains(err.Error(), `semantic access denied for metric "revenue"`) {
		t.Fatalf("denied metric alias error = %v", err)
	}

	if _, err := planner.PlanRawValues(RawValueRequest{
		Dataset: "orders",
		Metric:  Field{Field: "orderCount", Alias: "revenue"},
		Sort:    []Sort{{Field: "revenue", Direction: "desc"}},
	}); err != nil {
		t.Fatalf("allowed raw metric alias was denied: %v", err)
	}
	if _, err := planner.PlanRawValues(RawValueRequest{
		Dataset: "orders",
		Metric:  Field{Field: "revenue", Alias: "orderCount"},
		Sort:    []Sort{{Field: "orderCount", Direction: "desc"}},
	}); err == nil || !strings.Contains(err.Error(), `semantic access denied for metric "revenue"`) {
		t.Fatalf("denied raw metric alias error = %v", err)
	}
}

func TestSemanticAccessPlannerInvalidSortAliasStillFailsValidation(t *testing.T) {
	planner := aliasCollisionSemanticAccessPlanner(t)
	_, err := planner.Plan(Request{
		Dataset: "orders",
		Metrics: []Field{{Field: "orderCount", Alias: "revenue"}},
		Sort:    []Sort{{Field: "notSelected", Direction: "asc"}},
	})
	if err == nil || !strings.Contains(err.Error(), `sort field "notSelected" is unavailable`) {
		t.Fatalf("invalid sort error = %v", err)
	}
}

func TestSemanticAccessPlannerMultipleAndRowAliases(t *testing.T) {
	planner := aliasCollisionSemanticAccessPlanner(t)
	fields := []Field{{Field: "orderCount", Alias: "revenue"}, {Field: "orderCount", Alias: "otherCount"}}
	ordering := []Sort{{Field: "revenue", Direction: "desc"}, {Field: "otherCount"}}
	if _, err := planner.Plan(Request{Dataset: "orders", Metrics: fields, Sort: ordering}); err != nil {
		t.Fatalf("multiple aliases: %v", err)
	}
	if _, err := planner.PlanRows(RowRequest{Dataset: "orders", Metrics: fields, Sort: ordering}); err != nil {
		t.Fatalf("row sort aliases: %v", err)
	}
	if _, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "orderCount", Alias: "invalid alias!"}}}); err == nil {
		t.Fatal("invalid output alias was accepted")
	}
}

func conformedScopeSemanticAccessModel(t *testing.T) *semanticmodel.Model {
	t.Helper()
	model := testModel()
	populateFixtureTableModelNames(model)
	events := semanticmodel.Table{
		ModelName:   "events",
		GrainEntity: "event",
		Entities:    map[string]semanticmodel.EntityDefinition{"event": {Type: "primary", Fields: []string{"event_id"}}},
		Dimensions: map[string]semanticmodel.MetricDimension{
			"event_id":    {Datatype: semanticmodel.DataTypeInteger},
			"customer_id": {Datatype: semanticmodel.DataTypeInteger},
		},
	}
	model.Tables["events"] = events
	model.Datasets["events"] = semanticmodel.SemanticDatasetSpec{Model: "events"}
	privateCustomers := semanticmodel.Table{
		ModelName:   "private_customers",
		GrainEntity: "customer",
		Entities:    map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
		Dimensions: map[string]semanticmodel.MetricDimension{
			"customer_id": {Datatype: semanticmodel.DataTypeInteger},
			"state":       {Type: "string", Datatype: semanticmodel.DataTypeString},
		},
	}
	model.Tables["private_customers"] = privateCustomers
	model.Datasets["private_customers"] = semanticmodel.SemanticDatasetSpec{Model: "private_customers"}
	model.Relationships = append(model.Relationships, semanticmodel.Relationship{
		ID: "events_private_customers", FromDataset: "events", FromFields: []string{"customer_id"},
		ToDataset: "private_customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
	})
	customerState := model.Dimensions["customer_state"]
	customerState.Bindings["events"] = semanticmodel.DimensionBinding{Field: "private_customers.state", Path: []string{"events_private_customers"}}
	model.Dimensions["customer_state"] = customerState
	model.Metrics["event_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "events", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "events.event_id"}}
	engineering, err := semanticmodel.NewSemanticAccessLiteral("engineering")
	if err != nil {
		t.Fatal(err)
	}
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"canViewSales":  {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{mustSemanticAccessLiteral(t, "sales")}},
			"canViewEvents": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{engineering}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{
			"orders":            {RequiredAccessGrants: []string{"canViewSales"}},
			"tags":              {RequiredAccessGrants: []string{"canViewSales"}},
			"customers":         {RequiredAccessGrants: []string{"canViewSales"}},
			"events":            {RequiredAccessGrants: []string{"canViewSales"}},
			"private_customers": {RequiredAccessGrants: []string{"canViewEvents"}},
		},
	}
	return model
}

func TestSemanticAccessPlannerScopesConformedBindingsToParticipatingRoots(t *testing.T) {
	model := conformedScopeSemanticAccessModel(t)
	policy := compileSemanticAccessPlannerPolicy(t, model)
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)

	if _, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}}, Dimensions: []Field{{Field: "customer_state"}}}); err != nil {
		t.Fatalf("A/B conformed dimension was denied by unrelated C binding: %v", err)
	}
	_, err := planner.Plan(Request{Dataset: "events", Metrics: []Field{{Field: "event_count"}}, Dimensions: []Field{{Field: "customer_state"}}})
	if err == nil || !strings.Contains(err.Error(), `semantic access denied for dimension "customer_state"`) {
		t.Fatalf("C relationship-root denial error = %v", err)
	}
	var previousBundleSQL string
	var previousCanonical []byte
	for iteration := 0; iteration < 5; iteration++ {
		bundle, bundleErr := planner.PlanBundle([]BundleRequest{
			{ID: "ab", Request: Request{Dimensions: []Field{{Field: "customer_state"}}, Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}}}},
			{ID: "c", Request: Request{Dataset: "events", Metrics: []Field{{Field: "event_count"}}}},
		})
		if bundleErr != nil {
			t.Fatalf("request-scoped bundle was denied on iteration %d: %v", iteration, bundleErr)
		}
		if iteration > 0 && bundle.Plan.SQL != previousBundleSQL {
			t.Fatalf("bundle SQL is nondeterministic on iteration %d", iteration)
		}
		previousBundleSQL = bundle.Plan.SQL
		canonical, err := bundle.Plan.IR.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		if iteration > 0 && !bytes.Equal(previousCanonical, canonical) {
			t.Fatal("participating-binding plan identity is nondeterministic")
		}
		previousCanonical = canonical
	}
}
