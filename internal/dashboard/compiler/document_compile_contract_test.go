package compiler

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestValidateDerivedResultAliasesReservesOnlyShapeOwnedFields(t *testing.T) {
	query := LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{
		{Name: "node"}, {Name: "parent"}, {Name: "start"}, {Name: "end"}, {Name: "positive"},
	}}
	for _, test := range []struct {
		name     string
		typeName document.DashboardVisualType
		wantErr  bool
	}{
		{name: "hierarchy", typeName: document.DashboardVisualTypeTree, wantErr: true},
		{name: "graph", typeName: document.DashboardVisualTypeGraph, wantErr: false},
		{name: "waterfall", typeName: document.DashboardVisualTypeWaterfall, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDerivedResultAliases(query, test.typeName)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateDerivedResultAliases() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
	if err := validateDerivedResultAliases(LoweredDashboardQuery{ResultFrame: []DashboardQueryResultField{{Name: "value"}}}, document.DashboardVisualTypeTree); err != nil {
		t.Fatalf("hierarchy rejected authored value result name: %v", err)
	}
}

func TestCanonicalResultFieldsKeepAuthoredGraphAndStatisticalNames(t *testing.T) {
	bindings := []visualizationdefinition.FieldBinding{
		{FieldID: "status", Alias: "status"},
		{FieldID: "delivery", Alias: "delivery_bucket"},
	}
	metrics := []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "order_count"}}
	frame := []DashboardQueryResultField{{Source: "status", Name: "status"}, {Source: "delivery", Name: "delivery_bucket"}, {Source: "revenue", Name: "order_count"}}
	base := func(shape visualizationdefinition.ResultShape) LoweredDashboardQuery {
		return LoweredDashboardQuery{Type: "aggregate", Binding: visualizationdefinition.QueryBinding{
			ResultShape: shape,
			Aggregate:   &visualizationdefinition.AggregateQueryBinding{Dimensions: bindings, Metrics: metrics},
		}, ResultFrame: frame}
	}

	model := dashboardQueryTestModel()
	graph := canonicalResultFields(base(visualizationdefinition.ResultGraphEdges), model)
	if got := fieldIDs(graph); got != "status,delivery_bucket,order_count" {
		t.Fatalf("graph fields = %q, want authored result names without synthetic fields", got)
	}
	hierarchy := canonicalResultFields(base(visualizationdefinition.ResultHierarchyNodes), model)
	if got := fieldIDs(hierarchy); got != "status,delivery_bucket,order_count,node,parent" {
		t.Fatalf("hierarchy fields = %q", got)
	}
	if hierarchy[2].Role != visualizationir.VisualizationFieldRoleMetric || hierarchy[2].DataType != visualizationir.VisualizationDataTypeDecimal {
		t.Fatalf("hierarchy metric field = %#v", hierarchy[2])
	}
	waterfall := canonicalResultFields(base(visualizationdefinition.ResultCategoryDelta), model)
	if got := fieldIDs(waterfall); got != "status,delivery_bucket,order_count,start,end,positive" {
		t.Fatalf("waterfall fields = %q", got)
	}

	distribution := LoweredDashboardQuery{Type: "distribution", Binding: visualizationdefinition.QueryBinding{ResultShape: visualizationdefinition.ResultDistribution, Aggregate: &visualizationdefinition.AggregateQueryBinding{Distribution: &visualizationdefinition.DistributionQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: "revenue", Alias: "amount"}}}}, ResultFrame: []DashboardQueryResultField{{Name: "label"}, {Name: "min"}, {Name: "q1"}, {Name: "max"}}}
	fields := canonicalResultFields(distribution, nil)
	if got := fieldIDs(fields); got != "label,min,q1,max" {
		t.Fatalf("distribution fields = %q", got)
	}
	if fields[0].Role != visualizationir.VisualizationFieldRoleDimension || fields[0].DataType != visualizationir.VisualizationDataTypeString {
		t.Fatalf("distribution label field = %#v", fields[0])
	}
	for _, field := range fields[1:] {
		if field.Role != visualizationir.VisualizationFieldRoleMetric || field.DataType != visualizationir.VisualizationDataTypeDecimal {
			t.Fatalf("distribution statistic field = %#v", field)
		}
	}
}

