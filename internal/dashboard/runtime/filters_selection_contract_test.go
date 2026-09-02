package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalSelectionFixture() (*dashboarddefinition.Definition, *semanticmodel.Model) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"ratings": {Dimensions: map[string]semanticmodel.MetricDimension{"rating_bucket": {Type: "string", Datatype: semanticmodel.DataTypeString}, "rated_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTime}, "release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString}}},
			"tags":    {Dimensions: map[string]semanticmodel.MetricDimension{"tagged_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTime}, "release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString}}},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"release_decade": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.release_decade"}, "tags": {Field: "tags.release_decade"}}},
			"activity_date":  {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTime, Grains: []string{"day", "week", "month", "quarter", "year"}, Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.rated_at"}, "tags": {Field: "tags.tagged_at"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"rating_count": {Type: "aggregate", Dataset: "ratings", Input: &semanticmodel.MetricInput{Field: "ratings.rating_bucket"}}, "tag_count": {Type: "aggregate", Dataset: "tags", Input: &semanticmodel.MetricInput{Field: "tags.tagged_at"}}},
	}
	visuals := map[string]visualizationdefinition.Definition{
		"decades":     canonicalSelectionVisual("decades", "release_decade", "", "release_decade", "", "cross"),
		"buckets":     canonicalSelectionVisual("buckets", "ratings.rating_bucket", "ratings", "ratings.rating_bucket", "", "cross"),
		"months":      canonicalSelectionVisual("months", "activity_date", "", "activity_date", "month", "cross"),
		"cross":       canonicalSelectionVisual("cross", "", "", "", "", ""),
		"plain_table": {ID: "plain_table", Spec: visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "table", Title: "Table", Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "row", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString}}}}, Accessibility: visualizationir.VisualizationAccessibility{Title: "Table", Description: "Table"}}, Kind: "table", Columns: []visualizationir.TableVisualizationColumn{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "row"}, Label: "Row", Formatting: []visualizationir.TableVisualizationFormattingRule{}}}}}, Query: visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, Detail: &visualizationdefinition.DetailQueryBinding{TableID: "ratings", Fields: []visualizationdefinition.FieldBinding{{FieldID: "row", Alias: "row"}}, Limit: 100}}},
	}
	return &dashboarddefinition.Definition{Visualizations: visuals}, model
}

func canonicalSelectionVisual(id, field, dataset, targetField, grain, target string) visualizationdefinition.Definition {
	fields := []visualizationir.VisualizationField{}
	dimensions := []visualizationdefinition.FieldBinding{}
	refs := []visualizationir.VisualizationFieldRef{}
	if field != "" {
		alias := "label"
		if grain != "" {
			alias = "label"
		}
		fields = append(fields, visualizationir.VisualizationField{ID: alias, Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: alias})
		dimensions = append(dimensions, visualizationdefinition.FieldBinding{FieldID: field, Alias: alias})
		refs = append(refs, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: alias})
	}
	fields = append(fields, visualizationir.VisualizationField{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"})
	base := visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: id, Accessibility: visualizationir.VisualizationAccessibility{Title: id, Description: id}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}}
	if targetField != "" {
		mapping := visualizationir.VisualizationInteractionMapping{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, TargetFieldID: targetField}
		if dataset != "" {
			mapping.TargetDatasetID = &dataset
		}
		if grain != "" {
			mapping.Grain = &grain
		}
		targets := []visualizationir.VisualizationInteractionTarget{}
		if target != "" {
			targets = append(targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectFilter})
		}
		base.Interactions = []visualizationir.VisualizationInteraction{{ID: "interaction-0", Kind: visualizationir.VisualizationInteractionKindSelect, Mode: visualizationir.VisualizationSelectionModeSingle, RequiresStableIdentity: false, Mappings: []visualizationir.VisualizationInteractionMapping{mapping}, Targets: targets}}
	}
	if len(refs) == 0 {
		refs = []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}
	}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkBar, X: refs[0], Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	return visualizationdefinition.Definition{ID: id, Spec: spec, Query: visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "ratings", Dimensions: dimensions, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "rating_count", Alias: "value"}}, Time: func() *visualizationdefinition.TimeBinding {
		if grain == "" {
			return nil
		}
		return &visualizationdefinition.TimeBinding{FieldID: field, Alias: "label", Grain: grain}
	}(), Limit: 100}}}
}

