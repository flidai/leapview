package explorationadapter

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/dashboard/document"
)

// ReverseDashboardFilter converts one authored dashboard filter using the
// same field, dataset, and expression rules as the full visual conversion.
// The authenticated handoff uses this identity to bind a live control to its
// exact authored default; matching on a field name alone is not sufficient
// when fixed and editable predicates share a field.
func ReverseDashboardFilter(value document.DashboardFilter, options ReverseOptions, outputs map[string]string) (exploration.ExplorationFilter, error) {
	return (reverseConverter{options: options}).dashboardFilter(value, outputs)
}

func (c reverseConverter) filters(values []document.DashboardFilter, dimensions []exploration.ExplorationDimensionRef, pivot *exploration.ExplorationPivotConfig, outputs map[string]string) ([]exploration.ExplorationFilter, *exploration.ExplorationTimeSelection, error) {
	result := make([]exploration.ExplorationFilter, 0, len(values))
	timeFilterIndex := -1
	var timeRange *exploration.ExplorationTimeRange
	var timeDimension *exploration.ExplorationDimensionRef

	allDimensions := append([]exploration.ExplorationDimensionRef(nil), dimensions...)
	if pivot != nil {
		allDimensions = append(allDimensions, pivot.Rows...)
		allDimensions = append(allDimensions, pivot.Columns...)
	}
	for index := range values {
		value := values[index]
		converted, err := c.dashboardFilter(value, outputs)
		fieldName := strings.TrimSpace(value.Dimension)
		if err != nil {
			return nil, nil, fmt.Errorf("filter %d: %w", index, err)
		}
		field := converted.Field

		// Forward conversion emits a date-range filter for ExplorationSpec.Time.
		// Recover it only when its dimension is an explicitly grain-decorated
		// selected dimension; ordinary range filters remain ordinary filters.
		if timeRange == nil {
			if candidate, ok := reverseTimeRange(*value.Default); ok {
				for dimIndex := range allDimensions {
					dim := allDimensions[dimIndex]
					if dim.Grain == nil || (dim.Field != field && outputForCanonical(dim, outputs) != fieldName) {
						continue
					}
					if c.options.TimeField != "" {
						wanted, wantedErr := c.field(c.options.TimeField)
						if wantedErr != nil || wanted != dim.Field {
							continue
						}
					}
					if timeDimension != nil {
						return nil, nil, fmt.Errorf("time range is ambiguous across selected dimensions")
					}
					copyDimension := dim
					timeDimension = &copyDimension
					timeFilterIndex = index
					timeRange = candidate
				}
			}
		}
		if timeFilterIndex == index {
			continue
		}
		var datasetID *string
		if dataset := strings.TrimSpace(c.options.FilterDatasets[value.ID]); dataset != "" {
			datasetID = &dataset
		} else if dataset := strings.TrimSpace(c.options.FilterDatasets[fieldName]); dataset != "" {
			datasetID = &dataset
		}
		converted.DatasetID = datasetID
		result = append(result, converted)
	}
	if timeDimension == nil && strings.TrimSpace(c.options.TimeField) != "" {
		wanted, err := c.field(c.options.TimeField)
		if err != nil {
			return nil, nil, fmt.Errorf("time field: %w", err)
		}
		for index := range allDimensions {
			dim := allDimensions[index]
			if dim.Field != wanted || dim.Grain == nil {
				continue
			}
			copyDimension := dim
			timeDimension = &copyDimension
			break
		}
		if timeDimension == nil {
			return nil, nil, fmt.Errorf("time field %q is not a selected grain-decorated dimension", c.options.TimeField)
		}
	}
	if timeDimension == nil {
		return result, nil, nil
	}
	selection := &exploration.ExplorationTimeSelection{Field: timeDimension.Field, Grain: *timeDimension.Grain, Alias: cloneString(timeDimension.Alias), Range: timeRange}
	return result, selection, nil
}

