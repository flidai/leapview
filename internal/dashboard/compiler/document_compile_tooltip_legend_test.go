package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCanonicalDashboardTooltipLowersCompactAndConfiguredItems(t *testing.T) {
	label := "Amount"
	minimum, maximum := int32(1), int32(2)
	values := []document.DashboardTooltip{
		{String: tooltipStringPtr("category")},
		{Item: &document.DashboardTooltipItem{Field: "value", Label: &label, Format: &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}}}},
	}
	visual := document.DashboardVisual{Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Tooltip: &values}}}
	query := LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{{Name: "category"}, {Name: "value"}}}
	fields := []visualizationir.VisualizationField{{ID: "category", DataType: visualizationir.VisualizationDataTypeString, Label: "Category"}, {ID: "value", DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	items, refs, err := canonicalDashboardTooltip(visual, query, fields)
	if err != nil {
		t.Fatalf("canonicalDashboardTooltip() error = %v", err)
	}
	if len(items) != 2 || refs == nil || len(*refs) != 2 || items[1].Label == nil || *items[1].Label != label || items[1].Format == nil {
		t.Fatalf("tooltip lowering = %#v, refs = %#v", items, refs)
	}
}

func TestCanonicalDashboardTooltipAcceptsExplicitlyEmptyItems(t *testing.T) {
	values := []document.DashboardTooltip{}
	visual := document.DashboardVisual{Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Tooltip: &values}}}
	items, refs, err := canonicalDashboardTooltip(visual, LoweredDashboardQuery{}, nil)
	if err != nil {
		t.Fatalf("canonicalDashboardTooltip() error = %v", err)
	}
	if len(items) != 0 || refs == nil || len(*refs) != 0 {
		t.Fatalf("empty tooltip lowering = items %#v, refs %#v", items, refs)
	}
}

