package compiler

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestCompileDashboardFilterArchitectureResolvesBindingKeysAndComponentTargets(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"customers": {Model: "customers"},
			"orders":    {Model: "orders"},
		},
		Tables: map[string]semanticmodel.Table{
			"customers": {
				ModelName:   "customers",
				GrainEntity: "state",
				Entities: map[string]semanticmodel.ModelEntitySpec{
					"state": {Type: "primary", Fields: []string{"state"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"state": {Field: "customers.state", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"orders": {
				ModelName:   "orders",
				GrainEntity: "order_id",
				Entities: map[string]semanticmodel.ModelEntitySpec{
					"order_id": {Type: "primary", Fields: []string{"order_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Field: "orders.order_id", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"order_id"}, ToDataset: "customers", ToFields: []string{"state"}, Cardinality: "many_to_one"}},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"customer_state": {
				Type: "string", Datatype: semanticmodel.DataTypeString,
				Bindings: map[string]semanticmodel.DimensionBinding{
					"orders": {Field: "customers.state"},
				},
			},
		},
	}
	authored := &dashboardauthoring.Dashboard{
		ID: "sales", Title: "Sales", SemanticModel: "sales",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {
				Label: "State", Field: "customer_state",
				Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}},
				Options:    dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceDistinct, Limit: 50},
			},
		},
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"orders": {Type: "kpi", Query: dashboardauthoring.VisualQuery{Metrics: []dashboardauthoring.FieldRef{{Field: "order_count"}}}},
		}),
		Pages: []dashboard.Page{{
			ID: "overview", Title: "Overview",
			FilterBindings: map[string]dashboardfilter.Binding{
				"state": {
					Filter:       "state",
					Default:      dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered},
					URL:          dashboardfilter.URLPolicy{Param: "state", Encoding: dashboardfilter.URLEncodingTypedV1},
					TargetPolicy: dashboardfilter.TargetPolicy{Include: []string{"orders-card"}},
				},
			},
			Visuals: []dashboard.PageVisual{
				{ID: "state-slicer", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "state"}, Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 3, RowSpan: 2}},
				{ID: "orders-card", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 4, Row: 1, ColSpan: 3, RowSpan: 2}},
			},
		}},
	}

	normalized, err := ValidateAndNormalizeDashboard(authored, map[string]*semanticmodel.Model{"sales": model})
	if err != nil {
		t.Fatalf("ValidateAndNormalizeDashboard() error = %v", err)
	}
	visualizations, err := CompileVisualizationDefinitions(normalized, model)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := CompileDashboardDefinition(normalized, visualizations)
	if err != nil {
		t.Fatal(err)
	}

	binding := compiled.Pages[0].FilterBindings["state"]
	if binding.Key == "" || !strings.HasPrefix(binding.Key, "fb_") {
		t.Fatalf("compiled binding key = %q", binding.Key)
	}
	if binding.ValueKind != dashboardfilter.ValueString {
		t.Fatalf("compiled value kind = %q", binding.ValueKind)
	}
	if len(binding.Targets) != 1 || binding.Targets[0] != "overview/orders-card" {
		t.Fatalf("compiled targets = %#v", binding.Targets)
	}
}

func TestFilterValueKindPreservesIntegerLogicalDatatype(t *testing.T) {
	model := &semanticmodel.Model{
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{
				"delivery_days": {Field: "orders.delivery_days", Type: "number", Datatype: semanticmodel.DataTypeInteger},
			}},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"delivery_days": {
				Type: "number", Datatype: semanticmodel.DataTypeInteger,
				Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.delivery_days"}},
			},
		},
	}
	kind, err := filterValueKind(model, "delivery_days")
	if err != nil {
		t.Fatalf("filterValueKind() error = %v", err)
	}
	if kind != dashboardfilter.ValueInteger {
		t.Fatalf("filterValueKind() = %q, want %q", kind, dashboardfilter.ValueInteger)
	}
}

