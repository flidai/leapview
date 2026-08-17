package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// CompiledMetricLineage is the physical lineage retained for a metric.  The
// planner does not use this to emit SQL yet; retaining it here means a later
// typed planner can consume the serving-state contract without reparsing the
// semantic model.
type CompiledMetricLineage struct {
	Entries []CompiledLineageEntry
}

// CompiledNamedFilter retains authored semantic identity beside its typed
// predicate so planning never needs to consult mutable authoring maps.
type CompiledNamedFilter struct {
	Name   string
	Filter Filter
}

// CompiledLineageEntry keeps semantic reference identity alongside the
// physical field and chosen route. In particular, two named filters may use
// the same physical field through different role-playing paths.
type CompiledLineageEntry struct {
	Role      string
	Reference string
	Field     string
	Path      []semanticmodel.Relationship
}

// CompiledMetric is one immutable node in the metric evaluation DAG.  A node
// is tagged with its canonical semantic type and contains all request-invariant
// metadata needed by the current planner and future typed planning stages.
type CompiledMetric struct {
	Name          string
	Type          string
	Label         string
	Description   string
	Hidden        bool
	Expression    semanticmodel.Expression
	Dependencies  []string
	RootDatasets  []string
	Dataset       string
	Aggregation   string
	InputField    string
	NamedFilters  []CompiledNamedFilter
	Empty         string
	TimeDimension string
	Numerator     string
	Denominator   string
	Unit          string
	Format        string
	Lineage       CompiledMetricLineage
}

// CompiledModel is immutable semantic metadata shared by every query in a
// serving-state runtime. Expressions, named predicates, defaults, and metric
// lineage are compiled once during activation.
type CompiledModel struct {
	model *semanticmodel.Model

	// The DAG is intentionally private. Returning detached nodes prevents a
	// consumer from mutating serving-state metadata after activation.
	metrics          map[string]CompiledMetric
	topologicalOrder []string
}

// CompileModel builds the complete metric evaluation DAG after validating the
// entire semantic graph. ValidateSemanticGraph intentionally does not require
// project sources or connection credentials, so activation cannot admit a
// malformed relationship, dimension, filter, or metric definition.
func CompileModel(model *semanticmodel.Model) (*CompiledModel, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	validated := model.ExecutionSnapshot()
	if validated == nil {
		return nil, fmt.Errorf("semantic model snapshot is required")
	}
	if err := validated.ValidateSemanticGraph(); err != nil {
		return nil, fmt.Errorf("validate semantic graph: %w", err)
	}
	model = validated
	compiled := &CompiledModel{
		model:   model,
		metrics: make(map[string]CompiledMetric, len(model.Metrics)),
	}

	names := compiledMetricNames(model.Metrics)
	for _, name := range names {
		metric := model.Metrics[name]
		metric.Name = name
		node, err := compileMetricNode(model, name, metric)
		if err != nil {
			return nil, err
		}
		compiled.metrics[name] = node
	}

	state := make(map[string]uint8, len(names)) // 1=visiting, 2=visited
	order := make([]string, 0, len(names))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("metric dependency cycle includes %q", name)
		case 2:
			return nil
		}
		node, ok := compiled.metrics[name]
		if !ok {
			return fmt.Errorf("unknown aggregate member %q", name)
		}
		state[name] = 1
		for _, dependency := range node.Dependencies {
			if _, ok := compiled.metrics[dependency]; !ok {
				return fmt.Errorf("metric %q: unknown aggregate member %q", name, dependency)
			}
			if err := visit(dependency); err != nil {
				return fmt.Errorf("metric %q: %w", name, err)
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	compiled.topologicalOrder = append([]string(nil), order...)

	// Roots are resolved after the topological walk so every node has a stable
	// transitive dataset set, independent of map iteration order.
	for _, name := range order {
		node := compiled.metrics[name]
		roots := map[string]struct{}{}
		if node.Type == "aggregate" {
			roots[node.Dataset] = struct{}{}
		}
		for _, dependency := range node.Dependencies {
			dependencyNode := compiled.metrics[dependency]
			for _, root := range dependencyNode.RootDatasets {
				roots[root] = struct{}{}
			}
			for _, entry := range dependencyNode.Lineage.Entries {
				node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, entry)
			}
		}
		if len(roots) == 0 {
			return nil, fmt.Errorf("metric %q has no root dataset", name)
		}
		resolved := sortedKeys(roots)
		node.RootDatasets = resolved
		node.Lineage.Entries = cloneLineageEntries(node.Lineage.Entries)
		compiled.metrics[name] = node
	}
	return compiled, nil
}

