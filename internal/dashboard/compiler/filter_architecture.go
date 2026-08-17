package compiler

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
)

func validateFilterArchitecture(d *dashboardauthoring.Dashboard, model *semanticmodel.Model) error {
	if len(d.FilterDefinitions) == 0 {
		return nil
	}
	for id, definition := range d.FilterDefinitions {
		kind, err := filterValueKind(model, definition.Field)
		if err != nil {
			return fmt.Errorf("filter definition %q: %w", id, err)
		}
		definition.ValueKind = kind
		definition.Time = filterTimeSemantics(model, definition.Field)
		if err := validatePredicateTypes(definition); err != nil {
			return fmt.Errorf("filter definition %q: %w", id, err)
		}
		for index, option := range definition.Options.Values {
			value, err := dashboardfilter.Canonicalize(dashboardfilter.Expression{
				Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: []dashboardfilter.Value{option.Value},
			}, kind)
			if err != nil {
				return fmt.Errorf("filter definition %q option %d: %w", id, index, err)
			}
			definition.Options.Values[index].Value = value.Values[0]
		}
		d.FilterDefinitions[id] = definition
	}

	reportBindings := make(map[string]dashboardfilter.Binding, len(d.FilterBindings))
	for id, binding := range d.FilterBindings {
		compiled, err := compileFilterBinding(d, model, id, dashboardfilter.ScopeReport, "", binding)
		if err != nil {
			return err
		}
		reportBindings[id] = compiled
	}
	d.FilterBindings = reportBindings

	for pageIndex := range d.Pages {
		page := d.Pages[pageIndex]
		bindings := make(map[string]dashboardfilter.Binding, len(page.FilterBindings))
		for id, binding := range page.FilterBindings {
			compiled, err := compileFilterBinding(d, model, id, dashboardfilter.ScopePage, page.ID, binding)
			if err != nil {
				return err
			}
			bindings[id] = compiled
		}
		page.FilterBindings = bindings
		if err := validateSlicerPresentations(d, &page); err != nil {
			return err
		}
		d.Pages[pageIndex] = page
	}
	compileOptionDependencies(d)
	return nil
}

func filterTimeSemantics(model *semanticmodel.Model, field string) dashboardfilter.TimeSemantics {
	if dimension, ok := model.Dimensions[field]; ok {
		return dashboardfilter.TimeSemantics{
			Timezone: dimension.Timezone, Calendar: dimension.Calendar, WeekStart: dimension.WeekStart,
		}
	}
	return dashboardfilter.TimeSemantics{Timezone: "UTC", Calendar: "gregorian", WeekStart: "monday"}
}

func filterValueKind(model *semanticmodel.Model, field string) (dashboardfilter.ValueKind, error) {
	if dimension, ok := model.Dimensions[field]; ok {
		switch dimension.Type {
		case "string":
			return dashboardfilter.ValueString, nil
		case "boolean":
			return dashboardfilter.ValueBoolean, nil
		case "date":
			return dashboardfilter.ValueDate, nil
		case "timestamp":
			return dashboardfilter.ValueTimestamp, nil
		case "number":
			kind := dashboardfilter.ValueInteger
			for _, binding := range dimension.Bindings {
				physical, err := model.ResolveDimension(binding.Field)
				if err != nil {
					return "", err
				}
				if numericFilterValueKind(physical.Type) == dashboardfilter.ValueDecimal {
					kind = dashboardfilter.ValueDecimal
				}
			}
			return kind, nil
		}
	}
	physical, err := model.ResolveDimension(field)
	if err != nil {
		return "", fmt.Errorf("references unknown dimension %q", field)
	}
	switch canonicalDimensionTypeForFilter(physical.Type) {
	case "string":
		return dashboardfilter.ValueString, nil
	case "boolean":
		return dashboardfilter.ValueBoolean, nil
	case "date":
		return dashboardfilter.ValueDate, nil
	case "timestamp":
		return dashboardfilter.ValueTimestamp, nil
	case "number":
		return numericFilterValueKind(physical.Type), nil
	default:
		return "", fmt.Errorf("dimension %q has unsupported filter type %q", field, physical.Type)
	}
}

