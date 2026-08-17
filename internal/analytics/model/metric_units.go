package model

import (
	"fmt"
	"strings"
)

// metricUnit is deliberately small: known units are ISO-4217 currencies or
// the dimensionless count marker. Other authored units remain metadata and
// do not participate in compatibility checks.
type metricUnit struct {
	name          string
	known         bool
	dimensionless bool
}

// ISO 4217 List One is pinned here instead of accepting arbitrary three-letter
// strings as known units. The list contains current currency, fund, precious
// metal, testing, and no-currency codes; withdrawn codes belong to List Three
// and are intentionally not included.
//
// Snapshot metadata (the XML root's Pblshd date is also its effective date):
//   - version: ISO 4217:2015, List One publication 2026-01-01
//   - effective: 2026-01-01
//   - source: https://www.six-group.com/dam/download/financial-information/data-center/iso-currrency/lists/list-one.xml
const (
	iso4217SnapshotVersion   = "ISO 4217:2015 List One (2026-01-01 publication)"
	iso4217SnapshotEffective = "2026-01-01"
	iso4217SnapshotSource    = "https://www.six-group.com/dam/download/financial-information/data-center/iso-currrency/lists/list-one.xml"
)

var iso4217Units = map[string]struct{}{
	"AED": {}, "AFN": {}, "ALL": {}, "AMD": {}, "AOA": {}, "ARS": {}, "AUD": {}, "AWG": {}, "AZN": {}, "BAM": {},
	"BBD": {}, "BDT": {}, "BHD": {}, "BIF": {}, "BMD": {}, "BND": {}, "BOB": {}, "BOV": {}, "BRL": {}, "BSD": {},
	"BTN": {}, "BWP": {}, "BYN": {}, "BZD": {}, "CAD": {}, "CDF": {}, "CHE": {}, "CHF": {}, "CHW": {}, "CLF": {},
	"CLP": {}, "CNY": {}, "COP": {}, "COU": {}, "CRC": {}, "CUP": {}, "CVE": {}, "CZK": {}, "DJF": {}, "DKK": {},
	"DOP": {}, "DZD": {}, "EGP": {}, "ERN": {}, "ETB": {}, "EUR": {}, "FJD": {}, "FKP": {}, "GBP": {}, "GEL": {},
	"GHS": {}, "GIP": {}, "GMD": {}, "GNF": {}, "GTQ": {}, "GYD": {}, "HKD": {}, "HNL": {}, "HTG": {}, "HUF": {},
	"IDR": {}, "ILS": {}, "INR": {}, "IQD": {}, "IRR": {}, "ISK": {}, "JMD": {}, "JOD": {}, "JPY": {}, "KES": {},
	"KGS": {}, "KHR": {}, "KMF": {}, "KPW": {}, "KRW": {}, "KWD": {}, "KYD": {}, "KZT": {}, "LAK": {}, "LBP": {},
	"LKR": {}, "LRD": {}, "LSL": {}, "LYD": {}, "MAD": {}, "MDL": {}, "MGA": {}, "MKD": {}, "MMK": {}, "MNT": {},
	"MOP": {}, "MRU": {}, "MUR": {}, "MVR": {}, "MWK": {}, "MXN": {}, "MXV": {}, "MYR": {}, "MZN": {}, "NAD": {},
	"NGN": {}, "NIO": {}, "NOK": {}, "NPR": {}, "NZD": {}, "OMR": {}, "PAB": {}, "PEN": {}, "PGK": {}, "PHP": {},
	"PKR": {}, "PLN": {}, "PYG": {}, "QAR": {}, "RON": {}, "RSD": {}, "RUB": {}, "RWF": {}, "SAR": {}, "SBD": {},
	"SCR": {}, "SDG": {}, "SEK": {}, "SGD": {}, "SHP": {}, "SLE": {}, "SOS": {}, "SRD": {}, "SSP": {}, "STN": {},
	"SVC": {}, "SYP": {}, "SZL": {}, "THB": {}, "TJS": {}, "TMT": {}, "TND": {}, "TOP": {}, "TRY": {}, "TTD": {},
	"TWD": {}, "TZS": {}, "UAH": {}, "UGX": {}, "USD": {}, "USN": {}, "UYI": {}, "UYU": {}, "UYW": {}, "UZS": {},
	"VED": {}, "VES": {}, "VND": {}, "VUV": {}, "WST": {}, "XAD": {}, "XAF": {}, "XAG": {}, "XAU": {}, "XBA": {},
	"XBB": {}, "XBC": {}, "XBD": {}, "XCD": {}, "XCG": {}, "XDR": {}, "XOF": {}, "XPD": {}, "XPF": {}, "XPT": {},
	"XSU": {}, "XTS": {}, "XUA": {}, "XXX": {}, "YER": {}, "ZAR": {}, "ZMW": {}, "ZWG": {},
}

