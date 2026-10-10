package authoring

import (
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// Query changes must remove presentation bindings that no longer have a
// delivered cell. Other column content and presentation stay intact.
func pruneCanonicalGridCellContent(visual *document.DashboardVisual) {
	pruneCanonicalGridHierarchy(visual)
	presentation, ok := visual.Presentation.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil || presentation.CellContent == nil {
		return
	}
	delivered := make(map[string]bool)
	for _, field := range gridCellContentDeliveredFields(*visual) {
		delivered[field] = true
	}
	for field, content := range *presentation.CellContent {
		if !delivered[field] {
			delete(*presentation.CellContent, field)
			continue
		}
		switch value := content.Value.(type) {
		case *visualizationir.TableImageCellContent:
			if value != nil && value.AltField != nil && !delivered[*value.AltField] {
				value.AltField = nil
			}
		case *visualizationir.TableLinkCellContent:
			if value != nil && value.LabelField != nil && !delivered[*value.LabelField] {
				value.LabelField = nil
			}
		}
	}
	if len(*presentation.CellContent) == 0 {
		presentation.CellContent = nil
	}
}

// A field alias edit changes one delivered name while retaining the governed
// field itself. Move the target and auxiliary bindings to that new name rather
// than discarding the user's column formatting. Compare delivered aliases so
// implicit qualified names and fields hidden by pivot shaping stay correct.
func rewriteCanonicalGridCellContentAliases(visual *document.DashboardVisual, previousFields []string) {
	currentFields := gridCellContentDeliveredFields(*visual)
	previous := make(map[string]bool, len(previousFields))
	current := make(map[string]bool, len(currentFields))
	for _, field := range previousFields {
		previous[field] = true
	}
	for _, field := range currentFields {
		current[field] = true
	}
	var removed, added []string
	for _, field := range previousFields {
		if !current[field] {
			removed = append(removed, field)
		}
	}
	for _, field := range currentFields {
		if !previous[field] {
			added = append(added, field)
		}
	}
	if len(removed) == 0 && len(added) == 0 {
		return
	}
	if len(removed) == 1 && len(added) == 1 {
		rewriteCanonicalGridHierarchyAlias(visual, removed[0], added[0])
	}
	pruneCanonicalGridHierarchy(visual)
	presentation, ok := visual.Presentation.Value.(*document.TableDashboardPresentation)
	if !ok || presentation == nil || presentation.CellContent == nil {
		return
	}
	if len(removed) == 1 && len(added) == 1 {
		oldAlias, newAlias := removed[0], added[0]
		if content, exists := (*presentation.CellContent)[oldAlias]; exists {
			(*presentation.CellContent)[newAlias] = content
			delete(*presentation.CellContent, oldAlias)
		}
		for _, content := range *presentation.CellContent {
			switch value := content.Value.(type) {
			case *visualizationir.TableImageCellContent:
				if value != nil && value.AltField != nil && *value.AltField == oldAlias {
					value.AltField = &newAlias
				}
			case *visualizationir.TableLinkCellContent:
				if value != nil && value.LabelField != nil && *value.LabelField == oldAlias {
					value.LabelField = &newAlias
				}
			}
		}
	}
	pruneCanonicalGridCellContent(visual)
}
