package compiler

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// canonicalTooltipItems lowers the compact dashboard tooltip form and its
// authored display overrides without changing the canonical result-field
// identity used by selections and interactions.
func canonicalTooltipItems(values *[]document.DashboardTooltip, query LoweredDashboardQuery, fields []visualizationir.VisualizationField) ([]visualizationir.VisualizationTooltipItem, []visualizationir.VisualizationFieldRef, error) {
	if values == nil {
		return nil, nil, nil
	}
	if len(*values) == 0 {
		return []visualizationir.VisualizationTooltipItem{}, []visualizationir.VisualizationFieldRef{}, nil
	}
	byName := make(map[string]visualizationir.VisualizationField, len(fields))
	for _, field := range fields {
		byName[field.ID] = field
	}
	items := make([]visualizationir.VisualizationTooltipItem, 0, len(*values))
	refs := make([]visualizationir.VisualizationFieldRef, 0, len(*values))
	seen := make(map[string]int, len(*values))
	for index, value := range *values {
		name, label, format, err := dashboardTooltipValue(value)
		if err != nil {
			return nil, nil, fmt.Errorf("presentation.tooltip[%d]: %w", index, err)
		}
		name = strings.TrimSpace(name)
		if err := query.ValidateResultReference(name); err != nil {
			return nil, nil, fmt.Errorf("presentation.tooltip[%d].field: %w", index, err)
		}
		if previous, ok := seen[name]; ok {
			return nil, nil, fmt.Errorf("presentation.tooltip[%d].field %q duplicates tooltip[%d]", index, name, previous)
		}
		seen[name] = index
		if label != nil {
			if err := validatePresentationText(*label, fmt.Sprintf("presentation.tooltip[%d].label", index), false); err != nil {
				return nil, nil, err
			}
		}
		field, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("presentation.tooltip[%d].field %q is not in the primary result schema", index, name)
		}
		if err := validateTooltipFormat(format, field.DataType, fmt.Sprintf("presentation.tooltip[%d].format", index)); err != nil {
			return nil, nil, err
		}
		ref := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: name}
		refs = append(refs, ref)
		items = append(items, visualizationir.VisualizationTooltipItem{Field: ref, Label: label, Format: format})
	}
	return items, refs, nil
}

func canonicalDashboardTooltip(visual document.DashboardVisual, query LoweredDashboardQuery, fields []visualizationir.VisualizationField) ([]visualizationir.VisualizationTooltipItem, *[]visualizationir.VisualizationFieldRef, error) {
	var values *[]document.DashboardTooltip
	switch variant := visual.Presentation.Value.(type) {
	case *document.CartesianDashboardPresentation:
		if variant != nil {
			values = variant.Tooltip
		}
	case *document.PointDashboardPresentation:
		if variant != nil {
			values = variant.Tooltip
		}
	case *document.ProportionalDashboardPresentation:
		if variant != nil {
			values = variant.Tooltip
		}
	default:
		// Gauge, radar, hierarchy, KPI, grid, and reference-map surfaces do
		// not expose row-bound tooltip authoring in the canonical contract.
		return nil, nil, nil
	}
	items, refs, err := canonicalTooltipItems(values, query, fields)
	if err != nil || values == nil {
		return items, nil, err
	}
	return items, &refs, nil
}

func dashboardTooltipValue(value document.DashboardTooltip) (string, *string, *visualizationir.VisualizationFormat, error) {
	switch {
	case value.String != nil:
		return *value.String, nil, nil, nil
	case value.Item != nil:
		return value.Item.Field, value.Item.Label, value.Item.Format, nil
	default:
		return "", nil, nil, fmt.Errorf("tooltip item is required")
	}
}

