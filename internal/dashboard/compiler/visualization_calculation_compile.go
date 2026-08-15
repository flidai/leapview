package compiler

import (
	"fmt"
	"regexp"

	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

var visualCalculationIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func compileVisualCalculations(spec visualizationir.VisualizationSpec, authored []dashboardauthoring.VisualCalculation) error {
	base, err := mutableSpecificationBase(spec)
	if err != nil {
		return err
	}
	markCompiledFieldProvenance(base)
	if len(authored) == 0 {
		return nil
	}
	if base.Kind == "geographic" || base.Kind == "custom" {
		return fmt.Errorf("visual calculations are not supported for %s visualizations", base.Kind)
	}
	fieldsByDataset := make(map[string]map[string]visualizationir.VisualizationField, len(base.Datasets))
	for _, dataset := range base.Datasets {
		fields := make(map[string]visualizationir.VisualizationField, len(dataset.Fields))
		for _, field := range dataset.Fields {
			fields[field.ID] = field
		}
		fieldsByDataset[dataset.ID] = fields
	}
	primaryFields := fieldsByDataset["primary"]
	if primaryFields == nil {
		return fmt.Errorf("visual calculations require a primary dataset")
	}
	ids := make(map[string]int, len(authored))
	for index, calculation := range authored {
		if !visualCalculationIDPattern.MatchString(calculation.ID) {
			return fmt.Errorf("calculation %d id %q must be a stable field identifier", index, calculation.ID)
		}
		if _, exists := primaryFields[calculation.ID]; exists {
			return fmt.Errorf("calculation %q collides with compiled field", calculation.ID)
		}
		if _, exists := ids[calculation.ID]; exists {
			return fmt.Errorf("duplicate calculation %q", calculation.ID)
		}
		ids[calculation.ID] = index
	}
	compiled := make([]visualizationir.VisualizationCalculation, len(authored))
	for index, calculation := range authored {
		next, compileErr := compileVisualCalculation(base.Kind, calculation, primaryFields, ids)
		if compileErr != nil {
			return fmt.Errorf("calculation %q: %w", calculation.ID, compileErr)
		}
		compiled[index] = next
	}
	if err := validateCompiledCalculationDependencies(compiled, primaryFields); err != nil {
		return err
	}
	for _, calculation := range compiled {
		source := primaryFields[calculation.Source.Field]
		format := calculation.Format
		if format == nil {
			format = inferredCalculationFormat(calculation.Template, source.Format)
		}
		calculationID := calculation.ID
		field := visualizationir.VisualizationField{
			ID: calculation.ID, Role: visualizationir.VisualizationFieldRoleMeasure, DataType: visualizationir.VisualizationDataTypeDecimal,
			Nullable: true, Label: calculation.Label, Format: format,
			Provenance: &visualizationir.VisualizationFieldProvenance{
				Kind: visualizationir.VisualizationFieldProvenanceKindVisualCalculation, SourceRefs: []string{calculation.Source.Field}, CalculationID: &calculationID,
			},
		}
		appendCalculationField(base, field)
		primaryFields[field.ID] = field
		if !calculation.Hidden {
			appendVisibleCalculationBinding(spec, field)
		}
	}
	// A bounded calculation frame may be complete or may reach its cap. The
	// runtime carries that state and a warning diagnostic; requiring complete
	// data here would replace the useful partial result with an empty error
	// envelope.
	base.DataBudget.RequiredCompleteness = visualizationir.VisualizationCompletenessPartial
	base.Calculations = &compiled
	return nil
}

func compileVisualCalculation(kind string, authored dashboardauthoring.VisualCalculation, fields map[string]visualizationir.VisualizationField, calculationIDs map[string]int) (visualizationir.VisualizationCalculation, error) {
	template := visualizationir.VisualizationCalculationTemplate(authored.Template)
	switch template {
	case visualizationir.VisualizationCalculationTemplateRunningTotal,
		visualizationir.VisualizationCalculationTemplateMovingAverage,
		visualizationir.VisualizationCalculationTemplateDifference,
		visualizationir.VisualizationCalculationTemplatePercentageDifference,
		visualizationir.VisualizationCalculationTemplatePercentOfParent,
		visualizationir.VisualizationCalculationTemplatePercentOfGrandTotal,
		visualizationir.VisualizationCalculationTemplateRank,
		visualizationir.VisualizationCalculationTemplateCumulativeContribution,
		visualizationir.VisualizationCalculationTemplateLookup:
	default:
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("unsupported template %q", authored.Template)
	}
	axis := visualizationir.VisualizationCalculationAxis(authored.Axis)
	if axis == "" {
		axis = visualizationir.VisualizationCalculationAxisRows
	}
	switch axis {
	case visualizationir.VisualizationCalculationAxisRows:
	case visualizationir.VisualizationCalculationAxisColumns:
		if kind != "matrix" && kind != "pivot" {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("axis %q requires a matrix or pivot", axis)
		}
	case visualizationir.VisualizationCalculationAxisHierarchy:
		if kind != "hierarchy" {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("axis %q requires a hierarchy visualization", axis)
		}
	case visualizationir.VisualizationCalculationAxisFacets:
		if len(authored.PartitionBy) == 0 {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("axis %q requires partition_by", axis)
		}
	default:
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("unsupported axis %q", authored.Axis)
	}
	if authored.Source == "" {
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("source is required")
	}
	if _, field := fields[authored.Source]; !field {
		if _, calculation := calculationIDs[authored.Source]; !calculation {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("source references unknown compiled field %q", authored.Source)
		}
	}
	if calculationNeedsOrder(template) && len(authored.OrderBy) == 0 {
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("template %q requires explicit order_by", template)
	}
	orderBy := make([]visualizationir.VisualizationCalculationOrder, len(authored.OrderBy))
	for index, order := range authored.OrderBy {
		if err := validateCalculationReference(order.Field, fields, calculationIDs); err != nil {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("order_by %d: %w", index, err)
		}
		direction := visualizationir.VisualizationSortDirectionAscending
		switch order.Direction {
		case "", "asc", "ascending":
		case "desc", "descending":
			direction = visualizationir.VisualizationSortDirectionDescending
		default:
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("order_by %d has unsupported direction %q", index, order.Direction)
		}
		orderBy[index] = visualizationir.VisualizationCalculationOrder{Field: primaryFieldRef(order.Field), Direction: direction}
	}
	partitionBy := make([]visualizationir.VisualizationFieldRef, len(authored.PartitionBy))
	for index, field := range authored.PartitionBy {
		if err := validateCalculationReference(field, fields, calculationIDs); err != nil {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("partition_by %d: %w", index, err)
		}
		partitionBy[index] = primaryFieldRef(field)
	}
	reset := visualizationir.VisualizationCalculationReset(authored.Reset)
	if reset == "" {
		reset = visualizationir.VisualizationCalculationResetNone
	}
	switch reset {
	case visualizationir.VisualizationCalculationResetNone:
	case visualizationir.VisualizationCalculationResetHighestParent:
		if len(partitionBy) == 0 {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("reset %q requires partition_by hierarchy fields", reset)
		}
		partitionBy = partitionBy[:1]
	case visualizationir.VisualizationCalculationResetLowestParent:
		if len(partitionBy) == 0 {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("reset %q requires partition_by hierarchy fields", reset)
		}
	default:
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("unsupported reset %q", authored.Reset)
	}
	compiled := visualizationir.VisualizationCalculation{
		ID: authored.ID, Label: firstNonEmpty(authored.Label, authored.ID), Dataset: "primary", Template: template,
		Source: primaryFieldRef(authored.Source), Axis: axis, OrderBy: orderBy, PartitionBy: partitionBy, Reset: reset, Hidden: authored.Hidden,
	}
	if authored.Window != 0 {
		window := int64(authored.Window)
		compiled.Window = &window
	}
	if template == visualizationir.VisualizationCalculationTemplateMovingAverage && (compiled.Window == nil || *compiled.Window <= 0) {
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("moving_average requires a positive window")
	}
	if authored.Offset != 0 {
		offset := int64(authored.Offset)
		compiled.Offset = &offset
	}
	if (template == visualizationir.VisualizationCalculationTemplateDifference || template == visualizationir.VisualizationCalculationTemplatePercentageDifference) &&
		compiled.Offset != nil && *compiled.Offset <= 0 {
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("%s offset must be positive", template)
	}
	if authored.Parent != "" {
		if err := validateCalculationReference(authored.Parent, fields, calculationIDs); err != nil {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("parent: %w", err)
		}
		parent := primaryFieldRef(authored.Parent)
		compiled.Parent = &parent
	}
	if template == visualizationir.VisualizationCalculationTemplatePercentOfParent && len(compiled.PartitionBy) == 0 && compiled.Parent == nil {
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("percent_of_parent requires parent or partition_by")
	}
	if authored.Lookup != nil {
		if err := validateCalculationReference(authored.Lookup.Field, fields, calculationIDs); err != nil {
			return visualizationir.VisualizationCalculation{}, fmt.Errorf("lookup: %w", err)
		}
		compiled.Lookup = &visualizationir.VisualizationCalculationLookup{Field: primaryFieldRef(authored.Lookup.Field), Value: authored.Lookup.Value}
	}
	if template == visualizationir.VisualizationCalculationTemplateLookup && compiled.Lookup == nil {
		return visualizationir.VisualizationCalculation{}, fmt.Errorf("lookup template requires lookup field and value")
	}
	format, err := compiledCalculationFormat(authored.Format)
	if err != nil {
		return visualizationir.VisualizationCalculation{}, err
	}
	compiled.Format = format
	return compiled, nil
}

func calculationNeedsOrder(template visualizationir.VisualizationCalculationTemplate) bool {
	switch template {
	case visualizationir.VisualizationCalculationTemplateRunningTotal,
		visualizationir.VisualizationCalculationTemplateMovingAverage,
		visualizationir.VisualizationCalculationTemplateDifference,
		visualizationir.VisualizationCalculationTemplatePercentageDifference,
		visualizationir.VisualizationCalculationTemplateRank,
		visualizationir.VisualizationCalculationTemplateCumulativeContribution:
		return true
	default:
		return false
	}
}

func validateCalculationReference(field string, fields map[string]visualizationir.VisualizationField, calculations map[string]int) error {
	if _, ok := fields[field]; ok {
		return nil
	}
	if _, ok := calculations[field]; ok {
		return nil
	}
	return fmt.Errorf("references unknown compiled field %q", field)
}

func validateCompiledCalculationDependencies(calculations []visualizationir.VisualizationCalculation, fields map[string]visualizationir.VisualizationField) error {
	indexByID := make(map[string]int, len(calculations))
	for index, calculation := range calculations {
		indexByID[calculation.ID] = index
	}
	state := make([]uint8, len(calculations))
	var visit func(int) error
	visit = func(index int) error {
		switch state[index] {
		case 1:
			return fmt.Errorf("visual calculation dependency cycle includes %q", calculations[index].ID)
		case 2:
			return nil
		}
		state[index] = 1
		refs := []visualizationir.VisualizationFieldRef{calculations[index].Source}
		for _, order := range calculations[index].OrderBy {
			refs = append(refs, order.Field)
		}
		refs = append(refs, calculations[index].PartitionBy...)
		if calculations[index].Parent != nil {
			refs = append(refs, *calculations[index].Parent)
		}
		if calculations[index].Lookup != nil {
			refs = append(refs, calculations[index].Lookup.Field)
		}
		for _, ref := range refs {
			if dependency, ok := indexByID[ref.Field]; ok {
				if err := visit(dependency); err != nil {
					return err
				}
				continue
			}
			if _, ok := fields[ref.Field]; !ok {
				return fmt.Errorf("visual calculation %q references unknown field %q", calculations[index].ID, ref.Field)
			}
		}
		state[index] = 2
		return nil
	}
	for index := range calculations {
		if err := visit(index); err != nil {
			return err
		}
	}
	return nil
}

func primaryFieldRef(field string) visualizationir.VisualizationFieldRef {
	return visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field}
}

