package query

import (
	"fmt"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// compileSemanticFilter converts the canonical recursive filter tree into the
// planner's typed predicate representation. Named filters remain canonical in
// the model; this is deliberately a planner-boundary adapter.
func compileSemanticFilter(model *semanticmodel.Model, spec semanticmodel.SemanticFilterSpec) (Filter, error) {
	if len(spec.All) > 0 {
		children := make([]Filter, 0, len(spec.All))
		for _, child := range spec.All {
			compiled, err := compileSemanticFilter(model, child)
			if err != nil {
				return Filter{}, err
			}
			children = append(children, compiled)
		}
		return Filter{Groups: []FilterGroup{{Filters: children}}}, nil
	}
	if len(spec.Any) > 0 {
		groups := make([]FilterGroup, 0, len(spec.Any))
		for _, child := range spec.Any {
			compiled, err := compileSemanticFilter(model, child)
			if err != nil {
				return Filter{}, err
			}
			groups = append(groups, FilterGroup{Filters: []Filter{compiled}})
		}
		return Filter{Groups: groups}, nil
	}
	if spec.Not != nil {
		compiled, err := compileSemanticFilter(model, *spec.Not)
		if err != nil {
			return Filter{}, err
		}
		compiled.Not = !compiled.Not
		return compiled, nil
	}
	if strings.TrimSpace(spec.Field) == "" {
		return Filter{}, fmt.Errorf("semantic filter leaf requires field")
	}
	if strings.TrimSpace(spec.Operator) == "" {
		return Filter{}, fmt.Errorf("semantic filter leaf requires operator")
	}
	dimension, err := model.ResolveDimension(spec.Field)
	if err != nil {
		return Filter{}, err
	}
	filter := Filter{Field: spec.Field, Operator: spec.Operator, Path: append([]string(nil), spec.Path...)}
	switch spec.Operator {
	case "is_null", "is_not_null":
		if spec.Value != nil {
			return Filter{}, fmt.Errorf("semantic filter %q does not accept a value", spec.Operator)
		}
	case "in", "not_in":
		values, ok := semanticFilterList(spec.Value)
		if !ok || len(values) == 0 {
			return Filter{}, fmt.Errorf("semantic filter %q requires a non-empty value list", spec.Operator)
		}
		filter.Values = make([]any, 0, len(values))
		for _, value := range values {
			coerced, err := coerceSemanticLiteral(value, dimension)
			if err != nil {
				return Filter{}, fmt.Errorf("semantic filter %q: %w", spec.Field, err)
			}
			filter.Values = append(filter.Values, coerced)
		}
	default:
		if spec.Value == nil {
			return Filter{}, fmt.Errorf("semantic filter %q requires a value", spec.Operator)
		}
		coerced, err := coerceSemanticLiteral(spec.Value, dimension)
		if err != nil {
			return Filter{}, fmt.Errorf("semantic filter %q: %w", spec.Field, err)
		}
		filter.Values = []any{coerced}
	}
	return filter, nil
}

func compileNamedSemanticFilters(model *semanticmodel.Model, names []string) ([]Filter, error) {
	filters := make([]Filter, 0, len(names))
	for _, name := range names {
		spec, ok := model.Filters[name]
		if !ok {
			return nil, fmt.Errorf("unknown semantic filter %q", name)
		}
		filter, err := compileSemanticFilter(model, spec)
		if err != nil {
			return nil, fmt.Errorf("semantic filter %q: %w", name, err)
		}
		filter.RequireMatch = true
		filters = append(filters, filter)
	}
	return filters, nil
}

func scopeMetricWhereFilters(filters []Filter, fact string) []Filter {
	out := make([]Filter, len(filters))
	for index, filter := range filters {
		out[index] = scopeMetricWhereFilter(filter, fact)
	}
	return out
}

func namedMetricFilters(filters []CompiledNamedFilter) []Filter {
	out := make([]Filter, 0, len(filters))
	for _, named := range filters {
		out = append(out, named.Filter)
	}
	return out
}

func scopeMetricWhereFilter(filter Filter, fact string) Filter {
	if filter.Field != "" && filter.Fact == "" {
		filter.Fact = fact
	}
	for index, group := range filter.Groups {
		for childIndex, child := range group.Filters {
			group.Filters[childIndex] = scopeMetricWhereFilter(child, fact)
		}
		filter.Groups[index] = group
	}
	return filter
}

func markFilterMatchGuards(filter Filter) Filter {
	if filter.Field != "" {
		filter.MatchGuard = true
	}
	for index, group := range filter.Groups {
		for childIndex, child := range group.Filters {
			group.Filters[childIndex] = markFilterMatchGuards(child)
		}
		filter.Groups[index] = group
	}
	return filter
}

func semanticFilterList(value any) ([]any, bool) {
	switch values := value.(type) {
	case []any:
		return values, true
	case []string:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []int:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []int64:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []float64:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []bool:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func coerceSemanticLiteral(value any, dimension semanticmodel.MetricDimension) (any, error) {
	return semanticmodel.CoerceSemanticLiteral(value, dimension)
}
