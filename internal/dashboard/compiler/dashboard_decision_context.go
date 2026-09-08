package compiler

import (
	"fmt"
	"math"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// lowerCanonicalDecisionContext lowers dashboard-owned context declarations
// into the renderer-neutral Visual IR. Context fields are deliberately limited
// to the primary governed result frame; secondary query datasets are not a
// presentation binding surface.
func lowerCanonicalDecisionContext(spec *visualizationir.VisualizationSpec, authored document.DashboardPresentation, visualType document.DashboardVisualType, query LoweredDashboardQuery) error {
	if spec == nil || authored.Value == nil {
		return nil
	}
	base, err := spec.Base()
	if err != nil {
		return err
	}
	primary := (*visualizationir.VisualizationDatasetSchema)(nil)
	for index := range base.Datasets {
		if base.Datasets[index].ID == "primary" {
			primary = &base.Datasets[index]
			break
		}
	}
	if primary == nil {
		return fmt.Errorf("presentation context requires a primary result frame")
	}

	var axes *[]document.DashboardAxisConfiguration
	var lines *[]document.DashboardReferenceLine
	var bands *[]document.DashboardReferenceBand
	var events *[]document.DashboardEventAnnotation
	var cartesian *visualizationir.CartesianVisualizationSpec
	var point *visualizationir.PointVisualizationSpec
	switch value := authored.Value.(type) {
	case *document.CartesianDashboardPresentation:
		axes, lines, bands, events = value.Axes, value.ReferenceLines, value.ReferenceBands, value.EventAnnotations
		cartesian, _ = spec.Value.(*visualizationir.CartesianVisualizationSpec)
	case *document.PointDashboardPresentation:
		axes, lines, bands, events = value.Axes, value.ReferenceLines, value.ReferenceBands, value.EventAnnotations
		point, _ = spec.Value.(*visualizationir.PointVisualizationSpec)
	default:
		return nil
	}
	if cartesian == nil && point == nil {
		return fmt.Errorf("presentation context is incompatible with %s visual", visualType)
	}
	if visualType != document.DashboardVisualTypeLine && visualType != document.DashboardVisualTypeArea && visualType != document.DashboardVisualTypeBar && visualType != document.DashboardVisualTypeColumn && visualType != document.DashboardVisualTypeCombo && visualType != document.DashboardVisualTypeScatter && visualType != document.DashboardVisualTypeWaterfall {
		for _, named := range []struct {
			path   string
			values int
		}{{"presentation.referenceLines", pointerLen(lines)}, {"presentation.referenceBands", pointerLen(bands)}, {"presentation.eventAnnotations", pointerLen(events)}} {
			if named.values > 0 {
				return fmt.Errorf("%s is not supported for %s visuals", named.path, visualType)
			}
		}
	}
	axisField := func(axis visualizationir.VisualizationCartesianAxis) (visualizationir.VisualizationField, bool) {
		var ref visualizationir.VisualizationFieldRef
		if cartesian != nil {
			if axis == visualizationir.VisualizationCartesianAxisX {
				ref = cartesian.X
			} else if cartesian.Mark == visualizationir.VisualizationCartesianMarkCombo {
				ref, _ = visualizationir.CartesianComboAxisOwner(*cartesian, axis)
			} else if len(cartesian.Y) > 0 {
				ref = cartesian.Y[0]
			}
		} else if point != nil {
			if axis == visualizationir.VisualizationCartesianAxisX {
				ref = point.X
			} else {
				ref = point.Y
			}
		}
		return primaryVisualizationField(*primary, ref.Field)
	}
	axisIsNumeric := func(axis visualizationir.VisualizationCartesianAxis) bool {
		field, ok := axisField(axis)
		return ok && dashboardNumericField(field)
	}
	axisDefaultType := func(axis visualizationir.VisualizationCartesianAxis) visualizationir.VisualizationAxisType {
		field, hasField := axisField(axis)
		temporal := hasField && (field.DataType == visualizationir.VisualizationDataTypeTemporal || field.DataType == visualizationir.VisualizationDataTypeDate)
		if temporal {
			return visualizationir.VisualizationAxisTypeTime
		}
		if axisIsNumeric(axis) && (point != nil || (cartesian != nil && axis != visualizationir.VisualizationCartesianAxisX && cartesian.Mark != visualizationir.VisualizationCartesianMarkHeatmap)) {
			return visualizationir.VisualizationAxisTypeValue
		}
		return visualizationir.VisualizationAxisTypeCategory
	}
	axisEffectiveType := func(axis visualizationir.VisualizationCartesianAxis) visualizationir.VisualizationAxisType {
		effective := axisDefaultType(axis)
		if axes != nil {
			for _, authoredAxis := range *axes {
				if authoredAxis.ID != axis || authoredAxis.Type == nil || *authoredAxis.Type == visualizationir.VisualizationAxisTypeAutomatic {
					continue
				}
				effective = *authoredAxis.Type
				break
			}
		}
		return effective
	}

	lowerAxes := func() (*[]visualizationir.VisualizationAxisConfiguration, error) {
		if axes == nil {
			return nil, nil
		}
		out := make([]visualizationir.VisualizationAxisConfiguration, len(*axes))
		seen := make(map[visualizationir.VisualizationCartesianAxis]int, len(*axes))
		for index, authoredAxis := range *axes {
			path := fmt.Sprintf("presentation.axes[%d]", index)
			if authoredAxis.ID != visualizationir.VisualizationCartesianAxisX && authoredAxis.ID != visualizationir.VisualizationCartesianAxisPrimaryY && authoredAxis.ID != visualizationir.VisualizationCartesianAxisSecondaryY {
				return nil, fmt.Errorf("%s.id has unsupported axis %q", path, authoredAxis.ID)
			}
			if previous, exists := seen[authoredAxis.ID]; exists {
				return nil, fmt.Errorf("%s.id duplicates %s.id", path, fmt.Sprintf("presentation.axes[%d]", previous))
			}
			seen[authoredAxis.ID] = index
			if authoredAxis.ID == visualizationir.VisualizationCartesianAxisSecondaryY && (cartesian == nil || cartesian.Mark != visualizationir.VisualizationCartesianMarkCombo) {
				return nil, fmt.Errorf("%s.id secondary_y requires a combo visual", path)
			}
			if cartesian != nil && cartesian.Mark == visualizationir.VisualizationCartesianMarkCombo && authoredAxis.ID != visualizationir.VisualizationCartesianAxisX {
				if _, found := visualizationir.CartesianComboAxisOwner(*cartesian, authoredAxis.ID); !found {
					return nil, fmt.Errorf("%s.id requires a %s combo series", path, comboAxisOwnerName(authoredAxis.ID))
				}
			}
			if err := validateDashboardAxisEnums(authoredAxis, path); err != nil {
				return nil, err
			}
			if authoredAxis.ID == visualizationir.VisualizationCartesianAxisPrimaryY && cartesian != nil && cartesian.Presentation.Stacking != nil && *cartesian.Presentation.Stacking == visualizationir.VisualizationStackingModePercent && authoredAxis.DisplayUnits != nil {
				return nil, fmt.Errorf("%s.displayUnits is incompatible with percent stacking because the renderer owns the percent formatter", path)
			}
			numeric := axisIsNumeric(authoredAxis.ID)
			fieldDefinition, hasAxisField := axisField(authoredAxis.ID)
			temporal := hasAxisField && (fieldDefinition.DataType == visualizationir.VisualizationDataTypeTemporal || fieldDefinition.DataType == visualizationir.VisualizationDataTypeDate)
			effectiveType := axisDefaultType(authoredAxis.ID)
			if authoredAxis.Type != nil {
				switch *authoredAxis.Type {
				case visualizationir.VisualizationAxisTypeAutomatic:
				case visualizationir.VisualizationAxisTypeValue:
					if !numeric {
						return nil, fmt.Errorf("%s.type value requires a numeric axis", path)
					}
					effectiveType = visualizationir.VisualizationAxisTypeValue
				case visualizationir.VisualizationAxisTypeCategory:
					effectiveType = visualizationir.VisualizationAxisTypeCategory
				case visualizationir.VisualizationAxisTypeTime:
					if !temporal {
						return nil, fmt.Errorf("%s.type time requires a date or temporal axis", path)
					}
					effectiveType = visualizationir.VisualizationAxisTypeTime
				}
			}
			effectiveNumeric := effectiveType == visualizationir.VisualizationAxisTypeValue
			if authoredAxis.DisplayUnits != nil && !effectiveNumeric {
				return nil, fmt.Errorf("%s.displayUnits requires a numeric axis (effective type is %s)", path, effectiveType)
			}
			if authoredAxis.DateUnit != nil && *authoredAxis.DateUnit != visualizationir.VisualizationDateDisplayUnitAutomatic && effectiveType != visualizationir.VisualizationAxisTypeTime {
				return nil, fmt.Errorf("%s.dateUnit requires an effective time axis", path)
			}
			if authoredAxis.Zero != visualizationir.VisualizationAxisZeroPolicyAutomatic && !effectiveNumeric {
				return nil, fmt.Errorf("%s.zero requires an effective numeric axis", path)
			}
			if (authoredAxis.Minimum != nil || authoredAxis.Maximum != nil) && !effectiveNumeric {
				return nil, fmt.Errorf("%s domain bounds require an effective numeric axis", path)
			}
			if (authoredAxis.Scale == visualizationir.VisualizationAxisScaleLog || authoredAxis.Scale == visualizationir.VisualizationAxisScaleLinear) && !effectiveNumeric {
				return nil, fmt.Errorf("%s.scale %s requires an effective numeric axis", path, authoredAxis.Scale)
			}
			if authoredAxis.Minimum != nil && !finiteDashboardFloat(*authoredAxis.Minimum) {
				return nil, fmt.Errorf("%s.minimum must be finite", path)
			}
			if authoredAxis.Maximum != nil && !finiteDashboardFloat(*authoredAxis.Maximum) {
				return nil, fmt.Errorf("%s.maximum must be finite", path)
			}
			if authoredAxis.Minimum != nil && authoredAxis.Maximum != nil && *authoredAxis.Minimum >= *authoredAxis.Maximum {
				return nil, fmt.Errorf("%s.minimum must be less than maximum", path)
			}
			if authoredAxis.Scale == visualizationir.VisualizationAxisScaleLog {
				if authoredAxis.Zero == visualizationir.VisualizationAxisZeroPolicyInclude {
					return nil, fmt.Errorf("%s.zero cannot include zero on a log scale", path)
				}
				if authoredAxis.Minimum != nil && *authoredAxis.Minimum <= 0 {
					return nil, fmt.Errorf("%s.minimum must be positive on a log scale", path)
				}
				if authoredAxis.Maximum != nil && *authoredAxis.Maximum <= 0 {
					return nil, fmt.Errorf("%s.maximum must be positive on a log scale", path)
				}
			}
			axisType := visualizationir.VisualizationAxisTypeAutomatic
			if authoredAxis.Type != nil {
				axisType = *authoredAxis.Type
			}
			inversion := visualizationir.VisualizationAxisInversionAutomatic
			if authoredAxis.Inversion != nil {
				inversion = *authoredAxis.Inversion
			}
			ticks := visualizationir.VisualizationAxisTickVisibilityAutomatic
			if authoredAxis.Ticks != nil {
				ticks = *authoredAxis.Ticks
			}
			grid := visualizationir.VisualizationAxisGridVisibilityAutomatic
			if authoredAxis.Grid != nil {
				grid = *authoredAxis.Grid
			}
			rotation := visualizationir.VisualizationAxisLabelRotationAutomatic
			if authoredAxis.LabelRotation != nil {
				rotation = *authoredAxis.LabelRotation
			}
			dateUnit := visualizationir.VisualizationDateDisplayUnitAutomatic
			if authoredAxis.DateUnit != nil {
				dateUnit = *authoredAxis.DateUnit
			}
			out[index] = visualizationir.VisualizationAxisConfiguration{ID: authoredAxis.ID, Title: authoredAxis.Title, Type: axisType, Scale: authoredAxis.Scale, Zero: authoredAxis.Zero, Inversion: inversion, Minimum: authoredAxis.Minimum, Maximum: authoredAxis.Maximum, Unit: authoredAxis.Unit, DisplayUnits: authoredAxis.DisplayUnits, TickDensity: authoredAxis.TickDensity, Ticks: ticks, Grid: grid, LabelRotation: rotation, DateUnit: dateUnit}
		}
		return &out, nil
	}

	type loweredValue struct {
		value  visualizationir.VisualizationReferenceValue
		domain string
	}
	lowerValue := func(authoredValue document.DashboardReferenceValue, path string) (loweredValue, error) {
		switch value := authoredValue.Value.(type) {
		case *document.NumberDashboardReferenceValue:
			if value == nil || math.IsNaN(value.Value) || math.IsInf(value.Value, 0) {
				return loweredValue{}, fmt.Errorf("%s.value must be finite", path)
			}
			return loweredValue{value: visualizationir.VisualizationReferenceValue{Value: &visualizationir.NumberVisualizationReferenceValue{VisualizationReferenceValueBase: visualizationir.VisualizationReferenceValueBase{Kind: "number"}, Kind: "number", Value: value.Value}}, domain: "number"}, nil
		case *document.TextDashboardReferenceValue:
			if value == nil || strings.TrimSpace(value.Value) == "" {
				return loweredValue{}, fmt.Errorf("%s.value must be non-empty", path)
			}
			return loweredValue{value: visualizationir.VisualizationReferenceValue{Value: &visualizationir.TextVisualizationReferenceValue{VisualizationReferenceValueBase: visualizationir.VisualizationReferenceValueBase{Kind: "text"}, Kind: "text", Value: value.Value}}, domain: "text"}, nil
		case *document.FieldDashboardReferenceValue:
			if value == nil || strings.TrimSpace(value.Field) == "" {
				return loweredValue{}, fmt.Errorf("%s.field is required", path)
			}
			if err := query.ValidateResultReference(value.Field); err != nil {
				return loweredValue{}, fmt.Errorf("%s.field: %w", path, err)
			}
			field, ok := primaryVisualizationField(*primary, value.Field)
			if !ok {
				return loweredValue{}, fmt.Errorf("%s.field %q is not in the primary result frame", path, value.Field)
			}
			if !validDashboardReferenceReducer(value.Reducer) {
				return loweredValue{}, fmt.Errorf("%s.reducer has unsupported reducer %q", path, value.Reducer)
			}
			if (value.Reducer == visualizationir.VisualizationReferenceReducerMean || value.Reducer == visualizationir.VisualizationReferenceReducerMedian) && !dashboardNumericField(field) {
				return loweredValue{}, fmt.Errorf("%s.reducer %q requires a numeric result field", path, value.Reducer)
			}
			if (value.Reducer == visualizationir.VisualizationReferenceReducerMinimum || value.Reducer == visualizationir.VisualizationReferenceReducerMaximum) && field.DataType == visualizationir.VisualizationDataTypeBoolean {
				return loweredValue{}, fmt.Errorf("%s.reducer %q requires a comparable result field", path, value.Reducer)
			}
			domain := "text"
			if dashboardNumericField(field) {
				domain = "number"
			}
			return loweredValue{value: visualizationir.VisualizationReferenceValue{Value: &visualizationir.FieldVisualizationReferenceValue{VisualizationReferenceValueBase: visualizationir.VisualizationReferenceValueBase{Kind: "field"}, Kind: "field", Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: value.Field}, Reducer: value.Reducer}}, domain: domain}, nil
		default:
			return loweredValue{}, fmt.Errorf("%s must contain a number, text, or field value", path)
		}
	}
	validateAxis := func(axis visualizationir.VisualizationCartesianAxis, path string) error {
		if axis != visualizationir.VisualizationCartesianAxisX && axis != visualizationir.VisualizationCartesianAxisPrimaryY && axis != visualizationir.VisualizationCartesianAxisSecondaryY {
			return fmt.Errorf("%s has unsupported axis %q", path, axis)
		}
		if axis == visualizationir.VisualizationCartesianAxisSecondaryY && (cartesian == nil || cartesian.Mark != visualizationir.VisualizationCartesianMarkCombo) {
			return fmt.Errorf("%s secondary_y requires a combo visual", path)
		}
		if cartesian != nil && cartesian.Mark == visualizationir.VisualizationCartesianMarkCombo {
			ownerAxis := axis
			if ownerAxis == visualizationir.VisualizationCartesianAxisX {
				ownerAxis = visualizationir.VisualizationCartesianAxisPrimaryY
			}
			if _, found := visualizationir.CartesianComboAxisOwner(*cartesian, ownerAxis); !found {
				return fmt.Errorf("%s requires a %s combo series", path, comboAxisOwnerName(ownerAxis))
			}
		}
		return nil
	}
	validateAxisValue := func(value loweredValue, axis visualizationir.VisualizationCartesianAxis, path string) error {
		effectiveType := axisEffectiveType(axis)
		if axis != visualizationir.VisualizationCartesianAxisX && value.domain != "number" {
			return fmt.Errorf("%s must use a numeric value on %s", path, axis)
		}
		if axis == visualizationir.VisualizationCartesianAxisX {
			switch effectiveType {
			case visualizationir.VisualizationAxisTypeValue:
				if value.domain != "number" {
					return fmt.Errorf("%s must use a numeric value on x (effective type is value)", path)
				}
			}
		}
		if axes != nil {
			for _, authoredAxis := range *axes {
				if authoredAxis.ID != axis || authoredAxis.Scale != visualizationir.VisualizationAxisScaleLog {
					continue
				}
				if number, ok := value.value.Value.(*visualizationir.NumberVisualizationReferenceValue); ok && number != nil && number.Value <= 0 {
					return fmt.Errorf("%s must be positive on a log axis", path)
				}
				break
			}
		}
		return nil
	}
	ids := make(map[string]string)
	addID := func(id, path string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s.id is required", path)
		}
		if previous, exists := ids[id]; exists {
			return fmt.Errorf("%s.id duplicates %s.id", path, previous)
		}
		ids[id] = path
		return nil
	}
	lowerLines := func() (*[]visualizationir.VisualizationReferenceLine, error) {
		if lines == nil {
			return nil, nil
		}
		out := make([]visualizationir.VisualizationReferenceLine, len(*lines))
		for index, line := range *lines {
			path := fmt.Sprintf("presentation.referenceLines[%d]", index)
			if err := addID(line.ID, path); err != nil {
				return nil, err
			}
			if err := validateAxis(line.Axis, path+".axis"); err != nil {
				return nil, err
			}
			value, err := lowerValue(line.Value, path+".value")
			if err != nil {
				return nil, err
			}
			if err := validateAxisValue(value, line.Axis, path+".value"); err != nil {
				return nil, err
			}
			if !validDashboardTone(line.Tone) {
				return nil, fmt.Errorf("%s.tone has unsupported tone %q", path, line.Tone)
			}
			out[index] = visualizationir.VisualizationReferenceLine{ID: line.ID, Axis: line.Axis, Value: value.value, Label: line.Label, Tone: line.Tone}
		}
		return &out, nil
	}
	lowerBands := func() (*[]visualizationir.VisualizationReferenceBand, error) {
		if bands == nil {
			return nil, nil
		}
		out := make([]visualizationir.VisualizationReferenceBand, len(*bands))
		for index, band := range *bands {
			path := fmt.Sprintf("presentation.referenceBands[%d]", index)
			if err := addID(band.ID, path); err != nil {
				return nil, err
			}
			if err := validateAxis(band.Axis, path+".axis"); err != nil {
				return nil, err
			}
			from, err := lowerValue(band.From, path+".from")
			if err != nil {
				return nil, err
			}
			to, err := lowerValue(band.To, path+".to")
			if err != nil {
				return nil, err
			}
			if from.domain != to.domain {
				return nil, fmt.Errorf("%s.from and %s.to must use compatible value types", path, path)
			}
			if err := validateAxisValue(from, band.Axis, path+".from"); err != nil {
				return nil, err
			}
			if err := validateAxisValue(to, band.Axis, path+".to"); err != nil {
				return nil, err
			}
			if fromNumber, ok := from.value.Value.(*visualizationir.NumberVisualizationReferenceValue); ok {
				if toNumber, ok := to.value.Value.(*visualizationir.NumberVisualizationReferenceValue); ok && fromNumber.Value >= toNumber.Value {
					return nil, fmt.Errorf("%s.from must be less than %s.to", path, path)
				}
			}
			if !validDashboardTone(band.Tone) {
				return nil, fmt.Errorf("%s.tone has unsupported tone %q", path, band.Tone)
			}
			out[index] = visualizationir.VisualizationReferenceBand{ID: band.ID, Axis: band.Axis, From: from.value, To: to.value, Label: band.Label, Tone: band.Tone}
		}
		return &out, nil
	}
	lowerEvents := func() (*[]visualizationir.VisualizationEventAnnotation, error) {
		if events == nil {
			return nil, nil
		}
		out := make([]visualizationir.VisualizationEventAnnotation, len(*events))
		for index, annotation := range *events {
			path := fmt.Sprintf("presentation.eventAnnotations[%d]", index)
			if err := addID(annotation.ID, path); err != nil {
				return nil, err
			}
			if annotation.Axis != visualizationir.VisualizationCartesianAxisX {
				return nil, fmt.Errorf("%s.axis must be x", path)
			}
			if cartesian != nil && cartesian.Mark == visualizationir.VisualizationCartesianMarkCombo {
				if _, found := visualizationir.CartesianComboAxisOwner(*cartesian, visualizationir.VisualizationCartesianAxisPrimaryY); !found {
					return nil, fmt.Errorf("%s.axis requires a primary_y combo series", path)
				}
			}
			if strings.TrimSpace(annotation.Label) == "" {
				return nil, fmt.Errorf("%s.label is required", path)
			}
			value, err := lowerValue(annotation.Value, path+".value")
			if err != nil {
				return nil, err
			}
			if err := validateAxisValue(value, annotation.Axis, path+".value"); err != nil {
				return nil, err
			}
			if !validDashboardTone(annotation.Tone) {
				return nil, fmt.Errorf("%s.tone has unsupported tone %q", path, annotation.Tone)
			}
			out[index] = visualizationir.VisualizationEventAnnotation{ID: annotation.ID, Axis: annotation.Axis, Value: value.value, Label: annotation.Label, Description: annotation.Description, Tone: annotation.Tone}
		}
		return &out, nil
	}
	compiledAxes, err := lowerAxes()
	if err != nil {
		return err
	}
	compiledLines, err := lowerLines()
	if err != nil {
		return err
	}
	compiledBands, err := lowerBands()
	if err != nil {
		return err
	}
	compiledEvents, err := lowerEvents()
	if err != nil {
		return err
	}
	if cartesian != nil {
		cartesian.Axes, cartesian.ReferenceLines, cartesian.ReferenceBands, cartesian.EventAnnotations = compiledAxes, compiledLines, compiledBands, compiledEvents
	}
	if point != nil {
		point.Axes, point.ReferenceLines, point.ReferenceBands, point.EventAnnotations = compiledAxes, compiledLines, compiledBands, compiledEvents
	}
	return nil
}

