package compiler

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestDatasetLocalAuthoringNormalizesIntoNativeGraph(t *testing.T) {
	spec, _, err := decodeSemanticModelResource("semantic-model.yaml", []byte(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets:
    orders:
      model: orders_model
      defaultTimeDimension: ordered_at
      dimensions:
        ordered_at:
          time: {nativeGrain: day, grains: [day, month]}
        order_status: {field: status}
      metrics:
        amount: {type: simple, agg: sum}
        orders: {type: simple, agg: count_distinct, field: id, timeDimension: ordered_at}
  metrics:
    average_amount: {type: ratio, numerator: amount, denominator: orders}
`))
	if err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name:        "sales",
		Connections: map[string]semanticmodel.Connection{"files": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"orders_source": {Connection: "files", Path: "orders.csv", Format: "csv"}},
		Tables: map[string]semanticmodel.Table{"orders_model": {
			Execution:   semanticmodel.ExecutionDefinition{Source: "orders_source"},
			Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			GrainEntity: "order",
			Dimensions: map[string]semanticmodel.MetricDimension{
				"id": {Datatype: semanticmodel.DataTypeString}, "amount": {Datatype: semanticmodel.DataTypeDecimal},
				"ordered_at": {Datatype: semanticmodel.DataTypeDate}, "status": {Datatype: semanticmodel.DataTypeString},
			},
		}},
	}
	if err := applySemanticModelSpec(model, spec); err != nil {
		t.Fatal(err)
	}
	if err := model.ValidateAuthored(); err != nil {
		t.Fatal(err)
	}
	amount := model.Metrics["amount"]
	if amount.Type != "aggregate" || amount.Aggregation != "sum" || amount.Input.Field != "orders.amount" || amount.Dataset != "orders" || amount.TimeDimension != "ordered_at" || amount.Empty != "null" {
		t.Fatalf("normalized amount = %#v", amount)
	}
	if count := model.Metrics["orders"]; count.Input.Field != "orders.id" || count.Empty != "zero" {
		t.Fatalf("normalized count = %#v", count)
	}
	if dim := model.Dimensions["order_status"]; dim.Datatype != semanticmodel.DataTypeString || dim.Bindings["orders"].Field != "orders.status" {
		t.Fatalf("normalized dimension = %#v", dim)
	}
	if dim := model.Dimensions["ordered_at"]; dim.Datatype != semanticmodel.DataTypeDate || dim.NativeGrain != "day" {
		t.Fatalf("normalized time = %#v", dim)
	}
	if model.Metrics["average_amount"].Type != "ratio" {
		t.Fatal("global ratio missing")
	}
}

func TestDatasetLocalAuthoringRejectsDuplicateMembers(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"metric across scopes", "datasets: {orders: {model: orders_model, metrics: {revenue: {type: simple, agg: sum}}}}\nmetrics: {revenue: {type: derived, expression: revenue * 2}}", "datasets.orders.metrics.revenue conflicts with spec.metrics.revenue"},
		{"metric across datasets", "datasets: {orders: {model: orders_model, metrics: {revenue: {type: simple, agg: sum}}}, refunds: {model: refunds_model, metrics: {revenue: {type: simple, agg: sum}}}}", "datasets.refunds.metrics.revenue conflicts with datasets.orders.metrics.revenue"},
		{"dimension across scopes", "datasets: {orders: {model: orders_model, dimensions: {status: {}}}}\ndimensions: {status: {bindings: {orders: {field: orders.status}}}}", "datasets.orders.dimensions.status conflicts with spec.dimensions.status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic-model:sales, name: sales}\nspec:\n"
			for _, line := range strings.Split(tc.body, "\n") {
				doc += "  " + line + "\n"
			}
			spec, _, err := decodeSemanticModelResource("semantic-model.yaml", []byte(doc))
			if err != nil {
				t.Fatal(err)
			}
			model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders_model": {}, "refunds_model": {}}}
			err = applySemanticModelSpec(model, spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDatasetLocalDatatypeAssertionsAndDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, dimension, datatype, want string
		unresolved                      bool
	}{
		{"inferred", "amount: {}", "Decimal", "", false},
		{"assertion mismatch", "amount: {datatype: String}", "Decimal", "incompatible with binding", false},
		{"unresolved at activation", "amount: {}", "", "requires a logical datatype", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			yaml := "apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic-model:sales, name: sales}\nspec:\n  datasets:\n    orders:\n      model: orders_model\n      dimensions:\n        " + tc.dimension + "\n      metrics:\n        amount_sum: {type: simple, agg: sum, field: amount}\n"
			spec, _, err := decodeSemanticModelResource("semantic-model.yaml", []byte(yaml))
			if err != nil {
				t.Fatal(err)
			}
			model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders_model": {
				Entities: map[string]semanticmodel.EntityDefinition{"row": {Type: "primary", Fields: []string{"amount"}}}, GrainEntity: "row",
				Dimensions: map[string]semanticmodel.MetricDimension{"amount": {Datatype: semanticmodel.LogicalDataType(tc.datatype)}},
			}}}
			if err := applySemanticModelSpec(model, spec); err != nil {
				t.Fatal(err)
			}
			if tc.unresolved {
				if err := model.ValidateAuthoringSemanticGraph(); err != nil {
					t.Fatalf("authoring should defer discovery: %v", err)
				}
			}
			err = model.ValidateSemanticGraph()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validation error = %v, want %q", err, tc.want)
			}
		})
	}
}
