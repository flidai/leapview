package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

type aggregateDimension struct {
	Name     string
	Alias    string
	Type     string
	Grain    string
	Semantic bool
	Physical semanticmodel.MetricDimension
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
	Measures   map[string]ResolvedMeasure
	Metrics    map[string]semanticmodel.Expression
	Facts      []string
	MultiFact  bool
	Masks      columnMaskSet
}

// planAggregate preserves fact grain by compiling every fact through only safe
// relationship paths, aggregating each fact independently, and stitching the
// resulting grouped rows. Facts are never joined to each other before their
// measures have been reduced to the requested conformed dimensions.
func (p *Planner) planAggregate(request Request) (Plan, error) {
	resolved, err := p.resolveAggregate(request)
	if err != nil {
		return Plan{}, err
	}
	if err := p.validateAggregateFilters(request.Filters, resolved); err != nil {
		return Plan{}, err
	}

	measureNames := sortedMeasureNames(resolved.Measures)
	measureColumns := map[string]string{}
	for index, name := range measureNames {
		measureColumns[name] = fmt.Sprintf("__m%d", index)
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
			measureColumns,
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

	source, stitchCTEs := stitchFacts(resolved.Facts, resolved.Dimensions, resolved.Measures, measureNames, measureColumns)
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
	measureSQL := func(name string) (string, error) {
		measure, ok := resolved.Measures[name]
		if !ok {
			return "", fmt.Errorf("unknown measure %q", name)
		}
		expr := "s." + measureColumns[name]
		if measure.Empty == "zero" {
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
			if _, ok := resolved.Measures[ref]; ok {
				return measureSQL(ref)
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
		if member.Kind == "measure" {
			expr, err = measureSQL(member.Name)
		} else {
			expr, err = renderMetric(member.Name)
		}
		if err != nil {
			return Plan{}, err
		}
		selects = append(selects, expr+" AS "+member.Alias)
		columns = append(columns, member.Alias)
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
		Measures: map[string]ResolvedMeasure{},
		Metrics:  map[string]semanticmodel.Expression{},
		Masks:    masks,
	}
	visiting := map[string]bool{}
	var addMetric func(string) error
	addMeasure := func(name string) error {
		measure, err := p.Model.ResolveMeasure(name)
		if err != nil {
			return err
		}
		resolvedMeasure := p.resolvedMeasure(name, measure)
		if masks.matchesMeasure(name, resolvedMeasure) {
			return fmt.Errorf("measure %q depends on a masked field", name)
		}
		resolved.Measures[name] = resolvedMeasure
		return nil
	}
	addMetric = func(name string) error {
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
		visiting[name] = true
		expression, err := p.metricExpression(name, metric)
		if err != nil {
			return fmt.Errorf("metric %q: %w", name, err)
		}
		for _, ref := range expression.References() {
			if _, ok := p.Model.Measures[ref]; ok {
				if err := addMeasure(ref); err != nil {
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

	for _, item := range request.Measures {
		name := strings.TrimSpace(item.Field)
		if name == "" {
			return aggregateResolution{}, fmt.Errorf("selected measure or metric is required")
		}
		alias, err := outputAlias(item)
		if err != nil {
			return aggregateResolution{}, err
		}
		if _, ok := p.Model.Measures[name]; ok {
			if err := addMeasure(name); err != nil {
				return aggregateResolution{}, err
			}
			resolved.Members = append(resolved.Members, aggregateMember{Name: name, Alias: alias, Kind: "measure"})
			continue
		}
		if _, ok := p.Model.Metrics[name]; ok {
			if _, masked := masks[strings.ToLower(name)]; masked {
				return aggregateResolution{}, fmt.Errorf("metric %q is masked", name)
			}
			if err := addMetric(name); err != nil {
				return aggregateResolution{}, err
			}
			resolved.Members = append(resolved.Members, aggregateMember{Name: name, Alias: alias, Kind: "metric"})
			continue
		}
		return aggregateResolution{}, fmt.Errorf("unknown measure or metric %q", name)
	}

	factSet := map[string]struct{}{}
	for _, measure := range resolved.Measures {
		factSet[measure.Fact] = struct{}{}
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
				Name: item.Field, Alias: alias, Type: dimension.Type, Grain: grain, Semantic: true,
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
			Name: item.Field, Alias: alias, Type: physical.Type, Grain: grain, Physical: physical,
		})
	}

	if len(factSet) == 0 {
		if len(resolved.Dimensions) == 0 {
			return aggregateResolution{}, fmt.Errorf("aggregate query requires a measure, metric, or dimension")
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
// it. Queries with selected measures keep their measure-owned fact set and are
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

func (p *Planner) compileFactAggregate(request Request, resolved aggregateResolution, fact string, factIndex int, measureColumns map[string]string) (string, []any, []string, error) {
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
	for _, measure := range resolved.Measures {
		if measure.Fact != fact {
			continue
		}
		for _, field := range measurePhysicalFields(measure) {
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
	aliases, err := p.aliasesForFact(fact, bindings)
	if err != nil {
		return "", nil, nil, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return "", nil, nil, err
	}

	selects := []string{}
	for index, dimension := range resolved.Dimensions {
		field, path, _ := p.aggregateDimensionBinding(fact, dimension)
		physical, _ := p.Model.ResolveDimension(field)
		expr, err := dimensionExprForPath(physical, aliases, path)
		if err != nil {
			return "", nil, nil, err
		}
		expr = canonicalDimensionExpr(expr, dimension.Type)
		if dimension.Grain != "" {
			expr = "DATE_TRUNC('" + dimension.Grain + "', " + expr + ")"
		}
		selects = append(selects, fmt.Sprintf("%s AS __d%d", expr, index))
	}
	measureArgs := []any{}
	for _, name := range sortedMeasureNames(resolved.Measures) {
		measure := resolved.Measures[name]
		if measure.Fact != fact {
			continue
		}
		factAliases, err := aliases.context(nil)
		if err != nil {
			return "", nil, nil, err
		}
		expr, err := measureExpr(p.Model, measure, factAliases)
		if err != nil {
			return "", nil, nil, err
		}
		measureFilterParts := []string{}
		for _, filter := range measure.Filters {
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
				measureFilterParts = append(measureFilterParts, part)
				measureArgs = append(measureArgs, partArgs...)
			}
		}
		if len(measureFilterParts) > 0 {
			expr += " FILTER (WHERE " + strings.Join(measureFilterParts, " AND ") + ")"
		}
		if measure.Empty == "zero" && measure.Aggregation != "count" && measure.Aggregation != "count_distinct" {
			expr = "COALESCE(" + expr + ", 0)"
		}
		selects = append(selects, expr+" AS "+measureColumns[name])
	}
	whereParts, whereArgs, err := p.factWhereParts(request.Filters, resolved, fact, aliases)
	if err != nil {
		return "", nil, nil, err
	}
	if len(selects) == 0 {
		return "", nil, nil, fmt.Errorf("fact %q has no selected dimensions or measures", fact)
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
		if hasFactMeasures(resolved.Measures, fact) {
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
	return sql.String(), append(measureArgs, whereArgs...), dependencyList, nil
}

func stitchFacts(facts []string, dimensions []aggregateDimension, measures map[string]ResolvedMeasure, measureNames []string, measureColumns map[string]string) (string, []string) {
	if len(facts) == 1 {
		return "fact_0 s", nil
	}
	if len(dimensions) == 0 {
		selects := []string{}
		from := []string{}
		for index := range facts {
			alias := fmt.Sprintf("f%d", index)
			from = append(from, fmt.Sprintf("fact_%d %s", index, alias))
			for _, name := range measureNames {
				if measures[name].Fact == facts[index] {
					selects = append(selects, fmt.Sprintf("%s.%s AS %s", alias, measureColumns[name], measureColumns[name]))
				}
			}
		}
		cte := "stitched AS (\n  SELECT " + strings.Join(selects, ", ") + "\n  FROM " + strings.Join(from, " CROSS JOIN ") + "\n)"
		return "stitched s", []string{cte}
	}
	ctes := []string{}
	leftName := "fact_0"
	availableMeasures := map[string]bool{}
	for _, name := range measureNames {
		if measures[name].Fact == facts[0] {
			availableMeasures[name] = true
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
		for _, name := range measureNames {
			column := measureColumns[name]
			if availableMeasures[name] {
				selects = append(selects, leftAlias+"."+column+" AS "+column)
			} else if measures[name].Fact == facts[index] {
				selects = append(selects, rightAlias+"."+column+" AS "+column)
			}
		}
		for _, name := range measureNames {
			if measures[name].Fact == facts[index] {
				availableMeasures[name] = true
			}
		}
		cteName := fmt.Sprintf("stitch_%d", index)
		ctes = append(ctes, fmt.Sprintf("%s AS (\n  SELECT %s\n  FROM %s %s\n  FULL OUTER JOIN %s %s ON %s\n)", cteName, strings.Join(selects, ", "), leftName, leftAlias, rightName, rightAlias, strings.Join(joins, " AND ")))
		leftName = cteName
	}
	return leftName + " s", ctes
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
	return filterSQL(expr, filter)
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
	path, err := p.relationshipPath(fact, physical.Table)
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

func sortedMeasureNames(measures map[string]ResolvedMeasure) []string {
	names := make([]string, 0, len(measures))
	for name := range measures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hasFactMeasures(measures map[string]ResolvedMeasure, fact string) bool {
	for _, measure := range measures {
		if measure.Fact == fact {
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
		dependencies[relationship.From] = struct{}{}
		dependencies[relationship.To] = struct{}{}
	}
}