func canonicalDimensionTypeForFilter(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case value == "string" || strings.Contains(value, "char") || strings.Contains(value, "text") || value == "uuid":
		return "string"
	case value == "number" || strings.Contains(value, "int") || strings.Contains(value, "decimal") || strings.Contains(value, "numeric") || strings.Contains(value, "double") || strings.Contains(value, "float") || strings.Contains(value, "real"):
		return "number"
	case value == "boolean" || strings.Contains(value, "bool"):
		return "boolean"
	case value == "date":
		return "date"
	case strings.Contains(value, "timestamp") || strings.Contains(value, "datetime"):
		return "timestamp"
	default:
		return ""
	}
}

func numericFilterValueKind(value string) dashboardfilter.ValueKind {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.Contains(value, "int") {
		return dashboardfilter.ValueInteger
	}
	return dashboardfilter.ValueDecimal
}

func validatePredicateTypes(definition dashboardfilter.Definition) error {
	for _, predicate := range definition.Predicates {
		switch predicate.Kind {
		case dashboardfilter.ExpressionRelativePeriod:
			if definition.ValueKind != dashboardfilter.ValueDate && definition.ValueKind != dashboardfilter.ValueTimestamp {
				return fmt.Errorf("relative_period requires a date or timestamp field")
			}
		case dashboardfilter.ExpressionRange:
			if definition.ValueKind == dashboardfilter.ValueBoolean {
				return fmt.Errorf("range is not valid for boolean fields")
			}
		case dashboardfilter.ExpressionComparison:
			for _, operator := range predicate.Operators {
				if (operator == dashboardfilter.OperatorContains || operator == dashboardfilter.OperatorNotContains ||
					operator == dashboardfilter.OperatorStartsWith || operator == dashboardfilter.OperatorEndsWith) &&
					definition.ValueKind != dashboardfilter.ValueString {
					return fmt.Errorf("operator %q requires a string field", operator)
				}
			}
		}
	}
	return nil
}

func compileFilterBinding(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, id string, scope dashboardfilter.Scope, pageID string, binding dashboardfilter.Binding) (dashboardfilter.Binding, error) {
	definition := d.FilterDefinitions[binding.Filter]
	binding.ID, binding.Scope, binding.PageID, binding.ValueKind = id, scope, pageID, definition.ValueKind
	binding.Key = dashboardfilter.BindingKey(d.ID.String(), scope, pageID, id)
	if binding.Default.Kind == "" {
		binding.Default.Kind = dashboardfilter.ExpressionUnfiltered
	}
	canonical, err := dashboardfilter.Canonicalize(binding.Default, definition.ValueKind)
	if err != nil {
		return dashboardfilter.Binding{}, fmt.Errorf("%s filter binding %q default: %w", scope, id, err)
	}
	if !definitionAllowsExpression(definition, canonical) {
		return dashboardfilter.Binding{}, fmt.Errorf("%s filter binding %q default predicate %q operator %q is not allowed", scope, id, canonical.Kind, canonical.Operator)
	}
	binding.Default = canonical
	if binding.Selection.Mode == "" {
		binding.Selection.Mode = dashboardfilter.SelectionMultiple
	}
	if binding.Selection.Mode == dashboardfilter.SelectionSingle && binding.Selection.MaxSelectedValues > 1 {
		return dashboardfilter.Binding{}, fmt.Errorf("%s filter binding %q single selection cannot allow more than one value", scope, id)
	}
	if binding.URL.Param != "" && binding.URL.Encoding == "" {
		binding.URL.Encoding = dashboardfilter.URLEncodingTypedV1
	}
	targets, err := resolveBindingTargets(d, model, definition, binding)
	if err != nil {
		return dashboardfilter.Binding{}, fmt.Errorf("%s filter binding %q: %w", scope, id, err)
	}
	binding.Targets = targets
	return binding, nil
}

