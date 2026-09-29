package explorationadapter

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func (c converter) filters(visualID string) ([]document.DashboardFilter, error) {
	filters := make([]document.DashboardFilter, 0, len(c.spec.Filters)+1)
	for index, value := range c.spec.Filters {
		field, err := c.resolveFilterField(value)
		if err != nil {
			return nil, err
		}
		expression, control, operators, err := filterExpression(value.Expression)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", index, err)
		}
		id, err := scopedFilterID(visualID, index+1)
		if err != nil {
			return nil, err
		}
		label := field
		targets := []string{visualID}
		filters = append(filters, document.DashboardFilter{ID: id, Label: label, Dimension: field, Control: control, Operators: operators, Default: expression, Targets: &targets})
	}
	if c.spec.Time != nil && c.spec.Time.Range != nil {
		field, err := c.resolve(c.spec.Time.Field, "time")
		if err != nil {
			return nil, err
		}
		expression, err := timeRangeExpression(*c.spec.Time.Range)
		if err != nil {
			return nil, err
		}
		id, err := scopedFilterID(visualID, len(filters)+1)
		if err != nil {
			return nil, err
		}
		targets := []string{visualID}
		filters = append(filters, document.DashboardFilter{ID: id, Label: field, Dimension: field, Control: document.DashboardFilterControl{Value: &document.DateRangeDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "dateRange"}, Type: "dateRange"}}, Default: expression, Targets: &targets})
	}
	return filters, nil
}

func (c converter) resolveFilterField(value exploration.ExplorationFilter) (string, error) {
	if value.DatasetID != nil && strings.TrimSpace(*value.DatasetID) != "" {
		dataset := strings.TrimSpace(*value.DatasetID)
		field := strings.TrimSpace(value.Field)
		if hasConflictingDataset(field, dataset) {
			return "", fmt.Errorf("filter field %q conflicts with dataset %q", field, dataset)
		}
		if mapped := strings.TrimSpace(c.options.Bindings[dataset+"."+field]); mapped != "" {
			return mapped, nil
		}
		if mapped := strings.TrimSpace(c.options.Bindings[field]); mapped != "" {
			return mapped, nil
		}
		if strings.Contains(field, ".") {
			return "", fmt.Errorf("filter field %q is dataset-qualified but has no semantic dimension binding", field)
		}
		// The dashboard filter contract has no dataset operand. An
		// unqualified field is safe only when it is already semantic.
		return "", fmt.Errorf("filter field %q uses dataset %q but has no semantic dimension binding", field, dataset)
	}
	return c.resolve(value.Field, "filter")
}

func filterExpression(value exploration.ExplorationFilterExpression) (*document.DashboardFilterExpression, document.DashboardFilterControl, *[]document.DashboardFilterOperator, error) {
	if value.Value == nil {
		return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("expression is required")
	}
	base := func(expr document.DashboardFilterExpressionVariant) *document.DashboardFilterExpression {
		return &document.DashboardFilterExpression{Value: expr}
	}
	switch expr := value.Value.(type) {
	case *exploration.UnfilteredExplorationFilterExpression:
		if expr == nil {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("unfiltered expression is nil")
		}
		return base(&document.UnfilteredDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "unfiltered"}, Type: "unfiltered"}), document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "text"}, Type: "text"}}, nil, nil
	case *exploration.NullCheckExplorationFilterExpression:
		if expr == nil {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("null check expression is nil")
		}
		operator, err := filterOperator(expr.Operator)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		return base(&document.NullCheckDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "nullCheck"}, Type: "nullCheck", Operator: operator}), document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "text"}, Type: "text"}}, &[]document.DashboardFilterOperator{operator}, nil
	case *exploration.SetExplorationFilterExpression:
		if expr == nil {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("set expression is nil")
		}
		operator, err := filterOperator(expr.Operator)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		values := make([]document.DashboardFilterValue, 0, len(expr.Values))
		for _, value := range expr.Values {
			converted, err := filterValue(value)
			if err != nil {
				return nil, document.DashboardFilterControl{}, nil, err
			}
			values = append(values, converted)
		}
		return base(&document.SetDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "set"}, Type: "set", Operator: operator, Values: values}), document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "multiSelect"}, Type: "multiSelect"}}, &[]document.DashboardFilterOperator{operator}, nil
	case *exploration.ComparisonExplorationFilterExpression:
		if expr == nil {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("comparison expression is nil")
		}
		operator, err := filterOperator(expr.Operator)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		value, err := filterValue(expr.Value)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		return base(&document.ComparisonDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "comparison"}, Type: "comparison", Operator: operator, Value: value}), document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "text"}, Type: "text"}}, &[]document.DashboardFilterOperator{operator}, nil
	case *exploration.RangeExplorationFilterExpression:
		if expr == nil {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("range expression is nil")
		}
		lower, err := filterBound(expr.Lower)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		upper, err := filterBound(expr.Upper)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		control, err := rangeFilterControl(lower, upper)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		return base(&document.RangeDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "range"}, Type: "range", Lower: lower, Upper: upper}), control, nil, nil
	case *exploration.RelativePeriodExplorationFilterExpression:
		if expr == nil {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("relative period expression is nil")
		}
		direction, unit, anchorKind, err := relativeParts(expr.Direction, expr.Unit, expr.Anchor)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		if expr.Count <= 0 {
			return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("relative period count must be positive")
		}
		anchorValue, err := filterValuePtr(expr.AnchorValue)
		if err != nil {
			return nil, document.DashboardFilterControl{}, nil, err
		}
		return base(&document.RelativePeriodDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "relativePeriod"}, Type: "relativePeriod", Direction: direction, Count: expr.Count, Unit: unit, IncludeCurrent: expr.IncludeCurrent, Anchor: anchorKind, AnchorValue: anchorValue}), document.DashboardFilterControl{Value: &document.RelativePeriodDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "relativePeriod"}, Type: "relativePeriod"}}, nil, nil
	default:
		return nil, document.DashboardFilterControl{}, nil, fmt.Errorf("unsupported expression %T", value.Value)
	}
}

