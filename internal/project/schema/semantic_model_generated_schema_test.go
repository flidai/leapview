package configschema

import (
	"bytes"
	"testing"
)

func TestSemanticModelSchemaAcceptanceMatrix(t *testing.T) {
	t.Parallel()
	// The generated TypeSpec JSON Schema is authoritative after migration.
	// Keep datasets non-empty because that is the exported-schema/compiler-
	// effective rule, even though the legacy CUE definition did not encode
	// minProperties.
	cases := map[string]string{
		"minimal": `
  datasets: {orders: {model: sales_orders}}
  metrics: {}`,
		"complete recursive shape": `
  datasets:
    orders: {model: sales_orders, defaultTimeDimension: purchase_date, displayName: Orders, aiContext: {synonyms: [purchases]}}
    customers: {model: sales_customers}
  relationships:
    orders_customers:
      from: {dataset: orders, entity: customer}
      to: {dataset: customers, fields: [customer_id]}
      description: Customer ownership
  dimensions:
    purchase_date:
      datatype: Date
      time: {nativeGrain: day, grains: [day, week], calendar: iso8601, timezone: UTC}
      bindings: {orders: {field: orders.purchased_at, path: []}}
  filters:
    captured:
      all:
        - {field: orders.status, operator: in, value: [captured, 2, true]}
        - {not: {field: orders.deleted_at, operator: is_null}}
  metrics:
    revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}, where: [captured], empty: 'null'}
    doubled: {type: derived, expression: revenue * 2, hidden: true}
    share: {type: ratio, numerator: revenue, denominator: doubled, unit: percent}`,
		"float literal": `
  datasets: {orders: {model: sales_orders}}
  filters: {threshold: {field: orders.revenue, operator: greater_than, value: 2.5}}
  metrics: {}`,
		"missing datasets": `
  metrics: {}`,
		"empty datasets": `
  datasets: {}
  metrics: {}`,
		"empty metrics is allowed": `
  datasets: {orders: {model: sales_orders}}
  metrics: {}`,
		"missing metrics": `
  datasets: {orders: {model: sales_orders}}`,
		"explicit null optional": `
  datasets: {orders: {model: sales_orders, description: null}}
  metrics: {}`,
		"invalid dataset key": `
  datasets: {sales-orders: {model: sales_orders}}
  metrics: {}`,
		"external model id": `
  datasets: {orders: {model: model:sales_orders}}
  metrics: {}`,
		"resource name dotted segment": `
  datasets: {orders: {model: a.1}}
  metrics: {}`,
		"endpoint has both forms": `
  datasets: {orders: {model: sales_orders}}
  relationships: {loop: {from: {dataset: orders, entity: order, fields: [id]}, to: {dataset: orders, entity: order}}}
  metrics: {}`,
		"endpoint fields empty": `
  datasets: {orders: {model: sales_orders}}
  relationships: {loop: {from: {dataset: orders, fields: []}, to: {dataset: orders, entity: order}}}
  metrics: {}`,
		"not in values empty": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: not_in, value: []}}
  metrics: {}`,
		"time grains empty": `
  datasets: {orders: {model: sales_orders}}
  dimensions: {day: {datatype: Date, time: {nativeGrain: day, grains: []}, bindings: {}}}
  metrics: {}`,
		"invalid time grain": `
  datasets: {orders: {model: sales_orders}}
  dimensions: {day: {datatype: Date, time: {nativeGrain: fortnight, grains: [day]}, bindings: {}}}
  metrics: {}`,
		"invalid datatype": `
  datasets: {orders: {model: sales_orders}}
  dimensions: {state: {datatype: Text, bindings: {}}}
  metrics: {}`,
		"equals missing value": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: equals}}
  metrics: {}`,
		"equals null": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: equals, value: null}}
  metrics: {}`,
		"equals array literal": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: equals, value: [open]}}
  metrics: {}`,
		"equals object literal": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: equals, value: {state: open}}}
  metrics: {}`,
		"in scalar": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: in, value: open}}
  metrics: {}`,
		"in empty": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: in, value: []}}
  metrics: {}`,
		"null operator has value": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: is_null, value: open}}
  metrics: {}`,
		"empty boolean node": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {all: []}}
  metrics: {}`,
		"empty any node": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {any: []}}
  metrics: {}`,
		"mixed boolean and leaf": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {all: [{field: orders.state, operator: is_null}], field: orders.state, operator: is_null}}
  metrics: {}`,
		"invalid filter operator": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.state, operator: contains, value: open}}
  metrics: {}`,
		"invalid field reference": `
  datasets: {orders: {model: sales_orders}}
  filters: {bad: {field: orders.order-id, operator: is_null}}
  metrics: {}`,
		"aggregate foreign field": `
  datasets: {orders: {model: sales_orders}}
  metrics: {bad: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.total}, expression: total}}`,
		"invalid aggregation": `
  datasets: {orders: {model: sales_orders}}
  metrics: {bad: {type: aggregate, dataset: orders, aggregation: median, input: {field: orders.total}}}`,
		"invalid empty enum": `
  datasets: {orders: {model: sales_orders}}
  metrics: {bad: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.total}, empty: missing}}`,
		"empty where": `
  datasets: {orders: {model: sales_orders}}
  metrics: {bad: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.total}, where: []}}`,
		"unknown nested field": `
  datasets: {orders: {model: sales_orders, surprise: true}}
  metrics: {}`,
	}
	wantValid := map[string]bool{
		"minimal":                      true,
		"complete recursive shape":     true,
		"float literal":                true,
		"empty metrics is allowed":     true,
		"missing datasets":             false,
		"empty datasets":               false,
		"missing metrics":              false,
		"explicit null optional":       false,
		"invalid dataset key":          false,
		"external model id":            false,
		"resource name dotted segment": true,
		"endpoint has both forms":      false,
		"endpoint fields empty":        false,
		"not in values empty":          false,
		"time grains empty":            false,
		"invalid time grain":           false,
		"invalid datatype":             false,
		"equals missing value":         false,
		"equals null":                  false,
		"equals array literal":         false,
		"equals object literal":        false,
		"in scalar":                    false,
		"in empty":                     false,
		"null operator has value":      false,
		"empty boolean node":           false,
		"empty any node":               false,
		"mixed boolean and leaf":       false,
		"invalid filter operator":      false,
		"invalid field reference":      false,
		"aggregate foreign field":      false,
		"invalid aggregation":          false,
		"invalid empty enum":           false,
		"empty where":                  false,
		"unknown nested field":         false,
	}
	if len(wantValid) != len(cases) {
		t.Fatalf("acceptance matrix has %d expected outcomes for %d fixtures", len(wantValid), len(cases))
	}
	for name, spec := range cases {
		name, spec := name, spec
		t.Run(name, func(t *testing.T) {
			document := []byte("apiVersion: leapview.dev/v1\nkind: SemanticModel\nmetadata: {id: semantic-model:sales, name: sales}\nspec:" + spec + "\n")
			err := ValidateBytes(KindSemanticModel, "semantic-model.yaml", document)
			if wantValid[name] && err != nil {
				t.Fatalf("valid SemanticModel rejected: %v\n%s", err, document)
			}
			if !wantValid[name] && err == nil {
				t.Fatalf("invalid SemanticModel accepted\n%s", document)
			}
		})
	}
}

