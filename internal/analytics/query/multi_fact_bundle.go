package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// planMultiFactBundle builds one statement-local governed projection and
// independently pruned grouping aggregates per participating fact, then
// applies the ordinary null-safe full-outer stitch before decoding consumer
// branches. Each governed fact relation is scanned exactly once.
func (p *Planner) planMultiFactBundle(requests []BundleRequest, resolutions []aggregateResolution) (BundlePlan, error) {
	dimensions, branchDimensions, err := multiFactBundleDimensions(resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	facts := bundleFacts(resolutions)
	groupSets, branchGroups := bundleGroupsByFacts(branchDimensions, resolutions)
	aggregates := map[string]resolvedAggregateMetric{}
	metrics := map[string]semanticmodel.Expression{}
	for _, resolved := range resolutions {
		for name, metric := range resolved.Aggregates {
			aggregates[name] = metric
		}
		for name, metric := range resolved.Metrics {
			metrics[name] = metric
		}
	}
	metricNames := sortedAggregateMetricNames(aggregates)
	metricColumns := map[string]string{}
	for i, name := range metricNames {
		metricColumns[name] = fmt.Sprintf("__m%d", i)
	}

	ctes := []string{}
	args := []any{}
	dependencies := map[string]struct{}{}
	filterResolution := aggregateResolution{Facts: append([]string{}, facts...), MultiFact: len(facts) > 1}
	for factIndex, fact := range facts {
		factCTEs, factArgs, factDependencies, compileErr := p.compileMultiFactBundleFact(requests, resolutions, filterResolution, dimensions, groupSets, branchGroups, branchDimensions, aggregates, metricNames, metricColumns, fact, factIndex)
		if compileErr != nil {
			return BundlePlan{}, compileErr
		}
		ctes = append(ctes, factCTEs...)
		args = append(args, factArgs...)
		for _, dependency := range factDependencies {
			dependencies[dependency] = struct{}{}
		}
	}
	stitched, stitchCTEs := stitchBundleFacts(facts, dimensions, aggregates, metricNames, metricColumns)
	ctes = append(ctes, stitchCTEs...)

	memberNames, memberColumns := bundleMemberColumns(resolutions)
	memberExpr, err := renderBundleMembers(aggregates, metrics, metricColumns, memberNames)
	if err != nil {
		return BundlePlan{}, err
	}
	physicalColumns := []string{BundleBranchColumn}
	for i := range dimensions {
		physicalColumns = append(physicalColumns, fmt.Sprintf("__d%d", i))
	}
	physicalColumns = append(physicalColumns, memberColumns...)
	branches, branchSQL, err := renderMultiFactBundleBranches(requests, resolutions, branchDimensions, branchGroups, dimensions, memberNames, memberColumns, memberExpr, physicalColumns, stitched)
	if err != nil {
		return BundlePlan{}, err
	}
	physicalColumns = append(physicalColumns, BundleRowColumn)
	deps := make([]string, 0, len(dependencies))
	for dependency := range dependencies {
		deps = append(deps, dependency)
	}
	sort.Strings(deps)
	sql := "WITH " + strings.Join(ctes, ",\n") + "\n" + bundleUnionSQL(branchSQL) + "\nORDER BY " + BundleBranchColumn + " ASC, " + BundleRowColumn + " ASC"
	return BundlePlan{Plan: Plan{SQL: sql, Args: args, Columns: physicalColumns, Mode: "multi_fact", Facts: append([]string{}, facts...), PhysicalDependencies: deps}, Branches: branches}, nil
}

func multiFactBundleDimensions(resolutions []aggregateResolution) ([]bundleDimension, [][]int, error) {
	dimensions := []bundleDimension{}
	indexes := map[string]int{}
	branches := make([][]int, len(resolutions))
	for branchIndex, resolved := range resolutions {
		for _, dimension := range resolved.Dimensions {
			key := bundleDimensionKey(dimension)
			index, ok := indexes[key]
			if !ok {
				index = len(dimensions)
				indexes[key] = index
				dimensions = append(dimensions, bundleDimension{dimension: dimension, physical: fmt.Sprintf("__d%d", index)})
			}
			branches[branchIndex] = append(branches[branchIndex], index)
		}
	}
	return dimensions, branches, nil
}

func bundleFacts(resolutions []aggregateResolution) []string {
	set := map[string]bool{}
	for _, resolved := range resolutions {
		for _, fact := range resolved.Facts {
			set[fact] = true
		}
	}
	facts := make([]string, 0, len(set))
	for fact := range set {
		facts = append(facts, fact)
	}
	sort.Strings(facts)
	return facts
}

func bundleGroupsByFacts(branchDimensions [][]int, resolutions []aggregateResolution) ([][]int, []int) {
	groups := [][]int{}
	byKey := map[string]int{}
	branchGroups := make([]int, len(branchDimensions))
	for branchIndex, indexes := range branchDimensions {
		parts := make([]string, len(indexes))
		for i, index := range indexes {
			parts[i] = fmt.Sprint(index)
		}
		key := strings.Join(resolutions[branchIndex].Facts, ",") + "|" + strings.Join(parts, ",")
		group, ok := byKey[key]
		if !ok {
			group = len(groups)
			byKey[key] = group
			groups = append(groups, append([]int{}, indexes...))
		}
		branchGroups[branchIndex] = group
	}
	return groups, branchGroups
}

func (p *Planner) compileMultiFactBundleFact(requests []BundleRequest, resolutions []aggregateResolution, filterResolution aggregateResolution, dimensions []bundleDimension, groupSets [][]int, branchGroups []int, branchDimensions [][]int, metrics map[string]resolvedAggregateMetric, metricNames []string, metricColumns map[string]string, fact string, factIndex int) ([]string, []any, []string, error) {
	bindings := []physicalFieldBinding{}
	dependencies := map[string]struct{}{fact: {}}
	factGroupSet := map[int]bool{}
	dimensionGroups := make([]map[int]bool, len(dimensions))
	for branchIndex, resolved := range resolutions {
		if !bundleFactParticipates(resolved, fact) {
			continue
		}
		group := branchGroups[branchIndex]
		factGroupSet[group] = true
		for _, dimensionIndex := range branchDimensions[branchIndex] {
			if dimensionGroups[dimensionIndex] == nil {
				dimensionGroups[dimensionIndex] = map[int]bool{}
			}
			dimensionGroups[dimensionIndex][group] = true
		}
	}
	for dimensionIndex, item := range dimensions {
		if len(dimensionGroups[dimensionIndex]) == 0 {
			continue
		}
		field, path, err := p.aggregateDimensionBinding(fact, item.dimension)
		if err != nil {
			return nil, nil, nil, err
		}
		bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
		dependencies[field] = struct{}{}
		addPathDependencies(dependencies, path)
	}
	for _, metric := range metrics {
		if metric.Fact != fact {
			continue
		}
		for _, field := range aggregateMetricPhysicalFields(metric) {
			physical, err := p.Model.ResolveDimension(field)
			if err != nil {
				return nil, nil, nil, err
			}
			path, err := p.relationshipPath(fact, physical.Table)
			if err != nil {
				return nil, nil, nil, err
			}
			bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
			dependencies[field] = struct{}{}
			addPathDependencies(dependencies, path)
		}
	}
	filterBindings, err := p.factFilterFields(requests[0].Request.Filters, filterResolution, fact)
	if err != nil {
		return nil, nil, nil, err
	}
	bindings = append(bindings, filterBindings...)
	for _, binding := range filterBindings {
		dependencies[binding.Field] = struct{}{}
		addPathDependencies(dependencies, binding.Path)
	}
	aliases, err := p.aliasesForFact(fact, bindings)
	if err != nil {
		return nil, nil, nil, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return nil, nil, nil, err
	}
	baseSelects := []string{}
	baseArgs := []any{}
	for dimensionIndex, item := range dimensions {
		if len(dimensionGroups[dimensionIndex]) == 0 {
			continue
		}
		field, path, err := p.aggregateDimensionBinding(fact, item.dimension)
		if err != nil {
			return nil, nil, nil, err
		}
		physical, err := p.Model.ResolveDimension(field)
		if err != nil {
			return nil, nil, nil, err
		}
		expr, err := dimensionExprForPath(physical, aliases, path)
		if err != nil {
			return nil, nil, nil, err
		}
		expr = applyTimeSemantics(expr, item.dimension)
		expr = canonicalDimensionExpr(expr, item.dimension.Type)
		baseSelects = append(baseSelects, expr+fmt.Sprintf(" AS __d%d", dimensionIndex))
	}
	factAliases, err := aliases.context(nil)
	if err != nil {
		return nil, nil, nil, err
	}
	for metricIndex, name := range metricNames {
		metric := metrics[name]
		if metric.Fact != fact {
			continue
		}
		if metric.Aggregation != "count" {
			raw, err := rawAggregateMetricExpr(p.Model, metric, factAliases)
			if err != nil {
				return nil, nil, nil, err
			}
			baseSelects = append(baseSelects, raw+fmt.Sprintf(" AS __v%d", metricIndex))
		}
		if len(metric.Filters) > 0 {
			parts := []string{}
			for _, filter := range metric.Filters {
				physical, err := p.Model.ResolveDimension(filter.Field)
				if err != nil {
					return nil, nil, nil, err
				}
				path, err := p.relationshipPath(fact, physical.Table)
				if err != nil {
					return nil, nil, nil, err
				}
				filterExpr, err := dimensionExprForPath(physical, aliases, path)
				if err != nil {
					return nil, nil, nil, err
				}
				part, filterArgs, err := filterSQL(filterExpr, Filter{Operator: filter.Operator, Values: filter.Values})
				if err != nil {
					return nil, nil, nil, err
				}
				if part != "" {
					parts = append(parts, part)
					baseArgs = append(baseArgs, filterArgs...)
				}
			}
			baseSelects = append(baseSelects, "("+strings.Join(parts, " AND ")+") AS "+fmt.Sprintf("__f%d", metricIndex))
		}
	}
	// A scalar count-only fact has neither dimension nor raw input columns. It
	// still needs a syntactically valid governed base whose rows COUNT(*) can
	// consume, including when the physical fact is empty.
	if len(baseSelects) == 0 {
		baseSelects = append(baseSelects, "1 AS __row")
	}
	where, whereArgs, err := p.factWhereParts(requests[0].Request.Filters, filterResolution, fact, aliases)
	if err != nil {
		return nil, nil, nil, err
	}
	baseArgs = append(baseArgs, whereArgs...)
	baseName := fmt.Sprintf("bundle_base_%d", factIndex)
	aggregateName := fmt.Sprintf("bundle_fact_%d", factIndex)
	base := baseName + " AS MATERIALIZED (\n  SELECT " + strings.Join(baseSelects, ", ") + "\n  FROM " + strings.ReplaceAll(from, "\n", "\n  ")
	if len(where) > 0 {
		base += "\n  WHERE " + strings.Join(where, " AND ")
	}
	base += "\n)"
	groupCTEs := make([]string, 0, len(factGroupSet))
	groupNames := make([]string, 0, len(factGroupSet))
	for groupIndex := range groupSets {
		if !factGroupSet[groupIndex] {
			continue
		}
		selects := []string{fmt.Sprintf("%d AS __bundle_group", groupIndex)}
		groupBy := []string{}
		for dimensionIndex := range dimensions {
			if containsInt(groupSets[groupIndex], dimensionIndex) {
				selects = append(selects, fmt.Sprintf("__d%d", dimensionIndex))
				groupBy = append(groupBy, fmt.Sprintf("__d%d", dimensionIndex))
			} else {
				selects = append(selects, fmt.Sprintf("NULL AS __d%d", dimensionIndex))
			}
		}
		for metricIndex, name := range metricNames {
			metric := metrics[name]
			if metric.Fact != fact {
				continue
			}
			selected := false
			for branchIndex, resolved := range resolutions {
				if branchGroups[branchIndex] != groupIndex {
					continue
				}
				if _, selected = resolved.Aggregates[name]; selected {
					break
				}
			}
			if !selected {
				selects = append(selects, "NULL AS "+metricColumns[name])
				continue
			}
			expr, aggregateErr := bundleFactMetricAggregate(metric, metricIndex)
			if aggregateErr != nil {
				return nil, nil, nil, fmt.Errorf("metric %q: %w", name, aggregateErr)
			}
			selects = append(selects, expr+" AS "+metricColumns[name])
		}
		groupName := fmt.Sprintf("bundle_group_%d_%d", factIndex, groupIndex)
		groupNames = append(groupNames, groupName)
		groupSQL := groupName + " AS (\n  SELECT " + strings.Join(selects, ", ") + "\n  FROM " + baseName
		if len(groupBy) > 0 {
			groupSQL += "\n  GROUP BY " + strings.Join(groupBy, ", ")
		}
		groupCTEs = append(groupCTEs, groupSQL+"\n)")
	}
	groupSelects := make([]string, len(groupNames))
	for i, groupName := range groupNames {
		groupSelects[i] = "SELECT * FROM " + groupName
	}
	aggregate := aggregateName + " AS (\n  " + strings.Join(groupSelects, "\n  UNION ALL\n  ") + "\n)"
	deps := make([]string, 0, len(dependencies))
	for dependency := range dependencies {
		deps = append(deps, dependency)
	}
	sort.Strings(deps)
	ctes := append([]string{base}, groupCTEs...)
	ctes = append(ctes, aggregate)
	return ctes, baseArgs, deps, nil
}

func bundleFactMetricAggregate(metric resolvedAggregateMetric, metricIndex int) (string, error) {
	input := fmt.Sprintf("__v%d", metricIndex)
	expr := ""
	switch metric.Aggregation {
	case "count":
		expr = "COUNT(*)"
	case "count_distinct":
		expr = "COUNT(DISTINCT " + input + ")"
	case "sum", "avg", "min", "max":
		expr = strings.ToUpper(metric.Aggregation) + "(" + input + ")"
	default:
		return "", fmt.Errorf("unsupported aggregation %q", metric.Aggregation)
	}
	if len(metric.Filters) > 0 {
		expr += fmt.Sprintf(" FILTER (WHERE __f%d)", metricIndex)
	}
	if metric.Empty == "zero" && metric.Aggregation != "count" && metric.Aggregation != "count_distinct" {
		expr = "COALESCE(" + expr + ", 0)"
	}
	return expr, nil
}

func bundleFactParticipates(resolved aggregateResolution, fact string) bool {
	if hasFactAggregates(resolved.Aggregates, fact) {
		return true
	}
	if len(resolved.Aggregates) != 0 {
		return false
	}
	for _, candidate := range resolved.Facts {
		if candidate == fact {
			return true
		}
	}
	return false
}

func stitchBundleFacts(facts []string, dimensions []bundleDimension, metrics map[string]resolvedAggregateMetric, metricNames []string, metricColumns map[string]string) (string, []string) {
	left := "bundle_fact_0"
	available := map[string]bool{}
	for _, name := range metricNames {
		if metrics[name].Fact == facts[0] {
			available[name] = true
		}
	}
	ctes := []string{}
	for factIndex := 1; factIndex < len(facts); factIndex++ {
		right := fmt.Sprintf("bundle_fact_%d", factIndex)
		selects := []string{"COALESCE(l.__bundle_group, r.__bundle_group) AS __bundle_group"}
		joins := []string{"l.__bundle_group = r.__bundle_group"}
		for dimensionIndex := range dimensions {
			column := fmt.Sprintf("__d%d", dimensionIndex)
			selects = append(selects, "COALESCE(l."+column+", r."+column+") AS "+column)
			joins = append(joins, "l."+column+" IS NOT DISTINCT FROM r."+column)
		}
		for _, name := range metricNames {
			column := metricColumns[name]
			if available[name] {
				selects = append(selects, "l."+column+" AS "+column)
			} else if metrics[name].Fact == facts[factIndex] {
				selects = append(selects, "r."+column+" AS "+column)
			}
		}
		for _, name := range metricNames {
			if metrics[name].Fact == facts[factIndex] {
				available[name] = true
			}
		}
		cteName := fmt.Sprintf("bundle_stitch_%d", factIndex)
		ctes = append(ctes, cteName+" AS (\n  SELECT "+strings.Join(selects, ", ")+"\n  FROM "+left+" l\n  FULL OUTER JOIN "+right+" r ON "+strings.Join(joins, " AND ")+"\n)")
		left = cteName
	}
	return left, ctes
}

func renderMultiFactBundleBranches(requests []BundleRequest, resolutions []aggregateResolution, branchDimensions [][]int, branchGroups []int, dimensions []bundleDimension, memberNames, memberColumns, memberExpr, physicalColumns []string, source string) ([]BundleBranch, []string, error) {
	branches := make([]BundleBranch, 0, len(requests))
	sqlBranches := make([]string, 0, len(requests))
	for branchIndex, item := range requests {
		mapping := map[string]string{}
		columns := []BundleColumn{}
		outputs := map[string]bool{}
		for local, dimension := range resolutions[branchIndex].Dimensions {
			if err := addOutputColumn(outputs, dimension.Alias); err != nil {
				return nil, nil, err
			}
			physical := fmt.Sprintf("__d%d", branchDimensions[branchIndex][local])
			mapping[dimension.Alias] = physical
			columns = append(columns, BundleColumn{Output: dimension.Alias, Physical: physical})
		}
		for _, member := range resolutions[branchIndex].Members {
			if err := addOutputColumn(outputs, member.Alias); err != nil {
				return nil, nil, err
			}
			physical := memberColumns[indexString(memberNames, member.Name)]
			mapping[member.Alias] = physical
			columns = append(columns, BundleColumn{Output: member.Alias, Physical: physical})
		}
		branches = append(branches, BundleBranch{ID: item.ID, Ordinal: branchIndex, Columns: columns})
		selects := []string{fmt.Sprintf("%d AS %s", branchIndex, BundleBranchColumn)}
		for dimensionIndex := range dimensions {
			selects = append(selects, fmt.Sprintf("__d%d", dimensionIndex))
		}
		for memberIndex, expression := range memberExpr {
			selects = append(selects, expression+" AS "+memberColumns[memberIndex])
		}
		core := "SELECT " + strings.Join(selects, ", ") + "\nFROM " + source + "\nWHERE __bundle_group = " + fmt.Sprint(branchGroups[branchIndex])
		sorts := make([]Sort, len(item.Request.Sort))
		for i, sortSpec := range item.Request.Sort {
			physical, ok := mapping[sortSpec.Field]
			if !ok {
				return nil, nil, fmt.Errorf("bundle branch %q sort references unknown output %q", item.ID, sortSpec.Field)
			}
			sorts[i] = Sort{Field: physical, Direction: sortSpec.Direction}
		}
		set := map[string]bool{}
		for _, column := range physicalColumns {
			set[column] = true
		}
		orderSorts := append([]Sort{}, sorts...)
		ordered := map[string]bool{}
		for _, sortSpec := range orderSorts {
			ordered[sortSpec.Field] = true
		}
		for _, column := range columns {
			if !ordered[column.Physical] {
				orderSorts = append(orderSorts, Sort{Field: column.Physical, Direction: "asc"})
				ordered[column.Physical] = true
			}
		}
		orderParts, err := sortSQL(orderSorts, set)
		if err != nil {
			return nil, nil, err
		}
		var sql strings.Builder
		sql.WriteString("SELECT branch_data.*, ROW_NUMBER() OVER (ORDER BY " + strings.Join(orderParts, ", ") + ") AS " + BundleRowColumn)
		sql.WriteString("\nFROM (\n" + indentSQL(core, "  ") + "\n) branch_data")
		if err := writeOrderLimitOffset(&sql, orderSorts, set, item.Request.Limit, item.Request.Offset); err != nil {
			return nil, nil, err
		}
		sqlBranches = append(sqlBranches, "("+sql.String()+")")
	}
	return branches, sqlBranches, nil
}
