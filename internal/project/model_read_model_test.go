package project

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func TestModelAssetPayloadProjectsCompiledDefinition(t *testing.T) {
	model := semanticmodel.Table{
		Execution:          semanticmodel.ExecutionDefinition{Source: "olist.geolocation", SQL: "SELECT * FROM source.\"olist.geolocation\""},
		Columns:            map[string]semanticmodel.ModelColumn{"zip_prefix": {Type: "string", SourceField: "geolocation_zip_code_prefix"}},
		Entities:           map[string]semanticmodel.EntityDefinition{"location": {Type: "primary", Fields: []string{"zip_prefix", "city"}}},
		GrainEntity:        "location",
		Dimensions:         map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix", Type: "string"}},
		Schema:             semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "zip_prefix", Ordinal: 0, PhysicalType: "VARCHAR"}}},
		SourceDependencies: []string{"olist.geolocation"},
		ModelDependencies:  []string{"model:upstream"},
	}

	payload := ModelAssetPayload(model)
	definition, _ := payload["Definition"].(map[string]any)
	if definition["Source"] != "olist.geolocation" || payload["GrainEntity"] != "location" {
		t.Fatalf("payload metadata = %#v", payload)
	}
	if _, ok := payload["PrimaryKey"]; ok {
		t.Fatalf("payload retains removed PrimaryKey contract: %#v", payload)
	}
	entities, ok := payload["Entities"].(map[string]any)
	if !ok || entities["location"] == nil {
		t.Fatalf("payload entities = %#v", payload["Entities"])
	}
	entity, ok := entities["location"].(map[string]any)
	if !ok || entity["Fields"] == nil {
		t.Fatalf("payload composite entity = %#v", entities["location"])
	}
	if _, ok := payload["SQL"]; ok {
		t.Fatalf("payload retains removed top-level SQL alias: %#v", payload)
	}
	transform, ok := payload["Definition"].(map[string]any)
	if !ok || transform["SQL"] != model.Execution.SQL {
		t.Fatalf("payload definition = %#v, want SQL %q", payload["Definition"], model.Execution.SQL)
	}
	if got, ok := payload["SourceDependencies"].([]any); !ok || len(got) != 1 || got[0] != "olist.geolocation" {
		t.Fatalf("payload source dependencies = %#v", payload["SourceDependencies"])
	}
	if got, ok := payload["ModelDependencies"].([]any); !ok || len(got) != 1 || got[0] != "model:upstream" {
		t.Fatalf("payload model dependencies = %#v", payload["ModelDependencies"])
	}
	if fields, ok := payload["Dimensions"].(map[string]any); !ok || fields["zip_prefix"] == nil {
		t.Fatalf("payload dimensions = %#v", payload["Dimensions"])
	}
	if columns, ok := payload["Columns"].(map[string]any); !ok || columns["zip_prefix"] == nil {
		t.Fatalf("payload columns = %#v", payload["Columns"])
	}
	schema, ok := payload["Schema"].(map[string]any)
	if !ok {
		t.Fatalf("payload schema = %#v", payload["Schema"])
	}
	if columns, ok := schema["columns"].([]any); !ok || len(columns) != 1 {
		t.Fatalf("payload schema columns = %#v", schema["columns"])
	}
}

func TestModelAssetPayloadUsesAuthoredDefinitionOverTargetExecution(t *testing.T) {
	const authoredSQL = `WITH normalized AS (SELECT * FROM source."olist.geolocation") SELECT * FROM normalized`
	const authoredSource = "apiVersion: leapview.dev/v1\nkind: Model\nmetadata: {id: model:zip_geolocations, name: zip_geolocations}\n"
	model := semanticmodel.Table{
		// A target-bound runtime may retain only a direct source execution for
		// materialization. The detail projection must still show authored SQL.
		Execution: semanticmodel.ExecutionDefinition{Source: "source:olist.geolocation"},
		Entities:  map[string]semanticmodel.EntityDefinition{"zip": {Type: "primary", Fields: []string{"zip_prefix"}}},
	}
	authored := projectmanifest.AuthoredModelDefinition{Type: "sql", SQL: authoredSQL}
	payload := ModelAssetPayloadWithAuthoredSource(model, &authored, authoredSource)
	definition, ok := payload["Definition"].(map[string]any)
	if !ok || definition["SQL"] != authoredSQL || definition["Source"] != nil {
		t.Fatalf("payload definition = %#v, want authored SQL", payload["Definition"])
	}
	if payload["Configuration"] != authoredSource {
		t.Fatalf("payload configuration = %#v, want authored model source", payload["Configuration"])
	}
}

func TestModelAssetPayloadDoesNotAliasCompiledMaps(t *testing.T) {
	model := semanticmodel.Table{
		SourceDependencies: []string{"olist.geolocation"},
		Columns:            map[string]semanticmodel.ModelColumn{"zip_prefix": {Type: "string"}},
		Dimensions:         map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix"}},
	}
	payload := ModelAssetPayload(model)
	if payload == nil {
		t.Fatal("payload is nil")
	}
	model.SourceDependencies[0] = "changed"
	model.Columns["zip_prefix"] = semanticmodel.ModelColumn{Type: "integer"}
	model.Dimensions["zip_prefix"] = semanticmodel.MetricDimension{Label: "changed"}
	if got := payload["SourceDependencies"].([]any)[0]; got != "olist.geolocation" {
		t.Fatalf("payload source dependencies changed with source model: %#v", got)
	}
	if got := payload["Columns"].(map[string]any)["zip_prefix"].(map[string]any)["Type"]; got != "string" {
		t.Fatalf("payload columns changed with source model: %#v", got)
	}
	if got := payload["Dimensions"].(map[string]any)["zip_prefix"].(map[string]any)["Label"]; got != "ZIP prefix" {
		t.Fatalf("payload dimensions changed with source model: %#v", got)
	}
}
