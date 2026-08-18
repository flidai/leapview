package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestSemanticFiltersTranslateConformedAndDatasetLocalSelections(t *testing.T) {
	report, model := selectionFilterFixture()
	runtime := &modelRuntime{model: model}
	service := &FilterService{}

	for _, test := range []struct {
		name      string
		selection dashboard.InteractionSelection
		wantField string
		wantDataset  string
		wantValue any
		wantOp    string
	}{
		{
			name:      "conformed propagates without dataset",
			selection: filterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: "1990s"}),
			wantField: "release_decade", wantValue: "1990s", wantOp: "equals",
		},
		{
			name:      "local remains dataset scoped",
			selection: filterSelection("buckets", dashboard.InteractionSelectionMapping{Field: "ratings.rating_bucket", Dataset: "ratings", Value: "5"}),
			wantField: "ratings.rating_bucket", wantDataset: "ratings", wantValue: "5", wantOp: "equals",
		},
		{
			name:      "null uses is null",
			selection: filterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: nil}),
			wantField: "release_decade", wantOp: "is_null",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			filters, err := service.semanticFilters(context.Background(), runtime, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{test.selection}}, "visual", "cross")
			if err != nil {
				t.Fatal(err)
			}
			if len(filters) != 1 {
				t.Fatalf("filters = %#v", filters)
			}
			got := filters[0]
			if got.Field != test.wantField || got.Dataset != test.wantDataset || got.Operator != test.wantOp {
				t.Fatalf("filter = %#v", got)
			}
			if test.wantOp == "is_null" {
				if len(got.Values) != 0 {
					t.Fatalf("null filter values = %#v", got.Values)
				}
			} else if len(got.Values) != 1 || got.Values[0] != test.wantValue {
				t.Fatalf("filter values = %#v, want %#v", got.Values, test.wantValue)
			}
		})
	}
}

func TestSemanticFiltersTargetGridThroughVisualNamespace(t *testing.T) {
	report, model := selectionFilterFixture()
	source := report.Visualizations["decades"]
	base, err := source.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	base.Interactions[0].Targets = []visualizationir.VisualizationInteractionTarget{{VisualID: "plain_table", Effect: visualizationir.VisualizationInteractionEffectFilter}}
	report.Visualizations["decades"] = source

	selection := filterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: "1990s"})
	filters, err := (&FilterService{}).semanticFilters(
		context.Background(),
		&modelRuntime{model: model},
		report,
		dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}},
		"visual",
		"plain_table",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 1 || filters[0].Field != "release_decade" {
		t.Fatalf("grid visual filters = %#v, want conformed selection", filters)
	}
}

func TestSemanticFiltersKeepHighlightAndNoneEdgesOutOfTargetQueries(t *testing.T) {
	report, model := selectionFilterFixture()
	selection := filterSelection("decades", dashboard.InteractionSelectionMapping{Field: "release_decade", Value: "1990s"})
	source := report.Visualizations["decades"]
	base, err := source.Spec.Base()
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range []visualizationir.VisualizationInteractionEffect{
		visualizationir.VisualizationInteractionEffectHighlight,
		visualizationir.VisualizationInteractionEffectNone,
	} {
		t.Run(string(effect), func(t *testing.T) {
			base.Interactions[0].Targets = []visualizationir.VisualizationInteractionTarget{{VisualID: "cross", Effect: effect}}
			report.Visualizations["decades"] = source
			filters, err := (&FilterService{}).semanticFilters(
				context.Background(),
				&modelRuntime{model: model},
				report,
				dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}},
				"visual",
				"cross",
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(filters) != 0 {
				t.Fatalf("%s edge mutated target query: %#v", effect, filters)
			}
		})
	}
}

