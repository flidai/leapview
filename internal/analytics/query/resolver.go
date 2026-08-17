package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type Planner struct {
	model         *semanticmodel.Model
	compiled      *CompiledModel
	tableRelation TableRelation
}

// TableRelation resolves a validated semantic table name to the physical SQL
// relation used by a serving plan. Adapters use it to bind immutable storage
// versions without teaching the semantic planner about a storage engine.
type TableRelation func(table string) (string, error)

// IsCompiled reports whether the planner owns an activated immutable semantic
// snapshot. It intentionally does not expose that snapshot to consumers.
func (p *Planner) IsCompiled() bool {
	return p != nil && p.model != nil && p.compiled != nil
}

type tableAlias struct {
	Table string
	Alias string
	Path  []semanticmodel.Relationship
}

type queryView struct {
	Fact       string
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
	identifier, err := quoteIdent(table)
	if err != nil {
		return "", err
	}
	if p == nil || p.tableRelation == nil {
		return "model." + identifier, nil
	}
	relation, err := p.tableRelation(identifier)
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
		if node.Type == "aggregate" {
			return semanticmodel.Expression{}, fmt.Errorf("metric %q is aggregate", name)
		}
		return node.Expression, nil
	}
	return semanticmodel.Expression{}, fmt.Errorf("unknown metric %q", name)
}

func (p *Planner) resolvedAggregateMetric(name string) (resolvedAggregateMetric, error) {
	if p == nil || p.compiled == nil {
		return resolvedAggregateMetric{}, fmt.Errorf("planner is not compiled")
	}
	node, ok := p.compiled.metric(name)
	if !ok || node.Type != "aggregate" {
		if ok {
			return resolvedAggregateMetric{}, fmt.Errorf("metric %q is not aggregate", name)
		}
		return resolvedAggregateMetric{}, fmt.Errorf("unknown aggregate metric %q", name)
	}
	return resolvedAggregateMetric{
		Field:         node.Name,
		Name:          node.Name,
		Label:         node.Label,
		Description:   node.Description,
		Fact:          node.Dataset,
		Aggregation:   node.Aggregation,
		InputField:    node.InputField,
		Empty:         node.Empty,
		Unit:          node.Unit,
		Format:        node.Format,
		TimeDimension: node.TimeDimension,
		NamedFilters:  cloneCompiledNamedFilters(node.NamedFilters),
	}, nil
}

type AggregateAnalysis struct {
	Facts         []string
	AtomicMetrics []string
	MultiFact     bool
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
		Facts:         append([]string{}, resolved.Facts...),
		AtomicMetrics: metrics,
		MultiFact:     resolved.MultiFact,
	}, nil
}

func (p *Planner) queryView(request Request) (*queryView, error) {
	return p.semanticView(request.Table, request.Dimensions, request.Metrics, request.Filters, request.Time.Field)
}

func (p *Planner) rowView(request RowRequest) (*queryView, error) {
	if request.Table == "" && len(request.Metrics) == 0 {
		return nil, fmt.Errorf("row query requires table when no metric is selected")
	}
	return p.semanticView(request.Table, request.Dimensions, request.Metrics, request.Filters, "")
}

func (p *Planner) rawValueView(request RawValueRequest) (*queryView, error) {
	metrics := []Field{}
	if request.Metric.Field != "" {
		metrics = append(metrics, request.Metric)
	}
	return p.semanticView(request.Table, request.Dimensions, metrics, request.Filters, "")
}

func (p *Planner) countView(request CountRequest) (*queryView, error) {
	if request.Table == "" {
		return nil, fmt.Errorf("count query requires table")
	}
	return p.semanticView(request.Table, nil, nil, request.Filters, "")
}

