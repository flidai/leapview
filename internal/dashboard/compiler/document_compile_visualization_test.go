package compiler

import (
	"math"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCanonicalVisualizationSpecAcceptsPointColorScaleKinds(t *testing.T) {
	t.Parallel()

	minimum, maximum := 0.0, 100.0
	tests := []struct {
		name  string
		color string
		scale document.PointDashboardColorScale
	}{
		{name: "categorical", color: "state", scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindCategorical}},
		{name: "quantitative", color: "revenue", scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative, Minimum: &minimum, Maximum: &maximum}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			spec, err := lowerPointColorScaleSpec(t, test.color, &test.scale)
			if err != nil {
				t.Fatalf("canonicalVisualizationSpec() error = %v", err)
			}
			point := spec.Value.(*visualizationir.PointVisualizationSpec)
			if point.ColorScale == nil || point.ColorScale.Kind != test.scale.Kind {
				t.Fatalf("compiled color scale = %#v, want %q", point.ColorScale, test.scale.Kind)
			}
			if err := visualizationir.ValidateSpec(spec); err != nil {
				t.Fatalf("compiled point IR invalid: %v", err)
			}
		})
	}
}

func TestCanonicalVisualizationSpecRejectsInvalidPointColorScales(t *testing.T) {
	t.Parallel()

	minimum, maximum := 0.0, 100.0
	tests := []struct {
		name  string
		color string
		scale document.PointDashboardColorScale
		want  string
	}{
		{
			name:  "categorical domain",
			color: "state",
			scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindCategorical, Minimum: &minimum, Maximum: &maximum},
			want:  "minimum and maximum require a quantitative scale",
		},
		{
			name:  "unknown kind",
			color: "state",
			scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKind("unknown")},
			want:  "colorScale.kind \"unknown\" is unsupported",
		},
		{
			name:  "nonfinite minimum",
			color: "revenue",
			scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative, Minimum: floatPtr(math.NaN()), Maximum: &maximum},
			want:  "minimum must be finite",
		},
		{
			name:  "nonfinite maximum",
			color: "revenue",
			scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative, Minimum: &minimum, Maximum: floatPtr(math.Inf(1))},
			want:  "maximum must be finite",
		},
		{
			name:  "reversed domain",
			color: "revenue",
			scale: document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative, Minimum: &maximum, Maximum: &minimum},
			want:  "minimum must be less than maximum",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			_, err := lowerPointColorScaleSpec(t, test.color, &test.scale)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("canonicalVisualizationSpec() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCompileVisualsRejectsQuantitativePointColorOnDimension(t *testing.T) {
	t.Parallel()

	visual := pointDashboardVisual("state", &document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative})
	_, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"scatter": visual})
	if err == nil || !strings.Contains(err.Error(), "quantitative point color scale requires a numeric field") {
		t.Fatalf("compileVisuals() error = %v, want quantitative color field diagnostic", err)
	}
}

func TestCompileVisualsAcceptsOnePointMarkFillAndRejectsDuplicate(t *testing.T) {
	t.Parallel()

	visual := pointDashboardVisual("revenue", &document.PointDashboardColorScale{Kind: visualizationir.VisualizationPointColorScaleKindQuantitative})
	format := pointGradientFormat("revenue", visualizationir.VisualizationConditionalTargetMarkFill)
	point := visual.Presentation.Value.(*document.PointDashboardPresentation)
	point.ConditionalFormatting = &[]document.DashboardConditionalFormat{format}
	if _, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"scatter": visual}); err != nil {
		t.Fatalf("compileVisuals() rejected one point mark_fill rule: %v", err)
	}

	duplicate := pointGradientFormat("state", visualizationir.VisualizationConditionalTargetMarkFill)
	point.ConditionalFormatting = &[]document.DashboardConditionalFormat{format, duplicate}
	_, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"scatter": visual})
	if err == nil || !strings.Contains(err.Error(), "point visualizations allow one mark_fill rule") {
		t.Fatalf("compileVisuals() error = %v, want duplicate mark_fill diagnostic", err)
	}
}

