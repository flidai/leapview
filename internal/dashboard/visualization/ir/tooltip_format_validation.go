package ir

import (
	"fmt"
	"strings"
)

func validateVisualizationTooltipFormat(format *VisualizationFormat, dataType VisualizationDataType, path string) error {
	if format == nil {
		return nil
	}
	numeric := dataType == VisualizationDataTypeInteger || dataType == VisualizationDataTypeDecimal || dataType == VisualizationDataTypeFloat
	temporal := dataType == VisualizationDataTypeDate || dataType == VisualizationDataTypeTemporal
	switch value := format.Value.(type) {
	case *NumberVisualizationFormat:
		if !numeric {
			return fmt.Errorf("%s kind number is incompatible with %s data", path, dataType)
		}
		return validateIRFractionDigits(value.MinimumFractionDigits, value.MaximumFractionDigits, path)
	case *CurrencyVisualizationFormat:
		if !numeric {
			return fmt.Errorf("%s kind currency is incompatible with %s data", path, dataType)
		}
		if strings.TrimSpace(value.Currency) == "" {
			return fmt.Errorf("%s.currency must not be empty", path)
		}
		if strings.TrimSpace(value.Currency) != value.Currency {
			return fmt.Errorf("%s.currency must not contain surrounding whitespace", path)
		}
		switch strings.TrimSpace(value.Currency) {
		case "USD", "BRL", "EUR":
		default:
			return fmt.Errorf("%s.currency %q is unsupported; supported currencies are USD, BRL, and EUR", path, value.Currency)
		}
		return validateIRFractionDigits(value.MinimumFractionDigits, value.MaximumFractionDigits, path)
	case *PercentVisualizationFormat:
		if !numeric {
			return fmt.Errorf("%s kind percent is incompatible with %s data", path, dataType)
		}
		return validateIRFractionDigits(value.MinimumFractionDigits, value.MaximumFractionDigits, path)
	case *CompactVisualizationFormat:
		if !numeric {
			return fmt.Errorf("%s kind compact is incompatible with %s data", path, dataType)
		}
		return validateIRFractionDigits(nil, value.MaximumFractionDigits, path)
	case *DurationVisualizationFormat:
		if !numeric {
			return fmt.Errorf("%s kind duration is incompatible with %s data", path, dataType)
		}
		if strings.TrimSpace(value.Unit) == "" {
			return fmt.Errorf("%s.unit must not be empty", path)
		}
		if strings.TrimSpace(value.Unit) != value.Unit {
			return fmt.Errorf("%s.unit must not contain surrounding whitespace", path)
		}
		switch strings.TrimSpace(value.Unit) {
		case "milliseconds", "seconds", "minutes", "hours", "days":
		default:
			return fmt.Errorf("%s.unit %q is unsupported; supported units are milliseconds, seconds, minutes, hours, and days", path, value.Unit)
		}
	case *TemporalVisualizationFormat:
		if !temporal {
			return fmt.Errorf("%s kind temporal is incompatible with %s data", path, dataType)
		}
		if (value.DateStyle != nil && !validIRTemporalStyle(*value.DateStyle)) || (value.TimeStyle != nil && !validIRTemporalStyle(*value.TimeStyle)) {
			return fmt.Errorf("%s dateStyle/timeStyle is unsupported", path)
		}
	default:
		return fmt.Errorf("%s format variant is required", path)
	}
	return nil
}