func (c reverseConverter) dashboardFilter(value document.DashboardFilter, outputs map[string]string) (exploration.ExplorationFilter, error) {
	fieldName := strings.TrimSpace(value.Dimension)
	if fieldName == "" {
		return exploration.ExplorationFilter{}, fmt.Errorf("dimension is required")
	}
	field, err := c.field(fieldName)
	if canonical, ok := outputs[fieldName]; ok {
		field = canonical
	}
	if err != nil {
		return exploration.ExplorationFilter{}, err
	}
	if value.Default == nil {
		return exploration.ExplorationFilter{}, fmt.Errorf("(%s) has no default expression", fieldName)
	}
	expression, err := reverseFilterExpression(*value.Default)
	if err != nil {
		return exploration.ExplorationFilter{}, fmt.Errorf("(%s): %w", fieldName, err)
	}
	var datasetID *string
	if dataset := strings.TrimSpace(c.options.FilterDatasets[value.ID]); dataset != "" {
		datasetID = &dataset
	} else if dataset := strings.TrimSpace(c.options.FilterDatasets[fieldName]); dataset != "" {
		datasetID = &dataset
	}
	return exploration.ExplorationFilter{Field: field, DatasetID: datasetID, Expression: expression}, nil
}

func outputForCanonical(value exploration.ExplorationDimensionRef, outputs map[string]string) string {
	for output, field := range outputs {
		if field == value.Field {
			return output
		}
	}
	return ""
}

func reverseFilterExpression(value document.DashboardFilterExpression) (exploration.ExplorationFilterExpression, error) {
	switch expression := value.Value.(type) {
	case *document.UnfilteredDashboardFilterExpression:
		if expression == nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("unfiltered expression is nil")
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.UnfilteredExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "unfiltered"}, Kind: "unfiltered"}}, nil
	case *document.NullCheckDashboardFilterExpression:
		if expression == nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("null-check expression is nil")
		}
		operator, err := reverseFilterOperator(expression.Operator)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, err
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.NullCheckExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "null_check"}, Kind: "null_check", Operator: operator}}, nil
	case *document.SetDashboardFilterExpression:
		if expression == nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("set expression is nil")
		}
		operator, err := reverseFilterOperator(expression.Operator)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, err
		}
		values := make([]exploration.ExplorationFilterValue, 0, len(expression.Values))
		for index, value := range expression.Values {
			converted, valueErr := reverseFilterValue(value)
			if valueErr != nil {
				return exploration.ExplorationFilterExpression{}, fmt.Errorf("set value %d: %w", index, valueErr)
			}
			values = append(values, converted)
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "set"}, Kind: "set", Operator: operator, Values: values}}, nil
	case *document.ComparisonDashboardFilterExpression:
		if expression == nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("comparison expression is nil")
		}
		operator, err := reverseFilterOperator(expression.Operator)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, err
		}
		converted, err := reverseFilterValue(expression.Value)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, err
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "comparison"}, Kind: "comparison", Operator: operator, Value: converted}}, nil
	case *document.RangeDashboardFilterExpression:
		if expression == nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("range expression is nil")
		}
		lower, err := reverseFilterBound(expression.Lower)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("lower bound: %w", err)
		}
		upper, err := reverseFilterBound(expression.Upper)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("upper bound: %w", err)
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.RangeExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "range"}, Kind: "range", Lower: lower, Upper: upper}}, nil
	case *document.RelativePeriodDashboardFilterExpression:
		if expression == nil {
			return exploration.ExplorationFilterExpression{}, fmt.Errorf("relative-period expression is nil")
		}
		anchor, err := reverseRelativeAnchor(expression.Anchor)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, err
		}
		anchorValue, err := reverseFilterValuePtr(expression.AnchorValue)
		if err != nil {
			return exploration.ExplorationFilterExpression{}, err
		}
		return exploration.ExplorationFilterExpression{Value: &exploration.RelativePeriodExplorationFilterExpression{ExplorationFilterExpressionBase: exploration.ExplorationFilterExpressionBase{Kind: "relative_period"}, Kind: "relative_period", Direction: exploration.ExplorationRelativeDirection(expression.Direction), Count: expression.Count, Unit: exploration.ExplorationRelativeUnit(expression.Unit), IncludeCurrent: expression.IncludeCurrent, Anchor: anchor, AnchorValue: anchorValue}}, nil
	default:
		return exploration.ExplorationFilterExpression{}, fmt.Errorf("unsupported filter expression %T", value.Value)
	}
}

