package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type aggregateDimension struct {
	Name      string
	Alias     string
	Type      string
	Datatype  semanticmodel.LogicalDataType
	Grain     string
	Timezone  string
	Calendar  string
	WeekStart string
	Semantic  bool
	Physical  semanticmodel.MetricDimension
}

type aggregateMember struct {
	Name  string
	Alias string
	Kind  string
}

type physicalFieldBinding struct {
	Field string
	Path  []semanticmodel.Relationship
}

type pathAliasSet struct {
	BaseTable string
	ByPath    map[string]tableAlias
	Ordered   []tableAlias
}

type aggregateResolution struct {
	Dimensions []aggregateDimension
	Members    []aggregateMember
	Aggregates map[string]resolvedAggregateMetric
	Metrics    map[string]semanticmodel.Expression
	Facts      []string
	MultiFact  bool
	Masks      columnMaskSet
}

// planAggregate preserves fact grain by compiling every fact through only safe
// relationship paths, aggregating each fact independently, and stitching the
// resulting grouped rows. Facts are never joined to each other before their
// metrics have been reduced to the requested conformed dimensions.
func (p *Planner) planAggregate(request Request) (Plan, error) {
	resolved, err := p.resolveAggregate(request)
	if err != nil {
		return Plan{}, err
	}
	if err := p.validateAggregateFilters(request.Filters, resolved); err != nil {
		return Plan{}, err
	}
	if request.SpatialBucket != nil {
		if resolved.MultiFact {
			return Plan{}, fmt.Errorf("spatial tile buckets require a single fact query")
		}
		if err := validateAggregateSpatialBucket(*request.SpatialBucket, resolved.Dimensions); err != nil {
			return Plan{}, err
		}
	}

	metricNames := sortedAggregateMetricNames(resolved.Aggregates)
	metricColumns := map[string]string{}
	for index, name := range metricNames {
		metricColumns[name] = fmt.Sprintf("__m%d", index)
	}

	ctes := make([]string, 0, len(resolved.Facts)+len(resolved.Facts))
	args := []any{}
	dependencies := map[string]struct{}{}
	for factIndex, fact := range resolved.Facts {
		cte, cteArgs, cteDependencies, err := p.compileFactAggregate(
			request,
			resolved,
			fact,
			factIndex,
			metricColumns,
		)
		if err != nil {
			return Plan{}, err
		}
		ctes = append(ctes, cte)
		args = append(args, cteArgs...)
		for _, dependency := range cteDependencies {
			dependencies[dependency] = struct{}{}
		}
	}

	source, stitchCTEs := stitchFacts(resolved.Facts, resolved.Dimensions, resolved.Aggregates, metricNames, metricColumns, request.SpatialBucket != nil)
	ctes = append(ctes, stitchCTEs...)

	selects := []string{}
	columns := []string{}
	columnSet := map[string]bool{}
	for index, dimension := range resolved.Dimensions {
		if err := addOutputColumn(columnSet, dimension.Alias); err != nil {
			return Plan{}, err
		}
		selects = append(selects, fmt.Sprintf("s.__d%d AS %s", index, dimension.Alias))
		columns = append(columns, dimension.Alias)
	}

	metricSQL := map[string]string{}
	var renderMetric func(string) (string, error)
	aggregateSQL := func(name string) (string, error) {
		metric, ok := resolved.Aggregates[name]
		if !ok {
			return "", fmt.Errorf("unknown metric %q", name)
		}
		expr := "s." + metricColumns[name]
		if metric.Empty == "zero" {
			expr = "COALESCE(" + expr + ", 0)"
		}
		return expr, nil
	}
	renderMetric = func(name string) (string, error) {
		if sql, ok := metricSQL[name]; ok {
			return sql, nil
		}
		expression, ok := resolved.Metrics[name]
		if !ok {
			return "", fmt.Errorf("unknown metric %q", name)
		}
		sql, err := expression.SQL(func(ref string) (string, error) {
			if _, ok := resolved.Aggregates[ref]; ok {
				return aggregateSQL(ref)
			}
			return renderMetric(ref)
		})
		if err != nil {
			return "", err
		}
		metricSQL[name] = sql
		return sql, nil
	}

	for _, member := range resolved.Members {
		if err := addOutputColumn(columnSet, member.Alias); err != nil {
			return Plan{}, err
		}
		var expr string
		if member.Kind == "metric" {
			expr, err = renderMetric(member.Name)
		} else {
			expr, err = aggregateSQL(member.Name)
		}
		if err != nil {
			return Plan{}, err
		}
		selects = append(selects, expr+" AS "+member.Alias)
		columns = append(columns, member.Alias)
	}
	if request.SpatialBucket != nil {
		selects = append(selects,
			"s.__spatial_count AS __lv_count",
			"s.__spatial_coordinate_count AS __lv_coordinate_count",
			"s.__spatial_center_longitude AS __lv_center_longitude",
			"s.__spatial_center_latitude AS __lv_center_latitude",
			"s.__spatial_west AS __lv_west",
			"s.__spatial_south AS __lv_south",
			"s.__spatial_east AS __lv_east",
			"s.__spatial_north AS __lv_north",
		)
		columns = append(columns, "__lv_count", "__lv_coordinate_count", "__lv_center_longitude", "__lv_center_latitude", "__lv_west", "__lv_south", "__lv_east", "__lv_north")
		columnSet["__lv_count"] = true
		columnSet["__lv_coordinate_count"] = true
	}
	if len(selects) == 0 {
		return Plan{}, fmt.Errorf("aggregate query requires at least one selected field")
	}

	var sql strings.Builder
	sql.WriteString("WITH ")
	sql.WriteString(strings.Join(ctes, ",\n"))
	sql.WriteString("\nSELECT ")
	sql.WriteString(strings.Join(selects, ", "))
	sql.WriteString("\nFROM ")
	sql.WriteString(source)
	if err := writeOrderLimitOffset(&sql, request.Sort, columnSet, request.Limit, request.Offset); err != nil {
		return Plan{}, err
	}
	effectiveOrdering := effectiveOrderSorts(request.Sort, columnSet)

	physicalDependencies := make([]string, 0, len(dependencies))
	for dependency := range dependencies {
		physicalDependencies = append(physicalDependencies, dependency)
	}
	sort.Strings(physicalDependencies)
	mode := "single_fact"
	if resolved.MultiFact {
		mode = "multi_fact"
	}
	stitchDimensions := []string{}
	if resolved.MultiFact {
		for _, dimension := range resolved.Dimensions {
			stitchDimensions = append(stitchDimensions, dimension.Name)
		}
	}
	dependencyResolution, err := ResolveDependencies(p.Model, request)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		SQL:                  sql.String(),
		Args:                 args,
		Columns:              columns,
		Mode:                 mode,
		Facts:                append([]string{}, resolved.Facts...),
		StitchDimensions:     stitchDimensions,
		PhysicalDependencies: physicalDependencies,
		RelationshipPaths:    dependencyResolution.RelationshipPaths,
		EffectiveOrdering:    effectiveOrdering,
	}, nil
}

