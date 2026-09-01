package compiler

// Canonical dashboard filters are compiled directly from the generated
// document DTO.  This file intentionally does not use dashboard/authoring:
// the DTO is the authoring boundary for LEA-419 and the returned values are
// the same immutable filter contracts consumed by the existing state machine.

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

var canonicalURLParameterPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// CanonicalFilterCompilation is the compiler seam used by the canonical
// dashboard document compiler.  Order is retained separately because maps in
// the runtime contracts are keyed by stable IDs and intentionally unordered.
type CanonicalFilterCompilation struct {
	Definitions map[string]dashboardfilter.Definition
	Bindings    map[string]dashboardfilter.Binding // report-scoped bindings
	Pages       []dashboard.Page
	Order       []string
	Application dashboardfilter.ApplicationPolicy
}

// ApplyToDefinition is the hand-off point for the atomic canonical authoring
// cutover. It mutates only the supplied compiler-owned definition and never
// creates an authoring shadow.
func (compiled CanonicalFilterCompilation) ApplyToDefinition(definition *dashboarddefinition.Definition) error {
	if definition == nil {
		return fmt.Errorf("dashboard definition is required")
	}
	compiledSlicers := make(map[string]dashboard.PageVisual)
	for _, page := range compiled.Pages {
		for _, component := range page.Visuals {
			if component.Kind == "slicer" {
				compiledSlicers[page.ID+"\x00"+component.ID] = component
			}
		}
	}
	for pageIndex := range definition.Pages {
		page := &definition.Pages[pageIndex]
		for _, compiledPage := range compiled.Pages {
			if compiledPage.ID == page.ID {
				page.FilterBindings = cloneCanonicalBindings(compiledPage.FilterBindings)
				break
			}
		}
		for componentIndex := range page.Visuals {
			component := &page.Visuals[componentIndex]
			if component.Kind != "slicer" {
				continue
			}
			canonical, ok := compiledSlicers[page.ID+"\x00"+component.ID]
			if !ok {
				return fmt.Errorf("page %q slicer %q is missing from canonical filter compilation", page.ID, component.ID)
			}
			component.Binding = canonical.Binding
			component.Presentation = canonical.Presentation
		}
	}
	definition.FilterDefinitions = cloneCanonicalDefinitions(compiled.Definitions)
	definition.FilterBindings = cloneCanonicalBindings(compiled.Bindings)
	definition.FilterOrder = append([]string(nil), compiled.Order...)
	definition.FilterApplication = compiled.Application.WithDefaults()
	return nil
}

func cloneCanonicalDefinitions(values map[string]dashboardfilter.Definition) map[string]dashboardfilter.Definition {
	result := make(map[string]dashboardfilter.Definition, len(values))
	for key, value := range values {
		value.Predicates = append([]dashboardfilter.PredicatePolicy(nil), value.Predicates...)
		for index := range value.Predicates {
			value.Predicates[index].Operators = append([]dashboardfilter.Operator(nil), value.Predicates[index].Operators...)
		}
		value.Options.Values = append([]dashboardfilter.Option(nil), value.Options.Values...)
		result[key] = value
	}
	return result
}

func cloneCanonicalBindings(values map[string]dashboardfilter.Binding) map[string]dashboardfilter.Binding {
	result := make(map[string]dashboardfilter.Binding, len(values))
	for key, value := range values {
		value.TargetPolicy.Include = append([]string(nil), value.TargetPolicy.Include...)
		value.Targets = append([]string(nil), value.Targets...)
		value.OptionDependencies = append([]dashboardfilter.BindingRef(nil), value.OptionDependencies...)
		result[key] = value
	}
	return result
}

// CompileCanonicalDashboardFilters compiles the one ordered spec.filters
// sequence into the existing governed filter definition, binding, placement,
// URL and state contracts. Visual query lowering is deliberately not coupled
// to this seam; callers can compile visuals independently and pass the same
// canonical document to the query compiler.
func CompileCanonicalDashboardFilters(doc document.DashboardDocument, model *semanticmodel.Model) (CanonicalFilterCompilation, error) {
	return compileCanonicalDashboardFilters(doc, model, canonicalFilterVisualValidationStrict)
}

// CompileCanonicalDashboardFilterContract compiles the governed filter state
// contract without lowering visual queries. It is used by exact draft option
// loading, where an unrelated in-progress visual must not block filter state
// or static/distinct option metadata.
func CompileCanonicalDashboardFilterContract(doc document.DashboardDocument, model *semanticmodel.Model) (CanonicalFilterCompilation, error) {
	return compileCanonicalDashboardFilters(doc, model, canonicalFilterVisualValidationNone)
}

// CompileCanonicalDashboardBuilderFilters validates filter compatibility for
// every visual query that can be lowered independently. Invalid in-progress
// visuals are omitted by the builder preview, so they must not hide filter
// validation errors on otherwise valid visual targets.
func CompileCanonicalDashboardBuilderFilters(doc document.DashboardDocument, model *semanticmodel.Model) (CanonicalFilterCompilation, error) {
	return compileCanonicalDashboardFilters(doc, model, canonicalFilterVisualValidationBestEffort)
}

type canonicalFilterVisualValidation uint8

const (
	canonicalFilterVisualValidationNone canonicalFilterVisualValidation = iota
	canonicalFilterVisualValidationBestEffort
	canonicalFilterVisualValidationStrict
)

func compileCanonicalDashboardFilters(doc document.DashboardDocument, model *semanticmodel.Model, visualValidation canonicalFilterVisualValidation) (CanonicalFilterCompilation, error) {
	if model == nil {
		return CanonicalFilterCompilation{}, fmt.Errorf("semantic model is required")
	}
	dashboardID := strings.TrimSpace(doc.Metadata.ID)
	if dashboardID == "" {
		dashboardID = strings.TrimSpace(doc.Metadata.Name)
	}
	if dashboardID == "" {
		return CanonicalFilterCompilation{}, fmt.Errorf("dashboard metadata.id or metadata.name is required")
	}
	result := CanonicalFilterCompilation{
		Definitions: make(map[string]dashboardfilter.Definition, len(doc.Spec.Filters)),
		Bindings:    make(map[string]dashboardfilter.Binding, len(doc.Spec.Filters)),
		Pages:       make([]dashboard.Page, 0, len(doc.Spec.Pages)),
		Order:       make([]string, 0, len(doc.Spec.Filters)),
		Application: dashboardfilter.ApplicationPolicy{Mode: dashboardfilter.ApplicationImmediate},
	}
	for index, authored := range doc.Spec.Filters {
		id := strings.TrimSpace(authored.ID)
		if id == "" {
			return CanonicalFilterCompilation{}, fmt.Errorf("filter %d requires id", index)
		}
		if _, exists := result.Definitions[id]; exists {
			return CanonicalFilterCompilation{}, fmt.Errorf("duplicate filter id %q", id)
		}
		definition, binding, err := compileCanonicalFilter(authored, model, dashboardID, index)
		if err != nil {
			return CanonicalFilterCompilation{}, fmt.Errorf("filter %q: %w", id, err)
		}
		result.Definitions[id] = definition
		result.Bindings[id] = binding
		result.Order = append(result.Order, id)
	}
	if err := validateCanonicalOptionDependencies(doc.Spec.Filters, result.Definitions, model); err != nil {
		return CanonicalFilterCompilation{}, err
	}
	pages, err := compileCanonicalFilterPages(doc, model, dashboardID, result.Definitions, result.Bindings, visualValidation)
	if err != nil {
		return CanonicalFilterCompilation{}, err
	}
	result.Pages = pages
	if err := validateCanonicalRouteURLParameters(result.Bindings, result.Pages); err != nil {
		return CanonicalFilterCompilation{}, err
	}
	return result, nil
}