func canonicalFilterSelection(source string, mapping dashboard.InteractionSelectionMapping) dashboard.InteractionSelection {
	return dashboard.InteractionSelection{SourceKind: "visual", SourceID: source, InteractionKind: "interaction-0", Entries: []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{mapping}}}}
}

func TestCanonicalSemanticFiltersTranslateConformedAndDatasetLocalSelections(t *testing.T) {
	report, model := canonicalSelectionFixture()
	service := &FilterService{}
	for _, test := range []struct {
		name           string
		selection      dashboard.InteractionSelection
		field, dataset string
		value          any
		operator       string
	}{
		{name: "conformed", selection: canonicalFilterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: "1990s"}), field: "release_decade", value: "1990s", operator: "equals"},
		{name: "local", selection: canonicalFilterSelection("buckets", dashboard.InteractionSelectionMapping{Field: "ratings.rating_bucket", Dataset: "ratings", Value: "5"}), field: "ratings.rating_bucket", dataset: "ratings", value: "5", operator: "equals"},
		{name: "null", selection: canonicalFilterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: nil}), field: "release_decade", operator: "is_null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			filters, err := service.semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{test.selection}}, "visual", "cross")
			if err != nil || len(filters) != 1 {
				t.Fatalf("filters=%#v err=%v", filters, err)
			}
			got := filters[0]
			if got.Field != test.field || got.Dataset != test.dataset || got.Operator != test.operator {
				t.Fatalf("filter=%#v", got)
			}
			if test.operator == "is_null" {
				if len(got.Values) != 0 {
					t.Fatalf("null values=%#v", got.Values)
				}
			} else if len(got.Values) != 1 || got.Values[0] != test.value {
				t.Fatalf("values=%#v", got.Values)
			}
		})
	}
}

func TestCanonicalSelectionMappingFiltersBuildHalfOpenRangesForEveryTimeGrain(t *testing.T) {
	for _, test := range []struct{ grain, value, start, end string }{{"day", "2026-02-03", "2026-02-03", "2026-02-04"}, {"week", "2026-02-02", "2026-02-02", "2026-02-09"}, {"month", "2026-02", "2026-02-01", "2026-03-01"}, {"quarter", "2026-Q2", "2026-04-01", "2026-07-01"}, {"year", "2026", "2026-01-01", "2027-01-01"}} {
		t.Run(test.grain, func(t *testing.T) {
			filters, err := selectionMappingFilters(reportmodel.ResolvedSelectionMapping{Field: "activity_date", Grain: test.grain}, test.value)
			if err != nil || len(filters) != 2 || filters[0].Operator != "greater_than_or_equal" || filters[1].Operator != "less_than" {
				t.Fatalf("filters=%#v err=%v", filters, err)
			}
			if got := filters[0].Values[0].(time.Time).Format(time.DateOnly); got != test.start {
				t.Fatalf("start=%s", got)
			}
			if got := filters[1].Values[0].(time.Time).Format(time.DateOnly); got != test.end {
				t.Fatalf("end=%s", got)
			}
		})
	}
}

func TestCanonicalSemanticFiltersEmitConformedHalfOpenTimeRange(t *testing.T) {
	report, model := canonicalSelectionFixture()
	selection := canonicalFilterSelection("months", dashboard.InteractionSelectionMapping{Field: "activity_date", Grain: "month", Value: "2026-02"})
	filters, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err != nil || len(filters) != 2 || filters[0].Field != "activity_date" || filters[0].Dataset != "" || filters[1].Dataset != "" || filters[0].Operator != "greater_than_or_equal" || filters[1].Operator != "less_than" {
		t.Fatalf("time filters=%#v err=%v", filters, err)
	}
}

func TestCanonicalSemanticFiltersIgnoreUIOnlyRowSelections(t *testing.T) {
	report, model := canonicalSelectionFixture()
	selection := dashboard.InteractionSelection{SourceKind: "visual", SourceID: "plain_table", InteractionKind: "row_selection", Entries: []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{{Field: dashboard.UIRowSelectionField, Value: "row-1"}}}}}
	filters, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err != nil || len(filters) != 0 {
		t.Fatalf("UI-only filters=%#v err=%v", filters, err)
	}
}

