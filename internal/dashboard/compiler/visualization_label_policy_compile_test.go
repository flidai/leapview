package compiler

import (
	"reflect"
	"testing"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompiledLabelPolicyPreservesLegacyAndAuthoredIntent(t *testing.T) {
	t.Parallel()

	automatic := compiledLabelPolicy(dashboardauthoring.VisualPresentation{}, "line")
	if automatic.Density != visualizationir.VisualizationLabelDensityAutomatic || len(automatic.Priority) != 3 || !automatic.TooltipFallback {
		t.Fatalf("default policy = %#v, want automatic collision management", automatic)
	}

	gauge := compiledLabelPolicy(dashboardauthoring.VisualPresentation{}, "gauge")
	if gauge.Density != visualizationir.VisualizationLabelDensityAutomatic || !gauge.TooltipFallback {
		t.Fatalf("gauge default policy = %#v, want automatic with tooltip fallback", gauge)
	}

	radar := compiledLabelPolicy(dashboardauthoring.VisualPresentation{}, "radar")
	if radar.Density != visualizationir.VisualizationLabelDensityHidden || len(radar.Priority) != 0 || !radar.TooltipFallback {
		t.Fatalf("radar default policy = %#v, want hidden because radar data labels are unsupported", radar)
	}

	legacy := compiledLabelPolicy(dashboardauthoring.VisualPresentation{ShowLabels: true}, "line")
	if legacy.Density != visualizationir.VisualizationLabelDensityAutomatic {
		t.Fatalf("show_labels policy = %#v, want automatic collision management", legacy)
	}

	maxCharacters, minimumSpacing, tooltipFallback := 36, 3, true
	authored := compiledLabelPolicy(dashboardauthoring.VisualPresentation{Labels: dashboardauthoring.VisualLabelPolicy{
		Density:         "dense",
		Priority:        []string{"threshold", "selected"},
		MaxCharacters:   &maxCharacters,
		MinimumSpacing:  &minimumSpacing,
		TooltipFallback: &tooltipFallback,
	}}, "line")
	if authored.Density != visualizationir.VisualizationLabelDensityDense ||
		authored.MaxCharacters != 36 || authored.MinimumSpacing != 3 || !authored.TooltipFallback {
		t.Fatalf("authored policy = %#v", authored)
	}
	wantPriority := []visualizationir.VisualizationLabelPriority{
		visualizationir.VisualizationLabelPriorityThreshold,
		visualizationir.VisualizationLabelPrioritySelected,
	}
	if !reflect.DeepEqual(authored.Priority, wantPriority) {
		t.Fatalf("priority = %#v, want %#v", authored.Priority, wantPriority)
	}
}
