package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/masking"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func (p *Planner) Plan(request Request) (Plan, error) {
	return p.planAggregate(request)
}

func (p *Planner) PlanRows(request RowRequest) (Plan, error) {
	view, err := p.rowView(request)
	if err != nil {
		return Plan{}, err
	}
	_, err = columnMaskMap(request.ColumnMasks)
	if err != nil {
		return Plan{}, err
	}
	for _, dimension := range request.Dimensions {
		_, _, _, err := view.ResolveDimensionRefPath(dimension.Field)
		if err != nil {
			return Plan{}, err
		}
	}
	var population []CompiledNamedFilter
	populationSignature := ""
	populationSet := false
	for _, metric := range request.Metrics {
		field, resolved, err := view.ResolveMetricRef(metric.Field)
		if err != nil {
			return Plan{}, err
		}
		if resolved.Dataset != view.Dataset {
			return Plan{}, fmt.Errorf("metric %q is not owned by dataset %q", field, view.Dataset)
		}
		signature, err := flatPlanFilterSignature(resolved.NamedFilters)
		if err != nil {
			return Plan{}, fmt.Errorf("metric %q population signature: %w", field, err)
		}
		if !populationSet {
			population = canonicalCompiledNamedFilters(resolved.NamedFilters)
			populationSignature = signature
			populationSet = true
		} else if signature != populationSignature {
			return Plan{}, fmt.Errorf("row query selects metrics with divergent populations")
		}
		for _, field := range aggregateMetricPhysicalFields(resolved) {
			physical, err := p.resolveDimension(field)
			if err != nil {
				return Plan{}, err
			}
			path, err := p.relationshipPath(view.Dataset, physical.Table)
			if err != nil {
				return Plan{}, err
			}
			_ = path // relationship validation is retained; lowering is PlanIR-owned
		}
	}
	metricFilters := namedFlatPlanFilters(population, view.Dataset)
	if len(metricFilters) > 0 {
		compiledFilters := make([]Filter, 0, len(metricFilters))
		for _, spec := range metricFilters {
			compiledFilters = append(compiledFilters, spec.Filter)
		}
		if err := p.exposeViewFilters(view, compiledFilters); err != nil {
			return Plan{}, err
		}
	}
	if _, err := filterFieldBindings(view, request.Filters); err != nil {
		return Plan{}, err
	}
	for _, spec := range metricFilters {
		if _, err := filterFieldBindings(view, []Filter{spec.Filter}); err != nil {
			return Plan{}, err
		}
	}
	if len(request.Dimensions) == 0 && len(request.Metrics) == 0 {
		return Plan{}, fmt.Errorf("row query requires at least one selected field")
	}
	filterSpecs := append(requestFlatPlanFilters(request.Filters), metricFilters...)
	irGraph, irErr := p.buildFlatPlanIRWithFilters(view.Dataset, request.Dimensions, request.Metrics, filterSpecs, view.Paths, request.Sort, request.Limit, request.Offset)
	if irErr != nil {
		return Plan{}, fmt.Errorf("build row plan IR: %w", irErr)
	}
	if sortNode, ok := irGraph.Nodes[irGraph.Output].(planir.SortLimit); ok {
		masks, _ := columnMaskMap(request.ColumnMasks)
		for index := range sortNode.Projection {
			if kind, found := masks[strings.ToLower(sortNode.Projection[index].Source)]; found {
				sortNode.Projection[index].Mask = string(kind)
			}
			if physical, _, err := p.flatPhysicalField(view.Dataset, sortNode.Projection[index].Source); err == nil {
				for _, key := range []string{physical.Field, physical.Table + "." + physical.Name} {
					if kind, found := masks[strings.ToLower(key)]; found {
						sortNode.Projection[index].Mask = string(kind)
					}
				}
			}
		}
		irGraph.Nodes[irGraph.Output] = sortNode
		irGraph.NodeMeta = sortNode.NodeMeta
	}
	rendered, irErr := planir.RenderDuckDB(irGraph)
	if irErr != nil {
		return Plan{}, fmt.Errorf("render row plan IR: %w", irErr)
	}
	columnSet := make(map[string]bool, len(rendered.Columns))
	for _, column := range rendered.Columns {
		columnSet[column] = true
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, EffectiveOrdering: effectiveOrderSorts(request.Sort, columnSet), IR: irGraph}, nil
}

