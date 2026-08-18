package definition

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompiledDashboardOwnsVisualizationsWithoutAuthoringVisualMaps(t *testing.T) {
	spec := ir.VisualizationSpec{Value: &ir.KPIVisualizationSpec{VisualizationSpecBase: ir.VisualizationSpecBase{
		Kind: "kpi", Title: "Orders", Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Label: "Orders"}}}},
		DataBudget: ir.VisualizationDataBudget{MaxRows: 1, RequiredCompleteness: ir.VisualizationCompletenessComplete}, Accessibility: ir.VisualizationAccessibility{Title: "Orders", Description: "Orders"}, Interactions: []ir.VisualizationInteraction{},
	}, Kind: "kpi", Value: ir.VisualizationFieldRef{Dataset: "primary", Field: "value"}, Presentation: ir.KPIVisualizationPresentation{
		Mode:               ir.VisualizationKPIModeCompact,
		Delta:              ir.VisualizationKPIDeltaModeAbsolute,
		FavorableDirection: ir.VisualizationKPIDirectionNeutral,
		MissingComparison:  ir.VisualizationKPIMissingComparisonShowUnavailable,
		Ranges:             []ir.VisualizationKPIQualitativeRange{},
	}}}
	visual, err := visualizationdefinition.New("orders", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: "sales", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	pages := []dashboard.Page{{ID: "overview"}}
	compiled, err := New("sales", "Sales", "", "sales", pages, map[string]visualizationdefinition.Definition{"orders": visual})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Visualizations["orders"].SpecRevision != visual.SpecRevision {
		t.Fatal("compiled dashboard changed with authoring input")
	}
	if compiled.Visualizations["orders"].Query.Aggregate.Metrics[0].FieldID != "order_count" {
		t.Fatalf("compiled binding = %#v", compiled.Visualizations["orders"].Query)
	}
}

func TestCompiledDashboardOwnsCanonicalFilterNormalization(t *testing.T) {
	regionKey := dashboardfilter.BindingKey("sales", dashboardfilter.ScopePage, "overview", "region")
	hiddenKey := dashboardfilter.BindingKey("sales", dashboardfilter.ScopePage, "other", "hidden")
	compiled := Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"region": {
				ValueKind: dashboardfilter.ValueString,
				Predicates: []dashboardfilter.PredicatePolicy{{
					Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn},
				}},
			},
			"hidden": {
				ValueKind: dashboardfilter.ValueString,
				Predicates: []dashboardfilter.PredicatePolicy{{
					Kind: dashboardfilter.ExpressionComparison, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorEquals},
				}},
			},
		},
		Pages: []dashboard.Page{
			{ID: "overview", FilterBindings: map[string]dashboardfilter.Binding{
				"region": {
					Key: regionKey, ID: "region", Filter: "region", Scope: dashboardfilter.ScopePage, PageID: "overview",
					Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "EU"}}},
				},
			}},
			{ID: "other", FilterBindings: map[string]dashboardfilter.Binding{
				"hidden": {
					Key: hiddenKey, ID: "hidden", Filter: "hidden", Scope: dashboardfilter.ScopePage, PageID: "other",
					Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals, Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "ignored"}},
				},
			}},
		},
	}
	filters := compiled.NormalizeFiltersForPage("overview", dashboard.Filters{
		ServingStateID:      "serving-v7",
		InteractionRevision: 9,
		DataRevisions:       map[string]int64{"orders": 4},
	})
	if filters.CompiledState == nil {
		t.Fatal("compiled filter state is nil")
	}
	if got := filters.CompiledState.AppliedControls[regionKey].Expression.Values; len(got) != 1 || got[0].Value != "EU" {
		t.Fatalf("region defaults = %#v", got)
	}
	if _, ok := filters.CompiledState.AppliedControls[hiddenKey]; !ok {
		t.Fatal("dashboard session state did not retain off-page filter state")
	}
	if filters.ServingStateID != "serving-v7" || filters.InteractionRevision != 9 {
		t.Fatalf("runtime identity = (%q, %d), want (serving-v7, 9)", filters.ServingStateID, filters.InteractionRevision)
	}
	if filters.DataRevisions["orders"] != 4 {
		t.Fatalf("orders data revision = %d, want 4", filters.DataRevisions["orders"])
	}
}
