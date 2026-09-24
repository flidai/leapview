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
	if got := component.Placement; got != (document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 3}) {
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
	if got := component.Placement; got != (document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 5}) {
		t.Fatalf("table placement = %#v", got)
	}
}

func TestCanonicalVisualTypeSwitchPacksPageWithoutVisualGaps(t *testing.T) {
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
		"base-component": {Column: 1, Row: 5, ColumnSpan: 6, RowSpan: 4},
		"left":           {Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4},
		"collider":       {Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4},
		"below":          {Column: 7, Row: 5, ColumnSpan: 6, RowSpan: 4},
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

func TestCanonicalVisualTypeSwitchPacksCFOLayoutAroundFixedFilters(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	page := &revision.Document.Spec.Pages[0]
	page.Components = []document.DashboardPageComponent{
		canonicalTestFilterComponent("reporting-period", document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 2}),
		canonicalTestFilterComponent("country", document.DashboardPlacement{Column: 5, Row: 1, ColumnSpan: 4, RowSpan: 2}),
		canonicalTestFilterComponent("segment", document.DashboardPlacement{Column: 9, Row: 1, ColumnSpan: 4, RowSpan: 2}),
		canonicalTestVisualComponent("net-revenue", "net-revenue-visual", document.DashboardPlacement{Column: 1, Row: 3, ColumnSpan: 3, RowSpan: 3}),
		canonicalTestVisualComponent("gross-margin", "gross-margin-visual", document.DashboardPlacement{Column: 4, Row: 3, ColumnSpan: 3, RowSpan: 3}),
		canonicalTestVisualComponent("current-cash", "current-cash-visual", document.DashboardPlacement{Column: 7, Row: 3, ColumnSpan: 6, RowSpan: 4}),
		canonicalTestVisualComponent("ebitda", "ebitda-visual", document.DashboardPlacement{Column: 1, Row: 7, ColumnSpan: 3, RowSpan: 3}),
		canonicalTestVisualComponent("performance", "performance-visual", document.DashboardPlacement{Column: 1, Row: 10, ColumnSpan: 8, RowSpan: 6}),
		canonicalTestVisualComponent("variance", "variance-visual", document.DashboardPlacement{Column: 9, Row: 10, ColumnSpan: 4, RowSpan: 6}),
		canonicalTestVisualComponent("scorecard", "scorecard-visual", document.DashboardPlacement{Column: 1, Row: 16, ColumnSpan: 12, RowSpan: 6}),
	}
	for _, visualID := range []string{"net-revenue-visual", "gross-margin-visual", "ebitda-visual"} {
		revision.Document.Spec.Visuals[visualID] = defaultCanonicalVisual("kpi", visualID)
	}
	revision.Document.Spec.Visuals["current-cash-visual"] = defaultCanonicalVisual("line", "Current cash")
	revision.Document.Spec.Visuals["performance-visual"] = defaultCanonicalVisual("combo", "Performance")
	revision.Document.Spec.Visuals["variance-visual"] = defaultCanonicalVisual("table", "Variance")
	revision.Document.Spec.Visuals["scorecard-visual"] = defaultCanonicalVisual("matrix", "Scorecard")

	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{
		PageID: "overview", VisualID: "current-cash", Type: document.DashboardVisualTypeArea,
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]document.DashboardPlacement{
		"reporting-period": {Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 2},
		"country":          {Column: 5, Row: 1, ColumnSpan: 4, RowSpan: 2},
		"segment":          {Column: 9, Row: 1, ColumnSpan: 4, RowSpan: 2},
		"net-revenue":      {Column: 1, Row: 3, ColumnSpan: 4, RowSpan: 3},
		"gross-margin":     {Column: 5, Row: 3, ColumnSpan: 4, RowSpan: 3},
		"ebitda":           {Column: 9, Row: 3, ColumnSpan: 4, RowSpan: 3},
		"current-cash":     {Column: 1, Row: 6, ColumnSpan: 6, RowSpan: 4},
		"performance":      {Column: 7, Row: 6, ColumnSpan: 6, RowSpan: 4},
		"variance":         {Column: 1, Row: 10, ColumnSpan: 6, RowSpan: 5},
		"scorecard":        {Column: 7, Row: 10, ColumnSpan: 6, RowSpan: 5},
	}
	for _, component := range page.Components {
		placed, err := component.Base()
		if err != nil {
			t.Fatal(err)
		}
		if placed.Placement != want[placed.ID] {
			t.Errorf("%s placement = %#v, want %#v", placed.ID, placed.Placement, want[placed.ID])
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

func canonicalTestFilterComponent(componentID string, placement document.DashboardPlacement) document.DashboardPageComponent {
	return document.DashboardPageComponent{Value: &document.FilterDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: componentID, Type: "filter", Placement: placement},
		Type:                       "filter",
		Filter:                     "filter-1",
	}}
}