func (p *Planner) resolveAggregate(request Request) (aggregateResolution, error) {
	if p.Model == nil {
		return aggregateResolution{}, fmt.Errorf("semantic model is required")
	}
	masks, err := columnMaskMap(request.ColumnMasks)
	if err != nil {
		return aggregateResolution{}, err
	}
	resolved := aggregateResolution{
		Aggregates: map[string]resolvedAggregateMetric{},
		Metrics:    map[string]semanticmodel.Expression{},
		Masks:      masks,
	}
	visiting := map[string]bool{}
	var addMetric func(string) error
	addAggregate := func(name string) error {
		metric, err := p.Model.ResolveAggregateMetric(name)
		if err != nil {
			return err
		}
		resolvedMetric, err := p.resolvedAggregateMetric(name, metric)
		if err != nil {
			return err
		}
		if masks.matchesMetric(name, resolvedMetric) {
			return fmt.Errorf("metric %q depends on a masked field", name)
		}
		resolved.Aggregates[name] = resolvedMetric
		return nil
	}
	addMetric = func(name string) error {
		if _, ok := resolved.Aggregates[name]; ok {
			return nil
		}
		if _, ok := resolved.Metrics[name]; ok {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("metric dependency cycle includes %q", name)
		}
		metric, ok := p.Model.Metrics[name]
		if !ok {
			return fmt.Errorf("unknown metric %q", name)
		}
		if metric.Type == "aggregate" {
			if err := addAggregate(name); err != nil {
				return err
			}
			return nil
		}
		visiting[name] = true
		expression, err := p.metricExpression(name, metric)
		if err != nil {
			return fmt.Errorf("metric %q: %w", name, err)
		}
		for _, ref := range expression.References() {
			if metricRef, ok := p.Model.Metrics[ref]; ok && metricRef.Type == "aggregate" {
				if err := addAggregate(ref); err != nil {
					return err
				}
				continue
			}
			if _, ok := p.Model.Metrics[ref]; ok {
				if err := addMetric(ref); err != nil {
					return err
				}
				continue
			}
			if err := addMetric(ref); err != nil {
				return err
			}
		}
		delete(visiting, name)
		resolved.Metrics[name] = expression
		return nil
	}

	for _, item := range request.Metrics {
		name := strings.TrimSpace(item.Field)
		if name == "" {
			return aggregateResolution{}, fmt.Errorf("selected metric is required")
		}
		alias, err := outputAlias(item)
		if err != nil {
			return aggregateResolution{}, err
		}
		if _, ok := p.Model.Metrics[name]; ok {
			if _, masked := masks[strings.ToLower(name)]; masked {
				return aggregateResolution{}, fmt.Errorf("metric %q is masked", name)
			}
			if p.Model.Metrics[name].Type == "aggregate" {
				if err := addAggregate(name); err != nil {
					return aggregateResolution{}, err
				}
				resolved.Members = append(resolved.Members, aggregateMember{Name: name, Alias: alias, Kind: "aggregate"})
				continue
			}
			if err := addMetric(name); err != nil {
				return aggregateResolution{}, err
			}
			resolved.Members = append(resolved.Members, aggregateMember{Name: name, Alias: alias, Kind: "metric"})
			continue
		}
		return aggregateResolution{}, fmt.Errorf("unknown metric %q", name)
	}

	factSet := map[string]struct{}{}
	for _, metric := range resolved.Aggregates {
		factSet[metric.Fact] = struct{}{}
	}
	if request.Table != "" {
		if _, ok := p.Model.Tables[request.Table]; !ok {
			return aggregateResolution{}, fmt.Errorf("unknown table %q", request.Table)
		}
		for fact := range factSet {
			if fact != request.Table {
				return aggregateResolution{}, fmt.Errorf("table-scoped query for %q selects dependency from fact %q", request.Table, fact)
			}
		}
		factSet = map[string]struct{}{request.Table: {}}
	}

	dimensionFields := append([]Field{}, request.Dimensions...)
	if request.Time.Field != "" {
		if !allowedTimeGrain(request.Time.Grain) {
			return aggregateResolution{}, fmt.Errorf("unsupported time grain %q", request.Time.Grain)
		}
		dimensionFields = append(dimensionFields, Field{Field: request.Time.Field, Alias: request.Time.Alias})
	}
	for index, item := range dimensionFields {
		alias, err := outputAlias(item)
		if err != nil {
			return aggregateResolution{}, err
		}
		grain := ""
		if request.Time.Field != "" && index == len(dimensionFields)-1 {
			grain = request.Time.Grain
		}
		if dimension, ok := p.Model.Dimensions[item.Field]; ok {
			if grain != "" && !containsString(dimension.Grains, grain) {
				return aggregateResolution{}, fmt.Errorf("semantic dimension %q does not support grain %q", item.Field, grain)
			}
			resolved.Dimensions = append(resolved.Dimensions, aggregateDimension{
				Name: item.Field, Alias: alias, Type: dimension.Type, Datatype: dimension.Datatype, Grain: grain, Timezone: dimension.Timezone, Calendar: dimension.Calendar, WeekStart: dimension.WeekStart, Semantic: true,
			})
			continue
		}
		physical, err := p.Model.ResolveDimension(item.Field)
		if err != nil {
			return aggregateResolution{}, err
		}
		if grain != "" && physical.Type != "date" && physical.Type != "timestamp" {
			return aggregateResolution{}, fmt.Errorf("time field %q is not date or timestamp", item.Field)
		}
		resolved.Dimensions = append(resolved.Dimensions, aggregateDimension{
			Name: item.Field, Alias: alias, Type: physical.Type, Datatype: physical.Datatype, Grain: grain, Timezone: "UTC", Calendar: "gregorian", WeekStart: "sunday", Physical: physical,
		})
	}

	if len(factSet) == 0 {
		if len(resolved.Dimensions) == 0 {
			return aggregateResolution{}, fmt.Errorf("aggregate query requires a metric or dimension")
		}
		for _, fact := range p.Model.FactNames() {
			compatible := true
			for _, dimension := range resolved.Dimensions {
				if !dimension.Semantic {
					compatible = false
					break
				}
				if _, ok := p.Model.Dimensions[dimension.Name].Bindings[fact]; !ok {
					compatible = false
					break
				}
			}
			if compatible && !p.factSupportsInferredFilters(request.Filters, fact) {
				compatible = false
			}
			if compatible {
				factSet[fact] = struct{}{}
			}
		}
	}
	for fact := range factSet {
		resolved.Facts = append(resolved.Facts, fact)
	}
	sort.Strings(resolved.Facts)
	if len(resolved.Facts) == 0 {
		return aggregateResolution{}, fmt.Errorf("no fact is compatible with the selected dimensions")
	}
	resolved.MultiFact = len(resolved.Facts) > 1
	for _, dimension := range resolved.Dimensions {
		if !dimension.Semantic {
			if resolved.MultiFact {
				return aggregateResolution{}, fmt.Errorf("qualified local dimension %q is invalid in a multi-fact query", dimension.Name)
			}
			if _, err := p.relationshipPath(resolved.Facts[0], dimension.Physical.Table); err != nil {
				return aggregateResolution{}, err
			}
			continue
		}
		semanticDimension := p.Model.Dimensions[dimension.Name]
		for _, fact := range resolved.Facts {
			if _, ok := semanticDimension.Bindings[fact]; !ok {
				return aggregateResolution{}, fmt.Errorf("semantic dimension %q has no binding for fact %q", dimension.Name, fact)
			}
		}
	}
	return resolved, nil
}

