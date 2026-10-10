package authoring

import (
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// Hierarchy references must stay within the delivered row axis after ordinary
// field and visual-type edits. A plain table has no row hierarchy.
func pruneCanonicalGridHierarchy(visual *document.DashboardVisual) {
	presentation, ok := visual.Presentation.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil || presentation.Hierarchy == nil {
		return
	}
	if visual.Type != document.DashboardVisualTypeMatrix && visual.Type != document.DashboardVisualTypePivot {
		presentation.Hierarchy = nil
		return
	}
	delivered := make(map[string]bool)
	for _, field := range gridCellContentDeliveredFields(*visual) {
		delivered[field] = true
	}
	switch hierarchy := presentation.Hierarchy.Value.(type) {
	case *visualizationir.GridLevelHierarchy:
		fields := make([]string, 0, len(hierarchy.Fields))
		for _, field := range hierarchy.Fields {
			if delivered[field] {
				fields = append(fields, field)
			}
		}
		hierarchy.Fields = fields
		if len(fields) == 0 {
			presentation.Hierarchy = nil
		}
	case *visualizationir.GridParentChildHierarchy:
		if !delivered[hierarchy.IDField] || !delivered[hierarchy.ParentField] || !delivered[hierarchy.LabelField] {
			presentation.Hierarchy = nil
		}
	case *visualizationir.GridNestedHierarchy:
		if !delivered[hierarchy.ChildrenField] || !delivered[hierarchy.LabelField] {
			presentation.Hierarchy = nil
		} else if hierarchy.IDField != nil && !delivered[*hierarchy.IDField] {
			hierarchy.IDField = nil
		}
	}
}

func rewriteCanonicalGridHierarchyAlias(visual *document.DashboardVisual, oldAlias, newAlias string) {
	presentation, ok := visual.Presentation.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil || presentation.Hierarchy == nil {
		return
	}
	replace := func(field *string) {
		if *field == oldAlias {
			*field = newAlias
		}
	}
	switch hierarchy := presentation.Hierarchy.Value.(type) {
	case *visualizationir.GridLevelHierarchy:
		for index := range hierarchy.Fields {
			replace(&hierarchy.Fields[index])
		}
	case *visualizationir.GridParentChildHierarchy:
		replace(&hierarchy.IDField)
		replace(&hierarchy.ParentField)
		replace(&hierarchy.LabelField)
	case *visualizationir.GridNestedHierarchy:
		replace(&hierarchy.ChildrenField)
		replace(&hierarchy.LabelField)
		if hierarchy.IDField != nil {
			replace(hierarchy.IDField)
		}
	}
}
