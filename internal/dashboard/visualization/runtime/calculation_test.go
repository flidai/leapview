package runtime

import (
	"math"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestApplyVisualCalculationsEvaluatesClosedTemplatesDeterministically(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{
			ID: "primary",
			Fields: []ir.VisualizationField{
				{ID: "period", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: false, Label: "Period"},
				{ID: "parent", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: false, Label: "Parent"},
				{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Value"},
			},
		}},
		Calculations: calculationList(
			calculation("running", ir.VisualizationCalculationTemplateRunningTotal, "value"),
			ir.VisualizationCalculation{ID: "moving", Label: "Moving average", Dataset: "primary", Template: ir.VisualizationCalculationTemplateMovingAverage, Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows, Window: int64Pointer(2)},
			calculation("difference", ir.VisualizationCalculationTemplateDifference, "value"),
			calculation("percentage_difference", ir.VisualizationCalculationTemplatePercentageDifference, "value"),
			ir.VisualizationCalculation{ID: "parent_share", Label: "Parent share", Dataset: "primary", Template: ir.VisualizationCalculationTemplatePercentOfParent, Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows, PartitionBy: []ir.VisualizationFieldRef{fieldRef("parent")}},
			calculation("grand_share", ir.VisualizationCalculationTemplatePercentOfGrandTotal, "value"),
			ir.VisualizationCalculation{ID: "rank", Label: "Rank", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRank, Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows, OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("value"), Direction: ir.VisualizationSortDirectionDescending}}},
			ir.VisualizationCalculation{ID: "cumulative", Label: "Cumulative contribution", Dataset: "primary", Template: ir.VisualizationCalculationTemplateCumulativeContribution, Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows, OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("value"), Direction: ir.VisualizationSortDirectionDescending}}},
			ir.VisualizationCalculation{ID: "lookup", Label: "Baseline", Dataset: "primary", Template: ir.VisualizationCalculationTemplateLookup, Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows, Lookup: &ir.VisualizationCalculationLookup{Field: fieldRef("period"), Value: "Q1"}},
		),
	}
	frame := Frame{
		Columns: []string{"period", "parent", "value"},
		Rows: [][]any{
			{"Q1", "A", 10.0},
			{"Q2", "A", 20.0},
			{"Q3", "B", 30.0},
			{"Q4", "B", 40.0},
		},
	}

	result, diagnostics, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
	wantColumns := []string{"period", "parent", "value", "running", "moving", "difference", "percentage_difference", "parent_share", "grand_share", "rank", "cumulative", "lookup"}
	if strings.Join(result.Columns, ",") != strings.Join(wantColumns, ",") {
		t.Fatalf("columns = %#v, want %#v", result.Columns, wantColumns)
	}
	assertNumericColumn(t, result, "running", []any{10.0, 30.0, 60.0, 100.0})
	assertNumericColumn(t, result, "moving", []any{10.0, 15.0, 25.0, 35.0})
	assertNumericColumn(t, result, "difference", []any{nil, 10.0, 10.0, 10.0})
	assertNumericColumn(t, result, "percentage_difference", []any{nil, 1.0, 0.5, 1.0 / 3.0})
	assertNumericColumn(t, result, "parent_share", []any{1.0 / 3.0, 2.0 / 3.0, 3.0 / 7.0, 4.0 / 7.0})
	assertNumericColumn(t, result, "grand_share", []any{0.1, 0.2, 0.3, 0.4})
	assertNumericColumn(t, result, "rank", []any{4.0, 3.0, 2.0, 1.0})
	assertNumericColumn(t, result, "cumulative", []any{1.0, 0.9, 0.7, 0.4})
	assertNumericColumn(t, result, "lookup", []any{10.0, 10.0, 10.0, 10.0})
}

func TestApplyVisualCalculationsSupportsDependenciesAndRejectsCycles(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: false, Label: "Value"},
		}}},
		Calculations: calculationList(
			calculation("running", ir.VisualizationCalculationTemplateRunningTotal, "value"),
			calculation("running_share", ir.VisualizationCalculationTemplatePercentOfGrandTotal, "running"),
		),
	}
	frame := Frame{Columns: []string{"value"}, Rows: [][]any{{10.0}, {20.0}}}
	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	assertNumericColumn(t, result, "running_share", []any{0.25, 0.75})

	base.Calculations = calculationList(
		calculation("a", ir.VisualizationCalculationTemplateRunningTotal, "b"),
		calculation("b", ir.VisualizationCalculationTemplateRunningTotal, "a"),
	)
	if _, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v, want dependency cycle", err)
	}
}

