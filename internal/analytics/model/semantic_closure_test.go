package model

import (
	"strings"
	"testing"
)

func TestSemanticGraphRejectsUnboundExecutionTableDeterministically(t *testing.T) {
	m := strictClosureModel()
	m.Tables["orphan"] = Table{
		GrainEntity: "orphan",
		Entities:    map[string]ModelEntitySpec{"orphan": {Type: "primary", Fields: []string{"id"}}},
		Dimensions:  map[string]MetricDimension{"id": {Datatype: DataTypeInteger}},
	}
	err := m.ValidateSemanticGraph()
	if err == nil || err.Error() != `model table "orphan" is not bound to a semantic dataset` {
		t.Fatalf("ValidateSemanticGraph() error = %v", err)
	}
}

func TestSemanticGraphRejectsMissingDatasetModelBinding(t *testing.T) {
	m := strictClosureModel()
	m.Datasets["orders"] = SemanticDatasetSpec{}
	err := m.ValidateSemanticGraph()
	if err == nil || !strings.Contains(err.Error(), `semantic dataset "orders" model is required`) {
		t.Fatalf("ValidateSemanticGraph() error = %v", err)
	}
}

func TestSemanticGraphRejectsInvalidEntityAndGrainContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Model)
		want   string
	}{
		{"missing entity field", func(m *Model) {
			table := m.Tables["orders"]
			table.Entities["order"] = ModelEntitySpec{Type: "primary", Fields: []string{"missing"}}
			m.Tables["orders"] = table
		}, `model table "orders" entity "order" field "missing" is not declared`},
		{"grain points to foreign", func(m *Model) {
			table := m.Tables["orders"]
			table.GrainEntity = "customer_ref"
			m.Tables["orders"] = table
		}, `model table "orders" grain.entity "customer_ref" must be primary or unique`},
		{"invalid entity type", func(m *Model) {
			table := m.Tables["orders"]
			table.Entities["bad"] = ModelEntitySpec{Type: "unknown", Fields: []string{"order_id"}}
			m.Tables["orders"] = table
		}, `model table "orders" entity "bad" has unsupported type "unknown"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := strictClosureModel()
			test.mutate(m)
			err := m.ValidateSemanticGraph()
			if err == nil || err.Error() != test.want {
				t.Fatalf("ValidateSemanticGraph() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSemanticGraphRequiresClosedTimeGrainUnion(t *testing.T) {
	m := strictClosureModel()
	m.Dimensions = map[string]SemanticDimension{}
	m.Dimensions["order_date"] = SemanticDimension{
		Datatype: DataTypeDate, Type: "date", Grains: []string{"day"},
		Bindings: map[string]DimensionBinding{"orders": {Field: "orders.order_date"}},
	}
	err := m.ValidateSemanticGraph()
	if err == nil || !strings.Contains(err.Error(), `time semantics requires native grain`) {
		t.Fatalf("ValidateSemanticGraph() error = %v", err)
	}
}

func TestSemanticGraphAppliesMetricDefaultsAndRejectsCrossTagFields(t *testing.T) {
	m := strictClosureModel()
	if err := m.ValidateSemanticGraph(); err != nil {
		t.Fatalf("default metric graph rejected: %v", err)
	}
	if got := m.Metrics["revenue"].Empty; got != "null" {
		t.Fatalf("aggregate empty default = %q, want null", got)
	}
	m.Metrics["bad"] = Metric{Type: "ratio", Numerator: "revenue", Denominator: "revenue", Where: []string{"owned"}}
	err := m.ValidateSemanticGraph()
	if err == nil || !strings.Contains(err.Error(), `ratio does not accept aggregate or derived fields`) {
		t.Fatalf("cross-tag metric error = %v", err)
	}
}

func TestSemanticGraphRejectsFilterBooleanAuthoringContext(t *testing.T) {
	m := strictClosureModel()
	m.Filters["owned"] = SemanticFilterSpec{All: []SemanticFilterSpec{{Field: "orders.status", Operator: "equals", Value: "open"}}, AIContext: &AIContext{Instructions: "not executable"}}
	err := m.ValidateSemanticGraph()
	if err == nil || !strings.Contains(err.Error(), "boolean node cannot contain leaf fields") {
		t.Fatalf("boolean filter error = %v", err)
	}
}

func strictClosureModel() *Model {
	return &Model{
		Name: "strict",
		Tables: map[string]Table{
			"orders": {
				GrainEntity: "order",
				Entities: map[string]ModelEntitySpec{
					"order":        {Type: "primary", Fields: []string{"order_id"}},
					"customer_ref": {Type: "foreign", Fields: []string{"customer_id"}},
				},
				Dimensions: map[string]MetricDimension{
					"order_id": {Datatype: DataTypeInteger}, "customer_id": {Datatype: DataTypeInteger},
					"amount": {Datatype: DataTypeDecimal}, "status": {Datatype: DataTypeString}, "order_date": {Datatype: DataTypeDate},
				},
			},
			"customers": {
				GrainEntity: "customer",
				Entities:    map[string]ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
				Dimensions:  map[string]MetricDimension{"customer_id": {Datatype: DataTypeInteger}, "state": {Datatype: DataTypeString}},
			},
		},
		Datasets:      map[string]SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
		Relationships: []Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
		Filters:       map[string]SemanticFilterSpec{"owned": {Field: "orders.status", Operator: "equals", Value: "open"}},
		Metrics:       map[string]Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &MetricInput{Field: "orders.amount"}, Where: []string{"owned"}}},
	}
}
