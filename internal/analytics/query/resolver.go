package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type Planner struct {
	compiled      *CompiledModel
	tableRelation TableRelation
}

// datasetTable resolves a semantic alias through the compiled serving
// binding. It is the only runtime path from a semantic alias to executable
// table metadata.
func (p *Planner) datasetTable(alias string) (semanticmodel.Table, bool) {
	if p == nil || p.compiled == nil {
		return semanticmodel.Table{}, false
	}
	dataset, ok := p.compiled.dataset(alias)
	if !ok {
		return semanticmodel.Table{}, false
	}
	return dataset.table, true
}

func (p *Planner) resolveDimension(ref string) (semanticmodel.MetricDimension, error) {
	parts := strings.Split(ref, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return semanticmodel.MetricDimension{}, fmt.Errorf("field %q must be qualified as dataset.field", ref)
	}
	if p == nil || p.compiled == nil {
		return semanticmodel.MetricDimension{}, fmt.Errorf("planner is not compiled")
	}
	if _, ok := p.compiled.dataset(parts[0]); !ok {
		return semanticmodel.MetricDimension{}, fmt.Errorf("unknown dataset %q", parts[0])
	}
	dimension, ok := p.compiled.PhysicalField(ref)
	if !ok {
		return semanticmodel.MetricDimension{}, fmt.Errorf("unknown field %q on dataset %q", parts[1], parts[0])
	}
	return dimension, nil
}

func (p *Planner) resolveBindingPath(dataset string, binding semanticmodel.DimensionBinding) ([]semanticmodel.Relationship, error) {
	if p == nil || p.compiled == nil {
		return nil, fmt.Errorf("planner is not compiled")
	}
	return p.compiled.ResolveBindingPath(dataset, binding.Field, binding.Path)
}

// TableRelation resolves a validated backing model materialization name to the
// physical SQL relation used by a serving plan. Planner callers provide
// semantic dataset aliases; the planner translates aliases before invoking
// this callback.
type TableRelation func(table string) (string, error)

// IsCompiled reports whether the planner owns activation-compiled facts. It
// intentionally does not expose mutable authoring metadata to consumers.
func (p *Planner) IsCompiled() bool {
	return p != nil && p.compiled != nil
}

type tableAlias struct {
	Table string
	Alias string
	Path  []semanticmodel.Relationship
}

type queryView struct {
	Dataset    string
	Dimensions map[string]semanticmodel.MetricDimension
	Metrics    map[string]resolvedAggregateMetric
	// Paths is keyed by the caller's semantic reference first. The physical
	// field is retained as a fallback for callers that resolve a qualified
	// field directly, but must not be used as the identity of a role-playing
	// dimension: two semantic references can intentionally resolve to the same
	// physical table and field through different relationship paths.
	Paths map[string][]semanticmodel.Relationship
}

func (p *Planner) physicalTable(table string) (string, error) {
	if dataset, ok := p.compiled.dataset(table); ok {
		table = dataset.ModelName()
	}
	if p == nil || p.tableRelation == nil {
		identifier, err := quoteIdent(table)
		if err != nil {
			return "", err
		}
		return "model." + identifier, nil
	}
	if strings.TrimSpace(table) == "" {
		return "", fmt.Errorf("physical table name is required")
	}
	relation, err := p.tableRelation(table)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(relation) == "" {
		return "", fmt.Errorf("physical relation for table %q is empty", table)
	}
	return relation, nil
}

func (p *Planner) TableRelation() TableRelation {
	if p == nil {
		return nil
	}
	return p.tableRelation
}

