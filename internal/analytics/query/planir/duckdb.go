package planir

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rendered is the renderer boundary result. Literal values are always returned
// separately in Args and represented by placeholders in SQL.
type Rendered struct {
	SQL     string
	Args    []any
	Columns []string
}

// DuckDBRenderer is intentionally narrower than a generic SQL renderer. A
// renderer consumes a validated typed graph and owns DuckDB quoting and
// aggregate syntax. No authored SQL is accepted by this boundary.
type DuckDBRenderer interface {
	RenderDuckDB(*Graph) (Rendered, error)
}

type Renderer struct{}

func (Renderer) RenderDuckDB(graph *Graph) (Rendered, error) { return RenderDuckDB(graph) }

// RenderDuckDB renders every closed PlanIR node. Source nodes are lowered to
// governed DuckDB relations, aggregate roots are reduced independently, and
// all post-aggregate operations are rendered through typed CTE boundaries.
func RenderDuckDB(graph *Graph) (Rendered, error) {
	if graph == nil {
		return Rendered{}, fmt.Errorf("plan graph is nil")
	}
	if err := graph.Validate(); err != nil {
		return Rendered{}, err
	}
	if _, ok := asAggregate(graph.Nodes[graph.Output]); !ok {
		if _, bundle := asBundle(graph.Nodes[graph.Output]); !bundle {
			if _, stitch := asStitch(graph.Nodes[graph.Output]); !stitch {
				if _, ratio := asRatio(graph.Nodes[graph.Output]); !ratio {
					if _, derived := asDerived(graph.Nodes[graph.Output]); !derived {
						if _, sortLimit := asSortLimit(graph.Nodes[graph.Output]); !sortLimit {
							return Rendered{}, fmt.Errorf("duckdb renderer supports AggregateMetrics output and post-aggregate/bundle outputs, got %s", graph.Nodes[graph.Output].Kind())
						}
					}
				}
			}
		}
	}
	r := &duckRenderer{graph: graph, names: map[string]string{}, done: map[string]bool{}, useCount: map[string]int{}}
	countNodeUses(graph, graph.Output, r.useCount)
	root, columns, err := r.renderNode(graph.Output)
	if err != nil {
		return Rendered{}, err
	}
	var sql string
	if len(r.ctes) > 0 {
		sql = "WITH " + strings.Join(r.ctes, ",\n") + "\n"
	}
	output := graph.Nodes[graph.Output]
	if _, ok := asBundle(output); ok {
		// renderNode already returns the complete UNION statement for an envelope.
		sql += root
		return Rendered{SQL: sql, Args: r.args, Columns: columns}, nil
	}
	if _, ok := asSortLimit(output); ok {
		sql += root
	} else if _, ok := asAggregate(output); ok && len(r.ctes) == 0 {
		// Preserve the compact canonical form for a direct aggregate graph.
		sql += root
	} else {
		sql += "SELECT * FROM " + quoteName(root)
	}
	return Rendered{SQL: sql, Args: r.args, Columns: columns}, nil
}

type duckRenderer struct {
	graph    *Graph
	args     []any
	ctes     []string
	names    map[string]string
	done     map[string]bool
	useCount map[string]int
	seq      int
}

type sourceContext struct {
	from        string
	where       []string
	aliases     map[string][]string
	pathAliases map[string]string
	lineage     []PhysicalLineage
	root        string
	joined      bool
	lastAlias   string
	path        []string
}

