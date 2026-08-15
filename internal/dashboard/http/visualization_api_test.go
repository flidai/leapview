package http

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
)

func TestDashboardVisualizationDescriptionContainsOnlyCompiledContract(t *testing.T) {
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{
		ID: "sales", SemanticModel: "sales",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{"revenue": {Type: "line", Title: "Revenue", Query: dashboardauthoring.VisualQuery{Table: "orders", Dimensions: []dashboardauthoring.FieldRef{{Field: "orders.month"}}, Measures: []dashboardauthoring.FieldRef{{Field: "orders.revenue"}}}}}),
	})
	if err != nil {
		t.Fatalf("compile definitions: %v", err)
	}
	definition := definitions["revenue"]
	payload, err := json.Marshal(dashboardVisualizationDefinitionDTO(definition, dashboard.PageVisual{ID: "revenue-card"}))
	if err != nil {
		t.Fatalf("marshal description: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode description: %v", err)
	}
	for _, required := range []string{"id", "rendererID", "specRevision", "spec"} {
		if _, ok := decoded[required]; !ok {
			t.Fatalf("description missing %q: %s", required, payload)
		}
	}
	for _, legacy := range []string{"shape", "renderer", "options", "extensions", "query", "columns"} {
		if _, ok := decoded[legacy]; ok {
			t.Fatalf("description retained legacy field %q: %s", legacy, payload)
		}
	}
}

func TestDashboardGridJSONUsesVisualizationEnvelope(t *testing.T) {
	table := dashboard.Table{
		Title: "Orders", Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order ID"}},
		Cardinality: dashboard.ExactCardinality(1), AvailableRows: 1,
		Blocks: map[string]dashboard.TableBlock{"a": {Rows: []map[string]any{{"order_id": "A-1"}}}},
	}
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&dashboardauthoring.Dashboard{ID: "sales", SemanticModel: "sales", Visuals: dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{"orders": {Title: "Orders", Columns: table.Columns, Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"order_id"}}}})})
	if err != nil {
		t.Fatalf("compile table definition: %v", err)
	}
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definitions["orders"], table, 7, 3)
	if err != nil {
		t.Fatalf("build table envelope: %v", err)
	}
	if envelope.RendererID != visualizationdefinition.RendererTanStack || envelope.DataRevision != 7 {
		t.Fatalf("envelope = %#v", envelope)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	for _, legacy := range []string{"rows", "columns", "queryId", "servingSnapshot"} {
		if _, ok := decoded[legacy]; ok {
			t.Fatalf("grid response retained legacy top-level field %q: %s", legacy, encoded)
		}
	}
}
