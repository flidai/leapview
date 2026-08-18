package compiler

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestLowerDashboardQueryAggregatePreservesOrderAliasesGrainAndSort(t *testing.T) {
	month := document.DashboardTimeGrainMonth
	alias := "purchaseMonth"
	query := document.DashboardQuery{Value: &document.AggregateDashboardQuery{
		Type: "aggregate",
		Dimensions: []document.DashboardDimensionSelection{
			{Reference: &document.DashboardDimensionReference{Dimension: "purchaseDate", Grain: &month, Alias: &alias}},
			{String: stringPtr("state")},
		},
		Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}},
		Sort:    &[]document.DashboardSort{{Field: "purchaseMonth", Direction: document.DashboardSortDirectionAsc}},
	}}

	lowered, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales")
	if err != nil {
		t.Fatalf("LowerDashboardQuery() error = %v", err)
	}
	if lower := lowerResultNames(lowered); lower != "purchaseMonth,state,revenue" {
		t.Fatalf("result names = %q", lower)
	}
	if lower := lowerBindingFields(lowered.Binding.Aggregate.Dimensions); lower != "purchaseMonth,state" {
		t.Fatalf("dimension aliases = %q", lower)
	}
	if got := lowered.Binding.Aggregate.Sort[0].FieldID; got != "purchaseMonth" {
		t.Fatalf("sort field = %q", got)
	}
	if !strings.Contains(lowered.Plan.SQL, "purchaseMonth") {
		t.Fatalf("plan SQL does not expose result alias: %s", lowered.Plan.SQL)
	}
	if got := lowered.Request.Dimensions[0].Grain; got != "month" {
		t.Fatalf("dimension grain = %q", got)
	}
	if got := lowered.Binding.Aggregate.Dimensions[0].Grain; got != "month" {
		t.Fatalf("compiled dimension grain = %q", got)
	}
}

func TestLowerDashboardQueryRecordsQualifiesOnlyRootPhysicalFields(t *testing.T) {
	alias := "orderId"
	query := document.DashboardQuery{Value: &document.RecordsDashboardQuery{
		Type: "records", Dataset: "orders",
		Fields: []document.DashboardRecordFieldSelection{
			{Reference: &document.DashboardRecordFieldReference{Field: "order_id", Alias: &alias}},
			{String: stringPtr("status")},
		},
		Sort: &[]document.DashboardSort{{Field: "orderId", Direction: document.DashboardSortDirectionDesc}},
	}}
	lowered, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales")
	if err != nil {
		t.Fatalf("LowerDashboardQuery() error = %v", err)
	}
	if lowered.Binding.Kind != visualizationdefinition.QueryDetail {
		t.Fatalf("binding kind = %q", lowered.Binding.Kind)
	}
	if got := lowerBindingFields(lowered.Binding.Detail.Fields); got != "orderId,status" {
		t.Fatalf("record aliases = %q", got)
	}
	if got := lowered.Binding.Detail.Fields[0].FieldID; got != "orders.order_id" {
		t.Fatalf("qualified record field = %q", got)
	}
	if lowered.RowRequest == nil || lowered.RowRequest.Dataset != "orders" {
		t.Fatalf("row request = %#v", lowered.RowRequest)
	}
}

func TestLowerDashboardQueryPivotUsesExplicitPivotBinding(t *testing.T) {
	rows, columns, grand := true, false, true
	offset, limit := int32(4), int32(25)
	month, day := document.DashboardTimeGrainMonth, document.DashboardTimeGrainDay
	rowAlias, columnAlias := "purchaseMonth", "purchaseDay"
	query := document.DashboardQuery{Value: &document.PivotDashboardQuery{
		Type:    "pivot",
		Rows:    []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "purchaseDate", Grain: &month, Alias: &rowAlias}}},
		Columns: []document.DashboardDimensionSelection{{Reference: &document.DashboardDimensionReference{Dimension: "shipDate", Grain: &day, Alias: &columnAlias}}},
		Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue"}}},
		Totals:  &document.DashboardPivotTotals{Rows: &rows, Columns: &columns, Grand: &grand},
		Window:  &document.DashboardPivotWindow{Offset: &offset, Limit: limit},
	}}
	lowered, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales")
	if err != nil {
		t.Fatalf("LowerDashboardQuery() error = %v", err)
	}
	if lowered.Binding.Kind != visualizationdefinition.QueryPivot || lowered.Binding.Pivot == nil {
		t.Fatalf("pivot binding = %#v", lowered.Binding)
	}
	if got := lowerBindingFields(lowered.Binding.Pivot.Rows); got != "purchaseMonth" {
		t.Fatalf("pivot rows = %q", got)
	}
	if got := lowerBindingFields(lowered.Binding.Pivot.Columns); got != "purchaseDay" {
		t.Fatalf("pivot columns = %q", got)
	}
	if lowered.Binding.Pivot.Rows[0].Grain != "month" || lowered.Binding.Pivot.Columns[0].Grain != "day" {
		t.Fatalf("pivot dimension grains = %#v / %#v", lowered.Binding.Pivot.Rows[0], lowered.Binding.Pivot.Columns[0])
	}
	if lowered.Binding.Pivot.Offset != int64(offset) || lowered.Binding.Pivot.Limit != int64(limit) || lowered.Binding.Pivot.Totals == nil || !lowered.Binding.Pivot.Totals.Rows || lowered.Binding.Pivot.Totals.Columns || !lowered.Binding.Pivot.Totals.Grand {
		t.Fatalf("pivot window/totals = %#v", lowered.Binding.Pivot)
	}
	if lowered.Request.Offset != int(offset) {
		t.Fatalf("planner offset = %d", lowered.Request.Offset)
	}
}