// CompileCanonicalDashboardFilterDefinition compiles one DTO filter. It is
// exported for focused compiler tests and for the LEA-426 document cutover.
func CompileCanonicalDashboardFilterDefinition(authored document.DashboardFilter, model *semanticmodel.Model) (dashboardfilter.Definition, dashboardfilter.Binding, error) {
	return compileCanonicalFilter(authored, model, "canonical", 0)
}

func compileCanonicalFilter(authored document.DashboardFilter, model *semanticmodel.Model, dashboardID string, order int) (dashboardfilter.Definition, dashboardfilter.Binding, error) {
	id := strings.TrimSpace(authored.ID)
	if id == "" || strings.TrimSpace(authored.Label) == "" {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("requires id and label")
	}
	dimension := strings.TrimSpace(authored.Dimension)
	if dimension == "" {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("requires semantic dimension")
	}
	semantic, err := model.ResolveSemanticDimension(dimension)
	if err != nil {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("dimension %q is not a semantic dimension: %w", dimension, err)
	}
	kind, err := canonicalSemanticValueKind(semantic)
	if err != nil {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, err
	}
	controlType, err := authored.Control.Type()
	if err != nil {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("control: %w", err)
	}
	definition := dashboardfilter.Definition{
		Label: authored.Label, Field: dimension, Description: stringPointerValue(authored.Description), ValueKind: kind,
		Time: dashboardfilter.TimeSemantics{Timezone: semantic.Timezone, Calendar: semantic.Calendar, WeekStart: semantic.WeekStart},
	}
	if definition.Time.Timezone == "" {
		definition.Time.Timezone = "UTC"
	}
	if definition.Time.Calendar == "" {
		definition.Time.Calendar = "gregorian"
	}
	if definition.Time.WeekStart == "" {
		definition.Time.WeekStart = "monday"
	}
	definition.Predicates, err = canonicalPredicates(controlType, authored.Operators, kind)
	if err != nil {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, err
	}
	if err := compileCanonicalOptions(&definition, authored.Control, authored.ID, model, kind); err != nil {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, err
	}
	if definition.Options.IncludeNull {
		// A null option is represented by the null-check predicate rather than a
		// fabricated typed value. Permit that predicate only when the canonical
		// option source explicitly exposes null.
		for index := range definition.Predicates {
			if definition.Predicates[index].Kind == dashboardfilter.ExpressionSet && !slices.Contains(definition.Predicates[index].Operators, dashboardfilter.OperatorIsNull) {
				definition.Predicates[index].Operators = append(definition.Predicates[index].Operators, dashboardfilter.OperatorIsNull)
			}
		}
	}
	defaultExpression := dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}
	if authored.Default != nil {
		defaultExpression, err = canonicalExpression(*authored.Default, kind)
		if err != nil {
			return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("default: %w", err)
		}
	}
	if !definitionAllowsExpression(definition, defaultExpression) {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("default predicate %q operator %q is not allowed", defaultExpression.Kind, defaultExpression.Operator)
	}
	required := authored.Required != nil && *authored.Required
	if required && defaultExpression.Kind == dashboardfilter.ExpressionUnfiltered {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("required filter must have a non-empty default")
	}
	selection := dashboardfilter.SelectionPolicy{Mode: dashboardfilter.SelectionMultiple}
	if controlType == "singleSelect" {
		selection.Mode = dashboardfilter.SelectionSingle
	} else if controlType == "multiSelect" {
		if control, ok := authored.Control.Value.(*document.MultiSelectDashboardFilterControl); ok && control.MaxSelectedValues != nil {
			if *control.MaxSelectedValues <= 0 {
				return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("maxSelectedValues must be positive")
			}
			selection.MaxSelectedValues = int(*control.MaxSelectedValues)
		}
	}
	if selection.Mode == dashboardfilter.SelectionSingle && len(defaultExpression.Values) > 1 {
		return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("singleSelect default contains multiple values")
	}
	editable := true
	if authored.ReaderEditable != nil {
		editable = *authored.ReaderEditable
	}
	binding := dashboardfilter.Binding{
		Filter: id, Default: defaultExpression, Required: required, Selection: selection,
		ReaderEditable: &editable, Pane: dashboardfilter.PanePolicy{Order: order}, Scope: dashboardfilter.ScopeReport,
		ID: id, Key: dashboardfilter.BindingKey(dashboardID, dashboardfilter.ScopeReport, "", id), ValueKind: kind,
	}
	if authored.URLParameter != nil {
		param := strings.TrimSpace(*authored.URLParameter)
		if err := validateCanonicalURLParameter(param); err != nil {
			return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("urlParameter: %w", err)
		}
		// The authored document owns only the parameter name. Wire codec
		// selection belongs to the active shared-link registry lifecycle.
		binding.URL = dashboardfilter.URLPolicy{Param: param}
	}
	if targets := authored.Targets; targets != nil {
		if len(*targets) == 0 {
			return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("targets cannot be empty when specified")
		}
		seenTargets := map[string]struct{}{}
		for _, target := range *targets {
			target = strings.TrimSpace(target)
			if target == "" {
				return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("targets cannot contain empty identities")
			}
			if _, exists := seenTargets[target]; exists {
				return dashboardfilter.Definition{}, dashboardfilter.Binding{}, fmt.Errorf("duplicate target %q", target)
			}
			seenTargets[target] = struct{}{}
		}
		binding.TargetPolicy.Include = append([]string(nil), (*targets)...)
	}
	return definition, binding, nil
}

