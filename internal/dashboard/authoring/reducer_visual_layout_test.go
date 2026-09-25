package authoring

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalVisualPlacementSizeCoversCatalog(t *testing.T) {
	for _, visual := range CanonicalVisualCatalog() {
		wantColumns, wantRows := int32(6), int32(4)
		switch visual.Type {
		case document.DashboardVisualTypeKpi, document.DashboardVisualTypeGauge:
			wantColumns, wantRows = 4, 3
		case document.DashboardVisualTypeTree:
			wantColumns, wantRows = 6, 6
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

	verticalSource := document.DashboardPlacement{Column: 2, Row: 5, ColumnSpan: 6, RowSpan: 4}
	if err := setCanonicalPlacements(&revision.Document, SetPlacementsPayload{
		PageID: "overview", Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: verticalSource}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := setCanonicalVisualType(&revision.Document, SetVisualTypePayload{
		PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeTree,
	}); err != nil {
		t.Fatal(err)
	}
	if got := component.Placement; got != (document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 6}) {
		t.Fatalf("tree placement = %#v", got)
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
		PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeTree,
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]document.DashboardPlacement{
		"base-component": {Column: 1, Row: 5, ColumnSpan: 6, RowSpan: 6},
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

func TestCanonicalPlacementResizeCompactsCFOLayoutWithoutChangingChosenSize(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	page := &revision.Document.Spec.Pages[0]
	page.Components = []document.DashboardPageComponent{
		canonicalTestFilterComponent("reporting-period", document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 2}),
		canonicalTestFilterComponent("country", document.DashboardPlacement{Column: 5, Row: 1, ColumnSpan: 4, RowSpan: 2}),
		canonicalTestFilterComponent("segment", document.DashboardPlacement{Column: 9, Row: 1, ColumnSpan: 4, RowSpan: 2}),
		canonicalTestVisualComponent("net-revenue", "net-revenue-visual", document.DashboardPlacement{Column: 1, Row: 3, ColumnSpan: 4, RowSpan: 3}),
		canonicalTestVisualComponent("gross-margin", "gross-margin-visual", document.DashboardPlacement{Column: 5, Row: 3, ColumnSpan: 4, RowSpan: 3}),
		canonicalTestVisualComponent("ebitda", "ebitda-visual", document.DashboardPlacement{Column: 9, Row: 3, ColumnSpan: 4, RowSpan: 3}),
		canonicalTestVisualComponent("current-cash", "current-cash-visual", document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 6, RowSpan: 4}),
		canonicalTestVisualComponent("performance", "performance-visual", document.DashboardPlacement{Column: 7, Row: 6, ColumnSpan: 6, RowSpan: 4}),
		canonicalTestVisualComponent("variance", "variance-visual", document.DashboardPlacement{Column: 1, Row: 10, ColumnSpan: 6, RowSpan: 5}),
		canonicalTestVisualComponent("scorecard", "scorecard-visual", document.DashboardPlacement{Column: 7, Row: 10, ColumnSpan: 6, RowSpan: 5}),
	}

	if err := setCanonicalPlacements(&revision.Document, SetPlacementsPayload{
		PageID:  "overview",
		Compact: true,
		Placements: []PlacementUpdate{{
			ComponentID: "current-cash",
			Placement:   document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 3, RowSpan: 4},
		}},
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
		"current-cash":     {Column: 1, Row: 6, ColumnSpan: 3, RowSpan: 4},
		"performance":      {Column: 4, Row: 6, ColumnSpan: 6, RowSpan: 4},
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

func TestCanonicalManualPlacementPreservesDroppedPositionAndUnmovedSiblings(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		t.Helper()
		command := Command{
			ID: CommandID("manual-layout-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID,
			DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
		}
		nextLifecycle, nextRevision, err := ApplyEdit(
			lifecycle, current, canonicalReducerCommandWithPayload(command, payload),
			RevisionID("manual-layout-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1,
			time.Date(2026, 9, 25, 12, 0, int(current.Number), 0, time.UTC),
		)
		if err != nil {
			return err
		}
		lifecycle, current = nextLifecycle, nextRevision
		return nil
	}
	placements := func(revision Revision) map[string]document.DashboardPlacement {
		t.Helper()
		result := make(map[string]document.DashboardPlacement)
		for _, component := range revision.Document.Spec.Pages[0].Components {
			base, err := component.Base()
			if err != nil {
				t.Fatal(err)
			}
			result[base.ID] = base.Placement
		}
		return result
	}

	for _, addition := range []struct{ visualID, componentID, title string }{
		{visualID: "secondary", componentID: "secondary-component", title: "Secondary"},
		{visualID: "tertiary", componentID: "tertiary-component", title: "Tertiary"},
	} {
		if err := apply(&AddVisualPayload{PageID: "overview", VisualID: addition.visualID, ComponentID: addition.componentID, Type: "bar", Title: addition.title}); err != nil {
			t.Fatalf("add %s visual: %v", addition.componentID, err)
		}
	}
	initial := map[string]document.DashboardPlacement{
		"base-component":      {Column: 1, Row: 1, ColumnSpan: 3, RowSpan: 3},
		"secondary-component": {Column: 6, Row: 2, ColumnSpan: 3, RowSpan: 3},
		"tertiary-component":  {Column: 10, Row: 8, ColumnSpan: 3, RowSpan: 4},
	}
	initialUpdates := make([]PlacementUpdate, 0, len(initial))
	for componentID, placement := range initial {
		initialUpdates = append(initialUpdates, PlacementUpdate{ComponentID: componentID, Placement: placement})
	}
	if err := apply(&SetPlacementsPayload{PageID: "overview", Compact: false, Placements: initialUpdates}); err != nil {
		t.Fatalf("save gapped starting layout: %v", err)
	}
	if got := placements(current); !reflect.DeepEqual(got, initial) {
		t.Fatalf("starting placements = %#v, want %#v", got, initial)
	}

	dropped := document.DashboardPlacement{Column: 3, Row: 6, ColumnSpan: 3, RowSpan: 3}
	if err := apply(&SetPlacementsPayload{PageID: "overview", Compact: false, Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: dropped}}}); err != nil {
		t.Fatalf("save manual drop: %v", err)
	}
	want := map[string]document.DashboardPlacement{
		"base-component":      dropped,
		"secondary-component": initial["secondary-component"],
		"tertiary-component":  initial["tertiary-component"],
	}
	if got := placements(current); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved manual placements = %#v, want exact dropped position, intentional gaps, and unmoved siblings %#v", got, want)
	}
	if current.Number != 5 || current.Token() != lifecycle.Draft.Revision {
		t.Fatalf("manual drop was not saved as the selected revision: revision=%#v draft=%#v", current.Token(), lifecycle.Draft.Revision)
	}

	beforeOverlap := placements(current)
	command := Command{
		ID: "manual-layout-overlap", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
		SetPlacements: &SetPlacementsPayload{PageID: "overview", Compact: false, Placements: []PlacementUpdate{{
			ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 7, Row: 3, ColumnSpan: 3, RowSpan: 3},
		}}},
	}
	if _, _, err := ApplyEdit(lifecycle, current, command, "manual-layout-overlap-rev", current.Number+1, time.Date(2026, 9, 25, 12, 1, 0, 0, time.UTC)); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping manual placement error = %v, want ErrConflict", err)
	}
	if got := placements(current); !reflect.DeepEqual(got, beforeOverlap) {
		t.Fatalf("rejected overlapping drop mutated saved revision: got %#v, want %#v", got, beforeOverlap)
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