func appendCalculationField(base *visualizationir.VisualizationSpecBase, field visualizationir.VisualizationField) {
	for index := range base.Datasets {
		if base.Datasets[index].ID == "primary" {
			base.Datasets[index].Fields = append(base.Datasets[index].Fields, field)
			return
		}
	}
}

func appendVisibleCalculationBinding(spec visualizationir.VisualizationSpec, field visualizationir.VisualizationField) {
	ref := primaryFieldRef(field.ID)
	switch value := spec.Value.(type) {
	case *visualizationir.CartesianVisualizationSpec:
		value.Y = append(value.Y, ref)
	case *visualizationir.TableVisualizationSpec:
		value.Columns = append(value.Columns, visualizationir.TableVisualizationColumn{Field: ref, Label: field.Label, Formatting: []visualizationir.TableVisualizationFormattingRule{}})
	case *visualizationir.MatrixVisualizationSpec:
		value.Measures = append(value.Measures, ref)
	case *visualizationir.PivotVisualizationSpec:
		value.Measures = append(value.Measures, ref)
	}
}

func mutableSpecificationBase(spec visualizationir.VisualizationSpec) (*visualizationir.VisualizationSpecBase, error) {
	switch value := spec.Value.(type) {
	case *visualizationir.CartesianVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.PointVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.ProportionalVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.HierarchyVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.PolarVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.TableVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.MatrixVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.PivotVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.KPIVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	case *visualizationir.GeographicVisualizationSpec:
		return &value.VisualizationSpecBase, nil
	default:
		return nil, fmt.Errorf("unsupported visualization specification %T", spec.Value)
	}
}