func compileCanonicalOptions(definition *dashboardfilter.Definition, control document.DashboardFilterControl, filterID string, model *semanticmodel.Model, kind dashboardfilter.ValueKind) error {
	var options *document.DashboardFilterOptions
	switch value := control.Value.(type) {
	case *document.SingleSelectDashboardFilterControl:
		options = value.Options
	case *document.MultiSelectDashboardFilterControl:
		options = value.Options
	case *document.TextDashboardFilterControl, *document.NumericRangeDashboardFilterControl, *document.DateRangeDashboardFilterControl, *document.RelativePeriodDashboardFilterControl:
		return nil
	default:
		return fmt.Errorf("unsupported control %T", control.Value)
	}
	if options == nil {
		return nil
	}
	typ, err := options.Type()
	if err != nil {
		return fmt.Errorf("options: %w", err)
	}
	switch typ {
	case "static":
		value := options.Value.(*document.StaticDashboardFilterOptions)
		if len(value.Values) == 0 {
			return fmt.Errorf("static options require values")
		}
		definition.Options.Kind = dashboardfilter.OptionSourceStatic
		compiledOptions := make([]dashboardfilter.Option, 0, len(value.Values))
		for index, option := range value.Values {
			typed, err := canonicalFilterValue(option.Value, kind)
			if err != nil {
				return fmt.Errorf("static option %d: %w", index, err)
			}
			compiledOptions = append(compiledOptions, dashboardfilter.Option{Value: typed, Label: option.Label})
		}
		definition.Options.Values, err = dashboardfilter.CanonicalizeStaticOptions(compiledOptions, kind)
		if err != nil {
			return err
		}
	case "distinct":
		value := options.Value.(*document.DistinctDashboardFilterOptions)
		dataset := strings.TrimSpace(value.Dataset)
		if dataset == "" {
			return fmt.Errorf("distinct options dataset is required")
		}
		if _, ok := model.Datasets[dataset]; !ok {
			return fmt.Errorf("distinct options references unknown dataset %q", dataset)
		}
		if semantic, ok := model.Dimensions[definition.Field]; !ok {
			return fmt.Errorf("distinct options field %q is not a semantic dimension", definition.Field)
		} else if binding, ok := semantic.Bindings[dataset]; !ok {
			return fmt.Errorf("distinct options dataset %q cannot resolve dimension %q", dataset, definition.Field)
		} else if _, err := model.ResolveDimension(binding.Field); err != nil {
			return fmt.Errorf("distinct options dimension binding %q: %w", definition.Field, err)
		}
		definition.Dataset = dataset
		definition.Options.Kind = dashboardfilter.OptionSourceDistinct
		if value.Limit != nil {
			if *value.Limit <= 0 || *value.Limit > 500 {
				return fmt.Errorf("distinct option limit must be between 1 and 500")
			}
			definition.Options.Limit = int(*value.Limit)
		}
		definition.Options.IncludeNull = value.IncludeNull != nil && *value.IncludeNull
		if value.DependsOn != nil {
			for _, dependency := range *value.DependsOn {
				dependency = strings.TrimSpace(dependency)
				if dependency == "" || dependency == filterID {
					return fmt.Errorf("distinct option dependency %q is invalid", dependency)
				}
			}
		}
	default:
		return fmt.Errorf("unsupported options type %q", typ)
	}
	return nil
}

func validateCanonicalOptionDependencies(filters []document.DashboardFilter, definitions map[string]dashboardfilter.Definition, model *semanticmodel.Model) error {
	for _, authored := range filters {
		control := authored.Control.Value
		var options *document.DashboardFilterOptions
		switch value := control.(type) {
		case *document.SingleSelectDashboardFilterControl:
			options = value.Options
		case *document.MultiSelectDashboardFilterControl:
			options = value.Options
		}
		if options == nil {
			continue
		}
		value, ok := options.Value.(*document.DistinctDashboardFilterOptions)
		if !ok || value.DependsOn == nil {
			continue
		}
		seen := map[string]struct{}{}
		for _, dependency := range *value.DependsOn {
			if _, exists := seen[dependency]; exists {
				return fmt.Errorf("filter %q has duplicate option dependency %q", authored.ID, dependency)
			}
			seen[dependency] = struct{}{}
			if _, exists := definitions[dependency]; !exists {
				return fmt.Errorf("filter %q option dependency references unknown filter %q", authored.ID, dependency)
			}
			definition := definitions[authored.ID]
			dependencyDefinition := definitions[dependency]
			semantic, ok := model.Dimensions[dependencyDefinition.Field]
			if !ok {
				return fmt.Errorf("filter %q dependency %q is not a semantic dimension", authored.ID, dependency)
			}
			if binding, ok := semantic.Bindings[definition.Dataset]; !ok {
				return fmt.Errorf("filter %q dependency %q cannot resolve on option dataset %q", authored.ID, dependency, definition.Dataset)
			} else if _, err := model.ResolveDimension(binding.Field); err != nil {
				return fmt.Errorf("filter %q dependency %q: %w", authored.ID, dependency, err)
			}
		}
	}
	return nil
}

