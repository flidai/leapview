package model

import (
	"fmt"
	"strings"
)

// MetricDataType resolves the exact logical numeric type produced by a
// semantic metric, including ratio/derived references. Cyclic references are
// reported rather than recursed indefinitely; callers may use Decimal as the
// conservative fallback for malformed or unknown metrics.
func (m *Model) MetricDataType(name string) (LogicalDataType, error) {
	if m == nil || strings.TrimSpace(name) == "" {
		return DataTypeDecimal, nil
	}
	metric, ok := m.Metrics[name]
	if !ok {
		return DataTypeDecimal, nil
	}
	return m.metricDataType(metric, map[string]bool{name: true})
}

// MetricDataTypeFor resolves a metric value that was already looked up by a
// caller. It is useful for read-only model maps whose Metric.Name is unset.
func (m *Model) MetricDataTypeFor(metric Metric) (LogicalDataType, error) {
	if m == nil {
		return DataTypeDecimal, nil
	}
	visiting := map[string]bool{}
	if strings.TrimSpace(metric.Name) != "" {
		visiting[metric.Name] = true
	}
	return m.metricDataType(metric, visiting)
}

func (m *Model) metricDataType(metric Metric, visiting map[string]bool) (LogicalDataType, error) {
	if metric.Aggregation == "count" || metric.Aggregation == "count_distinct" {
		return DataTypeInteger, nil
	}
	if metric.Input != nil && strings.TrimSpace(metric.Input.Field) != "" {
		if dimension, err := m.ResolveDimension(metric.Input.Field); err == nil {
			switch dimension.Datatype {
			case DataTypeInteger:
				if metric.Aggregation == "avg" {
					return DataTypeDecimal, nil
				}
				return DataTypeInteger, nil
			case DataTypeDecimal:
				return DataTypeDecimal, nil
			case DataTypeFloat:
				return DataTypeFloat, nil
			}
		}
	}
	if metric.Type == "ratio" {
		numerator, err := m.metricReferenceDataType(metric.Numerator, visiting)
		if err != nil {
			return DataTypeDecimal, err
		}
		denominator, err := m.metricReferenceDataType(metric.Denominator, visiting)
		if err != nil {
			return DataTypeDecimal, err
		}
		if numerator == DataTypeFloat || denominator == DataTypeFloat {
			return DataTypeFloat, nil
		}
		return DataTypeDecimal, nil
	}
	if metric.Type == "derived" {
		expression, err := ParseExpression(metric.Expression)
		if err == nil {
			for _, reference := range expression.References() {
				dataType, refErr := m.metricReferenceDataType(reference, visiting)
				if refErr != nil {
					return DataTypeDecimal, refErr
				}
				if dataType == DataTypeFloat {
					return DataTypeFloat, nil
				}
			}
		}
		return DataTypeDecimal, nil
	}
	if metric.Format == "integer" {
		return DataTypeInteger, nil
	}
	return DataTypeDecimal, nil
}

func (m *Model) metricReferenceDataType(name string, visiting map[string]bool) (LogicalDataType, error) {
	if strings.TrimSpace(name) == "" {
		return DataTypeDecimal, nil
	}
	if visiting[name] {
		return DataTypeDecimal, fmt.Errorf("metric datatype dependency cycle includes %q", name)
	}
	metric, ok := m.Metrics[name]
	if !ok {
		return DataTypeDecimal, nil
	}
	visiting[name] = true
	defer delete(visiting, name)
	return m.metricDataType(metric, visiting)
}