func pointerLen[T any](value *[]T) int {
	if value == nil {
		return 0
	}
	return len(*value)
}

func comboAxisOwnerName(axis visualizationir.VisualizationCartesianAxis) string {
	if axis == visualizationir.VisualizationCartesianAxisSecondaryY {
		return "secondary_y"
	}
	return "primary_y"
}

func primaryVisualizationField(schema visualizationir.VisualizationDatasetSchema, name string) (visualizationir.VisualizationField, bool) {
	for _, field := range schema.Fields {
		if field.ID == name {
			return field, true
		}
	}
	return visualizationir.VisualizationField{}, false
}

func dashboardNumericField(field visualizationir.VisualizationField) bool {
	switch field.DataType {
	case visualizationir.VisualizationDataTypeInteger, visualizationir.VisualizationDataTypeDecimal, visualizationir.VisualizationDataTypeFloat:
		return true
	default:
		return false
	}
}

func validDashboardReferenceReducer(reducer visualizationir.VisualizationReferenceReducer) bool {
	switch reducer {
	case visualizationir.VisualizationReferenceReducerFirst, visualizationir.VisualizationReferenceReducerLast, visualizationir.VisualizationReferenceReducerMinimum, visualizationir.VisualizationReferenceReducerMaximum, visualizationir.VisualizationReferenceReducerMean, visualizationir.VisualizationReferenceReducerMedian:
		return true
	default:
		return false
	}
}

