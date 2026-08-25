package compiler

import (
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

func TestTypedModelRelationshipReferenceRequiresDatasetAndField(t *testing.T) {
	valid := `apiVersion: leapview.dev/v1
kind: Model
metadata: {id: model:orders, name: orders}
spec:
  definition: {type: direct, source: source:orders}
  entities: {id: {type: primary, fields: [id]}}
  grain: {entity: id}
  fields: {id: {datatype: Integer}, customer_id: {datatype: Integer}}
  checks:
    - {type: relationship, field: customer_id, to: customers.customer_id}
`
	table, _, err := decodeModelResource("model.yaml", []byte(valid), metadata{})
	if err != nil {
		t.Fatalf("valid relationship reference rejected: %v", err)
	}
	if len(table.Checks) != 1 || table.Checks[0].To != "customers.customer_id" {
		t.Fatalf("lowered relationship check = %#v", table.Checks)
	}
	dotted := strings.Replace(valid, "customers.customer_id", "customers.v2.customer_id", 1)
	if dottedTable, _, err := decodeModelResource("model-dotted.yaml", []byte(dotted), metadata{}); err != nil || len(dottedTable.Checks) != 1 || dottedTable.Checks[0].To != "customers.v2.customer_id" {
		t.Fatalf("dotted relationship reference table=%#v err=%v", dottedTable, err)
	}
	for _, reference := range []string{"customers", "customers..customer_id", "customers;DROP.customer_id"} {
		t.Run(reference, func(t *testing.T) {
			invalid := strings.Replace(valid, "customers.customer_id", reference, 1)
			if _, _, err := decodeModelResource("model.yaml", []byte(invalid), metadata{}); err == nil {
				t.Fatalf("relationship reference %q was accepted", reference)
			}
		})
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