func normalizeMetricUnit(value string) metricUnit {
	name := strings.ToUpper(strings.TrimSpace(value))
	if name == "" {
		return metricUnit{}
	}
	if strings.EqualFold(name, "dimensionless") {
		return metricUnit{name: "dimensionless", known: true, dimensionless: true}
	}
	if _, ok := iso4217Units[name]; ok {
		return metricUnit{name: name, known: true}
	}
	return metricUnit{name: strings.TrimSpace(value)}
}

func (m *Model) validateMetricUnits() error {
	units := make(map[string]metricUnit, len(m.Metrics))
	for name, metric := range m.Metrics {
		if metric.Type == "aggregate" && (metric.Aggregation == "count" || metric.Aggregation == "count_distinct") {
			units[name] = metricUnit{name: "dimensionless", known: true, dimensionless: true}
			continue
		}
		units[name] = normalizeMetricUnit(metric.Unit)
	}
	state := map[string]int{}
	var inferMetric func(string) (metricUnit, error)
	inferMetric = func(name string) (metricUnit, error) {
		if state[name] == 2 {
			return units[name], nil
		}
		if state[name] == 1 {
			return metricUnit{}, fmt.Errorf("semantic metric dependency cycle includes %q", name)
		}
		metric, ok := m.Metrics[name]
		if !ok {
			return metricUnit{}, nil
		}
		state[name] = 1
		inferred := units[name]
		switch metric.Type {
		case "aggregate":
			// Aggregate units are authored metadata except for count metrics,
			// whose result is always dimensionless.
		case "ratio":
			numerator, err := inferMetric(metric.Numerator)
			if err != nil {
				return metricUnit{}, err
			}
			denominator, err := inferMetric(metric.Denominator)
			if err != nil {
				return metricUnit{}, err
			}
			inferred = divideMetricUnits(numerator, denominator)
		case "derived":
			expression, err := ParseExpression(metric.Expression)
			if err != nil {
				return metricUnit{}, err
			}
			inferred, err = inferExpressionUnit(expression.root, inferMetric)
			if err != nil {
				return metricUnit{}, fmt.Errorf("semantic metric %q units: %w", name, err)
			}
		}
		authored := normalizeMetricUnit(metric.Unit)
		if authored.known && inferred.known && !sameMetricUnit(authored, inferred) {
			return metricUnit{}, fmt.Errorf("semantic metric %q unit %q contradicts inferred unit %q", name, metric.Unit, inferred.name)
		}
		isCount := metric.Type == "aggregate" && (metric.Aggregation == "count" || metric.Aggregation == "count_distinct")
		if strings.TrimSpace(metric.Unit) == "" && inferred.known && !isCount {
			metric.Unit = inferred.name
			m.Metrics[name] = metric
		}
		if isCount && strings.TrimSpace(metric.Unit) != "" && !authored.dimensionless {
			return metricUnit{}, fmt.Errorf("semantic metric %q count aggregation must be dimensionless, got unit %q", name, metric.Unit)
		}
		units[name] = inferred
		state[name] = 2
		return inferred, nil
	}
	for name := range m.Metrics {
		if _, err := inferMetric(name); err != nil {
			return err
		}
	}
	return nil
}

