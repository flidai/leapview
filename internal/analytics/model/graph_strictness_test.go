package model

import (
	"strings"
	"testing"
)

func TestSemanticGraphRejectsDuplicateStructuredRelationshipEndpoints(t *testing.T) {
	m := relationshipMatrixModel()
	m.Relationships = []Relationship{
		{ID: "z_duplicate", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
		{ID: "a_duplicate", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
	}
	err := m.validateSemanticGraph()
	if err == nil || !strings.Contains(err.Error(), `duplicate relationship definition`) || !strings.Contains(err.Error(), `"a_duplicate"`) {
		t.Fatalf("duplicate endpoint error = %v", err)
	}
}

func TestSemanticGraphRejectsDirectionalRelationshipCycle(t *testing.T) {
	m := relationshipMatrixModel()
	m.Relationships = []Relationship{
		{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
		{ID: "customers_profiles", FromDataset: "customers", FromFields: []string{"customer_id"}, ToDataset: "profiles", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"},
		{ID: "profiles_orders", FromDataset: "profiles", FromFields: []string{"customer_id"}, ToDataset: "orders", ToFields: []string{"order_id"}, Cardinality: "many_to_one"},
	}
	err := m.validateSemanticGraph()
	if err == nil || !strings.Contains(err.Error(), "relationship cycle detected in directional graph") || !strings.Contains(err.Error(), "customers -> profiles -> orders -> customers") {
		t.Fatalf("directional cycle error = %v", err)
	}
}

func TestMetricWhereRecursesFilterPathsPerAggregateRoot(t *testing.T) {
	m := relationshipMatrixModel()
	customers := m.Tables["customers"]
	customers.Dimensions["state"] = MetricDimension{Type: "string", Datatype: DataTypeString}
	m.Tables["customers"] = customers
	m.Relationships = []Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}}
	m.Dimensions = map[string]SemanticDimension{}
	m.Filters = map[string]SemanticFilterSpec{
		"nested": {All: []SemanticFilterSpec{{Any: []SemanticFilterSpec{{Not: &SemanticFilterSpec{Field: "customers.state", Operator: "equals", Value: "CA"}}}}}},
	}
	m.Metrics = map[string]Metric{
		"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &MetricInput{Field: "orders.order_id"}, Where: []string{"nested"}},
	}
	if err := m.validateSemanticGraph(); err != nil {
		t.Fatalf("safe nested filter path rejected: %v", err)
	}
	m.Filters["nested"] = SemanticFilterSpec{Field: "orders.order_id", Operator: "equals", Value: "o-1", Path: []string{"orders_customers"}}
	if err := m.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "path ends at") {
		t.Fatalf("incomplete explicit filter path accepted: %v", err)
	}
}
