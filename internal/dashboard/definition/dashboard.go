// Package definition owns immutable compiler-produced dashboard serving state.
package definition

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type Definition struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Description   string `json:"description,omitempty"`
	SemanticModel string `json:"semanticModel"`
	// Layout is populated by the canonical Dashboard compiler. Legacy
	// authoring callers may leave it nil while LEA-426 wires the generated
	// document into the project loader.
	Layout            *Layout                                       `json:"layout,omitempty"`
	FilterDefinitions map[string]dashboardfilter.Definition         `json:"filterDefinitions,omitempty"`
	FilterBindings    map[string]dashboardfilter.Binding            `json:"filterBindings,omitempty"`
	FilterApplication dashboardfilter.ApplicationPolicy             `json:"filterApplication,omitempty"`
	Pages             []dashboard.Page                              `json:"pages"`
	Visualizations    map[string]visualizationdefinition.Definition `json:"visualizations"`
}

func New(id, title, description, semanticModel string, pages []dashboard.Page, visualizations map[string]visualizationdefinition.Definition) (Definition, error) {
	if id == "" || semanticModel == "" {
		return Definition{}, fmt.Errorf("compiled dashboard requires ID and semantic model")
	}
	return Definition{ID: id, Title: title, Description: description, SemanticModel: semanticModel, Pages: append([]dashboard.Page(nil), pages...), Visualizations: cloneVisualizations(visualizations)}, nil
}

func (definition Definition) DefaultFilters() dashboard.Filters {
	filters := dashboard.Filters{}.WithDefaults()
	state := definition.DefaultFilterState()
	filters.CompiledState = &state
	return filters
}

func (definition Definition) CompiledFilterBindings() map[string]dashboardfilter.Binding {
	out := make(map[string]dashboardfilter.Binding, len(definition.FilterBindings))
	for _, binding := range definition.FilterBindings {
		out[binding.Key] = binding
	}
	for _, page := range definition.Pages {
		for _, binding := range page.FilterBindings {
			out[binding.Key] = binding
		}
	}
	return out
}

func (definition Definition) DefaultFilterState() dashboardfilter.State {
	return dashboardfilter.NewMachine(definition.FilterApplication.WithDefaults().Mode, definition.FilterBindingSpecs()).State()
}

func (definition Definition) FilterBindingSpecs() map[string]dashboardfilter.BindingSpec {
	specs := make(map[string]dashboardfilter.BindingSpec)
	for key, binding := range definition.CompiledFilterBindings() {
		filterDefinition := definition.FilterDefinitions[binding.Filter]
		specs[key] = dashboardfilter.BindingSpec{
			ValueKind:  filterDefinition.ValueKind,
			Default:    binding.Default,
			Selection:  binding.Selection,
			Editable:   binding.Editable(),
			Time:       filterDefinition.Time,
			Predicates: filterDefinition.Predicates,
		}
	}
	return specs
}

func (definition Definition) FilterStateFromURL(pageID string, values url.Values) (dashboardfilter.State, error) {
	specs := definition.FilterBindingSpecs()
	machine := dashboardfilter.NewMachine(definition.FilterApplication.WithDefaults().Mode, specs)
	bindings := definition.CompiledFilterBindings()
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		binding := bindings[key]
		if binding.URL.Param == "" || !bindingAvailableOnPage(binding, pageID) {
			continue
		}
		encoded := values.Get(binding.URL.Param)
		if encoded == "" {
			continue
		}
		expression, err := dashboardfilter.DecodeTypedV1(encoded, specs[key].ValueKind)
		if err != nil {
			return machine.State(), fmt.Errorf("URL parameter %q: %w", binding.URL.Param, err)
		}
		state := machine.State()
		if _, err := machine.Execute(dashboardfilter.Command{
			Kind: dashboardfilter.CommandMutate, BaseRevision: state.Revision,
			ClientMutationID: "url:" + key, BindingKey: key,
			Operation: dashboardfilter.MutationSet, Expression: &expression,
		}); err != nil {
			return machine.State(), fmt.Errorf("URL parameter %q: %w", binding.URL.Param, err)
		}
	}
	return machine.State(), nil
}