func TestCompileVisualsRejectsConditionalFormattingTargetOutsideRenderedChannel(t *testing.T) {
	format := pointGradientFormat("state", visualizationir.VisualizationConditionalTargetMarkFill)
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeColumn,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
			DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian", ConditionalFormatting: &[]document.DashboardConditionalFormat{format}}, Type: "cartesian",
		}},
	}
	_, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"orders": visual})
	if err == nil || !strings.Contains(err.Error(), "conditional formatting \"state-gradient\" field: field \"state\" is not rendered by the cartesian y channel") {
		t.Fatalf("compileVisuals() error = %v, want path-bearing rendered-channel diagnostic", err)
	}
}

func TestCompileVisualsRejectsCartesianMarkStrokeTarget(t *testing.T) {
	t.Parallel()

	for _, visualType := range []document.DashboardVisualType{document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar} {
		visualType := visualType
		t.Run(string(visualType), func(t *testing.T) {
			visual := document.DashboardVisual{
				Type: visualType,
				Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
					Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}},
				}},
				Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
					DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian", ConditionalFormatting: &[]document.DashboardConditionalFormat{pointGradientFormat("revenue", visualizationir.VisualizationConditionalTarget("mark_stroke"))}}, Type: "cartesian",
				}},
			}
			_, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{string(visualType): visual})
			if err == nil || !strings.Contains(err.Error(), "conditional formatting \"revenue-gradient\" target: unsupported target \"mark_stroke\"") {
				t.Fatalf("compileVisuals() error = %v, want conditional-format ID/path mark-stroke diagnostic", err)
			}
		})
	}
}

func TestCompileVisualsConditionalFormattingPreIRErrorIncludesIDAndPath(t *testing.T) {
	format := pointGradientFormat("missing", visualizationir.VisualizationConditionalTargetMarkFill)
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeColumn,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
			DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian", ConditionalFormatting: &[]document.DashboardConditionalFormat{format}}, Type: "cartesian",
		}},
	}
	_, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"orders": visual})
	if err == nil || !strings.Contains(err.Error(), "conditional formatting \"missing-gradient\" field: reference \"missing\" is not a compiled result field") {
		t.Fatalf("compileVisuals() error = %v, want conditional-format ID and field path", err)
	}
}

func TestCompileVisualsBindsWaterfallConditionalFormattingToMetricAlias(t *testing.T) {
	alias := "order_total"
	format := pointGradientFormat(alias, visualizationir.VisualizationConditionalTargetMarkFill)
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeWaterfall,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{Reference: &document.DashboardMetricReference{Metric: "revenue", Alias: &alias}}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{
			DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian", ConditionalFormatting: &[]document.DashboardConditionalFormat{format}}, Type: "cartesian",
		}},
	}
	compiled, err := (dashboardCompileContext{model: dashboardQueryTestModel(), modelID: "sales"}).compileVisuals(map[string]document.DashboardVisual{"waterfall": visual})
	if err != nil {
		t.Fatalf("compileVisuals() error = %v", err)
	}
	spec := compiled["waterfall"].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if len(spec.Y) != 2 || spec.Y[1].Field != alias {
		t.Fatalf("waterfall metric refs = %#v, want metric alias %q at y[1]", spec.Y, alias)
	}
	if formats := spec.ConditionalFormatting; formats == nil || len(*formats) != 1 || (*formats)[0].Field.Field != alias {
		t.Fatalf("waterfall conditional formats = %#v, want alias-bound format", formats)
	}
}

