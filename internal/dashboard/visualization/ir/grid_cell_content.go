package ir

import "fmt"

// Cell URLs and their optional labels must already be present in governed rows.
// Presentation never adds fields to the query or changes its access boundary.
func validateGridCellContent(spec VisualizationSpec, schemas map[string]VisualizationDatasetSchema) error {
	var presentation GridVisualizationPresentation
	var delivered []VisualizationFieldRef
	var authored map[string]*TableCellContent
	switch value := spec.Value.(type) {
	case *TableVisualizationSpec:
		presentation = value.Presentation
		authored = map[string]*TableCellContent{}
		for _, column := range value.Columns {
			delivered = append(delivered, column.Field)
			if column.Content != nil {
				authored[column.Field.Field] = column.Content
			}
		}
	case *MatrixVisualizationSpec:
		presentation, delivered = value.Presentation, value.Rows
	case *PivotVisualizationSpec:
		presentation, delivered = value.Presentation, value.Rows
	default:
		return nil
	}
	refs := map[string]VisualizationFieldRef{}
	for _, ref := range delivered {
		refs[ref.Field] = ref
	}
	collapsed := map[string]bool{}
	if presentation.Hierarchy != nil {
		switch hierarchy := presentation.Hierarchy.Value.(type) {
		case *GridLevelHierarchy:
			for _, field := range hierarchy.Fields {
				collapsed[field] = true
			}
		case *GridParentChildHierarchy:
			for _, field := range []string{hierarchy.IDField, hierarchy.ParentField} {
				if field != hierarchy.LabelField {
					collapsed[field] = true
				}
			}
		case *GridNestedHierarchy:
			collapsed[hierarchy.ChildrenField] = true
			if hierarchy.IDField != nil && *hierarchy.IDField != hierarchy.LabelField {
				collapsed[*hierarchy.IDField] = true
			}
		}
	}
	validate := func(alias string, content TableCellContent) error {
		ref, exists := refs[alias]
		if !exists {
			return fmt.Errorf("cellContent field %q must name a visible table column or matrix/pivot row alias", alias)
		}
		if collapsed[alias] {
			return fmt.Errorf("cellContent field %q is collapsed by the hierarchy; use a separate row field or the parent-child/nested label field", alias)
		}
		var field *VisualizationField
		for _, candidate := range schemas[ref.Dataset].Fields {
			if candidate.ID == ref.Field {
				copy := candidate
				field = &copy
				break
			}
		}
		if field == nil || field.DataType != VisualizationDataTypeString {
			return fmt.Errorf("cellContent field %q must contain string URLs", alias)
		}
		requireAuxiliary := func(name *string, property string) error {
			if name == nil {
				return nil
			}
			auxiliary, ok := refs[*name]
			if !ok || *name == "" || auxiliary.Dataset != ref.Dataset {
				return fmt.Errorf("cellContent.%s %q must name a delivered field in the same dataset", property, *name)
			}
			return nil
		}
		switch value := content.Value.(type) {
		case *TableImageCellContent:
			if value == nil {
				return fmt.Errorf("image cell content is required")
			}
			if value.Display != TableImageDisplayInline && value.Display != TableImageDisplayTooltip {
				return fmt.Errorf("image cell display must be inline or tooltip")
			}
			for _, size := range []*int32{value.Width, value.Height} {
				if size != nil && (*size < 1 || *size > 512) {
					return fmt.Errorf("image cell dimensions must be between 1 and 512 pixels")
				}
			}
			return requireAuxiliary(value.AltField, "altField")
		case *TableLinkCellContent:
			if value == nil {
				return fmt.Errorf("link cell content is required")
			}
			return requireAuxiliary(value.LabelField, "labelField")
		default:
			return fmt.Errorf("unsupported cellContent kind %T", value)
		}
	}
	if presentation.CellContent != nil {
		for alias, content := range *presentation.CellContent {
			if err := validate(alias, content); err != nil {
				return err
			}
		}
	}
	for alias, content := range authored {
		if err := validate(alias, *content); err != nil {
			return err
		}
	}
	return nil
}
