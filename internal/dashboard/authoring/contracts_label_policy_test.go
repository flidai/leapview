package authoring

import (
	"strings"
	"testing"
)

func TestLabelPolicyAcceptsDeterministicRendererNeutralSettings(t *testing.T) {
	t.Parallel()

	tooltipFallback := true
	maxCharacters, minimumSpacing := 24, 6
	visual := Visual{Type: "heatmap", Presentation: VisualPresentation{Labels: VisualLabelPolicy{
		Density:         "automatic",
		Priority:        []string{"selected", "anomaly", "threshold"},
		MaxCharacters:   &maxCharacters,
		MinimumSpacing:  &minimumSpacing,
		TooltipFallback: &tooltipFallback,
	}}}
	if err := validateVisualPresentation("dense_matrix", visual); err != nil {
		t.Fatalf("validateVisualPresentation() error = %v", err)
	}
}

func TestLabelPolicyRejectsAmbiguousOrUnsupportedSettings(t *testing.T) {
	t.Parallel()

	falseValue := false
	shortTruncation, excessSpacing := 2, 65
	tests := []struct {
		name       string
		visualType string
		policy     VisualLabelPolicy
		want       string
	}{
		{name: "unknown density", visualType: "line", policy: VisualLabelPolicy{Density: "random"}, want: "unsupported presentation.labels.density"},
		{name: "unknown priority", visualType: "line", policy: VisualLabelPolicy{Density: "automatic", Priority: []string{"largest"}}, want: "unsupported presentation.labels priority"},
		{name: "duplicate priority", visualType: "line", policy: VisualLabelPolicy{Density: "automatic", Priority: []string{"selected", "selected"}}, want: "duplicate presentation.labels priority"},
		{name: "short truncation", visualType: "line", policy: VisualLabelPolicy{Density: "automatic", MaxCharacters: &shortTruncation}, want: "max_characters must be between"},
		{name: "excess spacing", visualType: "line", policy: VisualLabelPolicy{Density: "automatic", MinimumSpacing: &excessSpacing}, want: "minimum_spacing must be between"},
		{name: "hidden without fallback", visualType: "line", policy: VisualLabelPolicy{Density: "hidden", TooltipFallback: &falseValue}, want: "labels that can be suppressed require tooltip fallback"},
		{name: "automatic without fallback", visualType: "line", policy: VisualLabelPolicy{Density: "automatic", TooltipFallback: &falseValue}, want: "labels that can be suppressed require tooltip fallback"},
		{name: "dense without fallback", visualType: "line", policy: VisualLabelPolicy{Density: "dense", TooltipFallback: &falseValue}, want: "labels that can be suppressed require tooltip fallback"},
		{name: "unsupported visual", visualType: "map", policy: VisualLabelPolicy{Density: "automatic"}, want: "label policies are unsupported"},
		{name: "unsupported radar", visualType: "radar", policy: VisualLabelPolicy{Density: "automatic"}, want: "label policies are unsupported"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateVisualPresentation("visual", Visual{Type: test.visualType, Presentation: VisualPresentation{Labels: test.policy}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
