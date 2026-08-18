package compiler

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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
	if connection.Kind != "managed" || connection.ReaderDefaults["csv"]["header"] != true || len(connection.Auth) != 0 || connection.Host != "" {
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
	effective, err := ResolveEffectiveSourceOptions(source, connection)
	if err != nil {
		t.Fatalf("resolve effective options: %v", err)
	}
	if effective["header"] != false {
		t.Fatalf("source option did not override connection default: %#v", effective)
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
