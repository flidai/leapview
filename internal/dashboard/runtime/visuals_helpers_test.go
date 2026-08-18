package runtime

import (
	"reflect"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestAggregateMemberMetadataResolvesMetricPresentation(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"tags_per_rating": {Label: "Tags per rating", Unit: "ratio", Format: "decimal"},
	}}
	got := aggregateMemberMetadata(model, "tags_per_rating")
	if got.Label != "Tags per rating" || got.Unit != "ratio" || got.Format != "decimal" {
		t.Fatalf("metric metadata = %#v", got)
	}
}

func TestCategoryMultiMeasureDatumsDecodesBundledWideRows(t *testing.T) {
	runtime := &modelRuntime{model: &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"rating_count": {Label: "Ratings"},
		"tag_count":    {Label: "Tags"},
	}}}
	visual := visualPlan{Metrics: []visualizationdefinition.FieldBinding{{FieldID: "rating_count"}, {FieldID: "tag_count"}}}
	rows := []dashboard.Datum{{"label": "2024-01-01", "value_0": int64(8), "value_1": int64(3)}}
	got := categoryMultiMeasureDatums(runtime, visual, rows)
	want := []dashboard.Datum{
		{"label": "2024-01-01", "series": "Ratings", "value": int64(8)},
		{"label": "2024-01-01", "series": "Tags", "value": int64(3)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("datums = %#v, want %#v", got, want)
	}
}

func TestCrossTabValueFieldsPreservesCalculationDataType(t *testing.T) {
	dataType := visualizationir.VisualizationDataTypeFloat
	base := visualizationir.VisualizationSpecBase{
		Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{
			ID: "growth", Role: visualizationir.VisualizationFieldRoleMetric, DataType: dataType, Label: "Growth",
		}}}},
		Calculations: &[]visualizationir.VisualizationCalculation{{
			ID: "growth", Dataset: "primary", Label: "Growth", Template: visualizationir.VisualizationCalculationTemplateDifference,
		}},
	}
	table := tablePlan{Definition: visualizationdefinition.Definition{Query: visualizationdefinition.QueryBinding{DatasetID: "primary"}}}
	fields := crossTabValueFields(table, base, nil)
	if len(fields) != 1 || fields[0].dataType != dataType {
		t.Fatalf("calculation value fields = %#v, want Float", fields)
	}
}
