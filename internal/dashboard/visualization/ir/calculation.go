package ir

import "fmt"

// CanonicalCalculationDataType returns the result type for a visual
// calculation from the resolved type of its source field. Keep this rule in
// the visualization IR package so compiler and runtime schema resolution use
// identical semantics.
func CanonicalCalculationDataType(template VisualizationCalculationTemplate, source VisualizationDataType) VisualizationDataType {
	switch template {
	case VisualizationCalculationTemplateRank:
		return VisualizationDataTypeInteger
	case VisualizationCalculationTemplateRunningTotal,
		VisualizationCalculationTemplateDifference:
		if source == VisualizationDataTypeInteger {
			return VisualizationDataTypeDecimal
		}
		return source
	case VisualizationCalculationTemplateMovingAverage,
		VisualizationCalculationTemplatePercentageDifference,
		VisualizationCalculationTemplatePercentOfParent,
		VisualizationCalculationTemplatePercentOfGrandTotal,
		VisualizationCalculationTemplateCumulativeContribution:
		return VisualizationDataTypeDecimal
	default:
		return source
	}
}

// ResolveCalculationDataTypes recomputes visual calculation output types after
// source fields have been rebound to their authoritative runtime types. It
// visits calculation references in dependency order so a calculation sourced
// from another calculation inherits that calculation's resolved type.
func ResolveCalculationDataTypes(base *VisualizationSpecBase) error {
	if base == nil || base.Calculations == nil || len(*base.Calculations) == 0 {
		return nil
	}

	type calculationKey struct {
		dataset string
		field   string
	}

	schemas := make(map[string]*VisualizationDatasetSchema, len(base.Datasets))
	for index := range base.Datasets {
		schemas[base.Datasets[index].ID] = &base.Datasets[index]
	}
	calculationIndexes := make(map[calculationKey]int, len(*base.Calculations))
	for index, calculation := range *base.Calculations {
		key := calculationKey{dataset: calculation.Dataset, field: calculation.ID}
		if _, exists := calculationIndexes[key]; exists {
			return fmt.Errorf("duplicate visual calculation %q in dataset %q", calculation.ID, calculation.Dataset)
		}
		calculationIndexes[key] = index
	}

	state := make([]uint8, len(*base.Calculations))
	var visit func(int) error
	visit = func(index int) error {
		switch state[index] {
		case 1:
			return fmt.Errorf("visual calculation dependency cycle includes %q", (*base.Calculations)[index].ID)
		case 2:
			return nil
		}
		state[index] = 1
		calculation := (*base.Calculations)[index]
		for _, ref := range visualCalculationRefs(calculation) {
			dependency, ok := calculationIndexes[calculationKey{dataset: ref.Dataset, field: ref.Field}]
			if !ok {
				continue
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}

		schema, ok := schemas[calculation.Dataset]
		if !ok {
			return fmt.Errorf("visual calculation %q targets unknown dataset %q", calculation.ID, calculation.Dataset)
		}
		source, sourceOK := visualizationSchemaField(*schema, calculation.Source.Field)
		if !sourceOK {
			return fmt.Errorf("visual calculation %q references unknown source %q", calculation.ID, calculation.Source.Field)
		}
		output, outputOK := visualizationSchemaField(*schema, calculation.ID)
		if !outputOK {
			return fmt.Errorf("visual calculation %q has no output field in dataset %q", calculation.ID, calculation.Dataset)
		}
		output.DataType = CanonicalCalculationDataType(calculation.Template, source.DataType)
		if err := setVisualizationSchemaField(schema, output); err != nil {
			return err
		}
		state[index] = 2
		return nil
	}

	for index := range *base.Calculations {
		if err := visit(index); err != nil {
			return err
		}
	}
	return nil
}

func visualizationSchemaField(schema VisualizationDatasetSchema, id string) (VisualizationField, bool) {
	for _, field := range schema.Fields {
		if field.ID == id {
			return field, true
		}
	}
	return VisualizationField{}, false
}

func setVisualizationSchemaField(schema *VisualizationDatasetSchema, field VisualizationField) error {
	for index := range schema.Fields {
		if schema.Fields[index].ID == field.ID {
			schema.Fields[index] = field
			return nil
		}
	}
	return fmt.Errorf("visualization dataset %q has no field %q", schema.ID, field.ID)
}

func validateVisualCalculations(calculations *[]VisualizationCalculation, schemas map[string]VisualizationDatasetSchema) error {
	if calculations == nil {
		return nil
	}
	byID := make(map[string]int, len(*calculations))
	for index, calculation := range *calculations {
		if calculation.ID == "" || calculation.Dataset == "" || calculation.Label == "" {
			return fmt.Errorf("visual calculation %d requires ID, label, and dataset", index)
		}
		if _, exists := byID[calculation.ID]; exists {
			return fmt.Errorf("duplicate visual calculation %q", calculation.ID)
		}
		byID[calculation.ID] = index
		if err := validateCalculationTemplate(calculation); err != nil {
			return fmt.Errorf("visual calculation %q: %w", calculation.ID, err)
		}
		if _, ok := schemas[calculation.Dataset]; !ok {
			return fmt.Errorf("visual calculation %q targets unknown dataset %q", calculation.ID, calculation.Dataset)
		}
		output, ok := visualizationField(VisualizationFieldRef{Dataset: calculation.Dataset, Field: calculation.ID}, schemas)
		if !ok || output.Provenance == nil ||
			output.Provenance.Kind != VisualizationFieldProvenanceKindVisualCalculation ||
			output.Provenance.CalculationID == nil || *output.Provenance.CalculationID != calculation.ID {
			return fmt.Errorf("visual calculation %q requires a matching calculated field provenance", calculation.ID)
		}
	}
	state := make([]uint8, len(*calculations))
	var visit func(int) error
	visit = func(index int) error {
		switch state[index] {
		case 1:
			return fmt.Errorf("visual calculation dependency cycle includes %q", (*calculations)[index].ID)
		case 2:
			return nil
		}
		state[index] = 1
		calculation := (*calculations)[index]
		for _, ref := range visualCalculationRefs(calculation) {
			if ref.Dataset != calculation.Dataset {
				return fmt.Errorf("visual calculation %q references dataset %q outside its result frame", calculation.ID, ref.Dataset)
			}
			if dependency, ok := byID[ref.Field]; ok {
				if (*calculations)[dependency].Dataset != calculation.Dataset {
					return fmt.Errorf("visual calculation %q dependency %q belongs to another dataset", calculation.ID, ref.Field)
				}
				if err := visit(dependency); err != nil {
					return err
				}
				continue
			}
			if err := validateFieldRef(ref, schemas); err != nil {
				return fmt.Errorf("visual calculation %q: %w", calculation.ID, err)
			}
		}
		state[index] = 2
		return nil
	}
	for index := range *calculations {
		if err := visit(index); err != nil {
			return err
		}
	}
	return nil
}

func validateCalculationTemplate(calculation VisualizationCalculation) error {
	switch calculation.Template {
	case VisualizationCalculationTemplateRunningTotal,
		VisualizationCalculationTemplateMovingAverage,
		VisualizationCalculationTemplateDifference,
		VisualizationCalculationTemplatePercentageDifference,
		VisualizationCalculationTemplatePercentOfParent,
		VisualizationCalculationTemplatePercentOfGrandTotal,
		VisualizationCalculationTemplateRank,
		VisualizationCalculationTemplateCumulativeContribution,
		VisualizationCalculationTemplateLookup:
	default:
		return fmt.Errorf("unsupported template %q", calculation.Template)
	}
	switch calculation.Axis {
	case VisualizationCalculationAxisRows, VisualizationCalculationAxisColumns, VisualizationCalculationAxisHierarchy, VisualizationCalculationAxisFacets:
	default:
		return fmt.Errorf("unsupported axis %q", calculation.Axis)
	}
	switch calculation.Reset {
	case VisualizationCalculationResetNone, VisualizationCalculationResetHighestParent, VisualizationCalculationResetLowestParent:
	default:
		return fmt.Errorf("unsupported reset %q", calculation.Reset)
	}
	if calculationNeedsOrder(calculation.Template) && len(calculation.OrderBy) == 0 {
		return fmt.Errorf("template %q requires orderBy", calculation.Template)
	}
	for index, order := range calculation.OrderBy {
		if order.Direction != VisualizationSortDirectionAscending && order.Direction != VisualizationSortDirectionDescending {
			return fmt.Errorf("orderBy %d has unsupported direction %q", index, order.Direction)
		}
	}
	if calculation.Template == VisualizationCalculationTemplateMovingAverage &&
		(calculation.Window == nil || *calculation.Window <= 0) {
		return fmt.Errorf("moving_average requires a positive window")
	}
	if calculation.Offset != nil && *calculation.Offset <= 0 {
		return fmt.Errorf("offset must be positive")
	}
	if calculation.Template == VisualizationCalculationTemplatePercentOfParent &&
		len(calculation.PartitionBy) == 0 && calculation.Parent == nil {
		return fmt.Errorf("percent_of_parent requires parent or partitionBy")
	}
	if calculation.Template == VisualizationCalculationTemplateLookup && calculation.Lookup == nil {
		return fmt.Errorf("lookup requires match field and value")
	}
	return nil
}

func calculationNeedsOrder(template VisualizationCalculationTemplate) bool {
	switch template {
	case VisualizationCalculationTemplateRunningTotal,
		VisualizationCalculationTemplateMovingAverage,
		VisualizationCalculationTemplateDifference,
		VisualizationCalculationTemplatePercentageDifference,
		VisualizationCalculationTemplateRank,
		VisualizationCalculationTemplateCumulativeContribution:
		return true
	default:
		return false
	}
}

func visualCalculationRefs(calculation VisualizationCalculation) []VisualizationFieldRef {
	refs := []VisualizationFieldRef{calculation.Source}
	for _, order := range calculation.OrderBy {
		refs = append(refs, order.Field)
	}
	refs = append(refs, calculation.PartitionBy...)
	if calculation.Parent != nil {
		refs = append(refs, *calculation.Parent)
	}
	if calculation.Lookup != nil {
		refs = append(refs, calculation.Lookup.Field)
	}
	return refs
}
