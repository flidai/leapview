package application

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestAddSlicerTargetsOnlyCompatibleFinanceDashboardVisuals(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "finance",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"financial_performance": {Model: "financial_performance"},
			"cash_forecast":         {Model: "cash_forecast"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"scenario": {
				Type: "string", Datatype: semanticmodel.DataTypeString,
				Bindings: map[string]semanticmodel.DimensionBinding{"cash_forecast": {Field: "cash_forecast.scenario"}},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"net_revenue":  {Dataset: "financial_performance"},
			"current_cash": {Dataset: "cash_forecast"},
		},
		Tables: map[string]semanticmodel.Table{
			"financial_performance": {Dimensions: map[string]semanticmodel.MetricDimension{"net_revenue": {Datatype: semanticmodel.DataTypeDecimal}}},
			"cash_forecast":         {Dimensions: map[string]semanticmodel.MetricDimension{"scenario": {Datatype: semanticmodel.DataTypeString}, "current_cash": {Datatype: semanticmodel.DataTypeDecimal}}},
		},
	}
	netRevenue, currentCash := "net_revenue", "current_cash"
	visual := func(metric *string) document.DashboardVisual {
		return document.DashboardVisual{
			Type: document.DashboardVisualTypeBar,
			Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
				DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
				Metrics: []document.DashboardMetricSelection{{String: metric}},
			}},
		}
	}
	pageComponent := func(id, visualID string) document.DashboardPageComponent {
		return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
			DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 3}},
			Type:                       "visual", Visual: visualID,
		}}
	}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:finance", Name: "finance"},
		Spec: document.DashboardSpec{
			SemanticModel: "finance",
			Filters:       []document.DashboardFilter{},
			Visuals:       map[string]document.DashboardVisual{"revenue": visual(&netRevenue), "cash": visual(&currentCash)},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{
				pageComponent("overview-revenue-card", "revenue"), pageComponent("overview-cash-card", "cash"),
			}}},
		},
	}
	slicer := &authoring.AddSlicerPayload{PageID: "overview", Label: "Scenario", Dimension: "scenario", Dataset: "cash_forecast", ControlType: "multiSelect"}
	if err := setCompatibleSlicerTargets(doc, model, slicer); err != nil {
		t.Fatalf("derive compatible slicer targets: %v", err)
	}
	if len(slicer.Targets) != 1 || slicer.Targets[0] != "cash" {
		t.Fatalf("slicer targets = %v, want only cash visual", slicer.Targets)
	}

	filterTargets := append([]string(nil), slicer.Targets...)
	doc.Spec.Filters = append(doc.Spec.Filters, document.DashboardFilter{
		ID: "scenario", Label: "Scenario", Dimension: "scenario", Targets: &filterTargets,
		Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{
			Type: "multiSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "cash_forecast"}},
		}},
	})
	compiled, err := compiler.CompileCanonicalDashboardBuilderFilters(doc, model)
	if err != nil {
		t.Fatalf("compile explicitly targeted Scenario slicer: %v", err)
	}
	if got := compiled.Bindings["scenario"].Targets; len(got) != 1 || got[0] != "overview/overview-cash-card" {
		t.Fatalf("compiled Scenario targets = %v, want only compatible cash visual", got)
	}

	doc.Spec.Filters[0].Targets = nil
	if _, err := compiler.CompileCanonicalDashboardBuilderFilters(doc, model); err == nil || !strings.Contains(err.Error(), "overview/overview-revenue-card") {
		t.Fatalf("unscoped Scenario filter error = %v, want incompatible revenue target", err)
	}
}