// Dimension-only aggregates infer their participating facts from the selected
// dimensions. Unscoped semantic filters must participate in that inference as
// well: a conformed filter cannot be applied to a fact that has no binding for
// it. Queries with selected metrics keep their metric-owned fact set and are
// validated normally, so a missing conformed binding remains an error there.
func (p *Planner) factSupportsInferredFilters(filters []Filter, fact string) bool {
	var supports func(Filter) bool
	supports = func(filter Filter) bool {
		if filter.Field != "" && filter.Fact == "" {
			if dimension, ok := p.Model.Dimensions[filter.Field]; ok {
				if _, bound := dimension.Bindings[fact]; !bound {
					return false
				}
			}
		}
		if filter.Spatial != nil && filter.Spatial.Fact == "" {
			for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				if dimension, ok := p.Model.Dimensions[field]; ok {
					if _, bound := dimension.Bindings[fact]; !bound {
						return false
					}
				}
			}
		}
		for _, group := range filter.Groups {
			for _, child := range group.Filters {
				if !supports(child) {
					return false
				}
			}
		}
		return true
	}
	for _, filter := range filters {
		if !supports(filter) {
			return false
		}
	}
	return true
}

func (p *Planner) compileFactAggregate(request Request, resolved aggregateResolution, fact string, factIndex int, metricColumns map[string]string) (string, []any, []string, error) {
	bindings := []physicalFieldBinding{}
	dependencies := map[string]struct{}{fact: {}}
	for _, dimension := range resolved.Dimensions {
		field, path, err := p.aggregateDimensionBinding(fact, dimension)
		if err != nil {
			return "", nil, nil, err
		}
		bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
		addPathDependencies(dependencies, path)
		dependencies[field] = struct{}{}
	}
	for _, metric := range resolved.Aggregates {
		if metric.Fact != fact {
			continue
		}
		for _, field := range aggregateMetricPhysicalFields(metric) {
			dependencies[field] = struct{}{}
			physical, err := p.Model.ResolveDimension(field)
			if err != nil {
				return "", nil, nil, err
			}
			path, err := p.relationshipPath(fact, physical.Table)
			if err != nil {
				return "", nil, nil, err
			}
			bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
			addPathDependencies(dependencies, path)
		}
	}
	filterBindings, err := p.factFilterFields(request.Filters, resolved, fact)
	if err != nil {
		return "", nil, nil, err
	}
	bindings = append(bindings, filterBindings...)
	for _, binding := range filterBindings {
		dependencies[binding.Field] = struct{}{}
		addPathDependencies(dependencies, binding.Path)
	}
	for _, metric := range resolved.Aggregates {
		if metric.Fact != fact || len(metric.WhereFilters) == 0 {
			continue
		}
		whereFilters := scopeMetricWhereFilters(metric.WhereFilters, fact)
		whereBindings, err := p.factFilterFields(whereFilters, resolved, fact)
		if err != nil {
			return "", nil, nil, err
		}
		bindings = append(bindings, whereBindings...)
		for _, binding := range whereBindings {
			dependencies[binding.Field] = struct{}{}
			addPathDependencies(dependencies, binding.Path)
		}
	}
	aliases, err := p.aliasesForFact(fact, bindings)
	if err != nil {
		return "", nil, nil, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return "", nil, nil, err
	}

	selects := []string{}
	spatialLatitudeExpr, spatialLongitudeExpr := "", ""
	for index, dimension := range resolved.Dimensions {
		field, path, _ := p.aggregateDimensionBinding(fact, dimension)
		physical, _ := p.Model.ResolveDimension(field)
		expr, err := dimensionExprForPath(physical, aliases, path)
		if err != nil {
			return "", nil, nil, err
		}
		if request.SpatialBucket != nil {
			switch dimension.Name {
			case request.SpatialBucket.Longitude.Field:
				spatialLongitudeExpr = expr
				expr = spatialBucketXExpression(expr, request.SpatialBucket.Zoom, request.SpatialBucket.CellPixels)
			case request.SpatialBucket.Latitude.Field:
				spatialLatitudeExpr = expr
				expr = spatialBucketYExpression(expr, request.SpatialBucket.Zoom, request.SpatialBucket.CellPixels)
			}
		}
		expr = applyTimeSemantics(expr, dimension)
		expr = canonicalDimensionExpr(expr, dimension.Type)
		selects = append(selects, fmt.Sprintf("%s AS __d%d", expr, index))
	}
	metricArgs := []any{}
	for _, name := range sortedAggregateMetricNames(resolved.Aggregates) {
		metric := resolved.Aggregates[name]
		if metric.Fact != fact {
			continue
		}
		factAliases, err := aliases.context(nil)
		if err != nil {
			return "", nil, nil, err
		}
		expr, err := aggregateMetricExpr(p.Model, metric, factAliases)
		if err != nil {
			return "", nil, nil, err
		}
		metricFilterParts := []string{}
		for _, filter := range metric.Filters {
			physical, _ := p.Model.ResolveDimension(filter.Field)
			path, err := p.relationshipPath(fact, physical.Table)
			if err != nil {
				return "", nil, nil, err
			}
			filterExpr, err := dimensionExprForPath(physical, aliases, path)
			if err != nil {
				return "", nil, nil, err
			}
			part, partArgs, err := filterSQL(filterExpr, Filter{Operator: filter.Operator, Values: filter.Values})
			if err != nil {
				return "", nil, nil, err
			}
			if part != "" {
				metricFilterParts = append(metricFilterParts, part)
				metricArgs = append(metricArgs, partArgs...)
			}
		}
		for _, filter := range scopeMetricWhereFilters(metric.WhereFilters, fact) {
			part, partArgs, err := p.factFilterPart(filter, resolved, fact, aliases)
			if err != nil {
				return "", nil, nil, err
			}
			if part != "" {
				metricFilterParts = append(metricFilterParts, part)
				metricArgs = append(metricArgs, partArgs...)
			}
		}
		if len(metricFilterParts) > 0 {
			expr += " FILTER (WHERE " + strings.Join(metricFilterParts, " AND ") + ")"
		}
		if metric.Empty == "zero" && metric.Aggregation != "count" && metric.Aggregation != "count_distinct" {
			expr = "COALESCE(" + expr + ", 0)"
		}
		selects = append(selects, expr+" AS "+metricColumns[name])
	}
	if request.SpatialBucket != nil {
		if spatialLatitudeExpr == "" || spatialLongitudeExpr == "" {
			return "", nil, nil, fmt.Errorf("spatial bucket coordinates are not resolved dimensions")
		}
		selects = append(selects,
			"COUNT(*) AS __spatial_count",
			"COUNT(DISTINCT ("+spatialLatitudeExpr+", "+spatialLongitudeExpr+")) AS __spatial_coordinate_count",
			"AVG("+spatialLongitudeExpr+") AS __spatial_center_longitude",
			"AVG("+spatialLatitudeExpr+") AS __spatial_center_latitude",
			"MIN("+spatialLongitudeExpr+") AS __spatial_west",
			"MIN("+spatialLatitudeExpr+") AS __spatial_south",
			"MAX("+spatialLongitudeExpr+") AS __spatial_east",
			"MAX("+spatialLatitudeExpr+") AS __spatial_north",
		)
	}
	whereParts, whereArgs, err := p.factWhereParts(request.Filters, resolved, fact, aliases)
	if err != nil {
		return "", nil, nil, err
	}
	if len(selects) == 0 {
		return "", nil, nil, fmt.Errorf("fact %q has no selected dimensions or metrics", fact)
	}
	var sql strings.Builder
	sql.WriteString(fmt.Sprintf("fact_%d AS (\n  SELECT ", factIndex))
	sql.WriteString(strings.Join(selects, ", "))
	sql.WriteString("\n  FROM ")
	sql.WriteString(strings.ReplaceAll(from, "\n", "\n  "))
	if len(whereParts) > 0 {
		sql.WriteString("\n  WHERE ")
		sql.WriteString(strings.Join(whereParts, " AND "))
	}
	if len(resolved.Dimensions) > 0 {
		positions := make([]string, len(resolved.Dimensions))
		for index := range positions {
			positions[index] = fmt.Sprint(index + 1)
		}
		if hasFactAggregates(resolved.Aggregates, fact) || request.SpatialBucket != nil {
			sql.WriteString("\n  GROUP BY ")
			sql.WriteString(strings.Join(positions, ", "))
		} else {
			// Dimension-only queries use every compatible binding and union through the stitch.
			sqlString := sql.String()
			sql.Reset()
			sql.WriteString(strings.Replace(sqlString, "SELECT ", "SELECT DISTINCT ", 1))
		}
	}
	sql.WriteString("\n)")
	dependencyList := make([]string, 0, len(dependencies))
	for dependency := range dependencies {
		dependencyList = append(dependencyList, dependency)
	}
	sort.Strings(dependencyList)
	return sql.String(), append(metricArgs, whereArgs...), dependencyList, nil
}

