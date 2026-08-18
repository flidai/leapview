package definition

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestNewWithSecondaryQueriesOwnsEveryDataset(t *testing.T) {
	t.Parallel()

	spec := ir.VisualizationSpec{Value: &ir.CartesianVisualizationSpec{
		VisualizationSpecBase: ir.VisualizationSpecBase{
			Kind: "cartesian", Title: "Revenue",
			Datasets: []ir.VisualizationDatasetSchema{
				{ID: "primary", Fields: []ir.VisualizationField{
					{ID: "label", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Label: "Month"},
					{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Label: "Revenue"},
				}},
				{ID: "context", Fields: []ir.VisualizationField{
					{ID: "region", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Label: "Region"},
					{ID: "target", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Label: "Target"},
				}},
			},
			DataBudget:    ir.VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: ir.VisualizationCompletenessComplete},
			Accessibility: ir.VisualizationAccessibility{Title: "Revenue", Description: "Revenue by month"},
			Interactions:  []ir.VisualizationInteraction{},
		},
		Kind: "cartesian", Mark: ir.VisualizationCartesianMarkLine,
		X: ir.VisualizationFieldRef{Dataset: "primary", Field: "label"},
		Y: []ir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
		Presentation: ir.CartesianVisualizationPresentation{
			VisualizationPresentation: ir.VisualizationPresentation{
				Legend: ir.VisualizationLegendPositionBottom,
				LabelPolicy: ir.VisualizationLabelPolicy{
					Density: ir.VisualizationLabelDensityHidden, Priority: []ir.VisualizationLabelPriority{},
					MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true,
				},
			},
			ShowSymbols: true,
		},
	}}
	primary := QueryBinding{
		Kind: QueryAggregate, ResultShape: ResultCategoryValue, ModelID: "sales", DatasetID: "primary",
		Aggregate: &AggregateQueryBinding{
			Dimensions: []FieldBinding{{FieldID: "orders.month", Alias: "label"}},
			Metrics:    []FieldBinding{{FieldID: "revenue", Alias: "value"}}, Limit: 100,
		},
	}
	context := QueryBinding{
		Kind: QueryAggregate, ResultShape: ResultCategoryMultiMeasure, ModelID: "sales", DatasetID: "context",
		Aggregate: &AggregateQueryBinding{
			Dimensions: []FieldBinding{{FieldID: "orders.region", Alias: "region"}},
			Metrics:    []FieldBinding{{FieldID: "target_revenue", Alias: "target"}}, Limit: 1,
		},
	}
	if _, err := NewWithSecondaryQueries("revenue", spec, primary, map[string]QueryBinding{"context": context}); err != nil {
		t.Fatalf("NewWithSecondaryQueries(): %v", err)
	}

	tests := []struct {
		name        string
		secondaries map[string]QueryBinding
		want        string
	}{
		{"missing query", nil, `dataset "context" has no compiled query`},
		{"mismatched key", map[string]QueryBinding{"other": context}, `key "other" does not match dataset "context"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewWithSecondaryQueries("revenue", spec, primary, test.secondaries)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}
