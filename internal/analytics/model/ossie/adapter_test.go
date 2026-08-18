package ossie

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestOfficialSchemaPinnedChecksum(t *testing.T) {
	digest := sha256.Sum256(OfficialSchema())
	if got := hex.EncodeToString(digest[:]); got != "8ce9f82aa92080265f9ae119e31cda5bef062f489674d3c467245c2d4c5ff264" {
		t.Fatalf("official schema bytes drifted: sha256=%s", got)
	}
}

func TestOfficialSchemaFixtures(t *testing.T) {
	validFixture, err := os.ReadFile("testdata/official-valid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(validFixture); err != nil {
		t.Fatalf("official valid fixture rejected: %v", err)
	}
	invalidFixture, err := os.ReadFile("testdata/official-invalid.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(invalidFixture); err == nil {
		t.Fatal("official invalid fixture accepted")
	}
	valid := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: orders
        primary_key: [order_id, line_no]
        fields:
          - name: order_id
            expression: {dialects: [{dialect: ANSI_SQL, expression: order_id}]}
            datatype: String
      - name: customers
        source: customers
        primary_key: [customer_id]
    relationships:
      - name: order_customer
        from: orders
        to: customers
        from_columns: [customer_id]
        to_columns: [customer_id]
    metrics:
      - name: revenue
        expression: {dialects: [{dialect: ANSI_SQL, expression: SUM(orders.revenue)}]}
`)
	if err := Validate(valid); err != nil {
		t.Fatalf("valid official fixture rejected: %v", err)
	}
	unknown := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders, unexpected: true}]
`)
	if err := Validate(unknown); err == nil {
		t.Fatal("official schema accepted unknown dataset property")
	}
	wrongVersion := []byte(`version: 0.1.0
semantic_model: [{name: sales, datasets: [{name: orders, source: orders}]}]
`)
	if err := Validate(wrongVersion); err == nil {
		t.Fatal("official schema accepted an unsupported version")
	}
}

