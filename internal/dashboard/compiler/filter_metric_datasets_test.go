package compiler

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	"reflect"
	"testing"
)

func TestFilterDatasetsIncludeDerivedAndRatioMetricDependencies(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"impact":   {Type: "aggregate", Dataset: "drivers"},
		"negative": {Type: "derived", Expression: "${impact} * -1"},
		"budget":   {Type: "aggregate", Dataset: "budget"},
		"ratio":    {Type: "ratio", Numerator: "negative", Denominator: "budget"},
	}}
	for _, tc := range []struct {
		name string
		want []string
	}{{"negative", []string{"drivers"}}, {"ratio", []string{"budget", "drivers"}}} {
		selection := document.DashboardMetricSelection{String: &tc.name}
		query := document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Metrics: []document.DashboardMetricSelection{selection}}}
		got, err := canonicalQueryDatasets(query, model)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: got %v, err %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

func TestFilterDatasetsRejectMetricDependencyCycles(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{"loop": {Type: "derived", Expression: "${loop} + 1"}}}
	name := "loop"
	_, err := canonicalQueryDatasets(document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Metrics: []document.DashboardMetricSelection{{String: &name}}}}, model)
	if err == nil {
		t.Fatal("expected cyclic metric to be rejected")
	}
}