func (definition Definition) URLParamsFromFilterState(pageID string, state dashboardfilter.State) (url.Values, error) {
	params := url.Values{}
	bindings := definition.CompiledFilterBindings()
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		binding := bindings[key]
		if binding.URL.Param == "" || !bindingAvailableOnPage(binding, pageID) {
			continue
		}
		applied, ok := state.AppliedControls[key]
		if !ok || expressionsEqual(applied.Expression, binding.Default) {
			continue
		}
		filterDefinition := definition.FilterDefinitions[binding.Filter]
		encoded, err := dashboardfilter.EncodeTypedV1(applied.Expression, filterDefinition.ValueKind)
		if err != nil {
			return nil, fmt.Errorf("encode binding %q URL: %w", binding.ID, err)
		}
		params.Set(binding.URL.Param, encoded)
	}
	return params, nil
}

func bindingAvailableOnPage(binding dashboardfilter.Binding, pageID string) bool {
	return binding.Scope == dashboardfilter.ScopeReport || binding.PageID == pageID
}

func expressionsEqual(left, right dashboardfilter.Expression) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func (definition Definition) NormalizeFiltersForPage(pageID string, filters dashboard.Filters) dashboard.Filters {
	filters, next := filters.WithDefaults(), dashboard.Filters{}.WithDefaults()
	next.Selections = append([]dashboard.InteractionSelection(nil), filters.Selections...)
	next.SpatialSelections = append([]dashboard.SpatialInteractionSelection(nil), filters.SpatialSelections...)
	if filters.CompiledState != nil {
		state := dashboardfilter.CloneState(*filters.CompiledState)
		next.CompiledState = &state
	} else {
		state := definition.DefaultFilterState()
		next.CompiledState = &state
	}
	next.ServingStateID = filters.ServingStateID
	next.InteractionRevision = filters.InteractionRevision
	next.DataRevisions = cloneDataRevisions(filters.DataRevisions)
	next.ActivePageID = pageID
	return next
}

func cloneDataRevisions(revisions map[string]int64) map[string]int64 {
	if revisions == nil {
		return nil
	}
	next := make(map[string]int64, len(revisions))
	for visualID, revision := range revisions {
		next[visualID] = revision
	}
	return next
}

func (definition Definition) DefaultFiltersForPage(pageID string) dashboard.Filters {
	return definition.NormalizeFiltersForPage(pageID, definition.DefaultFilters())
}
func (definition Definition) PageOrDefault(pageID string) (dashboard.Page, bool) {
	if len(definition.Pages) == 0 {
		return dashboard.Page{}, false
	}
	if pageID != "" {
		for _, page := range definition.Pages {
			if page.ID == pageID {
				return page, true
			}
		}
	}
	return definition.Pages[0], true
}
func (definition Definition) FiltersFromURLForPage(pageID string, values url.Values) dashboard.Filters {
	state, err := definition.FilterStateFromURL(pageID, values)
	if err != nil {
		return definition.DefaultFiltersForPage(pageID)
	}
	filters := dashboard.Filters{}.WithDefaults()
	filters.CompiledState = &state
	filters.ActivePageID = pageID
	return filters
}

func (definition Definition) URLParamsFromFiltersForPage(pageID string, filters dashboard.Filters) map[string]any {
	state := definition.DefaultFilterState()
	if filters.CompiledState != nil {
		state = *filters.CompiledState
	}
	typed, err := definition.URLParamsFromFilterState(pageID, state)
	if err != nil {
		return map[string]any{}
	}
	params := make(map[string]any, len(typed))
	for key, values := range typed {
		if len(values) == 1 {
			params[key] = values[0]
		} else if len(values) > 1 {
			params[key] = append([]string(nil), values...)
		}
	}
	return params
}

func SpecTitle(spec ir.VisualizationSpec) string {
	base, _ := ir.SpecificationBase(spec)
	return base.Title
}