func reverseFilterOperator(value document.DashboardFilterOperator) (string, error) {
	switch value {
	case document.DashboardFilterOperatorIsNull:
		return "is_null", nil
	case document.DashboardFilterOperatorIsNotNull:
		return "is_not_null", nil
	case document.DashboardFilterOperatorIn:
		return "in", nil
	case document.DashboardFilterOperatorNotIn:
		return "not_in", nil
	case document.DashboardFilterOperatorEquals:
		return "equals", nil
	case document.DashboardFilterOperatorNotEquals:
		return "not_equals", nil
	case document.DashboardFilterOperatorContains:
		return "contains", nil
	case document.DashboardFilterOperatorNotContains:
		return "not_contains", nil
	case document.DashboardFilterOperatorStartsWith:
		return "starts_with", nil
	case document.DashboardFilterOperatorEndsWith:
		return "ends_with", nil
	case document.DashboardFilterOperatorGreaterThan:
		return "greater_than", nil
	case document.DashboardFilterOperatorGreaterThanOrEqual:
		return "greater_than_or_equal", nil
	case document.DashboardFilterOperatorLessThan:
		return "less_than", nil
	case document.DashboardFilterOperatorLessThanOrEqual:
		return "less_than_or_equal", nil
	default:
		return "", fmt.Errorf("unsupported dashboard filter operator %q", value)
	}
}

func reverseFilterValue(value document.DashboardFilterValue) (exploration.ExplorationFilterValue, error) {
	switch item := value.Value.(type) {
	case *document.BooleanDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("boolean value is nil")
		}
		return exploration.ExplorationFilterValue{Value: &exploration.BooleanExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "boolean"}, Kind: "boolean", Value: item.Value}}, nil
	case *document.DateDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("date value is nil")
		}
		return exploration.ExplorationFilterValue{Value: &exploration.DateExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "date"}, Kind: "date", Value: item.Value}}, nil
	case *document.DecimalDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("decimal value is nil")
		}
		return exploration.ExplorationFilterValue{Value: &exploration.DecimalExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "decimal"}, Kind: "decimal", Value: item.Value}}, nil
	case *document.IntegerDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("integer value is nil")
		}
		return exploration.ExplorationFilterValue{Value: &exploration.IntegerExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "integer"}, Kind: "integer", Value: item.Value}}, nil
	case *document.StringDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("string value is nil")
		}
		return exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "string"}, Kind: "string", Value: item.Value}}, nil
	case *document.TimestampDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationFilterValue{}, fmt.Errorf("timestamp value is nil")
		}
		return exploration.ExplorationFilterValue{Value: &exploration.TimestampExplorationFilterValue{ExplorationFilterValueBase: exploration.ExplorationFilterValueBase{Kind: "timestamp"}, Kind: "timestamp", Value: item.Value}}, nil
	default:
		return exploration.ExplorationFilterValue{}, fmt.Errorf("unsupported dashboard filter value %T", value.Value)
	}
}

func reverseFilterValuePtr(value *document.DashboardFilterValue) (*exploration.ExplorationFilterValue, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := reverseFilterValue(*value)
	return &converted, err
}

func reverseFilterBound(value *document.DashboardFilterBound) (*exploration.ExplorationFilterBound, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := reverseFilterValue(value.Value)
	if err != nil {
		return nil, err
	}
	return &exploration.ExplorationFilterBound{Value: converted, Inclusive: value.Inclusive}, nil
}