func (p *Planner) PlanRawValues(request RawValueRequest) (Plan, error) {
	view, err := p.rawValueView(request)
	if err != nil {
		return Plan{}, err
	}
	masks, err := columnMaskMap(request.ColumnMasks)
	if err != nil {
		return Plan{}, err
	}
	for _, dimension := range request.Dimensions {
		_, _, _, err := view.ResolveDimensionRefPath(dimension.Field)
		if err != nil {
			return Plan{}, err
		}
	}
	metricField, metric, err := view.ResolveMetricRef(request.Metric.Field)
	if err != nil {
		return Plan{}, err
	}
	if metric.Dataset != view.Dataset {
		return Plan{}, fmt.Errorf("metric %q is not owned by dataset %q", metricField, view.Dataset)
	}
	if masks.matchesMetric(metricField, metric) {
		return Plan{}, fmt.Errorf("metric %q depends on a masked field", metricField)
	}
	metricFilterSpecs := namedFlatPlanFilters(metric.NamedFilters, view.Dataset)
	metricFilters := make([]Filter, 0, len(metricFilterSpecs))
	for _, spec := range metricFilterSpecs {
		metricFilters = append(metricFilters, spec.Filter)
	}
	if err := p.exposeViewFilters(view, metricFilters); err != nil {
		return Plan{}, err
	}
	for _, field := range aggregateMetricPhysicalFields(metric) {
		physical, err := p.resolveDimension(field)
		if err != nil {
			return Plan{}, err
		}
		path, err := p.relationshipPath(view.Dataset, physical.Table)
		if err != nil {
			return Plan{}, err
		}
		_ = path // relationship validation is retained; lowering is PlanIR-owned
	}
	valueAlias := request.Metric.Alias
	if valueAlias == "" {
		valueAlias = "value"
	}
	if _, err := quoteIdent(valueAlias); err != nil {
		return Plan{}, err
	}
	if _, err := filterFieldBindings(view, request.Filters); err != nil {
		return Plan{}, err
	}
	if _, err := filterFieldBindings(view, metricFilters); err != nil {
		return Plan{}, err
	}
	filterSpecs := append(requestFlatPlanFilters(request.Filters), metricFilterSpecs...)
	// The input null guard is a request-local execution constraint, not part of
	// the compiled named population identity. Statistical histogram queries may
	// explicitly retain nulls so the envelope can emit its null bucket.
	if !request.IncludeNull {
		filterSpecs = append(filterSpecs, flatPlanFilter{Filter: Filter{Field: metric.InputField, Operator: "is_not_null"}, Source: planir.FilterSourceRequest})
	}
	irGraph, irErr := p.buildFlatPlanIRWithFilters(view.Dataset, request.Dimensions, []Field{{Field: metricField, Alias: valueAlias}}, filterSpecs, view.Paths, request.Sort, request.Limit, 0)
	if irErr != nil {
		return Plan{}, fmt.Errorf("build raw-value plan IR: %w", irErr)
	}
	if sortNode, ok := irGraph.Nodes[irGraph.Output].(planir.SortLimit); ok {
		for index := range sortNode.Projection {
			if kind, found := masks[strings.ToLower(sortNode.Projection[index].Source)]; found {
				sortNode.Projection[index].Mask = string(kind)
			}
			if physical, _, err := p.flatPhysicalField(view.Dataset, sortNode.Projection[index].Source); err == nil {
				for _, key := range []string{physical.Field, physical.Table + "." + physical.Name} {
					if kind, found := masks[strings.ToLower(key)]; found {
						sortNode.Projection[index].Mask = string(kind)
					}
				}
			}
		}
		irGraph.Nodes[irGraph.Output] = sortNode
		irGraph.NodeMeta = sortNode.NodeMeta
	}
	rendered, irErr := planir.RenderDuckDB(irGraph)
	if irErr != nil {
		return Plan{}, fmt.Errorf("render raw-value plan IR: %w", irErr)
	}
	columnSet := make(map[string]bool, len(rendered.Columns))
	for _, column := range rendered.Columns {
		columnSet[column] = true
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, EffectiveOrdering: effectiveOrderSorts(request.Sort, columnSet), IR: irGraph}, nil
}

