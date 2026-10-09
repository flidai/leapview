package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	"reflect"
	"testing"
)

func TestCompatibleDashboardFilterTargetsUseEveryQueryDataset(t *testing.T) {
	model := &semanticmodel.Model{Dimensions: map[string]semanticmodel.SemanticDimension{
		"country": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{"sales": {Field: "sales.country"}}},
	}, Metrics: map[string]semanticmodel.Metric{"revenue": {Dataset: "sales"}, "cash": {Dataset: "cash"}}}
	query := func(metrics ...string) document.DashboardVisual {
		selections := []document.DashboardMetricSelection{}
		for _, metric := range metrics {
			id := metric
			selections = append(selections, document.DashboardMetricSelection{String: &id})
		}
		return document.DashboardVisual{Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Metrics: selections}}}
	}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{
		Visuals: map[string]document.DashboardVisual{"revenue": query("revenue"), "cash": query("cash"), "mixed": query("revenue", "cash")},
		Pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{
			{Value: &document.VisualDashboardPageComponent{Visual: "revenue"}},
			{Value: &document.VisualDashboardPageComponent{Visual: "cash"}},
			{Value: &document.VisualDashboardPageComponent{Visual: "mixed"}},
		}}},
	}}
	got, err := CompatibleDashboardFilterTargets(doc, "country", model)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"revenue"}) {
		t.Fatalf("targets = %#v, want only revenue", got)
	}
	delete(model.Dimensions["country"].Bindings, "sales")
	if _, err := CompatibleDashboardFilterTargets(doc, "country", model); err == nil {
		t.Fatal("incompatible field should not create a broken filter")
	}
	doc.Spec.Pages = nil
	if got, err := CompatibleDashboardFilterTargets(doc, "country", model); err != nil || got != nil {
		t.Fatalf("empty dashboard targets=%#v error=%v", got, err)
	}
}

func TestCompatibleDashboardFilterTargetsAllowUnfinishedDraft(t *testing.T) {
	model := &semanticmodel.Model{Dimensions: map[string]semanticmodel.SemanticDimension{"country": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{"sales": {Field: "sales.country"}}}}}
	doc := document.DashboardDocument{Spec: document.DashboardSpec{
		Visuals: map[string]document.DashboardVisual{"new": {Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate"}}}},
		Pages:   []document.DashboardPage{{Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{Visual: "new"}}}}},
	}}
	if targets, err := CompatibleDashboardFilterTargets(doc, "country", model); err != nil || targets != nil {
		t.Fatalf("unfinished draft targets=%v error=%v", targets, err)
	}
}

func TestCompatibleFilterTargetsRejectUnsupportedValuesEvenBeforeAddingVisuals(t *testing.T) {
	for _, datatype := range []semanticmodel.LogicalDataType{semanticmodel.DataTypeTime, semanticmodel.DataTypeOpaque} {
		t.Run(string(datatype), func(t *testing.T) {
			model := &semanticmodel.Model{Dimensions: map[string]semanticmodel.SemanticDimension{"unsupported": {Type: "timestamp", Datatype: datatype}}}
			doc := document.DashboardDocument{}
			if _, err := CompatibleDashboardFilterTargets(doc, "unsupported", model); err == nil {
				t.Fatal("Add filter advertised an unsupported value type")
			}
			if _, err := CanonicalCompatibleFilterVisualTargets(doc, model, "unsupported"); err == nil {
				t.Fatal("Add slicer advertised an unsupported value type")
			}
		})
	}
}
