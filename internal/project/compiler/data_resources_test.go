package compiler

import (
	"encoding/json"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestTypedDataResourceLoweringPreservesStructureAndSecrets(t *testing.T) {
	connection, err := decodeConnectionResource("connection.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:files, name: files}
spec:
  type: managed
  defaults:
    csv: {header: true}
`), metadata{})
	if err != nil {
		t.Fatalf("decode typed connection: %v", err)
	}
	if connection.Kind != "managed" || connection.ReaderDefaults == nil || connection.ReaderDefaults.Csv == nil || connection.ReaderDefaults.Csv.Header == nil || *connection.ReaderDefaults.Csv.Header != true || len(connection.Auth) != 0 || connection.Host != "" {
		t.Fatalf("lowered connection leaked or lost fields: %#v", connection)
	}
	source, err := decodeSourceResource("source.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec:
  connection: files
  location:
    type: path
    path: orders.csv
    format: csv
    options: {header: false}
  schema:
    mode: compatible
    fields:
      id: {datatype: Integer, nullable: false}
`), metadata{})
	if err != nil {
		t.Fatalf("decode typed source: %v", err)
	}
	if source.LocationType != semanticmodel.KindPath || source.Path != "orders.csv" || source.Fields["id"].Datatype != semanticmodel.DataTypeInteger {
		t.Fatalf("lowered source = %#v", source)
	}
	effective, err := ResolveEffectivePathLocation(source, connection)
	if err != nil {
		t.Fatalf("resolve effective options: %v", err)
	}
	variant, ok := effective.Value.(*projectcontracts.CSVPathSourceLocation)
	if !ok || variant.Options == nil || variant.Options.Header == nil || *variant.Options.Header != false {
		t.Fatalf("source option did not override connection default: %#v", effective)
	}
}

func TestTypedConnectionLoweringPreservesExplicitPublicAccess(t *testing.T) {
	public, err := decodeConnectionResource("connection.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:files, name: files}
spec:
  type: s3
  access: public
`), metadata{})
	if err != nil {
		t.Fatalf("decode public connection: %v", err)
	}
	omitted, err := decodeConnectionResource("connection.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:files, name: files}
spec:
  type: s3
`), metadata{})
	if err != nil {
		t.Fatalf("decode omitted connection: %v", err)
	}
	if public.Access != semanticmodel.ConnectionAccessPublic || omitted.Access != "" || public.Access == omitted.Access {
		t.Fatalf("access lowering public=%q omitted=%q", public.Access, omitted.Access)
	}
	if public.Credentials.Provider != "" || len(public.Auth) != 0 || public.Host != "" {
		t.Fatalf("public connection leaked target state: %#v", public)
	}
}

func TestTypedConnectionLoweringRejectsUnsupportedPublicVariant(t *testing.T) {
	_, err := decodeConnectionResource("connection.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata: {id: connection:warehouse, name: warehouse}
spec:
  type: postgres
  access: public
`), metadata{})
	if err == nil {
		t.Fatal("postgres public access was accepted despite generated variant having no access property")
	}
}

func TestTypedDataResourceLoweringRejectsWrongLocationOption(t *testing.T) {
	_, err := decodeSourceResource("source.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec:
  connection: files
  location:
    type: path
    path: orders.csv
    format: csv
    options: {unionByName: true}
`), metadata{})
	if err == nil {
		t.Fatal("typed source accepted a parquet-only option for csv")
	}
}