func (r *duckRenderer) renderNode(id string) (string, []string, error) {
	if id == "" {
		return "", nil, fmt.Errorf("renderer node id is empty")
	}
	if name, ok := r.names[id]; ok {
		node := r.graph.Nodes[id]
		return name, nodeColumns(node), nil
	}
	node, ok := r.graph.Nodes[id]
	if !ok || node == nil {
		return "", nil, fmt.Errorf("renderer node %q is unavailable", id)
	}
	switch value := node.(type) {
	case ScanDataset, *ScanDataset, TraverseRelationship, *TraverseRelationship, FilterRows, *FilterRows:
		return r.renderSource(id)
	case AggregateMetrics, *AggregateMetrics:
		n, ok := asAggregate(value)
		if !ok {
			return "", nil, fmt.Errorf("aggregate node %q is nil", id)
		}
		ctx, err := r.source(n.Input)
		if err != nil {
			return "", nil, err
		}
		parts := make([]string, 0, len(n.GroupBy)+len(n.Metrics))
		for _, field := range n.GroupBy {
			expr, err := r.fieldExpr(field, ctx)
			if err != nil {
				return "", nil, err
			}
			expr = renderSpatialBucketExpr(expr, field, n.Spatial)
			expr = renderTimeBucketExpr(expr, field, n.TimeBuckets)
			alias := columnName(field)
			if expr == quoteName(alias) {
				parts = append(parts, expr)
			} else {
				parts = append(parts, expr+" AS "+quoteName(alias))
			}
		}
		for _, metric := range n.Metrics {
			expr, err := renderMetricWithResolver(metric, &r.args, func(name string) (string, error) {
				return r.fieldExpr(name, ctx)
			}, func(filter AggregateFilter) (string, error) { return r.aggregateFilterGuard(filter, ctx) })
			if err != nil {
				return "", nil, err
			}
			if metric.Empty == "zero" && !strings.EqualFold(metric.Aggregation, "COUNT") && !strings.EqualFold(metric.Aggregation, "COUNT_DISTINCT") {
				expr = "COALESCE(" + expr + ", 0)"
			}
			parts = append(parts, expr+" AS "+quoteName(columnName(metric.Name)))
		}
		if len(parts) == 0 {
			return "", nil, fmt.Errorf("aggregate node %q has no projection", id)
		}
		from := ctx.from
		if len(ctx.where) > 0 {
			from += " WHERE " + strings.Join(ctx.where, " AND ")
		}
		sql := "SELECT " + strings.Join(parts, ", ") + " FROM " + from
		if len(n.GroupBy) > 0 {
			groups := make([]string, len(n.GroupBy))
			for i, field := range n.GroupBy {
				groups[i], _ = r.fieldExpr(field, ctx)
				groups[i] = renderSpatialBucketExpr(groups[i], field, n.Spatial)
				groups[i] = renderTimeBucketExpr(groups[i], field, n.TimeBuckets)
			}
			sql += " GROUP BY " + strings.Join(groups, ", ")
		}
		if id == r.graph.Output {
			r.names[id] = "(" + sql + ")"
			return sql, nodeColumns(node), nil
		}
		name := r.cteName(id)
		r.ctes = append(r.ctes, name+" AS ("+sql+")")
		r.names[id] = name
		return name, nodeColumns(node), nil
	case StitchAggregates, *StitchAggregates:
		n, ok := asStitch(value)
		if !ok {
			return "", nil, fmt.Errorf("stitch node %q is nil", id)
		}
		return r.renderStitch(id, n)
	case ComputeRatio, *ComputeRatio:
		n, ok := asRatio(value)
		if !ok {
			return "", nil, fmt.Errorf("ratio node %q is nil", id)
		}
		return r.renderCompute(id, n.Input, n.Output, "("+quoteName(n.Numerator)+" / NULLIF("+quoteName(n.Denominator)+", 0))")
	case ComputeDerived, *ComputeDerived:
		n, ok := asDerived(value)
		if !ok {
			return "", nil, fmt.Errorf("derived node %q is nil", id)
		}
		expr, err := renderScalarWithResolver(n.Expression, &r.args, func(name string) (string, error) { return quoteName(name), nil })
		if err != nil {
			return "", nil, err
		}
		return r.renderCompute(id, n.Input, n.Output, expr)
	case SortLimit, *SortLimit:
		n, ok := asSortLimit(value)
		if !ok {
			return "", nil, fmt.Errorf("sort-limit node %q is nil", id)
		}
		input, columns, err := r.renderNode(n.Input)
		if err != nil {
			return "", nil, err
		}
		from := quoteName(input)
		var source *sourceContext
		if _, ok := asAggregate(r.graph.Nodes[n.Input]); !ok {
			if _, ok := asStitch(r.graph.Nodes[n.Input]); !ok {
				if _, ok := asComputeSource(r.graph.Nodes[n.Input]); !ok {
					ctx, sourceErr := r.source(n.Input)
					if sourceErr == nil {
						from = ctx.from
						source = &ctx
					}
				}
			}
		}
		selectSQL := "*"
		if len(n.Projection) > 0 {
			parts := make([]string, 0, len(n.Projection))
			for _, projection := range n.Projection {
				if err := validName(projection.Source); err != nil {
					return "", nil, fmt.Errorf("projection source %q: %w", projection.Source, err)
				}
				expr := quoteName(columnName(projection.Source))
				if source != nil {
					expr, err = r.fieldExpr(projection.Source, *source)
					if err != nil {
						return "", nil, err
					}
				}
				if projection.Mask != "" {
					switch strings.ToLower(projection.Mask) {
					case "null":
						expr = "NULL"
					case "redact", "redacted":
						expr = "'REDACTED'"
					case "zero":
						expr = "0"
					default:
						return "", nil, fmt.Errorf("unsupported projection mask %q", projection.Mask)
					}
				}
				parts = append(parts, expr+" AS "+quoteName(columnName(projection.Name)))
			}
			selectSQL = strings.Join(parts, ", ")
			columns = projectionColumns(n.Projection)
		}
		sql := "SELECT " + selectSQL + " FROM " + from
		if source != nil && len(source.where) > 0 {
			sql += " WHERE " + strings.Join(source.where, " AND ")
		}
		if len(n.Sort) > 0 {
			keys := make([]string, len(n.Sort))
			for i, key := range n.Sort {
				keys[i] = quoteName(columnName(key.Field))
				if key.Descending {
					keys[i] += " DESC"
				} else {
					keys[i] += " ASC"
				}
			}
			sql += " ORDER BY " + strings.Join(keys, ", ")
		}
		if n.Limit > 0 {
			sql += fmt.Sprintf(" LIMIT %d", n.Limit)
		}
		if n.Offset > 0 {
			sql += fmt.Sprintf(" OFFSET %d", n.Offset)
		}
		if id == r.graph.Output {
			return sql, columns, nil
		}
		name := r.cteName(id)
		r.ctes = append(r.ctes, name+" AS ("+sql+")")
		r.names[id] = name
		return name, columns, nil
	case BundleBranches, *BundleBranches:
		n, ok := asBundle(value)
		if !ok {
			return "", nil, fmt.Errorf("bundle node %q is nil", id)
		}
		return r.renderBundle(id, n)
	default:
		return "", nil, fmt.Errorf("unsupported node kind %q", node.Kind())
	}
}

