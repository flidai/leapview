package runtime

import (
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestApplyVisualCalculationsKeepsCompositeStringPartitionsDistinct(t *testing.T) {
	t.Parallel()
	base := ir.VisualizationSpecBase{
		Kind: "cartesian",
		Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
			{ID: "group", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString},
			{ID: "subgroup", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString},
			{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal},
		}}},
		Calculations: calculationList(ir.VisualizationCalculation{
			ID: "running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
			Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisRows,
			PartitionBy: []ir.VisualizationFieldRef{fieldRef("group"), fieldRef("subgroup")},
		}),
	}
	frame := Frame{Columns: []string{"group", "subgroup", "value"}, Rows: [][]any{
		{"a\x00s:b", "c", 1.0},
		{"a", "b\x00s:c", 10.0},
		{"a\x00s:b", "c", 2.0},
		{"a", "b\x00s:c", 20.0},
	}}
	result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
	if err != nil {
		t.Fatal(err)
	}
	assertNumericColumn(t, result, "running", []any{1.0, 10.0, 3.0, 30.0})
}

// Keep the row count fixed to expose work that grows with partition count.
func BenchmarkVisualCalculationPartitionScaling(b *testing.B) {
	const rowCount = 10_000
	for _, partitions := range []int{1, 25, 250} {
		b.Run(fmt.Sprintf("partitions_%d", partitions), func(b *testing.B) {
			rows := make([][]any, rowCount)
			for index := range rows {
				rows[index] = []any{index % partitions, index, float64(index%1000) + 1}
			}
			base := ir.VisualizationSpecBase{
				Kind: "cartesian",
				Datasets: []ir.VisualizationDatasetSchema{{ID: "primary", Fields: []ir.VisualizationField{
					{ID: "facet", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeInteger},
					{ID: "position", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeInteger},
					{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal},
				}}},
				Calculations: calculationList(ir.VisualizationCalculation{
					ID: "running", Dataset: "primary", Template: ir.VisualizationCalculationTemplateRunningTotal,
					Source: fieldRef("value"), Axis: ir.VisualizationCalculationAxisFacets,
					PartitionBy: []ir.VisualizationFieldRef{fieldRef("facet")},
					OrderBy:     []ir.VisualizationCalculationOrder{{Field: fieldRef("position"), Direction: ir.VisualizationSortDirectionAscending}},
				}),
			}
			frame := Frame{Columns: []string{"facet", "position", "value"}, Rows: rows}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				result, _, err := ApplyVisualCalculations(base, "primary", frame, ir.VisualizationCompletenessComplete)
				if err != nil || len(result.Rows) != rowCount {
					b.Fatalf("invalid calculation result: rows=%d err=%v", len(result.Rows), err)
				}
			}
		})
	}
}
