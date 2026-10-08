package http

import (
	"encoding/json"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCopiedChatVisualRetainsCascadingFilters(t *testing.T) {
	for _, control := range []string{"singleSelect", "multiSelect"} {
		t.Run(control, func(t *testing.T) {
			var source chatDraftVisual
			body := `{"semanticModelId":"sales","visual":{"type":"kpi","query":{"type":"aggregate","dimensions":[],"metrics":["order_count"]},"presentation":{"type":"kpi"}},"filters":[
				{"id":"status","label":"Status","dimension":"status","control":{"type":"` + control + `","options":{"type":"distinct","dataset":"orders","dependsOn":["segment"]}}},
				{"id":"segment","label":"Segment","dimension":"segment","control":{"type":"singleSelect"}}]}`
			if err := json.Unmarshal([]byte(body), &source); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(source)
			model := &semanticmodel.Model{
				Name: "sales", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
				Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{
					"segment": {Field: "orders.segment", Type: "string", Datatype: semanticmodel.DataTypeString},
					"status":  {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString},
				}}},
				Dimensions: map[string]semanticmodel.SemanticDimension{
					"segment": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.segment"}}},
					"status":  {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
				},
				Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}}},
			}
			compile := func(input chatDraftVisual, id string) {
				t.Helper()
				doc := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard"}, Spec: document.DashboardSpec{
					SemanticModel: input.SemanticModelID, Filters: input.Filters, Visuals: map[string]document.DashboardVisual{id: input.Visual},
					Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{
						DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 2}}, Type: "visual", Visual: id,
					}}}}},
				}}
				if _, err := compiler.CompileCanonicalDashboardFilters(doc, model); err != nil {
					t.Fatalf("%s filter compilation failed: %v", id, err)
				}
			}
			compile(source, "original")
			saved, err := source.forVisual("saved")
			if err != nil {
				t.Fatal(err)
			}
			compile(saved, "saved")
			imported, err := saved.forVisual("copy")
			if err != nil {
				t.Fatal(err)
			}
			compile(imported, "copy")
			after, _ := json.Marshal(source)
			if string(before) != string(after) {
				t.Fatal("copying mutated the original filter definition")
			}
		})
	}
}