func TestSelectionMappingFiltersBuildHalfOpenRangesForEveryTimeGrain(t *testing.T) {
	for _, test := range []struct {
		grain string
		value string
		start string
		end   string
	}{
		{grain: "day", value: "2026-02-03", start: "2026-02-03", end: "2026-02-04"},
		{grain: "week", value: "2026-02-02", start: "2026-02-02", end: "2026-02-09"},
		{grain: "month", value: "2026-02", start: "2026-02-01", end: "2026-03-01"},
		{grain: "quarter", value: "2026-Q2", start: "2026-04-01", end: "2026-07-01"},
		{grain: "year", value: "2026", start: "2026-01-01", end: "2027-01-01"},
	} {
		t.Run(test.grain, func(t *testing.T) {
			filters, err := selectionMappingFilters(reportmodel.ResolvedSelectionMapping{Field: "activity_date", Grain: test.grain}, test.value)
			if err != nil {
				t.Fatal(err)
			}
			if len(filters) != 2 || filters[0].Operator != "greater_than_or_equal" || filters[1].Operator != "less_than" {
				t.Fatalf("filters = %#v", filters)
			}
			start := filters[0].Values[0].(time.Time).Format(time.DateOnly)
			end := filters[1].Values[0].(time.Time).Format(time.DateOnly)
			if start != test.start || end != test.end {
				t.Fatalf("range = [%s,%s), want [%s,%s)", start, end, test.start, test.end)
			}
		})
	}
}

func TestSemanticFiltersEmitConformedHalfOpenTimeRange(t *testing.T) {
	report, model := selectionFilterFixture()
	selection := filterSelection("months", dashboard.InteractionSelectionMapping{Field: "activity_date", Grain: "month", Value: "2026-02"})
	filters, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 || filters[0].Field != "activity_date" || filters[0].Dataset != "" || filters[1].Dataset != "" {
		t.Fatalf("conformed time filters = %#v", filters)
	}
	if filters[0].Operator != "greater_than_or_equal" || filters[1].Operator != "less_than" {
		t.Fatalf("time operators = %#v", filters)
	}
}

func TestSemanticFiltersPreserveOROfCompositeEntries(t *testing.T) {
	report, model := selectionFilterFixture()
	report.Visualizations["composite"] = compiledSelectionVisual("composite", dashboardauthoring.Visual{
		Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: "release_decade", Alias: "label"}, {Field: "activity_date", Alias: "series"}},
			Metrics:    []dashboardauthoring.FieldRef{{Field: "rating_count", Alias: "value"}},
		},
		Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{
			Mappings: []dashboardauthoring.SelectionMapping{{Field: "release_decade", Value: "label"}, {Field: "activity_date", Value: "series"}},
			Targets:  []string{"cross"},
		}},
	})
	selection := dashboard.InteractionSelection{
		SourceKind: "visual", SourceID: "composite", InteractionKind: "point_selection",
		Entries: []dashboard.InteractionSelectionEntry{
			{Mappings: []dashboard.InteractionSelectionMapping{{Field: "activity_date", Value: "2026-01-01"}, {Field: "release_decade", Value: "1990s"}}},
			{Mappings: []dashboard.InteractionSelectionMapping{{Field: "release_decade", Value: "2000s"}, {Field: "activity_date", Value: "2026-02-01"}}},
		},
	}
	filters, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 1 || len(filters[0].Groups) != 2 || len(filters[0].Groups[0].Filters) != 2 || len(filters[0].Groups[1].Filters) != 2 {
		t.Fatalf("composite OR filters = %#v", filters)
	}
}

func TestSemanticFiltersIgnoreUIOnlyRowSelections(t *testing.T) {
	report, model := selectionFilterFixture()
	selection := dashboard.InteractionSelection{
		SourceKind: "visual", SourceID: "plain_table", InteractionKind: "row_selection",
		Entries: []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{{Field: dashboard.UIRowSelectionField, Value: "row-1"}}}},
	}
	filters, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 0 {
		t.Fatalf("UI-only row filters = %#v, want none", filters)
	}
}

