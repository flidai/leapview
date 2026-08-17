package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

var supportedAggregations = map[string]struct{}{
	"sum": {}, "count": {}, "count_distinct": {}, "avg": {}, "min": {}, "max": {},
}

var supportedSemanticDimensionTypes = map[string]struct{}{
	"string": {}, "number": {}, "boolean": {}, "date": {}, "timestamp": {}, "opaque": {},
}

var supportedTimeGrains = map[string]struct{}{
	"second": {}, "minute": {}, "hour": {}, "day": {}, "week": {}, "month": {}, "quarter": {}, "year": {},
}

var timeGrainOrder = map[string]int{
	"second": 0, "minute": 1, "hour": 2, "day": 3, "week": 4, "month": 5, "quarter": 6, "year": 7,
}

func containsTimeGrain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *Model) FactNames() []string {
	seen := map[string]struct{}{}
	for _, metric := range m.Metrics {
		if metric.Type == "aggregate" && metric.Dataset != "" {
			seen[metric.Dataset] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for fact := range seen {
		out = append(out, fact)
	}
	sort.Strings(out)
	return out
}

// ValidateSemanticGraph validates relationships, dimensions, filters, and
// metric definitions using only the semantic graph. Unlike ValidateAuthored,
// it does not require project sources or connection credentials and is used
// by semantic-model import adapters before project binding is available.
func (m *Model) ValidateSemanticGraph() error {
	if m == nil {
		return fmt.Errorf("semantic model is required")
	}
	return m.validateSemanticGraph()
}

func (m *Model) validateSemanticDefinitions() error {
	for name, filter := range m.Filters {
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("semantic filter %q is invalid: %w", name, err)
		}
		if err := m.validateSemanticFilterNode(name, filter); err != nil {
			return err
		}
	}
	facts := map[string]struct{}{}
	for _, fact := range m.FactNames() {
		facts[fact] = struct{}{}
	}
	for name, dimension := range m.Dimensions {
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("semantic dimension %q is invalid: %w", name, err)
		}
		dimension.Name = name
		dimension.Label = defaultString(dimension.Label, titleFromIdentifier(name))
		if dimension.Timezone == "" {
			dimension.Timezone = "UTC"
		}
		if dimension.Calendar == "" {
			dimension.Calendar = "gregorian"
		}
		if dimension.WeekStart == "" {
			if dimension.Calendar == "iso8601" {
				dimension.WeekStart = "monday"
			} else {
				dimension.WeekStart = "sunday"
			}
		}
		if dimension.Calendar == "iso8601" && dimension.WeekStart != "monday" {
			return fmt.Errorf("semantic dimension %q iso8601 calendar requires monday week boundary", name)
		}
		if _, err := time.LoadLocation(dimension.Timezone); err != nil {
			return fmt.Errorf("semantic dimension %q has invalid timezone %q", name, dimension.Timezone)
		}
		if dimension.Calendar != "gregorian" && dimension.Calendar != "iso8601" {
			return fmt.Errorf("semantic dimension %q has unsupported calendar %q", name, dimension.Calendar)
		}
		switch dimension.WeekStart {
		case "monday", "sunday":
		default:
			return fmt.Errorf("semantic dimension %q has unsupported week_start %q", name, dimension.WeekStart)
		}
		if _, ok := supportedSemanticDimensionTypes[dimension.Type]; !ok {
			return fmt.Errorf("semantic dimension %q has unsupported type %q", name, dimension.Type)
		}
		if dimension.NativeGrain != "" {
			if dimension.Type != "date" && dimension.Type != "timestamp" {
				return fmt.Errorf("semantic dimension %q defines native grain for type %q", name, dimension.Type)
			}
			if _, ok := timeGrainOrder[dimension.NativeGrain]; !ok {
				return fmt.Errorf("semantic dimension %q has unsupported native time grain %q", name, dimension.NativeGrain)
			}
		}
		if len(dimension.Grains) > 0 && dimension.Type != "date" && dimension.Type != "timestamp" {
			return fmt.Errorf("semantic dimension %q defines time grains for type %q", name, dimension.Type)
		}
		for _, grain := range dimension.Grains {
			if _, ok := supportedTimeGrains[grain]; !ok {
				return fmt.Errorf("semantic dimension %q has unsupported time grain %q", name, grain)
			}
		}
		if dimension.NativeGrain != "" {
			if !containsTimeGrain(dimension.Grains, dimension.NativeGrain) {
				return fmt.Errorf("semantic dimension %q native grain %q is not declared in grains", name, dimension.NativeGrain)
			}
			for _, grain := range dimension.Grains {
				if timeGrainOrder[grain] < timeGrainOrder[dimension.NativeGrain] {
					return fmt.Errorf("semantic dimension %q grain %q is finer than native grain %q", name, grain, dimension.NativeGrain)
				}
			}
		}
		if len(dimension.Bindings) == 0 {
			return fmt.Errorf("semantic dimension %q requires bindings", name)
		}
		for fact, binding := range dimension.Bindings {
			if _, ok := facts[fact]; !ok {
				return fmt.Errorf("semantic dimension %q binding references non-fact table %q", name, fact)
			}
			physical, err := m.ResolveDimension(binding.Field)
			if err != nil {
				return fmt.Errorf("semantic dimension %q binding for fact %q: %w", name, fact, err)
			}
			if !compatibleConformedBindingTypes(dimension, physical) {
				return fmt.Errorf("semantic dimension %q logical datatype %q is incompatible with binding %q logical datatype %q", name, dimension.Datatype, binding.Field, physical.Datatype)
			}
			if _, err := m.ResolveBindingPath(fact, binding); err != nil {
				return fmt.Errorf("semantic dimension %q binding for fact %q: %w", name, fact, err)
			}
		}
		m.Dimensions[name] = dimension
	}
	for datasetName, dataset := range m.Datasets {
		if _, ok := m.Tables[datasetName]; !ok {
			return fmt.Errorf("semantic dataset %q has no runtime table", datasetName)
		}
		if dataset.DefaultTimeDimension == "" {
			continue
		}
		dimension, ok := m.Dimensions[dataset.DefaultTimeDimension]
		if !ok {
			return fmt.Errorf("semantic dataset %q default time dimension %q is unknown", datasetName, dataset.DefaultTimeDimension)
		}
		if dimension.Type != "date" && dimension.Type != "timestamp" {
			return fmt.Errorf("semantic dataset %q default time dimension %q is not temporal", datasetName, dataset.DefaultTimeDimension)
		}
		if _, ok := dimension.Bindings[datasetName]; !ok {
			return fmt.Errorf("semantic dataset %q default time dimension %q has no binding", datasetName, dataset.DefaultTimeDimension)
		}
	}
	return m.validateMetrics()
}