func TestNativeOssieNativeRoundTripPreservesPortableAndLeapViewSemantics(t *testing.T) {
	projectModels := map[string]semanticmodel.Table{
		"orders": {
			Description: "Orders",
			GrainEntity: "order_line",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"order_line": {Type: "primary", Fields: []string{"order_id", "line_no"}}, "customer": {Type: "foreign", Fields: []string{"customer_id"}}, "order_number": {Type: "unique", Fields: []string{"order_number"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"order_id": {SourceField: "order_id", Datatype: semanticmodel.DataTypeString}, "line_no": {SourceField: "line_no", Datatype: semanticmodel.DataTypeInteger}, "customer_id": {SourceField: "customer_id", Datatype: semanticmodel.DataTypeString}, "order_number": {SourceField: "order_number", Datatype: semanticmodel.DataTypeString}, "revenue": {SourceField: "revenue", Datatype: semanticmodel.DataTypeDecimal}, "payment_status": {SourceField: "payment_status", Datatype: semanticmodel.DataTypeString}, "purchase_date": {SourceField: "purchase_date", Datatype: semanticmodel.DataTypeDate}, "event_time": {SourceField: "event_time", Datatype: semanticmodel.DataTypeDateTimeTZ}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id":       {Type: "string", Datatype: semanticmodel.DataTypeString},
				"line_no":        {Type: "number", Datatype: semanticmodel.DataTypeInteger},
				"customer_id":    {Type: "string", Datatype: semanticmodel.DataTypeString},
				"order_number":   {Type: "string", Datatype: semanticmodel.DataTypeString},
				"revenue":        {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
				"payment_status": {Type: "string", Datatype: semanticmodel.DataTypeString},
				"purchase_date":  {Type: "date", Datatype: semanticmodel.DataTypeDate, Label: "Purchase date"},
				"event_time":     {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, Label: "Event time"},
			},
		},
		"customers": {
			Description: "Customers",
			GrainEntity: "customer",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"customer_id": {SourceField: "customer_id", Datatype: semanticmodel.DataTypeString}, "state": {SourceField: "state", Datatype: semanticmodel.DataTypeString}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
				"state":       {Type: "string", Datatype: semanticmodel.DataTypeString},
			},
		},
	}
	native := &semanticmodel.Model{
		Name: "sales", Description: "Governed sales", AIContext: &semanticmodel.AIContext{Instructions: "Use captured orders", Synonyms: []string{"sales"}, Examples: []string{"Revenue by state"}},
		Tables:   map[string]semanticmodel.Table{"orders": projectModels["orders"], "customers": projectModels["customers"]},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders", DefaultTimeDimension: "purchase_date"}, "customers": {Model: "customers"}},
	}
	native.StructuredRelationships = map[string]semanticmodel.RelationshipSpec{}
	native.StructuredRelationships["order_customer"] = semanticmodel.RelationshipSpec{
		From: semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Entity: "customer"},
		To:   semanticmodel.RelationshipEndpointSpec{Dataset: "customers", Entity: "customer"},
	}
	native.Dimensions = map[string]semanticmodel.SemanticDimension{
		"purchase_date": {Type: "Date", Datatype: semanticmodel.DataTypeDate, NativeGrain: "day", Grains: []string{"day", "week", "month"}, Calendar: "iso8601", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.purchase_date"}}},
		"event_time":    {Type: "DateTimeTz", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "hour", Grains: []string{"hour", "day"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.event_time"}}},
		"state":         {Type: "String", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "customers.state", Path: []string{"order_customer"}}}},
	}
	native.Filters = map[string]semanticmodel.SemanticFilterSpec{"captured": {Field: "orders.payment_status", Operator: "equals", Value: "captured"}}
	native.Metrics = map[string]semanticmodel.Metric{
		"revenue":             {Name: "revenue", Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Where: []string{"captured"}, Empty: "zero", Unit: "BRL", Format: "currency"},
		"order_count":         {Name: "order_count", Type: "aggregate", Dataset: "orders", Aggregation: "count_distinct", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
		"average_order_value": {Name: "average_order_value", Type: "ratio", Numerator: "revenue", Denominator: "order_count", Unit: "BRL", Format: "currency"},
		"revenue_per_line":    {Name: "revenue_per_line", Type: "derived", Expression: "safe_divide(${revenue}, ${order_count})"},
	}
	wire, err := Export(native)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if err := Validate(wire); err != nil {
		t.Fatalf("exported fixture official validation: %v\n%s", err, wire)
	}
	if !strings.Contains(string(wire), `"vendor_name": "LEAPVIEW"`) || !strings.Contains(string(wire), ExtensionVersion) {
		t.Fatalf("export omitted versioned LeapView extension: %s", wire)
	}
	got, err := Import(wire, projectModels)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got.Name != native.Name || got.Description != native.Description || len(got.Dimensions) != len(native.Dimensions) || len(got.Filters) != len(native.Filters) || len(got.Metrics) != len(native.Metrics) {
		t.Fatalf("roundtrip lost native semantics: got %#v", got)
	}
	if got.Metrics["average_order_value"].Type != "ratio" || got.Metrics["revenue"].Where[0] != "captured" || got.Dimensions["state"].Bindings["orders"].Path[0] != "order_customer" {
		t.Fatalf("roundtrip changed metric/dimension semantics: %#v", got)
	}
	if got.Dimensions["purchase_date"].Datatype != semanticmodel.DataTypeDate || got.Dimensions["purchase_date"].NativeGrain != "day" {
		t.Fatalf("roundtrip lost date datatype/native grain: %#v", got.Dimensions["purchase_date"])
	}
	if got.Dimensions["event_time"].Datatype != semanticmodel.DataTypeDateTimeTZ || got.Dimensions["event_time"].NativeGrain != "hour" {
		t.Fatalf("roundtrip lost DateTimeTz datatype/native grain: %#v", got.Dimensions["event_time"])
	}
	if got.Tables["orders"].GrainEntity != "order_line" || len(got.Tables["orders"].Entities["order_line"].Fields) != 2 {
		t.Fatalf("roundtrip lost composite model grain: %#v", got.Tables["orders"])
	}
}

func TestImportRejectsUnknownExtensionAndUnresolvedSourceWithoutPartialCompilation(t *testing.T) {
	unknown := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v999"}'
`)
	if _, err := Import(unknown, map[string]semanticmodel.Table{"orders": {}}); err == nil || !strings.Contains(err.Error(), "unsupported LeapView Ossie extension version") {
		t.Fatalf("unknown extension version error = %v", err)
	}
	unresolved := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: missing_model}]
`)
	if _, err := Import(unresolved, map[string]semanticmodel.Table{"orders": {}}); err == nil || !strings.Contains(err.Error(), "does not resolve to an existing project Model") {
		t.Fatalf("unresolved source error = %v", err)
	}
}

func TestImportRejectsUnsupportedCoreExecutableMetric(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: arbitrary
        expression: {dialects: [{dialect: ANSI_SQL, expression: SUM(orders.revenue) / COUNT(orders.id)}]}
`)
	if _, err := Import(doc, map[string]semanticmodel.Table{"orders": {}}); err == nil || !strings.Contains(err.Error(), "unsupported executable expression") {
		t.Fatalf("unsupported metric error = %v", err)
	}
}

func TestCoreImportRejectsPortableMetadataThatWouldSynthesizeModelState(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: orders
        primary_key: [order_id, line_no]
        fields:
          - name: order_id
            expression: {dialects: [{dialect: ANSI_SQL, expression: order_id}]}
            datatype: String
          - name: line_no
            expression: {dialects: [{dialect: ANSI_SQL, expression: line_no}]}
            datatype: Integer
            dimension: {is_time: false}
`)
	if _, err := Import(doc, map[string]semanticmodel.Table{"orders": {}}); err == nil || !strings.Contains(err.Error(), "requires an existing Model grain") {
		t.Fatalf("missing model grain error = %v", err)
	}
}