func TestSemanticFiltersRejectStoredSelectionWithOmittedJSONValue(t *testing.T) {
	report, model := selectionFilterFixture()
	var selection dashboard.InteractionSelection
	if err := json.Unmarshal([]byte(`{
		"sourceKind":"visual",
		"sourceId":"decades",
		"interactionKind":"point_selection",
		"entries":[{"mappings":[{"field":"release_decade"}]}]
	}`), &selection); err != nil {
		t.Fatal(err)
	}
	_, err := (&FilterService{}).semanticFilters(context.Background(), &modelRuntime{model: model}, report, dashboard.Filters{Selections: []dashboard.InteractionSelection{selection}}, "visual", "cross")
	if err == nil || !strings.Contains(err.Error(), "must include value") {
		t.Fatalf("semanticFilters() error = %v, want omitted-value rejection", err)
	}
}

func selectionFilterFixture() (*dashboarddefinition.Definition, *semanticmodel.Model) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"ratings": {Dimensions: map[string]semanticmodel.MetricDimension{"rating_bucket": {Type: "string"}, "rated_at": {Type: "timestamp"}, "release_decade": {Type: "string"}}},
			"tags":    {Dimensions: map[string]semanticmodel.MetricDimension{"tagged_at": {Type: "timestamp"}, "release_decade": {Type: "string"}}},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"release_decade": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.release_decade"}, "tags": {Field: "tags.release_decade"}}},
			"activity_date":  {Type: "timestamp", Grains: []string{"day", "week", "month", "quarter", "year"}, Bindings: map[string]semanticmodel.DimensionBinding{"ratings": {Field: "ratings.rated_at"}, "tags": {Field: "tags.tagged_at"}}},
		},
		Metrics: map[string]semanticmodel.Metric{"rating_count": {Type: "aggregate", Dataset: "ratings", Input: &semanticmodel.MetricInput{Field: "ratings.rating_bucket"}}, "tag_count": {Type: "aggregate", Dataset: "tags", Input: &semanticmodel.MetricInput{Field: "tags.tagged_at"}}},
	}
	report := &dashboardauthoring.Dashboard{
		Visuals: dashboardauthoring.MergeVisualizations(dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"decades": selectionFilterVisual([]dashboardauthoring.FieldRef{{Field: "release_decade", Alias: "label"}}, dashboardauthoring.QueryTime{}, []dashboardauthoring.SelectionMapping{{Field: "release_decade", Value: "label"}}),
			"buckets": selectionFilterVisual([]dashboardauthoring.FieldRef{{Field: "ratings.rating_bucket", Alias: "label"}}, dashboardauthoring.QueryTime{}, []dashboardauthoring.SelectionMapping{{Field: "ratings.rating_bucket", Dataset: "ratings", Value: "label"}}),
			"months":  selectionFilterVisual(nil, dashboardauthoring.QueryTime{Field: "activity_date", Grain: "month", Alias: "label"}, []dashboardauthoring.SelectionMapping{{Field: "activity_date", Grain: "month", Value: "label"}}),
			"cross":   {Query: dashboardauthoring.VisualQuery{Metrics: []dashboardauthoring.FieldRef{{Field: "rating_count"}, {Field: "tag_count"}}}},
		}), dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{"plain_table": {Query: dashboardauthoring.TableQuery{Dataset: "ratings"}}})),
	}
	return compiledSelectionDashboard(report), model
}

func compiledSelectionDashboard(authored *dashboardauthoring.Dashboard) *dashboarddefinition.Definition {
	visualizations := map[string]visualizationdefinition.Definition{}
	for id, visual := range authored.Visuals {
		if visual.Chart != nil {
			visualizations[id] = compiledSelectionVisual(id, *visual.Chart)
		} else if visual.Tabular != nil {
			visualizations[id] = visualizationdefinition.Definition{ID: id, Query: visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, Detail: &visualizationdefinition.DetailQueryBinding{TableID: visual.Tabular.Query.Dataset}}}
		}
	}
	return &dashboarddefinition.Definition{Visualizations: visualizations}
}