func TestTypedRelationLocationHonorsConnectorAndIdentifierBoundaries(t *testing.T) {
	source, err := decodeSourceResource("source.yaml", []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata: {id: source:orders, name: orders}
spec:
  connection: warehouse
  location:
    type: relation
    catalog: analytics
    schema: commerce
    name: orders
`), metadata{})
	if err != nil {
		t.Fatalf("decode relation source: %v", err)
	}
	if err := source.Validate("orders", map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres"}}); err != nil {
		t.Fatalf("valid relation source rejected: %v", err)
	}
	invalid := source
	invalid.Catalog = "analytics.bad"
	if err := invalid.Validate("orders", map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres"}}); err == nil {
		t.Fatal("relation source accepted catalog traversal/dot identifier")
	}
	pathSource := semanticmodel.Source{LocationType: semanticmodel.KindPath, Path: "orders.csv", Format: "csv", Connection: "warehouse"}
	if err := pathSource.Validate("orders", map[string]semanticmodel.Connection{"warehouse": {Kind: "postgres"}}); err == nil {
		t.Fatal("path source accepted relation-only connector")
	}
}

func TestTypedModelLoweringRetainsAuthoredSQLForDetailProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{name: "zip_geolocations", sql: `WITH normalized AS (SELECT * FROM source."olist.geolocation") SELECT * FROM normalized`},
		{name: "sales_orders", sql: `SELECT order_id, revenue FROM source."olist.orders"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:` + tc.name + `, name: ` + tc.name + `}
spec:
  definition:
    type: sql
    sql: |-
      ` + strings.ReplaceAll(tc.sql, "\n", "\n      ") + `
  entities: {order: {type: primary, fields: [order_id]}}
  grain: {entity: order}
  fields: {order_id: {datatype: String}}
`
			table, _, authored, err := decodeModelResourceWithDefinition("model.yaml", []byte(document), metadata{})
			if err != nil {
				t.Fatalf("decode model: %v", err)
			}
			if authored.Type != "sql" || authored.SQL != tc.sql {
				t.Fatalf("authored definition = %#v, want SQL %q", authored, tc.sql)
			}
			if table.Execution.SQL != tc.sql {
				t.Fatalf("runtime execution SQL = %q, want authored SQL", table.Execution.SQL)
			}
		})
	}
}

func TestTypedModelFieldsAllowMetadataWithoutDatatypeAndMayBeOmitted(t *testing.T) {
	base := `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:customers, name: customers}
spec:
  definition: {type: sql, sql: "SELECT customer_id, state FROM source.customers"}
  entities: {customer: {type: primary, fields: [customer_id]}}
  grain: {entity: customer}