func TableColumns(spec ir.VisualizationSpec) []dashboard.TableColumn {
	var columns []ir.TableVisualizationColumn
	if value, ok := spec.Value.(*ir.TableVisualizationSpec); ok {
		columns = value.Columns
	} else if base, err := ir.SpecificationBase(spec); err == nil && len(base.Datasets) > 0 {
		for _, field := range base.Datasets[0].Fields {
			columns = append(columns, ir.TableVisualizationColumn{Field: ir.VisualizationFieldRef{Dataset: "primary", Field: field.ID}, Label: field.Label})
		}
	}
	base, _ := ir.SpecificationBase(spec)
	fields := map[string]ir.VisualizationField{}
	if len(base.Datasets) > 0 {
		for _, field := range base.Datasets[0].Fields {
			fields[field.ID] = field
		}
	}
	out := make([]dashboard.TableColumn, len(columns))
	for index, column := range columns {
		out[index] = dashboard.TableColumn{Key: column.Field.Field, Label: column.Label, Formatting: formatting(column.Formatting)}
		if column.Width != nil {
			out[index].Width = int(*column.Width)
		}
		if column.Group != nil {
			out[index].Group = *column.Group
		}
		if column.Metric != nil {
			out[index].Metric = *column.Metric
		}
		if column.ColumnValue != nil {
			out[index].ColumnValue = *column.ColumnValue
		}
		if field, ok := fields[column.Field.Field]; ok {
			if field.Role == ir.VisualizationFieldRoleMetric {
				out[index].Role, out[index].Align = "metric", "right"
			} else {
				out[index].Role = "row_header"
			}
			out[index].Format = fieldFormat(field)
		}
	}
	return out
}

func MetricFormatting(spec ir.VisualizationSpec, metrics []visualizationdefinition.FieldBinding) map[string][]dashboard.TableFormattingRule {
	values := map[string][]ir.TableVisualizationFormattingRule{}
	switch value := spec.Value.(type) {
	case *ir.MatrixVisualizationSpec:
		values = value.MetricFormatting
	case *ir.PivotVisualizationSpec:
		values = value.MetricFormatting
	}
	out := make(map[string][]dashboard.TableFormattingRule, len(values))
	for _, metric := range metrics {
		if rules := values[metric.Alias]; len(rules) > 0 {
			out[metric.FieldID] = formatting(rules)
		}
	}
	return out
}

func formatting(values []ir.TableVisualizationFormattingRule) []dashboard.TableFormattingRule {
	out := make([]dashboard.TableFormattingRule, 0, len(values))
	for _, value := range values {
		switch rule := value.Value.(type) {
		case *ir.TableBadgeFormattingRule:
			out = append(out, dashboard.TableFormattingRule{Kind: rule.Kind, Values: cloneStringMap(rule.Values)})
		case *ir.TableTextColorFormattingRule:
			item := dashboard.TableFormattingRule{Kind: rule.Kind, Color: rule.Color, Min: rule.Minimum, Max: rule.Maximum}
			if rule.Values != nil {
				item.Values = cloneStringMap(*rule.Values)
			}
			out = append(out, item)
		case *ir.TableBackgroundScaleFormattingRule:
			item := dashboard.TableFormattingRule{Kind: rule.Kind, Min: rule.Minimum, Max: rule.Maximum}
			if rule.LowColor != nil {
				item.LowColor = *rule.LowColor
			}
			if rule.HighColor != nil {
				item.HighColor = *rule.HighColor
			}
			out = append(out, item)
		case *ir.TableDataBarFormattingRule:
			item := dashboard.TableFormattingRule{Kind: rule.Kind, Min: rule.Minimum, Max: rule.Maximum, Color: rule.Color}
			if rule.Background != nil {
				item.Background = *rule.Background
			}
			out = append(out, item)
		}
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func fieldFormat(field ir.VisualizationField) string {
	if field.Format != nil {
		switch field.Format.Value.(type) {
		case *ir.CurrencyVisualizationFormat:
			return "currency"
		case *ir.DurationVisualizationFormat:
			return "days"
		case *ir.NumberVisualizationFormat:
			if field.DataType == ir.VisualizationDataTypeInteger {
				return "integer"
			}
			return "decimal"
		}
	}
	return "text"
}
func cloneVisualizations(values map[string]visualizationdefinition.Definition) map[string]visualizationdefinition.Definition {
	out := make(map[string]visualizationdefinition.Definition, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