func stitchFacts(facts []string, dimensions []aggregateDimension, metrics map[string]resolvedAggregateMetric, metricNames []string, metricColumns map[string]string, spatial bool) (string, []string) {
	if len(facts) == 1 {
		return "fact_0 s", nil
	}
	if len(dimensions) == 0 {
		selects := []string{}
		from := []string{}
		for index := range facts {
			alias := fmt.Sprintf("f%d", index)
			from = append(from, fmt.Sprintf("fact_%d %s", index, alias))
			for _, name := range metricNames {
				if metrics[name].Fact == facts[index] {
					selects = append(selects, fmt.Sprintf("%s.%s AS %s", alias, metricColumns[name], metricColumns[name]))
				}
			}
		}
		cte := "stitched AS (\n  SELECT " + strings.Join(selects, ", ") + "\n  FROM " + strings.Join(from, " CROSS JOIN ") + "\n)"
		return "stitched s", []string{cte}
	}
	ctes := []string{}
	leftName := "fact_0"
	availableMetrics := map[string]bool{}
	for _, name := range metricNames {
		if metrics[name].Fact == facts[0] {
			availableMetrics[name] = true
		}
	}
	for index := 1; index < len(facts); index++ {
		rightName := fmt.Sprintf("fact_%d", index)
		leftAlias := "l"
		rightAlias := "r"
		selects := []string{}
		joins := []string{}
		for dimensionIndex := range dimensions {
			column := fmt.Sprintf("__d%d", dimensionIndex)
			selects = append(selects, fmt.Sprintf("COALESCE(%s.%s, %s.%s) AS %s", leftAlias, column, rightAlias, column, column))
			joins = append(joins, fmt.Sprintf("%s.%s IS NOT DISTINCT FROM %s.%s", leftAlias, column, rightAlias, column))
		}
		for _, name := range metricNames {
			column := metricColumns[name]
			if availableMetrics[name] {
				selects = append(selects, leftAlias+"."+column+" AS "+column)
			} else if metrics[name].Fact == facts[index] {
				selects = append(selects, rightAlias+"."+column+" AS "+column)
			}
		}
		if spatial {
			selects = append(selects,
				fmt.Sprintf("GREATEST(COALESCE(%s.__spatial_count, 0), COALESCE(%s.__spatial_count, 0)) AS __spatial_count", leftAlias, rightAlias),
				fmt.Sprintf("GREATEST(COALESCE(%s.__spatial_coordinate_count, 0), COALESCE(%s.__spatial_coordinate_count, 0)) AS __spatial_coordinate_count", leftAlias, rightAlias),
				spatialWeightedCenter(leftAlias, rightAlias, "longitude"),
				spatialWeightedCenter(leftAlias, rightAlias, "latitude"),
				spatialExtentMerge(leftAlias, rightAlias, "west", "LEAST"),
				spatialExtentMerge(leftAlias, rightAlias, "south", "LEAST"),
				spatialExtentMerge(leftAlias, rightAlias, "east", "GREATEST"),
				spatialExtentMerge(leftAlias, rightAlias, "north", "GREATEST"),
			)
		}
		for _, name := range metricNames {
			if metrics[name].Fact == facts[index] {
				availableMetrics[name] = true
			}
		}
		cteName := fmt.Sprintf("stitch_%d", index)
		ctes = append(ctes, fmt.Sprintf("%s AS (\n  SELECT %s\n  FROM %s %s\n  FULL OUTER JOIN %s %s ON %s\n)", cteName, strings.Join(selects, ", "), leftName, leftAlias, rightName, rightAlias, strings.Join(joins, " AND ")))
		leftName = cteName
	}
	return leftName + " s", ctes
}

