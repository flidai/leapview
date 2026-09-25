package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestVisualTypeSwitchDropsUnsupportedSameFamilyFormatting(t *testing.T) {
	for _, tc := range []struct {
		source, target document.DashboardVisualType
		key, value     string
	}{
		{document.DashboardVisualTypePie, document.DashboardVisualTypeFunnel, "rose", "false"},
		{document.DashboardVisualTypeDonut, document.DashboardVisualTypePie, "centerLabel", "Cash"},
		{document.DashboardVisualTypeLine, document.DashboardVisualTypeBar, "smooth", "false"},
		{document.DashboardVisualTypeTreemap, document.DashboardVisualTypeGraph, "breadcrumb", "false"},
		{document.DashboardVisualTypeGauge, document.DashboardVisualTypeRadar, "showPointer", "true"},
	} {
		t.Run(string(tc.source)+"_to_"+string(tc.target), func(t *testing.T) {
			_, revision := canonicalReducerFixture(t)
			visual := defaultCanonicalVisual(string(tc.source), "Cash")
			if err := applyCanonicalVisualFormatOption(&visual, tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			revision.Document.Spec.Visuals["base"] = visual
			if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: tc.target}); err != nil {
				t.Fatal(err)
			}
			raw, err := presentationObject(revision.Document.Spec.Visuals["base"].Presentation)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := raw[tc.key]; exists {
				t.Fatalf("unsupported %s survived switch to %s: %#v", tc.key, tc.target, raw)
			}
			if _, err := compiler.LowerCanonicalDashboardPresentation(revision.Document.Spec.Visuals["base"].Presentation, tc.target); err != nil {
				t.Fatalf("switched presentation does not compile: %v", err)
			}
		})
	}
}

func TestSameFamilySwitchKeepsTargetDefaultsAndCompatibleFormatting(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	visual := defaultCanonicalVisual("radar", "Cash")
	if err := applyCanonicalVisualFormatOption(&visual, "displayUnits", "millions"); err != nil {
		t.Fatal(err)
	}
	revision.Document.Spec.Visuals["base"] = visual
	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeGauge}); err != nil {
		t.Fatal(err)
	}
	raw, err := presentationObject(revision.Document.Spec.Visuals["base"].Presentation)
	if err != nil {
		t.Fatal(err)
	}
	if raw["minimum"] != nil || raw["maximum"] != nil || raw["displayUnits"] != "millions" {
		t.Fatalf("lost target defaults or compatible formatting: %#v", raw)
	}
}

func TestGaugeAutoRangeExplicitlyClearsBothAuthoredBounds(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	visual := defaultCanonicalVisual("gauge", "Actual")
	minimum, maximum := 0.0, 100.0
	presentation := visual.Presentation.Value.(*document.PolarDashboardPresentation)
	presentation.Minimum, presentation.Maximum = &minimum, &maximum
	revision.Document.Spec.Visuals["base"] = visual
	formatValue := "true"
	if err := updateCanonicalVisualFormat(&revision.Document, UpdateVisualFormatPayload{
		PageID: "overview", VisualID: "base-component", FormatKey: "autoRange", FormatValue: &formatValue,
	}); err != nil {
		t.Fatal(err)
	}
	got := revision.Document.Spec.Visuals["base"].Presentation.Value.(*document.PolarDashboardPresentation)
	if got.Minimum != nil || got.Maximum != nil {
		t.Fatalf("auto range left explicit bounds: minimum=%v maximum=%v", got.Minimum, got.Maximum)
	}
}