func compiledSelectionVisual(id string, authored dashboardauthoring.Visual) visualizationdefinition.Definition {
	dimensions := make([]visualizationdefinition.FieldBinding, len(authored.Query.Dimensions))
	fields := make([]visualizationir.VisualizationField, 0, len(dimensions)+1)
	for index, field := range authored.Query.Dimensions {
		dimensions[index] = visualizationdefinition.FieldBinding{FieldID: field.Field, Alias: field.Alias}
		fields = append(fields, visualizationir.VisualizationField{ID: field.Alias, Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: field.Alias})
	}
	var timeBinding *visualizationdefinition.TimeBinding
	if authored.Query.Time.Field != "" {
		timeBinding = &visualizationdefinition.TimeBinding{FieldID: authored.Query.Time.Field, Alias: authored.Query.Time.Alias, Grain: authored.Query.Time.Grain}
		fields = append(fields, visualizationir.VisualizationField{ID: authored.Query.Time.Alias, Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: authored.Query.Time.Alias})
	}
	metrics := make([]visualizationdefinition.FieldBinding, len(authored.Query.Metrics))
	for index, field := range authored.Query.Metrics {
		alias := field.Alias
		if alias == "" {
			alias = "value"
		}
		metrics[index] = visualizationdefinition.FieldBinding{FieldID: field.Field, Alias: alias}
	}
	interactions := []visualizationir.VisualizationInteraction{}
	if selection := authored.Interaction.PointSelection; len(selection.Mappings) > 0 {
		mappings := make([]visualizationir.VisualizationInteractionMapping, len(selection.Mappings))
		for index, mapping := range selection.Mappings {
			item := visualizationir.VisualizationInteractionMapping{Source: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: mapping.Value}, TargetFieldID: mapping.Field}
			if mapping.Dataset != "" {
				item.TargetDatasetID = &mapping.Dataset
			}
			if mapping.Grain != "" {
				item.Grain = &mapping.Grain
			}
			mappings[index] = item
		}
		targets := make([]visualizationir.VisualizationInteractionTarget, len(selection.Targets))
		for index, target := range selection.Targets {
			targets[index] = visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectFilter}
		}
		interactions = append(interactions, visualizationir.VisualizationInteraction{ID: "point_selection", Kind: visualizationir.VisualizationInteractionKindSelect, Mappings: mappings, Targets: targets, Mode: visualizationir.VisualizationSelectionModeSingle, RequiresStableIdentity: true})
	}
	base := visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: id, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, Interactions: interactions}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian"}}
	return visualizationdefinition.Definition{ID: id, Spec: spec, Query: visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, Aggregate: &visualizationdefinition.AggregateQueryBinding{Dimensions: dimensions, Metrics: metrics, Time: timeBinding, Limit: 100}}}
}

func selectionFilterVisual(dimensions []dashboardauthoring.FieldRef, queryTime dashboardauthoring.QueryTime, mappings []dashboardauthoring.SelectionMapping) dashboardauthoring.Visual {
	return dashboardauthoring.Visual{
		Query:       dashboardauthoring.VisualQuery{Dimensions: dimensions, Time: queryTime, Metrics: []dashboardauthoring.FieldRef{{Field: "rating_count"}}},
		Interaction: dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{Mappings: mappings, Targets: []string{"cross"}}},
	}
}

func filterSelection(source string, mapping dashboard.InteractionSelectionMapping) dashboard.InteractionSelection {
	return dashboard.InteractionSelection{SourceKind: "visual", SourceID: source, InteractionKind: "point_selection", Entries: []dashboard.InteractionSelectionEntry{{Mappings: []dashboard.InteractionSelectionMapping{mapping}}}}
}