func inferExpressionUnit(node expressionNode, resolve func(string) (metricUnit, error)) (metricUnit, error) {
	switch value := node.(type) {
	case expressionRef:
		return resolve(string(value))
	case expressionNumber:
		return metricUnit{name: "dimensionless", known: true, dimensionless: true}, nil
	case expressionUnary:
		return inferExpressionUnit(value.node, resolve)
	case expressionBinary:
		left, err := inferExpressionUnit(value.left, resolve)
		if err != nil {
			return metricUnit{}, err
		}
		right, err := inferExpressionUnit(value.right, resolve)
		if err != nil {
			return metricUnit{}, err
		}
		switch value.op {
		case '+', '-':
			return addMetricUnits(left, right)
		case '*':
			return multiplyMetricUnits(left, right), nil
		case '/':
			return divideMetricUnits(left, right), nil
		default:
			return metricUnit{}, nil
		}
	case expressionCall:
		switch value.name {
		case "safe_divide":
			if len(value.args) != 2 {
				return metricUnit{}, nil
			}
			numerator, err := inferExpressionUnit(value.args[0], resolve)
			if err != nil {
				return metricUnit{}, err
			}
			denominator, err := inferExpressionUnit(value.args[1], resolve)
			if err != nil {
				return metricUnit{}, err
			}
			return divideMetricUnits(numerator, denominator), nil
		case "abs", "round", "nullif":
			if len(value.args) == 0 {
				return metricUnit{}, nil
			}
			first, err := inferExpressionUnit(value.args[0], resolve)
			if err != nil {
				return metricUnit{}, err
			}
			if value.name == "nullif" && len(value.args) > 1 {
				second, err := inferExpressionUnit(value.args[1], resolve)
				if err != nil {
					return metricUnit{}, err
				}
				if first.known && second.known {
					if _, err := addMetricUnits(first, second); err != nil {
						return metricUnit{}, err
					}
				}
			}
			return first, nil
		case "coalesce":
			if len(value.args) == 0 {
				return metricUnit{}, nil
			}
			result, err := inferExpressionUnit(value.args[0], resolve)
			if err != nil {
				return metricUnit{}, err
			}
			for _, arg := range value.args[1:] {
				unit, err := inferExpressionUnit(arg, resolve)
				if err != nil {
					return metricUnit{}, err
				}
				result, err = addMetricUnits(result, unit)
				if err != nil {
					return metricUnit{}, err
				}
			}
			return result, nil
		default:
			return metricUnit{}, nil
		}
	default:
		return metricUnit{}, nil
	}
}

func sameMetricUnit(left, right metricUnit) bool {
	return left.known && right.known && left.dimensionless == right.dimensionless && left.name == right.name
}

func addMetricUnits(left, right metricUnit) (metricUnit, error) {
	if !left.known || !right.known {
		return metricUnit{}, nil
	}
	if left.dimensionless && right.dimensionless {
		return metricUnit{name: "dimensionless", known: true, dimensionless: true}, nil
	}
	if left.dimensionless != right.dimensionless || left.name != right.name {
		return metricUnit{}, fmt.Errorf("incompatible additive units %q and %q", left.name, right.name)
	}
	return left, nil
}

func multiplyMetricUnits(left, right metricUnit) metricUnit {
	if !left.known || !right.known {
		return metricUnit{}
	}
	if left.dimensionless {
		return right
	}
	if right.dimensionless {
		return left
	}
	return metricUnit{}
}

func divideMetricUnits(numerator, denominator metricUnit) metricUnit {
	if !numerator.known || !denominator.known {
		return metricUnit{}
	}
	if denominator.dimensionless {
		return numerator
	}
	if numerator.dimensionless {
		return metricUnit{}
	}
	if numerator.name == denominator.name {
		return metricUnit{name: "dimensionless", known: true, dimensionless: true}
	}
	return metricUnit{}
}
