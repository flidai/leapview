package authoring

import (
	"strings"
	"testing"
)

func TestValidateVisualPresentationRejectsUnsupportedRendererValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		visualType   string
		presentation VisualPresentation
		want         string
	}{
		{name: "funnel align", visualType: "funnel", presentation: VisualPresentation{Align: "middle"}, want: "presentation.align"},
		{name: "funnel sort", visualType: "funnel", presentation: VisualPresentation{Sort: "random"}, want: "presentation.sort"},
		{name: "hierarchy layout", visualType: "graph", presentation: VisualPresentation{Layout: "force"}, want: "presentation.layout"},
		{name: "graph focus", visualType: "graph", presentation: VisualPresentation{Focus: "series"}, want: "presentation.focus"},
		{name: "negative depth", visualType: "tree", presentation: VisualPresentation{InitialDepth: -1}, want: "presentation.initial_depth"},
		{name: "negative node gap", visualType: "sankey", presentation: VisualPresentation{NodeGap: -1}, want: "presentation.node_gap"},
		{name: "curveness range", visualType: "graph", presentation: VisualPresentation{Curveness: 1.1}, want: "presentation.curveness"},
		{name: "display units", visualType: "line", presentation: VisualPresentation{DisplayUnits: "crores"}, want: "presentation.display_units"},
		{name: "axis display units", visualType: "line", presentation: VisualPresentation{Axes: []VisualAxis{{ID: "primary_y", DisplayUnits: "lakhs"}}}, want: "presentation.axes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVisualPresentation("visual", Visual{Type: test.visualType, Presentation: test.presentation})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateVisualPresentationAcceptsEChartsTypedValues(t *testing.T) {
	t.Parallel()

	valid := []Visual{
		{Type: "funnel", Presentation: VisualPresentation{Align: "left", Sort: "ascending"}},
		{Type: "graph", Presentation: VisualPresentation{Layout: "circular", Focus: "adjacency", Curveness: 0.4}},
		{Type: "sankey", Presentation: VisualPresentation{NodeGap: 18, Curveness: 0.3}},
		{Type: "tree", Presentation: VisualPresentation{Layout: "standard", InitialDepth: 2}},
	}
	for _, visual := range valid {
		if err := validateVisualPresentation("visual", visual); err != nil {
			t.Fatalf("validateVisualPresentation(%s): %v", visual.Type, err)
		}
	}
}

func TestValidateGaugePresentationRequiresTruthfulDomain(t *testing.T) {
	t.Parallel()

	minimum, maximum, below, above := 0.0, 5.0, -1.0, 6.0
	tests := []struct {
		name         string
		presentation VisualPresentation
		want         string
	}{
		{name: "missing domain", presentation: VisualPresentation{}, want: "requires presentation.minimum and presentation.maximum"},
		{name: "missing maximum", presentation: VisualPresentation{Minimum: &minimum}, want: "requires presentation.minimum and presentation.maximum"},
		{name: "missing minimum", presentation: VisualPresentation{Maximum: &maximum}, want: "requires presentation.minimum and presentation.maximum"},
		{name: "threshold below domain", presentation: VisualPresentation{Minimum: &minimum, Maximum: &maximum, Thresholds: []VisualThreshold{{Value: -1, Tone: "danger"}}}, want: "threshold -1 must be within"},
		{name: "threshold above domain", presentation: VisualPresentation{Minimum: &minimum, Maximum: &maximum, Thresholds: []VisualThreshold{{Value: 6, Tone: "success"}}}, want: "threshold 6 must be within"},
		{name: "target below domain", presentation: VisualPresentation{Minimum: &minimum, Maximum: &maximum, Target: &below}, want: "target -1 must be within"},
		{name: "target above domain", presentation: VisualPresentation{Minimum: &minimum, Maximum: &maximum, Target: &above}, want: "target 6 must be within"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateVisualPresentation("gauge", Visual{Type: "gauge", Presentation: test.presentation})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateGaugePresentationAcceptsExplicitDomain(t *testing.T) {
	t.Parallel()

	minimum, maximum, target := 0.0, 5.0, 4.5
	visual := Visual{Type: "gauge", Presentation: VisualPresentation{
		Minimum: &minimum,
		Maximum: &maximum,
		Target:  &target,
		Thresholds: []VisualThreshold{
			{Value: 3, Tone: "danger"},
			{Value: 4, Tone: "warning"},
			{Value: 5, Tone: "success"},
		},
	}}
	if err := validateVisualPresentation("gauge", visual); err != nil {
		t.Fatalf("validateVisualPresentation(): %v", err)
	}
}

func TestValidateVisualPresentationRejectsGaugeTargetOnOtherVisuals(t *testing.T) {
	t.Parallel()

	target := 4.5
	err := validateVisualPresentation("line", Visual{Type: "line", Presentation: VisualPresentation{Target: &target}})
	if err == nil || !strings.Contains(err.Error(), "only valid for gauge") {
		t.Fatalf("validateVisualPresentation() error = %v, want gauge-only target error", err)
	}
}