func TestCanonicalGeographicReferenceLayerTooltipContract(t *testing.T) {
	t.Parallel()

	for name, tooltip := range map[string]*[]string{
		"omitted": nil,
		"empty":   func() *[]string { values := []string{}; return &values }(),
	} {
		name, tooltip := name, tooltip
		t.Run(name, func(t *testing.T) {
			layers, err := canonicalGeographicLayers(referenceMapPresentation(tooltip), LoweredDashboardQuery{})
			if err != nil {
				t.Fatalf("canonicalGeographicLayers() error = %v", err)
			}
			if got := len(layers[0].Value.(*visualizationir.VisualizationReferenceLayer).Tooltip); got != 0 {
				t.Fatalf("reference tooltip length = %d, want 0", got)
			}
		})
	}

	tooltip := []string{"state"}
	_, err := canonicalGeographicLayers(referenceMapPresentation(&tooltip), LoweredDashboardQuery{})
	if err == nil || !strings.Contains(err.Error(), "presentation.layers[0].tooltip") || !strings.Contains(err.Error(), "query-row locator") {
		t.Fatalf("canonicalGeographicLayers() error = %v, want a path-bearing reference tooltip diagnostic", err)
	}
}

func referenceMapPresentation(tooltip *[]string) *document.GeographicDashboardPresentation {
	layers := []document.DashboardGeographicLayer{{Value: &document.DashboardReferenceGeographicLayer{
		DashboardGeographicLayerBase: document.DashboardGeographicLayerBase{
			DashboardGeographicLayerOptions: document.DashboardGeographicLayerOptions{ID: "boundaries", Tooltip: tooltip},
			Kind:                            "reference",
		},
		Kind: "reference", GeometryAsset: "brazil_states",
	}}}
	return &document.GeographicDashboardPresentation{Type: "geographic", Layers: &layers}
}

func lowerPointColorScaleSpec(t *testing.T, color string, scale *document.PointDashboardColorScale) (visualizationir.VisualizationSpec, error) {
	t.Helper()
	authored := document.DashboardPresentation{Value: &document.PointDashboardPresentation{
		Type: "point", Identity: []string{"state"}, X: "revenue", Y: "revenue", Color: &color, ColorScale: scale,
	}}
	lowered, err := LowerCanonicalDashboardPresentation(authored, document.DashboardVisualTypeScatter)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	spec, err := canonicalVisualizationSpec("scatter", document.DashboardVisual{Type: document.DashboardVisualTypeScatter, Presentation: authored}, pointQuery(), lowered, nil, dashboardQueryTestModel())
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	return spec, nil
}

func pointQuery() LoweredDashboardQuery {
	return LoweredDashboardQuery{
		Type: "aggregate",
		Binding: visualizationdefinition.QueryBinding{
			ResultShape: visualizationdefinition.ResultPoints,
			Aggregate: &visualizationdefinition.AggregateQueryBinding{
				Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}},
				Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}},
			},
		},
		ResultFrame: []DashboardQueryResultField{{Source: "state", Name: "state"}, {Source: "revenue", Name: "revenue"}},
	}
}

func pointDashboardVisual(color string, scale *document.PointDashboardColorScale) document.DashboardVisual {
	return document.DashboardVisual{
		Type:  document.DashboardVisualTypeScatter,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("state")}}, Metrics: []document.DashboardMetricSelection{{String: stringPtr("revenue")}}}},
		Presentation: document.DashboardPresentation{Value: &document.PointDashboardPresentation{
			Type: "point", Identity: []string{"state"}, X: "revenue", Y: "revenue", Color: &color, ColorScale: scale,
		}},
	}
}

func pointGradientFormat(field string, target visualizationir.VisualizationConditionalTarget) document.DashboardConditionalFormat {
	danger := visualizationir.VisualizationColorIntentDanger
	style := document.DashboardConditionalStyle{Color: &danger}
	return document.DashboardConditionalFormat{
		ID: field + "-gradient", Target: target, Field: field,
		Rule: document.DashboardConditionalRule{Value: &document.DashboardGradientConditionalRule{
			DashboardConditionalRuleBase: document.DashboardConditionalRuleBase{Kind: "gradient"}, Kind: "gradient", Minimum: 0, Maximum: 100,
			Low: style, High: style, NullStyle: style,
		}},
	}
}
