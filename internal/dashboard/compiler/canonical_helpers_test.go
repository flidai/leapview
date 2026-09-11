package compiler

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestMutableSpecificationBaseRejectsTypedNil(t *testing.T) {
	tests := []struct {
		name  string
		value visualizationir.VisualizationSpecVariant
	}{
		{name: "cartesian", value: (*visualizationir.CartesianVisualizationSpec)(nil)},
		{name: "kpi", value: (*visualizationir.KPIVisualizationSpec)(nil)},
		{name: "geographic", value: (*visualizationir.GeographicVisualizationSpec)(nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if base, err := mutableSpecificationBase(visualizationir.VisualizationSpec{Value: test.value}); err == nil || base != nil {
				t.Fatalf("mutableSpecificationBase() = (%p, %v), want nil base and an error", base, err)
			}
		})
	}
}

func TestCanonicalMetricPresentationPreservesPercentFormat(t *testing.T) {
	model := &semanticmodel.Model{Metrics: map[string]semanticmodel.Metric{
		"gross_margin": {Label: "Gross margin", Format: "percent"},
	}}
	label, format := canonicalMetricPresentation(model, "gross_margin", "gross_margin")
	if label != "Gross margin" || format == nil {
		t.Fatalf("canonicalMetricPresentation() = (%q, %#v)", label, format)
	}
	percent, ok := format.Value.(*visualizationir.PercentVisualizationFormat)
	if !ok || percent.MinimumFractionDigits == nil || *percent.MinimumFractionDigits != 1 || percent.MaximumFractionDigits == nil || *percent.MaximumFractionDigits != 1 {
		t.Fatalf("percent format = %#v", format.Value)
	}
}
