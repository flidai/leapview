package compiler

import (
	"fmt"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func compileTabularVisualizationSpec(id, visualType string, authored dashboardauthoring.TableVisual, columns []dashboard.TableColumn, binding visualizationdefinition.QueryBinding) (visualizationir.VisualizationSpec, error) {
	if len(columns) == 0 {
		return visualizationir.VisualizationSpec{}, fmt.Errorf("tabular visualization requires at least one compiled column")
	}
	style := authored.Style.WithDefaults()
	title := firstNonEmpty(authored.Title, id)
	identityAliases := map[string]struct{}{}
	for _, mapping := range authored.Interaction.RowSelection.Mappings {
		identityAliases[mapping.Value] = struct{}{}
	}
	sourceByAlias := map[string]string{}
	for _, field := range queryBindingFields(binding) {
		sourceByAlias[field.Alias] = field.FieldID
	}
	fields := make([]visualizationir.VisualizationField, len(columns))
	tableColumns := make([]visualizationir.TableVisualizationColumn, len(columns))
	for index, column := range columns {
		role := visualizationir.VisualizationFieldRoleDimension
		if _, ok := identityAliases[column.Key]; ok {
			role = visualizationir.VisualizationFieldRoleIdentity
		} else if column.Role == "measure" || column.Align == "right" {
			role = visualizationir.VisualizationFieldRoleMeasure
		}
		fields[index] = visualizationir.VisualizationField{
			ID: column.Key, Role: role, DataType: compiledGridDataType(column), Nullable: true,
			Label: firstNonEmpty(column.Label, column.Key), SourceRef: optionalString(sourceByAlias[column.Key]), Format: compiledGridFormat(column),
			Grid: &visualizationir.VisualizationGridFieldMetadata{Group: optionalString(column.Group), Measure: optionalString(column.Measure), ColumnValue: optionalString(column.ColumnValue), Formatting: compiledGridFormatting(column.Formatting)},
		}
		tableColumns[index] = visualizationir.TableVisualizationColumn{
			Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: column.Key}, Label: firstNonEmpty(column.Label, column.Key),
			Group: optionalString(column.Group), Measure: optionalString(column.Measure), ColumnValue: optionalString(column.ColumnValue),
			Formatting: compiledGridFormatting(column.Formatting),
		}
		if column.Width > 0 {
			width := int64(column.Width)
			tableColumns[index].Width = &width
		}
	}
	base := visualizationir.VisualizationSpecBase{
		Kind: visualType, Title: title,
		Datasets:      []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
		DataBudget:    visualizationir.VisualizationDataBudget{MaxRows: dashboard.TableInteractiveRowCap, RequiredCompleteness: visualizationir.VisualizationCompletenessPartial},
		Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: firstNonEmpty(authored.Description, title)},
		Interactions:  compiledSelectionInteractions("row_selection", authored.Interaction.RowSelection),
	}
	fieldIDs := make([]string, len(fields))
	for index, field := range fields {
		fieldIDs[index] = field.ID
	}
	conditionalFormatting, err := compileConditionalFormatting(fieldIDs, authored.ConditionalFormatting)
	if err != nil {
		return visualizationir.VisualizationSpec{}, err
	}
	base.ConditionalFormatting = conditionalFormatting
	presentation := visualizationir.GridVisualizationPresentation{RowHeight: int64(style.RowHeight()), Striped: style.Zebra == nil || *style.Zebra, ShowHeader: true}
	refs := func(values []visualizationdefinition.FieldBinding) []visualizationir.VisualizationFieldRef {
		out := make([]visualizationir.VisualizationFieldRef, len(values))
		for index, field := range values {
			out[index] = visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field.Alias}
		}
		return out
	}
	formatting := map[string][]visualizationir.TableVisualizationFormattingRule{}
	for field, rules := range authored.MeasureFormatting {
		formatting[fieldAlias(field)] = compiledGridFormatting(rules)
	}
	switch visualType {
	case "matrix":
		base.Kind = "matrix"
		return visualizationir.VisualizationSpec{Value: &visualizationir.MatrixVisualizationSpec{VisualizationSpecBase: base, Kind: "matrix", Rows: refs(binding.Matrix.Rows), Columns: refs(binding.Matrix.Columns), Measures: refs(binding.Matrix.Measures), MeasureFormatting: formatting, Presentation: presentation}}, nil
	case "pivot":
		base.Kind = "pivot"
		return visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{VisualizationSpecBase: base, Kind: "pivot", Rows: refs(binding.Pivot.Rows), Columns: refs(binding.Pivot.Columns), Measures: refs(binding.Pivot.Measures), MeasureFormatting: formatting, Presentation: presentation}}, nil
	default:
		sortKey := authored.DefaultSort.Key
		if sortKey == "" {
			sortKey = columns[0].Key
		}
		sort := []visualizationir.VisualizationSort{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: sortKey}, Direction: compiledGridSortDirection(authored.DefaultSort.Direction)}}
		return visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: tableColumns, DefaultSort: &sort, Presentation: presentation}}, nil
	}
}

func queryBindingFields(binding visualizationdefinition.QueryBinding) []visualizationdefinition.FieldBinding {
	switch binding.Kind {
	case visualizationdefinition.QueryDetail:
		return append([]visualizationdefinition.FieldBinding(nil), binding.Detail.Fields...)
	case visualizationdefinition.QueryMatrix:
		fields := append([]visualizationdefinition.FieldBinding(nil), binding.Matrix.Rows...)
		fields = append(fields, binding.Matrix.Columns...)
		return append(fields, binding.Matrix.Measures...)
	case visualizationdefinition.QueryPivot:
		fields := append([]visualizationdefinition.FieldBinding(nil), binding.Pivot.Rows...)
		fields = append(fields, binding.Pivot.Columns...)
		return append(fields, binding.Pivot.Measures...)
	default:
		return nil
	}
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func compiledGridSortDirection(value string) visualizationir.VisualizationSortDirection {
	if value == "desc" {
		return visualizationir.VisualizationSortDirectionDescending
	}
	return visualizationir.VisualizationSortDirectionAscending
}
