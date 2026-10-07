package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestScalarMetricFieldCanBeClearedToPlaceholder(t *testing.T) {
	for _, visualType := range []document.DashboardVisualType{document.DashboardVisualTypeHistogram, document.DashboardVisualTypeBoxplot} {
		t.Run(string(visualType), func(t *testing.T) {
			_, revision := canonicalReducerFixture(t)
			revision.Document.Spec.Visuals["base"] = defaultCanonicalVisual(string(visualType), "Scalar")
			assign := AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric}
			if err := assignCanonicalField(&revision.Document, assign); err != nil {
				t.Fatal(err)
			}
			if err := removeCanonicalField(&revision.Document, RemoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric}); err != nil {
				t.Fatal(err)
			}
			if err := assignCanonicalField(&revision.Document, AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "margin", Role: FieldRoleMetric}); err != nil {
				t.Fatalf("assign replacement: %v", err)
			}
		})
	}
}