func TestReachableGeneratedDefinitionsPrunesUnrelatedContracts(t *testing.T) {
	definitions := map[string]any{
		"Root":   map[string]any{"properties": map[string]any{"child": map[string]any{"$ref": "#/$defs/Child"}}},
		"Child":  map[string]any{"type": "string"},
		"Orphan": map[string]any{"$ref": "#/$defs/Missing"},
	}
	got := reachableGeneratedDefinitions(definitions, "Root")
	if _, ok := got["Root"]; !ok {
		t.Fatal("reachable root was pruned")
	}
	if _, ok := got["Child"]; !ok {
		t.Fatal("transitively reachable definition was pruned")
	}
	if _, ok := got["Orphan"]; ok {
		t.Fatal("unreachable definition was retained")
	}

	for _, kind := range []Kind{KindConnection, KindSource, KindModel} {
		encoded, err := generatedJSONSchema(kind)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encoded, []byte(`"SemanticModel"`)) || bytes.Contains(encoded, []byte(`"SemanticAccessGrant"`)) {
			t.Fatalf("%s export retained SemanticModel-only definitions", kind)
		}
	}
}

func TestSemanticModelSchemaCarriesAccessProfile(t *testing.T) {
	encoded, err := generatedJSONSchema(KindSemanticModel)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(`"x-leapview-semantic-access-profile": "leapview.semantic-access/v1"`)
	if count := bytes.Count(encoded, marker); count != 3 {
		t.Fatalf("SemanticModel schema contains %d semantic-access profile markers, want root, definition, and contract metadata", count)
	}
}
