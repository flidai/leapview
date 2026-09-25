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

func TestScatterSortChangePreservesAuthoredAxisBinding(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	visual := defaultCanonicalVisual(string(document.DashboardVisualTypeScatter), "Scatter")
	category, revenue, orders := "category", "revenue", "order_count"
	visual.Query.Value = &document.AggregateDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
		Dimensions: []document.DashboardDimensionSelection{{String: &category}},
		Metrics:    []document.DashboardMetricSelection{{String: &revenue}, {String: &orders}},
	}
	point := visual.Presentation.Value.(*document.PointDashboardPresentation)
	point.Identity, point.X, point.Y = []string{"category"}, "order_count", "revenue"
	revision.Document.Spec.Visuals["base"] = visual
	sort := []document.DashboardSort{}
	if err := setCanonicalVisualQueryOptions(&revision.Document, SetVisualQueryOptionsPayload{PageID: "overview", VisualID: "base-component", Sort: &sort}); err != nil {
		t.Fatal(err)
	}
	got := revision.Document.Spec.Visuals["base"].Presentation.Value.(*document.PointDashboardPresentation)
	if got.X != "order_count" || got.Y != "revenue" {
		t.Fatalf("sort edit replaced authored scatter axes: X=%q Y=%q", got.X, got.Y)
	}
}