func TestCoreCountMetricsUseZeroEmptyPolicy(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: orders
        fields: [{name: order_id, expression: {dialects: [{dialect: ANSI_SQL, expression: order_id}]}, datatype: String}]
    metrics:
      - name: order_count
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(orders.order_id)}]}
      - name: distinct_orders
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(DISTINCT orders.order_id)}]}
`)
	got, err := Import(doc, strictnessProjectModels())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"order_count", "distinct_orders"} {
		if got.Metrics[name].Empty != "zero" {
			t.Fatalf("metric %q did not receive empty: zero: %#v", name, got.Metrics[name])
		}
	}
}

func TestCountExportPreservesDatasetRowSemantics(t *testing.T) {
	models := strictnessProjectModels()
	native := &semanticmodel.Model{
		Name:     "sales",
		Tables:   map[string]semanticmodel.Table{"orders": models["orders"]},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"row_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
		},
	}
	wire, err := Export(native)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"expression": "COUNT(orders.order_id)"`) {
		t.Fatalf("count export did not preserve input-field semantics: %s", wire)
	}
	got, err := Import(wire, models)
	if err != nil {
		t.Fatalf("import exported count: %v", err)
	}
	if got.Metrics["row_count"].Aggregation != "count" {
		t.Fatalf("round-tripped count metric = %#v", got.Metrics["row_count"])
	}
}

func TestCountExportRoundTripsNonGrainBookkeepingInput(t *testing.T) {
	models := strictnessProjectModels()
	native := &semanticmodel.Model{
		Name:     "sales",
		Tables:   map[string]semanticmodel.Table{"orders": models["orders"]},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"row_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.event_date"}, Empty: "zero"},
		},
	}
	wire, err := Export(native)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Import(wire, models)
	if err != nil {
		t.Fatalf("import exported count with non-grain input: %v", err)
	}
	if input := got.Metrics["row_count"].Input; input == nil || input.Field != "orders.event_date" {
		t.Fatalf("round-tripped count input = %#v", input)
	}
}