func compileCanonicalFilterPages(doc document.DashboardDocument, model *semanticmodel.Model, dashboardID string, definitions map[string]dashboardfilter.Definition, bindings map[string]dashboardfilter.Binding, visualValidation canonicalFilterVisualValidation) ([]dashboard.Page, error) {
	authoredFilters := make(map[string]document.DashboardFilter, len(doc.Spec.Filters))
	for _, filter := range doc.Spec.Filters {
		authoredFilters[strings.TrimSpace(filter.ID)] = filter
	}
	templates := cloneCanonicalBindings(bindings)
	pageBindings := make(map[string]map[string]dashboardfilter.Binding, len(doc.Spec.Pages))
	pageBindingIDsByFilter := make(map[string]map[string]string, len(doc.Spec.Pages))
	pageScopedFilters := map[string]struct{}{}
	for _, authoredPage := range doc.Spec.Pages {
		pageID := strings.TrimSpace(authoredPage.ID)
		pageBindings[pageID] = map[string]dashboardfilter.Binding{}
		pageBindingIDsByFilter[pageID] = map[string]string{}
		if authoredPage.FilterBindings == nil {
			continue
		}
		for _, authoredBinding := range *authoredPage.FilterBindings {
			bindingID := strings.TrimSpace(authoredBinding.ID)
			filterID := strings.TrimSpace(authoredBinding.Filter)
			if bindingID == "" || filterID == "" {
				return nil, fmt.Errorf("page %q filter binding requires id and filter", pageID)
			}
			if _, exists := pageBindings[pageID][bindingID]; exists {
				return nil, fmt.Errorf("page %q has duplicate filter binding id %q", pageID, bindingID)
			}
			if previous, exists := pageBindingIDsByFilter[pageID][filterID]; exists {
				return nil, fmt.Errorf("page %q binds filter %q more than once (%q and %q)", pageID, filterID, previous, bindingID)
			}
			template, exists := templates[filterID]
			if !exists {
				return nil, fmt.Errorf("page %q filter binding %q references unknown filter %q", pageID, bindingID, filterID)
			}
			template.ID = bindingID
			template.Scope = dashboardfilter.ScopePage
			template.PageID = pageID
			template.Key = dashboardfilter.BindingKey(dashboardID, dashboardfilter.ScopePage, pageID, bindingID)
			template.TargetPolicy = dashboardfilter.TargetPolicy{}
			template.Targets = nil
			template.OptionDependencies = nil
			definition := definitions[filterID]
			if authoredBinding.Default != nil {
				expression, err := canonicalExpression(*authoredBinding.Default, template.ValueKind)
				if err != nil {
					return nil, fmt.Errorf("page %q filter binding %q default: %w", pageID, bindingID, err)
				}
				if !definitionAllowsExpression(definition, expression) {
					return nil, fmt.Errorf("page %q filter binding %q default predicate is not allowed", pageID, bindingID)
				}
				template.Default = expression
			}
			if authoredBinding.Required != nil {
				template.Required = *authoredBinding.Required
			}
			if template.Required && template.Default.Kind == dashboardfilter.ExpressionUnfiltered {
				return nil, fmt.Errorf("page %q filter binding %q required filter must have a non-empty default", pageID, bindingID)
			}
			if authoredBinding.ReaderEditable != nil {
				template.ReaderEditable = authoredBinding.ReaderEditable
			}
			if authoredBinding.URLParameter != nil {
				param := strings.TrimSpace(*authoredBinding.URLParameter)
				if err := validateCanonicalURLParameter(param); err != nil {
					return nil, fmt.Errorf("page %q filter binding %q urlParameter: %w", pageID, bindingID, err)
				}
				template.URL = dashboardfilter.URLPolicy{Param: param}
			}
			if authoredBinding.Targets != nil {
				if len(*authoredBinding.Targets) == 0 {
					return nil, fmt.Errorf("page %q filter binding %q targets cannot be empty when specified", pageID, bindingID)
				}
				seenTargets := map[string]struct{}{}
				for _, target := range *authoredBinding.Targets {
					if _, exists := seenTargets[target]; exists {
						return nil, fmt.Errorf("page %q filter binding %q has duplicate target %q", pageID, bindingID, target)
					}
					seenTargets[target] = struct{}{}
				}
				template.TargetPolicy.Include = append([]string(nil), (*authoredBinding.Targets)...)
			}
			pageBindings[pageID][bindingID] = template
			pageBindingIDsByFilter[pageID][filterID] = bindingID
			pageScopedFilters[filterID] = struct{}{}
		}
	}
	// A governed filter has one authored state scope. Existing documents with
	// no page bindings retain their implicit report binding; explicitly placing
	// it on any page moves that definition out of report scope.
	for filterID := range pageScopedFilters {
		delete(bindings, filterID)
	}

	pages := make([]dashboard.Page, 0, len(doc.Spec.Pages))
	for _, authored := range doc.Spec.Pages {
		page := dashboard.Page{ID: strings.TrimSpace(authored.ID), Title: authored.Title, FilterBindings: pageBindings[strings.TrimSpace(authored.ID)], Visuals: []dashboard.PageVisual{}}
		if page.ID == "" {
			return nil, fmt.Errorf("page requires id")
		}
		seenComponents := map[string]struct{}{}
		seenFilters := map[string]struct{}{}
		for _, component := range authored.Components {
			base, err := component.Base()
			if err != nil {
				return nil, fmt.Errorf("page %q component: %w", page.ID, err)
			}
			if base.ID == "" {
				return nil, fmt.Errorf("page %q component requires id", page.ID)
			}
			if _, exists := seenComponents[base.ID]; exists {
				return nil, fmt.Errorf("page %q has duplicate component id %q", page.ID, base.ID)
			}
			seenComponents[base.ID] = struct{}{}
			placement := dashboard.PagePlacement{Col: int(base.Placement.Column), Row: int(base.Placement.Row), ColSpan: int(base.Placement.ColumnSpan), RowSpan: int(base.Placement.RowSpan)}
			if placement.Col <= 0 || placement.Row <= 0 || placement.ColSpan <= 0 || placement.RowSpan <= 0 {
				return nil, fmt.Errorf("page %q component %q has invalid placement", page.ID, base.ID)
			}
			switch value := component.Value.(type) {
			case *document.FilterDashboardPageComponent:
				filterID := strings.TrimSpace(value.Filter)
				if _, ok := definitions[filterID]; !ok {
					return nil, fmt.Errorf("page %q component %q references unknown filter %q", page.ID, base.ID, filterID)
				}
				presentation, err := canonicalFilterPresentation(authoredFilters[filterID].Control)
				if err != nil {
					return nil, fmt.Errorf("page %q component %q filter %q presentation: %w", page.ID, base.ID, filterID, err)
				}
				if _, exists := seenFilters[filterID]; exists {
					return nil, fmt.Errorf("page %q places filter %q more than once", page.ID, filterID)
				}
				seenFilters[filterID] = struct{}{}
				bindingRef := dashboardfilter.BindingRef{Scope: dashboardfilter.ScopeReport, ID: filterID}
				if bindingID, exists := pageBindingIDsByFilter[page.ID][filterID]; exists {
					bindingRef = dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: bindingID}
				} else if _, exists := bindings[filterID]; !exists {
					return nil, fmt.Errorf("page %q component %q references page-scoped filter %q without a local binding", page.ID, base.ID, filterID)
				}
				page.Visuals = append(page.Visuals, dashboard.PageVisual{ID: base.ID, Kind: "slicer", Binding: bindingRef, Presentation: presentation, Placement: placement})
			case *document.VisualDashboardPageComponent:
				if strings.TrimSpace(value.Visual) == "" {
					return nil, fmt.Errorf("page %q component %q visual is required", page.ID, base.ID)
				}
				page.Visuals = append(page.Visuals, dashboard.PageVisual{ID: base.ID, Kind: "visual", Visual: value.Visual, Placement: placement})
			case *document.HeaderDashboardPageComponent:
				visual := dashboard.PageVisual{ID: base.ID, Kind: "header", Placement: placement}
				if value.Title != nil {
					visual.Title = *value.Title
				}
				if value.Description != nil {
					visual.Description = *value.Description
				}
				page.Visuals = append(page.Visuals, visual)
			default:
				return nil, fmt.Errorf("page %q component %q has unsupported variant %T", page.ID, base.ID, component.Value)
			}
		}
		pages = append(pages, page)
	}
	if err := resolveCanonicalFilterTargets(doc, model, dashboardID, definitions, bindings, pages, visualValidation); err != nil {
		return nil, err
	}
	return pages, nil
}