func (c *CompiledModel) metric(name string) (CompiledMetric, bool) {
	if c == nil {
		return CompiledMetric{}, false
	}
	node, ok := c.metrics[name]
	if !ok {
		return CompiledMetric{}, false
	}
	return cloneCompiledMetric(node), true
}

func (c *CompiledModel) metricNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.metrics))
	for name := range c.metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneCompiledMetric(node CompiledMetric) CompiledMetric {
	node.Dependencies = append([]string(nil), node.Dependencies...)
	node.RootDatasets = append([]string(nil), node.RootDatasets...)
	if node.NamedFilters != nil {
		original := node.NamedFilters
		node.NamedFilters = make([]CompiledNamedFilter, len(node.NamedFilters))
		for index, filter := range original {
			node.NamedFilters[index] = CompiledNamedFilter{Name: filter.Name, Filter: cloneFilters([]Filter{filter.Filter})[0]}
		}
	}
	node.Lineage.Entries = cloneLineageEntries(node.Lineage.Entries)
	return node
}

func appendLineageEntry(values []CompiledLineageEntry, entry CompiledLineageEntry) []CompiledLineageEntry {
	for _, existing := range values {
		if existing.Role == entry.Role && existing.Reference == entry.Reference && existing.Field == entry.Field && relationshipPathSignature(existing.Path) == relationshipPathSignature(entry.Path) {
			return values
		}
	}
	entry.Path = cloneRelationships(entry.Path)
	return append(values, entry)
}

func cloneLineageEntries(values []CompiledLineageEntry) []CompiledLineageEntry {
	if values == nil {
		return nil
	}
	result := make([]CompiledLineageEntry, len(values))
	for index, entry := range values {
		result[index] = entry
		result[index].Path = cloneRelationships(entry.Path)
	}
	return result
}

func cloneCompiledNamedFilters(values []CompiledNamedFilter) []CompiledNamedFilter {
	if values == nil {
		return nil
	}
	out := make([]CompiledNamedFilter, len(values))
	for index, value := range values {
		out[index] = CompiledNamedFilter{Name: value.Name, Filter: cloneFilters([]Filter{value.Filter})[0]}
	}
	return out
}

