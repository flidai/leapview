package model

import (
	"fmt"
	"strings"
)

func (m *Model) ResolveDimension(ref string) (MetricDimension, error) {
	if !strings.Contains(ref, ".") {
		return MetricDimension{}, fmt.Errorf("semantic dimension %q is not a physical field", ref)
	}
	tableName, fieldName, err := splitSemanticField(ref)
	if err != nil {
		return MetricDimension{}, err
	}
	table, ok := m.Tables[tableName]
	if !ok {
		return MetricDimension{}, fmt.Errorf("unknown table %q", tableName)
	}
	dimension, ok := table.Dimensions[fieldName]
	if !ok {
		return MetricDimension{}, fmt.Errorf("unknown field %q on table %q", fieldName, tableName)
	}
	dimension.Field = ref
	dimension.Table = tableName
	dimension.Name = fieldName
	return dimension, nil
}

func (m *Model) ResolveRelationshipEndpoint(ref string) (MetricDimension, error) {
	tableName, fieldName, err := splitSemanticField(ref)
	if err != nil {
		return MetricDimension{}, err
	}
	table, ok := m.Tables[tableName]
	if !ok {
		return MetricDimension{}, fmt.Errorf("unknown table %q", tableName)
	}
	if dimension, ok := table.Dimensions[fieldName]; ok {
		dimension.Field = ref
		dimension.Table = tableName
		dimension.Name = fieldName
		return dimension, nil
	}
	return MetricDimension{}, fmt.Errorf("unknown relationship endpoint field %q on table %q", fieldName, tableName)
}

func (m *Model) ResolveMetric(ref string) (Metric, error) {
	metric, ok := m.Metrics[ref]
	if !ok {
		return Metric{}, fmt.Errorf("unknown metric %q", ref)
	}
	metric.Name = ref
	return metric, nil
}

func (m *Model) ResolveAggregateMetric(ref string) (Metric, error) {
	metric, err := m.ResolveMetric(ref)
	if err != nil {
		return Metric{}, err
	}
	if metric.Type != "aggregate" {
		return Metric{}, fmt.Errorf("metric %q is not aggregate", ref)
	}
	if metric.Input == nil || strings.TrimSpace(metric.Input.Field) == "" {
		return Metric{}, fmt.Errorf("metric %q aggregate input is required", ref)
	}
	return metric, nil
}

func (m *Model) ResolveSemanticDimension(ref string) (SemanticDimension, error) {
	dimension, ok := m.Dimensions[ref]
	if !ok {
		return SemanticDimension{}, fmt.Errorf("unknown semantic dimension %q", ref)
	}
	dimension.Name = ref
	return dimension, nil
}

func (m *Model) ValidateQueryDimension(ref string) error {
	if _, ok := m.Dimensions[ref]; ok {
		return nil
	}
	_, err := m.ResolveDimension(ref)
	return err
}

func (m *Model) ValidateAggregateMember(ref string) error {
	if _, ok := m.Metrics[ref]; ok {
		return nil
	}
	return fmt.Errorf("unknown metric or metric %q", ref)
}

func (m *Model) ResolveField(ref string) (MetricDimension, Metric, string, error) {
	if dimension, err := m.ResolveDimension(ref); err == nil {
		return dimension, Metric{}, "dimension", nil
	}
	if metric, err := m.ResolveMetric(ref); err == nil {
		return MetricDimension{}, metric, "metric", nil
	}
	return MetricDimension{}, Metric{}, "", fmt.Errorf("unknown field %q", ref)
}

func splitSemanticField(ref string) (string, string, error) {
	parts := strings.Split(ref, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("field %q must be qualified as table.field", ref)
	}
	if err := validateSemanticIdentifier(parts[0]); err != nil {
		return "", "", fmt.Errorf("table %q is invalid: %w", parts[0], err)
	}
	if err := validateSemanticIdentifier(parts[1]); err != nil {
		return "", "", fmt.Errorf("field %q is invalid: %w", parts[1], err)
	}
	return parts[0], parts[1], nil
}

func (d MetricDimension) SQLExpression() string {
	return d.Name
}