func renderSpatialBucketExpr(expr, field string, bucket *SpatialBucket) string {
	if bucket == nil {
		return expr
	}
	globalCells := (1 << bucket.Zoom) * (256 / bucket.CellPixels)
	if globalCells <= 0 {
		return expr
	}
	if field == bucket.Longitude {
		return fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR(((%s) + 180) / 360 * %d)))", globalCells-1, expr, globalCells)
	}
	if field == bucket.Latitude {
		const maxLatitude = 85.0511287798066
		clamped := fmt.Sprintf("LEAST(%.17g, GREATEST(-%.17g, (%s)))", maxLatitude, maxLatitude, expr)
		return fmt.Sprintf("LEAST(%d, GREATEST(0, FLOOR((1 - LN(TAN(RADIANS(%s)) + 1 / COS(RADIANS(%s))) / PI()) / 2 * %d)))", globalCells-1, clamped, clamped, globalCells)
	}
	return expr
}

func renderTimeBucketExpr(expr, field string, buckets []TimeBucket) string {
	for _, bucket := range buckets {
		if bucket.Field != field || bucket.Grain == "" {
			continue
		}
		if bucket.DateTimeTZ && bucket.Timezone != "" {
			tz := strings.ReplaceAll(bucket.Timezone, "'", "''")
			expr = "timezone('" + tz + "', CAST(" + expr + " AS TIMESTAMPTZ))"
		}
		if bucket.Grain == "week" && strings.EqualFold(bucket.WeekStart, "sunday") {
			return "DATE_TRUNC('week', " + expr + " + INTERVAL 1 DAY) - INTERVAL 1 DAY"
		}
		return "DATE_TRUNC('" + bucket.Grain + "', " + expr + ")"
	}
	return expr
}

func (r *duckRenderer) aggregateFilterGuard(filter AggregateFilter, ctx sourceContext) (string, error) {
	if !filter.MatchGuard || len(filter.RelationshipRoutes) == 0 {
		return "", nil
	}
	guards := make([]string, 0, len(filter.RelationshipRoutes))
	for _, route := range filter.RelationshipRoutes {
		if len(route.Edges) == 0 {
			continue
		}
		edge := route.Edges[len(route.Edges)-1]
		alias := ctx.pathAliases[strings.Join(routeEdgeNames(route), "/")]
		if alias == "" {
			alias = ctx.pathAliases[edge.Name]
		}
		if alias == "" {
			return "", fmt.Errorf("aggregate filter %q route %q has no rendered alias", filter.Name, edge.Name)
		}
		if len(edge.JoinKeys) == 0 {
			continue
		}
		guards = append(guards, quoteName(alias)+"."+quoteName(columnName(edge.JoinKeys[len(edge.JoinKeys)-1].To))+" IS NOT NULL")
	}
	if len(guards) == 0 {
		return "", nil
	}
	return strings.Join(guards, " AND "), nil
}

func routeEdgeNames(route RelationshipRoute) []string {
	result := make([]string, len(route.Edges))
	for index, edge := range route.Edges {
		result[index] = edge.Name
	}
	return result
}

func (r *duckRenderer) renderSource(id string) (string, []string, error) {
	ctx, err := r.source(id)
	if err != nil {
		return "", nil, err
	}
	return ctx.from, nodeColumns(r.graph.Nodes[id]), nil
}

func (r *duckRenderer) source(id string) (sourceContext, error) {
	node, ok := r.graph.Nodes[id]
	if !ok || node == nil {
		return sourceContext{}, fmt.Errorf("source node %q is unavailable", id)
	}
	switch n := node.(type) {
	case ScanDataset:
		return r.scanContext(id, n), nil
	case *ScanDataset:
		if n == nil {
			return sourceContext{}, fmt.Errorf("source node %q is nil", id)
		}
		return r.scanContext(id, *n), nil
	case FilterRows:
		return r.sourceFilter(n)
	case *FilterRows:
		if n == nil {
			return sourceContext{}, fmt.Errorf("source node %q is nil", id)
		}
		return r.sourceFilter(*n)
	case TraverseRelationship:
		return r.sourceTraverse(n)
	case *TraverseRelationship:
		if n == nil {
			return sourceContext{}, fmt.Errorf("source node %q is nil", id)
		}
		return r.sourceTraverse(*n)
	default:
		return sourceContext{}, fmt.Errorf("node %q is not a governed source", id)
	}
}

func countNodeUses(graph *Graph, id string, counts map[string]int) {
	if id == "" || graph == nil {
		return
	}
	node, ok := graph.Nodes[id]
	if !ok || node == nil {
		return
	}
	counts[id]++
	for _, input := range node.Inputs() {
		countNodeUses(graph, input, counts)
	}
}

