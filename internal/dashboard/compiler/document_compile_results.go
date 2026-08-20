package compiler

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalBindingRef(query LoweredDashboardQuery, metric bool, index int) visualizationir.VisualizationFieldRef {
	if query.Binding.Aggregate != nil {
		fields := query.Binding.Aggregate.Dimensions
		if metric {
			fields = query.Binding.Aggregate.Metrics
			if len(fields) == 0 && query.Binding.Aggregate.Histogram != nil {
				fields = []visualizationdefinition.FieldBinding{query.Binding.Aggregate.Histogram.Metric}
			}
			if len(fields) == 0 && query.Binding.Aggregate.Distribution != nil {
				fields = []visualizationdefinition.FieldBinding{query.Binding.Aggregate.Distribution.Metric}
			}
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	if query.Binding.Detail != nil && !metric && index >= 0 && index < len(query.Binding.Detail.Fields) {
		return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: query.Binding.Detail.Fields[index].Alias}
	}
	if query.Binding.Pivot != nil {
		fields := query.Binding.Pivot.Rows
		if metric {
			fields = query.Binding.Pivot.Metrics
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	if query.Binding.Matrix != nil {
		fields := query.Binding.Matrix.Rows
		if metric {
			fields = query.Binding.Matrix.Metrics
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	if query.Binding.Spatial != nil {
		fields := query.Binding.Spatial.Dimensions
		if metric {
			fields = query.Binding.Spatial.Metrics
		}
		if index >= 0 && index < len(fields) {
			return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: fields[index].Alias}
		}
	}
	return visualizationir.VisualizationFieldRef{}
}

func bindingRefs(values []visualizationdefinition.FieldBinding) []visualizationir.VisualizationFieldRef {
	refs := make([]visualizationir.VisualizationFieldRef, 0, len(values))
	for _, value := range values {
		refs = append(refs, visualizationir.VisualizationFieldRef{Dataset: "primary", Field: value.Alias})
	}
	return refs
}

func canonicalQueryOperandRefs(query LoweredDashboardQuery) (dimensions, metrics []visualizationir.VisualizationFieldRef) {
	switch {
	case query.Binding.Aggregate != nil:
		dimensions = bindingRefs(query.Binding.Aggregate.Dimensions)
		metrics = bindingRefs(query.Binding.Aggregate.Metrics)
		if len(metrics) == 0 && query.Binding.Aggregate.Histogram != nil {
			metrics = bindingRefs([]visualizationdefinition.FieldBinding{query.Binding.Aggregate.Histogram.Metric})
		}
		if len(metrics) == 0 && query.Binding.Aggregate.Distribution != nil {
			metrics = bindingRefs([]visualizationdefinition.FieldBinding{query.Binding.Aggregate.Distribution.Metric})
		}
	case query.Binding.Detail != nil:
		dimensions = bindingRefs(query.Binding.Detail.Fields)
	case query.Binding.Matrix != nil:
		dimensions = append(bindingRefs(query.Binding.Matrix.Rows), bindingRefs(query.Binding.Matrix.Columns)...)
		metrics = bindingRefs(query.Binding.Matrix.Metrics)
	case query.Binding.Pivot != nil:
		dimensions = append(bindingRefs(query.Binding.Pivot.Rows), bindingRefs(query.Binding.Pivot.Columns)...)
		metrics = bindingRefs(query.Binding.Pivot.Metrics)
	case query.Binding.Spatial != nil:
		dimensions = bindingRefs(query.Binding.Spatial.Dimensions)
		metrics = bindingRefs(query.Binding.Spatial.Metrics)
	}
	return dimensions, metrics
}

func canonicalResultFields(query LoweredDashboardQuery, model *semanticmodel.Model) []visualizationir.VisualizationField {
	fields := make([]visualizationir.VisualizationField, 0, len(query.ResultFrame))
	metricNames := map[string]struct{}{}
	if query.Binding.Aggregate != nil {
		for _, field := range query.Binding.Aggregate.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
		if query.Binding.Aggregate.Histogram != nil {
			metricNames[query.Binding.Aggregate.Histogram.Metric.Alias] = struct{}{}
		}
		if query.Binding.Aggregate.Distribution != nil {
			metricNames[query.Binding.Aggregate.Distribution.Metric.Alias] = struct{}{}
		}
	}
	if query.Binding.Matrix != nil {
		for _, field := range query.Binding.Matrix.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
	}
	if query.Binding.Pivot != nil {
		for _, field := range query.Binding.Pivot.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
	}
	if query.Binding.Spatial != nil {
		for _, field := range query.Binding.Spatial.Metrics {
			metricNames[field.Alias] = struct{}{}
		}
	}
	for i, field := range query.ResultFrame {
		role, typ := visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeString
		if _, ok := metricNames[field.Name]; ok {
			role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
		}
		if query.Type == "records" {
			role, typ = visualizationir.VisualizationFieldRoleDimension, canonicalPhysicalDataType(model, field.Source)
		} else if _, ok := metricNames[field.Name]; !ok {
			typ = canonicalSemanticDataType(model, field.Source, false)
		} else {
			typ = canonicalSemanticDataType(model, field.Source, true)
		}
		if query.Type == "distribution" {
			if i == 0 {
				role, typ = visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeString
			} else {
				role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
			}
		}
		if query.Type == "histogram" {
			switch field.Name {
			case "count":
				role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeInteger
			case "start", "end":
				role, typ = visualizationir.VisualizationFieldRoleMetric, visualizationir.VisualizationDataTypeDecimal
			default:
				role, typ = visualizationir.VisualizationFieldRoleDimension, visualizationir.VisualizationDataTypeString
			}
		}
		var sourceRef *string
		if strings.TrimSpace(field.Source) != "" {
			source := field.Source
			sourceRef = &source
		}
		label := field.Name
		var format *visualizationir.VisualizationFormat
		if query.Type == "records" {
			label = canonicalPhysicalFieldLabel(model, field.Source, label)
		} else if _, metric := metricNames[field.Name]; metric && query.Type != "distribution" && query.Type != "histogram" {
			label, format = canonicalMetricPresentation(model, field.Source, label)
		} else if query.Type != "distribution" && query.Type != "histogram" {
			label = canonicalDimensionLabel(model, field.Source, label)
		}
		fields = append(fields, visualizationir.VisualizationField{ID: field.Name, SourceRef: sourceRef, Role: role, DataType: typ, Nullable: true, Label: label, Format: format})
	}
	if query.Binding.ResultShape == visualizationdefinition.ResultHierarchyNodes {
		mark := "node"
		if query.Binding.Aggregate != nil && len(query.Binding.Aggregate.Dimensions) > 1 {
			mark = "node"
		}
		fields = append(fields,
			visualizationir.VisualizationField{ID: mark, Role: visualizationir.VisualizationFieldRoleIdentity, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: mark},
			visualizationir.VisualizationField{ID: "parent", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: "parent"},
		)
	}
	if query.Binding.ResultShape == visualizationdefinition.ResultCategoryDelta {
		fields = append(fields,
			visualizationir.VisualizationField{ID: "start", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "start"},
			visualizationir.VisualizationField{ID: "end", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "end"},
			visualizationir.VisualizationField{ID: "positive", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeBoolean, Nullable: true, Label: "positive"},
		)
	}
	return fields
}

func canonicalMetricPresentation(model *semanticmodel.Model, name, fallbackLabel string) (string, *visualizationir.VisualizationFormat) {
	if model == nil {
		return fallbackLabel, nil
	}
	metric, ok := model.Metrics[name]
	if !ok {
		return fallbackLabel, nil
	}
	label := fallbackLabel
	if strings.TrimSpace(metric.Label) != "" {
		label = metric.Label
	}
	switch metric.Format {
	case "currency":
		currency := strings.ToUpper(strings.TrimSpace(metric.Unit))
		if currency == "" {
			return label, nil
		}
		minimum, maximum := int32(2), int32(2)
		return label, &visualizationir.VisualizationFormat{Value: &visualizationir.CurrencyVisualizationFormat{Kind: "currency", Currency: currency, MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}}
	case "integer":
		digits := int32(0)
		return label, &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: &digits, MaximumFractionDigits: &digits}}
	case "decimal":
		return label, &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{Kind: "number"}}
	default:
		return label, nil
	}
}

func canonicalDimensionLabel(model *semanticmodel.Model, name, fallback string) string {
	if model == nil {
		return fallback
	}
	dimension, ok := model.Dimensions[name]
	if ok && strings.TrimSpace(dimension.Label) != "" {
		return dimension.Label
	}
	return fallback
}

func canonicalPhysicalFieldLabel(model *semanticmodel.Model, source, fallback string) string {
	if model == nil {
		return fallback
	}
	parts := strings.SplitN(source, ".", 2)
	if len(parts) != 2 {
		return fallback
	}
	table, ok := model.Tables[parts[0]]
	if !ok {
		return fallback
	}
	field, ok := table.Dimensions[parts[1]]
	if ok && strings.TrimSpace(field.Label) != "" {
		return field.Label
	}
	return fallback
}

func canonicalPhysicalDataType(model *semanticmodel.Model, source string) visualizationir.VisualizationDataType {
	if model == nil {
		return visualizationir.VisualizationDataTypeString
	}
	if dimension, err := model.ResolveDimension(source); err == nil {
		return visualizationDataType(dimension.Datatype)
	}
	return visualizationir.VisualizationDataTypeString
}

func canonicalSemanticDataType(model *semanticmodel.Model, source string, metric bool) visualizationir.VisualizationDataType {
	if model == nil {
		return visualizationir.VisualizationDataTypeString
	}
	if metric {
		if datatype, err := model.MetricDataType(source); err == nil {
			return visualizationDataType(datatype)
		}
		return visualizationir.VisualizationDataTypeDecimal
	}
	if dimension, err := model.ResolveSemanticDimension(source); err == nil {
		return visualizationDataType(dimension.Datatype)
	}
	return visualizationir.VisualizationDataTypeString
}

func visualizationDataType(value semanticmodel.LogicalDataType) visualizationir.VisualizationDataType {
	switch value {
	case semanticmodel.DataTypeInteger:
		return visualizationir.VisualizationDataTypeInteger
	case semanticmodel.DataTypeDecimal:
		return visualizationir.VisualizationDataTypeDecimal
	case semanticmodel.DataTypeFloat:
		return visualizationir.VisualizationDataTypeFloat
	case semanticmodel.DataTypeBoolean:
		return visualizationir.VisualizationDataTypeBoolean
	case semanticmodel.DataTypeDate:
		return visualizationir.VisualizationDataTypeDate
	case semanticmodel.DataTypeTime, semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return visualizationir.VisualizationDataTypeTemporal
	default:
		return visualizationir.VisualizationDataTypeString
	}
}

func canonicalResultRef(query LoweredDashboardQuery, dataset, name string) (visualizationir.VisualizationFieldRef, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("compiled result field is required")
	}
	if dataset == "" {
		dataset = "primary"
	}
	if dataset == "primary" {
		for _, field := range query.ResultFrame {
			if field.Name == name {
				return visualizationir.VisualizationFieldRef{Dataset: dataset, Field: name}, nil
			}
		}
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q is not a compiled result field", name)
	}
	return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q cannot resolve secondary dataset %q without its compiled result schema", name, dataset)
}