func TestValidateDashboardFilterArchitectureRejectsRouteVisibleURLCollision(t *testing.T) {
	authored := &dashboardauthoring.Dashboard{
		ID: "sales", Title: "Sales", SemanticModel: "sales",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state":    {Label: "State", Field: "orders.state", Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}}},
			"category": {Label: "Category", Field: "orders.category", Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}}},
		},
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"orders": {Type: "kpi", Query: dashboardauthoring.VisualQuery{Metrics: []dashboardauthoring.FieldRef{{Field: "missing"}}}},
		}),
		Pages: []dashboard.Page{{
			ID: "overview", Title: "Overview",
			FilterBindings: map[string]dashboardfilter.Binding{
				"state":    {Filter: "state", URL: dashboardfilter.URLPolicy{Param: "filter", Encoding: dashboardfilter.URLEncodingTypedV1}},
				"category": {Filter: "category", URL: dashboardfilter.URLPolicy{Param: "filter", Encoding: dashboardfilter.URLEncodingTypedV1}},
			},
		}},
	}

	err := authored.ValidateContract()
	if err == nil || !strings.Contains(err.Error(), "URL parameter") {
		t.Fatalf("ValidateContract() error = %v", err)
	}
}

func TestCompileDashboardFilterArchitecturePersistsDefaultSlicerPresentation(t *testing.T) {
	authored := &dashboardauthoring.Dashboard{
		ID: "sales", Title: "Sales", SemanticModel: "sales",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {
				Label: "State", Field: "orders.state",
				Predicates: []dashboardfilter.PredicatePolicy{{
					Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn},
				}},
			},
		},
		Pages: []dashboard.Page{{
			ID: "overview", Title: "Overview",
			FilterBindings: map[string]dashboardfilter.Binding{"state": {Filter: "state"}},
			Visuals: []dashboard.PageVisual{{
				ID: "state-slicer", Kind: "slicer",
				Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "state"},
			}},
		}},
	}
	authored.FilterDefinitions["state"] = dashboardfilter.Definition{
		Label: "State", Field: "orders.state", ValueKind: dashboardfilter.ValueString,
		Predicates: []dashboardfilter.PredicatePolicy{{
			Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn},
		}},
	}
	page := authored.Pages[0]
	page.FilterBindings["state"] = dashboardfilter.Binding{
		Filter: "state", ID: "state", Scope: dashboardfilter.ScopePage, PageID: "overview",
		ValueKind: dashboardfilter.ValueString,
	}
	if err := validateSlicerPresentations(authored, &page); err != nil {
		t.Fatal(err)
	}
	if got := page.Visuals[0].Presentation.Style; got != dashboardfilter.PresentationDropdown {
		t.Fatalf("default slicer presentation = %q, want dropdown", got)
	}
}

func TestCompileOptionDependenciesKeepsReportDependenciesFromEveryPage(t *testing.T) {
	authored := &dashboardauthoring.Dashboard{
		FilterBindings: map[string]dashboardfilter.Binding{
			"report": {
				ID: "report", Scope: dashboardfilter.ScopeReport,
				Targets: []string{"one/chart", "two/chart"},
			},
		},
		Pages: []dashboard.Page{
			{
				ID: "one",
				FilterBindings: map[string]dashboardfilter.Binding{
					"page_one": {
						ID: "page_one", Scope: dashboardfilter.ScopePage,
						Targets: []string{"one/chart"},
					},
				},
			},
			{
				ID: "two",
				FilterBindings: map[string]dashboardfilter.Binding{
					"page_two": {
						ID: "page_two", Scope: dashboardfilter.ScopePage,
						Targets: []string{"two/chart"},
					},
				},
			},
		},
	}
	compileOptionDependencies(authored)
	got := authored.FilterBindings["report"].OptionDependencies
	for _, want := range []dashboardfilter.BindingRef{
		{Scope: dashboardfilter.ScopePage, ID: "page_one"},
		{Scope: dashboardfilter.ScopePage, ID: "page_two"},
	} {
		if !bindingRefContains(got, want) {
			t.Fatalf("report dependencies = %#v, missing %#v", got, want)
		}
	}
}