func (r *duckRenderer) scanContext(id string, n ScanDataset) sourceContext {
	if err := validName(n.Dataset); err != nil {
		return sourceContext{from: "", root: n.Dataset, lineage: append([]PhysicalLineage(nil), n.PhysicalLineage...)}
	}
	relation := n.Relation
	if relation == "" {
		relation = quoteName(n.Dataset)
	}
	if r.useCount[id] > 1 {
		name := r.cteName(id)
		if !r.done[id] {
			r.ctes = append(r.ctes, name+" AS MATERIALIZED (SELECT * FROM "+relation+")")
			r.done[id] = true
		}
		relation = name
	}
	return sourceContext{from: relation, root: n.Dataset, aliases: map[string][]string{n.Dataset: nil}, pathAliases: map[string]string{"": ""}, lineage: append([]PhysicalLineage(nil), n.PhysicalLineage...)}
}

func (r *duckRenderer) sourceFilter(n FilterRows) (sourceContext, error) {
	ctx, err := r.source(n.Input)
	if err != nil {
		return sourceContext{}, err
	}
	ctx.lineage = append(ctx.lineage, n.PhysicalLineage...)
	resolve := func(name string) (string, error) { return r.fieldExpr(name, ctx) }
	var predicate string
	if n.MatchGuard && len(n.FieldRoutes) > 0 {
		predicate, err = renderPredicateWithFieldGuard(n.Predicate, &r.args, resolve, func(field string) (string, error) {
			return r.filterFieldGuard(n.FieldRoutes[field], ctx)
		})
	} else {
		predicate, err = renderPredicateWithResolver(n.Predicate, &r.args, resolve)
	}
	if err != nil {
		return sourceContext{}, err
	}
	ctx.where = append(ctx.where, predicate)
	return ctx, nil
}

func (r *duckRenderer) filterFieldGuard(routes []RelationshipRoute, ctx sourceContext) (string, error) {
	if len(routes) == 0 {
		return "", nil
	}
	guards := make([]string, 0, len(routes))
	for _, route := range routes {
		if len(route.Edges) == 0 {
			continue
		}
		edge := route.Edges[len(route.Edges)-1]
		if len(edge.JoinKeys) == 0 {
			continue
		}
		alias := ctx.pathAliases[strings.Join(routeEdgeNames(route), "/")]
		if alias == "" {
			alias = ctx.pathAliases[edge.Name]
		}
		if alias == "" {
			return "", fmt.Errorf("filter route %q has no rendered alias", edge.Name)
		}
		guards = append(guards, quoteName(alias)+"."+quoteName(columnName(edge.JoinKeys[len(edge.JoinKeys)-1].To))+" IS NOT NULL")
	}
	return strings.Join(guards, " AND "), nil
}

func (r *duckRenderer) sourceTraverse(n TraverseRelationship) (sourceContext, error) {
	ctx, err := r.source(n.Input)
	if err != nil {
		return sourceContext{}, err
	}
	if err := validName(n.Path.ToDataset); err != nil {
		return sourceContext{}, fmt.Errorf("relationship target %q: %w", n.Path.ToDataset, err)
	}
	toRelation := n.Path.ToRelation
	if toRelation == "" {
		toRelation = quoteName(n.Path.ToDataset)
	}
	left := n.Path.FromDataset
	leftAlias := ctx.latestAlias(left)
	joined := ctx.joined
	if leftAlias == "" {
		leftAlias = "r0"
		ctx.from = ctx.from + " AS " + quoteName(leftAlias)
		ctx.aliases[ctx.root] = []string{leftAlias}
		joined = true
	}
	alias := fmt.Sprintf("r%d", len(ctx.aliases)+1)
	ctx.from += " LEFT JOIN " + toRelation + " AS " + quoteName(alias) + " ON "
	parts := make([]string, 0, len(n.Path.JoinKeys))
	for _, key := range n.Path.JoinKeys {
		if err := validName(key.From); err != nil {
			return sourceContext{}, fmt.Errorf("relationship key %q: %w", key.From, err)
		}
		if err := validName(key.To); err != nil {
			return sourceContext{}, fmt.Errorf("relationship key %q: %w", key.To, err)
		}
		parts = append(parts, quoteName(leftAlias)+"."+quoteName(key.From)+" = "+quoteName(alias)+"."+quoteName(key.To))
	}
	ctx.from += strings.Join(parts, " AND ")
	ctx.aliases[n.Path.ToDataset] = append(ctx.aliases[n.Path.ToDataset], alias)
	ctx.path = append(append([]string(nil), ctx.path...), n.Path.Name)
	if ctx.pathAliases == nil {
		ctx.pathAliases = map[string]string{}
	}
	ctx.pathAliases[strings.Join(ctx.path, "/")] = alias
	ctx.pathAliases[n.Path.Name] = alias
	ctx.joined = joined
	ctx.lastAlias = alias
	ctx.lineage = append(ctx.lineage, n.PhysicalLineage...)
	return ctx, nil
}