func TestCanonicalVisualizationSpecsUseAuthoredResultAliases(t *testing.T) {
	model := dashboardQueryTestModel()
	base := func(shape visualizationdefinition.ResultShape, dimensions []visualizationdefinition.FieldBinding, metrics []visualizationdefinition.FieldBinding, frame []DashboardQueryResultField) LoweredDashboardQuery {
		return LoweredDashboardQuery{Type: "aggregate", Binding: visualizationdefinition.QueryBinding{ResultShape: shape, Aggregate: &visualizationdefinition.AggregateQueryBinding{Dimensions: dimensions, Metrics: metrics}}, ResultFrame: frame}
	}
	dimensions := []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "status"}, {FieldID: "delivery", Alias: "delivery_bucket"}}
	metric := []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "order_count"}}
	frame := []DashboardQueryResultField{{Name: "status"}, {Name: "delivery_bucket"}, {Name: "order_count"}}
	graphSpec, err := canonicalVisualizationSpec("graph", document.DashboardVisual{Type: document.DashboardVisualTypeGraph}, base(visualizationdefinition.ResultGraphEdges, dimensions, metric, frame), visualizationir.HierarchyVisualizationPresentation{}, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	graph := graphSpec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if graph.Source == nil || graph.Target == nil || graph.Value == nil || graph.Source.Field != "status" || graph.Target.Field != "delivery_bucket" || graph.Value.Field != "order_count" {
		t.Fatalf("graph refs = %#v, want authored aliases", graph)
	}
	hierarchySpec, err := canonicalVisualizationSpec("tree", document.DashboardVisual{Type: document.DashboardVisualTypeTree}, base(visualizationdefinition.ResultHierarchyNodes, dimensions, metric, frame), visualizationir.HierarchyVisualizationPresentation{}, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	hierarchy := hierarchySpec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if hierarchy.Node.Field != "node" || hierarchy.Parent == nil || hierarchy.Parent.Field != "parent" || hierarchy.Value == nil || hierarchy.Value.Field != "order_count" {
		t.Fatalf("hierarchy refs = %#v, want derived node/parent and authored metric", hierarchy)
	}
	waterfallSpec, err := canonicalVisualizationSpec("waterfall", document.DashboardVisual{Type: document.DashboardVisualTypeWaterfall}, base(visualizationdefinition.ResultCategoryDelta, dimensions[:1], metric, frame[:2]), visualizationir.CartesianVisualizationPresentation{}, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	waterfall := waterfallSpec.Value.(*visualizationir.CartesianVisualizationSpec)
	if len(waterfall.Y) != 2 || waterfall.Y[0].Field != "start" || waterfall.Y[1].Field != "order_count" {
		t.Fatalf("waterfall refs = %#v", waterfall.Y)
	}
}

func TestCanonicalVisualizationSpecPromotesSelectionSourcesToIdentity(t *testing.T) {
	model := dashboardQueryTestModel()
	dimensions := []visualizationdefinition.FieldBinding{{FieldID: "category", Alias: "category"}}
	metrics := []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}
	query := LoweredDashboardQuery{
		Type: "aggregate",
		Binding: visualizationdefinition.QueryBinding{
			ResultShape: visualizationdefinition.ResultCategoryValue,
			Aggregate:   &visualizationdefinition.AggregateQueryBinding{Dimensions: dimensions, Metrics: metrics},
		},
		ResultFrame: []DashboardQueryResultField{{Source: "category", Name: "category"}, {Source: "revenue", Name: "revenue"}},
	}
	targets := []string{"orders"}
	interactions := []document.DashboardInteraction{{Value: &document.SelectionDashboardInteraction{
		DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection", Targets: &targets},
		Type:                     "selection", Mode: document.DashboardSelectionModeMultiple,
		Mappings: []document.DashboardInteractionMapping{{Field: "category", Value: "category"}},
	}}}
	spec, err := canonicalVisualizationSpec("categories", document.DashboardVisual{Type: document.DashboardVisualTypeBar, Interactions: &interactions}, query, visualizationir.CartesianVisualizationPresentation{}, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	base, err := visualizationir.SpecificationBase(spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := base.Datasets[0].Fields[0].Role; got != visualizationir.VisualizationFieldRoleIdentity {
		t.Fatalf("selection source role = %q, want identity", got)
	}
}

func TestCanonicalVisualizationSpecBoxplotAndCandlestickContracts(t *testing.T) {
	model := dashboardQueryTestModel()
	boxplotQuery := LoweredDashboardQuery{Type: "distribution", Binding: visualizationdefinition.QueryBinding{ResultShape: visualizationdefinition.ResultDistribution, Aggregate: &visualizationdefinition.AggregateQueryBinding{Distribution: &visualizationdefinition.DistributionQueryBinding{Metric: visualizationdefinition.FieldBinding{FieldID: "revenue", Alias: "amount"}}}}, ResultFrame: []DashboardQueryResultField{{Name: "label"}, {Name: "min"}, {Name: "q1"}, {Name: "median"}, {Name: "q3"}, {Name: "max"}}}
	spec, err := canonicalVisualizationSpec("boxplot", document.DashboardVisual{Type: document.DashboardVisualTypeBoxplot}, boxplotQuery, visualizationir.CartesianVisualizationPresentation{}, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	boxplot := spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if boxplot.X.Field != "label" || len(boxplot.Y) != 5 || boxplot.Y[0].Field != "min" || boxplot.Y[4].Field != "max" {
		t.Fatalf("boxplot refs = %#v", boxplot)
	}
	dimensions := []visualizationdefinition.FieldBinding{{FieldID: "date", Alias: "trade_date"}}
	metrics := []visualizationdefinition.FieldBinding{{FieldID: "open", Alias: "open"}, {FieldID: "close", Alias: "close"}, {FieldID: "low", Alias: "low"}, {FieldID: "high", Alias: "high"}}
	frame := []DashboardQueryResultField{{Name: "trade_date"}, {Name: "open"}, {Name: "close"}, {Name: "low"}, {Name: "high"}}
	oc := func(ds, ms int) LoweredDashboardQuery {
		return LoweredDashboardQuery{Binding: visualizationdefinition.QueryBinding{ResultShape: visualizationdefinition.ResultOHLC, Aggregate: &visualizationdefinition.AggregateQueryBinding{Dimensions: dimensions[:ds], Metrics: metrics[:ms]}}, ResultFrame: frame}
	}
	valid, err := canonicalVisualizationSpec("candlestick", document.DashboardVisual{Type: document.DashboardVisualTypeCandlestick}, oc(1, 4), visualizationir.CartesianVisualizationPresentation{}, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	candlestick := valid.Value.(*visualizationir.CartesianVisualizationSpec)
	if candlestick.X.Field != "trade_date" || len(candlestick.Y) != 4 || candlestick.Y[0].Field != "open" || candlestick.Y[1].Field != "close" || candlestick.Y[2].Field != "low" || candlestick.Y[3].Field != "high" {
		t.Fatalf("candlestick refs = %#v", candlestick)
	}
	for _, test := range []LoweredDashboardQuery{oc(0, 4), oc(1, 3)} {
		if _, err := canonicalVisualizationSpec("candlestick", document.DashboardVisual{Type: document.DashboardVisualTypeCandlestick}, test, visualizationir.CartesianVisualizationPresentation{}, nil, model); err == nil {
			t.Fatalf("invalid candlestick operands accepted: %#v", test.Binding.Aggregate)
		}
	}
}

func TestCanonicalVisualizationSpecsCompileProportionalAndPolarAuthoredOptions(t *testing.T) {
	model := dashboardQueryTestModel()
	dimension := []visualizationdefinition.FieldBinding{{FieldID: "status", Alias: "status"}}
	metric := []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}
	categoryQuery := LoweredDashboardQuery{
		Type: "aggregate",
		Binding: visualizationdefinition.QueryBinding{
			ResultShape: visualizationdefinition.ResultCategoryValue,
			Aggregate:   &visualizationdefinition.AggregateQueryBinding{Dimensions: dimension, Metrics: metric},
		},
		ResultFrame: []DashboardQueryResultField{{Name: "status"}, {Name: "revenue"}},
	}
	valueQuery := LoweredDashboardQuery{
		Type: "aggregate",
		Binding: visualizationdefinition.QueryBinding{
			ResultShape: visualizationdefinition.ResultCategoryValue,
			Aggregate:   &visualizationdefinition.AggregateQueryBinding{Metrics: metric},
		},
		ResultFrame: []DashboardQueryResultField{{Name: "revenue"}},
	}
	outerRadius := 1.0
	minimum, maximum := 0.0, 100.0
	showPointer := false
	cases := []struct {
		name       string
		visualType document.DashboardVisualType
		visual     document.DashboardVisual
		query      LoweredDashboardQuery
		check      func(t *testing.T, spec visualizationir.VisualizationSpec)
	}{
		{
			name: "donut authored geometry", visualType: document.DashboardVisualTypeDonut, query: categoryQuery,
			visual: document.DashboardVisual{Type: document.DashboardVisualTypeDonut, Presentation: document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{Type: "proportional", InnerRadius: floatPointer(0), OuterRadius: &outerRadius, CenterLabel: stringPointer("Orders")}}},
			check: func(t *testing.T, spec visualizationir.VisualizationSpec) {
				got := spec.Value.(*visualizationir.ProportionalVisualizationSpec)
				if got.Mark != visualizationir.VisualizationProportionalMarkDonut || got.Category.Field != "status" || got.Value.Field != "revenue" || got.Presentation.InnerRadius == nil || *got.Presentation.InnerRadius != 0 || got.Presentation.CenterLabel == nil || *got.Presentation.CenterLabel != "Orders" {
					t.Fatalf("donut spec = %#v", got)
				}
			},
		},
		{
			name: "funnel authored ordering", visualType: document.DashboardVisualTypeFunnel, query: categoryQuery,
			visual: document.DashboardVisual{Type: document.DashboardVisualTypeFunnel, Presentation: document.DashboardPresentation{Value: &document.ProportionalDashboardPresentation{Type: "proportional", Align: alignmentPointer(document.DashboardProportionalAlignmentCenter), Sort: sortPointer(visualizationir.VisualizationSortDirectionDescending)}}},
			check: func(t *testing.T, spec visualizationir.VisualizationSpec) {
				got := spec.Value.(*visualizationir.ProportionalVisualizationSpec)
				if got.Mark != visualizationir.VisualizationProportionalMarkFunnel || got.Presentation.Align == nil || *got.Presentation.Align != "center" || got.Presentation.Sort == nil || *got.Presentation.Sort != visualizationir.VisualizationSortDirectionDescending {
					t.Fatalf("funnel spec = %#v", got)
				}
			},
		},
		{
			name: "gauge authored domain and pointer", visualType: document.DashboardVisualTypeGauge, query: valueQuery,
			visual: document.DashboardVisual{Type: document.DashboardVisualTypeGauge, Presentation: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar", Minimum: &minimum, Maximum: &maximum, ShowPointer: &showPointer}}},
			check: func(t *testing.T, spec visualizationir.VisualizationSpec) {
				got := spec.Value.(*visualizationir.PolarVisualizationSpec)
				if got.Mark != visualizationir.VisualizationPolarMarkGauge || got.Value.Field != "revenue" || got.Presentation.Minimum == nil || *got.Presentation.Minimum != 0 || got.Presentation.Maximum == nil || *got.Presentation.Maximum != 100 || got.Presentation.ShowPointer {
					t.Fatalf("gauge spec = %#v", got)
				}
			},
		},
		{
			name: "radar authored maximum", visualType: document.DashboardVisualTypeRadar, query: categoryQuery,
			visual: document.DashboardVisual{Type: document.DashboardVisualTypeRadar, Presentation: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar", Maximum: floatPointer(10)}}},
			check: func(t *testing.T, spec visualizationir.VisualizationSpec) {
				got := spec.Value.(*visualizationir.PolarVisualizationSpec)
				if got.Mark != visualizationir.VisualizationPolarMarkRadar || got.Category == nil || got.Category.Field != "status" || got.Presentation.Maximum == nil || *got.Presentation.Maximum != 10 {
					t.Fatalf("radar spec = %#v", got)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			presentation, err := LowerCanonicalDashboardPresentationForQuery(test.visual.Presentation, test.visualType, test.query)
			if err != nil {
				t.Fatalf("lower presentation: %v", err)
			}
			spec, err := canonicalVisualizationSpec(test.name, test.visual, test.query, presentation, nil, model)
			if err != nil {
				t.Fatalf("compile spec: %v", err)
			}
			test.check(t, spec)
		})
	}
}

func TestCanonicalPolarVisualizationSpecEnforcesShapeAndSeriesContracts(t *testing.T) {
	model := dashboardQueryTestModel()
	metric := []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}}
	query := func(dimensions []visualizationdefinition.FieldBinding, shape visualizationdefinition.ResultShape) LoweredDashboardQuery {
		frame := make([]DashboardQueryResultField, 0, len(dimensions)+1)
		for _, dimension := range dimensions {
			frame = append(frame, DashboardQueryResultField{Source: dimension.FieldID, Name: dimension.Alias})
		}
		frame = append(frame, DashboardQueryResultField{Source: "revenue", Name: "revenue"})
		return LoweredDashboardQuery{
			Type: "aggregate",
			Binding: visualizationdefinition.QueryBinding{
				ResultShape: shape,
				Aggregate:   &visualizationdefinition.AggregateQueryBinding{Dimensions: dimensions, Metrics: metric},
			},
			ResultFrame: frame,
		}
	}
	compile := func(t *testing.T, visualType document.DashboardVisualType, visual document.DashboardVisual, lowered LoweredDashboardQuery) visualizationir.VisualizationSpec {
		t.Helper()
		presentation, err := LowerCanonicalDashboardPresentationForQuery(visual.Presentation, visualType, lowered)
		if err != nil {
			t.Fatalf("lower presentation: %v", err)
		}
		spec, err := canonicalVisualizationSpec(string(visualType), visual, lowered, presentation, nil, model)
		if err != nil {
			t.Fatalf("compile spec: %v", err)
		}
		return spec
	}

	t.Run("gauge accepts scalar shape only", func(t *testing.T) {
		visual := document.DashboardVisual{Type: document.DashboardVisualTypeGauge, Presentation: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar", Minimum: floatPointer(0), Maximum: floatPointer(100)}}}
		spec := compile(t, document.DashboardVisualTypeGauge, visual, query(nil, visualizationdefinition.ResultScalar))
		got := spec.Value.(*visualizationir.PolarVisualizationSpec)
		if got.Category != nil || got.Series != nil || got.Value.Field != "revenue" {
			t.Fatalf("gauge spec = %#v", got)
		}
	})

	t.Run("gauge rejects category dimension", func(t *testing.T) {
		visual := document.DashboardVisual{Type: document.DashboardVisualTypeGauge, Presentation: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar", Minimum: floatPointer(0), Maximum: floatPointer(100)}}}
		presentation, err := LowerCanonicalDashboardPresentationForQuery(visual.Presentation, visual.Type, query([]visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}}, visualizationdefinition.ResultCategoryValue))
		if err != nil {
			t.Fatalf("lower presentation: %v", err)
		}
		_, err = canonicalVisualizationSpec("gauge", visual, query([]visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}}, visualizationdefinition.ResultCategoryValue), presentation, nil, model)
		if err == nil || !strings.Contains(err.Error(), "gauge requires zero dimensions") {
			t.Fatalf("error = %v, want gauge dimension contract", err)
		}
	})

	t.Run("radar requires category dimension", func(t *testing.T) {
		visual := document.DashboardVisual{Type: document.DashboardVisualTypeRadar, Presentation: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar"}}}
		lowered := query(nil, visualizationdefinition.ResultScalar)
		presentation, err := LowerCanonicalDashboardPresentationForQuery(visual.Presentation, visual.Type, lowered)
		if err != nil {
			t.Fatalf("lower presentation: %v", err)
		}
		_, err = canonicalVisualizationSpec("radar", visual, lowered, presentation, nil, model)
		if err == nil || !strings.Contains(err.Error(), "radar requires exactly one category dimension") {
			t.Fatalf("error = %v, want radar category contract", err)
		}
	})

	t.Run("radar derives second dimension as governed series", func(t *testing.T) {
		dimensions := []visualizationdefinition.FieldBinding{{FieldID: "state", Alias: "state"}, {FieldID: "status", Alias: "status"}}
		lowered := query(dimensions, visualizationdefinition.ResultCategoryMultiMeasure)
		if err := lowerCanonicalVisualSeries(&lowered, document.DashboardVisualTypeRadar); err != nil {
			t.Fatalf("lower radar series: %v", err)
		}
		if lowered.Binding.ResultShape != visualizationdefinition.ResultCategorySeriesValue || lowered.Binding.Aggregate.Series == nil || lowered.Binding.Aggregate.Series.Alias != "status" {
			t.Fatalf("radar query series = %#v", lowered.Binding)
		}
		visual := document.DashboardVisual{Type: document.DashboardVisualTypeRadar, Presentation: document.DashboardPresentation{Value: &document.PolarDashboardPresentation{Type: "polar", Legend: func() *document.DashboardLegendPosition {
			value := document.DashboardLegendPositionRight
			return &value
		}()}}}
		spec := compile(t, document.DashboardVisualTypeRadar, visual, lowered)
		got := spec.Value.(*visualizationir.PolarVisualizationSpec)
		if got.Category == nil || got.Category.Field != "state" || got.Series == nil || got.Series.Field != "status" || got.Value.Field != "revenue" {
			t.Fatalf("radar spec = %#v", got)
		}
	})
}

func alignmentPointer(value document.DashboardProportionalAlignment) *document.DashboardProportionalAlignment {
	return &value
}

func sortPointer(value visualizationir.VisualizationSortDirection) *visualizationir.VisualizationSortDirection {
	return &value
}

func fieldIDs(fields []visualizationir.VisualizationField) string {
	ids := make([]string, len(fields))
	for i, field := range fields {
		ids[i] = field.ID
	}
	return joinComma(ids)
}

func joinComma(values []string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += ","
		}
		result += value
	}
	return result
}
