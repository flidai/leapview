package authoring

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalVisualPlacementSizeCoversCatalog(t *testing.T) {
	for _, visual := range CanonicalVisualCatalog() {
		wantColumns, wantRows := int32(6), int32(4)
		switch visual.Type {
		case document.DashboardVisualTypeKpi, document.DashboardVisualTypeGauge:
			wantColumns, wantRows = 4, 3
		case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
			wantColumns, wantRows = 6, 5
		}
		columns, rows := canonicalVisualPlacementSize(visual.Type)
		if columns != wantColumns || rows != wantRows {
			t.Errorf("%s placement size = %dx%d, want %dx%d", visual.Type, columns, rows, wantColumns, wantRows)
		}
	}
}

func TestCanonicalVisualTypeSwitchAppliesTargetFootprint(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	component, err := revision.Document.Spec.Pages[0].Components[0].Base()
	if err != nil {
		t.Fatal(err)
	}
	component.Placement = document.DashboardPlacement{Column: 7, Row: 2, ColumnSpan: 9, RowSpan: 7}

	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{
		PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeKpi,
	}); err != nil {
		t.Fatal(err)
	}
	if got := component.Placement; got != (document.DashboardPlacement{Column: 7, Row: 2, ColumnSpan: 4, RowSpan: 3}) {
		t.Fatalf("KPI placement = %#v", got)
	}

	manual := document.DashboardPlacement{Column: 2, Row: 5, ColumnSpan: 8, RowSpan: 6}
	if err := setCanonicalPlacements(&revision.Document, SetPlacementsPayload{
		PageID: "overview", Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: manual}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := component.Placement; got != manual {
		t.Fatalf("manual placement = %#v, want %#v", got, manual)
	}

	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{
		PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeTable,
	}); err != nil {
		t.Fatal(err)
	}
	if got := component.Placement; got != (document.DashboardPlacement{Column: 2, Row: 5, ColumnSpan: 6, RowSpan: 5}) {
		t.Fatalf("table placement = %#v", got)
	}
}

func TestCanonicalVisualTypeSwitchReflowsOnlyOverlappingComponents(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	page := &revision.Document.Spec.Pages[0]
	base, err := page.Components[0].Base()
	if err != nil {
		t.Fatal(err)
	}
	base.Placement = document.DashboardPlacement{Column: 10, Row: 1, ColumnSpan: 3, RowSpan: 3}
	page.Components = append(page.Components,
		canonicalTestVisualComponent("left", "left-visual", document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}),
		canonicalTestVisualComponent("collider", "collider-visual", document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 3, RowSpan: 3}),
		canonicalTestVisualComponent("below", "below-visual", document.DashboardPlacement{Column: 7, Row: 4, ColumnSpan: 6, RowSpan: 4}),
	)
	for _, visualID := range []string{"left-visual", "collider-visual", "below-visual"} {
		revision.Document.Spec.Visuals[visualID] = defaultCanonicalVisual("bar", visualID)
	}

	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{
		PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeArea,
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]document.DashboardPlacement{
		"base-component": {Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4},
		"left":           {Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4},
		"collider":       {Column: 7, Row: 5, ColumnSpan: 3, RowSpan: 3},
		"below":          {Column: 7, Row: 8, ColumnSpan: 6, RowSpan: 4},
	}
	for _, component := range page.Components {
		placed, err := component.Base()
		if err != nil {
			t.Fatal(err)
		}
		if placed.Placement != want[placed.ID] {
			t.Fatalf("%s placement = %#v, want %#v", placed.ID, placed.Placement, want[placed.ID])
		}
	}
	for leftIndex, left := range page.Components {
		leftBase, _ := left.Base()
		for _, right := range page.Components[leftIndex+1:] {
			rightBase, _ := right.Base()
			if placementsOverlapCanonical(leftBase.Placement, rightBase.Placement) {
				t.Fatalf("placements overlap: %s %#v and %s %#v", leftBase.ID, leftBase.Placement, rightBase.ID, rightBase.Placement)
			}
		}
	}
}

func canonicalTestVisualComponent(componentID, visualID string, placement document.DashboardPlacement) document.DashboardPageComponent {
	return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "visual", Placement: placement},
		Type:                       "visual",
		Visual:                     visualID,
	}}
}