func TestCanonicalSemanticFiltersRejectSelectionWhoseIDDoesNotMatchCompiledInteraction(t *testing.T) {
	report, model := canonicalSelectionFixture()
	selection := canonicalFilterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: "1990s"})
	selection.InteractionKind = "point_selection"
	_, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err == nil || !strings.Contains(err.Error(), `has no selection interaction "point_selection"`) {
		t.Fatalf("error = %v, want compiled interaction ID rejection", err)
	}
}

func TestCanonicalSemanticFiltersRejectStoredSelectionWithOmittedJSONValue(t *testing.T) {
	report, model := canonicalSelectionFixture()
	var selection dashboard.InteractionSelection
	if err := json.Unmarshal([]byte(`{"sourceKind":"visual","sourceId":"decades","interactionKind":"interaction-0","entries":[{"mappings":[{"field":"release_decade"}]}]}`), &selection); err != nil {
		t.Fatal(err)
	}
	_, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err == nil || !strings.Contains(err.Error(), "must include value") {
		t.Fatalf("error=%v", err)
	}
}

func TestCanonicalSemanticFiltersORAdditiveAreasFromOneMapAndANDSeparateMaps(t *testing.T) {
	model := &semanticmodel.Model{
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"latitude":  {Type: "number", Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.latitude"}}},
			"longitude": {Type: "number", Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.longitude"}}},
		},
	}
	spatialInteraction := visualizationir.VisualizationSpatialSelectionInteraction{
		ID:        "spatial_selection",
		Latitude:  visualizationir.VisualizationSpatialFieldMapping{TargetFieldID: "latitude"},
		Longitude: visualizationir.VisualizationSpatialFieldMapping{TargetFieldID: "longitude"},
		Targets:   []visualizationir.VisualizationInteractionTarget{{VisualID: "orders", Effect: visualizationir.VisualizationInteractionEffectFilter}},
	}
	mapVisual := func(id string) visualizationdefinition.Definition {
		return visualizationdefinition.Definition{ID: id, Spec: visualizationir.VisualizationSpec{Value: &visualizationir.GeographicVisualizationSpec{
			VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "geographic", Title: id},
			Kind:                  "geographic", SpatialInteractions: []visualizationir.VisualizationSpatialSelectionInteraction{spatialInteraction},
		}}}
	}
	report := &dashboarddefinition.Definition{Visualizations: map[string]visualizationdefinition.Definition{
		"map-a": mapVisual("map-a"), "map-b": mapVisual("map-b"), "orders": {ID: "orders"},
	}}
	box := visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialBoxSelection{
		VisualizationSpatialSelectionGeometryBase: visualizationir.VisualizationSpatialSelectionGeometryBase{Kind: "box"},
		Kind: "box", Bounds: visualizationir.VisualizationSpatialBounds{West: -50, South: -25, East: -40, North: -15},
	}}
	radius := visualizationir.VisualizationSpatialSelectionGeometry{Value: &visualizationir.VisualizationSpatialRadiusSelection{
		VisualizationSpatialSelectionGeometryBase: visualizationir.VisualizationSpatialSelectionGeometryBase{Kind: "radius"},
		Kind: "radius", Center: visualizationir.VisualizationSpatialCoordinate{Longitude: -46, Latitude: -23}, RadiusMeters: 10_000,
	}}
	filters, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{SpatialSelections: []dashboard.SpatialInteractionSelection{
		{VisualID: "map-a", InteractionID: "spatial_selection", Geometry: box},
		{VisualID: "map-a", InteractionID: "spatial_selection", Geometry: radius},
		{VisualID: "map-b", InteractionID: "spatial_selection", Geometry: box},
	}}, "visual", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 || len(filters[0].Groups) != 2 || filters[1].Spatial == nil {
		t.Fatalf("spatial filters = %#v, want one two-area OR group AND one separate-map predicate", filters)
	}
	if filters[0].Groups[0].Filters[0].Spatial.Kind != "box" || filters[0].Groups[1].Filters[0].Spatial.Kind != "radius" {
		t.Fatalf("additive area group = %#v", filters[0].Groups)
	}
}
