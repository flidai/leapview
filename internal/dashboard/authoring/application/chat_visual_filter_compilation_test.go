package application

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestImportedChatFiltersCompileWithPageURLCollisionsAndCascadingOptions(t *testing.T) {
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
	metric, parentParameter, childParameter := "order_count", "region", "status"
	visual := document.DashboardVisual{Type: document.DashboardVisualTypeKpi, Presentation: document.DashboardPresentation{Value: &document.KPIDashboardPresentation{Type: "kpi"}}, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: &metric}}}}}
	filters := []document.DashboardFilter{
		{ID: "status", Label: "Status", Dimension: "status", URLParameter: &childParameter, Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect", Options: &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", DependsOn: stringSlicePointer([]string{"segment"})}}}}},
		{ID: "segment", Label: "Segment", Dimension: "segment", Targets: stringSlicePointer([]string{"page/visual"}), URLParameter: &parentParameter,
			Default: &document.DashboardFilterExpression{Value: &document.SetDashboardFilterExpression{Type: "set", Operator: document.DashboardFilterOperatorIn, Values: []document.DashboardFilterValue{{Value: &document.StringDashboardFilterValue{Type: "string", Value: "consumer"}}}}},
			Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}},
	}
	source := ChatVisualImport{ArtifactID: "chat-artifact", SemanticModelID: "sales", Visual: visual, Filters: filters}
	standalone := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "standalone"}, Spec: document.DashboardSpec{
		SemanticModel: "sales", Filters: filters, Visuals: map[string]document.DashboardVisual{"chat-artifact": visual},
		Pages: []document.DashboardPage{{ID: "page", Title: "Page", Components: []document.DashboardPageComponent{filterCompilationVisualComponent("visual", "chat-artifact")}}},
	}}
	if _, err := compiler.CompileCanonicalDashboardFilters(standalone, model); err != nil {
		t.Fatalf("original chat filter contract is invalid: %v", err)
	}
	before, err := standalone.Clone()
	if err != nil {
		t.Fatal(err)
	}
	bindings := []document.DashboardPageFilterBinding{{ID: "existing-region", Filter: "segment", URLParameter: &parentParameter}}
	dashboard := document.DashboardDocument{Metadata: document.DashboardMetadata{ID: "dashboard"}, Spec: document.DashboardSpec{
		SemanticModel: "sales", Filters: []document.DashboardFilter{{ID: "segment", Label: "Existing segment", Dimension: "segment", Control: document.DashboardFilterControl{Value: &document.SingleSelectDashboardFilterControl{Type: "singleSelect"}}}},
		Visuals: map[string]document.DashboardVisual{"existing": visual},
		Pages:   []document.DashboardPage{{ID: "overview", Title: "Overview", FilterBindings: &bindings, Components: []document.DashboardPageComponent{filterCompilationVisualComponent("existing", "existing")}}},
	}}
	for _, command := range []authoring.CommandID{"018f5c8a-9d40-7c50-9e31-1c8a7b1a0e0b", "018f5c8a-9d40-7c50-9e31-1c8a7b1a0e0c"} {
		if _, err := AddChatVisualToDocument(&dashboard, "overview", source, command); err != nil {
			t.Fatal(err)
		}
		importedID, err := importedChatVisualID(source, command)
		if err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.CompileCanonicalDashboardFilters(dashboard, model)
		if err != nil {
			t.Fatalf("dashboard failed filter compilation after import: %v", err)
		}
		var parentID, childID string
		for _, filter := range dashboard.Spec.Filters {
			if filter.Targets == nil || len(*filter.Targets) != 1 || (*filter.Targets)[0] != importedID {
				continue
			}
			if filter.Dimension == "segment" {
				parentID = filter.ID
			}
			if filter.Dimension == "status" {
				childID = filter.ID
			}
		}
		if parentID == "" || childID == "" {
			t.Fatalf("imported filters are missing: parent=%q child=%q", parentID, childID)
		}
		if got := compiled.Bindings[childID].OptionDependencies; !reflect.DeepEqual(got, []dashboardfilter.BindingRef{{Scope: dashboardfilter.ScopeReport, ID: parentID}}) {
			t.Fatalf("compiled option dependencies=%#v, want imported parent %q", got, parentID)
		}
		if got := compiled.Bindings[parentID].Default; got.Kind != dashboardfilter.ExpressionSet || len(got.Values) != 1 {
			t.Fatalf("imported default filter was lost: %#v", got)
		}
		count := len(dashboard.Spec.Filters)
		alreadyAdded, err := AddChatVisualToDocument(&dashboard, "overview", source, command)
		if err != nil || !alreadyAdded || len(dashboard.Spec.Filters) != count {
			t.Fatalf("replay changed filters: already=%v err=%v count=%d", alreadyAdded, err, len(dashboard.Spec.Filters))
		}
	}
	if !reflect.DeepEqual(source.Filters, before.Spec.Filters) {
		t.Fatal("import mutated the source filters")
	}
}

func filterCompilationVisualComponent(id, visual string) document.DashboardPageComponent {
	return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}}, Type: "visual", Visual: visual}}
}
