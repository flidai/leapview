package exploration

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/cockroachdb/apd/v3"
)

func validateFilterExpression(expression *ExplorationFilterExpression, expectedType string) error {
	if expression == nil {
		return errors.New("filter expression is required")
	}
	kind, err := expression.Kind()
	if err != nil {
		return err
	}
	switch variant := expression.Value.(type) {
	case *UnfilteredExplorationFilterExpression:
		if variant.Kind != "unfiltered" {
			return fmt.Errorf("filter expression kind %q does not match variant", kind)
		}
	case *NullCheckExplorationFilterExpression:
		if variant.Kind != "null_check" {
			return fmt.Errorf("filter expression kind %q does not match variant", kind)
		}
		if string(variant.Operator) != "is_null" && string(variant.Operator) != "is_not_null" {
			return fmt.Errorf("invalid null-check operator %q", variant.Operator)
		}
	case *SetExplorationFilterExpression:
		if variant.Kind != "set" {
			return fmt.Errorf("filter expression kind %q does not match variant", kind)
		}
		if string(variant.Operator) != "in" && string(variant.Operator) != "not_in" {
			return fmt.Errorf("invalid set operator %q", variant.Operator)
		}
		if len(variant.Values) == 0 || len(variant.Values) > 100 {
			return errors.New("set filter requires 1..100 values")
		}
		var valueKind string
		for index := range variant.Values {
			if err := validateFilterValue(&variant.Values[index], expectedType); err != nil {
				return fmt.Errorf("invalid set value %d: %w", index, err)
			}
			current, _ := variant.Values[index].Kind()
			if valueKind == "" {
				valueKind = current
			} else if valueKind != current {
				return errors.New("set filter values must have one type")
			}
		}
	case *ComparisonExplorationFilterExpression:
		if variant.Kind != "comparison" {
			return fmt.Errorf("filter expression kind %q does not match variant", kind)
		}
		if !isComparisonOperator(string(variant.Operator)) {
			return fmt.Errorf("invalid comparison operator %q", variant.Operator)
		}
		if err := validateComparisonOperator(string(variant.Operator), expectedType); err != nil {
			return err
		}
		if err := validateFilterValue(&variant.Value, expectedType); err != nil {
			return fmt.Errorf("invalid comparison value: %w", err)
		}
	case *RangeExplorationFilterExpression:
		if variant.Kind != "range" {
			return fmt.Errorf("filter expression kind %q does not match variant", kind)
		}
		if expectedType != "" && !isOrderedExplorationType(expectedType) {
			return fmt.Errorf("range filter is incompatible with field type %q", expectedType)
		}
		if variant.Lower == nil && variant.Upper == nil {
			return errors.New("range filter requires a lower or upper bound")
		}
		if variant.Lower != nil {
			if err := validateFilterValue(&variant.Lower.Value, expectedType); err != nil {
				return fmt.Errorf("invalid lower bound: %w", err)
			}
		}
		if variant.Upper != nil {
			if err := validateFilterValue(&variant.Upper.Value, expectedType); err != nil {
				return fmt.Errorf("invalid upper bound: %w", err)
			}
		}
		if variant.Lower != nil && variant.Upper != nil {
			if err := validateFilterBoundOrder(variant.Lower.Value, variant.Upper.Value); err != nil {
				return err
			}
		}
	case *RelativePeriodExplorationFilterExpression:
		if variant.Kind != "relative_period" {
			return fmt.Errorf("filter expression kind %q does not match variant", kind)
		}
		if err := validateRelativePeriod(variant.Direction, variant.Count, variant.Unit, variant.Anchor, variant.AnchorValue != nil); err != nil {
			return err
		}
		if expectedType != "" {
			if !isTemporalExplorationType(expectedType) {
				return fmt.Errorf("relative period filter is incompatible with field type %q", expectedType)
			}
			if err := validateRelativeUnitForType(variant.Unit, expectedType); err != nil {
				return err
			}
		}
		if variant.AnchorValue != nil {
			if err := validateFilterValue(variant.AnchorValue, expectedType); err != nil {
				return fmt.Errorf("invalid relative anchorValue: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported filter expression variant %T", expression.Value)
	}
	return nil
}

func isComparisonOperator(operator string) bool {
	switch operator {
	case "equals", "not_equals", "contains", "not_contains", "starts_with", "ends_with",
		"greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
		return true
	default:
		return false
	}
}

func validateComparisonOperator(operator, expectedType string) error {
	if expectedType == "" {
		return nil
	}
	if operator == "equals" || operator == "not_equals" {
		return nil
	}
	if operator == "contains" || operator == "not_contains" || operator == "starts_with" || operator == "ends_with" {
		if !isTextExplorationType(expectedType) {
			return fmt.Errorf("operator %q is incompatible with field type %q", operator, expectedType)
		}
		return nil
	}
	if !isOrderedExplorationType(expectedType) {
		return fmt.Errorf("operator %q is incompatible with field type %q", operator, expectedType)
	}
	return nil
}

func isTextExplorationType(valueType string) bool {
	switch valueType {
	case "string", "text", "varchar", "char":
		return true
	default:
		return false
	}
}

func isOrderedExplorationType(valueType string) bool {
	switch valueType {
	case "integer", "int", "bigint", "decimal", "float", "number", "numeric", "double", "real", "date", "datetime", "datetimetz", "timestamp":
		return true
	default:
		return false
	}
}

func validateRelativeUnitForType(unit ExplorationRelativeUnit, expectedType string) error {
	if expectedType != "date" {
		return nil
	}
	if unit == ExplorationRelativeUnitMinute || unit == ExplorationRelativeUnitHour {
		return fmt.Errorf("relative unit %q is incompatible with date-only field", unit)
	}
	return nil
}

func validateFilterValue(value *ExplorationFilterValue, expectedType string) error {
	kind, err := value.Kind()
	if err != nil {
		return err
	}
	if !filterKindMatches(expectedType, kind) {
		return fmt.Errorf("value kind %q is incompatible with field type %q", kind, expectedType)
	}
	switch variant := value.Value.(type) {
	case *IntegerExplorationFilterValue:
		if variant.Kind != "integer" {
			return fmt.Errorf("filter value kind %q does not match variant", kind)
		}
		literal := string(variant.Value)
		if !explorationIntegerPattern.MatchString(literal) {
			return errors.New("integer value is required")
		}
		if _, err := strconv.ParseInt(literal, 10, 64); err != nil {
			return fmt.Errorf("integer value %q is not a signed 64-bit integer: %w", literal, err)
		}
	case *DecimalExplorationFilterValue:
		if variant.Kind != "decimal" {
			return fmt.Errorf("filter value kind %q does not match variant", kind)
		}
		literal := string(variant.Value)
		if !explorationDecimalPattern.MatchString(literal) {
			return errors.New("decimal value is required")
		}
		decimal, _, err := apd.BaseContext.NewFromString(literal)
		if err != nil {
			return fmt.Errorf("decimal value %q is not finite: %v", literal, err)
		}
		if decimal == nil || decimal.Form != apd.Finite {
			return fmt.Errorf("decimal value %q is not finite", literal)
		}
	case *DateExplorationFilterValue:
		if variant.Kind != "date" {
			return fmt.Errorf("filter value kind %q does not match variant", kind)
		}
		if err := validateExplorationDate(variant.Value); err != nil {
			return err
		}
	case *TimestampExplorationFilterValue:
		if variant.Kind != "timestamp" {
			return fmt.Errorf("filter value kind %q does not match variant", kind)
		}
		if err := validateExplorationTimestamp(variant.Value); err != nil {
			return err
		}
	case *StringExplorationFilterValue:
		if variant.Kind != "string" {
			return fmt.Errorf("filter value kind %q does not match variant", kind)
		}
	case *BooleanExplorationFilterValue:
		if variant.Kind != "boolean" {
			return fmt.Errorf("filter value kind %q does not match variant", kind)
		}
	}
	return nil
}

func validateFilterBoundOrder(lower, upper ExplorationFilterValue) error {
	lowerKind, err := lower.Kind()
	if err != nil {
		return err
	}
	upperKind, err := upper.Kind()
	if err != nil {
		return err
	}
	if lowerKind != upperKind {
		return fmt.Errorf("range bounds must have compatible value kinds, got %q and %q", lowerKind, upperKind)
	}
	comparison, err := compareFilterValues(lower, upper)
	if err != nil {
		return err
	}
	if comparison > 0 {
		return errors.New("range lower bound must not exceed upper bound")
	}
	return nil
}

func compareFilterValues(lower, upper ExplorationFilterValue) (int, error) {
	lowerVariant := lower.Value
	upperVariant := upper.Value
	switch typedLower := lowerVariant.(type) {
	case *IntegerExplorationFilterValue:
		typedUpper, ok := upperVariant.(*IntegerExplorationFilterValue)
		if !ok {
			return 0, errors.New("range bounds have incompatible value kinds")
		}
		left, err := strconv.ParseInt(string(typedLower.Value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid lower integer bound: %w", err)
		}
		right, err := strconv.ParseInt(string(typedUpper.Value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid upper integer bound: %w", err)
		}
		switch {
		case left < right:
			return -1, nil
		case left > right:
			return 1, nil
		default:
			return 0, nil
		}
	case *DecimalExplorationFilterValue:
		typedUpper, ok := upperVariant.(*DecimalExplorationFilterValue)
		if !ok {
			return 0, errors.New("range bounds have incompatible value kinds")
		}
		left, _, err := apd.BaseContext.NewFromString(string(typedLower.Value))
		if err != nil {
			return 0, fmt.Errorf("invalid lower decimal bound: %w", err)
		}
		right, _, err := apd.BaseContext.NewFromString(string(typedUpper.Value))
		if err != nil {
			return 0, fmt.Errorf("invalid upper decimal bound: %w", err)
		}
		return left.Cmp(right), nil
	case *DateExplorationFilterValue:
		typedUpper, ok := upperVariant.(*DateExplorationFilterValue)
		if !ok {
			return 0, errors.New("range bounds have incompatible value kinds")
		}
		if typedLower.Value < typedUpper.Value {
			return -1, nil
		}
		if typedLower.Value > typedUpper.Value {
			return 1, nil
		}
		return 0, nil
	case *TimestampExplorationFilterValue:
		typedUpper, ok := upperVariant.(*TimestampExplorationFilterValue)
		if !ok {
			return 0, errors.New("range bounds have incompatible value kinds")
		}
		left, err := time.Parse(time.RFC3339Nano, typedLower.Value)
		if err != nil {
			return 0, err
		}
		right, err := time.Parse(time.RFC3339Nano, typedUpper.Value)
		if err != nil {
			return 0, err
		}
		if left.Before(right) {
			return -1, nil
		}
		if left.After(right) {
			return 1, nil
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("range value kind %T is not orderable", lowerVariant)
	}
}

func filterKindMatches(expectedType, kind string) bool {
	switch expectedType {
	case "string", "text", "varchar", "char":
		return kind == "string"
	case "integer", "int", "bigint":
		return kind == "integer"
	case "decimal", "float", "number", "numeric", "double", "real":
		return kind == "decimal" || kind == "integer"
	case "boolean", "bool":
		return kind == "boolean"
	case "date":
		return kind == "date"
	case "datetime", "datetimetz", "timestamp":
		return kind == "timestamp"
	case "time":
		return kind == "string"
	case "opaque":
		return false
	default:
		return true
	}
}

func validateExplorationDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("date value %q is not a Gregorian YYYY-MM-DD date", value)
	}
	return nil
}

func validateExplorationTimestamp(value string) error {
	if value == "" {
		return errors.New("timestamp value is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("timestamp value %q is not RFC3339: %w", value, err)
	}
	// ParseFloat is intentionally not used here: RFC3339Nano accepts only the
	// timestamp grammar and preserves the authored offset representation.
	return nil
}

func validateRelativePeriod(direction ExplorationRelativeDirection, count int32, unit ExplorationRelativeUnit, anchor ExplorationRelativeAnchor, hasAnchorValue bool) error {
	if direction != ExplorationRelativeDirectionPrevious && direction != ExplorationRelativeDirectionCurrent && direction != ExplorationRelativeDirectionNext {
		return fmt.Errorf("invalid relative direction %q", direction)
	}
	if count < 1 {
		return errors.New("relative count must be at least 1")
	}
	switch unit {
	case ExplorationRelativeUnitMinute, ExplorationRelativeUnitHour,
		ExplorationRelativeUnitDay, ExplorationRelativeUnitWeek,
		ExplorationRelativeUnitMonth, ExplorationRelativeUnitQuarter,
		ExplorationRelativeUnitYear:
	default:
		return fmt.Errorf("invalid relative unit %q", unit)
	}
	if anchor != ExplorationRelativeAnchorCurrentTime && anchor != ExplorationRelativeAnchorFirstAvailable && anchor != ExplorationRelativeAnchorLastAvailable && anchor != ExplorationRelativeAnchorFixed {
		return fmt.Errorf("invalid relative anchor %q", anchor)
	}
	if (anchor == ExplorationRelativeAnchorFixed) != hasAnchorValue {
		return errors.New("fixed relative ranges require anchorValue and other anchors forbid it")
	}
	return nil
}

func validateTemporalValue(value *ExplorationTemporalValue, expectedType string) error {
	kind, err := value.Kind()
	if err != nil {
		return err
	}
	if expectedType == "date" && kind != "date" {
		return fmt.Errorf("temporal value kind %q is incompatible with date field", kind)
	}
	if (expectedType == "datetime" || expectedType == "datetimetz" || expectedType == "timestamp") && kind != "timestamp" {
		return fmt.Errorf("temporal value kind %q is incompatible with timestamp field", kind)
	}
	switch variant := value.Value.(type) {
	case *DateExplorationTemporalValue:
		if variant.Kind != "date" {
			return fmt.Errorf("temporal value kind %q does not match variant", kind)
		}
		return validateExplorationDate(variant.Value)
	case *TimestampExplorationTemporalValue:
		if variant.Kind != "timestamp" {
			return fmt.Errorf("temporal value kind %q does not match variant", kind)
		}
		return validateExplorationTimestamp(variant.Value)
	}
	return nil
}

func validateTimeRange(value *ExplorationTimeRange, expectedType string) error {
	if value == nil {
		return nil
	}
	kind, err := value.Kind()
	if err != nil {
		return err
	}
	switch variant := value.Value.(type) {
	case *AbsoluteExplorationTimeRange:
		if variant.Kind != "absolute" {
			return fmt.Errorf("time range kind %q does not match variant", kind)
		}
		if variant.Lower == nil && variant.Upper == nil {
			return errors.New("absolute range requires a lower or upper bound")
		}
		if variant.Lower != nil {
			if err := validateTemporalValue(&variant.Lower.Value, expectedType); err != nil {
				return fmt.Errorf("invalid lower bound: %w", err)
			}
		}
		if variant.Upper != nil {
			if err := validateTemporalValue(&variant.Upper.Value, expectedType); err != nil {
				return fmt.Errorf("invalid upper bound: %w", err)
			}
		}
		if variant.Lower != nil && variant.Upper != nil {
			if err := validateTemporalBoundOrder(*variant.Lower, *variant.Upper); err != nil {
				return err
			}
		}
	case *RelativeExplorationTimeRange:
		if variant.Kind != "relative" {
			return fmt.Errorf("time range kind %q does not match variant", kind)
		}
		if err := validateRelativePeriod(variant.Direction, variant.Count, variant.Unit, variant.Anchor, variant.AnchorValue != nil); err != nil {
			return err
		}
		if expectedType != "" {
			if !isTemporalExplorationType(expectedType) {
				return fmt.Errorf("relative time range is incompatible with field type %q", expectedType)
			}
			if err := validateRelativeUnitForType(variant.Unit, expectedType); err != nil {
				return err
			}
		}
		if variant.AnchorValue != nil {
			if err := validateTemporalValue(variant.AnchorValue, expectedType); err != nil {
				return fmt.Errorf("invalid relative anchorValue: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported time range variant %q", kind)
	}
	return nil
}

func validateTemporalBoundOrder(lower, upper ExplorationTimeBound) error {
	lowerKind, err := lower.Value.Kind()
	if err != nil {
		return err
	}
	upperKind, err := upper.Value.Kind()
	if err != nil {
		return err
	}
	if lowerKind != upperKind {
		return fmt.Errorf("time range bounds must have compatible value kinds, got %q and %q", lowerKind, upperKind)
	}
	lowerValue, ok := temporalValueString(lower.Value)
	if !ok {
		return errors.New("unsupported temporal range bound")
	}
	upperValue, ok := temporalValueString(upper.Value)
	if !ok {
		return errors.New("unsupported temporal range bound")
	}
	if lowerKind == "date" {
		if lowerValue > upperValue {
			return errors.New("time range lower bound must not exceed upper bound")
		}
		return nil
	}
	left, err := time.Parse(time.RFC3339Nano, lowerValue)
	if err != nil {
		return err
	}
	right, err := time.Parse(time.RFC3339Nano, upperValue)
	if err != nil {
		return err
	}
	if left.After(right) {
		return errors.New("time range lower bound must not exceed upper bound")
	}
	return nil
}

func temporalValueString(value ExplorationTemporalValue) (string, bool) {
	switch variant := value.Value.(type) {
	case *DateExplorationTemporalValue:
		return variant.Value, true
	case *TimestampExplorationTemporalValue:
		return variant.Value, true
	default:
		return "", false
	}
}