func compiledMetricNames(metrics map[string]semanticmodel.Metric) []string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compileMetricNode(model *semanticmodel.Model, name string, metric semanticmodel.Metric) (CompiledMetric, error) {
	metricType := metric.Type
	node := CompiledMetric{
		Name: name, Type: metricType, Dataset: metric.Dataset,
		Numerator: metric.Numerator, Denominator: metric.Denominator,
		Unit: metric.Unit, Format: metric.Format,
		Label: metric.Label, Description: metric.Description, Hidden: metric.Hidden,
		Aggregation: metric.Aggregation, Empty: metric.Empty,
		Lineage: CompiledMetricLineage{},
	}
	switch metricType {
	case "aggregate":
		if strings.TrimSpace(metric.Dataset) == "" {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate dataset is required", name)
		}
		if metric.Input == nil || strings.TrimSpace(metric.Input.Field) == "" {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input is required", name)
		}
		if _, ok := model.Tables[metric.Dataset]; !ok {
			return CompiledMetric{}, fmt.Errorf("metric %q references unknown dataset %q", name, metric.Dataset)
		}
		input := semanticmodel.MetricDimension{Table: metric.Dataset}
		var err error
		input, err = model.ResolveDimension(metric.Input.Field)
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input: %w", name, err)
		}
		if input.Table != metric.Dataset {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input field %q is not owned by dataset %q", name, metric.Input.Field, metric.Dataset)
		}
		node.InputField = metric.Input.Field
		node.TimeDimension = metric.TimeDimension
		if node.TimeDimension == "" {
			if dataset, ok := model.Datasets[metric.Dataset]; ok {
				node.TimeDimension = dataset.DefaultTimeDimension
			}
		}
		if node.TimeDimension != "" {
			if err := validateMetricTimeDimension(model, name, metric.Dataset, node.TimeDimension); err != nil {
				return CompiledMetric{}, err
			}
			dimension := model.Dimensions[node.TimeDimension]
			binding := dimension.Bindings[metric.Dataset]
			timeDimension, err := model.ResolveDimension(binding.Field)
			if err != nil {
				return CompiledMetric{}, fmt.Errorf("metric %q time dimension %q binding: %w", name, node.TimeDimension, err)
			}
			timePath, err := model.ResolveBindingPath(metric.Dataset, binding)
			if err != nil {
				return CompiledMetric{}, fmt.Errorf("metric %q time dimension %q path: %w", name, node.TimeDimension, err)
			}
			node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "time", Reference: node.TimeDimension, Field: timeDimension.Field, Path: timePath})
		}
		inputPath := []semanticmodel.Relationship(nil)
		inputPath, err = model.SafeRelationshipPath(metric.Dataset, input.Table)
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input path: %w", name, err)
		}
		node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "input", Reference: "input:" + name, Field: metric.Input.Field, Path: inputPath})
		if err := compileMetricWhere(model, name, metric.Dataset, metric.Where, &node); err != nil {
			return CompiledMetric{}, err
		}
	case "derived":
		expression, err := semanticmodel.ParseExpression(metric.Expression)
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q: %w", name, err)
		}
		refs := expression.References()
		if len(refs) == 0 {
			return CompiledMetric{}, fmt.Errorf("metric %q has no root dataset", name)
		}
		node.Expression = expression
		node.Dependencies = sortedUnique(refs)
	case "ratio":
		if strings.TrimSpace(metric.Numerator) == "" || strings.TrimSpace(metric.Denominator) == "" {
			return CompiledMetric{}, fmt.Errorf("metric %q ratio requires numerator and denominator", name)
		}
		node.Dependencies = sortedUnique([]string{metric.Numerator, metric.Denominator})
		expression, err := semanticmodel.ParseExpression(metricExecutableExpression(metric))
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q: %w", name, err)
		}
		node.Expression = expression
	default:
		return CompiledMetric{}, fmt.Errorf("metric %q has unsupported type %q", name, metric.Type)
	}
	return node, nil
}

func validateMetricTimeDimension(model *semanticmodel.Model, metric, dataset, name string) error {
	dimension, ok := model.Dimensions[name]
	if !ok {
		return fmt.Errorf("metric %q time dimension %q is unknown", metric, name)
	}
	if dimension.Type != "date" && dimension.Type != "timestamp" {
		return fmt.Errorf("metric %q time dimension %q is not temporal", metric, name)
	}
	if _, ok := dimension.Bindings[dataset]; !ok {
		return fmt.Errorf("metric %q time dimension %q has no binding for dataset %q", metric, name, dataset)
	}
	return nil
}

func compileMetricWhere(model *semanticmodel.Model, metric, dataset string, names []string, node *CompiledMetric) error {
	if names != nil && len(names) == 0 {
		return fmt.Errorf("metric %q aggregate where requires a non-empty list", metric)
	}
	filters := make([]Filter, 0, len(names))
	for _, name := range names {
		compiled, err := compileNamedSemanticFilters(model, []string{name})
		if err != nil {
			return fmt.Errorf("metric %q: %w", metric, err)
		}
		if len(compiled) != 1 {
			return fmt.Errorf("metric %q: semantic filter %q compiled to %d filters", metric, name, len(compiled))
		}
		filter := scopeMetricWhereFilter(compiled[0], dataset)
		if err := compileFilterLineage(model, metric, dataset, &filter, node, "filter:"+name); err != nil {
			return err
		}
		filters = append(filters, filter)
	}
	node.NamedFilters = make([]CompiledNamedFilter, len(filters))
	for index, filter := range filters {
		node.NamedFilters[index] = CompiledNamedFilter{Name: names[index], Filter: cloneFilters([]Filter{filter})[0]}
	}
	return nil
}

