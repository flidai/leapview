package authoring

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/dashboard/document"
)

type canonicalPackedComponent struct {
	index     int
	id        string
	placement document.DashboardPlacement
}

func canonicalVisualPlacementSize(visualType document.DashboardVisualType) (columnSpan, rowSpan int32) {
	switch visualType {
	case document.DashboardVisualTypeKpi, document.DashboardVisualTypeGauge:
		return 4, 3
	case document.DashboardVisualTypeTree:
		return 6, 6
	case document.DashboardVisualTypeTable, document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot:
		return 6, 5
	default:
		return 6, 4
	}
}

// compactCanonicalPagePlacements closes gaps after an explicit resize while
// retaining every user-chosen component footprint. Ordinary placement edits
// remain authoritative unless the caller asks for this compaction.
func compactCanonicalPagePlacements(value *document.DashboardDocument, pageID string) error {
	return packCanonicalPageComponents(value, pageID)
}

func packCanonicalPageComponents(value *document.DashboardDocument, pageID string) error {
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == pageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, pageID)
	}

	page := &value.Spec.Pages[pageIndex]
	columns, err := canonicalPlacementColumns(*value, *page)
	if err != nil {
		return err
	}

	components := make([]canonicalPackedComponent, 0, len(page.Components))
	for index := range page.Components {
		base, err := page.Components[index].Base()
		if err != nil {
			return err
		}
		item := canonicalPackedComponent{index: index, id: base.ID, placement: base.Placement}
		components = append(components, item)
	}
	sort.SliceStable(components, func(left, right int) bool {
		if components[left].placement.Row != components[right].placement.Row {
			return components[left].placement.Row < components[right].placement.Row
		}
		if components[left].placement.Column != components[right].placement.Column {
			return components[left].placement.Column < components[right].placement.Column
		}
		return components[left].id < components[right].id
	})

	const maxDensePackingColumns int64 = 4096
	if columns > maxDensePackingColumns {
		return packCanonicalComponentsInRows(page, components, int32(columns))
	}
	skyline := make([]int64, int(columns))
	for index := range skyline {
		skyline[index] = 1
	}
	for _, item := range components {
		columnSpan, rowSpan := item.placement.ColumnSpan, item.placement.RowSpan
		columnSpan = minPositive(int32(columns), columnSpan)
		column, row := lowestCanonicalSkylinePosition(skyline, columnSpan)
		if row > int64(1<<31-1) {
			return fmt.Errorf("%w: visual type reflow exceeds maximum grid row", ErrInvalidPayload)
		}
		placement := document.DashboardPlacement{Column: column, Row: int32(row), ColumnSpan: columnSpan, RowSpan: rowSpan}
		if err := validatePlacementCoordinates(placement); err != nil {
			return fmt.Errorf("%w: component %q: %v", ErrInvalidPayload, item.id, err)
		}
		bottom := row + int64(rowSpan)
		for index := int(column - 1); index < int(column-1+columnSpan); index++ {
			skyline[index] = bottom
		}
		base, err := page.Components[item.index].Base()
		if err != nil {
			return err
		}
		base.Placement = placement
	}
	return nil
}

func lowestCanonicalSkylinePosition(skyline []int64, columnSpan int32) (int32, int64) {
	width := int(columnSpan)
	deque := make([]int, 0, len(skyline))
	bestColumn, bestRow := int32(1), int64(1<<62)
	for index, height := range skyline {
		for len(deque) > 0 && skyline[deque[len(deque)-1]] <= height {
			deque = deque[:len(deque)-1]
		}
		deque = append(deque, index)
		if deque[0] <= index-width {
			deque = deque[1:]
		}
		if index < width-1 {
			continue
		}
		row := skyline[deque[0]]
		column := int32(index - width + 2)
		if row < bestRow {
			bestColumn, bestRow = column, row
		}
	}
	return bestColumn, bestRow
}

func packCanonicalComponentsInRows(page *document.DashboardPage, components []canonicalPackedComponent, columns int32) error {
	column, row, rowSpan := int32(1), int64(1), int32(0)
	for _, item := range components {
		columnSpan, componentRows := item.placement.ColumnSpan, item.placement.RowSpan
		columnSpan = minPositive(columns, columnSpan)
		if column+columnSpan-1 > columns {
			row += int64(rowSpan)
			column, rowSpan = 1, 0
		}
		if row > int64(1<<31-1) {
			return fmt.Errorf("%w: visual type reflow exceeds maximum grid row", ErrInvalidPayload)
		}
		base, err := page.Components[item.index].Base()
		if err != nil {
			return err
		}
		base.Placement = document.DashboardPlacement{Column: column, Row: int32(row), ColumnSpan: columnSpan, RowSpan: componentRows}
		column += columnSpan
		rowSpan = max(rowSpan, componentRows)
	}
	return nil
}
