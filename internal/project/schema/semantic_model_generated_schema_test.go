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
  datasets:
  - name: orders
    model: sales_orders
`,
		"complete recursive shape": `
  datasets:
  - name: orders
    model: sales_orders
    defaultTimeDimension: purchase_date
    displayName: Orders
    aiContext:
      synonyms:
      - purchases
    metrics:
    - name: revenue
      type: simple
      where:
      - captured
      empty: 'null'
      agg: sum
  - name: customers
    model: sales_customers
  relationships:
  - name: orders_customers
    from:
      dataset: orders
      entity: customer
    to:
      dataset: customers
      fields:
      - customer_id
    description: Customer ownership
  dimensions:
  - name: purchase_date
    datatype: Date
    time:
      nativeGrain: day
      grains:
      - day
      - week
      calendar: iso8601
      timezone: UTC
    bindings:
    - dataset: orders
      field: orders.purchased_at
      path: []
  filters:
  - name: captured
    definition:
      all:
      - field: orders.status
        operator: in
        value:
        - captured
        - 2
        - true
      - not:
          field: orders.deleted_at
          operator: is_null
  metrics:
  - name: doubled
    type: derived
    expression: revenue * 2
    hidden: true
  - name: share
    type: ratio
    numerator: revenue
    denominator: doubled
    unit: percent
`,
		"float literal": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: threshold
    definition:
      field: orders.revenue
      operator: greater_than
      value: 2.5
`,
		"missing datasets": `
  metrics: []
`,
		"empty datasets": `
  datasets: []
`,
		"empty metrics is allowed": `
  datasets:
  - name: orders
    model: sales_orders
`,
		"missing metrics": `
  datasets:
  - name: orders
    model: sales_orders
`,
		"explicit null optional": `
  datasets:
  - name: orders
    model: sales_orders
    description: null
`,
		"invalid dataset key": `
  datasets:
  - name: sales-orders
    model: sales_orders
`,
		"external model id": `
  datasets:
  - name: orders
    model: model:sales_orders
`,
		"resource name dotted segment": `
  datasets:
  - name: orders
    model: a.1
`,
		"endpoint has both forms": `
  datasets:
  - name: orders
    model: sales_orders
  relationships:
  - name: loop
    from:
      dataset: orders
      entity: order
      fields:
      - id
    to:
      dataset: orders
      entity: order
`,
		"endpoint fields empty": `
  datasets:
  - name: orders
    model: sales_orders
  relationships:
  - name: loop
    from:
      dataset: orders
      fields: []
    to:
      dataset: orders
      entity: order
`,
		"not in values empty": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: not_in
      value: []
`,
		"time grains empty": `
  datasets:
  - name: orders
    model: sales_orders
  dimensions:
  - name: day
    datatype: Date
    time:
      nativeGrain: day
      grains: []
    bindings: []
`,
		"invalid time grain": `
  datasets:
  - name: orders
    model: sales_orders
  dimensions:
  - name: day
    datatype: Date
    time:
      nativeGrain: fortnight
      grains:
      - day
    bindings: []
`,
		"invalid datatype": `
  datasets:
  - name: orders
    model: sales_orders
  dimensions:
  - name: state
    datatype: Text
    bindings: []
`,
		"equals missing value": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: equals
`,
		"equals null": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: equals
      value: null
`,
		"equals array literal": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: equals
      value:
      - open
`,
		"equals object literal": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: equals
      value:
        state: open
`,
		"in scalar": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: in
      value: open
`,
		"in empty": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: in
      value: []
`,
		"null operator has value": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: is_null
      value: open
`,
		"empty boolean node": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      all: []
`,
		"empty any node": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      any: []
`,
		"mixed boolean and leaf": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      all:
      - field: orders.state
        operator: is_null
      field: orders.state
      operator: is_null
`,
		"invalid filter operator": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.state
      operator: contains
      value: open
`,
		"invalid field reference": `
  datasets:
  - name: orders
    model: sales_orders
  filters:
  - name: bad
    definition:
      field: orders.order-id
      operator: is_null
`,
		"simple rejects derived expression": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: simple
      expression: total
      agg: sum
      field: total
`,
		"invalid aggregation": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: simple
      agg: median
      field: total
`,
		"invalid empty enum": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: simple
      empty: missing
      agg: sum
      field: total
`,
		"empty where": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: simple
      where: []
      agg: sum
      field: total
`,
		"old top-level aggregate": `
  datasets:
  - name: orders
    model: sales_orders
  metrics:
  - name: bad
    type: aggregate
    dataset: orders
    aggregation: sum
    input:
      field: orders.total
`,
		"simple at top level": `
  datasets:
  - name: orders
    model: sales_orders
  metrics:
  - name: bad
    type: simple
    agg: sum
    field: total
`,
		"derived inside dataset": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: derived
      expression: total * 2
`,
		"qualified local field": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: simple
      agg: sum
      field: orders.total
`,
		"old local aggregate spelling": `
  datasets:
  - name: orders
    model: sales_orders
    metrics:
    - name: bad
      type: aggregate
      aggregation: sum
      field: total
`,
		"unknown nested field": `
  datasets:
  - name: orders
    model: sales_orders
    surprise: true
`,
	}
	wantValid := map[string]bool{
		"minimal":                           true,
		"complete recursive shape":          true,
		"float literal":                     true,
		"empty metrics is allowed":          true,
		"missing datasets":                  false,
		"empty datasets":                    false,
		"missing metrics":                   true,
		"explicit null optional":            false,
		"invalid dataset key":               false,
		"external model id":                 false,
		"resource name dotted segment":      true,
		"endpoint has both forms":           false,
		"endpoint fields empty":             false,
		"not in values empty":               false,
		"time grains empty":                 false,
		"invalid time grain":                false,
		"invalid datatype":                  false,
		"equals missing value":              false,
		"equals null":                       false,
		"equals array literal":              false,
		"equals object literal":             false,
		"in scalar":                         false,
		"in empty":                          false,
		"null operator has value":           false,
		"empty boolean node":                false,
		"empty any node":                    false,
		"mixed boolean and leaf":            false,
		"invalid filter operator":           false,
		"invalid field reference":           false,
		"simple rejects derived expression": false,
		"old top-level aggregate":           false,
		"simple at top level":               false,
		"derived inside dataset":            false,
		"qualified local field":             false,
		"old local aggregate spelling":      false,
		"invalid aggregation":               false,
		"invalid empty enum":                false,
		"empty where":                       false,
		"unknown nested field":              false,
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
