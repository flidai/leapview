package authoring

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestAddCanonicalFilterRetainsResolvedCompatibleTargets(t *testing.T) {
	doc := document.DashboardDocument{}
	targets := []string{"revenue", "variance"}
	if err := addCanonicalFilter(&doc, AddFilterPayload{Label: "Country", Dimension: "country", Dataset: "sales", ControlType: "multiSelect", ResolvedTargets: targets}); err != nil {
		t.Fatal(err)
	}
	targets[0] = "cash"
	filter := doc.Spec.Filters[0]
	if filter.Targets == nil || len(*filter.Targets) != 2 || (*filter.Targets)[0] != "revenue" || (*filter.Targets)[1] != "variance" {
		t.Fatalf("compatible targets were not retained independently: %#v", filter.Targets)
	}
}

func TestCanonicalReducerFixesFieldsAndLayoutInOneRevision(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	original := current
	patch := &SetPlacementsPayload{PageID: "overview", FillMissingFields: true, Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 5}}}, ResolvedFields: []AssignFieldPayload{{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric}, {PageID: "overview", VisualID: "base-component", FieldID: "country", Role: FieldRoleDimension}}}
	command := Command{ID: "fix-view", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(), SetPlacements: patch}
	_, fixed, err := ApplyEdit(lifecycle, current, command, "rev-fixed", current.Number+1, time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	query := fixed.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	placement, _ := fixed.Document.Spec.Pages[0].Components[0].Base()
	if len(query.Metrics) != 1 || len(query.Dimensions) != 1 || placement.Placement.ColumnSpan != 6 || fixed.Number != current.Number+1 {
		t.Fatalf("incomplete atomic edit: %#v %#v", query, placement)
	}
	// The exact prior revision retained by Undo still has the original fields and layout.
	prior := original.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	priorPlacement, _ := original.Document.Spec.Pages[0].Components[0].Base()
	if len(prior.Metrics) != 0 || len(prior.Dimensions) != 0 || priorPlacement.Placement.ColumnSpan != 12 {
		t.Fatal("fix mutated its Undo revision")
	}
}

func TestFixScatterRepairsExistingQueryWithoutChangingAuthoredBindings(t *testing.T) {
	for _, authored := range []bool{false, true} {
		t.Run(strconv.FormatBool(authored), func(t *testing.T) {
			_, revision := canonicalReducerFixture(t)
			visual := defaultCanonicalVisual("scatter", "Scatter")
			country, revenue, budget := "country", "revenue", "budget"
			visual.Query.Value = &document.AggregateDashboardQuery{
				DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"}, Type: "aggregate",
				Dimensions: []document.DashboardDimensionSelection{{String: &country}},
				Metrics:    []document.DashboardMetricSelection{{String: &revenue}, {String: &budget}},
			}
			point := visual.Presentation.Value.(*document.PointDashboardPresentation)
			if authored {
				point.X = budget
				point.Y = revenue
				point.Identity = []string{country}
			}
			revision.Document.Spec.Visuals["base"] = visual
			patch := &SetPlacementsPayload{PageID: "overview", FillMissingFields: true, Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 3, Row: 4, ColumnSpan: 6, RowSpan: 5}}}}
			if err := applyCanonicalPayload(&revision.Document, patch); err != nil {
				t.Fatal(err)
			}
			got := revision.Document.Spec.Visuals["base"].Presentation.Value.(*document.PointDashboardPresentation)
			wantX, wantY := revenue, budget
			if authored {
				wantX, wantY = budget, revenue
			}
			if got.X != wantX || got.Y != wantY || !reflect.DeepEqual(got.Identity, []string{country}) {
				t.Fatalf("wrong bindings: %#v", got)
			}
			placement, _ := revision.Document.Spec.Pages[0].Components[0].Base()
			if placement.Placement.Column != 3 || placement.Placement.Row != 4 {
				t.Fatal("authored placement moved")
			}
		})
	}
}
