package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// The scripted data port exposes the governed requests as well as their output;
// redundant full-axis predicates are observable even when values match.
type pivotAxisDataRuntime struct {
	pivotWindowDataRuntime
	t         *testing.T
	responses []reportdef.QueryRows
}

func (r *pivotAxisDataRuntime) Query(_ context.Context, query reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	r.queries = append(r.queries, query)
	index := len(r.queries) - 1
	if index >= len(r.responses) {
		r.t.Fatalf("unexpected governed query %d: %#v", index, query)
	}
	return r.responses[index], nil
}

func pivotAxisTestPlan() tablePlan {
	base := visualizationir.VisualizationSpecBase{
		Title: "Pivot",
		Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{
			{ID: "row", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString},
			{ID: "column", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString},
			{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal},
		}}},
		DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete},
	}
	return tablePlan{
		Definition: visualizationdefinition.Definition{ID: "pivot", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{
			VisualizationSpecBase: base, Kind: "pivot",
		}}, Query: visualizationdefinition.QueryBinding{DatasetID: "primary"}},
		Table: "orders", Rows: []visualizationdefinition.FieldBinding{{FieldID: "row", Alias: "row"}},
		ColumnDims: []visualizationdefinition.FieldBinding{{FieldID: "column", Alias: "column"}},
		Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "value", Alias: "value"}}, Limit: 10,
	}
}