func timeRangeExpression(value exploration.ExplorationTimeRange) (*document.DashboardFilterExpression, error) {
	switch rangeValue := value.Value.(type) {
	case *exploration.AbsoluteExplorationTimeRange:
		if rangeValue == nil {
			return nil, fmt.Errorf("absolute time range is nil")
		}
		if rangeValue.Lower == nil && rangeValue.Upper == nil {
			return nil, fmt.Errorf("absolute time range requires a lower or upper bound")
		}
		lower, err := timeBound(rangeValue.Lower)
		if err != nil {
			return nil, err
		}
		upper, err := timeBound(rangeValue.Upper)
		if err != nil {
			return nil, err
		}
		return &document.DashboardFilterExpression{Value: &document.RangeDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "range"}, Type: "range", Lower: lower, Upper: upper}}, nil
	case *exploration.RelativeExplorationTimeRange:
		if rangeValue == nil {
			return nil, fmt.Errorf("relative time range is nil")
		}
		direction, unit, anchor, err := relativeParts(rangeValue.Direction, rangeValue.Unit, rangeValue.Anchor)
		if err != nil {
			return nil, err
		}
		if rangeValue.Count <= 0 {
			return nil, fmt.Errorf("relative time range count must be positive")
		}
		anchorValue, err := temporalValuePtr(rangeValue.AnchorValue)
		if err != nil {
			return nil, err
		}
		return &document.DashboardFilterExpression{Value: &document.RelativePeriodDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "relativePeriod"}, Type: "relativePeriod", Direction: direction, Count: rangeValue.Count, Unit: unit, IncludeCurrent: rangeValue.IncludeCurrent, Anchor: anchor, AnchorValue: anchorValue}}, nil
	default:
		return nil, fmt.Errorf("unsupported time range %T", value.Value)
	}
}

func filterBound(value *exploration.ExplorationFilterBound) (*document.DashboardFilterBound, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := filterValue(value.Value)
	if err != nil {
		return nil, err
	}
	return &document.DashboardFilterBound{Value: converted, Inclusive: value.Inclusive}, nil
}

func timeBound(value *exploration.ExplorationTimeBound) (*document.DashboardFilterBound, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := temporalFilterValue(value.Value)
	if err != nil {
		return nil, err
	}
	return &document.DashboardFilterBound{Value: converted, Inclusive: value.Inclusive}, nil
}