func TestCanonicalDashboardTooltipRejectsInvalidItems(t *testing.T) {
	query := LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{{Name: "value"}}}
	fields := []visualizationir.VisualizationField{{ID: "value", DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	cases := []struct {
		name   string
		values []document.DashboardTooltip
		want   string
	}{
		{name: "unknown", values: []document.DashboardTooltip{{String: tooltipStringPtr("missing")}}, want: "not a compiled result field"},
		{name: "duplicate", values: []document.DashboardTooltip{{String: tooltipStringPtr("value")}, {String: tooltipStringPtr("value")}}, want: "duplicates"},
		{name: "format type", values: []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "value", Format: &visualizationir.VisualizationFormat{Value: &visualizationir.TemporalVisualizationFormat{Kind: "temporal"}}}}}, want: "incompatible"},
		{name: "unsupported currency", values: []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "value", Format: &visualizationir.VisualizationFormat{Value: &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: "JPY"}}}}}, want: "unsupported"},
		{name: "noncanonical currency", values: []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "value", Format: &visualizationir.VisualizationFormat{Value: &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: " USD "}}}}}, want: "unsupported"},
		{name: "unsupported duration", values: []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "value", Format: &visualizationir.VisualizationFormat{Value: &visualizationir.DurationVisualizationFormat{Kind: "duration", Unit: "weeks"}}}}}, want: "unsupported"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			visual := document.DashboardVisual{Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Tooltip: &test.values}}}
			_, _, err := canonicalDashboardTooltip(visual, query, fields)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("canonicalDashboardTooltip() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLowerCanonicalLegendMetadataRejectsUnsupportedAuthoringSurfaces(t *testing.T) {
	title := "Legend"
	position := document.DashboardLegendPositionRight
	cases := []struct {
		name  string
		value document.DashboardPresentation
		type_ document.DashboardVisualType
		want  string
	}{
		{name: "heatmap", type_: document.DashboardVisualTypeHeatmap, value: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", LegendTitle: &title}}, want: "presentation.legendTitle"},
		{name: "hierarchy legend position", type_: document.DashboardVisualTypeTree, value: document.DashboardPresentation{Value: &document.HierarchyDashboardPresentation{Type: "hierarchy", Legend: &position}}, want: "presentation.legend is not supported for tree visuals"},
		{name: "hierarchy", type_: document.DashboardVisualTypeTree, value: document.DashboardPresentation{Value: &document.HierarchyDashboardPresentation{Type: "hierarchy", LegendItems: &[]document.DashboardLegendItem{{Value: "x"}}}}, want: "presentation.legendItems"},
		{name: "point legend without categorical series", type_: document.DashboardVisualTypeScatter, value: document.DashboardPresentation{Value: &document.PointDashboardPresentation{Type: "point", Legend: &position, Color: stringPtr("value"), ColorScale: &document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative}}}, want: "presentation.legend requires a categorical point color series"},
		{name: "point without categorical series", type_: document.DashboardVisualTypeScatter, value: document.DashboardPresentation{Value: &document.PointDashboardPresentation{Type: "point", LegendTitle: &title, Color: stringPtr("value"), ColorScale: &document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative}}}, want: "categorical"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := LowerCanonicalDashboardPresentationForQuery(test.value, test.type_, LoweredDashboardQuery{Type: "aggregate"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLowerCanonicalLegendMetadataPreservesOrder(t *testing.T) {
	position := document.DashboardLegendPositionRight
	title := "Status"
	items := []document.DashboardLegendItem{{Value: "open", Label: tooltipStringPtr("Open")}, {Value: "closed"}}
	lowered, err := LowerCanonicalDashboardPresentation(document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Legend: &position, LegendTitle: &title, LegendItems: &items}}, document.DashboardVisualTypeBar)
	if err != nil {
		t.Fatalf("lower presentation: %v", err)
	}
	presentation := lowered.(visualizationir.CartesianVisualizationPresentation)
	if presentation.Legend != visualizationir.VisualizationLegendPositionRight || presentation.LegendTitle == nil || *presentation.LegendTitle != title || presentation.LegendItems == nil || len(*presentation.LegendItems) != 2 || (*presentation.LegendItems)[0].Value != "open" {
		t.Fatalf("legend metadata = %#v", presentation.VisualizationPresentation)
	}
}

func TestLowerCanonicalLegendItemsRequireStaticCartesianMetricAliases(t *testing.T) {
	items := []document.DashboardLegendItem{{Value: "revenue"}}
	query := LoweredDashboardQuery{
		Type:    "aggregate",
		Binding: visualizationdefinition.QueryBinding{Aggregate: &visualizationdefinition.AggregateQueryBinding{Metrics: []visualizationdefinition.FieldBinding{{Alias: "revenue"}}}},
	}
	value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", LegendItems: &items}}
	if _, err := LowerCanonicalDashboardPresentationForQuery(value, document.DashboardVisualTypeBar, query); err != nil {
		t.Fatalf("valid static legend item rejected: %v", err)
	}
	items[0].Value = "missing"
	if _, err := LowerCanonicalDashboardPresentationForQuery(value, document.DashboardVisualTypeBar, query); err == nil || !strings.Contains(err.Error(), "presentation.legendItems[0].value") {
		t.Fatalf("unknown static legend item error = %v, want author path", err)
	}
	query.Binding.Aggregate.Series = &visualizationdefinition.FieldBinding{Alias: "series"}
	if _, err := LowerCanonicalDashboardPresentationForQuery(value, document.DashboardVisualTypeBar, query); err != nil {
		t.Fatalf("dynamic series legend item rejected: %v", err)
	}
}

func TestLowerCanonicalCandlestickRejectsLegendItemsForQuery(t *testing.T) {
	items := []document.DashboardLegendItem{{Value: "close"}}
	value := document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", LegendItems: &items}}
	query := LoweredDashboardQuery{Type: "aggregate", Binding: visualizationdefinition.QueryBinding{Aggregate: &visualizationdefinition.AggregateQueryBinding{Metrics: []visualizationdefinition.FieldBinding{{Alias: "close"}}}}}
	if _, err := LowerCanonicalDashboardPresentationForQuery(value, document.DashboardVisualTypeCandlestick, query); err == nil || !strings.Contains(err.Error(), "presentation.legendItems is not supported for candlestick visuals") {
		t.Fatalf("candlestick legendItems error = %v, want explicit applicability rejection", err)
	}
}

func TestAuthoredTooltipOverridesReachVisualizationIRAcrossRowBoundSurfaces(t *testing.T) {
	label := "Revenue"
	minimum, maximum := int32(1), int32(2)
	format := &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}}
	tooltip := []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "revenue", Label: &label, Format: format}}}
	query := tooltipCompilationQuery()
	for _, test := range []struct {
		name         string
		visualType   document.DashboardVisualType
		presentation document.DashboardPresentation
		check        func(*testing.T, visualizationir.VisualizationSpec)
	}{
		{
			name: "cartesian", visualType: document.DashboardVisualTypeBar,
			presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian", Tooltip: &tooltip}},
			check: func(t *testing.T, spec visualizationir.VisualizationSpec) {
				value := spec.Value.(*visualizationir.CartesianVisualizationSpec)
				assertTooltipItem(t, dereferenceTooltipRefs(value.Tooltip), specBaseTooltipItems(t, spec), label)
			},
		},
		{
			name: "proportional", visualType: document.DashboardVisualTypePie,
			presentation: document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{Type: "proportional", Tooltip: &tooltip}},
			check: func(t *testing.T, spec visualizationir.VisualizationSpec) {
				value := spec.Value.(*visualizationir.ProportionalVisualizationSpec)
				assertTooltipItem(t, dereferenceTooltipRefs(value.Tooltip), specBaseTooltipItems(t, spec), label)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lowered, err := LowerCanonicalDashboardPresentation(test.presentation, test.visualType)
			if err != nil {
				t.Fatalf("lower presentation: %v", err)
			}
			spec, err := canonicalVisualizationSpec(test.name, document.DashboardVisual{Type: test.visualType, Presentation: test.presentation}, query, lowered, nil, dashboardQueryTestModel())
			if err != nil {
				t.Fatalf("canonicalVisualizationSpec: %v", err)
			}
			test.check(t, spec)
		})
	}

	mapTooltip := []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "revenue", Label: &label, Format: format}}}
	point := document.DashboardPointGeographicLayer{
		DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "points", Tooltip: &mapTooltip}, Kind: "point"},
		Kind:                         "point", Latitude: "latitude", Longitude: "longitude",
	}
	layers, err := canonicalGeographicLayers(&document.GeographicDashboardPresentation{Type: "geographic", Layers: &[]document.DashboardGeographicLayer{{Value: &point}}}, query, canonicalResultFields(query, dashboardQueryTestModel()))
	if err != nil {
		t.Fatalf("canonicalGeographicLayers: %v", err)
	}
	base, err := layers[0].Base()
	if err != nil {
		t.Fatalf("geographic layer base: %v", err)
	}
	assertTooltipItem(t, base.Tooltip, base.TooltipItems, label)
}

