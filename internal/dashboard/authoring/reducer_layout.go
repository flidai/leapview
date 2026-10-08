package authoring

import (
	"errors"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func updateCanonicalPageLayout(value *document.DashboardDocument, patch UpdatePageLayoutPayload) error {
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	columns := int64(patch.Columns)
	page := &value.Spec.Pages[pageIndex]
	for componentIndex, component := range page.Components {
		base, err := component.Base()
		if err != nil || base == nil {
			if err == nil {
				err = errors.New("component base is empty")
			}
			return fmt.Errorf("%w: page layout component %d: %v", ErrInvalidPayload, componentIndex, err)
		}
		if err := validatePlacementCoordinates(base.Placement); err != nil {
			return fmt.Errorf("%w: page layout component %q: %v", ErrInvalidPayload, base.ID, err)
		}
		columnEnd := int64(base.Placement.Column) + int64(base.Placement.ColumnSpan) - 1
		if int64(base.Placement.Column) > columns || columnEnd > columns {
			return fmt.Errorf("%w: page layout component %q columns %d..%d exceed grid of %d columns", ErrInvalidPayload, base.ID, base.Placement.Column, columnEnd, columns)
		}
	}
	columnsValue, rowHeight, gap, padding := patch.Columns, patch.RowHeight, patch.Gap, patch.Padding
	page.Layout = &document.DashboardLayoutOverride{Columns: &columnsValue, RowHeight: &rowHeight, Gap: &gap, Padding: &padding}
	return nil
}

func setCanonicalPlacements(value *document.DashboardDocument, patch SetPlacementsPayload) error {
	pageIndex := -1
	for index := range value.Spec.Pages {
		if value.Spec.Pages[index].ID == patch.PageID {
			pageIndex = index
			break
		}
	}
	if pageIndex < 0 {
		return fmt.Errorf("%w: page %q", ErrNotFound, patch.PageID)
	}
	page := &value.Spec.Pages[pageIndex]
	columns, err := canonicalPlacementColumns(*value, *page)
	if err != nil {
		return err
	}

	placements := make(map[string]document.DashboardPlacement, len(page.Components))
	componentIndexes := make(map[string]int, len(page.Components))
	for index := range page.Components {
		base, err := page.Components[index].Base()
		if err != nil {
			return fmt.Errorf("%w: component %d: %v", ErrInvalidPayload, index, err)
		}
		if _, exists := placements[base.ID]; exists {
			return fmt.Errorf("%w: page %q contains duplicate component %q", ErrInvalidPayload, patch.PageID, base.ID)
		}
		placements[base.ID] = base.Placement
		componentIndexes[base.ID] = index
	}
	for _, update := range patch.Placements {
		if _, exists := placements[update.ComponentID]; !exists {
			return fmt.Errorf("%w: component %q on page %q", ErrNotFound, update.ComponentID, patch.PageID)
		}
		placements[update.ComponentID] = update.Placement
	}

	ids := make([]string, 0, len(placements))
	for id := range placements {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		placement := placements[id]
		if err := validatePlacementCoordinates(placement); err != nil {
			return fmt.Errorf("%w: component %q: %v", ErrInvalidPayload, id, err)
		}
		columnEnd := int64(placement.Column) + int64(placement.ColumnSpan) - 1
		if int64(placement.Column) > columns || columnEnd > columns {
			return fmt.Errorf("%w: component %q columns %d..%d exceed grid of %d columns", ErrInvalidPayload, id, placement.Column, columnEnd, columns)
		}
	}
	for leftIndex, leftID := range ids {
		for _, rightID := range ids[leftIndex+1:] {
			if placementsOverlapCanonical(placements[leftID], placements[rightID]) {
				return fmt.Errorf("%w: components %q and %q overlap", ErrConflict, leftID, rightID)
			}
		}
	}
	for id, placement := range placements {
		index := componentIndexes[id]
		base, err := page.Components[index].Base()
		if err != nil {
			return fmt.Errorf("%w: component %q: %v", ErrInvalidPayload, id, err)
		}
		base.Placement = placement
	}
	if patch.Compact {
		return compactCanonicalPagePlacements(value, patch.PageID)
	}
	return nil
}

func canonicalPlacementColumns(value document.DashboardDocument, page document.DashboardPage) (int64, error) {
	const defaultColumns int64 = 12
	columns := defaultColumns
	if value.Spec.Layout != nil {
		if value.Spec.Layout.Columns <= 0 {
			return 0, fmt.Errorf("%w: dashboard layout columns must be greater than zero", ErrInvalidPayload)
		}
		columns = int64(value.Spec.Layout.Columns)
	}
	if page.Layout != nil && page.Layout.Columns != nil {
		if *page.Layout.Columns <= 0 {
			return 0, fmt.Errorf("%w: page layout columns must be greater than zero", ErrInvalidPayload)
		}
		columns = int64(*page.Layout.Columns)
	}
	return columns, nil
}

func nextCanonicalVisualPlacement(value document.DashboardDocument, pageIndex int, visualType document.DashboardVisualType) document.DashboardPlacement {
	columnSpan, rowSpan := canonicalVisualPlacementSize(visualType)
	return nextCanonicalComponentPlacement(value, pageIndex, columnSpan, rowSpan)
}

func nextCanonicalComponentPlacement(value document.DashboardDocument, pageIndex int, columnSpan, rowSpan int32) document.DashboardPlacement {
	const defaultColumns int32 = 12
	columns := defaultColumns
	if value.Spec.Layout != nil && value.Spec.Layout.Columns > 0 {
		columns = value.Spec.Layout.Columns
	}
	page := value.Spec.Pages[pageIndex]
	if page.Layout != nil && page.Layout.Columns != nil && *page.Layout.Columns > 0 {
		columns = *page.Layout.Columns
	}
	columnSpan = minPositive(columns, columnSpan)
	for row := int32(1); ; row++ {
		for column := int32(1); column <= columns-columnSpan+1; column++ {
			candidate := document.DashboardPlacement{Column: column, Row: row, ColumnSpan: columnSpan, RowSpan: rowSpan}
			available := true
			for _, component := range page.Components {
				base, err := component.Base()
				if err == nil && base != nil && placementsOverlap(candidate, base.Placement) {
					available = false
					break
				}
			}
			if available {
				return candidate
			}
		}
	}
}

func placementsOverlap(left, right document.DashboardPlacement) bool {
	leftColumnEnd := left.Column + maxPositive(left.ColumnSpan, 1)
	leftRowEnd := left.Row + maxPositive(left.RowSpan, 1)
	rightColumnEnd := right.Column + maxPositive(right.ColumnSpan, 1)
	rightRowEnd := right.Row + maxPositive(right.RowSpan, 1)
	return left.Column < rightColumnEnd && right.Column < leftColumnEnd && left.Row < rightRowEnd && right.Row < leftRowEnd
}

func placementsOverlapCanonical(left, right document.DashboardPlacement) bool {
	leftColumnEnd := int64(left.Column) + int64(left.ColumnSpan) - 1
	leftRowEnd := int64(left.Row) + int64(left.RowSpan) - 1
	rightColumnEnd := int64(right.Column) + int64(right.ColumnSpan) - 1
	rightRowEnd := int64(right.Row) + int64(right.RowSpan) - 1
	return int64(left.Column) <= rightColumnEnd && int64(right.Column) <= leftColumnEnd && int64(left.Row) <= rightRowEnd && int64(right.Row) <= leftRowEnd
}

func maxPositive(value, fallback int32) int32 {
	if value > 0 {
		return value
	}
	return fallback
}

func minPositive(left, right int32) int32 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}