func canonicalFilterPresentation(control document.DashboardFilterControl) (dashboardfilter.Presentation, error) {
	presentation := dashboardfilter.Presentation{}
	switch value := control.Value.(type) {
	case *document.SingleSelectDashboardFilterControl:
		presentation.Style = dashboardfilter.PresentationDropdown
		if value.Options != nil {
			switch value.Options.Value.(type) {
			case *document.DistinctDashboardFilterOptions:
				presentation.Style = dashboardfilter.PresentationList
			case *document.StaticDashboardFilterOptions:
				presentation.Style = dashboardfilter.PresentationButtons
			case nil:
				return dashboardfilter.Presentation{}, fmt.Errorf("singleSelect options variant is required")
			default:
				return dashboardfilter.Presentation{}, fmt.Errorf("unsupported singleSelect options %T", value.Options.Value)
			}
		}
	case *document.MultiSelectDashboardFilterControl:
		presentation.Style = dashboardfilter.PresentationDropdown
	case *document.TextDashboardFilterControl:
		presentation.Style = dashboardfilter.PresentationInput
	case *document.NumericRangeDashboardFilterControl:
		presentation.Style = dashboardfilter.PresentationNumericRange
	case *document.DateRangeDashboardFilterControl:
		presentation.Style = dashboardfilter.PresentationDateRange
	case *document.RelativePeriodDashboardFilterControl:
		presentation.Style = dashboardfilter.PresentationRelativePeriod
	case nil:
		return dashboardfilter.Presentation{}, fmt.Errorf("filter control variant is required")
	default:
		return dashboardfilter.Presentation{}, fmt.Errorf("unsupported filter control %T", control.Value)
	}
	return presentation, nil
}

func resolveCanonicalFilterTargets(doc document.DashboardDocument, model *semanticmodel.Model, _ string, definitions map[string]dashboardfilter.Definition, bindings map[string]dashboardfilter.Binding, pages []dashboard.Page, visualValidation canonicalFilterVisualValidation) error {
	for pageIndex := range pages {
		page := &pages[pageIndex]
		for _, component := range page.Visuals {
			if component.Kind != "visual" {
				continue
			}
			visual, ok := doc.Spec.Visuals[component.Visual]
			if !ok {
				return fmt.Errorf("page %q component %q references unknown visual %q", page.ID, component.ID, component.Visual)
			}
			var datasets []string
			if visualValidation != canonicalFilterVisualValidationNone {
				if lowered, err := LowerDashboardQuery(visual.Query, model, model.Name); err == nil {
					datasets = append(datasets, lowered.Plan.Datasets...)
					if len(datasets) == 0 {
						datasets, err = canonicalQueryDatasets(visual.Query, model)
						if err != nil {
							if visualValidation == canonicalFilterVisualValidationBestEffort {
								continue
							}
							return fmt.Errorf("visual %q query: %w", component.Visual, err)
						}
					}
				} else {
					var resolveErr error
					datasets, resolveErr = canonicalQueryDatasets(visual.Query, model)
					if resolveErr != nil {
						if visualValidation == canonicalFilterVisualValidationBestEffort {
							continue
						}
						return fmt.Errorf("visual %q query: %w", component.Visual, resolveErr)
					}
				}
			}
			for filterID, definition := range definitions {
				if binding, exists := bindings[filterID]; exists {
					if err := addCanonicalBindingTarget(&binding, definition, page.ID, component, datasets, model, visualValidation != canonicalFilterVisualValidationNone); err != nil {
						return fmt.Errorf("filter %q: %w", filterID, err)
					}
					bindings[filterID] = binding
				}
				for bindingID, binding := range page.FilterBindings {
					if binding.Filter != filterID {
						continue
					}
					if err := addCanonicalBindingTarget(&binding, definition, page.ID, component, datasets, model, visualValidation != canonicalFilterVisualValidationNone); err != nil {
						return fmt.Errorf("filter %q page binding %q: %w", filterID, bindingID, err)
					}
					page.FilterBindings[bindingID] = binding
				}
			}
		}
	}
	for filterID, binding := range bindings {
		if len(binding.TargetPolicy.Include) > 0 {
			for _, target := range binding.TargetPolicy.Include {
				if !canonicalTargetExists(target, pages) {
					return fmt.Errorf("filter %q references unknown target %q", filterID, target)
				}
				if pageID, componentID, qualified := strings.Cut(target, "/"); qualified && !canonicalComponentTargetIsUnique(pages, pageID, componentID) {
					return fmt.Errorf("filter %q target %q reuses a visual definition and cannot have independent component state", filterID, target)
				}
			}
		}
		sort.Strings(binding.Targets)
		dependencies, err := canonicalOptionDependencyRefsForBinding(doc.Spec.Filters, filterID, binding, bindings, pages)
		if err != nil {
			return err
		}
		binding.OptionDependencies = dependencies
		bindings[filterID] = binding
	}
	for pageIndex := range pages {
		page := &pages[pageIndex]
		for bindingID, binding := range page.FilterBindings {
			if len(binding.TargetPolicy.Include) > 0 {
				for _, target := range binding.TargetPolicy.Include {
					if !canonicalTargetExistsOnPage(target, *page) {
						return fmt.Errorf("page %q filter binding %q references target %q outside its page", page.ID, bindingID, target)
					}
					if !canonicalComponentTargetIsUnique(pages, page.ID, target) {
						return fmt.Errorf("page %q filter binding %q target %q reuses a visual definition and cannot have independent component state", page.ID, bindingID, target)
					}
				}
			}
			sort.Strings(binding.Targets)
			dependencies, err := canonicalOptionDependencyRefsForBinding(doc.Spec.Filters, binding.Filter, binding, bindings, pages)
			if err != nil {
				return err
			}
			binding.OptionDependencies = dependencies
			page.FilterBindings[bindingID] = binding
		}
	}
	return nil
}

func addCanonicalBindingTarget(binding *dashboardfilter.Binding, definition dashboardfilter.Definition, pageID string, component dashboard.PageVisual, datasets []string, model *semanticmodel.Model, validateCompatibility bool) error {
	if !validateCompatibility {
		// Filter-contract-only compilation cannot truthfully derive semantic
		// consumers without lowering the visual query. Keep targets empty; the
		// full preview compiler remains authoritative for query fan-out.
		return nil
	}
	explicit := len(binding.TargetPolicy.Include) > 0
	matches := explicit && canonicalBindingTargetMatches(*binding, pageID, component)
	compatible := canonicalDimensionApplies(definition.Field, datasets, model)
	if explicit && matches && !compatible {
		return fmt.Errorf("target %q is semantically incompatible with dimension %q", pageID+"/"+component.ID, definition.Field)
	}
	if !explicit && !compatible {
		return fmt.Errorf("is visible on incompatible target %q; narrow targets explicitly", pageID+"/"+component.ID)
	}
	if compatible && (!explicit || matches) {
		binding.Targets = append(binding.Targets, pageID+"/"+component.ID)
	}
	return nil
}

