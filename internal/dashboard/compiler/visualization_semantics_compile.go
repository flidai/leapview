package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func compiledGridDataType(column dashboard.TableColumn) visualizationir.VisualizationDataType {
	if column.DataType != "" {
		switch visualizationir.VisualizationDataType(column.DataType) {
		case visualizationir.VisualizationDataTypeString, visualizationir.VisualizationDataTypeInteger, visualizationir.VisualizationDataTypeDecimal, visualizationir.VisualizationDataTypeFloat, visualizationir.VisualizationDataTypeBoolean, visualizationir.VisualizationDataTypeDate, visualizationir.VisualizationDataTypeTemporal, visualizationir.VisualizationDataTypeGeographic:
			return visualizationir.VisualizationDataType(column.DataType)
		}
	}
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
		bindings = append(bindings, compiledFields(authored.Query.Metrics)...)
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
				column.DataType = string(compiledDimensionDataType(dimension))
				column.Format = compiledPhysicalFieldFormat(model, binding.FieldID, string(dimension.Datatype))
				if dimension.Label != "" {
					column.Label = dimension.Label
				}
			} else if metric, ok := model.Metrics[binding.FieldID]; ok {
				column.Role, column.Align, column.Metric = "metric", "right", binding.Alias
				column.DataType = string(compiledMetricDataType(model, metric))
				if metric.Label != "" {
					column.Label = metric.Label
				}
				column.Format = compiledMetricFormat(metric.Format)
			} else if metric, err := model.ResolveMetric(binding.FieldID); err == nil {
				column.Role, column.Align, column.Metric = "metric", "right", binding.Alias
				column.DataType = string(compiledMetricDataType(model, metric))
				if metric.Label != "" {
					column.Label = metric.Label
				}
				column.Format = compiledMetricFormat(metric.Format)
			}
		}
		if override, ok := overrides[binding.Alias]; ok {
			column = mergeCompiledTableColumn(column, override)
		}
		if rules := authored.MetricFormatting[binding.FieldID]; len(rules) > 0 {
			column.Formatting = append([]dashboard.TableFormattingRule(nil), rules...)
		}
		out = append(out, column)
	}
	return out
}

