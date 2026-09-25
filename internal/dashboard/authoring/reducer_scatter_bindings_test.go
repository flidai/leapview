package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestScatterPresentationTracksFieldChangesAfterTypeSwitch(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	component := "base-component"
	page := "overview"
	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{PageID: page, VisualID: component, Type: document.DashboardVisualTypeScatter, ResolvedBindings: &VisualTypeFieldBindings{Dimensions: []string{"purchase_month"}, Metrics: []string{"revenue", "order_count"}}}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		id   string
		role FieldRole
	}{{"purchase_month", FieldRoleDimension}, {"revenue", FieldRoleMetric}, {"order_count", FieldRoleMetric}} {
		if err := removeCanonicalField(&revision.Document, RemoveFieldPayload{PageID: page, VisualID: component, FieldID: field.id, Role: field.role}); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []struct {
		id   string
		role FieldRole
	}{{"category", FieldRoleDimension}, {"margin", FieldRoleMetric}, {"cost", FieldRoleMetric}} {
		if err := assignCanonicalField(&revision.Document, AssignFieldPayload{PageID: page, VisualID: component, FieldID: field.id, Role: field.role}); err != nil {
			t.Fatal(err)
		}
	}
	presentation := revision.Document.Spec.Visuals["base"].Presentation.Value.(*document.PointDashboardPresentation)
	if len(presentation.Identity) != 1 || presentation.Identity[0] != "category" || presentation.X != "margin" || presentation.Y != "cost" {
		t.Fatalf("scatter presentation retained stale field references: %#v", presentation)
	}
}
