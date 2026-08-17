package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/masking"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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
	masks, err := columnMaskMap(request.ColumnMasks)
	if err != nil {
		return Plan{}, err
	}
	bindings := []physicalFieldBinding{}
	for _, dimension := range request.Dimensions {
		field, _, path, err := view.ResolveDimensionRefPath(dimension.Field)
		if err != nil {
			return Plan{}, err
		}
		bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
	}
	for _, metric := range request.Metrics {
		field, resolved, err := view.ResolveMetricRef(metric.Field)
		if err != nil {
			return Plan{}, err
		}
		if resolved.Fact != view.Fact {
			return Plan{}, fmt.Errorf("metric %q is not owned by fact %q", field, view.Fact)
		}
		for _, field := range aggregateMetricPhysicalFields(resolved) {
			physical, err := p.model.ResolveDimension(field)
			if err != nil {
				return Plan{}, err
			}
			path, err := p.relationshipPath(view.Fact, physical.Table)
			if err != nil {
				return Plan{}, err
			}
			bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
		}
	}
	filterBindings, err := filterFieldBindings(view, request.Filters)
	if err != nil {
		return Plan{}, err
	}
	bindings = append(bindings, filterBindings...)
	aliases, err := p.aliasesForFact(view.Fact, bindings)
	if err != nil {
		return Plan{}, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return Plan{}, err
	}
	factAliases, err := aliases.context(nil)
	if err != nil {
		return Plan{}, err
	}
	selects := []string{}
	columns := []string{}
	columnSet := map[string]bool{}
	for _, item := range request.Dimensions {
		_, dimension, path, err := view.ResolveDimensionRefPath(item.Field)
		if err != nil {
			return Plan{}, err
		}
		alias, err := outputAlias(Field{Field: item.Field, Alias: item.Alias})
		if err != nil {
			return Plan{}, err
		}
		if err := addOutputColumn(columnSet, alias); err != nil {
			return Plan{}, err
		}
		expr, err := maskedDimensionExprForPath(item.Field, dimension, aliases, path, masks)
		if err != nil {
			return Plan{}, err
		}
		selects = append(selects, expr+" AS "+alias)
		columns = append(columns, alias)
	}
	for _, item := range request.Metrics {
		field, metric, err := view.ResolveMetricRef(item.Field)
		if err != nil {
			return Plan{}, err
		}
		alias, err := outputAlias(Field{Field: field, Alias: item.Alias})
		if err != nil {
			return Plan{}, err
		}
		if err := addOutputColumn(columnSet, alias); err != nil {
			return Plan{}, err
		}
		expr, err := maskedRawMetricExpr(p.model, field, metric, factAliases, masks)
		if err != nil {
			return Plan{}, err
		}
		selects = append(selects, expr+" AS "+alias)
		columns = append(columns, alias)
	}
	if len(selects) == 0 {
		return Plan{}, fmt.Errorf("row query requires at least one selected field")
	}
	whereParts, args, err := p.wherePartsPath(view, aliases, request.Filters)
	if err != nil {
		return Plan{}, err
	}
	var sql strings.Builder
	sql.WriteString("SELECT ")
	sql.WriteString(strings.Join(selects, ", "))
	sql.WriteString("\nFROM ")
	sql.WriteString(from)
	sql.WriteString("\nWHERE ")
	sql.WriteString(strings.Join(whereParts, " AND "))
	if err := writeOrderLimitOffset(&sql, request.Sort, columnSet, request.Limit, request.Offset); err != nil {
		return Plan{}, err
	}
	_ = args
	irGraph, irErr := p.buildFlatPlanIR(view.Fact, request.Dimensions, request.Metrics, request.Filters, request.Sort, request.Limit, request.Offset)
	if irErr != nil {
		return Plan{}, fmt.Errorf("build row plan IR: %w", irErr)
	}
	if sortNode, ok := irGraph.Nodes[irGraph.Output].(planir.SortLimit); ok {
		masks, _ := columnMaskMap(request.ColumnMasks)
		for index := range sortNode.Projection {
			if kind, found := masks[strings.ToLower(sortNode.Projection[index].Source)]; found {
				sortNode.Projection[index].Mask = string(kind)
			}
			if physical, _, err := p.flatPhysicalField(view.Fact, sortNode.Projection[index].Source); err == nil {
				if kind, found := masks[strings.ToLower(physical.Table+"."+physical.Field)]; found {
					sortNode.Projection[index].Mask = string(kind)
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
	bindings := []physicalFieldBinding{}
	for _, dimension := range request.Dimensions {
		field, _, path, err := view.ResolveDimensionRefPath(dimension.Field)
		if err != nil {
			return Plan{}, err
		}
		bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
	}
	metricField, metric, err := view.ResolveMetricRef(request.Metric.Field)
	if err != nil {
		return Plan{}, err
	}
	if metric.Fact != view.Fact {
		return Plan{}, fmt.Errorf("metric %q is not owned by fact %q", metricField, view.Fact)
	}
	if masks.matchesMetric(metricField, metric) {
		return Plan{}, fmt.Errorf("metric %q depends on a masked field", metricField)
	}
	metricFilters := scopeMetricWhereFilters(namedMetricFilters(metric.NamedFilters), view.Fact)
	if err := p.exposeViewFilters(view, metricFilters); err != nil {
		return Plan{}, err
	}
	for _, field := range aggregateMetricPhysicalFields(metric) {
		physical, err := p.model.ResolveDimension(field)
		if err != nil {
			return Plan{}, err
		}
		path, err := p.relationshipPath(view.Fact, physical.Table)
		if err != nil {
			return Plan{}, err
		}
		bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
	}
	filterBindings, err := filterFieldBindings(view, request.Filters)
	if err != nil {
		return Plan{}, err
	}
	bindings = append(bindings, filterBindings...)
	metricFilterBindings, err := filterFieldBindings(view, metricFilters)
	if err != nil {
		return Plan{}, err
	}
	bindings = append(bindings, metricFilterBindings...)
	aliases, err := p.aliasesForFact(view.Fact, bindings)
	if err != nil {
		return Plan{}, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return Plan{}, err
	}
	factAliases, err := aliases.context(nil)
	if err != nil {
		return Plan{}, err
	}
	selects := []string{}
	columns := []string{}
	columnSet := map[string]bool{}
	for _, item := range request.Dimensions {
		_, dimension, path, err := view.ResolveDimensionRefPath(item.Field)
		if err != nil {
			return Plan{}, err
		}
		alias, err := outputAlias(Field{Field: item.Field, Alias: item.Alias})
		if err != nil {
			return Plan{}, err
		}
		if err := addOutputColumn(columnSet, alias); err != nil {
			return Plan{}, err
		}
		expr, err := maskedDimensionExprForPath(item.Field, dimension, aliases, path, masks)
		if err != nil {
			return Plan{}, err
		}
		selects = append(selects, expr+" AS "+alias)
		columns = append(columns, alias)
	}
	rawExpr, err := rawAggregateMetricExpr(p.model, metric, factAliases)
	if err != nil {
		return Plan{}, err
	}
	valueAlias := request.Metric.Alias
	if valueAlias == "" {
		valueAlias = "value"
	}
	if _, err := quoteIdent(valueAlias); err != nil {
		return Plan{}, err
	}
	if err := addOutputColumn(columnSet, valueAlias); err != nil {
		return Plan{}, err
	}
	selects = append(selects, "CAST("+rawExpr+" AS DOUBLE) AS "+valueAlias)
	columns = append(columns, valueAlias)
	whereParts, args, err := p.wherePartsPath(view, aliases, request.Filters)
	if err != nil {
		return Plan{}, err
	}
	filterResolution := aggregateResolution{Facts: []string{view.Fact}}
	for _, filter := range metricFilters {
		part, partArgs, err := p.factFilterPart(filter, filterResolution, view.Fact, aliases)
		if err != nil {
			return Plan{}, err
		}
		if part != "" {
			whereParts = append(whereParts, part)
			args = append(args, partArgs...)
		}
	}
	whereParts = append(whereParts, rawExpr+" IS NOT NULL")
	var sql strings.Builder
	sql.WriteString("SELECT ")
	sql.WriteString(strings.Join(selects, ", "))
	sql.WriteString("\nFROM ")
	sql.WriteString(from)
	sql.WriteString("\nWHERE ")
	sql.WriteString(strings.Join(whereParts, " AND "))
	if err := writeOrderLimitOffset(&sql, request.Sort, columnSet, request.Limit, 0); err != nil {
		return Plan{}, err
	}
	irFilters := append([]Filter(nil), request.Filters...)
	irFilters = append(irFilters, metricFilters...)
	irFilters = append(irFilters, Filter{Field: metric.InputField, Operator: "is_not_null"})
	irGraph, irErr := p.buildFlatPlanIR(view.Fact, request.Dimensions, []Field{{Field: metricField, Alias: valueAlias}}, irFilters, request.Sort, request.Limit, 0)
	if irErr != nil {
		return Plan{}, fmt.Errorf("build raw-value plan IR: %w", irErr)
	}
	if sortNode, ok := irGraph.Nodes[irGraph.Output].(planir.SortLimit); ok {
		for index := range sortNode.Projection {
			if kind, found := masks[strings.ToLower(sortNode.Projection[index].Source)]; found {
				sortNode.Projection[index].Mask = string(kind)
			}
			if physical, _, err := p.flatPhysicalField(view.Fact, sortNode.Projection[index].Source); err == nil {
				if kind, found := masks[strings.ToLower(physical.Table+"."+physical.Field)]; found {
					sortNode.Projection[index].Mask = string(kind)
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
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, EffectiveOrdering: effectiveOrderSorts(request.Sort, columnSet), IR: irGraph}, nil
}

func (p *Planner) PlanCount(request CountRequest) (Plan, error) {
	view, err := p.countView(request)
	if err != nil {
		return Plan{}, err
	}
	bindings, err := filterFieldBindings(view, request.Filters)
	if err != nil {
		return Plan{}, err
	}
	aliases, err := p.aliasesForFact(view.Fact, bindings)
	if err != nil {
		return Plan{}, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return Plan{}, err
	}
	whereParts, args, err := p.wherePartsPath(view, aliases, request.Filters)
	if err != nil {
		return Plan{}, err
	}
	sql := "SELECT COUNT(*) AS value\nFROM " + from + "\nWHERE " + strings.Join(whereParts, " AND ")
	_ = args
	_ = sql
	irGraph, irErr := p.buildFlatPlanIR(view.Fact, nil, nil, request.Filters, nil, 0, 0)
	if irErr != nil {
		return Plan{}, fmt.Errorf("build count plan IR: %w", irErr)
	}
	rendered, irErr := planir.RenderDuckDB(irGraph)
	if irErr != nil {
		return Plan{}, fmt.Errorf("render count plan IR: %w", irErr)
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, IR: irGraph}, nil
}

func (p *Planner) wherePartsPath(view *queryView, aliases pathAliasSet, filters []Filter) ([]string, []any, error) {
	whereParts := []string{"1 = 1"}
	args := []any{}
	for _, filter := range filters {
		part, partArgs, err := p.filterPartPath(view, aliases, filter)
		if err != nil {
			return nil, nil, err
		}
		if part != "" {
			whereParts = append(whereParts, part)
			args = append(args, partArgs...)
		}
	}
	return whereParts, args, nil
}

func (p *Planner) filterPartPath(view *queryView, aliases pathAliasSet, filter Filter) (string, []any, error) {
	if filter.Spatial != nil {
		if filter.Field != "" || len(filter.Groups) != 0 {
			return "", nil, fmt.Errorf("spatial filter cannot combine scalar or grouped filter fields")
		}
		_, latitude, latitudePath, err := view.ResolveDimensionRefPath(filter.Spatial.LatitudeField)
		if err != nil {
			return "", nil, err
		}
		_, longitude, longitudePath, err := view.ResolveDimensionRefPath(filter.Spatial.LongitudeField)
		if err != nil {
			return "", nil, err
		}
		latitudeExpr, err := dimensionExprForPath(latitude, aliases, latitudePath)
		if err != nil {
			return "", nil, err
		}
		longitudeExpr, err := dimensionExprForPath(longitude, aliases, longitudePath)
		if err != nil {
			return "", nil, err
		}
		return spatialFilterSQL(latitudeExpr, longitudeExpr, *filter.Spatial)
	}
	if len(filter.Groups) > 0 {
		parts := []string{}
		args := []any{}
		for _, group := range filter.Groups {
			groupParts := []string{}
			for _, child := range group.Filters {
				part, partArgs, err := p.filterPartPath(view, aliases, child)
				if err != nil {
					return "", nil, err
				}
				if part == "" {
					continue
				}
				groupParts = append(groupParts, part)
				args = append(args, partArgs...)
			}
			if len(groupParts) > 0 {
				parts = append(parts, "("+strings.Join(groupParts, " AND ")+")")
			}
		}
		if len(parts) == 0 {
			return "", nil, nil
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	}
	if filter.Field == "" {
		return "", nil, nil
	}
	_, dimension, path, err := view.ResolveDimensionRefPath(filter.Field)
	if err != nil {
		return "", nil, err
	}
	expr, err := dimensionExprForPath(dimension, aliases, path)
	if err != nil {
		return "", nil, err
	}
	return filterSQL(expr, filter)
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

func (m columnMaskSet) matchesDimension(ref string, dimension semanticmodel.MetricDimension) bool {
	if len(m) == 0 {
		return false
	}
	if _, ok := m[strings.ToLower(strings.TrimSpace(ref))]; ok {
		return true
	}
	return false
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

func maskedDimensionExprForPath(ref string, dimension semanticmodel.MetricDimension, aliases pathAliasSet, path []semanticmodel.Relationship, masks columnMaskSet) (string, error) {
	for _, key := range []string{ref, dimension.Field} {
		if mask, ok := masks[strings.ToLower(strings.TrimSpace(key))]; ok {
			return mask.SQL(), nil
		}
	}
	return dimensionExprForPath(dimension, aliases, path)
}

func maskedRawMetricExpr(model *semanticmodel.Model, ref string, metric resolvedAggregateMetric, aliases map[string]tableAlias, masks columnMaskSet) (string, error) {
	if mask, ok := masks[strings.ToLower(strings.TrimSpace(ref))]; ok {
		return mask.SQL(), nil
	}
	for _, dependency := range aggregateMetricPhysicalFields(metric) {
		if mask, ok := masks[strings.ToLower(strings.TrimSpace(dependency))]; ok {
			return mask.SQL(), nil
		}
	}
	return rawAggregateMetricExpr(model, metric, aliases)
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

func sortSQL(sorts []Sort, columns map[string]bool) ([]string, error) {
	parts := make([]string, 0, len(sorts))
	for _, sort := range sorts {
		field, err := quoteIdent(sort.Field)
		if err != nil {
			return nil, err
		}
		if !columns[field] {
			return nil, fmt.Errorf("sort field %q is not a selected output alias", sort.Field)
		}
		direction := "ASC"
		switch {
		case sort.Direction == "" || strings.EqualFold(sort.Direction, "asc"):
			direction = "ASC"
		case strings.EqualFold(sort.Direction, "desc"):
			direction = "DESC"
		default:
			return nil, fmt.Errorf("unsupported sort direction %q", sort.Direction)
		}
		parts = append(parts, field+" "+direction)
	}
	return parts, nil
}

func writeOrderLimitOffset(sql *strings.Builder, sorts []Sort, columns map[string]bool, limit, offset int) error {
	effective := effectiveOrderSorts(sorts, columns)
	if len(effective) > 0 {
		parts, err := sortSQL(effective, columns)
		if err != nil {
			return err
		}
		sql.WriteString("\nORDER BY ")
		sql.WriteString(strings.Join(parts, ", "))
	}
	return writeLimitOffset(sql, limit, offset)
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

func writeLimitOffset(sql *strings.Builder, limit, offset int) error {
	if limit > 0 {
		sql.WriteString(fmt.Sprintf("\nLIMIT %d", limit))
	}
	if offset > 0 {
		if limit <= 0 {
			return fmt.Errorf("offset requires limit")
		}
		sql.WriteString(fmt.Sprintf("\nOFFSET %d", offset))
	}
	return nil
}