func (p *Planner) PlanCount(request CountRequest) (Plan, error) {
	view, err := p.countView(request)
	if err != nil {
		return Plan{}, err
	}
	if _, err := filterFieldBindings(view, request.Filters); err != nil {
		return Plan{}, err
	}
	irGraph, irErr := p.buildFlatPlanIR(view.Dataset, nil, nil, request.Filters, nil, 0, 0)
	if irErr != nil {
		return Plan{}, fmt.Errorf("build count plan IR: %w", irErr)
	}
	rendered, irErr := planir.RenderDuckDB(irGraph)
	if irErr != nil {
		return Plan{}, fmt.Errorf("render count plan IR: %w", irErr)
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, IR: irGraph}, nil
}

type columnMaskSet map[string]masking.Kind

func columnMaskMap(masks []ColumnMask) (columnMaskSet, error) {
	out := columnMaskSet{}
	for _, mask := range masks {
		field := strings.ToLower(strings.TrimSpace(mask.Field))
		if field == "" {
			continue
		}
		compiled, err := masking.Compile(mask.Mask)
		if err != nil {
			return nil, err
		}
		out[field] = compiled
	}
	return out, nil
}

func (m columnMaskSet) matchesMetric(ref string, metric resolvedAggregateMetric) bool {
	if len(m) == 0 {
		return false
	}
	for _, key := range []string{ref, metric.Field} {
		if _, ok := m[strings.ToLower(strings.TrimSpace(key))]; ok {
			return true
		}
	}
	for _, dependency := range aggregateMetricPhysicalFields(metric) {
		if _, ok := m[strings.ToLower(strings.TrimSpace(dependency))]; ok {
			return true
		}
	}
	return false
}

func aggregateMetricPhysicalFields(metric resolvedAggregateMetric) []string {
	fields := []string{}
	if metric.InputField != "" {
		fields = append(fields, metric.InputField)
	}
	for _, filter := range metric.Filters {
		if filter.Field != "" {
			fields = append(fields, filter.Field)
		}
	}
	return fields
}

func filterFieldBindings(view *queryView, filters []Filter) ([]physicalFieldBinding, error) {
	bindings := []physicalFieldBinding{}
	var walk func(Filter) error
	walk = func(filter Filter) error {
		if filter.Spatial != nil {
			for _, ref := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				field, _, path, err := view.ResolveDimensionRefPath(ref)
				if err != nil {
					return err
				}
				bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
			}
		}
		if filter.Field != "" {
			field, _, path, err := view.ResolveDimensionRefPath(filter.Field)
			if err != nil {
				return err
			}
			bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
		}
		for _, group := range filter.Groups {
			for _, child := range group.Filters {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, filter := range filters {
		if err := walk(filter); err != nil {
			return nil, err
		}
	}
	return bindings, nil
}

func allowedTimeGrain(grain string) bool {
	switch grain {
	case "second", "minute", "hour", "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}

func fieldAlias(field string) string {
	if field == "value" || field == "" {
		return field
	}
	parts := strings.Split(field, ".")
	return parts[len(parts)-1]
}

func outputAlias(field Field) (string, error) {
	if field.Alias != "" {
		if _, err := quoteIdent(field.Alias); err != nil {
			return "", err
		}
		return field.Alias, nil
	}
	alias := fieldAlias(field.Field)
	if _, err := quoteIdent(alias); err != nil {
		return "", err
	}
	return alias, nil
}

func addOutputColumn(columns map[string]bool, alias string) error {
	if columns[alias] {
		return fmt.Errorf("duplicate output alias %q", alias)
	}
	columns[alias] = true
	return nil
}

// effectiveOrderSorts makes every paginated result deterministic. Explicit
// sorts remain authoritative and selected output columns are appended as
// ascending tie-breakers in deterministic alias order. Aggregate rows are
// unique by their selected dimension tuple; a zero-dimension aggregate has one
// row, so its metric ordering is deterministic but vacuous. Row/value plans
// also receive a stable order over their projected columns.
func effectiveOrderSorts(sorts []Sort, columns map[string]bool) []Sort {
	effective := append([]Sort(nil), sorts...)
	ordered := make(map[string]struct{}, len(effective))
	for _, sort := range effective {
		ordered[sort.Field] = struct{}{}
	}
	fields := make([]string, 0, len(columns))
	for field := range columns {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		if _, ok := ordered[field]; ok {
			continue
		}
		effective = append(effective, Sort{Field: field, Direction: "asc"})
	}
	return effective
}