func validateCanonicalRouteURLParameters(reportBindings map[string]dashboardfilter.Binding, pages []dashboard.Page) error {
	validate := func(pageID string, pageBindings map[string]dashboardfilter.Binding) error {
		seen := map[string]string{}
		add := func(binding dashboardfilter.Binding) error {
			identity := string(binding.Scope) + ":" + binding.ID
			if binding.URL.Param == "" {
				return nil
			}
			if previous, exists := seen[binding.URL.Param]; exists {
				return fmt.Errorf("URL parameter %q is used by bindings %q and %q on page %q", binding.URL.Param, previous, identity, pageID)
			}
			seen[binding.URL.Param] = identity
			return nil
		}
		for _, binding := range reportBindings {
			if err := add(binding); err != nil {
				return err
			}
		}
		for _, binding := range pageBindings {
			if err := add(binding); err != nil {
				return err
			}
		}
		return nil
	}
	if len(pages) == 0 {
		return validate("", nil)
	}
	for _, page := range pages {
		if err := validate(page.ID, page.FilterBindings); err != nil {
			return err
		}
	}
	return nil
}

func canonicalQueryDatasets(query document.DashboardQuery, model *semanticmodel.Model) ([]string, error) {
	var datasets = map[string]struct{}{}
	addMetric := func(selection []document.DashboardMetricSelection) error {
		for _, value := range selection {
			name, _, err := canonicalMetric(value)
			if err != nil {
				return err
			}
			metric, err := model.ResolveMetric(name)
			if err != nil {
				return err
			}
			if metric.Dataset != "" {
				datasets[metric.Dataset] = struct{}{}
			}
		}
		return nil
	}
	switch value := query.Value.(type) {
	case *document.RecordsDashboardQuery:
		if value.Dataset == "" {
			return nil, fmt.Errorf("records query dataset is required")
		}
		datasets[value.Dataset] = struct{}{}
	case *document.AggregateDashboardQuery:
		if err := addMetric(value.Metrics); err != nil {
			return nil, err
		}
	case *document.PivotDashboardQuery:
		if err := addMetric(value.Metrics); err != nil {
			return nil, err
		}
	case *document.HistogramDashboardQuery:
		name, _, err := canonicalMetric(value.Field)
		if err != nil {
			return nil, err
		}
		metric, err := model.ResolveMetric(name)
		if err != nil {
			return nil, err
		}
		datasets[metric.Dataset] = struct{}{}
	case *document.DistributionDashboardQuery:
		name, _, err := canonicalMetric(value.Field)
		if err != nil {
			return nil, err
		}
		metric, err := model.ResolveMetric(name)
		if err != nil {
			return nil, err
		}
		datasets[metric.Dataset] = struct{}{}
	default:
		return nil, fmt.Errorf("query variant is required")
	}
	result := make([]string, 0, len(datasets))
	for dataset := range datasets {
		if dataset != "" {
			result = append(result, dataset)
		}
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, fmt.Errorf("query has no resolvable dataset")
	}
	return result, nil
}

func canonicalOptionDependencyRefs(filters []document.DashboardFilter, id string) []dashboardfilter.BindingRef {
	for _, filter := range filters {
		if filter.ID != id {
			continue
		}
		var options *document.DashboardFilterOptions
		switch control := filter.Control.Value.(type) {
		case *document.SingleSelectDashboardFilterControl:
			options = control.Options
		case *document.MultiSelectDashboardFilterControl:
			options = control.Options
		}
		if options == nil {
			return nil
		}
		if value, ok := options.Value.(*document.DistinctDashboardFilterOptions); ok && value.DependsOn != nil {
			refs := make([]dashboardfilter.BindingRef, 0, len(*value.DependsOn))
			for _, dependency := range *value.DependsOn {
				refs = append(refs, dashboardfilter.BindingRef{Scope: dashboardfilter.ScopeReport, ID: dependency})
			}
			return refs
		}
	}
	return nil
}

func canonicalOptionDependencyRefsForBinding(filters []document.DashboardFilter, id string, current dashboardfilter.Binding, reportBindings map[string]dashboardfilter.Binding, pages []dashboard.Page) ([]dashboardfilter.BindingRef, error) {
	dependencies := canonicalOptionDependencyRefs(filters, id)
	if len(dependencies) == 0 {
		return nil, nil
	}
	refs := make([]dashboardfilter.BindingRef, 0, len(dependencies))
	for _, dependency := range dependencies {
		if current.Scope == dashboardfilter.ScopePage {
			for _, page := range pages {
				if page.ID != current.PageID {
					continue
				}
				for _, binding := range page.FilterBindings {
					if binding.Filter == dependency.ID {
						if canonicalBindingTargetPoliciesOverlap(current, binding, pages) {
							refs = append(refs, dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: binding.ID})
						}
						goto resolved
					}
				}
			}
		}
		if report, exists := reportBindings[dependency.ID]; exists {
			if canonicalBindingTargetPoliciesOverlap(current, report, pages) {
				refs = append(refs, dashboardfilter.BindingRef{Scope: dashboardfilter.ScopeReport, ID: report.ID})
			}
			goto resolved
		}
		return nil, fmt.Errorf("filter %q option dependency %q has no binding available in %s scope", id, dependency.ID, current.Scope)
	resolved:
	}
	return refs, nil
}

func canonicalBindingTargetPoliciesOverlap(left, right dashboardfilter.Binding, pages []dashboard.Page) bool {
	if len(left.TargetPolicy.Include) == 0 || len(right.TargetPolicy.Include) == 0 {
		return true
	}
	leftConsumers := canonicalAuthoredTargetConsumers(left, pages)
	for consumer := range canonicalAuthoredTargetConsumers(right, pages) {
		if _, exists := leftConsumers[consumer]; exists {
			return true
		}
	}
	return false
}

func canonicalAuthoredTargetConsumers(binding dashboardfilter.Binding, pages []dashboard.Page) map[string]struct{} {
	consumers := map[string]struct{}{}
	for _, target := range binding.TargetPolicy.Include {
		if binding.Scope == dashboardfilter.ScopePage {
			consumers[binding.PageID+"/"+target] = struct{}{}
			continue
		}
		if strings.Contains(target, "/") {
			consumers[target] = struct{}{}
			continue
		}
		for _, page := range pages {
			for _, component := range page.Visuals {
				if component.Kind == "visual" && component.Visual == target {
					consumers[page.ID+"/"+component.ID] = struct{}{}
				}
			}
		}
	}
	return consumers
}

func canonicalTargetMatches(targets []string, visualID string) bool {
	for _, target := range targets {
		if target == visualID {
			return true
		}
	}
	return false
}

func canonicalBindingTargetMatches(binding dashboardfilter.Binding, pageID string, component dashboard.PageVisual) bool {
	for _, target := range binding.TargetPolicy.Include {
		if binding.Scope == dashboardfilter.ScopePage {
			if target == component.ID {
				return true
			}
			continue
		}
		if target == component.Visual || target == pageID+"/"+component.ID {
			return true
		}
	}
	return false
}

func canonicalTargetExists(target string, pages []dashboard.Page) bool {
	for _, page := range pages {
		for _, component := range page.Visuals {
			if component.Kind != "visual" {
				continue
			}
			if target == component.Visual || target == page.ID+"/"+component.ID {
				return true
			}
		}
	}
	return false
}