func validateDashboardAxisEnums(axis document.DashboardAxisConfiguration, path string) error {
	if axis.Type != nil {
		switch *axis.Type {
		case visualizationir.VisualizationAxisTypeAutomatic, visualizationir.VisualizationAxisTypeCategory, visualizationir.VisualizationAxisTypeValue, visualizationir.VisualizationAxisTypeTime:
		default:
			return fmt.Errorf("%s.type has unsupported type %q", path, *axis.Type)
		}
	}
	switch axis.Scale {
	case visualizationir.VisualizationAxisScaleAutomatic, visualizationir.VisualizationAxisScaleLinear, visualizationir.VisualizationAxisScaleLog:
	default:
		return fmt.Errorf("%s.scale has unsupported scale %q", path, axis.Scale)
	}
	switch axis.Zero {
	case visualizationir.VisualizationAxisZeroPolicyAutomatic, visualizationir.VisualizationAxisZeroPolicyInclude, visualizationir.VisualizationAxisZeroPolicyExclude:
	default:
		return fmt.Errorf("%s.zero has unsupported policy %q", path, axis.Zero)
	}
	switch axis.TickDensity {
	case visualizationir.VisualizationAxisTickDensityAutomatic, visualizationir.VisualizationAxisTickDensitySparse, visualizationir.VisualizationAxisTickDensityNormal, visualizationir.VisualizationAxisTickDensityDense:
	default:
		return fmt.Errorf("%s.tickDensity has unsupported density %q", path, axis.TickDensity)
	}
	if axis.DisplayUnits != nil && !validDashboardDisplayUnits(*axis.DisplayUnits) {
		return fmt.Errorf("%s.displayUnits has unsupported value %q", path, *axis.DisplayUnits)
	}
	if axis.Inversion != nil {
		switch *axis.Inversion {
		case visualizationir.VisualizationAxisInversionAutomatic, visualizationir.VisualizationAxisInversionNormal, visualizationir.VisualizationAxisInversionInverted:
		default:
			return fmt.Errorf("%s.inversion has unsupported value %q", path, *axis.Inversion)
		}
	}
	if axis.Ticks != nil {
		switch *axis.Ticks {
		case visualizationir.VisualizationAxisTickVisibilityAutomatic, visualizationir.VisualizationAxisTickVisibilityVisible, visualizationir.VisualizationAxisTickVisibilityHidden:
		default:
			return fmt.Errorf("%s.ticks has unsupported value %q", path, *axis.Ticks)
		}
	}
	if axis.Grid != nil {
		switch *axis.Grid {
		case visualizationir.VisualizationAxisGridVisibilityAutomatic, visualizationir.VisualizationAxisGridVisibilityVisible, visualizationir.VisualizationAxisGridVisibilityHidden:
		default:
			return fmt.Errorf("%s.grid has unsupported value %q", path, *axis.Grid)
		}
	}
	if axis.LabelRotation != nil {
		switch *axis.LabelRotation {
		case visualizationir.VisualizationAxisLabelRotationAutomatic, visualizationir.VisualizationAxisLabelRotationHorizontal, visualizationir.VisualizationAxisLabelRotationDiagonal, visualizationir.VisualizationAxisLabelRotationVertical:
		default:
			return fmt.Errorf("%s.labelRotation has unsupported value %q", path, *axis.LabelRotation)
		}
	}
	if axis.DateUnit != nil {
		switch *axis.DateUnit {
		case visualizationir.VisualizationDateDisplayUnitAutomatic, visualizationir.VisualizationDateDisplayUnitYear, visualizationir.VisualizationDateDisplayUnitQuarter, visualizationir.VisualizationDateDisplayUnitMonth, visualizationir.VisualizationDateDisplayUnitWeek, visualizationir.VisualizationDateDisplayUnitDay, visualizationir.VisualizationDateDisplayUnitHour, visualizationir.VisualizationDateDisplayUnitMinute, visualizationir.VisualizationDateDisplayUnitSecond:
		default:
			return fmt.Errorf("%s.dateUnit has unsupported value %q", path, *axis.DateUnit)
		}
	}
	return nil
}

func validDashboardDisplayUnits(units visualizationir.VisualizationDisplayUnits) bool {
	switch units {
	case visualizationir.VisualizationDisplayUnitsAuto, visualizationir.VisualizationDisplayUnitsNone, visualizationir.VisualizationDisplayUnitsThousands, visualizationir.VisualizationDisplayUnitsMillions, visualizationir.VisualizationDisplayUnitsBillions, visualizationir.VisualizationDisplayUnitsTrillions:
		return true
	default:
		return false
	}
}

func validDashboardTone(tone visualizationir.VisualizationTone) bool {
	switch tone {
	case visualizationir.VisualizationToneNeutral, visualizationir.VisualizationToneInk, visualizationir.VisualizationToneSuccess, visualizationir.VisualizationToneWarning, visualizationir.VisualizationToneDanger:
		return true
	default:
		return false
	}
}