func TestLoweredDashboardQueryValidatesOnlyCompiledResultNames(t *testing.T) {
	query := document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}}}}
	lowered, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales")
	if err != nil {
		t.Fatalf("LowerDashboardQuery() error = %v", err)
	}
	if err := ValidateDashboardResultReferences(lowered, []string{"state", "revenue"}); err != nil {
		t.Fatalf("compiled references rejected: %v", err)
	}
	for _, reference := range []string{"orders.status", "missing", ""} {
		if err := lowered.ValidateResultReference(reference); err == nil {
			t.Fatalf("source/unknown reference %q accepted", reference)
		}
	}
	if err := lowered.ValidateDownstreamReferences(DashboardResultReferences{
		Presentation:  []string{"state"},
		Calculations:  []string{"revenue"},
		Interactions:  []string{"state"},
		Accessibility: []string{"state"},
		Export:        []string{"revenue"},
	}); err != nil {
		t.Fatalf("downstream result references rejected: %v", err)
	}
	if err := lowered.ValidateDownstreamReferences(DashboardResultReferences{Interactions: []string{"orders.status"}}); err == nil || !strings.Contains(err.Error(), "interactions") {
		t.Fatal("interaction source field was accepted as result reference")
	}
}

func TestLowerDashboardQueryRejectsResultReferenceAndRootCollisions(t *testing.T) {
	tests := map[string]struct {
		query document.DashboardQuery
		want  string
	}{
		"duplicate result name": {
			query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: stringPtr("state")}}}}},
			want:  "duplicated",
		},
		"physical aggregate dimension": {
			query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("orders.status")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}}}},
			want:  "physical field",
		},
		"sort source member": {
			query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: stringPtr("totalRevenue")}}}, Sort: &[]document.DashboardSort{{Field: "revenue", Direction: document.DashboardSortDirectionAsc}}}},
			want:  "unknown compiled result field",
		},
		"records outside root": {
			query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records", Dataset: "orders", Fields: []document.DashboardRecordFieldSelection{{String: stringPtr("customers.state")}}}},
			want:  "must be an unqualified root physical field",
		},
		"records empty fields": {
			query: document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records", Dataset: "orders"}},
			want:  "requires at least one field",
		},
		"pivot empty rows": {
			query: document.DashboardQuery{Value: &document.PivotDashboardQuery{Type: "pivot", Columns: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}}}},
			want:  "at least one row dimension",
		},
		"pivot empty columns": {
			query: document.DashboardQuery{Value: &document.PivotDashboardQuery{Type: "pivot", Rows: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}}}},
			want:  "at least one column dimension",
		},
		"pivot empty metrics": {
			query: document.DashboardQuery{Value: &document.PivotDashboardQuery{Type: "pivot", Rows: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Columns: []document.DashboardDimensionSelection{{String: stringPtr("purchaseDate")}}}},
			want:  "at least one metric",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := LowerDashboardQuery(test.query, dashboardQueryTestModel(), "sales")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLowerDashboardQueryLowersHistogramStatisticalContract(t *testing.T) {
	query := document.DashboardQuery{Value: &document.HistogramDashboardQuery{Type: "histogram", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Bins: 10, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}}
	lowered, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales")
	if err != nil {
		t.Fatalf("LowerDashboardQuery() error = %v", err)
	}
	if lowered.Binding.ResultShape != visualizationdefinition.ResultHistogramBins || lowered.Binding.Aggregate == nil || lowered.Binding.Aggregate.Histogram == nil {
		t.Fatalf("histogram binding = %#v", lowered.Binding)
	}
	if got := lowerResultNames(lowered); got != "bucket,count,start,end" {
		t.Fatalf("histogram result frame = %q", got)
	}
}

func TestLowerDashboardQueryLowersDistributionAndPreservesStatisticalOperands(t *testing.T) {
	query := document.DashboardQuery{Value: &document.DistributionDashboardQuery{
		Type: "distribution", Field: document.DashboardMetricSelection{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: stringPtr("amount")}},
		Quantiles: []float64{0.1, 0.5, 0.9}, Whiskers: &document.DashboardDistributionWhiskers{Lower: 0.05, Upper: 0.95}, Outliers: document.DashboardDistributionOutlierPolicyOmit, Approximation: document.DashboardHistogramApproximationApproximate,
	}}
	lowered, err := LowerDashboardQuery(query, dashboardQueryTestModel(), "sales")
	if err != nil {
		t.Fatalf("LowerDashboardQuery() error = %v", err)
	}
	binding := lowered.Binding.Aggregate.Distribution
	if binding == nil || binding.Metric.Alias != "amount" || binding.Approximation != "approximate" || binding.Outliers != "omit" || len(binding.Quantiles) != 3 || binding.Whiskers == nil || binding.Whiskers.Lower != 0.05 {
		t.Fatalf("distribution binding = %#v", binding)
	}
	if got := lowerResultNames(lowered); got != "label,min,q0,q1,q2,max" {
		t.Fatalf("distribution result frame = %q", got)
	}
	if !strings.Contains(lowered.Plan.SQL, "approx_quantile") || !strings.Contains(lowered.Plan.SQL, "lower_value") {
		t.Fatalf("distribution SQL does not preserve approximation/whisker semantics: %s", lowered.Plan.SQL)
	}
	explain, err := lowered.Plan.Explain()
	if err != nil || !strings.Contains(explain, "approximation=approximate") || !strings.Contains(explain, "outliers=omit") {
		t.Fatalf("distribution PlanIR explanation omitted statistical policy: %s (err=%v)", explain, err)
	}
}