func canonicalTargetExistsOnPage(target string, page dashboard.Page) bool {
	for _, component := range page.Visuals {
		if component.Kind == "visual" && target == component.ID {
			return true
		}
	}
	return false
}

func canonicalComponentTargetIsUnique(pages []dashboard.Page, pageID, componentID string) bool {
	for _, page := range pages {
		if page.ID != pageID {
			continue
		}
		visualID := ""
		for _, component := range page.Visuals {
			if component.Kind == "visual" && component.ID == componentID {
				visualID = component.Visual
				break
			}
		}
		if visualID == "" {
			return false
		}
		placements := 0
		for _, component := range page.Visuals {
			if component.Kind == "visual" && component.Visual == visualID {
				placements++
			}
		}
		return placements == 1
	}
	return false
}

func canonicalDimensionApplies(dimension string, datasets []string, model *semanticmodel.Model) bool {
	semantic, ok := model.Dimensions[dimension]
	if !ok {
		return false
	}
	for _, dataset := range datasets {
		if _, ok := semantic.Bindings[dataset]; !ok {
			return false
		}
	}
	return len(datasets) > 0
}

func canonicalSemanticValueKind(dimension semanticmodel.SemanticDimension) (dashboardfilter.ValueKind, error) {
	switch dimension.Datatype {
	case semanticmodel.DataTypeString:
		return dashboardfilter.ValueString, nil
	case semanticmodel.DataTypeBoolean:
		return dashboardfilter.ValueBoolean, nil
	case semanticmodel.DataTypeInteger:
		return dashboardfilter.ValueInteger, nil
	case semanticmodel.DataTypeDecimal, semanticmodel.DataTypeFloat:
		return dashboardfilter.ValueDecimal, nil
	case semanticmodel.DataTypeDate:
		return dashboardfilter.ValueDate, nil
	case semanticmodel.DataTypeDateTime, semanticmodel.DataTypeDateTimeTZ:
		return dashboardfilter.ValueTimestamp, nil
	}
	switch strings.ToLower(dimension.Type) {
	case "string":
		return dashboardfilter.ValueString, nil
	case "boolean":
		return dashboardfilter.ValueBoolean, nil
	case "number":
		return dashboardfilter.ValueDecimal, nil
	case "date":
		return dashboardfilter.ValueDate, nil
	case "timestamp", "datetime":
		return dashboardfilter.ValueTimestamp, nil
	default:
		return "", fmt.Errorf("dimension has unsupported type %q", dimension.Type)
	}
}

func canonicalPredicates(controlType string, operators *[]document.DashboardFilterOperator, kind dashboardfilter.ValueKind) ([]dashboardfilter.PredicatePolicy, error) {
	allowed := map[string][]dashboardfilter.Operator{
		"singleSelect": {dashboardfilter.OperatorIn, dashboardfilter.OperatorNotIn},
		"multiSelect":  {dashboardfilter.OperatorIn, dashboardfilter.OperatorNotIn},
		"text":         {dashboardfilter.OperatorEquals, dashboardfilter.OperatorNotEquals, dashboardfilter.OperatorContains, dashboardfilter.OperatorNotContains, dashboardfilter.OperatorStartsWith, dashboardfilter.OperatorEndsWith},
		"numericRange": nil, "dateRange": nil, "relativePeriod": nil,
	}[controlType]
	if _, known := map[string]bool{"singleSelect": true, "multiSelect": true, "text": true, "numericRange": true, "dateRange": true, "relativePeriod": true}[controlType]; !known {
		return nil, fmt.Errorf("unsupported control type %q", controlType)
	}
	if controlType == "numericRange" {
		if operators != nil && len(*operators) > 0 {
			return nil, fmt.Errorf("numericRange does not accept operators")
		}
		if kind != dashboardfilter.ValueInteger && kind != dashboardfilter.ValueDecimal {
			return nil, fmt.Errorf("numericRange requires an integer or decimal dimension")
		}
		return []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionRange}}, nil
	}
	if controlType == "dateRange" {
		if operators != nil && len(*operators) > 0 {
			return nil, fmt.Errorf("dateRange does not accept operators")
		}
		if kind != dashboardfilter.ValueDate && kind != dashboardfilter.ValueTimestamp {
			return nil, fmt.Errorf("dateRange requires a date or timestamp dimension")
		}
		return []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionRange}}, nil
	}
	if controlType == "relativePeriod" {
		if operators != nil && len(*operators) > 0 {
			return nil, fmt.Errorf("relativePeriod does not accept operators")
		}
		if kind != dashboardfilter.ValueDate && kind != dashboardfilter.ValueTimestamp {
			return nil, fmt.Errorf("relativePeriod requires a date or timestamp dimension")
		}
		return []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionRelativePeriod}}, nil
	}
	if operators != nil {
		if len(*operators) == 0 {
			return nil, fmt.Errorf("operators cannot be empty")
		}
		allowedSet := map[dashboardfilter.Operator]struct{}{}
		for _, operator := range allowed {
			allowedSet[operator] = struct{}{}
		}
		seen := map[dashboardfilter.Operator]struct{}{}
		allowed = allowed[:0]
		for _, authored := range *operators {
			converted, err := canonicalOperator(authored)
			if err != nil {
				return nil, err
			}
			if _, ok := allowedSet[converted]; !ok {
				return nil, fmt.Errorf("operator %q is incompatible with %s control", authored, controlType)
			}
			if _, duplicate := seen[converted]; duplicate {
				return nil, fmt.Errorf("duplicate operator %q", authored)
			}
			seen[converted] = struct{}{}
			allowed = append(allowed, converted)
		}
	}
	return []dashboardfilter.PredicatePolicy{{Kind: map[string]dashboardfilter.ExpressionKind{"singleSelect": dashboardfilter.ExpressionSet, "multiSelect": dashboardfilter.ExpressionSet, "text": dashboardfilter.ExpressionComparison}[controlType], Operators: allowed}}, nil
}

