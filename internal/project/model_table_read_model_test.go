package project

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestModelTableAssetPayloadProjectsCompiledDefinition(t *testing.T) {
	table := semanticmodel.Table{
		Source:             "olist.geolocation",
		Sources:            []string{"olist.geolocation"},
		SQL:                "SELECT * FROM source.\"olist.geolocation\"",
		Transform:          semanticmodel.Transform{SQL: "SELECT * FROM source.\"olist.geolocation\""},
		Columns:            map[string]semanticmodel.ModelColumn{"zip_prefix": {Type: "string", SourceField: "geolocation_zip_code_prefix"}},
		PrimaryKey:         "zip_prefix",
		Grain:              "zip_prefix",
		Dimensions:         map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix", Type: "string"}},
		Schema:             semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{{Name: "zip_prefix", Ordinal: 0, PhysicalType: "VARCHAR"}}},
		SourceDependencies: []string{"olist.geolocation"},
		ModelDependencies:  []string{"model:upstream"},
	}

	payload := ModelTableAssetPayload(table)
	if payload["Source"] != "olist.geolocation" || payload["PrimaryKey"] != "zip_prefix" || payload["Grain"] != "zip_prefix" {
		t.Fatalf("payload metadata = %#v", payload)
	}
	if payload["SQL"] != table.SQL {
		t.Fatalf("payload SQL = %#v, want %q", payload["SQL"], table.SQL)
	}
	transform, ok := payload["Transform"].(map[string]any)
	if !ok || transform["SQL"] != table.Transform.SQL {
		t.Fatalf("payload transform = %#v, want SQL %q", payload["Transform"], table.Transform.SQL)
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

func TestModelTableAssetPayloadDoesNotAliasCompiledMaps(t *testing.T) {
	table := semanticmodel.Table{
		SourceReads: map[string][]string{"olist.geolocation": {"zip_prefix"}},
		Columns:     map[string]semanticmodel.ModelColumn{"zip_prefix": {Type: "string"}},
		Dimensions:  map[string]semanticmodel.MetricDimension{"zip_prefix": {Label: "ZIP prefix"}},
	}
	payload := ModelTableAssetPayload(table)
	if payload == nil {
		t.Fatal("payload is nil")
	}
	table.SourceReads["olist.geolocation"][0] = "changed"
	table.Columns["zip_prefix"] = semanticmodel.ModelColumn{Type: "integer"}
	table.Dimensions["zip_prefix"] = semanticmodel.MetricDimension{Label: "changed"}
	if got := payload["SourceReads"].(map[string]any)["olist.geolocation"].([]any)[0]; got != "zip_prefix" {
		t.Fatalf("payload source reads changed with source table: %#v", got)
	}
	if got := payload["Columns"].(map[string]any)["zip_prefix"].(map[string]any)["Type"]; got != "string" {
		t.Fatalf("payload columns changed with source table: %#v", got)
	}
	if got := payload["Dimensions"].(map[string]any)["zip_prefix"].(map[string]any)["Label"]; got != "ZIP prefix" {
		t.Fatalf("payload dimensions changed with source table: %#v", got)
	}
}
