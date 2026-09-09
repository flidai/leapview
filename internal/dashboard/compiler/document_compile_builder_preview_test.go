package compiler

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCompileDocumentBuilderPreviewIsolatesInvalidVisual(t *testing.T) {
	dimension, metric := "state", "revenue"
	query := document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
		Dimensions: []document.DashboardDimensionSelection{{String: &dimension}},
		Metrics:    []document.DashboardMetricSelection{{String: &metric}},
	}}
	good := document.DashboardVisual{
		Type: document.DashboardVisualTypeBar, Query: query,
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}},
	}
	bad := document.DashboardVisual{
		Type: document.DashboardVisualTypeHeatmap, Query: query,
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"}, Type: "cartesian"}},
	}
	component := func(id, visual string, column int32) document.DashboardPageComponent {
		return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
			DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: document.DashboardPlacement{Column: column, Row: 1, ColumnSpan: 6, RowSpan: 4}},
			Type:                       "visual", Visual: visual,
		}}
	}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"},
		Spec: document.DashboardSpec{
			SemanticModel: "sales", Filters: []document.DashboardFilter{},
			Visuals: map[string]document.DashboardVisual{"good": good, "bad": bad},
			Pages:   []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{component("good-component", "good", 1), component("bad-component", "bad", 7)}}},
		},
	}
	models := map[string]*semanticmodel.Model{"sales": dashboardQueryTestModel()}
	result, err := CompileDocumentBuilderPreview(doc, models)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Definition.Visualizations["good"]; !ok {
		t.Fatalf("valid visual missing: visuals=%#v errors=%#v", result.Definition.Visualizations, result.VisualErrors)
	}
	if _, ok := result.Definition.Visualizations["bad"]; ok {
		t.Fatalf("invalid visual was compiled: %#v", result.Definition.Visualizations["bad"])
	}
	if !strings.Contains(result.VisualErrors["bad"], `visual "bad"`) {
		t.Fatalf("bad visual error = %q", result.VisualErrors["bad"])
	}
	if len(result.Definition.Pages) != 1 || len(result.Definition.Pages[0].Visuals) != 1 || result.Definition.Pages[0].Visuals[0].Visual != "good" {
		t.Fatalf("preview pages = %#v", result.Definition.Pages)
	}
	if _, err := CompileDocument(doc, models); err == nil {
		t.Fatal("strict compilation unexpectedly accepted invalid visual")
	}
}

func TestCompileDocumentBuilderPreviewRejectsIncompatibleReportFilterTargets(t *testing.T) {
	model := canonicalFilterTestModel()
	model.Datasets["customers"] = semanticmodel.SemanticDatasetSpec{Model: "customers"}
	model.Tables["customers"] = semanticmodel.Table{ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"name": {Field: "customers.name", Type: "string", Datatype: semanticmodel.DataTypeString}}}
	model.Metrics["customer_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.name"}}
	component := func(id, visual string, column int32) document.DashboardPageComponent {
		return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
			DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: document.DashboardPlacement{Column: column, Row: 1, ColumnSpan: 6, RowSpan: 4}},
			Type:                       "visual", Visual: visual,
		}}
	}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:sales", Name: "sales"},
		Spec: document.DashboardSpec{
			SemanticModel: "sales",
			Filters:       []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
			Visuals:       map[string]document.DashboardVisual{"orders": canonicalVisual("order_count"), "customers": canonicalVisual("customer_count")},
			Pages:         []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{component("orders-card", "orders", 1), component("customers-card", "customers", 7)}}},
		},
	}

	if _, err := CompileDocumentBuilderPreview(doc, map[string]*semanticmodel.Model{"sales": model}); err == nil || !strings.Contains(err.Error(), "narrow targets") {
		t.Fatalf("builder preview accepted incompatible report filter targets: %v", err)
	}
}