func (p *Planner) metricExpression(name string) (semanticmodel.Expression, error) {
	if p == nil || p.compiled == nil {
		return semanticmodel.Expression{}, fmt.Errorf("planner is not compiled")
	}
	if node, ok := p.compiled.metric(name); ok {
		if node.Derived != nil {
			return node.Derived.Expression, nil
		}
		if node.Aggregate != nil {
			return semanticmodel.Expression{}, fmt.Errorf("metric %q is aggregate", name)
		}
		if node.Ratio != nil {
			return semanticmodel.Expression{}, fmt.Errorf("metric %q is ratio; use its typed numerator and denominator", name)
		}
		return semanticmodel.Expression{}, fmt.Errorf("metric %q has no compiled payload", name)
	}
	return semanticmodel.Expression{}, fmt.Errorf("unknown metric %q", name)
}

func (p *Planner) resolvedAggregateMetric(name string) (resolvedAggregateMetric, error) {
	if p == nil || p.compiled == nil {
		return resolvedAggregateMetric{}, fmt.Errorf("planner is not compiled")
	}
	node, ok := p.compiled.metric(name)
	if !ok || node.Aggregate == nil {
		if ok {
			return resolvedAggregateMetric{}, fmt.Errorf("metric %q is not aggregate", name)
		}
		return resolvedAggregateMetric{}, fmt.Errorf("unknown aggregate metric %q", name)
	}
	aggregate := node.Aggregate
	return resolvedAggregateMetric{
		Field:         node.Name,
		Name:          node.Name,
		Label:         node.Label,
		Description:   node.Description,
		Dataset:       aggregate.Dataset,
		Aggregation:   aggregate.Aggregation,
		InputField:    aggregate.InputField,
		Empty:         aggregate.Empty,
		Unit:          node.Unit,
		Format:        node.Format,
		TimeDimension: aggregate.TimeDimension,
		NamedFilters:  cloneCompiledNamedFilters(aggregate.NamedFilters),
	}, nil
}

type AggregateAnalysis struct {
	Datasets      []string
	AtomicMetrics []string
	MultiDataset  bool
}

// AnalyzeAggregate exposes the normalized semantic dependencies used by
// higher-level physical optimizers without exposing the planner's mutable
// resolution internals.
func (p *Planner) AnalyzeAggregate(request Request) (AggregateAnalysis, error) {
	resolved, err := p.resolveAggregate(request)
	if err != nil {
		return AggregateAnalysis{}, err
	}
	metrics := make([]string, 0, len(resolved.Aggregates))
	for name := range resolved.Aggregates {
		metrics = append(metrics, name)
	}
	sort.Strings(metrics)
	return AggregateAnalysis{
		Datasets:      append([]string{}, resolved.Datasets...),
		AtomicMetrics: metrics,
		MultiDataset:  resolved.MultiDataset,
	}, nil
}

func (p *Planner) queryView(request Request) (*queryView, error) {
	return p.semanticView(request.Dataset, request.Dimensions, request.Metrics, request.Filters, request.Time.Field)
}

func (p *Planner) rowView(request RowRequest) (*queryView, error) {
	if request.Dataset == "" && len(request.Metrics) == 0 {
		return nil, fmt.Errorf("row query requires dataset when no metric is selected")
	}
	return p.semanticView(request.Dataset, request.Dimensions, request.Metrics, request.Filters, "")
}

func (p *Planner) rawValueView(request RawValueRequest) (*queryView, error) {
	metrics := []Field{}
	if request.Metric.Field != "" {
		metrics = append(metrics, request.Metric)
	}
	return p.semanticView(request.Dataset, request.Dimensions, metrics, request.Filters, "")
}

func (p *Planner) countView(request CountRequest) (*queryView, error) {
	if request.Dataset == "" {
		return nil, fmt.Errorf("count query requires dataset")
	}
	return p.semanticView(request.Dataset, nil, nil, request.Filters, "")
}