func TestApplyVisualCalculationsPromotedIntegerArithmeticPreservesExactValues(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeInteger, Nullable: false, Label: "Value"},
			{ID: "running", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Running"},
			{ID: "difference", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Difference"},
		}}},
		Calculations: calculationList(
			calculation("running", ir.VisualizationCalculationTemplateRunningTotal, "value"),
			calculation("difference", ir.VisualizationCalculationTemplateDifference, "value"),
		),
	}
	frame := Frame{Columns: []string{"value"}, Rows: [][]any{{int64(9007199254740993)}, {int64(2)}}}
	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	if got := result.Rows[1][1]; got != "9007199254740995" {
		t.Fatalf("running[1] = %#v, want exact 9007199254740995", got)
	}
	if got := result.Rows[1][2]; got != "-9007199254740991" {
		t.Fatalf("difference[1] = %#v, want exact -9007199254740991", got)
	}
}

func TestApplyVisualCalculationsComparesExactNumericTransports(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "decimal_value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: false, Label: "Decimal value"},
			{ID: "integer_value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeInteger, Nullable: false, Label: "Integer value"},
			{ID: "decimal_rank", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeInteger, Nullable: true, Label: "Decimal rank"},
			{ID: "integer_rank", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeInteger, Nullable: true, Label: "Integer rank"},
			{ID: "ordered_running", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Ordered running"},
			{ID: "integer_lookup", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeInteger, Nullable: true, Label: "Integer lookup"},
		}}},
		Calculations: calculationList(
			ir.VisualizationCalculation{
				ID: "decimal_rank", Label: "Decimal rank", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRank,
				Source: fieldRef("decimal_value"), Axis: ir.VisualizationCalculationAxisRows,
				OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("decimal_value"), Direction: ir.VisualizationSortDirectionDescending}},
			},
			ir.VisualizationCalculation{
				ID: "integer_rank", Label: "Integer rank", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRank,
				Source: fieldRef("integer_value"), Axis: ir.VisualizationCalculationAxisRows,
				OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("integer_value"), Direction: ir.VisualizationSortDirectionDescending}},
			},
			ir.VisualizationCalculation{
				ID: "ordered_running", Label: "Ordered running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
				Source: fieldRef("decimal_value"), Axis: ir.VisualizationCalculationAxisRows,
				OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("decimal_value"), Direction: ir.VisualizationSortDirectionDescending}},
			},
			ir.VisualizationCalculation{
				ID: "integer_lookup", Label: "Integer lookup", Dataset: "primary", Template: ir.VisualizationCalculationTemplateLookup,
				Source: fieldRef("integer_value"), Axis: ir.VisualizationCalculationAxisRows,
				Lookup: &ir.VisualizationCalculationLookup{Field: fieldRef("integer_value"), Value: "9007199254740993.0"},
			},
		),
	}
	frame := Frame{
		Columns: []string{"decimal_value", "integer_value"},
		Rows: [][]any{
			{"9007199254740993.125", int64(9007199254740992)},
			{"9007199254740994.125", int64(9007199254740993)},
			{"1", int64(2)},
		},
	}

	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	assertNumericColumn(t, result, "decimal_rank", []any{int64(2), int64(1), int64(3)})
	assertNumericColumn(t, result, "integer_rank", []any{int64(2), int64(1), int64(3)})
	assertStringColumn(t, result, "ordered_running", []string{"18014398509481987.250", "9007199254740994.125", "18014398509481988.250"})
	assertAnyColumn(t, result, "integer_lookup", []any{int64(9007199254740993), int64(9007199254740993), int64(9007199254740993)})
}

func TestApplyVisualCalculationsKeepsLargeIntegerPartitionsDistinct(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "group", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeInteger, Nullable: false, Label: "Group"},
			{ID: "subgroup", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeInteger, Nullable: false, Label: "Subgroup"},
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: false, Label: "Value"},
			{ID: "running_group", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Group running"},
			{ID: "running_pair", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Pair running"},
		}}},
		Calculations: calculationList(
			ir.VisualizationCalculation{
				ID: "running_group", Label: "Group running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows, PartitionBy: []ir.VisualizationFieldRef{fieldRef("group")},
			},
			ir.VisualizationCalculation{
				ID: "running_pair", Label: "Pair running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows,
				PartitionBy: []ir.VisualizationFieldRef{fieldRef("group"), fieldRef("subgroup")},
			},
		),
	}
	frame := Frame{
		Columns: []string{"group", "subgroup", "value"},
		Rows: [][]any{
			{int64(9007199254740992), int64(1), "1"},
			{int64(9007199254740993), int64(1), "1"},
			{int64(9007199254740992), int64(1), "2"},
			{int64(9007199254740993), int64(2), "4"},
		},
	}

	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	assertStringColumn(t, result, "running_group", []string{"1", "1", "3", "5"})
	assertStringColumn(t, result, "running_pair", []string{"1", "1", "3", "4"})
}