func canonicalDatasetResultRef(query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema, dataset, name string) (visualizationir.VisualizationFieldRef, error) {
	dataset = strings.TrimSpace(dataset)
	if dataset == "" || dataset == "primary" {
		return canonicalResultRef(query, "primary", name)
	}
	for _, schema := range secondary {
		if schema.ID != dataset {
			continue
		}
		for _, field := range schema.Fields {
			if field.ID == name {
				return visualizationir.VisualizationFieldRef{Dataset: dataset, Field: name}, nil
			}
		}
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q is not a compiled result field in dataset %q", name, dataset)
	}
	return visualizationir.VisualizationFieldRef{}, fmt.Errorf("reference %q targets unknown dataset %q", name, dataset)
}

func canonicalMetadataBindings(value *document.DashboardMetadataBindings, query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationMetadataBindings, error) {
	if value == nil {
		return nil, nil
	}
	convert := func(binding *document.DashboardTextBinding) (*visualizationir.VisualizationTextBinding, error) {
		if binding == nil {
			return nil, nil
		}
		field, err := canonicalDatasetResultRef(query, secondary, binding.Dataset, binding.Field)
		if err != nil {
			return nil, err
		}
		reducer := visualizationir.VisualizationReferenceReducerFirst
		if binding.Reducer != nil {
			reducer = *binding.Reducer
		}
		return &visualizationir.VisualizationTextBinding{Field: field, Reducer: reducer, Prefix: valueOrString(binding.Prefix, ""), Suffix: valueOrString(binding.Suffix, ""), Fallback: valueOrString(binding.Fallback, "")}, nil
	}
	title, err := convert(value.Title)
	if err != nil {
		return nil, err
	}
	subtitle, err := convert(value.Subtitle)
	if err != nil {
		return nil, err
	}
	description, err := convert(value.Description)
	if err != nil {
		return nil, err
	}
	summary, err := convert(value.Summary)
	if err != nil {
		return nil, err
	}
	return &visualizationir.VisualizationMetadataBindings{Title: title, Subtitle: subtitle, Description: description, Summary: summary}, nil
}