func (p *Planner) semanticView(table string, dimensions []Field, metrics []Field, filters []Filter, timeField string) (*queryView, error) {
	if p.model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	fact := table
	resolvedMetrics := map[string]resolvedAggregateMetric{}
	for _, item := range metrics {
		resolved, err := p.resolvedAggregateMetric(item.Field)
		if err != nil {
			return nil, err
		}
		if fact == "" {
			fact = resolved.Fact
		}
		if resolved.Fact != fact {
			return nil, fmt.Errorf("cross-fact metrics are not supported")
		}
		resolvedMetrics[item.Field] = resolved
	}
	if fact == "" {
		return nil, fmt.Errorf("query requires a fact table")
	}
	if _, ok := p.model.Tables[fact]; !ok {
		return nil, fmt.Errorf("unknown table %q", fact)
	}
	if err := validateSingleFactFilterScope(fact, filters); err != nil {
		return nil, err
	}
	resolvedDimensions := map[string]semanticmodel.MetricDimension{}
	paths := map[string][]semanticmodel.Relationship{}
	for _, item := range dimensions {
		dimension, path, err := p.resolveViewDimension(fact, item.Field)
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
	view := &queryView{Fact: fact, Dimensions: resolvedDimensions, Metrics: resolvedMetrics, Paths: paths}
	if err := p.exposeViewFilters(view, filters); err != nil {
		return nil, err
	}
	if timeField != "" {
		dimension, path, err := p.resolveViewDimension(fact, timeField)
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

func (p *Planner) resolveViewDimension(fact, ref string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	if semanticDimension, ok := p.model.Dimensions[ref]; ok {
		binding, ok := semanticDimension.Bindings[fact]
		if !ok {
			return semanticmodel.MetricDimension{}, nil, fmt.Errorf("semantic dimension %q has no binding for fact %q", ref, fact)
		}
		dimension, err := p.model.ResolveDimension(binding.Field)
		if err != nil {
			return semanticmodel.MetricDimension{}, nil, err
		}
		path, err := p.model.ResolveBindingPath(fact, binding)
		return dimension, path, err
	}
	dimension, err := p.model.ResolveDimension(ref)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, err
	}
	path, err := p.relationshipPath(fact, dimension.Table)
	return dimension, path, err
}

func validateSingleFactFilterScope(fact string, filters []Filter) error {
	for _, filter := range filters {
		if filter.Fact != "" && filter.Fact != fact {
			return fmt.Errorf("filter fact %q does not match query fact %q", filter.Fact, fact)
		}
		if filter.Spatial != nil && filter.Spatial.Fact != "" && filter.Spatial.Fact != fact {
			return fmt.Errorf("spatial filter fact %q does not match query fact %q", filter.Spatial.Fact, fact)
		}
		for _, group := range filter.Groups {
			if err := validateSingleFactFilterScope(fact, group.Filters); err != nil {
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
			dimension, path, err := p.resolveViewFilterDimension(view.Fact, ref.field, ref.path)
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

func (p *Planner) resolveViewFilterDimension(fact, ref string, explicitPath []string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	if semanticDimension, ok := p.model.Dimensions[ref]; ok {
		binding, ok := semanticDimension.Bindings[fact]
		if !ok {
			return semanticmodel.MetricDimension{}, nil, fmt.Errorf("semantic dimension %q has no binding for fact %q", ref, fact)
		}
		if len(explicitPath) > 0 {
			binding.Path = append([]string(nil), explicitPath...)
		}
		dimension, err := p.model.ResolveDimension(binding.Field)
		if err != nil {
			return semanticmodel.MetricDimension{}, nil, err
		}
		path, err := p.model.ResolveBindingPath(fact, binding)
		return dimension, path, err
	}
	dimension, err := p.model.ResolveDimension(ref)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, err
	}
	var path []semanticmodel.Relationship
	if len(explicitPath) > 0 {
		path, err = p.model.ResolveBindingPath(fact, semanticmodel.DimensionBinding{Field: ref, Path: append([]string(nil), explicitPath...)})
	} else {
		path, err = p.relationshipPath(fact, dimension.Table)
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
	return p.model.SafeRelationshipPath(base, target)
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