func validateTooltipFormat(format *visualizationir.VisualizationFormat, dataType visualizationir.VisualizationDataType, path string) error {
	if format == nil {
		return nil
	}
	kind, err := format.Kind()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	numeric := dataType == visualizationir.VisualizationDataTypeInteger || dataType == visualizationir.VisualizationDataTypeDecimal || dataType == visualizationir.VisualizationDataTypeFloat
	temporal := dataType == visualizationir.VisualizationDataTypeDate || dataType == visualizationir.VisualizationDataTypeTemporal
	switch kind {
	case "number":
		if !numeric {
			return fmt.Errorf("%s kind number is incompatible with %s data", path, dataType)
		}
		value := format.Value.(*visualizationir.NumberVisualizationFormat)
		if err := validateFractionDigits(value.MinimumFractionDigits, value.MaximumFractionDigits, path); err != nil {
			return err
		}
	case "currency":
		if !numeric {
			return fmt.Errorf("%s kind currency is incompatible with %s data", path, dataType)
		}
		value := format.Value.(*visualizationir.CurrencyVisualizationFormat)
		if strings.TrimSpace(value.Currency) == "" {
			return fmt.Errorf("%s.currency must not be empty", path)
		}
		switch value.Currency {
		case "USD", "BRL", "EUR":
		default:
			return fmt.Errorf("%s.currency %q is unsupported; supported currencies are USD, BRL, and EUR", path, value.Currency)
		}
		if err := validateFractionDigits(value.MinimumFractionDigits, value.MaximumFractionDigits, path); err != nil {
			return err
		}
	case "percent":
		if !numeric {
			return fmt.Errorf("%s kind percent is incompatible with %s data", path, dataType)
		}
		value := format.Value.(*visualizationir.PercentVisualizationFormat)
		if err := validateFractionDigits(value.MinimumFractionDigits, value.MaximumFractionDigits, path); err != nil {
			return err
		}
	case "compact":
		if !numeric {
			return fmt.Errorf("%s kind compact is incompatible with %s data", path, dataType)
		}
		value := format.Value.(*visualizationir.CompactVisualizationFormat)
		if err := validateFractionDigits(nil, value.MaximumFractionDigits, path); err != nil {
			return err
		}
	case "duration":
		if !numeric {
			return fmt.Errorf("%s kind duration is incompatible with %s data", path, dataType)
		}
		value := format.Value.(*visualizationir.DurationVisualizationFormat)
		if strings.TrimSpace(value.Unit) == "" {
			return fmt.Errorf("%s.unit must not be empty", path)
		}
		switch value.Unit {
		case "milliseconds", "seconds", "minutes", "hours", "days":
		default:
			return fmt.Errorf("%s.unit %q is unsupported; supported units are milliseconds, seconds, minutes, hours, and days", path, value.Unit)
		}
	case "temporal":
		if !temporal {
			return fmt.Errorf("%s kind temporal is incompatible with %s data", path, dataType)
		}
		value := format.Value.(*visualizationir.TemporalVisualizationFormat)
		if value.DateStyle != nil && !validTemporalStyle(*value.DateStyle) {
			return fmt.Errorf("%s.dateStyle is unsupported", path)
		}
		if value.TimeStyle != nil && !validTemporalStyle(*value.TimeStyle) {
			return fmt.Errorf("%s.timeStyle is unsupported", path)
		}
	default:
		return fmt.Errorf("%s kind %q is unsupported", path, kind)
	}
	return nil
}

func validateFractionDigits(minimum, maximum *int32, path string) error {
	if minimum != nil && (*minimum < 0 || *minimum > 12) {
		return fmt.Errorf("%s.minimumFractionDigits must be between 0 and 12", path)
	}
	if maximum != nil && (*maximum < 0 || *maximum > 12) {
		return fmt.Errorf("%s.maximumFractionDigits must be between 0 and 12", path)
	}
	if minimum != nil && maximum != nil && *minimum > *maximum {
		return fmt.Errorf("%s.minimumFractionDigits must be less than or equal to maximumFractionDigits", path)
	}
	return nil
}

func validTemporalStyle(value string) bool {
	switch value {
	case "full", "long", "medium", "short":
		return true
	default:
		return false
	}
}
