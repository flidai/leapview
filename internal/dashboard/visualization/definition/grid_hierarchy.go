package definition

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// Appended cross-tab total rows do not carry valid account or nested identities.
// Row-total columns remain compatible because they preserve each row identity.
func (query QueryBinding) ValidateHierarchyTotals(hierarchy *ir.GridRowHierarchy) error {
	if hierarchy == nil || query.Pivot == nil || query.Pivot.Totals == nil {
		return nil
	}
	switch hierarchy.Value.(type) {
	case *ir.GridParentChildHierarchy, *ir.GridNestedHierarchy:
		totals := query.Pivot.Totals
		if totals.Columns || totals.Grand {
			return fmt.Errorf("parent-child and nested hierarchies require totals.columns and totals.grand to be disabled; supply parent totals as query rows")
		}
	}
	return nil
}
