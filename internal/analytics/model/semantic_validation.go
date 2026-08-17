package model

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

var supportedAggregations = map[string]struct{}{
	"sum": {}, "count": {}, "count_distinct": {}, "avg": {}, "min": {}, "max": {},
}

var supportedLogicalDataTypes = map[LogicalDataType]struct{}{
	DataTypeString: {}, DataTypeInteger: {}, DataTypeDecimal: {}, DataTypeFloat: {},
	DataTypeBoolean: {}, DataTypeDate: {}, DataTypeTime: {}, DataTypeDateTime: {},
	DataTypeDateTimeTZ: {}, DataTypeOpaque: {},
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

func validateLogicalDataType(scope string, datatype LogicalDataType) error {
	if datatype == "" {
		return fmt.Errorf("%s requires a logical datatype", scope)
	}
	if _, ok := supportedLogicalDataTypes[datatype]; !ok {
		return fmt.Errorf("%s has unsupported datatype %q", scope, datatype)
	}
	return nil
}

func semanticDimensionTypeForDatatype(datatype LogicalDataType) string {
	switch datatype {
	case DataTypeString:
		return "string"
	case DataTypeInteger, DataTypeDecimal, DataTypeFloat:
		return "number"
	case DataTypeBoolean:
		return "boolean"
	case DataTypeDate:
		return "date"
	case DataTypeTime, DataTypeDateTime, DataTypeDateTimeTZ:
		return "timestamp"
	case DataTypeOpaque:
		return "opaque"
	default:
		return ""
	}
}

func validateMetricInputDatatype(name, aggregation string, input MetricDimension) error {
	if input.Datatype == "" {
		return fmt.Errorf("semantic metric %q input requires a logical datatype", name)
	}
	if aggregation == "sum" || aggregation == "avg" {
		switch input.Datatype {
		case DataTypeInteger, DataTypeDecimal, DataTypeFloat:
			return nil
		default:
			return fmt.Errorf("semantic metric %q %s input has unsupported datatype %q", name, aggregation, input.Datatype)
		}
	}
	if aggregation == "min" || aggregation == "max" {
		if input.Datatype == DataTypeOpaque {
			return fmt.Errorf("semantic metric %q %s input has unsupported datatype %q", name, aggregation, input.Datatype)
		}
	}
	return nil
}

func containsTimeGrain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *Model) DatasetNames() []string {
	seen := map[string]struct{}{}
	for _, metric := range m.Metrics {
		if metric.Type == "aggregate" && metric.Dataset != "" {
			seen[metric.Dataset] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for dataset := range seen {
		out = append(out, dataset)
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
	for _, tableName := range m.TableNames() {
		table := m.Tables[tableName]
		fieldNames := make([]string, 0, len(table.Dimensions))
		for field := range table.Dimensions {
			fieldNames = append(fieldNames, field)
		}
		sort.Strings(fieldNames)
		for _, field := range fieldNames {
			dimension := table.Dimensions[field]
			if err := validateLogicalDataType("model table "+tableName+" field "+field, dimension.Datatype); err != nil {
				return err
			}
		}
		columnNames := make([]string, 0, len(table.Columns))
		for field := range table.Columns {
			columnNames = append(columnNames, field)
		}
		sort.Strings(columnNames)
		for _, field := range columnNames {
			column := table.Columns[field]
			if err := validateLogicalDataType("model table "+tableName+" column "+field, column.Datatype); err != nil {
				return err
			}
		}
	}
	filterNames := make([]string, 0, len(m.Filters))
	for name := range m.Filters {
		filterNames = append(filterNames, name)
	}
	sort.Strings(filterNames)
	for _, name := range filterNames {
		filter := m.Filters[name]
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("semantic filter %q is invalid: %w", name, err)
		}
		if err := m.validateSemanticFilterNode(name, filter); err != nil {
			return err
		}
	}
	datasets := map[string]struct{}{}
	for _, dataset := range m.DatasetNames() {
		datasets[dataset] = struct{}{}
	}
	dimensionNames := make([]string, 0, len(m.Dimensions))
	for name := range m.Dimensions {
		dimensionNames = append(dimensionNames, name)
	}
	sort.Strings(dimensionNames)
	for _, name := range dimensionNames {
		dimension := m.Dimensions[name]
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("semantic dimension %q is invalid: %w", name, err)
		}
		if err := validateLogicalDataType("semantic dimension "+name, dimension.Datatype); err != nil {
			return err
		}
		if dimension.Datatype != "" {
			canonicalType := semanticDimensionTypeForDatatype(dimension.Datatype)
			if dimension.Type == "" {
				dimension.Type = canonicalType
			} else if canonicalType != "" && dimension.Type != canonicalType {
				return fmt.Errorf("semantic dimension %q type %q disagrees with logical datatype %q", name, dimension.Type, dimension.Datatype)
			}
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
		bindingDatasets := make([]string, 0, len(dimension.Bindings))
		for dataset := range dimension.Bindings {
			bindingDatasets = append(bindingDatasets, dataset)
		}
		sort.Strings(bindingDatasets)
		for _, dataset := range bindingDatasets {
			binding := dimension.Bindings[dataset]
			if err := validateSemanticIdentifier(dataset); err != nil {
				return fmt.Errorf("semantic dimension %q binding dataset %q is invalid: %w", name, dataset, err)
			}
			if _, ok := datasets[dataset]; !ok {
				return fmt.Errorf("semantic dimension %q binding references non-dataset table %q", name, dataset)
			}
			physical, err := m.ResolveDimension(binding.Field)
			if err != nil {
				return fmt.Errorf("semantic dimension %q binding for dataset %q: %w", name, dataset, err)
			}
			if !compatibleConformedBindingTypes(dimension, physical) {
				return fmt.Errorf("semantic dimension %q logical datatype %q is incompatible with binding %q logical datatype %q", name, dimension.Datatype, binding.Field, physical.Datatype)
			}
			if _, err := m.ResolveBindingPath(dataset, binding); err != nil {
				return fmt.Errorf("semantic dimension %q binding for dataset %q: %w", name, dataset, err)
			}
		}
		m.Dimensions[name] = dimension
	}
	datasetNames := make([]string, 0, len(m.Datasets))
	for datasetName := range m.Datasets {
		datasetNames = append(datasetNames, datasetName)
	}
	sort.Strings(datasetNames)
	for _, datasetName := range datasetNames {
		dataset := m.Datasets[datasetName]
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
	if filter.All != nil {
		branches++
		if len(filter.All) == 0 {
			return fmt.Errorf("semantic filter %q all node requires a non-empty child list", name)
		}
		for _, child := range filter.All {
			if err := m.validateSemanticFilterNode(name, child); err != nil {
				return err
			}
		}
	}
	if filter.Any != nil {
		branches++
		if len(filter.Any) == 0 {
			return fmt.Errorf("semantic filter %q any node requires a non-empty child list", name)
		}
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
	if (filter.All != nil || filter.Any != nil || filter.Not != nil) &&
		(filter.Field != "" || filter.Operator != "" || filter.Value != nil || len(filter.Path) > 0 || filter.AIContext != nil) {
		return fmt.Errorf("semantic filter %q boolean node cannot contain leaf fields", name)
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
			if filter.Value == nil || isNilSemanticLiteral(filter.Value) {
				return fmt.Errorf("semantic filter %q operator %q requires a value", name, filter.Operator)
			}
		case "in", "not_in":
			values, ok := semanticFilterValues(filter.Value)
			if !ok || len(values) == 0 {
				return fmt.Errorf("semantic filter %q operator %q requires a non-empty value list", name, filter.Operator)
			}
			for _, value := range values {
				if isNilSemanticLiteral(value) {
					return fmt.Errorf("semantic filter %q operator %q prohibits null values", name, filter.Operator)
				}
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
			if err := validateSemanticIdentifier(relationshipID); err != nil {
				return fmt.Errorf("semantic filter %q relationship path id %q is invalid: %w", name, relationshipID, err)
			}
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

func isNilSemanticLiteral(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
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

// compatibleConformedBindingTypes requires the portable logical datatype to
// match exactly across every dataset binding of a conformed dimension.
func compatibleConformedBindingTypes(dimension SemanticDimension, physical MetricDimension) bool {
	return dimension.Datatype != "" && physical.Datatype != "" && dimension.Datatype == physical.Datatype
}

func (m *Model) validateMetrics() error {
	dependencies := map[string][]string{}
	metricNames := make([]string, 0, len(m.Metrics))
	for name := range m.Metrics {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	for _, name := range metricNames {
		metric := m.Metrics[name]
		if err := validateSemanticIdentifier(name); err != nil {
			return fmt.Errorf("semantic metric %q is invalid: %w", name, err)
		}
		metric.Name = name
		metric.Label = defaultString(metric.Label, titleFromIdentifier(name))
		var refs []string
		switch metric.Type {
		case "aggregate":
			if metric.Expression != "" || metric.Numerator != "" || metric.Denominator != "" {
				return fmt.Errorf("semantic metric %q aggregate does not accept derived or ratio fields", name)
			}
			if metric.Dataset == "" {
				return fmt.Errorf("semantic metric %q aggregate dataset is required", name)
			}
			if err := validateSemanticIdentifier(metric.Dataset); err != nil {
				return fmt.Errorf("semantic metric %q aggregate dataset %q is invalid: %w", name, metric.Dataset, err)
			}
			if _, ok := supportedAggregations[metric.Aggregation]; !ok {
				return fmt.Errorf("semantic metric %q has unsupported aggregation %q", name, metric.Aggregation)
			}
			if metric.Empty == "" {
				metric.Empty = defaultMetricEmpty(metric.Aggregation)
			}
			if metric.Empty != "zero" && metric.Empty != "null" {
				return fmt.Errorf("semantic metric %q has unsupported empty value %q", name, metric.Empty)
			}
			if metric.Where != nil && len(metric.Where) == 0 {
				return fmt.Errorf("semantic metric %q aggregate where requires a non-empty list", name)
			}
			if _, ok := m.Tables[metric.Dataset]; !ok {
				return fmt.Errorf("semantic metric %q references unknown dataset %q", name, metric.Dataset)
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
			if err := validateMetricInputDatatype(name, metric.Aggregation, input); err != nil {
				return err
			}
			if metric.TimeDimension != "" {
				if err := validateSemanticIdentifier(metric.TimeDimension); err != nil {
					return fmt.Errorf("semantic metric %q time dimension %q is invalid: %w", name, metric.TimeDimension, err)
				}
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
				if err := validateSemanticIdentifier(filter); err != nil {
					return fmt.Errorf("semantic metric %q where filter %q is invalid: %w", name, filter, err)
				}
				definition, ok := m.Filters[filter]
				if !ok {
					return fmt.Errorf("semantic metric %q references unknown semantic filter %q", name, filter)
				}
				if err := m.validateMetricFilterReachability(name, metric.Dataset, filter, definition); err != nil {
					return err
				}
			}
			seenFilters := map[string]struct{}{}
			for _, filter := range metric.Where {
				if _, exists := seenFilters[filter]; exists {
					return fmt.Errorf("semantic metric %q where contains duplicate filter %q", name, filter)
				}
				seenFilters[filter] = struct{}{}
			}
		case "derived":
			if metric.Dataset != "" || metric.Aggregation != "" || metric.Input != nil || metric.Where != nil || metric.Empty != "" || metric.TimeDimension != "" || metric.Numerator != "" || metric.Denominator != "" {
				return fmt.Errorf("semantic metric %q derived does not accept aggregate or ratio fields", name)
			}
			expression, err := ParseExpression(metric.Expression)
			if err != nil {
				return fmt.Errorf("semantic metric %q: %w", name, err)
			}
			refs = expression.References()
		case "ratio":
			if metric.Dataset != "" || metric.Aggregation != "" || metric.Input != nil || metric.Where != nil || metric.Empty != "" || metric.TimeDimension != "" || metric.Expression != "" {
				return fmt.Errorf("semantic metric %q ratio does not accept aggregate or derived fields", name)
			}
			refs = []string{metric.Numerator, metric.Denominator}
			if metric.Numerator == "" || metric.Denominator == "" {
				return fmt.Errorf("semantic metric %q ratio requires numerator and denominator", name)
			}
			if err := validateSemanticIdentifier(metric.Numerator); err != nil {
				return fmt.Errorf("semantic metric %q numerator %q is invalid: %w", name, metric.Numerator, err)
			}
			if err := validateSemanticIdentifier(metric.Denominator); err != nil {
				return fmt.Errorf("semantic metric %q denominator %q is invalid: %w", name, metric.Denominator, err)
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
	for _, name := range metricNames {
		if err := visit(name); err != nil {
			return err
		}
	}
	return m.validateMetricUnits()
}

func defaultMetricEmpty(aggregation string) string {
	if aggregation == "count" || aggregation == "count_distinct" {
		return "zero"
	}
	return "null"
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

func (m *Model) ResolveBindingPath(dataset string, binding DimensionBinding) ([]Relationship, error) {
	dimension, err := m.ResolveDimension(binding.Field)
	if err != nil {
		return nil, err
	}
	if len(binding.Path) == 0 {
		return m.SafeRelationshipPath(dataset, dimension.Table)
	}
	current := dataset
	visited := map[string]struct{}{dataset: {}}
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
