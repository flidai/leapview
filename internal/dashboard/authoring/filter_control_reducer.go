package authoring

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func canonicalBuilderFilterControl(controlType, dataset string, existing *document.DashboardFilterControl) (document.DashboardFilterControl, error) {
	controlType, dataset = strings.TrimSpace(controlType), strings.TrimSpace(dataset)
	if existing != nil {
		if existingType, err := existing.Type(); err == nil {
			if existingType == controlType {
				return *existing, nil
			}
			if (existingType == "singleSelect" || existingType == "multiSelect") && (controlType == "singleSelect" || controlType == "multiSelect") {
				return document.DashboardFilterControl{Value: builderSelectControl(controlType, dataset, existing)}, nil
			}
		}
	}
	switch controlType {
	case "singleSelect":
		return document.DashboardFilterControl{Value: builderSelectControl(controlType, dataset, nil)}, nil
	case "multiSelect":
		return document.DashboardFilterControl{Value: builderSelectControl(controlType, dataset, nil)}, nil
	case "text":
		return document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{Type: controlType}}, nil
	case "numericRange":
		return document.DashboardFilterControl{Value: &document.NumericRangeDashboardFilterControl{Type: controlType}}, nil
	case "dateRange":
		return document.DashboardFilterControl{Value: &document.DateRangeDashboardFilterControl{Type: controlType}}, nil
	case "relativePeriod":
		return document.DashboardFilterControl{Value: &document.RelativePeriodDashboardFilterControl{Type: controlType}}, nil
	default:
		return document.DashboardFilterControl{}, fmt.Errorf("%w: unsupported filter control %q", ErrInvalidPayload, controlType)
	}
}

func builderSelectControl(controlType, dataset string, existing *document.DashboardFilterControl) document.DashboardFilterControlVariant {
	options := builderSelectOptions(dataset, existing)
	switch controlType {
	case "singleSelect":
		return &document.SingleSelectDashboardFilterControl{Type: controlType, Options: options}
	case "multiSelect":
		return &document.MultiSelectDashboardFilterControl{Type: controlType, Options: options}
	default:
		return &document.TextDashboardFilterControl{Type: controlType}
	}
}

func builderSelectOptions(dataset string, existing *document.DashboardFilterControl) *document.DashboardFilterOptions {
	var options *document.DashboardFilterOptions
	if existing != nil {
		switch value := existing.Value.(type) {
		case *document.SingleSelectDashboardFilterControl:
			options = value.Options
		case *document.MultiSelectDashboardFilterControl:
			options = value.Options
		}
	}
	if options == nil {
		return &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: dataset}}
	}
	switch value := options.Value.(type) {
	case *document.DistinctDashboardFilterOptions:
		copied := *value
		return &document.DashboardFilterOptions{Value: &copied}
	case *document.StaticDashboardFilterOptions:
		copied := *value
		copied.Values = append([]document.DashboardFilterOption(nil), value.Values...)
		return &document.DashboardFilterOptions{Value: &copied}
	default:
		return &document.DashboardFilterOptions{Value: &document.DistinctDashboardFilterOptions{Type: "distinct", Dataset: dataset}}
	}
}

// migrateCanonicalFilterState drops code-owned filter state that the new
// control cannot represent. Keeping an old set operator/default on a text or
// range control makes the next strict compile fail even though the builder
// only changed the presentation control.
func migrateCanonicalFilterState(filter *document.DashboardFilter, previousControlType, nextControlType string, nextControl document.DashboardFilterControl) {
	if filter == nil || previousControlType == "" || previousControlType == nextControlType {
		return
	}
	if filter.Operators != nil && !canonicalFilterOperatorsCompatible(*filter.Operators, nextControlType) {
		filter.Operators = nil
	}
	if filter.Default != nil {
		if nextControlType == "singleSelect" {
			if set, ok := filter.Default.Value.(*document.SetDashboardFilterExpression); ok && len(set.Values) > 1 {
				copied := *set
				copied.Values = append([]document.DashboardFilterValue(nil), set.Values[:1]...)
				filter.Default = &document.DashboardFilterExpression{Value: &copied}
			}
		}
		if !canonicalFilterDefaultCompatible(*filter.Default, nextControlType, nextControl) {
			filter.Default = nil
		}
	}
}

func canonicalFilterOperatorsCompatible(operators []document.DashboardFilterOperator, controlType string) bool {
	if len(operators) == 0 {
		return false
	}
	allowed := map[document.DashboardFilterOperator]struct{}{}
	switch controlType {
	case "singleSelect", "multiSelect":
		allowed[document.DashboardFilterOperatorIn] = struct{}{}
		allowed[document.DashboardFilterOperatorNotIn] = struct{}{}
	case "text":
		for _, operator := range []document.DashboardFilterOperator{
			document.DashboardFilterOperatorEquals, document.DashboardFilterOperatorNotEquals,
			document.DashboardFilterOperatorContains, document.DashboardFilterOperatorNotContains,
			document.DashboardFilterOperatorStartsWith, document.DashboardFilterOperatorEndsWith,
		} {
			allowed[operator] = struct{}{}
		}
	default:
		return false
	}
	for _, operator := range operators {
		if _, ok := allowed[operator]; !ok {
			return false
		}
	}
	return true
}

func canonicalFilterDefaultCompatible(expression document.DashboardFilterExpression, controlType string, control document.DashboardFilterControl) bool {
	switch value := expression.Value.(type) {
	case *document.UnfilteredDashboardFilterExpression:
		return true
	case *document.SetDashboardFilterExpression:
		if controlType != "singleSelect" && controlType != "multiSelect" {
			return false
		}
		return value.Operator == document.DashboardFilterOperatorIn || value.Operator == document.DashboardFilterOperatorNotIn
	case *document.ComparisonDashboardFilterExpression:
		if controlType != "text" {
			return false
		}
		return canonicalFilterOperatorsCompatible([]document.DashboardFilterOperator{value.Operator}, "text")
	case *document.RangeDashboardFilterExpression:
		return false
	case *document.RelativePeriodDashboardFilterExpression:
		return false
	case *document.NullCheckDashboardFilterExpression:
		if controlType != "singleSelect" && controlType != "multiSelect" {
			return false
		}
		return builderSelectOptionsIncludeNull(control)
	default:
		return false
	}
}

func builderSelectOptionsIncludeNull(control document.DashboardFilterControl) bool {
	var options *document.DashboardFilterOptions
	switch value := control.Value.(type) {
	case *document.SingleSelectDashboardFilterControl:
		options = value.Options
	case *document.MultiSelectDashboardFilterControl:
		options = value.Options
	}
	if options == nil {
		return false
	}
	value, ok := options.Value.(*document.DistinctDashboardFilterOptions)
	return ok && value.IncludeNull != nil && *value.IncludeNull
}
