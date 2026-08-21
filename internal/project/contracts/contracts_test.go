package contracts_test

import (
	"encoding/json"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
	configschema "github.com/flidai/leapview/internal/project/schema"
)

func TestGeneratedResourceBoundaryDecodesTaggedVariants(t *testing.T) {
	connectionYAML := []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:olist
  name: olist
spec:
  type: managed
  defaults:
    csv:
      header: true
`)
	var connection contracts.Connection
	if err := configschema.DecodeResource(configschema.KindConnection, "connection.yaml", connectionYAML, &connection); err != nil {
		t.Fatalf("decode Connection: %v", err)
	}
	if connection.Kind != "Connection" {
		t.Fatalf("kind = %q, want Connection", connection.Kind)
	}
	if _, ok := connection.Spec.Value.(*contracts.ManagedConnection); !ok {
		t.Fatalf("connection variant = %T, want *ManagedConnection", connection.Spec.Value)
	}

	sourceYAML := []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:olist.orders
  name: olist.orders
spec:
  connection: olist
  location:
    type: path
    path: olist_orders_dataset.csv
    format: csv
    options:
      header: true
  schema:
    mode: inferred
  freshness:
    basis: field
    field: updated_at
    warningAfter:
      amount: 1
      unit: hour
    errorAfter:
      amount: 2
      unit: hour
`)
	var source contracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, "source.yaml", sourceYAML, &source); err != nil {
		t.Fatalf("decode Source: %v", err)
	}
	pathLocation, ok := source.Spec.Location.Value.(*contracts.SourceLocationPathVariant)
	if !ok {
		t.Fatalf("source location variant = %T, want path variant", source.Spec.Location.Value)
	}
	csvLocation, ok := pathLocation.PathSourceLocation.Value.(*contracts.CSVPathSourceLocation)
	if !ok {
		t.Fatalf("path format variant = %T, want CSVPathSourceLocation", pathLocation.PathSourceLocation.Value)
	}
	if csvLocation.Options == nil || csvLocation.Options.Header == nil || !*csvLocation.Options.Header {
		t.Fatalf("source path options = %#v, want csv header=true", csvLocation.Options)
	}
	encodedSource, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("marshal Source with generated path validation: %v", err)
	}
	var roundTrip contracts.Source
	if err := json.Unmarshal(encodedSource, &roundTrip); err != nil {
		t.Fatalf("round-trip Source with generated path validation: %v", err)
	}

	relationYAML := []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:commerce.orders
  name: commerce.orders
spec:
  connection: warehouse
  location:
    type: relation
    catalog: analytics
    schema: commerce
    name: orders
`)
	var relation contracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, "relation-source.yaml", relationYAML, &relation); err != nil {
		t.Fatalf("decode relation Source: %v", err)
	}
	if _, ok := relation.Spec.Location.Value.(*contracts.SourceLocationRelationVariant); !ok {
		t.Fatalf("relation source location variant = %T, want relation variant", relation.Spec.Location.Value)
	}

	modelYAML := []byte(`apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders
  name: orders
aiContext:
  instructions: Order identifiers are stable customer-facing references.
spec:
  definition:
    type: sql
    sql: |
      SELECT order_id, customer_id
      FROM source."olist.orders"
  entities:
    order:
      type: primary
      fields: [order_id]
  grain:
    entity: order
  fields:
    order_id:
      datatype: String
    customer_id:
      datatype: String
`)
	var model contracts.Model
	if err := configschema.DecodeResource(configschema.KindModel, "model.yaml", modelYAML, &model); err != nil {
		t.Fatalf("decode Model: %v", err)
	}
	if _, ok := model.Spec.Definition.Value.(*contracts.SQLModelDefinition); !ok {
		t.Fatalf("model definition variant = %T, want *SQLModelDefinition", model.Spec.Definition.Value)
	}
	if model.AiContext == nil || model.AiContext.Instructions == nil {
		t.Fatalf("model aiContext = %#v, want preserved top-level instructions", model.AiContext)
	}

	directYAML := []byte(`apiVersion: leapview.dev/v1
kind: Model
metadata:
  id: model:orders-direct
  name: orders-direct
spec:
  definition:
    type: direct
    source: olist.orders
  entities:
    order:
      type: primary
      fields: [order_id]
  grain:
    entity: order
  fields:
    order_id:
      datatype: String