func (c sourceContext) latestAlias(dataset string) string {
	values := c.aliases[dataset]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

func (r *duckRenderer) fieldExpr(field string, ctx sourceContext) (string, error) {
	if field == "__scalar_key" {
		return "1", nil
	}
	for i := len(ctx.lineage) - 1; i >= 0; i-- {
		lineage := ctx.lineage[i]
		if lineage.Logical != field {
			continue
		}
		if len(lineage.Route) > 0 {
			if alias := ctx.pathAliases[strings.Join(lineage.Route, "/")]; alias != "" {
				return quoteName(alias) + "." + quoteName(columnName(lineage.Field)), nil
			}
		}
		if alias := ctx.latestAlias(lineage.Dataset); alias != "" {
			return quoteName(alias) + "." + quoteName(columnName(lineage.Field)), nil
		}
		return quoteName(columnName(lineage.Field)), nil
	}
	if strings.Contains(field, ".") {
		parts := strings.Split(field, ".")
		if len(parts) == 2 {
			if alias := ctx.latestAlias(parts[0]); alias != "" {
				return quoteName(alias) + "." + quoteName(parts[1]), nil
			}
			return quoteName(parts[0]) + "." + quoteName(parts[1]), nil
		}
	}
	if alias := ctx.latestAlias(ctx.root); alias != "" {
		return quoteName(alias) + "." + quoteName(field), nil
	}
	return quoteName(columnName(field)), nil
}

func (r *duckRenderer) renderStitch(id string, n StitchAggregates) (string, []string, error) {
	if len(n.InputsList) < 2 {
		return "", nil, fmt.Errorf("stitch node %q requires two inputs", id)
	}
	left, _, err := r.renderNode(n.InputsList[0])
	if err != nil {
		return "", nil, err
	}
	leftName := left
	available := map[string]bool{}
	for _, metric := range r.graph.Nodes[n.InputsList[0]].Meta().AvailableMetrics {
		available[metric.Name] = true
	}
	for i, inputID := range n.InputsList[1:] {
		right, _, err := r.renderNode(inputID)
		if err != nil {
			return "", nil, err
		}
		rightMeta := r.graph.Nodes[inputID].Meta()
		selects := make([]string, 0, len(n.Keys)+len(n.AvailableMetrics))
		joins := make([]string, 0, len(n.Keys))
		for _, key := range n.Keys {
			column := columnName(key)
			selects = append(selects, "COALESCE(l."+quoteName(column)+", r."+quoteName(column)+") AS "+quoteName(column))
			joins = append(joins, "l."+quoteName(column)+" IS NOT DISTINCT FROM r."+quoteName(column))
		}
		for _, metric := range n.AvailableMetrics {
			if available[metric.Name] {
				value := "l." + quoteName(columnName(metric.Name))
				value = "COALESCE(" + value + ", 0)"
				selects = append(selects, value+" AS "+quoteName(columnName(metric.Name)))
				continue
			}
			for _, candidate := range rightMeta.AvailableMetrics {
				if candidate.Name == metric.Name {
					value := "r." + quoteName(columnName(metric.Name))
					value = "COALESCE(" + value + ", 0)"
					selects = append(selects, value+" AS "+quoteName(columnName(metric.Name)))
					available[metric.Name] = true
					break
				}
			}
		}
		name := r.cteName(fmt.Sprintf("%s_stitch_%d", id, i+1))
		r.ctes = append(r.ctes, name+" AS (SELECT "+strings.Join(selects, ", ")+" FROM "+quoteName(leftName)+" l FULL OUTER JOIN "+quoteName(right)+" r ON "+strings.Join(joins, " AND ")+")")
		leftName = name
	}
	r.names[id] = leftName
	return leftName, nodeColumns(n), nil
}

func stitchMetricZero(node Node, name string) bool {
	aggregate, ok := asAggregate(node)
	if !ok {
		return false
	}
	for _, metric := range aggregate.Metrics {
		if metric.Name == name {
			return metric.Empty == "zero" || strings.EqualFold(metric.Aggregation, "COUNT") || strings.EqualFold(metric.Aggregation, "COUNT_DISTINCT")
		}
	}
	return false
}

func (r *duckRenderer) renderCompute(id, input, output, expression string) (string, []string, error) {
	child, columns, err := r.renderNode(input)
	if err != nil {
		return "", nil, err
	}
	name := r.cteName(id)
	r.ctes = append(r.ctes, name+" AS (SELECT *, "+expression+" AS "+quoteName(output)+" FROM "+quoteName(child)+")")
	r.names[id] = name
	return name, append(columns, output), nil
}

func (r *duckRenderer) renderBundle(id string, n BundleBranches) (string, []string, error) {
	// Bundle outputs are heterogeneous. Each branch is projected to the union
	// of its typed fields/metrics, with branch identity and deterministic row
	// order added at this closed boundary.
	type branchResult struct {
		sql     string
		columns []string
		ordinal int
		order   []string
	}
	branches := make([]branchResult, 0, len(n.Branches))
	unionColumns := map[string]bool{}
	for _, branch := range n.Branches {
		relation, columns, err := r.renderNode(branch.Input)
		if err != nil {
			return "", nil, err
		}
		for _, column := range columns {
			unionColumns[column] = true
		}
		order := []string{}
		if sortLimit, ok := asSortLimit(r.graph.Nodes[branch.Input]); ok {
			projectionNames := map[string]string{}
			for _, projection := range sortLimit.Projection {
				projectionNames[projection.Source] = projection.Name
			}
			for _, key := range sortLimit.Sort {
				field := key.Field
				if projected, exists := projectionNames[field]; exists {
					field = projected
				}
				direction := "ASC"
				if key.Descending {
					direction = "DESC"
				}
				order = append(order, quoteName(columnName(field))+" "+direction)
			}
		}
		if len(order) == 0 {
			for _, column := range columns {
				order = append(order, quoteName(column)+" ASC")
			}
		}
		branches = append(branches, branchResult{relation, columns, branch.Ordinal, order})
	}
	all := make([]string, 0, len(unionColumns))
	for column := range unionColumns {
		all = append(all, column)
	}
	sort.Strings(all)
	parts := make([]string, 0, len(branches))
	for branchIndex, branch := range branches {
		set := map[string]bool{}
		for _, column := range branch.columns {
			set[column] = true
		}
		selects := []string{fmt.Sprintf("CAST(%d AS BIGINT) AS __bundle_branch", branch.ordinal), "ROW_NUMBER() OVER (ORDER BY " + strings.Join(branch.order, ", ") + ") AS __bundle_row"}
		for otherIndex, otherBranch := range branches {
			for _, column := range all {
				physical := fmt.Sprintf("__bundle_%d_%s", otherBranch.ordinal, column)
				if otherIndex == branchIndex && set[column] {
					selects = append(selects, quoteName(column)+" AS "+quoteName(physical))
				} else {
					selects = append(selects, "NULL AS "+quoteName(physical))
				}
			}
		}
		parts = append(parts, "SELECT "+strings.Join(selects, ", ")+" FROM "+quoteName(branch.sql))
	}
	physicalColumns := []string{"__bundle_branch", "__bundle_row"}
	for _, branch := range branches {
		for _, column := range all {
			physicalColumns = append(physicalColumns, fmt.Sprintf("__bundle_%d_%s", branch.ordinal, column))
		}
	}
	return strings.Join(parts, " UNION ALL ") + " ORDER BY __bundle_branch ASC, __bundle_row ASC", physicalColumns, nil
}

func (r *duckRenderer) cteName(id string) string {
	if name, ok := r.names[id]; ok {
		return name
	}
	name := "p_" + regexp.MustCompile(`[^A-Za-z0-9_]`).ReplaceAllString(id, "_")
	if name == "p_" {
		r.seq++
		name += fmt.Sprint(r.seq)
	}
	r.names[id] = name
	return name
}

func nodeColumns(node Node) []string {
	if node == nil {
		return nil
	}
	m := node.Meta()
	columns := make([]string, 0, len(m.AvailableFields)+len(m.AvailableMetrics))
	for _, field := range m.AvailableFields {
		columns = append(columns, field.Name)
	}
	for _, metric := range m.AvailableMetrics {
		columns = append(columns, metric.Name)
	}
	return uniqueColumns(columns)
}

func projectionColumns(values []Projection) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = value.Name
	}
	return out
}
func uniqueColumns(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func asAggregate(node Node) (AggregateMetrics, bool) {
	switch value := node.(type) {
	case AggregateMetrics:
		return value, true
	case *AggregateMetrics:
		if value != nil {
			return *value, true
		}
	}
	return AggregateMetrics{}, false
}
func asStitch(node Node) (StitchAggregates, bool) {
	switch value := node.(type) {
	case StitchAggregates:
		return value, true
	case *StitchAggregates:
		if value != nil {
			return *value, true
		}
	}
	return StitchAggregates{}, false
}
func asRatio(node Node) (ComputeRatio, bool) {
	switch value := node.(type) {
	case ComputeRatio:
		return value, true
	case *ComputeRatio:
		if value != nil {
			return *value, true
		}
	}
	return ComputeRatio{}, false
}
func asDerived(node Node) (ComputeDerived, bool) {
	switch value := node.(type) {
	case ComputeDerived:
		return value, true
	case *ComputeDerived:
		if value != nil {
			return *value, true
		}
	}
	return ComputeDerived{}, false
}
func asSortLimit(node Node) (SortLimit, bool) {
	switch value := node.(type) {
	case SortLimit:
		return value, true
	case *SortLimit:
		if value != nil {
			return *value, true
		}
	}
	return SortLimit{}, false
}
func asComputeSource(node Node) (Node, bool) {
	switch node.(type) {
	case ComputeRatio, *ComputeRatio, ComputeDerived, *ComputeDerived:
		return node, true
	default:
		return nil, false
	}
}
func asBundle(node Node) (BundleBranches, bool) {
	switch value := node.(type) {
	case BundleBranches:
		return value, true
	case *BundleBranches:
		if value != nil {
			return *value, true
		}
	}
	return BundleBranches{}, false
}

func renderPredicate(predicate Predicate, args *[]any) (string, error) {
	return renderPredicateWithResolver(predicate, args, func(name string) (string, error) { return quoteName(name), nil })
}
func renderPredicateWithResolver(predicate Predicate, args *[]any, resolve func(string) (string, error)) (string, error) {
	switch predicate.Kind {
	case PredicateCompare:
		value, err := bindLiteral(predicate.Value, args)
		if err != nil {
			return "", err
		}
		field, err := resolve(predicate.Field)
		if err != nil {
			return "", err
		}
		return field + " " + strings.ToUpper(predicate.Operator) + " " + value, nil
	case PredicateIsNull:
		field, err := resolve(predicate.Field)
		if err != nil {
			return "", err
		}
		op := "IS NULL"
		if predicate.Negated {
			op = "IS NOT NULL"
		}
		return field + " " + op, nil
	case PredicateIn:
		field, err := resolve(predicate.Field)
		if err != nil {
			return "", err
		}
		values := make([]string, len(predicate.Values))
		for i, value := range predicate.Values {
			values[i], err = bindLiteral(value, args)
			if err != nil {
				return "", err
			}
		}
		return field + " IN (" + strings.Join(values, ", ") + ")", nil
	case PredicateAnd, PredicateOr:
		parts := make([]string, len(predicate.Children))
		join := " AND "
		if predicate.Kind == PredicateOr {
			join = " OR "
		}
		for i, child := range predicate.Children {
			var err error
			parts[i], err = renderPredicateWithResolver(child, args, resolve)
			if err != nil {
				return "", err
			}
		}
		return "(" + strings.Join(parts, join) + ")", nil
	case PredicateNot:
		if len(predicate.Children) != 1 {
			return "", fmt.Errorf("not predicate requires one child")
		}
		child, err := renderPredicateWithResolver(predicate.Children[0], args, resolve)
		if err != nil {
			return "", err
		}
		return "NOT (" + child + ")", nil
	default:
		return "", fmt.Errorf("unsupported predicate kind %q", predicate.Kind)
	}
}

func bindLiteral(value Literal, args *[]any) (string, error) {
	switch value.Kind {
	case LiteralString:
		*args = append(*args, value.String)
	case LiteralNumber:
		if !exactNumber.MatchString(value.NumberText) {
			return "", fmt.Errorf("number literal requires an exact token")
		}
		*args = append(*args, value.NumberText)
		return "CAST(? AS DECIMAL)", nil
	case LiteralBool:
		*args = append(*args, value.Bool)
	default:
		return "", fmt.Errorf("unsupported bound literal kind %q", value.Kind)
	}
	return "?", nil
}

var safeName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

func validName(name string) error {
	if name == "" || !safeName.MatchString(name) {
		return fmt.Errorf("invalid identifier")
	}
	return nil
}
func quoteName(name string) string {
	parts := strings.Split(name, ".")
	for i := range parts {
		parts[i] = `"` + strings.ReplaceAll(parts[i], `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

func columnName(name string) string {
	parts := strings.Split(name, ".")
	return parts[len(parts)-1]
}

func renderMetric(metric MetricSpec, args *[]any) (string, error) {
	return renderMetricWithResolver(metric, args, func(name string) (string, error) { return quoteName(name), nil })
}
func renderMetricWithResolver(metric MetricSpec, args *[]any, resolve func(string) (string, error), guards ...func(AggregateFilter) (string, error)) (string, error) {
	if err := validName(metric.Name); err != nil {
		return "", fmt.Errorf("metric %q: %w", metric.Name, err)
	}
	aggregation := strings.ToUpper(metric.Aggregation)
	if metric.Input == "" {
		if !strings.EqualFold(aggregation, "COUNT_DISTINCT_PAIR") && !strings.EqualFold(aggregation, "COUNT_STAR") {
			return "", fmt.Errorf("metric %q %s requires an input", metric.Name, aggregation)
		}
	}
	input := ""
	var err error
	if metric.Input != "" {
		input, err = resolve(metric.Input)
		if err != nil {
			return "", err
		}
	}
	expression := ""
	switch aggregation {
	case "COUNT_STAR":
		expression = "COUNT(*)"
	case "COUNT":
		expression = "COUNT(" + input + ")"
	case "SUM", "AVG", "MIN", "MAX":
		expression = aggregation + "(" + input + ")"
	case "COUNT_DISTINCT":
		expression = "COUNT(DISTINCT " + input + ")"
	case "COUNT_DISTINCT_PAIR":
		if len(metric.Inputs) != 2 {
			return "", fmt.Errorf("metric %q COUNT_DISTINCT_PAIR requires two inputs", metric.Name)
		}
		left, leftErr := resolve(metric.Inputs[0])
		if leftErr != nil {
			return "", leftErr
		}
		right, rightErr := resolve(metric.Inputs[1])
		if rightErr != nil {
			return "", rightErr
		}
		expression = "COUNT(DISTINCT (" + left + ", " + right + "))"
	default:
		return "", fmt.Errorf("metric %q has unsupported aggregation %q", metric.Name, metric.Aggregation)
	}
	if len(metric.Filters) == 0 {
		return expression, nil
	}
	parts := make([]string, len(metric.Filters))
	for i, filter := range metric.Filters {
		predicateRender := renderPredicateWithResolver
		if filter.MatchGuard && len(filter.RelationshipRoutes) > 0 {
			parts[i], err = renderPredicateWithFieldGuard(filter.Predicate, args, resolve, func(field string) (string, error) {
				return routeGuardForField(filter, field, resolve)
			})
		} else {
			parts[i], err = predicateRender(filter.Predicate, args, resolve)
		}
		if err != nil {
			return "", fmt.Errorf("metric %q filter %q: %w", metric.Name, filter.Name, err)
		}
		// Relationship match guards are applied to joined predicate leaves by
		// renderPredicateWithFieldGuard above, preserving authored boolean tree
		// semantics. The optional callback remains for callers that need to
		// validate route aliases without widening an OR subtree.
		if len(guards) > 0 {
			if _, guardErr := guards[0](filter); guardErr != nil {
				return "", guardErr
			}
		}
	}
	return expression + " FILTER (WHERE " + strings.Join(parts, " AND ") + ")", nil
}

func routeGuardForField(filter AggregateFilter, field string, resolve func(string) (string, error)) (string, error) {
	routes := filter.FieldRoutes[field]
	if len(routes) == 0 && len(filter.FieldRoutes) > 0 {
		return "", nil
	}
	if len(routes) == 0 {
		routes = filter.RelationshipRoutes
	}
	guards := []string{}
	for _, route := range routes {
		if len(route.Edges) == 0 {
			continue
		}
		edge := route.Edges[len(route.Edges)-1]
		if len(edge.JoinKeys) == 0 {
			continue
		}
		key, err := resolve(edge.ToDataset + "." + edge.JoinKeys[len(edge.JoinKeys)-1].To)
		if err != nil {
			continue
		}
		guards = append(guards, key+" IS NOT NULL")
	}
	return strings.Join(guards, " AND "), nil
}

func renderPredicateWithFieldGuard(predicate Predicate, args *[]any, resolve func(string) (string, error), guard func(string) (string, error)) (string, error) {
	switch predicate.Kind {
	case PredicateCompare, PredicateIsNull, PredicateIn:
		part, err := renderPredicateWithResolver(predicate, args, resolve)
		if err != nil {
			return "", err
		}
		g, err := guard(predicate.Field)
		if err != nil || g == "" {
			return part, err
		}
		return "(" + g + " AND " + part + ")", nil
	case PredicateAnd, PredicateOr:
		parts := make([]string, len(predicate.Children))
		join := " AND "
		if predicate.Kind == PredicateOr {
			join = " OR "
		}
		for i, child := range predicate.Children {
			var err error
			parts[i], err = renderPredicateWithFieldGuard(child, args, resolve, guard)
			if err != nil {
				return "", err
			}
		}
		return "(" + strings.Join(parts, join) + ")", nil
	case PredicateNot:
		if len(predicate.Children) != 1 {
			return "", fmt.Errorf("not predicate requires one child")
		}
		child, err := renderPredicateWithFieldGuard(predicate.Children[0], args, resolve, guard)
		if err != nil {
			return "", err
		}
		return "NOT (" + child + ")", nil
	default:
		return renderPredicateWithResolver(predicate, args, resolve)
	}
}

func renderScalar(expression ScalarExpr, args *[]any) (string, error) {
	return renderScalarWithResolver(expression, args, func(name string) (string, error) {
		if err := validName(name); err != nil {
			return "", err
		}
		return quoteName(name), nil
	})
}
func renderScalarWithResolver(expression ScalarExpr, args *[]any, resolve func(string) (string, error)) (string, error) {
	switch expression.Kind {
	case ScalarMetricRef:
		return resolve(expression.Metric)
	case ScalarLiteral:
		return bindLiteral(expression.Literal, args)
	case ScalarNeg, ScalarPos:
		if len(expression.Children) != 1 {
			return "", fmt.Errorf("%s expression requires one child", expression.Kind)
		}
		child, err := renderScalarWithResolver(expression.Children[0], args, resolve)
		if err != nil {
			return "", err
		}
		prefix := "-"
		if expression.Kind == ScalarPos {
			prefix = "+"
		}
		return "(" + prefix + child + ")", nil
	case ScalarAdd, ScalarSub, ScalarMul, ScalarDiv, ScalarSafeDiv:
		if len(expression.Children) != 2 {
			return "", fmt.Errorf("%s expression requires two children", expression.Kind)
		}
		left, err := renderScalarWithResolver(expression.Children[0], args, resolve)
		if err != nil {
			return "", err
		}
		right, err := renderScalarWithResolver(expression.Children[1], args, resolve)
		if err != nil {
			return "", err
		}
		if expression.Kind == ScalarSafeDiv {
			return "(" + left + " / NULLIF(" + right + ", 0))", nil
		}
		op := map[ScalarKind]string{ScalarAdd: "+", ScalarSub: "-", ScalarMul: "*", ScalarDiv: "/"}[expression.Kind]
		return "(" + left + " " + op + " " + right + ")", nil
	case ScalarFunction:
		name := strings.ToUpper(expression.Function)
		if name != "COALESCE" && name != "NULLIF" && name != "ABS" && name != "ROUND" && name != "SAFE_DIVIDE" {
			return "", fmt.Errorf("unsupported scalar function %q", expression.Function)
		}
		if len(expression.Children) == 0 {
			return "", fmt.Errorf("scalar function %q requires children", expression.Function)
		}
		children := make([]string, len(expression.Children))
		for i, child := range expression.Children {
			var err error
			children[i], err = renderScalarWithResolver(child, args, resolve)
			if err != nil {
				return "", err
			}
		}
		if name == "SAFE_DIVIDE" {
			if len(children) != 2 {
				return "", fmt.Errorf("safe_divide requires two children")
			}
			return "(" + children[0] + " / NULLIF(" + children[1] + ", 0))", nil
		}
		return name + "(" + strings.Join(children, ", ") + ")", nil
	default:
		return "", fmt.Errorf("unsupported scalar expression kind %q", expression.Kind)
	}
}