func spatialWeightedCenter(left, right, axis string) string {
	column := "__spatial_center_" + axis
	return fmt.Sprintf("CASE WHEN %s.%s IS NULL THEN %s.%s WHEN %s.%s IS NULL THEN %s.%s ELSE ((%s.%s * %s.__spatial_count) + (%s.%s * %s.__spatial_count)) / NULLIF(%s.__spatial_count + %s.__spatial_count, 0) END AS %s", left, column, right, column, right, column, left, column, left, column, left, right, column, right, left, right, column)
}

func spatialExtentMerge(left, right, edge, operation string) string {
	column := "__spatial_" + edge
	return fmt.Sprintf("CASE WHEN %s.%s IS NULL THEN %s.%s WHEN %s.%s IS NULL THEN %s.%s ELSE %s(%s.%s, %s.%s) END AS %s", left, column, right, column, right, column, left, column, operation, left, column, right, column, column)
}

func (p *Planner) validateAggregateFilters(filters []Filter, resolved aggregateResolution) error {
	factSet := map[string]bool{}
	for _, fact := range resolved.Facts {
		factSet[fact] = true
	}
	for _, filter := range filters {
		scopes := []string{}
		collectField := func(field, filterFact string) error {
			if _, semantic := p.Model.Dimensions[field]; semantic && filterFact == "" {
				scopes = append(scopes, "conformed")
				return nil
			}
			fact := filterFact
			if fact == "" {
				if resolved.MultiFact {
					return fmt.Errorf("fact-local filter %q requires fact in a multi-fact query", field)
				}
				fact = resolved.Facts[0]
			}
			if !factSet[fact] {
				return fmt.Errorf("filter fact %q is not a participating fact", fact)
			}
			scopes = append(scopes, fact)
			return nil
		}
		var collect func(Filter) error
		collect = func(item Filter) error {
			if item.Field != "" {
				if err := collectField(item.Field, item.Fact); err != nil {
					return err
				}
			}
			if item.Spatial != nil {
				if err := collectField(item.Spatial.LatitudeField, item.Spatial.Fact); err != nil {
					return err
				}
				if err := collectField(item.Spatial.LongitudeField, item.Spatial.Fact); err != nil {
					return err
				}
			}
			for _, group := range item.Groups {
				for _, child := range group.Filters {
					if err := collect(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := collect(filter); err != nil {
			return err
		}
		if len(scopes) == 0 {
			continue
		}
		first := scopes[0]
		for _, scope := range scopes[1:] {
			if scope != first {
				return fmt.Errorf("boolean filter group must be entirely conformed or resolve to one fact")
			}
		}
	}
	return nil
}

func (p *Planner) factFilterFields(filters []Filter, resolved aggregateResolution, fact string) ([]physicalFieldBinding, error) {
	bindings := []physicalFieldBinding{}
	var walk func(Filter) error
	walk = func(filter Filter) error {
		if filter.Spatial != nil {
			for _, ref := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				field, path, applies, err := p.resolveFactFilterField(Filter{Field: ref, Fact: filter.Spatial.Fact}, resolved, fact)
				if err != nil {
					return err
				}
				if applies {
					bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
				}
			}
		}
		if filter.Field != "" {
			field, path, applies, err := p.resolveFactFilterField(filter, resolved, fact)
			if err != nil {
				return err
			}
			if applies {
				bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
			}
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

func (p *Planner) factWhereParts(filters []Filter, resolved aggregateResolution, fact string, aliases pathAliasSet) ([]string, []any, error) {
	parts := []string{}
	args := []any{}
	for _, filter := range filters {
		part, partArgs, err := p.factFilterPart(filter, resolved, fact, aliases)
		if err != nil {
			return nil, nil, err
		}
		if part != "" {
			parts = append(parts, part)
			args = append(args, partArgs...)
		}
	}
	return parts, args, nil
}

func (p *Planner) factFilterPart(filter Filter, resolved aggregateResolution, fact string, aliases pathAliasSet) (string, []any, error) {
	if filter.RequireMatch {
		filter.RequireMatch = false
		filter = markFilterMatchGuards(filter)
	}
	if filter.Not {
		inner := filter
		inner.Not = false
		inner.MatchGuard = false
		part, args, err := p.factFilterPart(inner, resolved, fact, aliases)
		if err != nil || part == "" {
			return "", args, err
		}
		if guards, guardErr := p.filterMatchGuards(inner, resolved, fact, aliases); guardErr != nil {
			return "", nil, guardErr
		} else if len(guards) > 0 {
			return "(" + strings.Join(guards, " AND ") + " AND NOT (" + part + "))", args, nil
		}
		return "NOT (" + part + ")", args, nil
	}
	if filter.Spatial != nil {
		if filter.Field != "" || len(filter.Groups) != 0 {
			return "", nil, fmt.Errorf("spatial filter cannot combine scalar or grouped filter fields")
		}
		resolveExpr := func(ref string) (string, bool, error) {
			field, path, applies, err := p.resolveFactFilterField(Filter{Field: ref, Fact: filter.Spatial.Fact}, resolved, fact)
			if err != nil || !applies {
				return "", applies, err
			}
			physical, err := p.Model.ResolveDimension(field)
			if err != nil {
				return "", false, err
			}
			expr, err := dimensionExprForPath(physical, aliases, path)
			return expr, true, err
		}
		latitudeExpr, latitudeApplies, err := resolveExpr(filter.Spatial.LatitudeField)
		if err != nil {
			return "", nil, err
		}
		longitudeExpr, longitudeApplies, err := resolveExpr(filter.Spatial.LongitudeField)
		if err != nil {
			return "", nil, err
		}
		if latitudeApplies != longitudeApplies {
			return "", nil, fmt.Errorf("spatial coordinate fields resolve to different fact scopes")
		}
		if !latitudeApplies {
			return "", nil, nil
		}
		return spatialFilterSQL(latitudeExpr, longitudeExpr, *filter.Spatial)
	}
	if len(filter.Groups) > 0 {
		orParts := []string{}
		args := []any{}
		for _, group := range filter.Groups {
			andParts := []string{}
			for _, child := range group.Filters {
				part, partArgs, err := p.factFilterPart(child, resolved, fact, aliases)
				if err != nil {
					return "", nil, err
				}
				if part != "" {
					andParts = append(andParts, part)
					args = append(args, partArgs...)
				}
			}
			if len(andParts) > 0 {
				orParts = append(orParts, "("+strings.Join(andParts, " AND ")+")")
			}
		}
		if len(orParts) == 0 {
			return "", nil, nil
		}
		return "(" + strings.Join(orParts, " OR ") + ")", args, nil
	}
	if filter.Field == "" {
		return "", nil, nil
	}
	field, path, applies, err := p.resolveFactFilterField(filter, resolved, fact)
	if err != nil || !applies {
		return "", nil, err
	}
	physical, err := p.Model.ResolveDimension(field)
	if err != nil {
		return "", nil, err
	}
	expr, err := dimensionExprForPath(physical, aliases, path)
	if err != nil {
		return "", nil, err
	}
	part, args, err := filterSQL(expr, filter)
	if err != nil || !filter.MatchGuard || len(path) == 0 || part == "" {
		return part, args, err
	}
	guard, err := p.relationshipMatchGuard(fact, physical.Table, path, aliases)
	if err != nil {
		return "", nil, err
	}
	return "(" + guard + " AND " + part + ")", args, nil
}

func (p *Planner) filterMatchGuards(filter Filter, resolved aggregateResolution, fact string, aliases pathAliasSet) ([]string, error) {
	guards := []string{}
	var walk func(Filter) error
	walk = func(item Filter) error {
		if item.Field != "" {
			field, path, applies, err := p.resolveFactFilterField(item, resolved, fact)
			if err != nil || !applies {
				return err
			}
			if len(path) > 0 {
				physical, err := p.Model.ResolveDimension(field)
				if err != nil {
					return err
				}
				guard, err := p.relationshipMatchGuard(fact, physical.Table, path, aliases)
				if err != nil {
					return err
				}
				guards = append(guards, guard)
			}
		}
		for _, group := range item.Groups {
			for _, child := range group.Filters {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(filter); err != nil {
		return nil, err
	}
	return uniqueStrings(guards), nil
}

func (p *Planner) relationshipMatchGuard(fact, target string, path []semanticmodel.Relationship, aliases pathAliasSet) (string, error) {
	current := fact
	var fields []string
	var dataset string
	for _, relationship := range path {
		fromTable, fromFields, err := semanticmodel.RelationshipEndpoint(relationship, true)
		if err != nil {
			return "", err
		}
		toTable, toFields, err := semanticmodel.RelationshipEndpoint(relationship, false)
		if err != nil {
			return "", err
		}
		switch {
		case current == fromTable:
			current, dataset, fields = toTable, toTable, toFields
		case current == toTable && relationship.Cardinality == "one_to_one":
			current, dataset, fields = fromTable, fromTable, fromFields
		default:
			return "", fmt.Errorf("relationship path %q does not safely continue from %q", relationshipPathSignature(path), current)
		}
	}
	if current != target || dataset == "" || len(fields) == 0 {
		return "", fmt.Errorf("relationship path %q does not reach filter table %q", relationshipPathSignature(path), target)
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		physical, err := p.Model.ResolveDimension(dataset + "." + field)
		if err != nil {
			return "", err
		}
		expr, err := dimensionExprForPath(physical, aliases, path)
		if err != nil {
			return "", err
		}
		parts = append(parts, expr+" IS NOT NULL")
	}
	return "(" + strings.Join(parts, " AND ") + ")", nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (p *Planner) resolveFactFilterField(filter Filter, resolved aggregateResolution, fact string) (string, []semanticmodel.Relationship, bool, error) {
	if semanticDimension, ok := p.Model.Dimensions[filter.Field]; ok {
		if filter.Fact != "" && filter.Fact != fact {
			return "", nil, false, nil
		}
		binding, ok := semanticDimension.Bindings[fact]
		if !ok {
			return "", nil, false, fmt.Errorf("semantic dimension %q has no binding for fact %q", filter.Field, fact)
		}
		if len(filter.Path) > 0 {
			binding.Path = append([]string(nil), filter.Path...)
		}
		path, err := p.Model.ResolveBindingPath(fact, binding)
		return binding.Field, path, true, err
	}
	target := filter.Fact
	if target == "" {
		if resolved.MultiFact {
			return "", nil, false, fmt.Errorf("fact-local filter %q requires fact in a multi-fact query", filter.Field)
		}
		target = resolved.Facts[0]
	}
	if target != fact {
		return "", nil, false, nil
	}
	physical, err := p.Model.ResolveDimension(filter.Field)
	if err != nil {
		return "", nil, false, err
	}
	var path []semanticmodel.Relationship
	if len(filter.Path) > 0 {
		path, err = p.Model.ResolveBindingPath(fact, semanticmodel.DimensionBinding{Field: filter.Field, Path: append([]string(nil), filter.Path...)})
	} else {
		path, err = p.relationshipPath(fact, physical.Table)
	}
	return filter.Field, path, true, err
}

func (p *Planner) aggregateDimensionBinding(fact string, dimension aggregateDimension) (string, []semanticmodel.Relationship, error) {
	if !dimension.Semantic {
		path, err := p.relationshipPath(fact, dimension.Physical.Table)
		return dimension.Name, path, err
	}
	binding, ok := p.Model.Dimensions[dimension.Name].Bindings[fact]
	if !ok {
		return "", nil, fmt.Errorf("semantic dimension %q has no binding for fact %q", dimension.Name, fact)
	}
	path, err := p.Model.ResolveBindingPath(fact, binding)
	return binding.Field, path, err
}

func (p *Planner) aliasesForFact(fact string, bindings []physicalFieldBinding) (pathAliasSet, error) {
	aliases := pathAliasSet{
		BaseTable: fact,
		ByPath:    map[string]tableAlias{"": {Table: fact, Alias: "t0"}},
	}
	paths := map[string]tablePath{}
	for _, binding := range bindings {
		table, _, err := splitField(binding.Field)
		if err != nil {
			return pathAliasSet{}, err
		}
		path := binding.Path
		if path == nil && table != fact {
			path, err = p.relationshipPath(fact, table)
			if err != nil {
				return pathAliasSet{}, err
			}
		}
		steps := pathTables(fact, path)
		if len(steps) != len(path) {
			return pathAliasSet{}, fmt.Errorf("invalid relationship path from %q for field %q", fact, binding.Field)
		}
		if len(steps) == 0 && table != fact {
			return pathAliasSet{}, fmt.Errorf("relationship path for field %q does not leave fact %q", binding.Field, fact)
		}
		if len(steps) > 0 && steps[len(steps)-1].Table != table {
			return pathAliasSet{}, fmt.Errorf("relationship path for field %q ends at table %q", binding.Field, steps[len(steps)-1].Table)
		}
		for _, step := range steps {
			signature := relationshipPathSignature(step.Path)
			if existing, ok := paths[signature]; ok && existing.Table != step.Table {
				return pathAliasSet{}, fmt.Errorf("relationship path %q resolves to both %q and %q", signature, existing.Table, step.Table)
			}
			paths[signature] = step
		}
	}
	orderedPaths := make([]tablePath, 0, len(paths))
	for _, path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Slice(orderedPaths, func(i, j int) bool {
		if len(orderedPaths[i].Path) != len(orderedPaths[j].Path) {
			return len(orderedPaths[i].Path) < len(orderedPaths[j].Path)
		}
		return relationshipPathSignature(orderedPaths[i].Path) < relationshipPathSignature(orderedPaths[j].Path)
	})
	for index, path := range orderedPaths {
		alias := tableAlias{Table: path.Table, Alias: fmt.Sprintf("t%d", index+1), Path: path.Path}
		aliases.ByPath[relationshipPathSignature(path.Path)] = alias
		aliases.Ordered = append(aliases.Ordered, alias)
	}
	return aliases, nil
}

func (a pathAliasSet) context(path []semanticmodel.Relationship) (map[string]tableAlias, error) {
	base, ok := a.ByPath[""]
	if !ok {
		return nil, fmt.Errorf("missing base alias for fact %q", a.BaseTable)
	}
	context := map[string]tableAlias{a.BaseTable: base}
	steps := pathTables(a.BaseTable, path)
	if len(steps) != len(path) {
		return nil, fmt.Errorf("invalid relationship path from %q", a.BaseTable)
	}
	for _, step := range steps {
		signature := relationshipPathSignature(step.Path)
		alias, ok := a.ByPath[signature]
		if !ok {
			return nil, fmt.Errorf("missing alias for relationship path %q", signature)
		}
		context[step.Table] = alias
	}
	return context, nil
}

func relationshipPathSignature(path []semanticmodel.Relationship) string {
	ids := make([]string, 0, len(path))
	for _, relationship := range path {
		ids = append(ids, relationship.ID)
	}
	return strings.Join(ids, "/")
}

func canonicalDimensionExpr(expr, dimensionType string) string {
	sqlType := map[string]string{
		"string": "VARCHAR", "number": "DOUBLE", "boolean": "BOOLEAN", "date": "DATE", "timestamp": "TIMESTAMP",
	}[dimensionType]
	if sqlType == "" {
		return expr
	}
	return "CAST(" + expr + " AS " + sqlType + ")"
}

// applyTimeSemantics executes the authored temporal contract at the SQL
// boundary. Timezone conversion happens before truncation so a UTC instant is
// grouped by its local wall-clock date/hour; Sunday week starts are normalized
// around DuckDB's ISO-Monday DATE_TRUNC implementation.
func applyTimeSemantics(expr string, dimension aggregateDimension) string {
	if dimension.Grain == "" {
		return expr
	}
	if dimension.Datatype == semanticmodel.DataTypeDateTimeTZ && dimension.Timezone != "" {
		tz := strings.ReplaceAll(dimension.Timezone, "'", "''")
		expr = "timezone('" + tz + "', CAST(" + expr + " AS TIMESTAMPTZ))"
	}
	if dimension.Grain == "week" && dimension.WeekStart == "sunday" {
		return "DATE_TRUNC('week', " + expr + " + INTERVAL 1 DAY) - INTERVAL 1 DAY"
	}
	return "DATE_TRUNC('" + dimension.Grain + "', " + expr + ")"
}

func sortedAggregateMetricNames(metrics map[string]resolvedAggregateMetric) []string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hasFactAggregates(metrics map[string]resolvedAggregateMetric, fact string) bool {
	for _, metric := range metrics {
		if metric.Fact == fact {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func addPathDependencies(dependencies map[string]struct{}, path []semanticmodel.Relationship) {
	for _, relationship := range path {
		for _, field := range relationshipPhysicalFields(relationship) {
			dependencies[field] = struct{}{}
		}
	}
}
