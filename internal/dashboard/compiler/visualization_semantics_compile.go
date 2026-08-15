package compiler

import (
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func compiledGridDataType(column dashboard.TableColumn) visualizationir.VisualizationDataType {
	switch column.Format {
	case "integer", "days":
		return visualizationir.VisualizationDataTypeInteger
	case "decimal", "currency":
		return visualizationir.VisualizationDataTypeDecimal
	case "boolean":
		return visualizationir.VisualizationDataTypeBoolean
	case "date":
		return visualizationir.VisualizationDataTypeDate
	case "timestamp":
		return visualizationir.VisualizationDataTypeTemporal
	default:
		return visualizationir.VisualizationDataTypeString
	}
}

func compiledGridFormat(column dashboard.TableColumn) *visualizationir.VisualizationFormat {
	var format visualizationir.VisualizationFormat
	switch column.Format {
	case "integer", "decimal":
		format.Value = &visualizationir.NumberVisualizationFormat{Kind: "number"}
	case "currency":
		format.Value = &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: "BRL"}
	case "days":
		format.Value = &visualizationir.DurationVisualizationFormat{Kind: "duration", Unit: "days"}
	case "date", "timestamp":
		format.Value = &visualizationir.TemporalVisualizationFormat{Kind: "temporal"}
	default:
		return nil
	}
	return &format
}

func compiledGridFormatting(rules []dashboard.TableFormattingRule) []visualizationir.TableVisualizationFormattingRule {
	out := make([]visualizationir.TableVisualizationFormattingRule, 0, len(rules))
	for _, rule := range rules {
		switch rule.Kind {
		case "badge":
			out = append(out, visualizationir.TableVisualizationFormattingRule{Value: &visualizationir.TableBadgeFormattingRule{Kind: rule.Kind, Values: cloneStringMap(rule.Values)}})
		case "text_color":
			values := cloneStringMap(rule.Values)
			var mappedValues *map[string]string
			if len(values) > 0 {
				mappedValues = &values
			}
			out = append(out, visualizationir.TableVisualizationFormattingRule{Value: &visualizationir.TableTextColorFormattingRule{Kind: rule.Kind, Color: rule.Color, Values: mappedValues, Minimum: rule.Min, Maximum: rule.Max}})
		case "background_scale":
			out = append(out, visualizationir.TableVisualizationFormattingRule{Value: &visualizationir.TableBackgroundScaleFormattingRule{Kind: rule.Kind, Minimum: rule.Min, Maximum: rule.Max, LowColor: optionalString(rule.LowColor), HighColor: optionalString(rule.HighColor)}})
		case "data_bar":
			out = append(out, visualizationir.TableVisualizationFormattingRule{Value: &visualizationir.TableDataBarFormattingRule{Kind: rule.Kind, Minimum: rule.Min, Maximum: rule.Max, Color: rule.Color, Background: optionalString(rule.Background)}})
		}
	}
	return out
}

func compiledDashboardTableColumns(visualType string, authored dashboardauthoring.TableVisual, model *semanticmodel.Model) []dashboard.TableColumn {
	bindings := compiledTableFields(authored)
	if visualType != "table" {
		bindings = append(compiledFields(authored.Query.Rows), compiledFields(authored.Query.Columns)...)
		bindings = append(bindings, compiledFields(authored.Query.Measures)...)
	}
	overrides := make(map[string]dashboard.TableColumn, len(authored.Columns))
	for _, column := range authored.Columns {
		overrides[column.Key] = column
	}
	out := make([]dashboard.TableColumn, 0, len(bindings))
	for _, binding := range bindings {
		column := dashboard.TableColumn{Key: binding.Alias, Label: binding.Alias}
		if model != nil {
			if dimension, err := model.ResolveDimension(binding.FieldID); err == nil {
				column.Role = "row_header"
				column.Format = compiledPhysicalFieldFormat(model, binding.FieldID, dimension.Type)
				if dimension.Label != "" {
					column.Label = dimension.Label
				}
			} else if measure, err := model.ResolveMeasure(binding.FieldID); err == nil {
				column.Role, column.Align, column.Measure = "measure", "right", binding.Alias
				if measure.Label != "" {
					column.Label = measure.Label
				}
				column.Format = compiledMeasureFormat(measure.Format)
			}
		}
		if override, ok := overrides[binding.Alias]; ok {
			column = mergeCompiledTableColumn(column, override)
		}
		if rules := authored.MeasureFormatting[binding.FieldID]; len(rules) > 0 {
			column.Formatting = append([]dashboard.TableFormattingRule(nil), rules...)
		}
		out = append(out, column)
	}
	return out
}