func compileFilterLineage(model *semanticmodel.Model, metric, root string, filter *Filter, node *CompiledMetric, reference string) error {
	if filter.Field != "" {
		dimension, err := model.ResolveDimension(filter.Field)
		if err != nil {
			return fmt.Errorf("metric %q filter field %q: %w", metric, filter.Field, err)
		}
		path, err := model.ResolveBindingPath(root, semanticmodel.DimensionBinding{Field: filter.Field, Path: append([]string(nil), filter.Path...)})
		if err != nil {
			return fmt.Errorf("metric %q filter field %q path: %w", metric, filter.Field, err)
		}
		node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "filter", Reference: reference, Field: filter.Field, Path: path})
		_ = dimension
	}
	for groupIndex := range filter.Groups {
		for childIndex := range filter.Groups[groupIndex].Filters {
			childReference := fmt.Sprintf("%s.groups[%d].filters[%d]", reference, groupIndex, childIndex)
			if err := compileFilterLineage(model, metric, root, &filter.Groups[groupIndex].Filters[childIndex], node, childReference); err != nil {
				return err
			}
		}
	}
	if filter.Spatial != nil {
		for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
			if field == "" {
				continue
			}
			dimension, err := model.ResolveDimension(field)
			if err != nil {
				return fmt.Errorf("metric %q filter field %q: %w", metric, field, err)
			}
			path, err := model.SafeRelationshipPath(root, dimension.Table)
			if err != nil {
				return fmt.Errorf("metric %q filter field %q path: %w", metric, field, err)
			}
			node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "filter", Reference: reference, Field: field, Path: path})
		}
	}
	return nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendRelationshipFields(values []string, path []semanticmodel.Relationship) []string {
	for _, relationship := range path {
		for _, from := range []bool{true, false} {
			table, fields, err := semanticmodel.RelationshipEndpoint(relationship, from)
			if err != nil {
				continue
			}
			for _, field := range fields {
				values = appendUnique(values, table+"."+field)
			}
		}
	}
	return values
}

func cloneRelationships(values []semanticmodel.Relationship) []semanticmodel.Relationship {
	out := make([]semanticmodel.Relationship, len(values))
	for index, value := range values {
		out[index] = value
		out[index].FromFields = append([]string(nil), value.FromFields...)
		out[index].ToFields = append([]string(nil), value.ToFields...)
	}
	return out
}

func cloneColumnSchemas(values []semanticmodel.ColumnSchema) []semanticmodel.ColumnSchema {
	if values == nil {
		return nil
	}
	out := make([]semanticmodel.ColumnSchema, len(values))
	for index, value := range values {
		out[index] = value
		if value.Nullable != nil {
			nullable := *value.Nullable
			out[index].Nullable = &nullable
		}
	}
	return out
}

func cloneFilters(values []Filter) []Filter {
	out := make([]Filter, len(values))
	for index, value := range values {
		out[index] = value
		out[index].Values = append([]any(nil), value.Values...)
		out[index].Path = append([]string(nil), value.Path...)
		out[index].Groups = make([]FilterGroup, len(value.Groups))
		for groupIndex, group := range value.Groups {
			out[index].Groups[groupIndex] = FilterGroup{Filters: cloneFilters(group.Filters)}
		}
		if value.Spatial != nil {
			spatial := *value.Spatial
			spatial.Points = append([]SpatialPoint(nil), value.Spatial.Points...)
			out[index].Spatial = &spatial
		}
	}
	return out
}

// metricExecutableExpression is a planner-boundary adapter. Canonical ratio
// metrics retain numerator/denominator fields in the model; only the existing
// expression evaluator receives the governed safe_divide form.
func metricExecutableExpression(metric semanticmodel.Metric) string {
	if metric.Type == "ratio" {
		return fmt.Sprintf("safe_divide(${%s}, ${%s})", metric.Numerator, metric.Denominator)
	}
	return metric.Expression
}

type PlannerOption func(*Planner) error

func WithTableRelation(relation TableRelation) PlannerOption {
	return func(planner *Planner) error {
		if relation == nil {
			return fmt.Errorf("table relation resolver is required")
		}
		planner.tableRelation = relation
		return nil
	}
}

func NewCompiledPlanner(model *semanticmodel.Model, options ...PlannerOption) (*Planner, error) {
	compiled, err := CompileModel(model)
	if err != nil {
		return nil, err
	}
	planner := &Planner{model: compiled.model, compiled: compiled}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("planner option is required")
		}
		if err := option(planner); err != nil {
			return nil, err
		}
	}
	return planner, nil
}
