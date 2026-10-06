package explorehandoff

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
)

// SpecForState preserves the effective predicates of this visual, including
// resolved relative periods and interactions scoped to the active page.
func SpecForState(definition dashboarddefinition.Definition, model *semanticmodel.Model, visualID, pageID string, filters dashboard.Filters) (exploration.ExplorationSpec, bool) {
	spec, ok := SpecForVisual(definition.Visualizations[visualID], model)
	if !ok || filters.CompiledState == nil && len(definition.CompiledFilterBindings()) != 0 {
		return exploration.ExplorationSpec{}, false
	}
	if filters.CompiledState != nil {
		bindings := definition.CompiledFilterBindings()
		for key, applied := range filters.CompiledState.AppliedControls {
			if _, found := bindings[key]; !found && (applied.Expression.Kind != "unfiltered" || applied.ResolvedExpression.Kind != "unfiltered") {
				return exploration.ExplorationSpec{}, false
			}
		}
	}
	for _, selection := range filters.Selections {
		if selection.SourceKind == "" || selection.SourceID == "" || len(selection.Entries) == 0 {
			return exploration.ExplorationSpec{}, false
		}
	}
	// Spatial predicates have no canonical Explorer representation yet.
	if len(filters.SpatialSelections) != 0 {
		return exploration.ExplorationSpec{}, false
	}
	filters.ActivePageID = pageID
	predicates, err := dashboardruntime.QueryFiltersForVisual(model, &definition, filters, visualID)
	if err != nil {
		return exploration.ExplorationSpec{}, false
	}
	for _, predicate := range predicates {
		converted, ok := explorerFilter(model, *spec.DatasetID, predicate)
		if !ok {
			return exploration.ExplorationSpec{}, false
		}
		spec.Filters = append(spec.Filters, converted)
	}
	if exploration.ValidateShape(&spec) != nil {
		return exploration.ExplorationSpec{}, false
	}
	return spec, true
}

func explorerFilter(model *semanticmodel.Model, dataset string, predicate report.QueryFilter) (exploration.ExplorationFilter, bool) {
	// Additive selections of one field are an IN predicate. Other OR groups
	// cannot be flattened into Explorer's conjunctive filter list.
	if len(predicate.Groups) > 0 {
		merged := report.QueryFilter{Operator: "in"}
		for _, group := range predicate.Groups {
			if len(group.Filters) != 1 {
				return exploration.ExplorationFilter{}, false
			}
			item := group.Filters[0]
			if item.Spatial != nil || len(item.Groups) != 0 || item.Operator != "equals" || len(item.Values) != 1 {
				return exploration.ExplorationFilter{}, false
			}
			if merged.Field == "" {
				merged.Field, merged.Dataset = item.Field, item.Dataset
			}
			if merged.Field != item.Field || merged.Dataset != item.Dataset {
				return exploration.ExplorationFilter{}, false
			}
			merged.Values = append(merged.Values, item.Values...)
		}
		predicate = merged
	}
	if predicate.Spatial != nil {
		return exploration.ExplorationFilter{}, false
	}
	if predicate.Dataset != "" {
		dataset = predicate.Dataset
	}
	field, ok := canonicalExplorerDimensionID(model, dataset, predicate.Field)
	if !ok {
		return exploration.ExplorationFilter{}, false
	}
	datatype := ""
	if semantic, err := model.ResolveSemanticDimension(predicate.Field); err == nil {
		datatype = string(semantic.Datatype)
		if datatype == "" {
			datatype = semantic.Type
		}
	} else if dimension, err := model.ResolveDimension(field); err == nil {
		datatype = string(dimension.Datatype)
		if datatype == "" {
			datatype = dimension.Type
		}
	}
	values := make([]exploration.ExplorationFilterValue, 0, len(predicate.Values))
	for _, raw := range predicate.Values {
		value, ok := explorerFilterValue(raw, datatype)
		if !ok {
			return exploration.ExplorationFilter{}, false
		}
		values = append(values, value)
	}
	var expression exploration.ExplorationFilterExpressionVariant
	switch predicate.Operator {
	case "is_null", "is_not_null":
		expression = &exploration.NullCheckExplorationFilterExpression{Kind: "null_check", Operator: predicate.Operator}
	case "in", "not_in":
		expression = &exploration.SetExplorationFilterExpression{Kind: "set", Operator: predicate.Operator, Values: values}
	default:
		if len(values) != 1 {
			return exploration.ExplorationFilter{}, false
		}
		expression = &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: predicate.Operator, Value: values[0]}
	}
	return exploration.ExplorationFilter{Field: field, DatasetID: optionalAlias(predicate.Dataset), Expression: exploration.ExplorationFilterExpression{Value: expression}}, true
}

func explorerFilterValue(raw any, datatype string) (exploration.ExplorationFilterValue, bool) {
	if raw == nil {
		return exploration.ExplorationFilterValue{}, false
	}
	kind := "string"
	value := any(fmt.Sprint(raw))
	switch strings.ToLower(datatype) {
	case "date":
		kind = "date"
	case "datetime", "datetimetz", "timestamp":
		kind = "timestamp"
	case "integer", "int", "bigint":
		kind = "integer"
	case "number", "float", "double", "decimal", "numeric":
		kind = "decimal"
	case "bool", "boolean":
		kind = "boolean"
		boolean, ok := raw.(bool)
		if !ok {
			return exploration.ExplorationFilterValue{}, false
		}
		value = boolean
	}
	payload, err := json.Marshal(map[string]any{"kind": kind, "value": value})
	if err != nil {
		return exploration.ExplorationFilterValue{}, false
	}
	var result exploration.ExplorationFilterValue
	if json.Unmarshal(payload, &result) != nil {
		return exploration.ExplorationFilterValue{}, false
	}
	return result, true
}