func (m *Model) validateSemanticFilterNode(name string, filter SemanticFilterSpec) error {
	branches := 0
	if len(filter.All) > 0 {
		branches++
		for _, child := range filter.All {
			if err := m.validateSemanticFilterNode(name, child); err != nil {
				return err
			}
		}
	}
	if len(filter.Any) > 0 {
		branches++
		for _, child := range filter.Any {
			if err := m.validateSemanticFilterNode(name, child); err != nil {
				return err
			}
		}
	}
	if filter.Not != nil {
		branches++
		if err := m.validateSemanticFilterNode(name, *filter.Not); err != nil {
			return err
		}
	}
	if filter.Field != "" || filter.Operator != "" {
		branches++
		if filter.Field == "" || filter.Operator == "" {
			return fmt.Errorf("semantic filter %q leaf requires field and operator", name)
		}
		dimension, err := m.ResolveDimension(filter.Field)
		if err != nil {
			return fmt.Errorf("semantic filter %q: %w", name, err)
		}
		switch filter.Operator {
		case "equals", "not_equals", "less_than", "less_than_or_equal", "greater_than", "greater_than_or_equal":
			if filter.Value == nil {
				return fmt.Errorf("semantic filter %q operator %q requires a value", name, filter.Operator)
			}
		case "in", "not_in":
			values, ok := semanticFilterValues(filter.Value)
			if !ok || len(values) == 0 {
				return fmt.Errorf("semantic filter %q operator %q requires a non-empty value list", name, filter.Operator)
			}
			for _, value := range values {
				if _, err := CoerceSemanticLiteral(value, dimension); err != nil {
					return fmt.Errorf("semantic filter %q: %w", name, err)
				}
			}
		case "is_null", "is_not_null":
			if filter.Value != nil {
				return fmt.Errorf("semantic filter %q operator %q does not accept a value", name, filter.Operator)
			}
		default:
			return fmt.Errorf("semantic filter %q has unsupported operator %q", name, filter.Operator)
		}
		if filter.Operator != "is_null" && filter.Operator != "is_not_null" && filter.Operator != "in" && filter.Operator != "not_in" {
			if _, err := CoerceSemanticLiteral(filter.Value, dimension); err != nil {
				return fmt.Errorf("semantic filter %q: %w", name, err)
			}
		}
		for _, relationshipID := range filter.Path {
			if _, ok := m.RelationshipByID(relationshipID); !ok {
				return fmt.Errorf("semantic filter %q references unknown relationship path %q", name, relationshipID)
			}
		}
	}
	if branches != 1 {
		return fmt.Errorf("semantic filter %q must contain exactly one leaf or boolean node", name)
	}
	return nil
}