func (p *Planner) semanticView(dataset string, dimensions []Field, metrics []Field, filters []Filter, timeField string) (*queryView, error) {
	if p == nil || p.compiled == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	resolvedMetrics := map[string]resolvedAggregateMetric{}
	for _, item := range metrics {
		resolved, err := p.resolvedAggregateMetric(item.Field)
		if err != nil {
			return nil, err
		}
		if dataset == "" {
			dataset = resolved.Dataset
		}
		if resolved.Dataset != dataset {
			return nil, fmt.Errorf("cross-dataset metrics are not supported")
		}
		resolvedMetrics[item.Field] = resolved
	}
	if dataset == "" {
		return nil, fmt.Errorf("query requires a dataset")
	}
	if _, ok := p.compiled.dataset(dataset); !ok {
		return nil, fmt.Errorf("unknown dataset %q", dataset)
	}
	if err := validateSingleDatasetFilterScope(dataset, filters); err != nil {
		return nil, err
	}
	resolvedDimensions := map[string]semanticmodel.MetricDimension{}
	paths := map[string][]semanticmodel.Relationship{}
	for _, item := range dimensions {
		dimension, path, err := p.resolveViewDimension(dataset, item.Field)
		if err != nil {
			return nil, err
		}
		resolvedDimensions[item.Field] = dimension
		resolvedDimensions[dimension.Field] = dimension
		paths[item.Field] = path
		if _, exists := paths[dimension.Field]; !exists {
			paths[dimension.Field] = path
		}
	}
	view := &queryView{Dataset: dataset, Dimensions: resolvedDimensions, Metrics: resolvedMetrics, Paths: paths}
	if err := p.exposeViewFilters(view, filters); err != nil {
		return nil, err
	}
	if timeField != "" {
		dimension, path, err := p.resolveViewDimension(dataset, timeField)
		if err != nil {
			return nil, err
		}
		resolvedDimensions[timeField] = dimension
		resolvedDimensions[dimension.Field] = dimension
		paths[timeField] = path
		if _, exists := paths[dimension.Field]; !exists {
			paths[dimension.Field] = path
		}
	}
	return view, nil
}

func (p *Planner) resolveViewDimension(dataset, ref string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	if _, ok := p.compiled.SemanticDimension(ref); ok {
		binding, ok := p.compiled.DimensionBinding(ref, dataset)
		if !ok {
			return semanticmodel.MetricDimension{}, nil, fmt.Errorf("semantic dimension %q has no binding for dataset %q", ref, dataset)
		}
		return binding.Physical, binding.Path, nil
	}
	dimension, err := p.resolveDimension(ref)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, err
	}
	path, err := p.relationshipPath(dataset, dimension.Table)
	return dimension, path, err
}