func compiledDimensionFormat(semanticType string) string {
	switch semanticType {
	case string(semanticmodel.DataTypeInteger), string(semanticmodel.DataTypeDecimal), string(semanticmodel.DataTypeFloat):
		return "decimal"
	case string(semanticmodel.DataTypeBoolean):
		return "boolean"
	case string(semanticmodel.DataTypeDate):
		return "date"
	case string(semanticmodel.DataTypeTime), string(semanticmodel.DataTypeDateTime), string(semanticmodel.DataTypeDateTimeTZ):
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
	for _, measureID := range sortedMapKeys(model.Metrics) {
		metric := model.Metrics[measureID]
		if metric.Input != nil && metric.Input.Field == fieldID && (metric.Aggregation == "sum" || metric.Aggregation == "avg" || metric.Aggregation == "min" || metric.Aggregation == "max") {
			return compiledMetricFormat(metric.Format)
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
	if override.Metric != "" {
		base.Metric = override.Metric
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

func compiledMetricFormat(value string) string {
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
	var metric dashboardauthoring.FieldRef
	if len(authored.Query.Metrics) > 0 {
		metric = authored.Query.Metrics[0]
	}

	switch shape {
	case "point":
		for _, binding := range authored.Query.Dimensions {
			decorate(compiledAlias(binding.Field, binding.Alias), binding)
		}
		if authored.Query.Time.Field != "" {
			decorate(compiledAlias(authored.Query.Time.Field, authored.Query.Time.Alias), dashboardauthoring.FieldRef{Field: authored.Query.Time.Field, Alias: authored.Query.Time.Alias})
		}
		for _, binding := range authored.Query.Metrics {
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
	// A normalized multi-metric frame stores heterogeneous metrics in one
	// value column. Do not attach one metric's format or source identity to all
	// rows; row-specific formatting requires a future typed series-format map.
	if shape != "category_multi_measure" {
		for _, id := range []string{"value", "start", "end", "binStart", "binEnd"} {
			decorate(id, metric)
		}
		if shape == "binned_measure" {
			// Histogram boundaries are renderer geometry produced by binning;
			// they are intentionally approximate even when the source metric is
			// transported as exact Decimal.
			for _, id := range []string{"binStart", "binEnd"} {
				if field := byID[id]; field != nil {
					field.DataType = visualizationir.VisualizationDataTypeFloat
					field.Format = nil
				}
			}
		}
	} else if valueField := byID["value"]; valueField != nil {
		// A heterogeneous value column must use one transport type for all
		// governed metrics. Integer values are promoted to Decimal strings at
		// frame construction; Float/Decimal mixtures are rejected above.
		hasFloat, hasDecimal, hasInteger := false, false, false
		for _, binding := range authored.Query.Metrics {
			if resolved, err := model.ResolveMetric(binding.Field); err == nil {
				switch compiledMetricDataType(model, resolved) {
				case visualizationir.VisualizationDataTypeFloat:
					hasFloat = true
				case visualizationir.VisualizationDataTypeDecimal:
					hasDecimal = true
				case visualizationir.VisualizationDataTypeInteger:
					hasInteger = true
				}
			}
		}
		if hasFloat && !hasDecimal && !hasInteger {
			valueField.DataType = visualizationir.VisualizationDataTypeFloat
		} else if hasInteger && !hasDecimal && !hasFloat {
			valueField.DataType = visualizationir.VisualizationDataTypeInteger
		}
	}
	if shape == "ohlc" || shape == "distribution" {
		// Distribution quantiles are all derived from the one raw metric. Carry
		// its semantic numeric type across every output statistic so Float does
		// not regress to the shape's Decimal defaults.
		if shape == "distribution" && len(authored.Query.Metrics) == 1 {
			binding := authored.Query.Metrics[0]
			for _, alias := range []string{"min", "q1", "median", "q3", "max"} {
				if byID[alias] != nil {
					decorate(alias, binding)
				}
			}
		}
		for index, binding := range authored.Query.Metrics {
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
		field.DataType = compiledDimensionDataType(dimension)
		field.Format = compiledVisualizationFormat(compiledDimensionFormat(string(dimension.Datatype)), "")
		return
	}
	if metric, err := model.ResolveMetric(source); err == nil {
		if metric.Label != "" {
			field.Label = metric.Label
		}
		field.DataType = compiledMetricDataType(model, metric)
		field.Format = compiledVisualizationFormat(metric.Format, metric.Unit)
		return
	}
	if metric, ok := model.Metrics[source]; ok {
		if metric.Label != "" {
			field.Label = metric.Label
		}
		field.DataType = compiledMetricDataType(model, metric)
		field.Format = compiledVisualizationFormat(metric.Format, metric.Unit)
	}
}

func compiledDimensionDataType(dimension semanticmodel.MetricDimension) visualizationir.VisualizationDataType {
	switch dimension.Datatype {
	case semanticmodel.DataTypeBoolean:
		return visualizationir.VisualizationDataTypeBoolean
	case semanticmodel.DataTypeDate:
		return visualizationir.VisualizationDataTypeDate
	case semanticmodel.DataTypeTime, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return visualizationir.VisualizationDataTypeTemporal
	case semanticmodel.DataTypeInteger:
		return visualizationir.VisualizationDataTypeInteger
	case semanticmodel.DataTypeDecimal:
		return visualizationir.VisualizationDataTypeDecimal
	case semanticmodel.DataTypeFloat:
		return visualizationir.VisualizationDataTypeFloat
	case semanticmodel.DataTypeString:
		return visualizationir.VisualizationDataTypeString
	case semanticmodel.DataTypeOpaque:
		return visualizationir.VisualizationDataTypeString
	}
	return visualizationir.VisualizationDataTypeString
}

func compiledMetricDataType(model *semanticmodel.Model, metric semanticmodel.Metric) visualizationir.VisualizationDataType {
	if model == nil {
		return visualizationir.VisualizationDataTypeDecimal
	}
	var dataType semanticmodel.LogicalDataType
	var err error
	if strings.TrimSpace(metric.Name) != "" {
		dataType, err = model.MetricDataType(metric.Name)
	} else {
		dataType, err = model.MetricDataTypeFor(metric)
	}
	if err != nil {
		return visualizationir.VisualizationDataTypeDecimal
	}
	switch dataType {
	case semanticmodel.DataTypeInteger:
		return visualizationir.VisualizationDataTypeInteger
	case semanticmodel.DataTypeDecimal:
		return visualizationir.VisualizationDataTypeDecimal
	case semanticmodel.DataTypeFloat:
		return visualizationir.VisualizationDataTypeFloat
	default:
		return visualizationir.VisualizationDataTypeString
	}
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

func validateVisualizationMetricTypes(authored dashboardauthoring.Visual, model *semanticmodel.Model) error {
	if model == nil || authored.ResultShape() != "category_multi_measure" {
		return nil
	}
	hasFloat, hasDecimal, hasInteger := false, false, false
	for _, binding := range authored.Query.Metrics {
		metric, err := model.ResolveMetric(binding.Field)
		if err != nil {
			continue
		}
		switch compiledMetricDataType(model, metric) {
		case visualizationir.VisualizationDataTypeFloat:
			hasFloat = true
		case visualizationir.VisualizationDataTypeDecimal:
			hasDecimal = true
		case visualizationir.VisualizationDataTypeInteger:
			hasInteger = true
		}
	}
	if hasFloat && (hasDecimal || hasInteger) {
		return fmt.Errorf("category_multi_measure cannot mix Float with exact numeric metrics in one value column")
	}
	return nil
}