func compiledDimensionFormat(semanticType string) string {
	switch semanticType {
	case "number":
		return "decimal"
	case "boolean":
		return "boolean"
	case "date":
		return "date"
	case "timestamp":
		return "timestamp"
	default:
		return ""
	}
}

func compiledPhysicalFieldFormat(model *semanticmodel.Model, fieldID, semanticType string) string {
	if format := compiledDimensionFormat(semanticType); format != "" {
		return format
	}
	if model == nil {
		return ""
	}
	for _, measureID := range sortedMapKeys(model.Measures) {
		measure := model.Measures[measureID]
		if measure.Input.Field == fieldID && (measure.Aggregation == "sum" || measure.Aggregation == "avg" || measure.Aggregation == "min" || measure.Aggregation == "max") {
			return compiledMeasureFormat(measure.Format)
		}
	}
	return ""
}

func mergeCompiledTableColumn(base, override dashboard.TableColumn) dashboard.TableColumn {
	if override.Label != "" {
		base.Label = override.Label
	}
	if override.Align != "" {
		base.Align = override.Align
	}
	if override.Role != "" {
		base.Role = override.Role
	}
	if override.Group != "" {
		base.Group = override.Group
	}
	if override.Measure != "" {
		base.Measure = override.Measure
	}
	if override.ColumnValue != "" {
		base.ColumnValue = override.ColumnValue
	}
	if override.Width > 0 {
		base.Width = override.Width
	}
	if override.Format != "" {
		base.Format = override.Format
	}
	if len(override.Formatting) > 0 {
		base.Formatting = append([]dashboard.TableFormattingRule(nil), override.Formatting...)
	}
	return base
}

func compiledMeasureFormat(value string) string {
	switch value {
	case "integer", "currency":
		return value
	default:
		return "decimal"
	}
}

func applyBuiltInFieldSemantics(fields []visualizationir.VisualizationField, shape string, authored dashboardauthoring.Visual, model *semanticmodel.Model) {
	if model == nil {
		return
	}
	byID := make(map[string]*visualizationir.VisualizationField, len(fields))
	for index := range fields {
		byID[fields[index].ID] = &fields[index]
	}
	decorate := func(id string, binding dashboardauthoring.FieldRef) {
		field := byID[id]
		if field == nil || strings.TrimSpace(binding.Field) == "" {
			return
		}
		applySemanticField(field, binding.Field, model)
	}
	var dimension dashboardauthoring.FieldRef
	if len(authored.Query.Dimensions) > 0 {
		dimension = authored.Query.Dimensions[0]
	} else if authored.Query.Time.Field != "" {
		dimension = dashboardauthoring.FieldRef{Field: authored.Query.Time.Field, Alias: authored.Query.Time.Alias}
	}
	var measure dashboardauthoring.FieldRef
	if len(authored.Query.Measures) > 0 {
		measure = authored.Query.Measures[0]
	}

	switch shape {
	case "point":
		for _, binding := range authored.Query.Dimensions {
			decorate(compiledAlias(binding.Field, binding.Alias), binding)
		}
		if authored.Query.Time.Field != "" {
			decorate(compiledAlias(authored.Query.Time.Field, authored.Query.Time.Alias), dashboardauthoring.FieldRef{Field: authored.Query.Time.Field, Alias: authored.Query.Time.Alias})
		}
		for _, binding := range authored.Query.Measures {
			decorate(compiledAlias(binding.Field, binding.Alias), binding)
		}
	case "single_value", "category_value", "category_series_value", "category_multi_measure", "category_delta", "binned_measure", "ohlc", "distribution":
		decorate("label", dimension)
	case "matrix":
		decorate("row", dimension)
	case "graph":
		if len(authored.Query.Dimensions) > 0 {
			decorate("source", authored.Query.Dimensions[0])
		}
		if len(authored.Query.Dimensions) > 1 {
			decorate("target", authored.Query.Dimensions[1])
		}
	}
	if !authored.Query.Series.IsZero() {
		decorate("series", authored.Query.Series)
	}
	// A normalized multi-measure frame stores heterogeneous measures in one
	// value column. Do not attach one measure's format or source identity to all
	// rows; row-specific formatting requires a future typed series-format map.
	if shape != "category_multi_measure" {
		for _, id := range []string{"value", "start", "end", "binStart", "binEnd"} {
			decorate(id, measure)
		}
	}
	if shape == "ohlc" || shape == "distribution" {
		for index, binding := range authored.Query.Measures {
			alias := binding.Alias
			if alias == "" {
				alias = fieldAlias(binding.Field)
			}
			if byID[alias] != nil {
				decorate(alias, binding)
				continue
			}
			ordered := map[string][]string{"ohlc": {"open", "close", "low", "high"}, "distribution": {"min", "q1", "median", "q3", "max"}}[shape]
			if index < len(ordered) {
				decorate(ordered[index], binding)
			}
		}
	}
}

