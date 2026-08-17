package compiler

import (
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestCompiledVisualizationResultShapes(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		visual dashboardauthoring.Visual
		want   visualizationdefinition.ResultShape
	}{
		"scalar":          {dashboardauthoring.Visual{Type: "kpi"}, visualizationdefinition.ResultScalar},
		"category":        {dashboardauthoring.Visual{Type: "bar"}, visualizationdefinition.ResultCategoryValue},
		"series":          {dashboardauthoring.Visual{Type: "line", Query: dashboardauthoring.VisualQuery{Series: dashboardauthoring.FieldRef{Field: "series"}}}, visualizationdefinition.ResultCategorySeriesValue},
		"multi metric":   {dashboardauthoring.Visual{Type: "combo", Query: dashboardauthoring.VisualQuery{Metrics: []dashboardauthoring.FieldRef{{Field: "one"}, {Field: "two"}}}}, visualizationdefinition.ResultCategoryMultiMeasure},
		"waterfall":       {dashboardauthoring.Visual{Type: "waterfall"}, visualizationdefinition.ResultCategoryDelta},
		"histogram":       {dashboardauthoring.Visual{Type: "histogram"}, visualizationdefinition.ResultHistogramBins},
		"matrix cells":    {dashboardauthoring.Visual{Type: "heatmap"}, visualizationdefinition.ResultMatrixCells},
		"hierarchy nodes": {dashboardauthoring.Visual{Type: "sunburst"}, visualizationdefinition.ResultHierarchyNodes},
		"graph edges":     {dashboardauthoring.Visual{Type: "sankey"}, visualizationdefinition.ResultGraphEdges},
		"ohlc":            {dashboardauthoring.Visual{Type: "candlestick"}, visualizationdefinition.ResultOHLC},
		"distribution":    {dashboardauthoring.Visual{Type: "boxplot"}, visualizationdefinition.ResultDistribution},
		"geographic":      {dashboardauthoring.Visual{Type: "map"}, visualizationdefinition.ResultGeographicFeatures},
		"points":          {dashboardauthoring.Visual{Type: "scatter"}, visualizationdefinition.ResultPoints},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := compiledVisualResultShape(test.visual)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("result shape = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCompiledVisualizationResultShapeFailsClosed(t *testing.T) {
	if _, err := compiledVisualResultShape(dashboardauthoring.Visual{Type: "unknown"}); err == nil {
		t.Fatal("unknown visualization result shape passed compilation")
	}
}

func TestCompiledTabularResultShapes(t *testing.T) {
	t.Parallel()
	tests := map[string]visualizationdefinition.ResultShape{
		"table":  visualizationdefinition.ResultDetailWindow,
		"matrix": visualizationdefinition.ResultMatrixWindow,
		"pivot":  visualizationdefinition.ResultPivotWindow,
	}
	for visualType, want := range tests {
		binding := compiledTableBinding("model", visualType, dashboardauthoring.TableVisual{Query: dashboardauthoring.TableQuery{Table: "orders"}})
		if binding.ResultShape != want {
			t.Fatalf("%s result shape = %q, want %q", visualType, binding.ResultShape, want)
		}
	}
}