func TestLowerDashboardQueryRejectsInvalidStatisticalOperands(t *testing.T) {
	model := dashboardQueryTestModel()
	tests := map[string]struct {
		query document.DashboardQuery
		want  string
	}{
		"zero bins": {
			query: document.DashboardQuery{Value: &document.HistogramDashboardQuery{Type: "histogram", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Bins: 0, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}}, want: "bins",
		},
		"excessive bins": {
			query: document.DashboardQuery{Value: &document.HistogramDashboardQuery{Type: "histogram", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Bins: 100001, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}}, want: "bins",
		},
		"partial domain": {
			query: document.DashboardQuery{Value: &document.HistogramDashboardQuery{Type: "histogram", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Bins: 2, Domain: &document.DashboardHistogramDomain{Minimum: floatPtr(0)}, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}}, want: "domain",
		},
		"nonnumeric metric": {
			query: document.DashboardQuery{Value: &document.HistogramDashboardQuery{Type: "histogram", Field: document.DashboardMetricSelection{String: stringPtr("statusMetric")}, Bins: 2, NullPolicy: document.DashboardHistogramNullPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}}, want: "unsupported datatype",
		},
		"duplicate quantiles": {
			query: document.DashboardQuery{Value: &document.DistributionDashboardQuery{Type: "distribution", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Quantiles: []float64{0.5, 0.5}, Outliers: document.DashboardDistributionOutlierPolicyInclude, Approximation: document.DashboardHistogramApproximationExact}}, want: "quantiles",
		},
		"invalid whiskers": {
			query: document.DashboardQuery{Value: &document.DistributionDashboardQuery{Type: "distribution", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Quantiles: []float64{0.5}, Whiskers: &document.DashboardDistributionWhiskers{Lower: 0.9, Upper: 0.1}, Outliers: document.DashboardDistributionOutlierPolicyInclude, Approximation: document.DashboardHistogramApproximationExact}}, want: "whiskers",
		},
		"inert outlier omit": {
			query: document.DashboardQuery{Value: &document.DistributionDashboardQuery{Type: "distribution", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Quantiles: []float64{0.5}, Outliers: document.DashboardDistributionOutlierPolicyOmit, Approximation: document.DashboardHistogramApproximationExact}}, want: "requires whiskers",
		},
		"inert whisker include": {
			query: document.DashboardQuery{Value: &document.DistributionDashboardQuery{Type: "distribution", Field: document.DashboardMetricSelection{String: stringPtr("revenue")}, Quantiles: []float64{0.5}, Whiskers: &document.DashboardDistributionWhiskers{Lower: 0.1, Upper: 0.9}, Outliers: document.DashboardDistributionOutlierPolicyInclude, Approximation: document.DashboardHistogramApproximationExact}}, want: "require outliers omit",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			caseModel := model
			if name == "nonnumeric metric" {
				caseModel = dashboardQueryTestModel()
				caseModel.Metrics["statusMetric"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.status"}}
			}
			if _, err := LowerDashboardQuery(test.query, caseModel, "sales"); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func dashboardQueryTestModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName:   "orders",
			GrainEntity: "order", Entities: map[string]semanticmodel.ModelEntitySpec{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"order_id": {Datatype: semanticmodel.DataTypeInteger}, "ordered_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
				"status": {Type: "string", Datatype: semanticmodel.DataTypeString}, "revenue": {Type: "number", Datatype: semanticmodel.DataTypeDecimal},
			},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"purchaseDate": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}}},
			"shipDate":     {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ, NativeGrain: "day", Grains: []string{"day", "month"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.ordered_at"}}},
			"state":        {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}}},
	}
}

func stringPtr(value string) *string { return &value }

func floatPtr(value float64) *float64 { return &value }

func lowerResultNames(value LoweredDashboardQuery) string {
	parts := make([]string, len(value.ResultFrame))
	for index, field := range value.ResultFrame {
		parts[index] = field.Name
	}
	return strings.Join(parts, ",")
}

func lowerBindingFields(values []visualizationdefinition.FieldBinding) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = value.Alias
	}
	return strings.Join(parts, ",")
}
