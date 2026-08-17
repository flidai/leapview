package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

type bundleDimension struct {
	dimension aggregateDimension
	physical  string
}

// PlanBundle fuses independently shaped aggregates with identical governed
// scope. Single-fact bundles scan their fact once; heterogeneous and
// multi-fact bundles materialize one statement-local projection per fact and
// retain the regular outer-stitch semantics before decoding consumer branches.
func (p *Planner) PlanBundle(requests []BundleRequest) (BundlePlan, error) {
	if len(requests) == 0 {
		return BundlePlan{}, fmt.Errorf("aggregate bundle requires at least one request")
	}
	resolutions := make([]aggregateResolution, len(requests))
	fact := ""
	factSignature := ""
	heterogeneousFacts := false
	containsMultiFact := false
	scopeFingerprint := ""
	ids := map[string]bool{}
	for i, item := range requests {
		if item.ID == "" || ids[item.ID] {
			return BundlePlan{}, fmt.Errorf("aggregate bundle branch IDs must be non-empty and unique")
		}
		ids[item.ID] = true
		resolved, err := p.resolveAggregate(item.Request)
		if err != nil {
			return BundlePlan{}, fmt.Errorf("bundle branch %q: %w", item.ID, err)
		}
		if err := p.validateAggregateFilters(item.Request.Filters, resolved); err != nil {
			return BundlePlan{}, fmt.Errorf("bundle branch %q: %w", item.ID, err)
		}
		if len(resolved.Facts) == 0 {
			return BundlePlan{}, fmt.Errorf("bundle branch %q has no participating fact", item.ID)
		}
		// The ordinary aggregate planner rejects masked dependencies member by
		// member. Until a bundle can preserve the exact transformer boundary for
		// every branch, fail closed instead of compiling a shared unmasked base.
		if len(item.Request.ColumnMasks) > 0 {
			return BundlePlan{}, fmt.Errorf("bundle branch %q has column masks and is not safely bundleable", item.ID)
		}
		branchFacts := strings.Join(resolved.Facts, ",")
		if factSignature == "" {
			factSignature = branchFacts
			fact = resolved.Facts[0]
		} else if branchFacts != factSignature {
			heterogeneousFacts = true
		}
		containsMultiFact = containsMultiFact || resolved.MultiFact
		fingerprint, err := p.bundleScopeFingerprint(item.Request, resolved)
		if err != nil {
			return BundlePlan{}, fmt.Errorf("bundle branch %q scope fingerprint: %w", item.ID, err)
		}
		if scopeFingerprint == "" {
			scopeFingerprint = fingerprint
		} else if !heterogeneousFacts && fingerprint != scopeFingerprint {
			return BundlePlan{}, fmt.Errorf("bundle branch %q has a different governed scope", item.ID)
		}
		resolutions[i] = resolved
	}
	if containsMultiFact || heterogeneousFacts {
		return p.renderBundlePlanIR(requests, resolutions)
	}
	return p.renderBundlePlanIR(requests, resolutions)

	dimensions := []bundleDimension{}
	dimensionIndex := map[string]int{}
	branchDimensions := make([][]int, len(requests))
	aggregates := map[string]resolvedAggregateMetric{}
	metrics := map[string]semanticmodel.Expression{}
	bindings := []physicalFieldBinding{}
	dependencies := map[string]struct{}{fact: {}}
	for branchIndex, resolved := range resolutions {
		for _, dimension := range resolved.Dimensions {
			key := bundleDimensionKey(dimension)
			index, ok := dimensionIndex[key]
			if !ok {
				index = len(dimensions)
				dimensionIndex[key] = index
				dimensions = append(dimensions, bundleDimension{dimension: dimension, physical: fmt.Sprintf("__d%d", index)})
			}
			branchDimensions[branchIndex] = append(branchDimensions[branchIndex], index)
			field, path, err := p.aggregateDimensionBinding(fact, dimension)
			if err != nil {
				return BundlePlan{}, err
			}
			bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
			dependencies[field] = struct{}{}
			addPathDependencies(dependencies, path)
		}
		for name, metric := range resolved.Aggregates {
			aggregates[name] = metric
			for _, field := range aggregateMetricPhysicalFields(metric) {
				physical, err := p.model.ResolveDimension(field)
				if err != nil {
					return BundlePlan{}, err
				}
				path, err := p.relationshipPath(fact, physical.Table)
				if err != nil {
					return BundlePlan{}, err
				}
				bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
				dependencies[field] = struct{}{}
				addPathDependencies(dependencies, path)
			}
		}
		for name, expression := range resolved.Metrics {
			metrics[name] = expression
		}
	}
	filterBindings, err := p.factFilterFields(requests[0].Request.Filters, resolutions[0], fact)
	if err != nil {
		return BundlePlan{}, err
	}
	bindings = append(bindings, filterBindings...)
	for _, binding := range filterBindings {
		dependencies[binding.Field] = struct{}{}
		addPathDependencies(dependencies, binding.Path)
	}
	for _, metric := range aggregates {
		if len(metric.NamedFilters) == 0 {
			continue
		}
		whereFilters := scopeMetricWhereFilters(namedMetricFilters(metric.NamedFilters), fact)
		whereBindings, err := p.factFilterFields(whereFilters, resolutions[0], fact)
		if err != nil {
			return BundlePlan{}, err
		}
		bindings = append(bindings, whereBindings...)
		for _, binding := range whereBindings {
			dependencies[binding.Field] = struct{}{}
			addPathDependencies(dependencies, binding.Path)
		}
	}
	aliases, err := p.aliasesForFact(fact, bindings)
	if err != nil {
		return BundlePlan{}, err
	}
	from, err := joinPathSQL(p, aliases)
	if err != nil {
		return BundlePlan{}, err
	}

	baseSelects := []string{}
	for _, item := range dimensions {
		field, path, err := p.aggregateDimensionBinding(fact, item.dimension)
		if err != nil {
			return BundlePlan{}, err
		}
		physical, err := p.model.ResolveDimension(field)
		if err != nil {
			return BundlePlan{}, err
		}
		expr, err := dimensionExprForPath(physical, aliases, path)
		if err != nil {
			return BundlePlan{}, err
		}
		expr = applyTimeSemantics(expr, item.dimension)
		expr = canonicalDimensionExpr(expr, item.dimension.Type)
		baseSelects = append(baseSelects, expr+" AS "+item.physical)
	}
	metricNames := sortedAggregateMetricNames(aggregates)
	metricColumns := map[string]string{}
	baseArgs := []any{}
	factAliases, err := aliases.context(nil)
	if err != nil {
		return BundlePlan{}, err
	}
	for i, name := range metricNames {
		metric := aggregates[name]
		metricColumns[name] = fmt.Sprintf("__m%d", i)
		raw, err := rawAggregateMetricExpr(p.model, metric, factAliases)
		if err != nil {
			return BundlePlan{}, err
		}
		baseSelects = append(baseSelects, raw+fmt.Sprintf(" AS __v%d", i))
		if len(metric.Filters) > 0 || len(metric.NamedFilters) > 0 {
			parts := []string{}
			for _, filter := range metric.Filters {
				physical, err := p.model.ResolveDimension(filter.Field)
				if err != nil {
					return BundlePlan{}, err
				}
				path, err := p.relationshipPath(fact, physical.Table)
				if err != nil {
					return BundlePlan{}, err
				}
				filterExpr, err := dimensionExprForPath(physical, aliases, path)
				if err != nil {
					return BundlePlan{}, err
				}
				part, args, err := filterSQL(filterExpr, Filter{Operator: filter.Operator, Values: filter.Values})
				if err != nil {
					return BundlePlan{}, err
				}
				if part != "" {
					parts = append(parts, part)
					baseArgs = append(baseArgs, args...)
				}
			}
			for _, filter := range scopeMetricWhereFilters(namedMetricFilters(metric.NamedFilters), fact) {
				part, args, err := p.factFilterPart(filter, resolutions[0], fact, aliases)
				if err != nil {
					return BundlePlan{}, err
				}
				if part != "" {
					parts = append(parts, part)
					baseArgs = append(baseArgs, args...)
				}
			}
			baseSelects = append(baseSelects, "("+strings.Join(parts, " AND ")+") AS "+fmt.Sprintf("__f%d", i))
		}
	}
	if len(baseSelects) == 0 {
		baseSelects = append(baseSelects, "1 AS __row")
	}
	where, whereArgs, err := p.factWhereParts(requests[0].Request.Filters, resolutions[0], fact, aliases)
	if err != nil {
		return BundlePlan{}, err
	}
	baseArgs = append(baseArgs, whereArgs...)
	var base strings.Builder
	base.WriteString("governed_base AS (\n  SELECT " + strings.Join(baseSelects, ", ") + "\n  FROM " + strings.ReplaceAll(from, "\n", "\n  "))
	if len(where) > 0 {
		base.WriteString("\n  WHERE " + strings.Join(where, " AND "))
	}
	base.WriteString("\n)")

	groupSets := [][]int{}
	groupIndexes := map[string]int{}
	branchGroups := make([]int, len(branchDimensions))
	for branchIndex, indexes := range branchDimensions {
		columns := make([]string, len(indexes))
		for i, index := range indexes {
			columns[i] = fmt.Sprint(index)
		}
		key := strings.Join(columns, ",")
		groupIndex, exists := groupIndexes[key]
		if !exists {
			groupIndex = len(groupSets)
			groupIndexes[key] = groupIndex
			groupSets = append(groupSets, append([]int{}, indexes...))
		}
		branchGroups[branchIndex] = groupIndex
	}
	groupIDs := make([]string, len(groupSets))
	for i := range groupSets {
		groupIDs[i] = fmt.Sprint(i)
	}
	expanded := "bundle_expanded AS (\n  SELECT governed_base.*, __bundle_group\n  FROM governed_base\n  CROSS JOIN UNNEST([" + strings.Join(groupIDs, ", ") + "]) AS groups(__bundle_group)\n)"

	aggSelects := []string{"__bundle_group"}
	for dimensionIndex := range dimensions {
		groups := []int{}
		for groupIndex, indexes := range groupSets {
			if containsInt(indexes, dimensionIndex) {
				groups = append(groups, groupIndex)
			}
		}
		expr := fmt.Sprintf("CASE WHEN %s THEN __d%d END AS __d%d", integerPredicate("__bundle_group", groups), dimensionIndex, dimensionIndex)
		aggSelects = append(aggSelects, expr)
	}
	for i, name := range metricNames {
		metric := aggregates[name]
		input := fmt.Sprintf("__v%d", i)
		expr := ""
		switch metric.Aggregation {
		case "count":
			expr = "COUNT(" + input + ")"
		case "count_distinct":
			expr = "COUNT(DISTINCT " + input + ")"
		case "sum", "avg", "min", "max":
			expr = strings.ToUpper(metric.Aggregation) + "(" + input + ")"
		default:
			return BundlePlan{}, fmt.Errorf("metric %q has unsupported aggregation %q", name, metric.Aggregation)
		}
		groups := []int{}
		seenGroups := map[int]bool{}
		for branchIndex, resolved := range resolutions {
			if _, selected := resolved.Aggregates[name]; selected && !seenGroups[branchGroups[branchIndex]] {
				seenGroups[branchGroups[branchIndex]] = true
				groups = append(groups, branchGroups[branchIndex])
			}
		}
		filterParts := []string{integerPredicate("__bundle_group", groups)}
		if len(metric.Filters) > 0 || len(metric.NamedFilters) > 0 {
			filterParts = append(filterParts, fmt.Sprintf("__f%d", i))
		}
		expr += " FILTER (WHERE " + strings.Join(filterParts, " AND ") + ")"
		if metric.Empty == "zero" && metric.Aggregation != "count" && metric.Aggregation != "count_distinct" {
			expr = "COALESCE(" + expr + ", 0)"
		}
		aggSelects = append(aggSelects, expr+" AS "+metricColumns[name])
	}
	groupPositions := make([]string, len(dimensions)+1)
	for i := range groupPositions {
		groupPositions[i] = fmt.Sprint(i + 1)
	}
	agg := "bundle_aggregate AS (\n  SELECT " + strings.Join(aggSelects, ", ") + "\n  FROM bundle_expanded\n  GROUP BY " + strings.Join(groupPositions, ", ") + "\n)"

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
	branches := make([]BundleBranch, 0, len(requests))
	branchSQL := make([]string, 0, len(requests))
	for i, item := range requests {
		mapping := map[string]string{}
		columns := []BundleColumn{}
		outputAliases := map[string]bool{}
		for local, dimension := range resolutions[i].Dimensions {
			if err := addOutputColumn(outputAliases, dimension.Alias); err != nil {
				return BundlePlan{}, fmt.Errorf("bundle branch %q: %w", item.ID, err)
			}
			physical := fmt.Sprintf("__d%d", branchDimensions[i][local])
			mapping[dimension.Alias] = physical
			columns = append(columns, BundleColumn{Output: dimension.Alias, Physical: physical})
		}
		for _, member := range resolutions[i].Members {
			if err := addOutputColumn(outputAliases, member.Alias); err != nil {
				return BundlePlan{}, fmt.Errorf("bundle branch %q: %w", item.ID, err)
			}
			physical := memberColumns[indexString(memberNames, member.Name)]
			mapping[member.Alias] = physical
			columns = append(columns, BundleColumn{Output: member.Alias, Physical: physical})
		}
		branches = append(branches, BundleBranch{ID: item.ID, Ordinal: i, Columns: columns})
		selects := []string{fmt.Sprintf("%d AS %s", i, BundleBranchColumn)}
		for d := range dimensions {
			selects = append(selects, fmt.Sprintf("__d%d", d))
		}
		for m, expression := range memberExpr {
			selects = append(selects, expression+" AS "+memberColumns[m])
		}
		var core strings.Builder
		core.WriteString("SELECT " + strings.Join(selects, ", ") + "\nFROM bundle_aggregate")
		core.WriteString(fmt.Sprintf("\nWHERE __bundle_group = %d", branchGroups[i]))
		sorts := make([]Sort, len(item.Request.Sort))
		for s, sortSpec := range item.Request.Sort {
			physical, ok := mapping[sortSpec.Field]
			if !ok {
				return BundlePlan{}, fmt.Errorf("bundle branch %q sort references unknown output %q", item.ID, sortSpec.Field)
			}
			sorts[s] = Sort{Field: physical, Direction: sortSpec.Direction}
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
			return BundlePlan{}, err
		}
		var sql strings.Builder
		sql.WriteString("SELECT branch_data.*, ROW_NUMBER() OVER (ORDER BY " + strings.Join(orderParts, ", ") + ") AS " + BundleRowColumn)
		sql.WriteString("\nFROM (\n" + indentSQL(core.String(), "  ") + "\n) branch_data")
		if err := writeOrderLimitOffset(&sql, orderSorts, set, item.Request.Limit, item.Request.Offset); err != nil {
			return BundlePlan{}, err
		}
		branchSQL = append(branchSQL, "("+sql.String()+")")
	}
	deps := make([]string, 0, len(dependencies))
	for dependency := range dependencies {
		deps = append(deps, dependency)
	}
	sort.Strings(deps)
	physicalColumns = append(physicalColumns, BundleRowColumn)
	planSQL := "WITH " + base.String() + ",\n" + expanded + ",\n" + agg + "\n" + bundleUnionSQL(branchSQL) + "\nORDER BY " + BundleBranchColumn + " ASC, " + BundleRowColumn + " ASC"
	irGraph, err := p.buildBundlePlanIR(requests, resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	fingerprints, err := p.bundleBranchFingerprints(requests, resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	for i := range branches {
		branches[i].Fingerprint = fingerprints[i]
	}
	return BundlePlan{Plan: Plan{SQL: planSQL, Args: baseArgs, Columns: physicalColumns, Mode: "single_fact", Facts: []string{fact}, PhysicalDependencies: deps, IR: irGraph}, Branches: branches}, nil
}

// renderBundlePlanIR is the sole production lowering for aggregate bundles.
// The older SQL lowering above remains an equivalence oracle for focused
// tests, but PlanBundle never selects it.
func (p *Planner) renderBundlePlanIR(requests []BundleRequest, resolutions []aggregateResolution) (BundlePlan, error) {
	irGraph, err := p.buildBundlePlanIR(requests, resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	rendered, err := planir.RenderDuckDB(irGraph)
	if err != nil {
		return BundlePlan{}, fmt.Errorf("render bundle plan IR: %w", err)
	}
	fingerprints, err := p.bundleBranchFingerprints(requests, resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	branches := make([]BundleBranch, len(requests))
	for index, item := range requests {
		columns := make([]BundleColumn, 0, len(resolutions[index].Dimensions)+len(resolutions[index].Members))
		for _, dimension := range resolutions[index].Dimensions {
			columns = append(columns, BundleColumn{Output: dimension.Alias, Physical: fmt.Sprintf("__bundle_%d_%s", index, dimension.Alias)})
		}
		for _, member := range resolutions[index].Members {
			columns = append(columns, BundleColumn{Output: member.Alias, Physical: fmt.Sprintf("__bundle_%d_%s", index, member.Alias)})
		}
		branches[index] = BundleBranch{ID: item.ID, Ordinal: index, Columns: columns, Fingerprint: fingerprints[index]}
	}
	lineage, err := irGraph.Dependencies()
	if err != nil {
		return BundlePlan{}, fmt.Errorf("derive bundle dependencies: %w", err)
	}
	facts := []string{}
	factSet := map[string]bool{}
	multiFact := false
	for _, resolved := range resolutions {
		if resolved.MultiFact {
			multiFact = true
		}
		for _, fact := range resolved.Facts {
			if !factSet[fact] {
				factSet[fact] = true
				facts = append(facts, fact)
			}
		}
	}
	sort.Strings(facts)
	mode := "single_fact"
	if multiFact || len(facts) > 1 {
		mode = "multi_fact"
	}
	return BundlePlan{Plan: Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, Mode: mode, Facts: facts,
		PhysicalDependencies: uniqueStrings(append(append([]string(nil), lineage.Datasets...), lineage.PhysicalFields...)), RelationshipPaths: lineage.RelationshipPaths, IR: irGraph}, Branches: branches}, nil
}

func bundleUnionSQL(branches []string) string {
	if len(branches) == 1 {
		// DuckDB accepts the parenthesized branches around UNION operands, but a
		// lone parenthesized SELECT cannot be followed directly by the final ORDER
		// BY. Preserve the branch-local ordering inside a derived table.
		return "SELECT * FROM " + branches[0] + " bundle_union"
	}
	return strings.Join(branches, "\nUNION ALL\n")
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func integerPredicate(column string, values []int) string {
	if len(values) == 1 {
		return fmt.Sprintf("%s = %d", column, values[0])
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprint(value)
	}
	return column + " IN (" + strings.Join(parts, ", ") + ")"
}

func indentSQL(value, prefix string) string {
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}

func bundleDimensionKey(d aggregateDimension) string {
	return fmt.Sprintf("%t|%s|%s|%s", d.Semantic, d.Name, d.Type, d.Grain)
}

func bundleMemberColumns(resolutions []aggregateResolution) ([]string, []string) {
	set := map[string]bool{}
	names := []string{}
	for _, resolved := range resolutions {
		for _, member := range resolved.Members {
			if !set[member.Name] {
				set[member.Name] = true
				names = append(names, member.Name)
			}
		}
	}
	columns := make([]string, len(names))
	for i := range names {
		columns[i] = fmt.Sprintf("__o%d", i)
	}
	return names, columns
}

func renderBundleMembers(aggregates map[string]resolvedAggregateMetric, metrics map[string]semanticmodel.Expression, metricColumns map[string]string, names []string) ([]string, error) {
	cache := map[string]string{}
	aggregateSQL := func(name string) (string, error) {
		metric, ok := aggregates[name]
		if !ok {
			return "", fmt.Errorf("unknown metric %q", name)
		}
		expr := metricColumns[name]
		if metric.Empty == "zero" {
			expr = "COALESCE(" + expr + ", 0)"
		}
		return expr, nil
	}
	var metricSQL func(string) (string, error)
	metricSQL = func(name string) (string, error) {
		if value, ok := cache[name]; ok {
			return value, nil
		}
		expression, ok := metrics[name]
		if !ok {
			return "", fmt.Errorf("unknown metric %q", name)
		}
		value, err := expression.SQL(func(ref string) (string, error) {
			if _, ok := aggregates[ref]; ok {
				return aggregateSQL(ref)
			}
			return metricSQL(ref)
		})
		if err == nil {
			cache[name] = value
		}
		return value, err
	}
	out := make([]string, len(names))
	for i, name := range names {
		var err error
		if _, ok := aggregates[name]; ok {
			out[i], err = aggregateSQL(name)
		} else {
			out[i], err = metricSQL(name)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func indexString(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

// Decode restores each branch's authored aliases and drops the wide physical
// bundle columns used to preserve types across unlike branch shapes.
func (b BundlePlan) Decode(rows Rows) (map[string]Rows, error) {
	byOrdinal := map[int]BundleBranch{}
	result := map[string]Rows{}
	for _, branch := range b.Branches {
		byOrdinal[branch.Ordinal] = branch
		result[branch.ID] = Rows{}
	}
	for _, row := range rows {
		ordinal, err := integerValue(row[BundleBranchColumn])
		if err != nil {
			return nil, err
		}
		branch, ok := byOrdinal[ordinal]
		if !ok {
			return nil, fmt.Errorf("unknown bundle branch ordinal %d", ordinal)
		}
		decoded := Row{}
		for _, column := range branch.Columns {
			decoded[column.Output] = row[column.Physical]
		}
		result[branch.ID] = append(result[branch.ID], decoded)
	}
	return result, nil
}

func integerValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil
	default:
		return 0, fmt.Errorf("bundle branch ordinal has type %T", value)
	}
}