func TestCoreCountStarImportInfersSingleDataset(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: row_count
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(*)}]}
`)
	got, err := Import(doc, strictnessProjectModels())
	if err != nil {
		t.Fatal(err)
	}
	metric := got.Metrics["row_count"]
	if metric.Aggregation != "count" || metric.Dataset != "orders" || metric.Input == nil || metric.Input.Field != "orders.order_id" {
		t.Fatalf("core COUNT(*) metric = %#v", metric)
	}
}

func TestCoreCountFieldImportPreservesInputField(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: row_count
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(orders.event_date)}]}
`)
	got, err := Import(doc, strictnessProjectModels())
	if err != nil {
		t.Fatal(err)
	}
	metric := got.Metrics["row_count"]
	if metric.Aggregation != "count" || metric.Input == nil || metric.Input.Field != "orders.event_date" {
		t.Fatalf("core COUNT(field) metric = %#v", metric)
	}
}

func TestCountStarExtensionMustAgreeWithProvenModelInput(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: rows
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(*)}]}
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","metrics":{"rows":{"type":"aggregate","dataset":"orders","aggregation":"count","input":{"field":"orders.event_date"},"empty":"zero"}}}'
`)
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "disagrees with Ossie core") {
		t.Fatalf("COUNT(*) extension contradiction error = %v", err)
	}
}

func TestExtensionMetricsMergeWithCompatibleCoreMetrics(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: row_count
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(orders.order_id)}]}
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","metrics":{"distinct_orders":{"type":"aggregate","dataset":"orders","aggregation":"count_distinct","input":{"field":"orders.order_id"},"empty":"zero"}}}'
`)
	got, err := Import(doc, strictnessProjectModels())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Metrics) != 2 || got.Metrics["row_count"].Aggregation != "count" || got.Metrics["distinct_orders"].Aggregation != "count_distinct" {
		t.Fatalf("core and extension metrics were not merged: %#v", got.Metrics)
	}
}

func TestExtensionRejectsPortableMetricContradiction(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: row_count
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(orders.order_id)}]}
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","metrics":{"row_count":{"type":"aggregate","dataset":"orders","aggregation":"count_distinct","input":{"field":"orders.order_id"},"empty":"zero"}}}'
`)
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "disagrees with Ossie core") {
		t.Fatalf("portable metric contradiction error = %v", err)
	}
}

func TestEmptyExtensionRelationshipsDoNotEraseCoreRelationships(t *testing.T) {
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}, {name: customers, source: customers}]
    relationships:
      - name: order_customer
        from: orders
        to: customers
        from_columns: [customer_id]
        to_columns: [customer_id]
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","relationships":{}}'
`)
	got, err := Import(doc, map[string]semanticmodel.Table{
		"orders": {
			GrainEntity: "order",
			Entities: map[string]semanticmodel.ModelEntitySpec{
				"order":    {Type: "primary", Fields: []string{"order_id"}},
				"customer": {Type: "foreign", Fields: []string{"customer_id"}},
			},
			Columns: map[string]semanticmodel.ModelColumn{
				"order_id": {Datatype: semanticmodel.DataTypeString}, "customer_id": {Datatype: semanticmodel.DataTypeString},
			},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
			},
		},
		"customers": {
			GrainEntity: "customer",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"customer_id": {Datatype: semanticmodel.DataTypeString}},
			Dimensions:  map[string]semanticmodel.MetricDimension{"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.StructuredRelationships["order_customer"]; !ok {
		t.Fatalf("empty extension erased core relationship: %#v", got.StructuredRelationships)
	}
}

