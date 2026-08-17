package runtime

import (
	"testing"

	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestEnvelopeFromFramesOrdersAndValidatesContextDatasets(t *testing.T) {
	t.Parallel()

	base := ir.VisualizationSpecBase{
		Kind: "cartesian", Title: "Revenue",
		Datasets: []ir.VisualizationDatasetSchema{
			{ID: "primary", Fields: []ir.VisualizationField{
				{ID: "label", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Label: "Month"},
				{ID: "value", Role: ir.VisualizationFieldRoleMetric, DataType: ir.VisualizationDataTypeDecimal, Label: "Revenue"},
			}},
			{ID: "context", Fields: []ir.VisualizationField{
				{ID: "region", Role: ir.VisualizationFieldRoleDimension, DataType: ir.VisualizationDataTypeString, Label: "Region"},
			}},
		},
		DataBudget:    ir.VisualizationDataBudget{MaxRows: 100, RequiredCompleteness: ir.VisualizationCompletenessComplete},
		Accessibility: ir.VisualizationAccessibility{Title: "Revenue", Description: "Revenue by month"},
		Interactions:  []ir.VisualizationInteraction{},
	}
	spec := ir.VisualizationSpec{Value: &ir.CartesianVisualizationSpec{
		VisualizationSpecBase: base, Kind: "cartesian", Mark: ir.VisualizationCartesianMarkLine,
		X: ir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: []ir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}},
		Presentation: ir.CartesianVisualizationPresentation{
			VisualizationPresentation: ir.VisualizationPresentation{
				Legend: ir.VisualizationLegendPositionBottom,
				LabelPolicy: ir.VisualizationLabelPolicy{
					Density: ir.VisualizationLabelDensityHidden, Priority: []ir.VisualizationLabelPriority{},
					MaxCharacters: 24, MinimumSpacing: 0, TooltipFallback: true,
				},
			}, ShowSymbols: true,
		},
	}}
	primary := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "sales", DatasetID: "primary",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{
			Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "month", Alias: "label"}},
			Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "value"}}, Limit: 100,
		},
	}
	context := visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryMultiMeasure, ModelID: "sales", DatasetID: "context",
		Aggregate: &visualizationdefinition.AggregateQueryBinding{
			Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "region", Alias: "region"}}, Limit: 1,
		},
	}
	definition, err := visualizationdefinition.NewWithSecondaryQueries("revenue", spec, primary, map[string]visualizationdefinition.QueryBinding{"context": context})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := EnvelopeFromFrames(definition, map[string]Frame{
		"context": {Columns: []string{"region"}, Rows: [][]any{{"EMEA"}}},
		"primary": {Columns: []string{"label", "value"}, Rows: [][]any{{"Jan", 42.0}}},
	}, nil, 4, 2)
	if err != nil {
		t.Fatalf("EnvelopeFromFrames(): %v", err)
	}
	state := envelope.DataState.Value.(*ir.InlineVisualizationDataState)
	if len(state.Datasets) != 2 || state.Datasets[0].ID != "primary" || state.Datasets[1].ID != "context" {
		t.Fatalf("datasets = %#v", state.Datasets)
	}
	if _, err := EnvelopeFromFrames(definition, map[string]Frame{
		"primary": {Columns: []string{"label", "value"}},
	}, nil, 4, 2); err == nil {
		t.Fatal("EnvelopeFromFrames() accepted missing context dataset")
	}
	empty, err := EnvelopeFromFrames(definition, map[string]Frame{
		"primary": {Columns: []string{"label", "value"}},
		"context": {Columns: []string{"region"}, Rows: [][]any{{"EMEA"}}},
	}, nil, 5, 3)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status.Kind != ir.VisualizationStatusKindNoData {
		t.Fatalf("status = %q, want no_data from empty primary dataset", empty.Status.Kind)
	}

	initial, err := EmptyEnvelopeFromDefinition(definition, 1, 1, 0)
	if err != nil {
		t.Fatalf("EmptyEnvelopeFromDefinition(): %v", err)
	}
	initialState := initial.DataState.Value.(*ir.InlineVisualizationDataState)
	if len(initialState.Datasets) != 2 || initialState.Datasets[0].ID != "primary" || initialState.Datasets[1].ID != "context" {
		t.Fatalf("initial datasets = %#v", initialState.Datasets)
	}
	if got := initialState.Datasets[1].Columns; len(got) != 1 || got[0] != "region" {
		t.Fatalf("initial context columns = %#v, want [region]", got)
	}
	if initialState.Datasets[0].Completeness != ir.VisualizationCompletenessEmpty ||
		initialState.Datasets[1].Completeness != ir.VisualizationCompletenessEmpty {
		t.Fatalf("initial completeness = %#v", initialState.Datasets)
	}
}