func canonicalKPIValueBinding(value *document.DashboardKPIValueBinding, query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationKPIValueBinding, error) {
	if value == nil {
		return nil, nil
	}
	field, err := canonicalDatasetResultRef(query, secondary, value.Dataset, value.Field)
	if err != nil {
		return nil, err
	}
	reducer := visualizationir.VisualizationReferenceReducerFirst
	if value.Reducer != nil {
		reducer = *value.Reducer
	}
	return &visualizationir.VisualizationKPIValueBinding{Field: field, Reducer: reducer, Label: value.Label}, nil
}

func canonicalKPITrendBinding(value *document.DashboardKPITrendBinding, query LoweredDashboardQuery, secondary []visualizationir.VisualizationDatasetSchema) (*visualizationir.VisualizationKPITrendBinding, error) {
	if value == nil {
		return nil, nil
	}
	category, err := canonicalDatasetResultRef(query, secondary, value.Dataset, value.Category)
	if err != nil {
		return nil, err
	}
	metric, err := canonicalDatasetResultRef(query, secondary, value.Dataset, value.Value)
	if err != nil {
		return nil, err
	}
	return &visualizationir.VisualizationKPITrendBinding{Category: category, Value: metric}, nil
}

func canonicalCalculations(values *[]document.DashboardCalculation, query LoweredDashboardQuery) (*[]visualizationir.VisualizationCalculation, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]visualizationir.VisualizationCalculation, len(*values))
	for index, value := range *values {
		source, err := canonicalResultRef(query, "primary", value.Source)
		if err != nil {
			return nil, fmt.Errorf("calculation %d source: %w", index, err)
		}
		calculation := visualizationir.VisualizationCalculation{ID: value.ID, Dataset: "primary", Source: source, Hidden: valueOrBool(value.Hidden), Template: value.Template, OrderBy: []visualizationir.VisualizationCalculationOrder{}, PartitionBy: []visualizationir.VisualizationFieldRef{}}
		calculation.Label = valueOrString(value.Label, value.ID)
		calculation.Axis = visualizationir.VisualizationCalculationAxisRows
		if value.Axis != nil {
			calculation.Axis = *value.Axis
		}
		calculation.Reset = visualizationir.VisualizationCalculationResetNone
		if value.Reset != nil {
			calculation.Reset = *value.Reset
		}
		if value.Window != nil {
			window := int64(*value.Window)
			calculation.Window = &window
		}
		if value.Offset != nil {
			offset := int64(*value.Offset)
			calculation.Offset = &offset
		}
		if value.Parent != nil {
			parent, parentErr := canonicalResultRef(query, "primary", *value.Parent)
			if parentErr != nil {
				return nil, fmt.Errorf("calculation %d parent: %w", index, parentErr)
			}
			calculation.Parent = &parent
		}
		if value.PartitionBy != nil {
			calculation.PartitionBy = make([]visualizationir.VisualizationFieldRef, len(*value.PartitionBy))
			for partIndex, field := range *value.PartitionBy {
				ref, refErr := canonicalResultRef(query, "primary", field)
				if refErr != nil {
					return nil, fmt.Errorf("calculation %d partition %d: %w", index, partIndex, refErr)
				}
				calculation.PartitionBy[partIndex] = ref
			}
		}
		if value.OrderBy != nil {
			calculation.OrderBy = make([]visualizationir.VisualizationCalculationOrder, len(*value.OrderBy))
			for orderIndex, order := range *value.OrderBy {
				ref, refErr := canonicalResultRef(query, "primary", order.Field)
				if refErr != nil {
					return nil, fmt.Errorf("calculation %d order %d: %w", index, orderIndex, refErr)
				}
				direction := visualizationir.VisualizationSortDirectionAscending
				if order.Direction == "desc" {
					direction = visualizationir.VisualizationSortDirectionDescending
				}
				calculation.OrderBy[orderIndex] = visualizationir.VisualizationCalculationOrder{Field: ref, Direction: direction}
			}
		}
		if value.Lookup != nil {
			ref, refErr := canonicalResultRef(query, "primary", value.Lookup.Field)
			if refErr != nil {
				return nil, fmt.Errorf("calculation %d lookup: %w", index, refErr)
			}
			calculation.Lookup = &visualizationir.VisualizationCalculationLookup{Field: ref, Value: value.Lookup.Value}
		}
		result[index] = calculation
	}
	return &result, nil
}