func semanticFilterValues(value any) ([]any, bool) {
	switch values := value.(type) {
	case []any:
		return values, true
	case []string:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []int:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []int64:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []float64:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	case []bool:
		out := make([]any, len(values))
		for index, value := range values {
			out[index] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func canonicalDimensionType(value string) string {
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

func compatibleDimensionTypes(canonical, physical string) bool {
	if canonical == physical {
		return true
	}
	return (canonical == "date" || canonical == "timestamp") && (physical == "date" || physical == "timestamp")
}

// compatibleConformedBindingTypes keeps the portable logical datatype exact
// for conformed dimensions. Legacy dimensions that omit datatype continue to
// use the existing broad category check, but once either side declares the
// logical contract, both sides must declare the same type. In particular,
// Date, DateTime, and DateTimeTz are not interchangeable timestamp aliases.
func compatibleConformedBindingTypes(dimension SemanticDimension, physical MetricDimension) bool {
	if dimension.Datatype != "" || physical.Datatype != "" {
		if dimension.Datatype == "" || physical.Datatype == "" {
			return false
		}
		if dimension.Datatype == physical.Datatype {
			return true
		}
		return false
	}
	physicalType := canonicalDimensionType(physical.Type)
	return physicalType == "" || compatibleDimensionTypes(dimension.Type, physicalType)
}

func (m *Model) validateMetrics() error {
	dependencies := map[string][]string{}
	for name, metric := range m.Metrics {
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("semantic metric %q is invalid: %w", name, err)
		}
		metric.Name = name
		metric.Label = defaultString(metric.Label, titleFromIdentifier(name))
		var refs []string
		switch metric.Type {
		case "aggregate":
			if _, ok := m.Tables[metric.Dataset]; !ok {
				return fmt.Errorf("semantic metric %q references unknown dataset %q", name, metric.Dataset)
			}
			if metric.Aggregation == "count" && metric.Input != nil && strings.TrimSpace(metric.Input.Field) != "" && strings.TrimSpace(metric.Input.Expression) != "" {
				return fmt.Errorf("semantic metric %q count requires at most one input", name)
			}
			if metric.Input == nil || strings.TrimSpace(metric.Input.Field) == "" {
				return fmt.Errorf("semantic metric %q aggregate input is required", name)
			}
			input, err := m.ResolveDimension(metric.Input.Field)
			if err != nil {
				return fmt.Errorf("semantic metric %q aggregate input: %w", name, err)
			}
			if input.Table != metric.Dataset {
				return fmt.Errorf("semantic metric %q aggregate input field %q is not owned by dataset %q", name, metric.Input.Field, metric.Dataset)
			}
			if metric.TimeDimension != "" {
				dimension, ok := m.Dimensions[metric.TimeDimension]
				if !ok {
					return fmt.Errorf("semantic metric %q time dimension %q is unknown", name, metric.TimeDimension)
				}
				if dimension.Type != "date" && dimension.Type != "timestamp" {
					return fmt.Errorf("semantic metric %q time dimension %q is not temporal", name, metric.TimeDimension)
				}
				if _, ok := dimension.Bindings[metric.Dataset]; !ok {
					return fmt.Errorf("semantic metric %q time dimension %q has no binding for dataset %q", name, metric.TimeDimension, metric.Dataset)
				}
			}
			for _, filter := range metric.Where {
				definition, ok := m.Filters[filter]
				if !ok {
					return fmt.Errorf("semantic metric %q references unknown semantic filter %q", name, filter)
				}
				if err := m.validateMetricFilterReachability(name, metric.Dataset, filter, definition); err != nil {
					return err
				}
			}
		case "derived":
			expression, err := ParseExpression(metric.Expression)
			if err != nil {
				return fmt.Errorf("semantic metric %q: %w", name, err)
			}
			refs = expression.References()
		case "ratio":
			refs = []string{metric.Numerator, metric.Denominator}
			if metric.Numerator == "" || metric.Denominator == "" {
				return fmt.Errorf("semantic metric %q ratio requires numerator and denominator", name)
			}
		default:
			return fmt.Errorf("semantic metric %q has unsupported type %q", name, metric.Type)
		}
		dependencies[name] = append([]string(nil), refs...)
		for _, ref := range refs {
			if _, ok := m.Metrics[ref]; !ok {
				return fmt.Errorf("semantic metric %q references unknown metric %q", name, ref)
			}
		}
		m.Metrics[name] = metric
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("semantic metric dependency cycle includes %q", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, ref := range dependencies[name] {
			if _, ok := dependencies[ref]; ok {
				if err := visit(ref); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		return nil
	}
	for name := range dependencies {
		if err := visit(name); err != nil {
			return err
		}
	}
	return m.validateMetricUnits()
}

// validateMetricFilterReachability proves every leaf in a named filter tree
// against the aggregate metric's root dataset. A leaf path is either omitted,
// in which case SafeRelationshipPath enforces the exactly-one-safe-route
// rule, or explicit, in which case ResolveBindingPath requires a complete safe
// route ending at the leaf's field table. The recursion keeps composed all/any/
// not filters subject to the same proof.
func (m *Model) validateMetricFilterReachability(metricName, root, filterName string, filter SemanticFilterSpec) error {
	if len(filter.All) > 0 {
		for index, child := range filter.All {
			if err := m.validateMetricFilterReachability(metricName, root, fmt.Sprintf("%s.all[%d]", filterName, index), child); err != nil {
				return err
			}
		}
	}
	if len(filter.Any) > 0 {
		for index, child := range filter.Any {
			if err := m.validateMetricFilterReachability(metricName, root, fmt.Sprintf("%s.any[%d]", filterName, index), child); err != nil {
				return err
			}
		}
	}
	if filter.Not != nil {
		if err := m.validateMetricFilterReachability(metricName, root, filterName+".not", *filter.Not); err != nil {
			return err
		}
	}
	if filter.Field == "" && filter.Operator == "" {
		return nil
	}
	dimension, err := m.ResolveDimension(filter.Field)
	if err != nil {
		return fmt.Errorf("semantic metric %q filter %q leaf: %w", metricName, filterName, err)
	}
	binding := DimensionBinding{Field: filter.Field, Path: append([]string(nil), filter.Path...)}
	if len(filter.Path) == 0 {
		if _, err := m.SafeRelationshipPath(root, dimension.Table); err != nil {
			return fmt.Errorf("semantic metric %q filter %q leaf: %w", metricName, filterName, err)
		}
	} else if _, err := m.ResolveBindingPath(root, binding); err != nil {
		return fmt.Errorf("semantic metric %q filter %q leaf: explicit path: %w", metricName, filterName, err)
	}
	return nil
}

func (m *Model) ResolveBindingPath(fact string, binding DimensionBinding) ([]Relationship, error) {
	dimension, err := m.ResolveDimension(binding.Field)
	if err != nil {
		return nil, err
	}
	if len(binding.Path) == 0 {
		return m.SafeRelationshipPath(fact, dimension.Table)
	}
	current := fact
	visited := map[string]struct{}{fact: {}}
	path := make([]Relationship, 0, len(binding.Path))
	for _, id := range binding.Path {
		relationship, ok := m.RelationshipByID(id)
		if !ok {
			return nil, fmt.Errorf("unknown relationship %q", id)
		}
		fromTable, _, _ := relationshipEndpoint(relationship, true)
		toTable, _, _ := relationshipEndpoint(relationship, false)
		switch {
		case current == fromTable:
			current = toTable
		case relationship.Cardinality == "one_to_one" && current == toTable:
			current = fromTable
		default:
			return nil, fmt.Errorf("relationship %q does not safely continue from %q", id, current)
		}
		if _, exists := visited[current]; exists {
			return nil, fmt.Errorf("relationship path revisits dataset %q", current)
		}
		visited[current] = struct{}{}
		path = append(path, relationship)
	}
	if current != dimension.Table {
		return nil, fmt.Errorf("relationship path ends at %q, want %q", current, dimension.Table)
	}
	return path, nil
}

func (m *Model) RelationshipByID(id string) (Relationship, bool) {
	for _, relationship := range m.Relationships {
		if relationship.ID == id {
			return relationship, true
		}
	}
	return Relationship{}, false
}
