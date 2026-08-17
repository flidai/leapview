package model

import (
	"strings"
	"testing"
)

func TestRelationshipCardinalityMatrixRequiresKeyedOneEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		relationship Relationship
		wantErr      string
	}{
		{
			name: "many to one targets primary key",
			relationship: Relationship{
				ID: "orders_customers", From: "orders.customer_id", To: "customers.customer_id", Cardinality: "many_to_one",
			},
		},
		{
			name: "many to one rejects non key target",
			relationship: Relationship{
				ID: "orders_customers_by_region", From: "orders.region", To: "customers.region", Cardinality: "many_to_one",
			},
			wantErr: `relationship "orders_customers_by_region" many_to_one endpoint "customers.region" must belong to a primary or unique entity`,
		},
		{
			name: "one to one requires both primary keys",
			relationship: Relationship{
				ID: "customers_profiles", From: "customers.customer_id", To: "profiles.customer_id", Cardinality: "one_to_one",
			},
		},
		{
			name: "one to one rejects non key source",
			relationship: Relationship{
				ID: "customers_profiles_by_region", From: "customers.region", To: "profiles.customer_id", Cardinality: "one_to_one",
			},
			wantErr: `relationship "customers_profiles_by_region" one_to_one endpoint "customers.region" must belong to a primary or unique entity`,
		},
		{
			name: "one to one rejects non key target",
			relationship: Relationship{
				ID: "customers_profiles_by_tier", From: "customers.customer_id", To: "profiles.tier", Cardinality: "one_to_one",
			},
			wantErr: `relationship "customers_profiles_by_tier" one_to_one endpoint "profiles.tier" must belong to a primary or unique entity`,
		},
		{
			name: "one to many is unsafe",
			relationship: Relationship{
				ID: "customers_orders", From: "customers.customer_id", To: "orders.customer_id", Cardinality: "one_to_many",
			},
			wantErr: `relationship "customers_orders" has unsafe cardinality "one_to_many"`,
		},
		{
			name: "many to many is unsafe",
			relationship: Relationship{
				ID: "orders_tags", From: "orders.order_id", To: "tags.order_id", Cardinality: "many_to_many",
			},
			wantErr: `relationship "orders_tags" has unsafe cardinality "many_to_many"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := relationshipMatrixModel()
			model.Relationships = []Relationship{test.relationship}
			err := model.validateSemanticGraph()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSemanticGraph() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateSemanticGraph() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestSafeRelationshipPathRejectsCyclicAlternativePaths(t *testing.T) {
	model := relationshipMatrixModel()
	model.Relationships = []Relationship{
		{ID: "customers_profiles", From: "customers.customer_id", To: "profiles.customer_id", Cardinality: "one_to_one"},
		{ID: "profiles_accounts", From: "profiles.customer_id", To: "accounts.customer_id", Cardinality: "one_to_one"},
		{ID: "accounts_customers", From: "accounts.customer_id", To: "customers.customer_id", Cardinality: "one_to_one"},
	}

	_, err := model.SafeRelationshipPath("customers", "accounts")
	if err == nil || !strings.Contains(err.Error(), "ambiguous relationship path") {
		t.Fatalf("SafeRelationshipPath() error = %v, want cyclic ambiguity rejection", err)
	}
	_, err = model.SafeRelationshipPath("customers", "tags")
	if err == nil || !strings.Contains(err.Error(), "no safe relationship path") {
		t.Fatalf("SafeRelationshipPath() error = %v, want unreachable target rejection", err)
	}
}

func TestCompositeRelationshipValidatesArityTypesAndUniqueTuple(t *testing.T) {
	model := relationshipMatrixModel()
	orders := model.Tables["orders"]
	orders.Dimensions["line_number"] = MetricDimension{Type: "number", Datatype: DataTypeInteger}
	orders.Entities = map[string]ModelEntitySpec{"order_line": {Type: "primary", Fields: []string{"order_id", "line_number"}}}
	orders.GrainEntity = "order_line"
	model.Tables["orders"] = orders
	customers := model.Tables["customers"]
	customers.Dimensions["line_number"] = MetricDimension{Type: "number", Datatype: DataTypeInteger}
	customers.Entities = map[string]ModelEntitySpec{"customer_line": {Type: "unique", Fields: []string{"customer_id", "line_number"}}}
	customers.GrainEntity = "customer_line"
	model.Tables["customers"] = customers
	model.Relationships = []Relationship{{
		ID: "orders_customers", FromDataset: "orders", FromFields: []string{"order_id", "line_number"},
		ToDataset: "customers", ToFields: []string{"customer_id", "line_number"}, Cardinality: "many_to_one",
	}}
	if err := model.validateSemanticGraph(); err != nil {
		t.Fatalf("valid composite relationship rejected: %v", err)
	}

	model.Relationships[0].ToFields = []string{"customer_id"}
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "arity mismatch") {
		t.Fatalf("arity mismatch error = %v", err)
	}

	model.Relationships[0].ToFields = []string{"customer_id", "line_number"}
	customerTable := model.Tables["customers"]
	customerTable.Dimensions["line_number"] = MetricDimension{Type: "string", Datatype: DataTypeString}
	model.Tables["customers"] = customerTable
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("type mismatch error = %v", err)
	}

	model.Tables["customers"] = customers
	customerTable = model.Tables["customers"]
	customerTable.Dimensions["line_number"] = MetricDimension{Type: "number", Datatype: DataTypeInteger}
	customerTable.Entities = map[string]ModelEntitySpec{"customer": {Type: "unique", Fields: []string{"customer_id"}}}
	model.Tables["customers"] = customerTable
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "primary or unique entity") {
		t.Fatalf("non-unique tuple error = %v", err)
	}
}

func relationshipMatrixModel() *Model {
	return &Model{
		Name: "fanout_matrix",
		Tables: map[string]Table{
			"orders": {
				Entities: map[string]ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]MetricDimension{
					"order_id": {Type: "string"}, "customer_id": {Type: "string"}, "region": {Type: "string"},
				},
			},
			"customers": {
				Entities: map[string]ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]MetricDimension{
					"customer_id": {Type: "string"}, "region": {Type: "string"},
				},
			},
			"profiles": {
				Entities: map[string]ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]MetricDimension{
					"customer_id": {Type: "string"}, "tier": {Type: "string"},
				},
			},
			"accounts": {
				Entities: map[string]ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]MetricDimension{"customer_id": {Type: "string"}},
			},
			"tags": {
				Entities: map[string]ModelEntitySpec{"tag_id": {Type: "primary", Fields: []string{"tag_id"}}}, GrainEntity: "tag_id",
				Dimensions: map[string]MetricDimension{
					"tag_id": {Type: "string"}, "order_id": {Type: "string"},
				},
			},
		},
		Dimensions: map[string]SemanticDimension{},
		Metrics:    map[string]Metric{},
	}
}