func markCompiledFieldProvenance(base *visualizationir.VisualizationSpecBase) {
	for datasetIndex := range base.Datasets {
		for fieldIndex := range base.Datasets[datasetIndex].Fields {
			field := &base.Datasets[datasetIndex].Fields[fieldIndex]
			if field.Provenance != nil {
				continue
			}
			kind := visualizationir.VisualizationFieldProvenanceKindModeled
			if field.Role == visualizationir.VisualizationFieldRoleMeasure {
				kind = visualizationir.VisualizationFieldProvenanceKindAggregated
			}
			sources := []string{}
			if field.SourceRef != nil {
				sources = []string{*field.SourceRef}
			}
			field.Provenance = &visualizationir.VisualizationFieldProvenance{Kind: kind, SourceRefs: sources}
		}
	}
}

func compiledCalculationFormat(format string) (*visualizationir.VisualizationFormat, error) {
	switch format {
	case "":
		return nil, nil
	case "number", "decimal", "integer":
		return &visualizationir.VisualizationFormat{Value: &visualizationir.NumberVisualizationFormat{VisualizationFormatBase: visualizationir.VisualizationFormatBase{Kind: "number"}}}, nil
	case "percent":
		return &visualizationir.VisualizationFormat{Value: &visualizationir.PercentVisualizationFormat{VisualizationFormatBase: visualizationir.VisualizationFormatBase{Kind: "percent"}}}, nil
	case "compact":
		return &visualizationir.VisualizationFormat{Value: &visualizationir.CompactVisualizationFormat{VisualizationFormatBase: visualizationir.VisualizationFormatBase{Kind: "compact"}}}, nil
	case "currency":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported calculation format %q", format)
	}
}

func inferredCalculationFormat(template visualizationir.VisualizationCalculationTemplate, source *visualizationir.VisualizationFormat) *visualizationir.VisualizationFormat {
	switch template {
	case visualizationir.VisualizationCalculationTemplatePercentageDifference,
		visualizationir.VisualizationCalculationTemplatePercentOfParent,
		visualizationir.VisualizationCalculationTemplatePercentOfGrandTotal,
		visualizationir.VisualizationCalculationTemplateCumulativeContribution:
		format, _ := compiledCalculationFormat("percent")
		return format
	case visualizationir.VisualizationCalculationTemplateRank:
		format, _ := compiledCalculationFormat("integer")
		return format
	default:
		return source
	}
}
