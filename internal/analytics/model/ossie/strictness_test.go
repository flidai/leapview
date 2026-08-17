package ossie

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func strictnessProjectModels() map[string]semanticmodel.Table {
	return map[string]semanticmodel.Table{
		"orders": {
			GrainEntity: "order",
			Entities: map[string]semanticmodel.ModelEntitySpec{
				"order": {Type: "primary", Fields: []string{"order_id"}},
			},
			Columns: map[string]semanticmodel.ModelColumn{
				"order_id":   {SourceField: "order_id", Datatype: semanticmodel.DataTypeString},
				"event_date": {SourceField: "event_date", Datatype: semanticmodel.DataTypeDate},
			},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id":   {Type: "string", Datatype: semanticmodel.DataTypeString},
				"event_date": {Type: "date", Datatype: semanticmodel.DataTypeDate},
			},
		},
	}
}

func strictnessDocument(fieldBlock string) []byte {
	return []byte("version: 0.2.0.dev0\nsemantic_model:\n  - name: sales\n    datasets:\n      - name: orders\n        source: orders\n" + fieldBlock)
}

func TestImportRejectsOssieMetadataThatWouldRequireModelSynthesis(t *testing.T) {
	doc := strictnessDocument("        primary_key: [missing_id]\n")
	models := strictnessProjectModels()
	models["orders"] = semanticmodel.Table{}
	if _, err := Import(doc, models); err == nil || !strings.Contains(err.Error(), "requires an existing Model grain") {
		t.Fatalf("missing model grain error = %v", err)
	}

	doc = strictnessDocument("        fields:\n          - name: missing_id\n            expression: {dialects: [{dialect: ANSI_SQL, expression: missing_id}]}\n            datatype: String\n")
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "not declared on the existing Model") {
		t.Fatalf("missing model field error = %v", err)
	}
}

func TestImportRejectsOssieLogicalDatatypeMismatch(t *testing.T) {
	doc := strictnessDocument("        fields:\n          - name: event_date\n            expression: {dialects: [{dialect: ANSI_SQL, expression: event_date}]}\n            datatype: DateTime\n")
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "disagrees with existing Model datatype") {
		t.Fatalf("logical datatype mismatch error = %v", err)
	}
}

func TestImportRejectsUnsupportedOssieDialectAndExtensions(t *testing.T) {
	doc := strictnessDocument("        fields:\n          - name: order_id\n            expression: {dialects: [{dialect: SNOWFLAKE, expression: order_id}]}\n            datatype: String\n")
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "unsupported expression dialect") {
		t.Fatalf("unsupported dialect error = %v", err)
	}

	doc = strictnessDocument("        custom_extensions:\n          - vendor_name: OTHER\n            data: '{} '\n")
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "contains unsupported custom extensions") {
		t.Fatalf("unsupported extension error = %v", err)
	}

	doc = []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: rows
        datatype: Integer
        expression: {dialects: [{dialect: ANSI_SQL, expression: COUNT(orders.order_id)}]}
`)
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "datatype") {
		t.Fatalf("unsupported metric datatype error = %v", err)
	}

	doc = []byte(`version: 0.2.0.dev0
semantic_model:
  - name: sales
    datasets: [{name: orders, source: orders}]
    metrics:
      - name: rows
        expression: {dialects: [{dialect: SNOWFLAKE, expression: COUNT(orders.order_id)}]}
`)
	if _, err := Import(doc, strictnessProjectModels()); err == nil || !strings.Contains(err.Error(), "unsupported executable dialect") {
		t.Fatalf("unsupported metric dialect error = %v", err)
	}
}

func TestExportValidatesNativeGraphBeforeSerialization(t *testing.T) {
	value := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": strictnessProjectModels()["orders"],
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"},
		},
		StructuredRelationships: map[string]semanticmodel.RelationshipSpec{
			"bad": {
				From: semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Entity: "missing"},
				To:   semanticmodel.RelationshipEndpointSpec{Dataset: "orders", Entity: "order"},
			},
		},
	}
	if _, err := Export(value); err == nil || !strings.Contains(err.Error(), "native semantic model relationships") {
		t.Fatalf("native validation error = %v", err)
	}
}

func TestExportRejectsLegacyRelationshipsWithoutStructuredRelationships(t *testing.T) {
	model := &semanticmodel.Model{
		Name:          "sales",
		Relationships: []semanticmodel.Relationship{{ID: "legacy", FromDataset: "orders", ToDataset: "customers", FromFields: []string{"customer_id"}, ToFields: []string{"customer_id"}}},
	}
	if _, err := Export(model); err == nil || !strings.Contains(err.Error(), "must use StructuredRelationships") {
		t.Fatalf("legacy relationship export error = %v", err)
	}
}

func TestImportDoesNotMutateProjectModelTables(t *testing.T) {
	models := strictnessProjectModels()
	before := models["orders"].Dimensions["order_id"]
	if _, err := Import(strictnessDocument(""), models); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	after := models["orders"].Dimensions["order_id"]
	if after != before {
		t.Fatalf("Import mutated project model dimension: before=%#v after=%#v", before, after)
	}
}
