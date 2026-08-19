package compiler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func canonicalPlacement(column, row, columnSpan, rowSpan int32) document.DashboardPlacement {
	return document.DashboardPlacement{Column: column, Row: row, ColumnSpan: columnSpan, RowSpan: rowSpan}
}

func canonicalVisualComponent(id, visual string, placement document.DashboardPlacement) document.DashboardPageComponent {
	return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: placement},
		Type:                       "visual", Visual: visual,
	}}
}

func TestCompileDashboardLayoutUsesDefaultsAndDerivesHeight(t *testing.T) {
	spec := document.DashboardSpec{Pages: []document.DashboardPage{{
		ID: "overview", Title: "Overview",
		Components: []document.DashboardPageComponent{
			canonicalVisualComponent("first", "revenue", canonicalPlacement(1, 1, 6, 2)),
			canonicalVisualComponent("second", "orders", canonicalPlacement(7, 4, 6, 3)),
		},
	}}}
	layout, err := CompileDashboardLayout(spec)
	if err != nil {
		t.Fatalf("CompileDashboardLayout: %v", err)
	}
	if got, want := layout.Defaults, (definition.LayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16}); got != want {
		t.Fatalf("defaults = %#v, want %#v", got, want)
	}
	page := layout.Pages[0]
	if page.ResponsiveLayout.OccupiedRows != 6 {
		t.Fatalf("occupied rows = %d, want 6", page.ResponsiveLayout.OccupiedRows)
	}
	if got, want := page.Height, 16*2+6*48+5*16; got != want {
		t.Fatalf("height = %d, want %d", got, want)
	}
	if page.Canvas.Width != 0 || page.Canvas.Height != 0 || page.Visuals[1].X != 0 || page.Visuals[1].Y != 0 || page.Visuals[1].Width != 0 || page.Visuals[1].Height != 0 {
		t.Fatalf("responsive page fabricated fixed geometry: %#v", page)
	}
	if placed := page.PlacedVisuals(); placed[1].X != 0 || placed[1].Y != 0 || placed[1].Width != 0 || placed[1].Height != 0 {
		t.Fatalf("responsive PlacedVisuals fabricated fixed geometry: %#v", placed[1])
	}
	if layout.NarrowView != definition.NarrowViewPolicyStack || page.ResponsiveLayout.NarrowView != string(definition.NarrowViewPolicyStack) {
		t.Fatalf("narrow policy = %q/%q", layout.NarrowView, page.ResponsiveLayout.NarrowView)
	}
	if got := []string{page.Visuals[0].ID, page.Visuals[1].ID}; got[0] != "first" || got[1] != "second" {
		t.Fatalf("narrow stacking order changed: %v", got)
	}
}

func TestCompileDashboardLayoutAppliesPageOverrides(t *testing.T) {
	columns, rowHeight, gap, padding := int32(8), int32(24), int32(4), int32(10)
	spec := document.DashboardSpec{
		Layout: &document.DashboardLayoutDefaults{Columns: 10, RowHeight: 30, Gap: 8, Padding: 12},
		Pages: []document.DashboardPage{{
			ID: "detail", Title: "Detail",
			Layout:     &document.DashboardLayoutOverride{Columns: &columns, RowHeight: &rowHeight, Gap: &gap, Padding: &padding},
			Components: []document.DashboardPageComponent{canonicalVisualComponent("card", "orders", canonicalPlacement(8, 2, 1, 1))},
		}},
	}
	layout, err := CompileDashboardLayout(spec)
	if err != nil {
		t.Fatalf("CompileDashboardLayout: %v", err)
	}
	page := layout.Pages[0]
	if page.Grid != (dashboard.PageGrid{Columns: 8, RowHeight: 24, Gap: 4, Padding: 10}) {
		t.Fatalf("page override = %#v", page.Grid)
	}
	if page.Height != 10*2+2*24+4 {
		t.Fatalf("height = %d, want %d", page.Height, 10*2+2*24+4)
	}
}

func TestCompileDashboardLayoutEmptyPageDerivesPaddingHeightAndPreservesResponsiveGeometry(t *testing.T) {
	layout, err := CompileDashboardLayout(document.DashboardSpec{Pages: []document.DashboardPage{{ID: "empty", Title: "Empty"}}})
	if err != nil {
		t.Fatalf("CompileDashboardLayout: %v", err)
	}
	page := layout.Pages[0]
	if page.Height != 32 || page.ResponsiveLayout.OccupiedRows != 0 {
		t.Fatalf("empty page geometry = height %d metadata %#v", page.Height, page.ResponsiveLayout)
	}
	if got := page.WithDefaults(); got.Canvas.Width != 0 || got.Canvas.Height != 0 || got.Width != 0 || got.Height != 32 {
		t.Fatalf("WithDefaults fabricated fixed geometry: %#v", got)
	}
}

