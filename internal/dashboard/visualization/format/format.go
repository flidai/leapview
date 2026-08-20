// Package format implements deterministic visualization formatting shared by
// browser-independent renderers and exports.
package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type localeData struct{ decimal, group, currencySpace string }

var locales = map[string]localeData{
	"en-US": {decimal: ".", group: ",", currencySpace: ""},
	"pt-BR": {decimal: ",", group: ".", currencySpace: "\u00a0"},
}
var currencies = map[string]map[string]string{
	"en-US": {"USD": "$", "BRL": "R$", "EUR": "€"},
	"pt-BR": {"USD": "US$", "BRL": "R$", "EUR": "€"},
}

func Value(locale string, format ir.VisualizationFormat, value any) (string, error) {
	data, ok := locales[locale]
	if !ok {
		return "", fmt.Errorf("unsupported visualization locale %q", locale)
	}
	if value == nil {
		return "—", nil
	}
	switch spec := format.Value.(type) {
	case *ir.NumberVisualizationFormat:
		return number(data, value, digits(spec.MinimumFractionDigits, 0), digits(spec.MaximumFractionDigits, 3), "")
	case *ir.CurrencyVisualizationFormat:
		symbol, ok := currencies[locale][spec.Currency]
		if !ok {
			return "", fmt.Errorf("unsupported visualization currency %q", spec.Currency)
		}
		formatted, err := number(data, value, digits(spec.MinimumFractionDigits, 2), digits(spec.MaximumFractionDigits, 2), "")
		if err != nil {
			return "", err
		}
		return symbol + data.currencySpace + formatted, nil
	case *ir.PercentVisualizationFormat:
		value, err := numeric(value)
		if err != nil {
			return "", err
		}
		return number(data, value*100, digits(spec.MinimumFractionDigits, 0), digits(spec.MaximumFractionDigits, 1), "%")
	case *ir.CompactVisualizationFormat:
		value, err := numeric(value)
		if err != nil {
			return "", err
		}
		scale, suffix := 1.0, ""
		absolute := math.Abs(value)
		if absolute >= 1_000_000_000 {
			scale, suffix = 1_000_000_000, "B"
		} else if absolute >= 1_000_000 {
			scale, suffix = 1_000_000, "M"
		} else if absolute >= 1_000 {
			scale, suffix = 1_000, "K"
		}
		return number(data, value/scale, 0, digits(spec.MaximumFractionDigits, 1), suffix)
	case *ir.DurationVisualizationFormat:
		value, err := numeric(value)
		if err != nil {
			return "", err
		}
		return duration(data, value, spec.Unit)
	case *ir.TemporalVisualizationFormat:
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("temporal visualization value must be a string")
		}
		if parsed, dateErr := time.Parse(time.DateOnly, text); dateErr == nil {
			if spec.TimeStyle != nil && spec.DateStyle == nil {
				return "", fmt.Errorf("date-only visualization value cannot satisfy a time-only format")
			}
			return parsed.Format(time.DateOnly), nil
		}
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return "", fmt.Errorf("parse temporal visualization value: %w", err)
		}
		if spec.TimeStyle != nil && spec.DateStyle == nil {
			return parsed.UTC().Format("15:04:05"), nil
		}
		return parsed.UTC().Format("2006-01-02"), nil
	default:
		return "", fmt.Errorf("unsupported visualization format %T", format.Value)
	}
}

func number(locale localeData, raw any, minimum, maximum int, suffix string) (string, error) {
	value, err := numeric(raw)
	if err != nil {
		return "", err
	}
	if maximum < minimum || minimum < 0 || maximum > 12 {
		return "", fmt.Errorf("invalid fraction digit range %d..%d", minimum, maximum)
	}
	factor := math.Pow10(maximum)
	rounded := math.Round(math.Abs(value)*factor) / factor
	parts := strings.Split(strconv.FormatFloat(rounded, 'f', maximum, 64), ".")
	for len(parts) > 1 && len(parts[1]) > minimum && strings.HasSuffix(parts[1], "0") {
		parts[1] = strings.TrimSuffix(parts[1], "0")
	}
	integer := group(parts[0], locale.group)
	if value < 0 {
		integer = "-" + integer
	}
	if len(parts) == 1 || parts[1] == "" {
		return integer + suffix, nil
	}
	return integer + locale.decimal + parts[1] + suffix, nil
}

func numeric(value any) (float64, error) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case int:
		number = float64(value)
	case int64:
		number = float64(value)
	case int32:
		number = float64(value)
	default:
		return 0, fmt.Errorf("visualization value %T is not numeric", value)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("visualization number must be finite")
	}
	return number, nil
}

func group(value, separator string) string {
	if len(value) <= 3 {
		return value
	}
	first := len(value) % 3
	if first == 0 {
		first = 3
	}
	var out strings.Builder
	out.WriteString(value[:first])
	for index := first; index < len(value); index += 3 {
		out.WriteString(separator)
		out.WriteString(value[index : index+3])
	}
	return out.String()
}

func digits(value *int32, fallback int) int {
	if value == nil {
		return fallback
	}
	return int(*value)
}

func duration(locale localeData, value float64, unit string) (string, error) {
	if unit == "days" {
		return number(locale, value, 0, 1, "d")
	}
	seconds := int64(math.Round(value))
	switch unit {
	case "milliseconds":
		seconds = int64(math.Round(value / 1000))
	case "minutes":
		seconds *= 60
	case "hours":
		seconds *= 3600
	case "seconds":
	default:
		return "", fmt.Errorf("unsupported duration unit %q", unit)
	}
	hours, rest := seconds/3600, seconds%3600
	minutes, secs := rest/60, rest%60
	parts := []string{}
	if hours != 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes != 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if secs != 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%ds", secs))
	}
	return strings.Join(parts, " "), nil
}