func definitionAllowsExpression(definition dashboardfilter.Definition, expression dashboardfilter.Expression) bool {
	if expression.Kind == dashboardfilter.ExpressionUnfiltered {
		return true
	}
	for _, predicate := range definition.Predicates {
		if predicate.Kind != expression.Kind {
			continue
		}
		if expression.Operator == "" {
			return true
		}
		for _, operator := range predicate.Operators {
			if operator == expression.Operator {
				return true
			}
		}
	}
	return false
}

func resolveBindingTargets(d *dashboardauthoring.Dashboard, model *semanticmodel.Model, definition dashboardfilter.Definition, binding dashboardfilter.Binding) ([]string, error) {
	candidates := []struct {
		key       string
		pageID    string
		component dashboard.PageVisual
	}{}
	for _, page := range d.Pages {
		if binding.Scope == dashboardfilter.ScopePage && page.ID != binding.PageID {
			continue
		}
		for _, component := range page.Visuals {
			if component.Kind != "visual" {
				continue
			}
			candidates = append(candidates, struct {
				key       string
				pageID    string
				component dashboard.PageVisual
			}{key: page.ID + "/" + component.ID, pageID: page.ID, component: component})
		}
	}
	byAuthoredTarget := map[string]struct {
		key       string
		component dashboard.PageVisual
	}{}
	for _, candidate := range candidates {
		target := candidate.component.ID
		if binding.Scope == dashboardfilter.ScopeReport {
			target = candidate.key
		}
		byAuthoredTarget[target] = struct {
			key       string
			component dashboard.PageVisual
		}{candidate.key, candidate.component}
	}
	for _, target := range append(append([]string{}, binding.TargetPolicy.Include...), binding.TargetPolicy.Exclude...) {
		if _, ok := byAuthoredTarget[target]; !ok {
			return nil, fmt.Errorf("references unknown component target %q", target)
		}
	}
	included := map[string]struct{}{}
	explicit := len(binding.TargetPolicy.Include) > 0
	for _, candidate := range candidates {
		authoredTarget := candidate.component.ID
		if binding.Scope == dashboardfilter.ScopeReport {
			authoredTarget = candidate.key
		}
		if explicit && !containsFilterTarget(binding.TargetPolicy.Include, authoredTarget) {
			continue
		}
		if containsFilterTarget(binding.TargetPolicy.Exclude, authoredTarget) {
			continue
		}
		applies, err := reportmodel.FieldAppliesToTarget(
			d, model, definition.Field, definition.Fact, "visual", candidate.component.Visual,
		)
		if err != nil {
			return nil, fmt.Errorf("target %q: %w", authoredTarget, err)
		}
		if !applies {
			if explicit {
				return nil, fmt.Errorf("target %q is semantically incompatible with field %q", authoredTarget, definition.Field)
			}
			continue
		}
		included[candidate.key] = struct{}{}
	}
	targets := make([]string, 0, len(included))
	for target := range included {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	return targets, nil
}

func containsFilterTarget(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func validateSlicerPresentations(d *dashboardauthoring.Dashboard, page *dashboard.Page) error {
	for index := range page.Visuals {
		component := &page.Visuals[index]
		if component.Kind != "slicer" {
			continue
		}
		binding := d.FilterBindings[component.Binding.ID]
		if component.Binding.Scope == dashboardfilter.ScopePage {
			binding = page.FilterBindings[component.Binding.ID]
		}
		definition := d.FilterDefinitions[binding.Filter]
		style := component.Presentation.Style
		if style == "" {
			style = defaultPresentationStyle(definition)
			component.Presentation.Style = style
		}
		if !presentationCompatible(style, definition, binding) {
			return fmt.Errorf("page %q slicer %q presentation %q is incompatible with filter %q", page.ID, component.ID, style, binding.Filter)
		}
	}
	return nil
}

func defaultPresentationStyle(definition dashboardfilter.Definition) dashboardfilter.PresentationStyle {
	for _, predicate := range definition.Predicates {
		switch predicate.Kind {
		case dashboardfilter.ExpressionSet:
			return dashboardfilter.PresentationDropdown
		case dashboardfilter.ExpressionRange:
			if definition.ValueKind == dashboardfilter.ValueDate || definition.ValueKind == dashboardfilter.ValueTimestamp {
				return dashboardfilter.PresentationDateRange
			}
			return dashboardfilter.PresentationNumericRange
		case dashboardfilter.ExpressionRelativePeriod:
			return dashboardfilter.PresentationRelativePeriod
		}
	}
	return dashboardfilter.PresentationInput
}

func presentationCompatible(style dashboardfilter.PresentationStyle, definition dashboardfilter.Definition, binding dashboardfilter.Binding) bool {
	switch style {
	case dashboardfilter.PresentationDropdown, dashboardfilter.PresentationList, dashboardfilter.PresentationButtons:
		return hasPredicateKind(definition, dashboardfilter.ExpressionSet)
	case dashboardfilter.PresentationInput:
		return hasPredicateKind(definition, dashboardfilter.ExpressionComparison)
	case dashboardfilter.PresentationNumericRange:
		return hasPredicateKind(definition, dashboardfilter.ExpressionRange) &&
			(binding.ValueKind == dashboardfilter.ValueInteger || binding.ValueKind == dashboardfilter.ValueDecimal)
	case dashboardfilter.PresentationDateRange:
		return hasPredicateKind(definition, dashboardfilter.ExpressionRange) &&
			(binding.ValueKind == dashboardfilter.ValueDate || binding.ValueKind == dashboardfilter.ValueTimestamp)
	case dashboardfilter.PresentationRelativePeriod:
		return hasPredicateKind(definition, dashboardfilter.ExpressionRelativePeriod)
	default:
		return false
	}
}

func hasPredicateKind(definition dashboardfilter.Definition, kind dashboardfilter.ExpressionKind) bool {
	for _, predicate := range definition.Predicates {
		if predicate.Kind == kind {
			return true
		}
	}
	return false
}

func compileOptionDependencies(d *dashboardauthoring.Dashboard) {
	for pageIndex := range d.Pages {
		page := d.Pages[pageIndex]
		active := make(map[string]dashboardfilter.Binding, len(d.FilterBindings)+len(page.FilterBindings))
		for id, binding := range d.FilterBindings {
			active["report/"+id] = binding
		}
		for id, binding := range page.FilterBindings {
			active["page/"+id] = binding
		}
		for targetIdentity, target := range active {
			dependencies := []dashboardfilter.BindingRef{}
			if target.Scope == dashboardfilter.ScopeReport {
				dependencies = append(dependencies, target.OptionDependencies...)
			}
			for sourceIdentity, source := range active {
				if sourceIdentity == targetIdentity || !targetsOverlap(source.Targets, target.Targets) {
					continue
				}
				reference := dashboardfilter.BindingRef{Scope: source.Scope, ID: source.ID}
				if bindingRefContains(target.OptionInteractions.Exclude, reference) {
					continue
				}
				if !bindingRefContains(dependencies, reference) {
					dependencies = append(dependencies, reference)
				}
			}
			for _, reference := range target.OptionInteractions.Include {
				if !bindingRefContains(dependencies, reference) {
					dependencies = append(dependencies, reference)
				}
			}
			sort.Slice(dependencies, func(i, j int) bool {
				if dependencies[i].Scope == dependencies[j].Scope {
					return dependencies[i].ID < dependencies[j].ID
				}
				return dependencies[i].Scope < dependencies[j].Scope
			})
			target.OptionDependencies = dependencies
			if target.Scope == dashboardfilter.ScopeReport {
				d.FilterBindings[target.ID] = target
			} else {
				page.FilterBindings[target.ID] = target
			}
		}
		d.Pages[pageIndex] = page
	}
}

func targetsOverlap(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, target := range left {
		seen[target] = struct{}{}
	}
	for _, target := range right {
		if _, ok := seen[target]; ok {
			return true
		}
	}
	return false
}

func bindingRefContains(values []dashboardfilter.BindingRef, value dashboardfilter.BindingRef) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
