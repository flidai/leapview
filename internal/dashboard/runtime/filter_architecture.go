package runtime

import (
	"fmt"
	"slices"
	"sort"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func semanticBindingFilters(definition *dashboarddefinition.Definition, state dashboardfilter.State, consumerKey string) ([]reportdef.QueryFilter, error) {
	bindings := compiledBindings(definition)
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []reportdef.QueryFilter{}
	for _, key := range keys {
		binding := bindings[key]
		if !slices.Contains(binding.Targets, consumerKey) {
			continue
		}
		applied, ok := state.AppliedControls[binding.Key]
		if !ok {
			continue
		}
		filterDefinition, ok := definition.FilterDefinitions[binding.Filter]
		if !ok {
			return nil, fmt.Errorf("binding %q references unknown compiled filter %q", binding.Key, binding.Filter)
		}
		filters, err := semanticFiltersForExpression(filterDefinition, applied.ResolvedExpression)
		if err != nil {
			return nil, fmt.Errorf("binding %q: %w", binding.Key, err)
		}
		result = append(result, filters...)
	}
	return result, nil
}

func semanticBindingFiltersForTarget(
	definition *dashboarddefinition.Definition,
	state dashboardfilter.State,
	pageID string,
	targetKind string,
	targetID string,
) ([]reportdef.QueryFilter, error) {
	if targetKind != "visual" {
		return nil, nil
	}
	bindings := compiledBindings(definition)
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := []reportdef.QueryFilter{}
	for _, key := range keys {
		binding := bindings[key]
		if !targetMatchesComponent(definition, binding.Targets, pageID, targetID) {
			continue
		}
		applied, ok := state.AppliedControls[key]
		if !ok {
			continue
		}
		filterDefinition, ok := definition.FilterDefinitions[binding.Filter]
		if !ok {
			return nil, fmt.Errorf("binding %q references unknown compiled filter %q", key, binding.Filter)
		}
		filters, err := semanticFiltersForExpression(filterDefinition, applied.ResolvedExpression)
		if err != nil {
			return nil, fmt.Errorf("binding %q: %w", key, err)
		}
		result = append(result, filters...)
	}
	return result, nil
}

func targetMatchesComponent(definition *dashboarddefinition.Definition, targets []string, pageID, visualID string) bool {
	consumerKeys := map[string]struct{}{pageID + "/" + visualID: {}}
	for _, page := range definition.Pages {
		if page.ID != pageID {
			continue
		}
		for _, component := range page.Visuals {
			if component.Kind == "visual" && component.Visual == visualID {
				consumerKeys[pageID+"/"+component.ID] = struct{}{}
			}
		}
	}
	for _, target := range targets {
		if _, ok := consumerKeys[target]; ok {
			return true
		}
	}
	return false
}

func compiledBindings(definition *dashboarddefinition.Definition) map[string]dashboardfilter.Binding {
	result := make(map[string]dashboardfilter.Binding, len(definition.FilterBindings))
	for _, binding := range definition.FilterBindings {
		result[binding.Key] = binding
	}
	for _, page := range definition.Pages {
		for _, binding := range page.FilterBindings {
			result[binding.Key] = binding
		}
	}
	return result
}

func semanticFiltersForExpression(definition dashboardfilter.Definition, expression dashboardfilter.Expression) ([]reportdef.QueryFilter, error) {
	base := reportdef.QueryFilter{Field: definition.Field, Dataset: definition.Dataset}
	switch expression.Kind {
	case "", dashboardfilter.ExpressionUnfiltered:
		return nil, nil
	case dashboardfilter.ExpressionNullCheck:
		base.Operator = string(expression.Operator)
		return []reportdef.QueryFilter{base}, nil
	case dashboardfilter.ExpressionSet:
		base.Operator = string(expression.Operator)
		for _, value := range expression.Values {
			base.Values = append(base.Values, value.Value)
		}
		return []reportdef.QueryFilter{base}, nil
	case dashboardfilter.ExpressionComparison:
		if expression.Value == nil {
			return nil, fmt.Errorf("comparison requires value")
		}
		base.Operator = string(expression.Operator)
		base.Values = []any{expression.Value.Value}
		return []reportdef.QueryFilter{base}, nil
	case dashboardfilter.ExpressionRange:
		result := []reportdef.QueryFilter{}
		if expression.Lower != nil {
			lower := base
			lower.Operator = string(dashboardfilter.OperatorGreaterThan)
			if expression.Lower.Inclusive {
				lower.Operator = string(dashboardfilter.OperatorGreaterThanOrEqual)
			}
			lower.Values = []any{expression.Lower.Value.Value}
			result = append(result, lower)
		}
		if expression.Upper != nil {
			upper := base
			upper.Operator = string(dashboardfilter.OperatorLessThan)
			if expression.Upper.Inclusive {
				upper.Operator = string(dashboardfilter.OperatorLessThanOrEqual)
			}
			upper.Values = []any{expression.Upper.Value.Value}
			result = append(result, upper)
		}
		return result, nil
	case dashboardfilter.ExpressionRelativePeriod:
		return nil, fmt.Errorf("relative period reached query planning without a resolved range")
	default:
		return nil, fmt.Errorf("unsupported expression kind %q", expression.Kind)
	}
}
