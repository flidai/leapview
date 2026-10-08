package http

import (
	"github.com/flidai/leapview/internal/dashboard/document"
	"testing"
)

func TestChatVisualLayoutPacksByType(t *testing.T) {
	spec := document.DashboardSpec{}
	page := document.DashboardPage{}
	for i, typ := range []string{"kpi", "kpi", "kpi", "kpi", "combo", "bar", "line"} {
		p := chatVisualPlacement(typ, spec, page, 1, i == 4)
		expected := []document.DashboardPlacement{
			{Column: 1, Row: 1, ColumnSpan: 3, RowSpan: 2}, {Column: 4, Row: 1, ColumnSpan: 3, RowSpan: 2},
			{Column: 7, Row: 1, ColumnSpan: 3, RowSpan: 2}, {Column: 10, Row: 1, ColumnSpan: 3, RowSpan: 2},
			{Column: 1, Row: 3, ColumnSpan: 12, RowSpan: 5}, {Column: 1, Row: 8, ColumnSpan: 6, RowSpan: 5}, {Column: 7, Row: 8, ColumnSpan: 6, RowSpan: 5},
		}[i]
		if p != expected {
			t.Fatalf("%s: got %+v, want %+v", typ, p, expected)
		}
		page.Components = append(page.Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{Placement: p}}})
	}
}

func TestChatVisualLayoutHonorsCustomGridAndOccupiedComponents(t *testing.T) {
	columns, rowHeight, gap := int32(8), int32(24), int32(8)
	spec := document.DashboardSpec{}
	page := document.DashboardPage{Layout: &document.DashboardLayoutOverride{Columns: &columns, RowHeight: &rowHeight, Gap: &gap}}
	// A filter/header occupies the left half; the visual fits on the right.
	page.Components = []document.DashboardPageComponent{{Value: &document.HeaderDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 12}}}}}
	p := chatVisualPlacement("pie", spec, page, 1)
	if p.Column != 5 || p.ColumnSpan != 4 || p.Row != 1 || p.RowSpan != 10 {
		t.Fatalf("unexpected placement: %+v", p)
	}
	p = chatVisualPlacement("table", spec, page, 5)
	if p.Row != 13 || p.ColumnSpan != 8 {
		t.Fatalf("table overlaps existing content: %+v", p)
	}
}