func temporalFilterValue(value exploration.ExplorationTemporalValue) (document.DashboardFilterValue, error) {
	if value.Value == nil {
		return document.DashboardFilterValue{}, fmt.Errorf("temporal value is required")
	}
	switch v := value.Value.(type) {
	case *exploration.DateExplorationTemporalValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("date temporal value is nil")
		}
		return document.DashboardFilterValue{Value: &document.DateDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "date"}, Type: "date", Value: v.Value}}, nil
	case *exploration.TimestampExplorationTemporalValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("timestamp temporal value is nil")
		}
		return document.DashboardFilterValue{Value: &document.TimestampDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "timestamp"}, Type: "timestamp", Value: v.Value}}, nil
	default:
		return document.DashboardFilterValue{}, fmt.Errorf("unsupported temporal value %T", value.Value)
	}
}
func filterValuePtr(value *exploration.ExplorationFilterValue) (*document.DashboardFilterValue, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := filterValue(*value)
	return &converted, err
}
func temporalValuePtr(value *exploration.ExplorationTemporalValue) (*document.DashboardFilterValue, error) {
	if value == nil {
		return nil, nil
	}
	if value.Value == nil {
		return nil, fmt.Errorf("temporal anchor value is required")
	}
	switch v := value.Value.(type) {
	case *exploration.DateExplorationTemporalValue:
		if v == nil {
			return nil, fmt.Errorf("date temporal anchor is nil")
		}
		return &document.DashboardFilterValue{Value: &document.DateDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "date"}, Type: "date", Value: v.Value}}, nil
	case *exploration.TimestampExplorationTemporalValue:
		if v == nil {
			return nil, fmt.Errorf("timestamp temporal anchor is nil")
		}
		return &document.DashboardFilterValue{Value: &document.TimestampDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "timestamp"}, Type: "timestamp", Value: v.Value}}, nil
	default:
		return nil, fmt.Errorf("unsupported temporal anchor %T", value.Value)
	}
}
func filterValue(value exploration.ExplorationFilterValue) (document.DashboardFilterValue, error) {
	if value.Value == nil {
		return document.DashboardFilterValue{}, fmt.Errorf("filter value is required")
	}
	switch v := value.Value.(type) {
	case *exploration.BooleanExplorationFilterValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("boolean filter value is nil")
		}
		return document.DashboardFilterValue{Value: &document.BooleanDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "boolean"}, Type: "boolean", Value: v.Value}}, nil
	case *exploration.DateExplorationFilterValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("date filter value is nil")
		}
		return document.DashboardFilterValue{Value: &document.DateDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "date"}, Type: "date", Value: v.Value}}, nil
	case *exploration.DecimalExplorationFilterValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("decimal filter value is nil")
		}
		return document.DashboardFilterValue{Value: &document.DecimalDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "decimal"}, Type: "decimal", Value: v.Value}}, nil
	case *exploration.IntegerExplorationFilterValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("integer filter value is nil")
		}
		return document.DashboardFilterValue{Value: &document.IntegerDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "integer"}, Type: "integer", Value: v.Value}}, nil
	case *exploration.StringExplorationFilterValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("string filter value is nil")
		}
		return document.DashboardFilterValue{Value: &document.StringDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "string"}, Type: "string", Value: v.Value}}, nil
	case *exploration.TimestampExplorationFilterValue:
		if v == nil {
			return document.DashboardFilterValue{}, fmt.Errorf("timestamp filter value is nil")
		}
		return document.DashboardFilterValue{Value: &document.TimestampDashboardFilterValue{DashboardFilterValueBase: document.DashboardFilterValueBase{Type: "timestamp"}, Type: "timestamp", Value: v.Value}}, nil
	default:
		return document.DashboardFilterValue{}, fmt.Errorf("unsupported filter value %T", value.Value)
	}
}
func filterOperator(value string) (document.DashboardFilterOperator, error) {
	switch value {
	case "is_null":
		return document.DashboardFilterOperatorIsNull, nil
	case "is_not_null":
		return document.DashboardFilterOperatorIsNotNull, nil
	case "in":
		return document.DashboardFilterOperatorIn, nil
	case "not_in":
		return document.DashboardFilterOperatorNotIn, nil
	case "equals":
		return document.DashboardFilterOperatorEquals, nil
	case "not_equals":
		return document.DashboardFilterOperatorNotEquals, nil
	case "contains":
		return document.DashboardFilterOperatorContains, nil
	case "not_contains":
		return document.DashboardFilterOperatorNotContains, nil
	case "starts_with":
		return document.DashboardFilterOperatorStartsWith, nil
	case "ends_with":
		return document.DashboardFilterOperatorEndsWith, nil
	case "greater_than":
		return document.DashboardFilterOperatorGreaterThan, nil
	case "greater_than_or_equal":
		return document.DashboardFilterOperatorGreaterThanOrEqual, nil
	case "less_than":
		return document.DashboardFilterOperatorLessThan, nil
	case "less_than_or_equal":
		return document.DashboardFilterOperatorLessThanOrEqual, nil
	// Accept already-canonical values as well. This keeps the pure adapter
	// tolerant of callers that have normalized an ExplorationSpec first.
	case "isNull":
		return document.DashboardFilterOperatorIsNull, nil
	case "isNotNull":
		return document.DashboardFilterOperatorIsNotNull, nil
	case "notIn":
		return document.DashboardFilterOperatorNotIn, nil
	case "notEquals":
		return document.DashboardFilterOperatorNotEquals, nil
	case "notContains":
		return document.DashboardFilterOperatorNotContains, nil
	case "startsWith":
		return document.DashboardFilterOperatorStartsWith, nil
	case "endsWith":
		return document.DashboardFilterOperatorEndsWith, nil
	case "greaterThan":
		return document.DashboardFilterOperatorGreaterThan, nil
	case "greaterThanOrEqual":
		return document.DashboardFilterOperatorGreaterThanOrEqual, nil
	case "lessThan":
		return document.DashboardFilterOperatorLessThan, nil
	case "lessThanOrEqual":
		return document.DashboardFilterOperatorLessThanOrEqual, nil
	default:
		return "", fmt.Errorf("unsupported filter operator %q", value)
	}
}
func relativeParts(direction exploration.ExplorationRelativeDirection, unit exploration.ExplorationRelativeUnit, anchor exploration.ExplorationRelativeAnchor) (document.DashboardRelativeDirection, document.DashboardRelativeUnit, document.DashboardRelativeAnchor, error) {
	resultDirection := document.DashboardRelativeDirection(direction)
	switch resultDirection {
	case document.DashboardRelativeDirectionPrevious, document.DashboardRelativeDirectionCurrent, document.DashboardRelativeDirectionNext:
	default:
		return "", "", "", fmt.Errorf("unsupported relative direction %q", direction)
	}
	resultUnit := document.DashboardRelativeUnit(unit)
	switch resultUnit {
	case document.DashboardRelativeUnitMinute, document.DashboardRelativeUnitHour, document.DashboardRelativeUnitDay, document.DashboardRelativeUnitWeek, document.DashboardRelativeUnitMonth, document.DashboardRelativeUnitQuarter, document.DashboardRelativeUnitYear:
	default:
		return "", "", "", fmt.Errorf("unsupported relative unit %q", unit)
	}
	resultAnchor := document.DashboardRelativeAnchor(toDashboardAnchor(anchor))
	switch resultAnchor {
	case document.DashboardRelativeAnchorCurrentTime, document.DashboardRelativeAnchorFirstAvailable, document.DashboardRelativeAnchorLastAvailable, document.DashboardRelativeAnchorFixed:
	default:
		return "", "", "", fmt.Errorf("unsupported relative anchor %q", anchor)
	}
	return resultDirection, resultUnit, resultAnchor, nil
}

func toDashboardAnchor(value exploration.ExplorationRelativeAnchor) string {
	switch value {
	case exploration.ExplorationRelativeAnchorCurrentTime:
		return "currentTime"
	case exploration.ExplorationRelativeAnchorFirstAvailable:
		return "firstAvailable"
	case exploration.ExplorationRelativeAnchorLastAvailable:
		return "lastAvailable"
	case exploration.ExplorationRelativeAnchorFixed:
		return "fixed"
	default:
		return string(value)
	}
}

func rangeFilterControl(lower, upper *document.DashboardFilterBound) (document.DashboardFilterControl, error) {
	for _, bound := range []*document.DashboardFilterBound{lower, upper} {
		if bound == nil {
			continue
		}
		switch bound.Value.Value.(type) {
		case *document.DateDashboardFilterValue, *document.TimestampDashboardFilterValue:
			return document.DashboardFilterControl{Value: &document.DateRangeDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "dateRange"}, Type: "dateRange"}}, nil
		}
	}
	return document.DashboardFilterControl{Value: &document.NumericRangeDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "numericRange"}, Type: "numericRange"}}, nil
}
