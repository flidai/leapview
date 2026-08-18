package compiler

import (
	"testing"

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