func appendCanonicalCalculationOutputs(spec *visualizationir.VisualizationSpec) error {
	if spec == nil {
		return nil
	}
	base, err := spec.Base()
	if err != nil {
		return err
	}
	if base.Calculations == nil || len(*base.Calculations) == 0 {
		return nil
	}
	var primary *visualizationir.VisualizationDatasetSchema
	for index := range base.Datasets {
		if base.Datasets[index].ID == "primary" {
			primary = &base.Datasets[index]
			break
		}
	}
	if primary == nil {
		return fmt.Errorf("primary dataset is required")
	}
	fields := make(map[string]visualizationir.VisualizationField, len(primary.Fields)+len(*base.Calculations))
	for _, field := range primary.Fields {
		fields[field.ID] = field
	}
	for _, calculation := range *base.Calculations {
		if _, exists := fields[calculation.ID]; exists {
			return fmt.Errorf("calculation output %q collides with an existing result field", calculation.ID)
		}
		source, exists := fields[calculation.Source.Field]
		if !exists {
			return fmt.Errorf("calculation %q references unknown source %q", calculation.ID, calculation.Source.Field)
		}
		calculationID := calculation.ID
		field := visualizationir.VisualizationField{
			ID: calculation.ID, Role: visualizationir.VisualizationFieldRoleMetric,
			DataType: canonicalCalculationDataType(calculation.Template, source.DataType), Nullable: true,
			Label: calculation.Label, Format: calculation.Format,
			Provenance: &visualizationir.VisualizationFieldProvenance{
				Kind:       visualizationir.VisualizationFieldProvenanceKindVisualCalculation,
				SourceRefs: []string{calculation.Source.Field}, CalculationID: &calculationID,
			},
		}
		primary.Fields = append(primary.Fields, field)
		fields[field.ID] = field
		if calculation.Hidden {
			continue
		}
		ref := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field.ID}
		switch value := spec.Value.(type) {
		case *visualizationir.CartesianVisualizationSpec:
			value.Y = append(value.Y, ref)
		case *visualizationir.TableVisualizationSpec:
			value.Columns = append(value.Columns, visualizationir.TableVisualizationColumn{Field: ref, Label: field.Label, Formatting: []visualizationir.TableVisualizationFormattingRule{}})
		case *visualizationir.MatrixVisualizationSpec:
			value.Metrics = append(value.Metrics, ref)
		case *visualizationir.PivotVisualizationSpec:
			value.Metrics = append(value.Metrics, ref)
		default:
			return fmt.Errorf("visible calculation %q is not supported for %T", calculation.ID, spec.Value)
		}
	}
	base.DataBudget.RequiredCompleteness = visualizationir.VisualizationCompletenessPartial
	return nil
}

func canonicalCalculationDataType(template visualizationir.VisualizationCalculationTemplate, source visualizationir.VisualizationDataType) visualizationir.VisualizationDataType {
	switch template {
	case visualizationir.VisualizationCalculationTemplateRank:
		return visualizationir.VisualizationDataTypeInteger
	case visualizationir.VisualizationCalculationTemplateRunningTotal,
		visualizationir.VisualizationCalculationTemplateDifference:
		if source == visualizationir.VisualizationDataTypeInteger {
			return visualizationir.VisualizationDataTypeDecimal
		}
		return source
	case visualizationir.VisualizationCalculationTemplateMovingAverage,
		visualizationir.VisualizationCalculationTemplatePercentageDifference,
		visualizationir.VisualizationCalculationTemplatePercentOfParent,
		visualizationir.VisualizationCalculationTemplatePercentOfGrandTotal,
		visualizationir.VisualizationCalculationTemplateCumulativeContribution:
		return visualizationir.VisualizationDataTypeDecimal
	default:
		return source
	}
}

func valueOrBool(value *bool) bool {
	return value != nil && *value
}