func TestApplyVisualCalculationsDoesNotMaskIncompleteFrames(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "table",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: false, Label: "Value"},
		}}},
		Calculations: calculationList(calculation("share", ir.VisualizationCalculationTemplatePercentOfGrandTotal, "value")),
	}
	frame := Frame{Columns: []string{"value"}, Rows: [][]any{{10.0}, {20.0}}}

	result, diagnostics, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessTruncated)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	assertNumericColumn(t, result, "share", []any{1.0 / 3.0, 2.0 / 3.0})
	if len(diagnostics) != 1 || diagnostics[0].Code != "visual_calculation_incomplete_frame" || diagnostics[0].Severity != ir.VisualizationDiagnosticSeverityWarning {
		t.Fatalf("diagnostics = %#v, want incomplete-frame warning", diagnostics)
	}
}

func TestApplyVisualCalculationsHonorsHierarchyFacetAndColumnPartitions(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "matrix",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "region", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: false, Label: "Region"},
			{ID: "parent", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: false, Label: "Parent"},
			{ID: "period", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: false, Label: "Period"},
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: false, Label: "Value"},
		}}},
		Calculations: calculationList(
			ir.VisualizationCalculation{
				ID: "parent_share", Label: "Parent share", Dataset: "primary", Template: ir.VisualizationCalculationTemplatePercentOfParent,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisHierarchy, Parent: fieldRefPointer("parent"),
			},
			ir.VisualizationCalculation{
				ID: "facet_running", Label: "Facet running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisFacets, PartitionBy: []ir.VisualizationFieldRef{fieldRef("region")},
				OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("period"), Direction: ir.VisualizationSortDirectionAscending}},
			},
			ir.VisualizationCalculation{
				ID: "column_running", Label: "Column running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisColumns, PartitionBy: []ir.VisualizationFieldRef{fieldRef("region")},
				OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("period"), Direction: ir.VisualizationSortDirectionAscending}},
			},
		),
	}
	frame := Frame{
		Columns: []string{"region", "parent", "period", "value"},
		Rows: [][]any{
			{"North", "A", "Q1", 10.0},
			{"North", "A", "Q2", 30.0},
			{"South", "B", "Q1", 20.0},
			{"South", "B", "Q2", 40.0},
		},
	}
	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	assertNumericColumn(t, result, "parent_share", []any{0.25, 0.75, 1.0 / 3.0, 2.0 / 3.0})
	assertNumericColumn(t, result, "facet_running", []any{10.0, 40.0, 20.0, 60.0})
	assertNumericColumn(t, result, "column_running", []any{10.0, 40.0, 20.0, 60.0})
}

func TestApplyVisualCalculationsKeepsNullOrderValuesLastAndNullSourcesNonPoisoning(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "period", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Nullable: true, Label: "Period"},
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Nullable: true, Label: "Value"},
		}}},
		Calculations: calculationList(ir.VisualizationCalculation{
			ID: "running", Label: "Running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
			Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows,
			OrderBy: []ir.VisualizationCalculationOrder{{Field: fieldRef("period"), Direction: ir.VisualizationSortDirectionAscending}},
		}),
	}
	frame := Frame{
		Columns: []string{"period", "value"},
		Rows: [][]any{
			{nil, 5.0},
			{"Q2", 20.0},
			{"Q1", nil},
		},
	}

	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatalf("ApplyVisualCalculations(): %v", err)
	}
	assertNumericColumn(t, result, "running", []any{25.0, 20.0, nil})
}

func calculation(id string, template ir.VisualizationCalculationTemplate, source string) ir.VisualizationCalculation {
	return ir.VisualizationCalculation{
		ID: id, Label: id, Dataset: "primary", Template: template, Source: fieldRef(source),
		Axis: ir.VisualizationCalculationAxisRows,
	}
}

