package explorationadapter

import (
	"testing"

	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func recordsSpec() exploration.ExplorationSpec {
	mode, dataset, alias := exploration.ExplorationQueryModeRecords, "orders", "Status"
	return exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", Mode: &mode, DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.id"}, {Field: "order_status", Alias: &alias}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{{Field: "order_status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{Kind: "set", Operator: "in", Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}}}}}},
		Sort:    []exploration.ExplorationSort{{Field: "order_status", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 80}
}

func recordsOptions() Options {
	return Options{VisualID: "records", Bindings: map[string]string{"order_status": "order_status"}, RecordFields: map[string]string{"orders.id": "id", "order_status": "status"}}
}

func TestConvertRecordsPreservesDatasetPhysicalFieldsAliasesSortAndFilters(t *testing.T) {
	spec := recordsSpec()
	density := exploration.ExplorationTableDensityCompact
	spec.Table = &exploration.ExplorationTableDisplayConfig{Density: &density}
	result, err := Convert(spec, recordsOptions())
	if err != nil {
		t.Fatal(err)
	}
	query, ok := result.Visual.Query.Value.(*document.RecordsDashboardQuery)
	if !ok || query.Dataset != "orders" || query.Limit == nil || *query.Limit != 80 || len(query.Fields) != 2 {
		t.Fatalf("records query=%#v", result.Visual.Query.Value)
	}
	if query.Fields[0].Reference.Field != "id" || *query.Fields[0].Reference.Alias != "id" || query.Fields[1].Reference.Field != "status" || *query.Fields[1].Reference.Alias != "Status" {
		t.Fatalf("record fields=%#v", query.Fields)
	}
	if query.Sort == nil || (*query.Sort)[0].Field != "Status" || (*query.Sort)[0].Direction != document.DashboardSortDirectionDesc {
		t.Fatalf("record sort=%#v", query.Sort)
	}
	presentation, ok := result.Visual.Presentation.Value.(*document.TableDashboardPresentation)
	if result.Visual.Type != document.DashboardVisualTypeTable || !ok || presentation.RowHeight != 24 {
		t.Fatalf("records presentation=%#v", result.Visual)
	}
	if len(result.Filters) != 1 || result.Filters[0].Dimension != "order_status" || result.Filters[0].Targets == nil || (*result.Filters[0].Targets)[0] != "records" {
		t.Fatalf("scoped filters=%#v", result.Filters)
	}
	expression := result.Filters[0].Default.Value.(*document.SetDashboardFilterExpression)
	if expression.Values[0].Value.(*document.StringDashboardFilterValue).Value != "paid" {
		t.Fatalf("filter value=%#v", expression)
	}
}

func TestConvertRecordsRejectsLostSemantics(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*exploration.ExplorationSpec, *Options)
	}{
		{"unbound field", func(_ *exploration.ExplorationSpec, options *Options) { delete(options.RecordFields, "orders.id") }},
		{"qualified physical field", func(_ *exploration.ExplorationSpec, options *Options) { options.RecordFields["orders.id"] = "other.id" }},
		{"metric", func(spec *exploration.ExplorationSpec, _ *Options) {
			spec.Metrics = []exploration.ExplorationMetricRef{{Field: "revenue"}}
		}},
		{"chart", func(spec *exploration.ExplorationSpec, _ *Options) {
			spec.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.CartesianExplorationVisualization{Kind: "cartesian", Mark: exploration.VisualizationCartesianMarkLine}}
		}},
		{"column subset", func(spec *exploration.ExplorationSpec, _ *Options) {
			spec.Visualization = &exploration.ExplorationVisualizationConfig{Value: &exploration.TableExplorationVisualization{Kind: "table", Columns: []exploration.ExplorationVisualizationFieldRef{{Field: "orders.id"}}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, options := recordsSpec(), recordsOptions()
			test.mutate(&spec, &options)
			if _, err := Convert(spec, options); err == nil {
				t.Fatal("unsupported records semantics accepted")
			}
		})
	}
}
