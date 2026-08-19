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
				ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
			},
		},
		{
			name: "many to one rejects non key target",
			relationship: Relationship{
				ID: "orders_customers_by_region", FromDataset: "orders", FromFields: []string{"region"}, ToDataset: "customers", ToFields: []string{"region"}, Cardinality: "many_to_one",
			},
			wantErr: `relationship "orders_customers_by_region" many_to_one endpoint "customers.region" must belong to a primary or unique entity`,
		},
		{
			name: "one to one requires both primary keys",
			relationship: Relationship{
				ID: "customers_profiles", FromDataset: "customers", FromFields: []string{"customer_id"}, ToDataset: "profiles", ToFields: []string{"customer_id"}, Cardinality: "one_to_one",
			},
		},
		{
			name: "one to one rejects non key source",
			relationship: Relationship{
				ID: "customers_profiles_by_region", FromDataset: "customers", FromFields: []string{"region"}, ToDataset: "profiles", ToFields: []string{"customer_id"}, Cardinality: "one_to_one",
			},
			wantErr: `relationship "customers_profiles_by_region" one_to_one endpoint "customers.region" must belong to a primary or unique entity`,
		},
		{
			name: "one to one rejects non key target",
			relationship: Relationship{
				ID: "customers_profiles_by_tier", FromDataset: "customers", FromFields: []string{"customer_id"}, ToDataset: "profiles", ToFields: []string{"tier"}, Cardinality: "one_to_one",
			},
			wantErr: `relationship "customers_profiles_by_tier" one_to_one endpoint "profiles.tier" must belong to a primary or unique entity`,
		},
		{
			name: "one to many is unsafe",
			relationship: Relationship{
				ID: "customers_orders", FromDataset: "customers", FromFields: []string{"customer_id"}, ToDataset: "orders", ToFields: []string{"customer_id"}, Cardinality: "one_to_many",
			},
			wantErr: `relationship "customers_orders" has unsafe cardinality "one_to_many"`,
		},
		{
			name: "many to many is unsafe",
			relationship: Relationship{
				ID: "orders_tags", FromDataset: "orders", FromFields: []string{"order_id"}, ToDataset: "tags", ToFields: []string{"order_id"}, Cardinality: "many_to_many",
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
		{ID: "customers_profiles", FromDataset: "customers", FromFields: []string{"customer_id"}, ToDataset: "profiles", ToFields: []string{"customer_id"}, Cardinality: "one_to_one"},
		{ID: "profiles_accounts", FromDataset: "profiles", FromFields: []string{"customer_id"}, ToDataset: "accounts", ToFields: []string{"customer_id"}, Cardinality: "one_to_one"},
		{ID: "accounts_customers", FromDataset: "accounts", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "one_to_one"},
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
	orders.Entities = map[string]EntityDefinition{"order_line": {Type: "primary", Fields: []string{"order_id", "line_number"}}}
	orders.GrainEntity = "order_line"
	model.Tables["orders"] = orders
	customers := model.Tables["customers"]
	customers.Dimensions["line_number"] = MetricDimension{Type: "number", Datatype: DataTypeInteger}
	customers.Entities = map[string]EntityDefinition{"customer_line": {Type: "unique", Fields: []string{"customer_id", "line_number"}}}
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
	customerTable.Entities = map[string]EntityDefinition{"customer": {Type: "unique", Fields: []string{"customer_id"}}}
	customerTable.GrainEntity = "customer"
	model.Tables["customers"] = customerTable
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "primary or unique entity") {
		t.Fatalf("non-unique tuple error = %v", err)
	}
}

func TestRelationshipKeyTupleRequiresExactLogicalDatatype(t *testing.T) {
	model := relationshipMatrixModel()
	orders := model.Tables["orders"]
	orders.Dimensions["customer_id"] = MetricDimension{Type: "number", Datatype: DataTypeInteger}
	model.Tables["orders"] = orders
	customers := model.Tables["customers"]
	customers.Dimensions["customer_id"] = MetricDimension{Type: "number", Datatype: DataTypeDecimal}
	model.Tables["customers"] = customers
	model.Relationships = []Relationship{{
		ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
	}}
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("numeric logical datatype mismatch accepted: %v", err)
	}
}

func TestModelChecksRejectImplicitAcceptedValueAndRelationshipCasts(t *testing.T) {
	model := relationshipMatrixModel()
	orders := model.Tables["orders"]
	orders.Checks = []ModelCheck{{Type: "accepted_values", Field: "customer_id", Values: []string{"1"}}}
	model.Tables["orders"] = orders
	if err := model.validateSemanticGraph(); err != nil {
		t.Fatalf("string accepted_values unexpectedly rejected: %v", err)
	}

	orders = model.Tables["orders"]
	orders.Dimensions["customer_id"] = MetricDimension{Type: "number", Datatype: DataTypeInteger}
	orders.Checks = []ModelCheck{{Type: "accepted_values", Field: "customer_id", Values: []string{"1"}}}
	model.Tables["orders"] = orders
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "accepted_values requires a String field") {
		t.Fatalf("numeric accepted_values error = %v", err)
	}

	model = relationshipMatrixModel()
	orders = model.Tables["orders"]
	orders.Checks = []ModelCheck{{Type: "relationship", Field: "customer_id", To: "customers.customer_id"}}
	model.Tables["orders"] = orders
	customers := model.Tables["customers"]
	customers.Dimensions["customer_id"] = MetricDimension{Type: "number", Datatype: DataTypeDecimal}
	model.Tables["customers"] = customers
	if err := model.validateSemanticGraph(); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible check relationship error = %v", err)
	}
}

func relationshipMatrixModel() *Model {
	return &Model{
		Name: "fanout_matrix",
		Tables: map[string]Table{
			"orders": {
				Entities: map[string]EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]MetricDimension{
					"order_id": {Type: "string", Datatype: DataTypeString}, "customer_id": {Type: "string", Datatype: DataTypeString}, "region": {Type: "string", Datatype: DataTypeString},
				},
			},
			"customers": {
				Entities: map[string]EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]MetricDimension{
					"customer_id": {Type: "string", Datatype: DataTypeString}, "region": {Type: "string", Datatype: DataTypeString},
				},
			},
			"profiles": {
				Entities: map[string]EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]MetricDimension{
					"customer_id": {Type: "string", Datatype: DataTypeString}, "tier": {Type: "string", Datatype: DataTypeString},
				},
			},
			"accounts": {
				Entities: map[string]EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer_id",
				Dimensions: map[string]MetricDimension{"customer_id": {Type: "string", Datatype: DataTypeString}},
			},
			"tags": {
				Entities: map[string]EntityDefinition{"tag_id": {Type: "primary", Fields: []string{"tag_id"}}}, GrainEntity: "tag_id",
				Dimensions: map[string]MetricDimension{
					"tag_id": {Type: "string", Datatype: DataTypeString}, "order_id": {Type: "string", Datatype: DataTypeString},
				},
			},
		},
		Datasets: map[string]SemanticDatasetSpec{
			"accounts": {Model: "accounts"}, "customers": {Model: "customers"}, "orders": {Model: "orders"}, "profiles": {Model: "profiles"}, "tags": {Model: "tags"},
		},
		Dimensions: map[string]SemanticDimension{},
		Metrics:    map[string]Metric{},
	}
}