func TestPivotCompleteAxisKeepsGovernedFiltersNullRowsAndExactTotals(t *testing.T) {
	fake := &pivotAxisDataRuntime{t: t, responses: []reportdef.QueryRows{
		{{"row": nil}, {"row": "North"}},
		{{"row": nil, "column": "East", "value": 7.0}, {"row": "North", "column": "West", "value": 3.0}},
		{{"column": "East", "value": 7.0}, {"column": "West", "value": 3.0}},
		{{"value": 10.0}},
	}}
	report := &dashboarddefinition.Definition{
		FilterDefinitions: map[string]dashboardfilter.Definition{"status": {Field: "orders.status", Dataset: "orders", ValueKind: dashboardfilter.ValueString}},
		FilterBindings:    map[string]dashboardfilter.Binding{"status": {Key: "status", Filter: "status", Scope: dashboardfilter.ScopeReport, Targets: []string{"overview/pivot"}}},
	}
	state := dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{"status": {ResolvedExpression: dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: []dashboardfilter.Value{{Kind: dashboardfilter.ValueString, Value: "paid"}},
	}}}}
	table := pivotAxisTestPlan()
	table.Totals = &visualizationdefinition.PivotTotals{Rows: true, Columns: true, Grand: true}
	service := &VisualizationDataService{filters: &FilterService{}}
	_, rows, incomplete, err := service.crossTabTableRows(context.Background(), &modelRuntime{model: &semanticmodel.Model{}, data: fake}, report, table,
		dashboard.Filters{CompiledState: &state, ActivePageID: "overview"}, dashboard.TableRequest{Table: "pivot"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if incomplete || len(rows) != 3 || rows[0]["row"] != nil || rows[1]["row"] != "North" {
		t.Fatalf("complete pivot rows=%#v incomplete=%v", rows, incomplete)
	}
	if rows[0]["pivot_total"] != 7.0 || rows[1]["pivot_total"] != 3.0 || rows[2]["pivot_east"] != 7.0 || rows[2]["pivot_west"] != 3.0 || rows[2]["pivot_grand"] != 10.0 {
		t.Fatalf("complete pivot lost exact row/column/grand totals: %#v", rows)
	}
	wantFilters := []reportdef.QueryFilter{{Field: "orders.status", Dataset: "orders", Operator: "in", Values: []any{"paid"}}}
	for i, query := range fake.queries {
		if !reflect.DeepEqual(query.Filters, wantFilters) {
			t.Fatalf("query %d filters=%#v, want original governed filters=%#v without full-axis enumeration", i, query.Filters, wantFilters)
		}
	}
	if len(fake.queries) != 4 || fake.queries[0].Offset != 0 || fake.queries[0].Limit != 11 || fake.queries[1].Limit != 101 {
		t.Fatalf("complete pivot query budgets=%#v", fake.queries)
	}
}

func TestPivotWindowRetainsIdentityRestrictionAndNullSemantics(t *testing.T) {
	cases := []struct {
		name       string
		offset     int64
		axis       reportdef.QueryRows
		incomplete bool
	}{
		{name: "offset window", offset: 4, axis: reportdef.QueryRows{{"row": nil}, {"row": "North"}}},
		{name: "lookahead window", axis: reportdef.QueryRows{{"row": nil}, {"row": "North"}, {"row": "South"}}, incomplete: true},
		// Raw lookahead rows must keep the restriction even if delivered identities deduplicate.
		{name: "duplicate lookahead identities", axis: reportdef.QueryRows{{"row": nil}, {"row": "North"}, {"row": "North"}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fake := &pivotAxisDataRuntime{t: t, responses: []reportdef.QueryRows{test.axis,
				{{"row": nil, "column": "East", "value": 7.0}, {"row": "North", "column": "East", "value": 3.0}, {"row": "South", "column": "East", "value": 99.0}},
			}}
			table := pivotAxisTestPlan()
			table.Offset, table.Limit = test.offset, 2
			service := &VisualizationDataService{filters: &FilterService{}}
			_, rows, incomplete, err := service.crossTabTableRows(context.Background(), &modelRuntime{model: &semanticmodel.Model{}, data: fake}, &dashboarddefinition.Definition{}, table, dashboard.Filters{}, dashboard.TableRequest{Table: "pivot"}, true)
			if err != nil {
				t.Fatal(err)
			}
			if incomplete != test.incomplete || len(rows) != 2 || rows[0]["row"] != nil || rows[1]["row"] != "North" {
				t.Fatalf("window rows=%#v incomplete=%v; want null/North and incomplete=%v", rows, incomplete, test.incomplete)
			}
			want := []reportdef.QueryFilter{{Groups: []reportdef.QueryFilterGroup{
				{Filters: []reportdef.QueryFilter{{Field: "row", Operator: "is_null"}}},
				{Filters: []reportdef.QueryFilter{{Field: "row", Operator: "equals", Values: []any{"North"}}}},
			}}}
			if len(fake.queries) != 2 || !reflect.DeepEqual(fake.queries[1].Filters, want) || fake.queries[0].Offset != int(test.offset) || fake.queries[0].Limit != 3 {
				t.Fatalf("window governed requests=%#v; want offset=%d lookahead=3 and null-safe selected identities=%#v", fake.queries, test.offset, want)
			}
		})
	}
}

func TestPivotCompleteAxisStillRejectsIncompleteCellBudget(t *testing.T) {
	fake := &pivotAxisDataRuntime{t: t, responses: []reportdef.QueryRows{
		{{"row": "North"}},
		{{"row": "North", "column": "East", "value": 7.0}, {"row": "North", "column": "West", "value": 3.0}},
	}}
	table := pivotAxisTestPlan()
	table.Definition.Spec.Value.(*visualizationir.PivotVisualizationSpec).DataBudget.MaxRows = 1
	service := &VisualizationDataService{filters: &FilterService{}}
	_, _, _, err := service.crossTabTableRows(context.Background(), &modelRuntime{model: &semanticmodel.Model{}, data: fake}, &dashboarddefinition.Definition{}, table, dashboard.Filters{}, dashboard.TableRequest{Table: "pivot"}, true)
	if err == nil || !strings.Contains(err.Error(), "pivot cells exceed data budget maxRows 1") {
		t.Fatalf("complete row axis accepted incomplete cells: error=%v", err)
	}
	if len(fake.queries) != 2 || fake.queries[1].Limit != 2 {
		t.Fatalf("cell query did not retain budget plus sentinel: %#v", fake.queries)
	}
}