func TestCanonicalGeographicTooltipUsesCompiledSemanticTypes(t *testing.T) {
	temporal := &visualizationir.VisualizationFormat{Value: &visualizationir.TemporalVisualizationFormat{Kind: "temporal"}}
	tooltip := []document.DashboardTooltip{{Item: &document.DashboardTooltipItem{Field: "purchaseDate", Format: temporal}}}
	point := document.DashboardPointGeographicLayer{
		DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "points", Tooltip: &tooltip}, Kind: "point"},
		Kind:                         "point", Latitude: "latitude", Longitude: "longitude",
	}
	query := LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{{Source: "purchaseDate", Name: "purchaseDate"}, {Name: "latitude"}, {Name: "longitude"}}}
	fields := canonicalResultFields(query, dashboardQueryTestModel())
	layers, err := canonicalGeographicLayers(&document.GeographicDashboardPresentation{Type: "geographic", Layers: &[]document.DashboardGeographicLayer{{Value: &point}}}, query, fields)
	if err != nil {
		t.Fatalf("canonicalGeographicLayers() rejected semantic temporal tooltip format: %v", err)
	}
	base, err := layers[0].Base()
	if err != nil {
		t.Fatalf("geographic layer base: %v", err)
	}
	if base.TooltipItems == nil || len(*base.TooltipItems) != 1 || (*base.TooltipItems)[0].Format == nil {
		t.Fatalf("compiled geographic tooltip items = %#v, want temporal format", base.TooltipItems)
	}
}

func specBaseTooltipItems(t *testing.T, spec visualizationir.VisualizationSpec) *[]visualizationir.VisualizationTooltipItem {
	t.Helper()
	base, err := spec.Base()
	if err != nil {
		t.Fatalf("spec base: %v", err)
	}
	return base.TooltipItems
}

func dereferenceTooltipRefs(value *[]visualizationir.VisualizationFieldRef) []visualizationir.VisualizationFieldRef {
	if value == nil {
		return nil
	}
	return *value
}

func tooltipCompilationQuery() LoweredDashboardQuery {
	return LoweredDashboardQuery{
		Type: "aggregate",
		Binding: visualizationdefinition.QueryBinding{Aggregate: &visualizationdefinition.AggregateQueryBinding{
			Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}},
			Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}},
		}},
		ResultFrame: []DashboardQueryResultField{{Source: "state", Name: "state"}, {Source: "revenue", Name: "revenue"}, {Source: "latitude", Name: "latitude"}, {Source: "longitude", Name: "longitude"}},
	}
}

func assertTooltipItem(t *testing.T, refs []visualizationir.VisualizationFieldRef, items *[]visualizationir.VisualizationTooltipItem, label string) {
	t.Helper()
	if len(refs) != 1 || refs[0].Dataset != "primary" || refs[0].Field != "revenue" || items == nil || len(*items) != 1 || (*items)[0].Label == nil || *(*items)[0].Label != label || (*items)[0].Format == nil {
		t.Fatalf("tooltip refs/items = %#v / %#v", refs, items)
	}
}

func tooltipStringPtr(value string) *string { return &value }