func reverseTimeRange(value document.DashboardFilterExpression) (*exploration.ExplorationTimeRange, bool) {
	switch expression := value.Value.(type) {
	case *document.RangeDashboardFilterExpression:
		if expression == nil {
			return nil, false
		}
		lower, lowerOK := reverseTemporalBound(expression.Lower)
		upper, upperOK := reverseTemporalBound(expression.Upper)
		if !lowerOK && !upperOK {
			return nil, false
		}
		return &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{ExplorationTimeRangeBase: exploration.ExplorationTimeRangeBase{Kind: "absolute"}, Kind: "absolute", Lower: lower, Upper: upper}}, true
	case *document.RelativePeriodDashboardFilterExpression:
		if expression == nil {
			return nil, false
		}
		anchor, err := reverseRelativeAnchor(expression.Anchor)
		if err != nil {
			return nil, false
		}
		anchorValue, err := reverseTemporalValuePtr(expression.AnchorValue)
		if err != nil {
			return nil, false
		}
		return &exploration.ExplorationTimeRange{Value: &exploration.RelativeExplorationTimeRange{ExplorationTimeRangeBase: exploration.ExplorationTimeRangeBase{Kind: "relative"}, Kind: "relative", Direction: exploration.ExplorationRelativeDirection(expression.Direction), Count: expression.Count, Unit: exploration.ExplorationRelativeUnit(expression.Unit), IncludeCurrent: expression.IncludeCurrent, Anchor: anchor, AnchorValue: anchorValue}}, true
	default:
		return nil, false
	}
}

func reverseTemporalBound(value *document.DashboardFilterBound) (*exploration.ExplorationTimeBound, bool) {
	if value == nil {
		return nil, false
	}
	converted, ok := reverseTemporalValue(value.Value)
	if !ok {
		return nil, false
	}
	return &exploration.ExplorationTimeBound{Value: converted, Inclusive: value.Inclusive}, true
}

func reverseTemporalValue(value document.DashboardFilterValue) (exploration.ExplorationTemporalValue, bool) {
	switch item := value.Value.(type) {
	case *document.DateDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationTemporalValue{}, false
		}
		return exploration.ExplorationTemporalValue{Value: &exploration.DateExplorationTemporalValue{ExplorationTemporalValueBase: exploration.ExplorationTemporalValueBase{Kind: "date"}, Kind: "date", Value: item.Value}}, true
	case *document.TimestampDashboardFilterValue:
		if item == nil {
			return exploration.ExplorationTemporalValue{}, false
		}
		return exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{ExplorationTemporalValueBase: exploration.ExplorationTemporalValueBase{Kind: "timestamp"}, Kind: "timestamp", Value: item.Value}}, true
	default:
		return exploration.ExplorationTemporalValue{}, false
	}
}

func reverseTemporalValuePtr(value *document.DashboardFilterValue) (*exploration.ExplorationTemporalValue, error) {
	if value == nil {
		return nil, nil
	}
	converted, ok := reverseTemporalValue(*value)
	if !ok {
		return nil, fmt.Errorf("temporal anchor must be date or timestamp")
	}
	return &converted, nil
}

func reverseRelativeAnchor(value document.DashboardRelativeAnchor) (exploration.ExplorationRelativeAnchor, error) {
	switch value {
	case document.DashboardRelativeAnchorCurrentTime:
		return exploration.ExplorationRelativeAnchorCurrentTime, nil
	case document.DashboardRelativeAnchorFirstAvailable:
		return exploration.ExplorationRelativeAnchorFirstAvailable, nil
	case document.DashboardRelativeAnchorLastAvailable:
		return exploration.ExplorationRelativeAnchorLastAvailable, nil
	case document.DashboardRelativeAnchorFixed:
		return exploration.ExplorationRelativeAnchorFixed, nil
	default:
		return "", fmt.Errorf("unsupported dashboard relative anchor %q", value)
	}
}