`
	table, _, err := decodeModelResource("model.yaml", []byte(base+"  fields: {customer_id: {label: Customer ID}}\n"), metadata{})
	if err != nil {
		t.Fatalf("metadata-only field rejected: %v", err)
	}
	if got := table.Dimensions["customer_id"]; got.Datatype != "" || got.Label != "Customer ID" {
		t.Fatalf("metadata-only field = %#v", got)
	}
	if _, _, err := decodeModelResource("model.yaml", []byte(base), metadata{}); err != nil {
		t.Fatalf("omitted fields rejected: %v", err)
	}
}

func TestTypedSemanticModelLoweringPreservesRuntimeCompatibility(t *testing.T) {
	spec, aiContext, err := decodeSemanticModelResource("semantic-model.yaml", []byte(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
aiContext: {instructions: Use governed sales language.}
spec:
  datasets:
    orders: {model: orders_model, defaultTimeDimension: ordered_at, displayName: Orders}
  relationships:
    customer:
      from: {dataset: orders, entity: customer}
      to: {dataset: orders, fields: [customer_id]}
  dimensions:
    ordered_at:
      datatype: DateTime
      time: {nativeGrain: second, grains: [second, day], timezone: UTC}
      bindings: {orders: {field: orders.ordered_at}}
  filters:
    captured: {field: orders.status, operator: equals, value: captured}
  metrics:
    order_count: {type: aggregate, dataset: orders, aggregation: count, input: {field: orders.order_id}}
    revenue: {type: aggregate, dataset: orders, aggregation: sum, input: {field: orders.revenue}, where: [captured]}
    doubled: {type: derived, expression: revenue * 2, hidden: true}
    share: {type: ratio, numerator: revenue, denominator: doubled}
`))
	if err != nil {
		t.Fatalf("decode SemanticModel: %v", err)
	}
	if aiContext == nil || aiContext.Instructions != "Use governed sales language." {
		t.Fatalf("top-level aiContext = %#v", aiContext)
	}
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders_model": {
				Entities: map[string]semanticmodel.EntityDefinition{
					"customer": {Type: "primary", Fields: []string{"customer_id"}},
				},
			},
		},
	}
	if err := applySemanticModelSpec(model, spec); err != nil {
		t.Fatalf("lower SemanticModel: %v", err)
	}
	if got := model.Datasets["orders"]; got.Model != "orders_model" || got.DefaultTimeDimension != "ordered_at" || got.DisplayName != "Orders" {
		t.Fatalf("dataset = %#v", got)
	}
	if got := model.Tables["orders"].ModelName; got != "orders_model" {
		t.Fatalf("runtime table model name = %q", got)
	}
	if len(model.Relationships) != 1 || model.Relationships[0].Cardinality != "one_to_one" || model.Relationships[0].FromFields[0] != "customer_id" {
		t.Fatalf("relationships = %#v", model.Relationships)
	}
	if got := model.Dimensions["ordered_at"]; got.Datatype != semanticmodel.DataTypeDateTime || got.NativeGrain != "second" || len(got.Grains) != 2 || got.Timezone != "UTC" {
		t.Fatalf("dimension = %#v", got)
	}
	if got := model.Filters["captured"]; got.Operator != "equals" || got.Value != "captured" {
		t.Fatalf("filter = %#v", got)
	}
	if got := model.Metrics["order_count"].Empty; got != "zero" {
		t.Fatalf("count empty default = %q, want zero", got)
	}
	if got := model.Metrics["revenue"]; got.Empty != "null" || len(got.Where) != 1 || got.Where[0] != "captured" {
		t.Fatalf("revenue metric = %#v", got)
	}
	if got := model.Metrics["doubled"]; got.Expression != "revenue * 2" || !got.Hidden {
		t.Fatalf("derived metric = %#v", got)
	}
	if got := model.Metrics["share"]; got.Numerator != "revenue" || got.Denominator != "doubled" {
		t.Fatalf("ratio metric = %#v", got)
	}
}

func TestTypedSemanticModelLoweringRejectsUncompiledAccessPolicy(t *testing.T) {
	spec, _, err := decodeSemanticModelResource("semantic-model.yaml", []byte(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  accessGrants:
    canViewSales: {userAttribute: department, allowedValues: [sales]}
  datasets:
    orders: {model: orders_model, requiredAccessGrants: [canViewSales]}
  metrics: {}
`))
	if err != nil {
		t.Fatalf("structural access policy decode: %v", err)
	}
	err = applySemanticModelSpec(&semanticmodel.Model{Name: "sales"}, spec)
	if err == nil || !strings.Contains(err.Error(), "compiled access-policy support is not available") {
		t.Fatalf("uncompiled access policy error = %v", err)
	}
}

func TestTypedSemanticModelLoweringPreservesExactNumericLiterals(t *testing.T) {
	for _, token := range []string{"5", "2.5", "9007199254740993", "9007199254740993.125"} {
		t.Run(token, func(t *testing.T) {
			spec, _, err := decodeSemanticModelResource("semantic-model.yaml", []byte(`apiVersion: leapview.dev/v1
kind: SemanticModel
metadata: {id: semantic-model:sales, name: sales}
spec:
  datasets: {orders: {model: orders_model}}
  filters: {threshold: {field: orders.amount, operator: equals, value: `+token+`}}
  metrics: {}
`))
			if err != nil {
				t.Fatal(err)
			}
			model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders_model": {}}}
			if err := applySemanticModelSpec(model, spec); err != nil {
				t.Fatal(err)
			}
			number, ok := model.Filters["threshold"].Value.(json.Number)
			if !ok || number.String() != token {
				t.Fatalf("lowered numeric literal = %#v (%T), want json.Number(%q)", model.Filters["threshold"].Value, model.Filters["threshold"].Value, token)
			}
		})
	}
}
