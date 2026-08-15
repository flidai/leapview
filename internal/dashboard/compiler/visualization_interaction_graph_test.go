package compiler

import (
	"strings"
	"testing"

	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCompleteInteractionTargetsMakesEveryEdgeExplicitAndDeterministic(t *testing.T) {
	targets, err := completeInteractionTargets(
		[]visualizationir.VisualizationInteractionTarget{
			{VisualID: "summary", Effect: visualizationir.VisualizationInteractionEffectHighlight},
			{VisualID: "detail", Effect: visualizationir.VisualizationInteractionEffectFilter},
		},
		[]string{"detail", "source", "summary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []visualizationir.VisualizationInteractionTarget{
		{VisualID: "detail", Effect: visualizationir.VisualizationInteractionEffectFilter},
		{VisualID: "source", Effect: visualizationir.VisualizationInteractionEffectNone},
		{VisualID: "summary", Effect: visualizationir.VisualizationInteractionEffectHighlight},
	}
	if len(targets) != len(want) {
		t.Fatalf("targets = %#v, want %#v", targets, want)
	}
	for index := range want {
		if targets[index] != want[index] {
			t.Fatalf("targets = %#v, want %#v", targets, want)
		}
	}
}

func TestCompleteInteractionTargetsRejectsConflictingAndUnknownEdges(t *testing.T) {
	for name, authored := range map[string][]visualizationir.VisualizationInteractionTarget{
		"conflict": {
			{VisualID: "detail", Effect: visualizationir.VisualizationInteractionEffectFilter},
			{VisualID: "detail", Effect: visualizationir.VisualizationInteractionEffectHighlight},
		},
		"unknown": {
			{VisualID: "forged", Effect: visualizationir.VisualizationInteractionEffectFilter},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := completeInteractionTargets(authored, []string{"detail", "source"}); err == nil {
				t.Fatalf("%s edge was accepted", name)
			}
		})
	}
}

func TestCompleteInteractionTargetsRejectsUnsupportedEffects(t *testing.T) {
	_, err := completeInteractionTargets(
		[]visualizationir.VisualizationInteractionTarget{{VisualID: "detail", Effect: "forged"}},
		[]string{"detail"},
	)
	if err == nil || !strings.Contains(err.Error(), "unsupported effect") {
		t.Fatalf("error = %v, want unsupported effect", err)
	}
}
