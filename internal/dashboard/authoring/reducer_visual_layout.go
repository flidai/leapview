package authoring

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func canonicalVisualPlacementSize(visualType document.DashboardVisualType) (columnSpan, rowSpan int32) {
	switch visualType {
	case document.DashboardVisualTypeKpi, document.DashboardVisualTypeGauge:
		return 4, 3
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return 6, 5
	default:
		return 6, 4
	}
}

// resizeCanonicalVisualPlacement applies the same type-specific footprint used
// for newly added visuals. The selected tile remains anchored as closely as the
// grid permits; only tiles that collide with it (or with a tile displaced by
// it) move downward. A later SetPlacements command remains authoritative, so a
// user can immediately fine-tune the suggested size.
func resizeCanonicalVisualPlacement(value *document.DashboardDocument, pageID, componentID string, visualType document.DashboardVisualType) error {
	pageIndex := -1
	selectedIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID != pageID {
			continue
		}
		pageIndex = index
		for componentIndex := range value.Spec.Pages[index].Components {
			component, ok := value.Spec.Pages[index].Components[componentIndex].Value.(*document.VisualDashboardPageComponent)
			if !ok {
				continue
			}
			base, err := value.Spec.Pages[index].Components[componentIndex].Base()
			if err != nil {
				return err
			}
			if base.ID == componentID || component.Visual == componentID {
				selectedIndex = componentIndex
				break
			}
		}
		break
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, pageID)
	}
	if selectedIndex < 0 {
		return fmt.Errorf("%w: visual component %q on page %q", ErrNotFound, componentID, pageID)
	}

	page := &value.Spec.Pages[pageIndex]
	columns, err := canonicalPlacementColumns(*value, *page)
	if err != nil {
		return err
	}
	columnSpan, rowSpan := canonicalVisualPlacementSize(visualType)
	columnSpan = minPositive(int32(columns), columnSpan)
	selected, err := page.Components[selectedIndex].Base()
	if err != nil {
		return err
	}
	selected.Placement.Column = minPositive(selected.Placement.Column, int32(columns)-columnSpan+1)
	selected.Placement.ColumnSpan = columnSpan
	selected.Placement.RowSpan = rowSpan

	type placedComponent struct {
		index     int
		id        string
		placement document.DashboardPlacement
	}
	remaining := make([]placedComponent, 0, len(page.Components)-1)
	for index := range page.Components {
		if index == selectedIndex {
			continue
		}
		base, err := page.Components[index].Base()
		if err != nil {
			return err
		}
		remaining = append(remaining, placedComponent{index: index, id: base.ID, placement: base.Placement})
	}
	sort.SliceStable(remaining, func(left, right int) bool {
		if remaining[left].placement.Row != remaining[right].placement.Row {
			return remaining[left].placement.Row < remaining[right].placement.Row
		}
		if remaining[left].placement.Column != remaining[right].placement.Column {
			return remaining[left].placement.Column < remaining[right].placement.Column
		}
		return remaining[left].id < remaining[right].id
	})

	occupied := []document.DashboardPlacement{selected.Placement}
	for _, item := range remaining {
		placement := item.placement
		for {
			nextRow := int64(placement.Row)
			for _, blocker := range occupied {
				if placementsOverlapCanonical(placement, blocker) {
					nextRow = max(nextRow, int64(blocker.Row)+int64(blocker.RowSpan))
				}
			}
			if nextRow == int64(placement.Row) {
				break
			}
			if nextRow > int64(1<<31-1) {
				return fmt.Errorf("%w: visual type reflow exceeds maximum grid row", ErrInvalidPayload)
			}
			placement.Row = int32(nextRow)
		}
		base, err := page.Components[item.index].Base()
		if err != nil {
			return err
		}
		base.Placement = placement
		occupied = append(occupied, placement)
	}
	return nil
}