func TestImportRejectsUnsafeRelationshipsAndMetricPopulation(t *testing.T) {
	models := map[string]semanticmodel.Table{
		"orders": {
			GrainEntity: "order",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}, "customer": {Type: "foreign", Fields: []string{"customer_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"order_id": {Datatype: semanticmodel.DataTypeString}, "customer_id": {Datatype: semanticmodel.DataTypeString}},
			Dimensions:  map[string]semanticmodel.MetricDimension{"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
		},
		"customers": {
			GrainEntity: "customer",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"customer_id": {Datatype: semanticmodel.DataTypeString}, "state": {Datatype: semanticmodel.DataTypeString}},
			Dimensions:  map[string]semanticmodel.MetricDimension{"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "state": {Type: "string", Datatype: semanticmodel.DataTypeString}},
		},
	}
	unknownEntity := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}, {name: customers, source: customers}]
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","relationships":{"bad":{"from":{"dataset":"orders","entity":"missing"},"to":{"dataset":"customers","entity":"customer"}}}}'
`)
	if _, err := Import(unknownEntity, models); err == nil || !strings.Contains(err.Error(), "extension endpoint") {
		t.Fatalf("unknown extension relationship endpoint error = %v", err)
	}
	unprovenTarget := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets:
      - {name: orders, source: orders}
      - {name: customers, source: customers}
    relationships:
      - {name: bad, from: orders, to: customers, from_columns: [customer_id], to_columns: [state]}
`)
	if _, err := Import(unprovenTarget, models); err == nil || !strings.Contains(err.Error(), "must belong to a primary or unique entity") {
		t.Fatalf("unproven target key error = %v", err)
	}
	invalidFilter := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","filters":{"bad":{"field":"orders.missing","operator":"equals","value":"x"}},"metrics":{"revenue":{"type":"aggregate","dataset":"orders","aggregation":"sum","input":{"field":"orders.order_id"},"where":["bad"]}}}'
`)
	if _, err := Import(invalidFilter, models); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("invalid filter/where error = %v", err)
	}
	unknownMetric := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","metrics":{"ratio":{"type":"ratio","numerator":"missing","denominator":"missing"}}}'
`)
	if _, err := Import(unknownMetric, models); err == nil || !strings.Contains(err.Error(), "unknown metric") {
		t.Fatalf("unknown metric dependency error = %v", err)
	}
}

func TestImportRejectsAmbiguousImplicitBindingPath(t *testing.T) {
	models := map[string]semanticmodel.Table{
		"orders": {
			GrainEntity: "order",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}, "customer": {Type: "foreign", Fields: []string{"customer_id"}}, "region": {Type: "foreign", Fields: []string{"region_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"order_id": {Datatype: semanticmodel.DataTypeString}, "customer_id": {Datatype: semanticmodel.DataTypeString}, "region_id": {Datatype: semanticmodel.DataTypeString}},
			Dimensions:  map[string]semanticmodel.MetricDimension{"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "region_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
		},
		"regions": {
			GrainEntity: "region",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"region": {Type: "primary", Fields: []string{"region_id"}}, "customer": {Type: "foreign", Fields: []string{"customer_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"region_id": {Datatype: semanticmodel.DataTypeString}, "customer_id": {Datatype: semanticmodel.DataTypeString}},
			Dimensions:  map[string]semanticmodel.MetricDimension{"region_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
		},
		"customers": {
			GrainEntity: "customer",
			Entities:    map[string]semanticmodel.ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
			Columns:     map[string]semanticmodel.ModelColumn{"customer_id": {Datatype: semanticmodel.DataTypeString}, "state": {Datatype: semanticmodel.DataTypeString}},
			Dimensions:  map[string]semanticmodel.MetricDimension{"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "state": {Type: "string", Datatype: semanticmodel.DataTypeString}},
		},
	}
	doc := []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets:
      - {name: orders, source: orders}
      - {name: regions, source: regions}
      - {name: customers, source: customers}
    relationships:
      - {name: order_customer, from: orders, to: customers, from_columns: [customer_id], to_columns: [customer_id]}
      - {name: order_region, from: orders, to: regions, from_columns: [region_id], to_columns: [region_id]}
      - {name: region_customer, from: regions, to: customers, from_columns: [customer_id], to_columns: [customer_id]}
    metrics:
      - name: order_count
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(orders.order_id)}]}
    custom_extensions:
      - vendor_name: LEAPVIEW
        data: '{"version":"leapview.dev/ossie-extension/v1","dimensions":{"state":{"datatype":"String","bindings":{"orders":{"field":"customers.state"}}}}}'
`)
	if _, err := Import(doc, models); err == nil || !strings.Contains(err.Error(), "ambiguous relationship path") {
		t.Fatalf("ambiguous implicit path error = %v", err)
	}
}
