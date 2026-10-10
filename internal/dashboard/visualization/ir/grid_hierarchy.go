package ir

import "fmt"

// Hierarchy field names refer to delivered row aliases, so runtime sorting and
// pivoting cannot drop the identity or children needed to reconstruct the tree.
func validateGridRowHierarchy(spec VisualizationSpec) error {
	var hierarchy *GridRowHierarchy
	var rows []VisualizationFieldRef
	switch value := spec.Value.(type) {
	case *TableVisualizationSpec:
		if value.Presentation.Hierarchy != nil {
			return fmt.Errorf("row hierarchy is supported only for matrix and pivot visuals")
		}
		return nil
	case *MatrixVisualizationSpec:
		hierarchy, rows = value.Presentation.Hierarchy, value.Rows
	case *PivotVisualizationSpec:
		hierarchy, rows = value.Presentation.Hierarchy, value.Rows
	default:
		return nil
	}
	if hierarchy == nil {
		return nil
	}
	base, err := hierarchy.Base()
	if err != nil {
		return err
	}
	if base.DefaultExpandedDepth != nil && *base.DefaultExpandedDepth < 0 {
		return fmt.Errorf("hierarchy.defaultExpandedDepth must be non-negative")
	}
	delivered := map[string]bool{}
	for _, row := range rows {
		delivered[row.Field] = true
	}
	require := func(field, property string) error {
		if field == "" || !delivered[field] {
			return fmt.Errorf("hierarchy.%s %q must name a query row field alias", property, field)
		}
		return nil
	}
	switch value := hierarchy.Value.(type) {
	case *GridLevelHierarchy:
		if len(value.Fields) == 0 {
			return fmt.Errorf("hierarchy.fields must contain at least one row field")
		}
		seen := map[string]bool{}
		for _, field := range value.Fields {
			if err := require(field, "fields"); err != nil {
				return err
			}
			if seen[field] {
				return fmt.Errorf("hierarchy.fields contains duplicate field %q", field)
			}
			seen[field] = true
		}
	case *GridParentChildHierarchy:
		for _, entry := range []struct{ field, property string }{{value.IDField, "idField"}, {value.ParentField, "parentField"}, {value.LabelField, "labelField"}} {
			if err := require(entry.field, entry.property); err != nil {
				return err
			}
		}
		if value.IDField == value.ParentField {
			return fmt.Errorf("hierarchy.idField and parentField must be different")
		}
	case *GridNestedHierarchy:
		for _, entry := range []struct{ field, property string }{{value.ChildrenField, "childrenField"}, {value.LabelField, "labelField"}} {
			if err := require(entry.field, entry.property); err != nil {
				return err
			}
		}
		if value.IDField != nil {
			if err := require(*value.IDField, "idField"); err != nil {
				return err
			}
		}
		if value.ChildrenField == value.LabelField || (value.IDField != nil && value.ChildrenField == *value.IDField) {
			return fmt.Errorf("hierarchy.childrenField must be different from labelField and idField")
		}
	default:
		return fmt.Errorf("unsupported grid row hierarchy %T", value)
	}
	return nil
}
