package authoring

import (
	"fmt"
	"sort"
	"strings"

	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func (d *Dashboard) validateFilterArchitectureContract() error {
	if len(d.FilterDefinitions) == 0 && len(d.FilterBindings) == 0 {
		return nil
	}
	application := d.FilterApplication.WithDefaults()
	if application.Mode != dashboardfilter.ApplicationImmediate && application.Mode != dashboardfilter.ApplicationDeferred {
		return fmt.Errorf("filter_application has unsupported mode %q", application.Mode)
	}
	d.FilterApplication = application
	for id, definition := range d.FilterDefinitions {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(definition.Label) == "" || strings.TrimSpace(definition.Field) == "" {
			return fmt.Errorf("filter definition %q requires id, label, and field", id)
		}
		if len(definition.Predicates) == 0 {
			return fmt.Errorf("filter definition %q requires predicates", id)
		}
		seenKinds := map[dashboardfilter.ExpressionKind]struct{}{}
		for _, predicate := range definition.Predicates {
			if _, exists := seenKinds[predicate.Kind]; exists {
				return fmt.Errorf("filter definition %q has duplicate predicate kind %q", id, predicate.Kind)
			}
			seenKinds[predicate.Kind] = struct{}{}
			if err := validatePredicatePolicy(predicate); err != nil {
				return fmt.Errorf("filter definition %q: %w", id, err)
			}
		}
		switch definition.Options.Kind {
		case dashboardfilter.OptionSourceNone:
			if len(definition.Options.Values) > 0 {
				return fmt.Errorf("filter definition %q options values require kind static", id)
			}
		case dashboardfilter.OptionSourceStatic:
			if len(definition.Options.Values) == 0 {
				return fmt.Errorf("filter definition %q static options require values", id)
			}
		case dashboardfilter.OptionSourceDistinct:
			if len(definition.Options.Values) > 0 {
				return fmt.Errorf("filter definition %q distinct options cannot define values", id)
			}
			if definition.Options.Limit < 0 || definition.Options.Limit > 500 {
				return fmt.Errorf("filter definition %q distinct option limit must be between 0 and 500", id)
			}
		default:
			return fmt.Errorf("filter definition %q has unsupported options kind %q", id, definition.Options.Kind)
		}
	}
	reportParams := map[string]string{}
	for id, binding := range d.FilterBindings {
		if err := d.validateFilterBinding(id, dashboardfilter.ScopeReport, "", binding); err != nil {
			return err
		}
		if err := recordBindingURLParam(reportParams, "report/"+id, binding.URL); err != nil {
			return err
		}
	}
	for _, page := range d.Pages {
		params := make(map[string]string, len(reportParams)+len(page.FilterBindings))
		for param, owner := range reportParams {
			params[param] = owner
		}
		for id, binding := range page.FilterBindings {
			if err := d.validateFilterBinding(id, dashboardfilter.ScopePage, page.ID, binding); err != nil {
				return err
			}
			if err := recordBindingURLParam(params, page.ID+"/"+id, binding.URL); err != nil {
				return err
			}
		}
	}
	return nil
}

func validatePredicatePolicy(predicate dashboardfilter.PredicatePolicy) error {
	switch predicate.Kind {
	case dashboardfilter.ExpressionNullCheck:
		return requireOperators(predicate, dashboardfilter.OperatorIsNull, dashboardfilter.OperatorIsNotNull)
	case dashboardfilter.ExpressionSet:
		return requireOperators(predicate, dashboardfilter.OperatorIn, dashboardfilter.OperatorNotIn)
	case dashboardfilter.ExpressionComparison:
		return requireOperators(predicate,
			dashboardfilter.OperatorEquals, dashboardfilter.OperatorNotEquals,
			dashboardfilter.OperatorContains, dashboardfilter.OperatorNotContains,
			dashboardfilter.OperatorStartsWith, dashboardfilter.OperatorEndsWith,
			dashboardfilter.OperatorGreaterThan, dashboardfilter.OperatorGreaterThanOrEqual,
			dashboardfilter.OperatorLessThan, dashboardfilter.OperatorLessThanOrEqual,
		)
	case dashboardfilter.ExpressionRange, dashboardfilter.ExpressionRelativePeriod:
		if len(predicate.Operators) != 0 {
			return fmt.Errorf("predicate kind %q does not accept operators", predicate.Kind)
		}
		return nil
	default:
		return fmt.Errorf("unsupported predicate kind %q", predicate.Kind)
	}
}

func requireOperators(predicate dashboardfilter.PredicatePolicy, allowed ...dashboardfilter.Operator) error {
	if len(predicate.Operators) == 0 {
		return fmt.Errorf("predicate kind %q requires operators", predicate.Kind)
	}
	allowedSet := make(map[dashboardfilter.Operator]struct{}, len(allowed))
	for _, operator := range allowed {
		allowedSet[operator] = struct{}{}
	}
	seen := map[dashboardfilter.Operator]struct{}{}
	for _, operator := range predicate.Operators {
		if _, ok := allowedSet[operator]; !ok {
			return fmt.Errorf("predicate kind %q has unsupported operator %q", predicate.Kind, operator)
		}
		if _, exists := seen[operator]; exists {
			return fmt.Errorf("predicate kind %q has duplicate operator %q", predicate.Kind, operator)
		}
		seen[operator] = struct{}{}
	}
	return nil
}

func (d *Dashboard) validateFilterBinding(id string, scope dashboardfilter.Scope, pageID string, binding dashboardfilter.Binding) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(binding.Filter) == "" {
		return fmt.Errorf("%s filter binding %q requires id and filter", scope, id)
	}
	if _, ok := d.FilterDefinitions[binding.Filter]; !ok {
		return fmt.Errorf("%s filter binding %q references unknown filter definition %q", scope, id, binding.Filter)
	}
	switch binding.Selection.Mode {
	case "", dashboardfilter.SelectionSingle, dashboardfilter.SelectionMultiple:
	default:
		return fmt.Errorf("%s filter binding %q has unsupported selection mode %q", scope, id, binding.Selection.Mode)
	}
	if binding.Selection.MaxSelectedValues < 0 {
		return fmt.Errorf("%s filter binding %q has negative max_selected_values", scope, id)
	}
	if len(binding.TargetPolicy.Include) > 0 && len(binding.TargetPolicy.Exclude) > 0 {
		return fmt.Errorf("%s filter binding %q targets cannot define both include and exclude", scope, id)
	}
	if binding.URL.Param != "" && binding.URL.Encoding != "" && binding.URL.Encoding != dashboardfilter.URLEncodingTypedV1 {
		return fmt.Errorf("%s filter binding %q has unsupported URL encoding %q", scope, id, binding.URL.Encoding)
	}
	if binding.URL.Param == "" && binding.URL.Encoding != "" {
		return fmt.Errorf("%s filter binding %q URL encoding requires param", scope, id)
	}
	if scope == dashboardfilter.ScopePage && pageID == "" {
		return fmt.Errorf("page filter binding %q requires page identity", id)
	}
	return nil
}

func recordBindingURLParam(seen map[string]string, owner string, policy dashboardfilter.URLPolicy) error {
	if policy.Param == "" {
		return nil
	}
	if previous, exists := seen[policy.Param]; exists {
		owners := []string{previous, owner}
		sort.Strings(owners)
		return fmt.Errorf("filter URL parameter %q is shared by bindings %q and %q", policy.Param, owners[0], owners[1])
	}
	seen[policy.Param] = owner
	return nil
}

func (d *Dashboard) bindingReferenceExists(pageID string, reference dashboardfilter.BindingRef) bool {
	switch reference.Scope {
	case dashboardfilter.ScopeReport:
		_, ok := d.FilterBindings[reference.ID]
		return ok
	case dashboardfilter.ScopePage:
		for _, page := range d.Pages {
			if page.ID == pageID {
				_, ok := page.FilterBindings[reference.ID]
				return ok
			}
		}
	}
	return false
}