func TestCompileDashboardLayoutRejectsInvalidPlacements(t *testing.T) {
	cases := []struct {
		name  string
		pages []document.DashboardPage
		want  string
	}{
		{
			name:  "zero span",
			pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{canonicalVisualComponent("bad", "revenue", canonicalPlacement(1, 1, 0, 1))}}},
			want:  "spans must be greater than zero",
		},
		{
			name:  "out of grid",
			pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{canonicalVisualComponent("bad", "revenue", canonicalPlacement(12, 1, 2, 1))}}},
			want:  "exceed grid",
		},
		{
			name: "overlap",
			pages: []document.DashboardPage{{ID: "overview", Components: []document.DashboardPageComponent{
				canonicalVisualComponent("one", "revenue", canonicalPlacement(1, 1, 4, 2)),
				canonicalVisualComponent("two", "orders", canonicalPlacement(4, 2, 4, 2)),
			}}},
			want: "overlap",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileDashboardLayout(document.DashboardSpec{Pages: test.pages})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCompileDashboardLayoutRejectsInvalidDefaults(t *testing.T) {
	defaults := document.DashboardLayoutDefaults{Columns: 0, RowHeight: 48, Gap: 16, Padding: 16}
	_, err := CompileDashboardLayout(document.DashboardSpec{Layout: &defaults})
	if err == nil || !strings.Contains(err.Error(), "columns must be greater than zero") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompileDashboardLayoutRejectsDuplicatePages(t *testing.T) {
	_, err := CompileDashboardLayout(document.DashboardSpec{Pages: []document.DashboardPage{{ID: "overview"}, {ID: "overview"}}})
	if err == nil || !strings.Contains(err.Error(), "duplicate page id") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompileDashboardPageLayoutRejectsDuplicateComponents(t *testing.T) {
	page := document.DashboardPage{ID: "overview", Components: []document.DashboardPageComponent{
		canonicalVisualComponent("same", "revenue", canonicalPlacement(1, 1, 2, 1)),
		canonicalVisualComponent("same", "orders", canonicalPlacement(3, 1, 2, 1)),
	}}
	_, err := CompileDashboardPageLayout(page, definition.LayoutDefaults{Columns: 12, RowHeight: 48, Gap: 16, Padding: 16})
	if err == nil || !strings.Contains(err.Error(), "duplicate component") {
		t.Fatalf("error = %v", err)
	}
}

func TestAttachDashboardLayoutPersistsOneResponsivePageTree(t *testing.T) {
	base, err := definition.New("dashboard:sales", "Sales", "", "semantic-model:sales", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := document.DashboardSpec{Pages: []document.DashboardPage{{
		ID: "overview", Title: "Overview",
		Components: []document.DashboardPageComponent{canonicalVisualComponent("revenue", "revenue", canonicalPlacement(1, 1, 12, 1))},
	}}}
	attached, err := AttachDashboardLayout(base, spec)
	if err != nil {
		t.Fatalf("AttachDashboardLayout: %v", err)
	}
	if attached.Layout == nil || len(attached.Pages) != 1 || len(attached.Pages[0].Visuals) != 1 {
		t.Fatalf("attached definition = %#v", attached)
	}
	encoded, err := json.Marshal(attached.Layout)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "\"pages\"") || strings.Contains(string(encoded), "\"components\"") {
		t.Fatalf("layout retained a second page/component tree: %s", encoded)
	}
}

func TestValidateCanonicalDashboardCompatibilityRejectsMismatchedPresentationAndQuery(t *testing.T) {
	spec := document.DashboardSpec{Visuals: map[string]document.DashboardVisual{
		"revenue": {
			Type:         document.DashboardVisualTypeBar,
			Query:        document.DashboardQuery{Value: &document.RecordsDashboardQuery{Type: "records"}},
			Presentation: document.DashboardPresentation{Value: &document.KPIDashboardPresentation{Type: "kpi"}},
		},
	}}
	err := ValidateCanonicalDashboardCompatibility(spec)
	if err == nil || !strings.Contains(err.Error(), "requires cartesian presentation") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateCanonicalDashboardCompatibilityRejectsUnknownPageReference(t *testing.T) {
	spec := document.DashboardSpec{Pages: []document.DashboardPage{{
		ID:         "overview",
		Components: []document.DashboardPageComponent{canonicalVisualComponent("missing", "unknown", canonicalPlacement(1, 1, 1, 1))},
	}}}
	err := ValidateCanonicalDashboardCompatibility(spec)
	if err == nil || !strings.Contains(err.Error(), "unknown visual") {
		t.Fatalf("error = %v", err)
	}
}
