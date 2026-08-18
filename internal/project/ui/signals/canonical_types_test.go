package signals

import (
	"encoding/json"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardsignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalSignalFixture(t *testing.T) (dashboarddefinition.Definition, *semanticmodel.Model, map[string]visualizationdefinition.Definition, []dashboard.Page) {
	t.Helper()
	fields := []visualizationir.VisualizationField{{ID: "status", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Status"}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Orders", Accessibility: visualizationir.VisualizationAccessibility{Title: "Orders", Description: "Orders"}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100}}, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkBar, X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "status"}, Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	visual, err := visualizationdefinition.New("active_chart", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "model", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "status", Alias: "status"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{Name: "model", Tables: map[string]semanticmodel.Table{}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count"}}}
	pages := []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "chart", Kind: "visual", Visual: "active_chart"}}}}
	compiled := dashboarddefinition.Definition{ID: "dashboard:sales", Title: "Sales", SemanticModel: "model", Pages: pages, Visualizations: map[string]visualizationdefinition.Definition{"active_chart": visual}}
	return compiled, model, map[string]visualizationdefinition.Definition{"active_chart": visual}, pages
}

func TestCanonicalVisualizationSignalKeepsDataStateOpaque(t *testing.T) {
	compiled, model, definitions, pages := canonicalSignalFixture(t)
	envelope := dashboardsignals.DashboardInitialEnvelope("client", "stream-instance", dashboard.Catalog{}, compiled, model, definitions, pages, pages[0], dashboard.Filters{})
	encoded, err := json.Marshal(envelope.Visuals["active_chart"])
	if err != nil {
		t.Fatal(err)
	}
	var signal map[string]any
	if err := json.Unmarshal(encoded, &signal); err != nil {
		t.Fatal(err)
	}
	transport, ok := signal["dataState"].(map[string]any)
	if !ok || transport["schemaVersion"] != float64(1) || transport["encoding"] != "json" || transport["kind"] != "inline" {
		t.Fatalf("canonical data-state transport = %#v", signal)
	}
	if _, ok := transport["payload"].(string); !ok {
		t.Fatalf("data-state payload is not opaque: %#v", transport)
	}
	if _, ok := signal["dataStateJson"]; ok {
		t.Fatal("legacy dataStateJson field emitted")
	}
}

func TestCanonicalDashboardEnvelopeValidatesPageScopedVisuals(t *testing.T) {
	compiled, model, definitions, pages := canonicalSignalFixture(t)
	envelope := dashboardsignals.DashboardInitialEnvelope("client", "stream-instance", dashboard.Catalog{}, compiled, model, definitions, pages, pages[0], dashboard.Filters{})
	if err := dashboardsignals.ValidateDashboardEnvelope(envelope); err != nil {
		t.Fatal(err)
	}
	delete(envelope.Visuals, "active_chart")
	if err := dashboardsignals.ValidateDashboardEnvelope(envelope); err == nil || !strings.Contains(err.Error(), `missing visual "active_chart"`) {
		t.Fatalf("missing visual validation = %v", err)
	}
}
