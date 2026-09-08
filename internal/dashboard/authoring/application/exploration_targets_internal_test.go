package application

import (
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
	dashsignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
)

func TestSafePlacementForDocumentPageRoundsHalfWidthToCustomGrid(t *testing.T) {
	columns := int32(8)
	placement, err := safePlacementForDocumentPage(document.DashboardPage{
		ID: "overview", Layout: &document.DashboardLayoutOverride{Columns: &columns},
	})
	if err != nil {
		t.Fatal(err)
	}
	if placement.ColumnSpan != 4 || placement.Row != 1 || placement.RowSpan != 4 {
		t.Fatalf("custom-grid half placement = %#v", placement)
	}
}

func TestSafePlacementForBuilderPageRoundsHalfWidthToCustomGrid(t *testing.T) {
	placement, err := safePlacementForBuilderPage(dashsignals.DashboardBuilderPageSignal{ID: "overview", Grid: dashsignals.DashboardPageGrid{Columns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if placement.ColumnSpan != 4 || placement.Row != 1 || placement.RowSpan != 4 {
		t.Fatalf("builder custom-grid half placement = %#v", placement)
	}
}
