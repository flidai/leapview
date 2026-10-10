package compiler

import visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"

func validateHierarchyPivotTotals(hierarchy *visualizationir.GridRowHierarchy, query LoweredDashboardQuery) error {
	return query.Binding.ValidateHierarchyTotals(hierarchy)
}