func compiledAlias(field, alias string) string {
	if alias != "" {
		return alias
	}
	return fieldAlias(field)
}

func applySemanticField(field *visualizationir.VisualizationField, source string, model *semanticmodel.Model) {
	field.SourceRef = &source
	if dimension, err := model.ResolveDimension(source); err == nil {
		if dimension.Label != "" {
			field.Label = dimension.Label
		}
		field.DataType = compiledDimensionDataType(dimension.Type)
		field.Format = compiledVisualizationFormat(compiledDimensionFormat(dimension.Type), "")
		return
	}
	if measure, err := model.ResolveMeasure(source); err == nil {
		if measure.Label != "" {
			field.Label = measure.Label
		}
		field.DataType = compiledMeasureDataType(measure.Format)
		field.Format = compiledVisualizationFormat(measure.Format, measure.Unit)
		return
	}
	if metric, ok := model.Metrics[source]; ok {
		if metric.Label != "" {
			field.Label = metric.Label
		}
		field.DataType = compiledMeasureDataType(metric.Format)
		field.Format = compiledVisualizationFormat(metric.Format, metric.Unit)
	}
}

func compiledDimensionDataType(dimensionType string) visualizationir.VisualizationDataType {
	switch dimensionType {
	case "boolean":
		return visualizationir.VisualizationDataTypeBoolean
	case "date":
		return visualizationir.VisualizationDataTypeDate
	case "timestamp":
		return visualizationir.VisualizationDataTypeTemporal
	case "number":
		return visualizationir.VisualizationDataTypeDecimal
	default:
		return visualizationir.VisualizationDataTypeString
	}

}

func compiledMeasureDataType(format string) visualizationir.VisualizationDataType {
	if format == "integer" {
		return visualizationir.VisualizationDataTypeInteger
	}
	return visualizationir.VisualizationDataTypeDecimal
}

func compiledVisualizationFormat(format, unit string) *visualizationir.VisualizationFormat {
	var value visualizationir.VisualizationFormat
	switch format {
	case "integer", "decimal":
		value.Value = &visualizationir.NumberVisualizationFormat{Kind: "number"}
	case "currency":
		currency := "BRL"
		switch strings.TrimSpace(unit) {
		case "$", "USD":
			currency = "USD"
		case "€", "EUR":
			currency = "EUR"
		}
		value.Value = &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: currency}
	case "date", "timestamp":
		value.Value = &visualizationir.TemporalVisualizationFormat{Kind: "temporal"}
	default:
		return nil
	}
	return &value
}