func validateSingleDatasetFilterScope(dataset string, filters []Filter) error {
	for _, filter := range filters {
		if filter.Dataset != "" && filter.Dataset != dataset {
			return fmt.Errorf("filter dataset %q does not match query dataset %q", filter.Dataset, dataset)
		}
		if filter.Spatial != nil && filter.Spatial.Dataset != "" && filter.Spatial.Dataset != dataset {
			return fmt.Errorf("spatial filter dataset %q does not match query dataset %q", filter.Spatial.Dataset, dataset)
		}
		for _, group := range filter.Groups {
			if err := validateSingleDatasetFilterScope(dataset, group.Filters); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Planner) exposeViewFilters(view *queryView, filters []Filter) error {
	var expose func(Filter) error
	expose = func(filter Filter) error {
		refs := []struct {
			field string
			path  []string
		}{}
		if filter.Field != "" {
			refs = append(refs, struct {
				field string
				path  []string
			}{field: filter.Field, path: filter.Path})
		}
		if filter.Spatial != nil {
			refs = append(refs,
				struct {
					field string
					path  []string
				}{field: filter.Spatial.LatitudeField},
				struct {
					field string
					path  []string
				}{field: filter.Spatial.LongitudeField},
			)
		}
		for _, ref := range refs {
			dimension, path, err := p.resolveViewFilterDimension(view.Dataset, ref.field, ref.path)
			if err != nil {
				return err
			}
			view.Dimensions[ref.field] = dimension
			view.Dimensions[dimension.Field] = dimension
			view.Paths[ref.field] = path
			if _, exists := view.Paths[dimension.Field]; !exists {
				view.Paths[dimension.Field] = path
			}
		}
		for _, group := range filter.Groups {
			for _, child := range group.Filters {
				if err := expose(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, filter := range filters {
		if err := expose(filter); err != nil {
			return err
		}
	}
	return nil
}

func (p *Planner) resolveViewFilterDimension(dataset, ref string, explicitPath []string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	if _, ok := p.compiled.SemanticDimension(ref); ok {
		binding, ok := p.compiled.DimensionBinding(ref, dataset)
		if !ok {
			return semanticmodel.MetricDimension{}, nil, fmt.Errorf("semantic dimension %q has no binding for dataset %q", ref, dataset)
		}
		if len(explicitPath) > 0 {
			path, err := p.compiled.ResolveBindingPath(dataset, binding.Physical.Field, explicitPath)
			return binding.Physical, path, err
		}
		return binding.Physical, binding.Path, nil
	}
	dimension, err := p.resolveDimension(ref)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, err
	}
	var path []semanticmodel.Relationship
	if len(explicitPath) > 0 {
		path, err = p.compiled.ResolveBindingPath(dataset, ref, explicitPath)
	} else {
		path, err = p.relationshipPath(dataset, dimension.Table)
	}
	return dimension, path, err
}

func filterRefs(filters []Filter) []string {
	fields := []string{}
	for _, filter := range filters {
		if filter.Field != "" {
			fields = append(fields, filter.Field)
		}
		if filter.Spatial != nil {
			fields = append(fields, filter.Spatial.LatitudeField, filter.Spatial.LongitudeField)
		}
		for _, group := range filter.Groups {
			fields = append(fields, filterRefs(group.Filters)...)
		}
	}
	return fields
}

func (s *queryView) ResolveDimensionRef(ref string) (string, semanticmodel.MetricDimension, error) {
	if dimension, ok := s.Dimensions[ref]; ok {
		return dimension.Field, dimension, nil
	}
	return "", semanticmodel.MetricDimension{}, fmt.Errorf("field %q is not exposed", ref)
}

func (s *queryView) ResolveDimensionRefPath(ref string) (string, semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	field, dimension, err := s.ResolveDimensionRef(ref)
	if err != nil {
		return "", semanticmodel.MetricDimension{}, nil, err
	}
	path, ok := s.Paths[ref]
	if !ok {
		path = s.Paths[field]
	}
	return field, dimension, path, nil
}

func (s *queryView) ResolveMetricRef(ref string) (string, resolvedAggregateMetric, error) {
	if metric, ok := s.Metrics[ref]; ok {
		return ref, metric, nil
	}
	return "", resolvedAggregateMetric{}, fmt.Errorf("field %q is not exposed", ref)
}

func (p *Planner) relationshipPath(base, target string) ([]semanticmodel.Relationship, error) {
	if p == nil || p.compiled == nil {
		return nil, fmt.Errorf("planner is not compiled")
	}
	return p.compiled.RelationshipPath(base, target)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func pathTables(base string, path []semanticmodel.Relationship) []tablePath {
	current := base
	tables := []tablePath{}
	for index, relationship := range path {
		fromTable, _, err := semanticmodel.RelationshipEndpoint(relationship, true)
		if err != nil {
			return tables
		}
		toTable, _, err := semanticmodel.RelationshipEndpoint(relationship, false)
		if err != nil {
			return tables
		}
		next := ""
		switch {
		case current == fromTable:
			next = toTable
		case relationship.Cardinality == "one_to_one" && current == toTable:
			next = fromTable
		default:
			return tables
		}
		tables = append(tables, tablePath{Table: next, Path: append([]semanticmodel.Relationship{}, path[:index+1]...)})
		current = next
	}
	return tables
}

type tablePath struct {
	Table string
	Path  []semanticmodel.Relationship
}

func splitField(field string) (string, string, error) {
	parts := strings.Split(field, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("field %q must be qualified as table.field", field)
	}
	return parts[0], parts[1], nil
}