`)
	var direct contracts.Model
	if err := configschema.DecodeResource(configschema.KindModel, "direct-model.yaml", directYAML, &direct); err != nil {
		t.Fatalf("decode direct Model: %v", err)
	}
	if _, ok := direct.Spec.Definition.Value.(*contracts.DirectModelDefinition); !ok {
		t.Fatalf("direct model definition variant = %T, want *DirectModelDefinition", direct.Spec.Definition.Value)
	}
}

func TestGeneratedResourceBoundaryRejectsTargetOwnedFields(t *testing.T) {
	content := []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:olist
  name: olist
spec:
  type: s3
  host: forbidden.example
`)
	var connection contracts.Connection
	if err := configschema.DecodeResource(configschema.KindConnection, "connection.yaml", content, &connection); err == nil {
		t.Fatal("target-owned host field was accepted")
	}
}

func TestGeneratedResourceBoundaryRequiresMatchingKind(t *testing.T) {
	content := []byte(`apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:olist
  name: olist
spec:
  type: managed
`)
	var connection contracts.Connection
	if err := configschema.DecodeResource(configschema.KindSource, "connection.yaml", content, &connection); err == nil {
		t.Fatal("connection destination accepted source kind")
	}
}

func TestGeneratedResourceBoundaryRejectsWrongTaggedOptions(t *testing.T) {
	content := []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:orders
  name: orders
spec:
  connection: files
  location:
    type: path
    path: orders.csv
    format: csv
    options:
      unionByName: true
`)
	var source contracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, "source.yaml", content, &source); err == nil {
		t.Fatal("csv location accepted parquet-only option")
	}
}

func TestGeneratedGoPathVariantRejectsWrongTaggedOptions(t *testing.T) {
	content := []byte(`{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders"},"spec":{"connection":"files","location":{"type":"path","path":"orders.csv","format":"csv","options":{"unionByName":true}}}}`)
	var source contracts.Source
	if err := json.Unmarshal(content, &source); err == nil {
		t.Fatal("generated Go Source DTO accepted parquet-only CSV option")
	}
}

func TestGeneratedResourceBoundaryRejectsInferredSchemaFields(t *testing.T) {
	content := []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:orders
  name: orders
spec:
  connection: files
  location:
    type: path
    path: orders.csv
    format: csv
  schema:
    mode: inferred
    fields:
      id:
        datatype: Integer
`)
	var source contracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, "source.yaml", content, &source); err == nil {
		t.Fatal("inferred schema accepted fields")
	}
}

func TestGeneratedResourceBoundaryRequiresFreshnessThreshold(t *testing.T) {
	content := []byte(`apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:orders
  name: orders
spec:
  connection: files
  location:
    type: path
    path: orders.csv
    format: csv
  freshness:
    basis: field
    field: updated_at
`)
	var source contracts.Source
	if err := configschema.DecodeResource(configschema.KindSource, "source.yaml", content, &source); err == nil {
		t.Fatal("freshness accepted without warningAfter or errorAfter")
	}
}

func TestGeneratedConnectorRegistryIsComplete(t *testing.T) {
	want := []string{"managed", "s3", "r2", "gcs", "http", "azure_blob", "postgres", "mysql", "sqlite", "ducklake", "quack"}
	if len(contracts.ConnectorRegistry) != len(want) {
		t.Fatalf("connector registry size = %d, want %d", len(contracts.ConnectorRegistry), len(want))
	}
	for _, key := range want {
		profile, ok := contracts.LookupConnector(key)
		if !ok || profile.AdapterKey != key || profile.Key != key {
			t.Fatalf("connector %q profile = %#v, ok=%v", key, profile, ok)
		}
	}
	for _, key := range []string{"managed", "s3", "r2", "gcs", "http", "azure_blob"} {
		profile, _ := contracts.LookupConnector(key)
		if !profile.AllowPublicAccess {
			t.Fatalf("connector %q profile does not expose generated public-access capability", key)
		}
	}
	for _, key := range []string{"postgres", "mysql", "sqlite", "ducklake", "quack"} {
		profile, _ := contracts.LookupConnector(key)
		if profile.AllowPublicAccess {
			t.Fatalf("connector %q profile unexpectedly exposes public-access capability", key)
		}
	}
}

func TestRequiredExtensionNamesDeriveConnectorAndFormatProfiles(t *testing.T) {
	want := []string{"avro", "azure", "delta", "ducklake", "excel", "httpfs", "iceberg", "lance", "mysql", "postgres", "quack", "spatial", "sqlite", "vortex"}
	got := contracts.RequiredExtensionNames()
	if len(got) != len(want) {
		t.Fatalf("required extension set = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("required extension set = %#v, want %#v", got, want)
		}
	}
}