func fieldRef(field string) ir.VisualizationFieldRef {
	return ir.VisualizationFieldRef{Dataset: "primary", Field: field}
}

func fieldRefPointer(field string) *ir.VisualizationFieldRef {
	ref := fieldRef(field)
	return &ref
}

func int64Pointer(value int64) *int64 {
	return &value
}

func calculationList(values ...ir.VisualizationCalculation) *[]ir.VisualizationCalculation {
	return &values
}

func assertNumericColumn(t *testing.T, frame Frame, column string, want []any) {
	t.Helper()
	index := -1
	for candidate, name := range frame.Columns {
		if name == column {
			index = candidate
			break
		}
	}
	if index < 0 {
		t.Fatalf("missing column %q in %#v", column, frame.Columns)
	}
	if len(frame.Rows) != len(want) {
		t.Fatalf("%s row count = %d, want %d", column, len(frame.Rows), len(want))
	}
	for rowIndex, expected := range want {
		actual := frame.Rows[rowIndex][index]
		if expected == nil {
			if actual != nil {
				t.Fatalf("%s[%d] = %#v, want nil", column, rowIndex, actual)
			}
			continue
		}
		actualNumber, ok := calculationNumber(actual)
		if !ok {
			t.Fatalf("%s[%d] = %#v (%T), want number", column, rowIndex, actual, actual)
		}
		expectedNumber, ok := calculationNumber(expected)
		if !ok {
			t.Fatalf("%s[%d] expected %#v (%T) is not numeric", column, rowIndex, expected, expected)
		}
		if math.Abs(actualNumber-expectedNumber) > 1e-9 {
			t.Fatalf("%s[%d] = %v, want %v", column, rowIndex, actualNumber, expectedNumber)
		}
	}
}

func assertStringColumn(t *testing.T, frame Frame, column string, want []string) {
	t.Helper()
	index := -1
	for candidate, name := range frame.Columns {
		if name == column {
			index = candidate
			break
		}
	}
	if index < 0 {
		t.Fatalf("missing column %q in %#v", column, frame.Columns)
	}
	if len(frame.Rows) != len(want) {
		t.Fatalf("%s row count = %d, want %d", column, len(frame.Rows), len(want))
	}
	for rowIndex, expected := range want {
		if actual := frame.Rows[rowIndex][index]; actual != expected {
			t.Fatalf("%s[%d] = %#v, want %q", column, rowIndex, actual, expected)
		}
	}
}

func assertAnyColumn(t *testing.T, frame Frame, column string, want []any) {
	t.Helper()
	index := -1
	for candidate, name := range frame.Columns {
		if name == column {
			index = candidate
			break
		}
	}
	if index < 0 {
		t.Fatalf("missing column %q in %#v", column, frame.Columns)
	}
	if len(frame.Rows) != len(want) {
		t.Fatalf("%s row count = %d, want %d", column, len(frame.Rows), len(want))
	}
	for rowIndex, expected := range want {
		if actual := frame.Rows[rowIndex][index]; actual != expected {
			t.Fatalf("%s[%d] = %#v (%T), want %#v (%T)", column, rowIndex, actual, actual, expected, expected)
		}
	}
}

func BenchmarkApplyVisualCalculationsLargeFacetedFrame(b *testing.B) {
	const rowCount = 100_000
	rows := make([][]any, rowCount)
	for index := range rows {
		rows[index] = []any{index % 25, index, float64(index%1000) + 1}
	}
	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "facet", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeInteger, Label: "Facet"},
			{ID: "position", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeInteger, Label: "Position"},
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Label: "Value"},
		}}},
		Calculations: calculationList(
			ir.VisualizationCalculation{
				ID: "running", Label: "Running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisFacets,
				PartitionBy: []ir.VisualizationFieldRef{fieldRef("facet")},
				OrderBy:     []ir.VisualizationCalculationOrder{{Field: fieldRef("position"), Direction: ir.VisualizationSortDirectionAscending}},
			},
			ir.VisualizationCalculation{
				ID: "share", Label: "Share", Dataset: "primary", Template: ir.VisualizationCalculationTemplatePercentOfGrandTotal,
				Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisFacets,
				PartitionBy: []ir.VisualizationFieldRef{fieldRef("facet")},
			},
		),
	}
	frame := Frame{Columns: []string{"facet", "position", "value"}, Rows: rows, Completeness: ir.VisualizationCompletenessComplete}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete); err != nil {
			b.Fatal(err)
		}
	}
}
