package runtime

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestBundleAggregateRequestPreservesTemporalDimensionGrain(t *testing.T) {
	visual := visualPlan{
		Definition: visualizationdefinition.Definition{Query: visualizationdefinition.QueryBinding{ResultShape: visualizationdefinition.ResultCategoryValue}},
		Table:      "orders",
		Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "orders.created_at", Alias: "month", Grain: "month"}},
		Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}},
	}
	service := &VisualizationDataService{filters: &FilterService{}}
	query, err := service.bundleAggregateRequest(context.Background(), &modelRuntime{}, &dashboarddefinition.Definition{}, dashboard.Filters{}, "monthly", visual)
	if err != nil {
		t.Fatal(err)
	}
	if query.Time.Field != "orders.created_at" || query.Time.Grain != "month" || query.Time.Alias != "month" {
		t.Fatalf("query time = %#v, want monthly temporal dimension", query.Time)
	}
	if len(query.Dimensions) != 0 {
		t.Fatalf("query dimensions = %#v, want temporal dimension in query time", query.Dimensions)
	}
}

func TestVisualSortsUseCanonicalResultNames(t *testing.T) {
	visual := visualPlan{
		Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "orders.status", Alias: "status"}},
		Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "orders.revenue", Alias: "amount"}},
		Sort:       []visualizationdefinition.Sort{{FieldID: "status", Direction: "desc"}},
	}
	for name, sorts := range map[string][]reportdef.QuerySort{"visualSorts": visualSorts(visual), "aliasedVisualSorts": aliasedVisualSorts(visual)} {
		if len(sorts) != 1 || sorts[0].Field != "status" || sorts[0].Direction != "desc" {
			t.Fatalf("%s = %#v, want canonical status sort", name, sorts)
		}
	}
}

func TestFlattenHierarchyRowsReadsAuthoredMetricAlias(t *testing.T) {
	rows, err := flattenHierarchyRowsTyped(reportdef.QueryRows{
		{"division": "sales", "team": "field", "order_count": "2.50"},
	}, []string{"division", "team"}, "order_count", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("flattened rows = %#v, want root and two child nodes", rows)
	}
	for _, row := range rows {
		if _, ok := row["order_count"]; !ok {
			t.Fatalf("flattened row omitted authored metric alias: %#v", row)
		}
		if _, synthetic := row["value"]; synthetic {
			t.Fatalf("flattened row emitted removed synthetic value alias: %#v", row)
		}
	}
}

func TestHierarchyMetricIsDecimalUsesMetricRole(t *testing.T) {
	decimal := visualizationir.VisualizationSpec{Value: &visualizationir.HierarchyVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "order_count", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal}}}}}}}
	if !hierarchyMetricIsDecimal(decimal) {
		t.Fatal("decimal metric role was not detected")
	}
	identityOnly := visualizationir.VisualizationSpec{Value: &visualizationir.HierarchyVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "value", Role: visualizationir.VisualizationFieldRoleIdentity, DataType: visualizationir.VisualizationDataTypeDecimal}}}}}}}
	if hierarchyMetricIsDecimal(identityOnly) {
		t.Fatal("identity field with decimal datatype was treated as metric")
	}
}