func canonicalOperator(value document.DashboardFilterOperator) (dashboardfilter.Operator, error) {
	if value == "" {
		return "", fmt.Errorf("operator is required")
	}
	operators := map[document.DashboardFilterOperator]dashboardfilter.Operator{
		document.DashboardFilterOperatorIsNull:             dashboardfilter.OperatorIsNull,
		document.DashboardFilterOperatorIsNotNull:          dashboardfilter.OperatorIsNotNull,
		document.DashboardFilterOperatorIn:                 dashboardfilter.OperatorIn,
		document.DashboardFilterOperatorNotIn:              dashboardfilter.OperatorNotIn,
		document.DashboardFilterOperatorEquals:             dashboardfilter.OperatorEquals,
		document.DashboardFilterOperatorNotEquals:          dashboardfilter.OperatorNotEquals,
		document.DashboardFilterOperatorContains:           dashboardfilter.OperatorContains,
		document.DashboardFilterOperatorNotContains:        dashboardfilter.OperatorNotContains,
		document.DashboardFilterOperatorStartsWith:         dashboardfilter.OperatorStartsWith,
		document.DashboardFilterOperatorEndsWith:           dashboardfilter.OperatorEndsWith,
		document.DashboardFilterOperatorGreaterThan:        dashboardfilter.OperatorGreaterThan,
		document.DashboardFilterOperatorGreaterThanOrEqual: dashboardfilter.OperatorGreaterThanOrEqual,
		document.DashboardFilterOperatorLessThan:           dashboardfilter.OperatorLessThan,
		document.DashboardFilterOperatorLessThanOrEqual:    dashboardfilter.OperatorLessThanOrEqual,
	}
	if converted, ok := operators[value]; ok {
		return converted, nil
	}
	return "", fmt.Errorf("unsupported operator %q", value)
}

func canonicalExpression(expression document.DashboardFilterExpression, kind dashboardfilter.ValueKind) (dashboardfilter.Expression, error) {
	typ, err := expression.Type()
	if err != nil {
		return dashboardfilter.Expression{}, err
	}
	result := dashboardfilter.Expression{}
	switch value := expression.Value.(type) {
	case *document.UnfilteredDashboardFilterExpression:
		result.Kind = dashboardfilter.ExpressionUnfiltered
	case *document.NullCheckDashboardFilterExpression:
		result.Kind, result.Operator = dashboardfilter.ExpressionNullCheck, mustCanonicalOperator(value.Operator)
	case *document.SetDashboardFilterExpression:
		result.Kind, result.Operator = dashboardfilter.ExpressionSet, mustCanonicalOperator(value.Operator)
		for index, item := range value.Values {
			typed, err := canonicalFilterValue(item, kind)
			if err != nil {
				return dashboardfilter.Expression{}, fmt.Errorf("set value %d: %w", index, err)
			}
			result.Values = append(result.Values, typed)
		}
	case *document.ComparisonDashboardFilterExpression:
		result.Kind, result.Operator = dashboardfilter.ExpressionComparison, mustCanonicalOperator(value.Operator)
		typed, err := canonicalFilterValue(value.Value, kind)
		if err != nil {
			return dashboardfilter.Expression{}, err
		}
		result.Value = &typed
	case *document.RangeDashboardFilterExpression:
		result.Kind = dashboardfilter.ExpressionRange
		if value.Lower != nil {
			typed, err := canonicalFilterValue(value.Lower.Value, kind)
			if err != nil {
				return dashboardfilter.Expression{}, err
			}
			result.Lower = &dashboardfilter.Bound{Value: typed, Inclusive: value.Lower.Inclusive}
		}
		if value.Upper != nil {
			typed, err := canonicalFilterValue(value.Upper.Value, kind)
			if err != nil {
				return dashboardfilter.Expression{}, err
			}
			result.Upper = &dashboardfilter.Bound{Value: typed, Inclusive: value.Upper.Inclusive}
		}
	case *document.RelativePeriodDashboardFilterExpression:
		result.Kind, result.Direction, result.Count, result.IncludeCurrent = dashboardfilter.ExpressionRelativePeriod, dashboardfilter.RelativeDirection(value.Direction), int(value.Count), value.IncludeCurrent
		anchor, err := canonicalRelativeAnchor(value.Anchor)
		if err != nil {
			return dashboardfilter.Expression{}, err
		}
		result.Unit, result.Anchor = dashboardfilter.RelativeUnit(value.Unit), anchor
		if value.AnchorValue != nil {
			typed, err := canonicalFilterValue(*value.AnchorValue, kind)
			if err != nil {
				return dashboardfilter.Expression{}, err
			}
			result.AnchorValue = &typed
		}
	default:
		return dashboardfilter.Expression{}, fmt.Errorf("unsupported expression type %q", typ)
	}
	return dashboardfilter.Canonicalize(result, kind)
}

func canonicalRelativeAnchor(value document.DashboardRelativeAnchor) (dashboardfilter.RelativeAnchor, error) {
	anchors := map[document.DashboardRelativeAnchor]dashboardfilter.RelativeAnchor{
		document.DashboardRelativeAnchorCurrentTime:    dashboardfilter.AnchorCurrentTime,
		document.DashboardRelativeAnchorFirstAvailable: dashboardfilter.AnchorFirstAvailable,
		document.DashboardRelativeAnchorLastAvailable:  dashboardfilter.AnchorLastAvailable,
		document.DashboardRelativeAnchorFixed:          dashboardfilter.AnchorFixed,
	}
	if anchor, ok := anchors[value]; ok {
		return anchor, nil
	}
	return "", fmt.Errorf("unsupported relative period anchor %q", value)
}

func mustCanonicalOperator(value document.DashboardFilterOperator) dashboardfilter.Operator {
	converted, _ := canonicalOperator(value)
	return converted
}

func canonicalFilterValue(value document.DashboardFilterValue, kind dashboardfilter.ValueKind) (dashboardfilter.Value, error) {
	var typed dashboardfilter.Value
	switch item := value.Value.(type) {
	case *document.StringDashboardFilterValue:
		typed = dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: item.Value}
	case *document.BooleanDashboardFilterValue:
		typed = dashboardfilter.Value{Kind: dashboardfilter.ValueBoolean, Value: item.Value}
	case *document.IntegerDashboardFilterValue:
		typed = dashboardfilter.Value{Kind: dashboardfilter.ValueInteger, Value: item.Value}
	case *document.DecimalDashboardFilterValue:
		typed = dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: item.Value}
	case *document.DateDashboardFilterValue:
		typed = dashboardfilter.Value{Kind: dashboardfilter.ValueDate, Value: item.Value}
	case *document.TimestampDashboardFilterValue:
		typed = dashboardfilter.Value{Kind: dashboardfilter.ValueTimestamp, Value: item.Value}
	default:
		return dashboardfilter.Value{}, fmt.Errorf("filter value is required")
	}
	canonical, err := dashboardfilter.Canonicalize(dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals, Value: &typed}, kind)
	if err != nil {
		return dashboardfilter.Value{}, err
	}
	return *canonical.Value, nil
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateCanonicalURLParameter(param string) error {
	if param == "" {
		return fmt.Errorf("must not be empty")
	}
	if !canonicalURLParameterPattern.MatchString(param) {
		return fmt.Errorf("%q is not a valid URL parameter name", param)
	}
	reserved := map[string]struct{}{"dashboard": {}, "page": {}, "project": {}, "updates": {}, "stream": {}, "csrf": {}, "token": {}, "filters": {}, "filter": {}, "view": {}, "embed": {}}
	if _, ok := reserved[strings.ToLower(param)]; ok {
		return fmt.Errorf("%q is reserved", param)
	}
	return nil
}
