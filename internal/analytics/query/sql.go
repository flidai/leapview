package query

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func quoteIdent(value string) (string, error) {
	if !identifierPattern.MatchString(value) {
		return "", fmt.Errorf("invalid identifier %q", value)
	}
	return value, nil
}

func applyAliases(expr string, aliases map[string]tableAlias, fallbackAlias string) string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return expr
	}
	if identifierPattern.MatchString(expr) {
		return fallbackAlias + "." + expr
	}
	for table, alias := range aliases {
		expr = regexp.MustCompile(`\b`+regexp.QuoteMeta(table)+`\.`).ReplaceAllString(expr, alias.Alias+".")
	}
	expr = strings.ReplaceAll(expr, "{alias}", fallbackAlias)
	return expr
}

func joinSQL(planner *Planner, base string, aliases map[string]tableAlias) (string, error) {
	baseRelation, err := planner.physicalTable(base)
	if err != nil {
		return "", err
	}
	model := planner.Model
	parts := []string{baseRelation + " t0"}
	joinAliases := make([]tableAlias, 0, len(aliases)-1)
	for table, alias := range aliases {
		if table != base {
			joinAliases = append(joinAliases, alias)
		}
	}
	sort.Slice(joinAliases, func(i, j int) bool {
		if len(joinAliases[i].Path) != len(joinAliases[j].Path) {
			return len(joinAliases[i].Path) < len(joinAliases[j].Path)
		}
		return joinAliases[i].Alias < joinAliases[j].Alias
	})
	for _, alias := range joinAliases {
		if len(alias.Path) == 0 {
			continue
		}
		relationship := alias.Path[len(alias.Path)-1]
		fromTable, fromFields, err := semanticmodel.RelationshipEndpoint(relationship, true)
		if err != nil {
			return "", err
		}
		toTable, toFields, err := semanticmodel.RelationshipEndpoint(relationship, false)
		if err != nil {
			return "", err
		}
		rightRelation, err := planner.physicalTable(alias.Table)
		if err != nil {
			return "", err
		}
		leftTable, leftFields := fromTable, fromFields
		rightTable, rightFields := toTable, toFields
		if alias.Table == fromTable && relationship.Cardinality == "one_to_one" {
			leftTable, leftFields = toTable, toFields
			rightTable, rightFields = fromTable, fromFields
		}
		left, ok := aliases[leftTable]
		if !ok {
			return "", fmt.Errorf("missing relationship alias for %q", leftTable)
		}
		right, ok := aliases[rightTable]
		if !ok {
			return "", fmt.Errorf("missing relationship alias for %q", rightTable)
		}
		if right.Table != alias.Table {
			return "", fmt.Errorf("relationship path to %q ends at %q", alias.Table, right.Table)
		}
		condition, err := tupleJoinCondition(model, leftTable, leftFields, rightTable, rightFields, aliases, left.Alias, alias.Alias)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("LEFT JOIN %s %s ON %s", rightRelation, alias.Alias, condition))
	}
	return strings.Join(parts, "\n"), nil
}

func joinPathSQL(planner *Planner, aliases pathAliasSet) (string, error) {
	baseRelation, err := planner.physicalTable(aliases.BaseTable)
	if err != nil {
		return "", err
	}
	baseAlias, ok := aliases.ByPath[""]
	if !ok {
		return "", fmt.Errorf("missing base alias for fact %q", aliases.BaseTable)
	}
	model := planner.Model
	parts := []string{baseRelation + " " + baseAlias.Alias}
	for _, alias := range aliases.Ordered {
		if len(alias.Path) == 0 {
			return "", fmt.Errorf("join alias %q has no relationship path", alias.Alias)
		}
		parentPath := alias.Path[:len(alias.Path)-1]
		parent, ok := aliases.ByPath[relationshipPathSignature(parentPath)]
		if !ok {
			return "", fmt.Errorf("missing parent alias for relationship path %q", relationshipPathSignature(alias.Path))
		}
		relationship := alias.Path[len(alias.Path)-1]
		fromTable, fromFields, err := semanticmodel.RelationshipEndpoint(relationship, true)
		if err != nil {
			return "", err
		}
		toTable, toFields, err := semanticmodel.RelationshipEndpoint(relationship, false)
		if err != nil {
			return "", err
		}
		leftTable, leftFields := fromTable, fromFields
		rightTable, rightFields := toTable, toFields
		switch {
		case parent.Table == fromTable && alias.Table == toTable:
		case relationship.Cardinality == "one_to_one" && parent.Table == toTable && alias.Table == fromTable:
			leftTable, leftFields = toTable, toFields
			rightTable, rightFields = fromTable, fromFields
		default:
			return "", fmt.Errorf("relationship path %q cannot join %q to %q", relationshipPathSignature(alias.Path), parent.Table, alias.Table)
		}
		rightRelation, err := planner.physicalTable(alias.Table)
		if err != nil {
			return "", err
		}
		leftAliases, err := aliases.context(parentPath)
		if err != nil {
			return "", err
		}
		rightAliases, err := aliases.context(alias.Path)
		if err != nil {
			return "", err
		}
		// The right aliases are needed for a joined tuple's fields; keep each
		// path context separate so the same table can be role-played safely.
		condition, err := tupleJoinConditionWithAliases(model, leftTable, leftFields, rightTable, rightFields, leftAliases, rightAliases, parent.Alias, alias.Alias)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("LEFT JOIN %s %s ON %s", rightRelation, alias.Alias, condition))
	}
	return strings.Join(parts, "\n"), nil
}

func tupleJoinCondition(model *semanticmodel.Model, leftTable string, leftFields []string, rightTable string, rightFields []string, aliases map[string]tableAlias, leftAlias, rightAlias string) (string, error) {
	return tupleJoinConditionWithAliases(model, leftTable, leftFields, rightTable, rightFields, aliases, aliases, leftAlias, rightAlias)
}

func tupleJoinConditionWithAliases(model *semanticmodel.Model, leftTable string, leftFields []string, rightTable string, rightFields []string, leftAliases, rightAliases map[string]tableAlias, leftAlias, rightAlias string) (string, error) {
	if len(leftFields) == 0 || len(leftFields) != len(rightFields) {
		return "", fmt.Errorf("relationship tuple arity mismatch joining %s (%d) to %s (%d)", leftTable, len(leftFields), rightTable, len(rightFields))
	}
	parts := make([]string, 0, len(leftFields))
	for index := range leftFields {
		left, err := model.ResolveDimension(leftTable + "." + leftFields[index])
		if err != nil {
			return "", err
		}
		right, err := model.ResolveDimension(rightTable + "." + rightFields[index])
		if err != nil {
			return "", err
		}
		if left.Datatype != "" && right.Datatype != "" && left.Datatype != right.Datatype {
			return "", fmt.Errorf("relationship tuple field %q datatype %q is incompatible with %q datatype %q", left.Field, left.Datatype, right.Field, right.Datatype)
		}
		leftExpr := applyAliases(left.SQLExpression(), leftAliases, leftAlias)
		rightExpr := applyAliases(right.SQLExpression(), rightAliases, rightAlias)
		parts = append(parts, leftExpr+" = "+rightExpr)
	}
	return strings.Join(parts, " AND "), nil
}

func dimensionExpr(dimension semanticmodel.MetricDimension, aliases map[string]tableAlias) string {
	alias := aliases[dimension.Table].Alias
	return applyAliases(dimension.SQLExpression(), aliases, alias)
}

func dimensionExprForPath(dimension semanticmodel.MetricDimension, aliases pathAliasSet, path []semanticmodel.Relationship) (string, error) {
	context, err := aliases.context(path)
	if err != nil {
		return "", err
	}
	alias, ok := context[dimension.Table]
	if !ok {
		return "", fmt.Errorf("relationship path %q does not expose table %q", relationshipPathSignature(path), dimension.Table)
	}
	return applyAliases(dimension.SQLExpression(), context, alias.Alias), nil
}

func dimensionWhereExpr(dimension semanticmodel.MetricDimension, aliases map[string]tableAlias) string {
	return ""
}

func aggregateMetricExpr(model *semanticmodel.Model, metric resolvedAggregateMetric, aliases map[string]tableAlias) (string, error) {
	input, err := rawAggregateMetricExpr(model, metric, aliases)
	if err != nil && metric.Aggregation != "count" {
		return "", err
	}
	expr := ""
	switch metric.Aggregation {
	case "count":
		expr = "COUNT(*)"
	case "count_distinct":
		expr = "COUNT(DISTINCT " + input + ")"
	case "sum", "avg", "min", "max":
		expr = strings.ToUpper(metric.Aggregation) + "(" + input + ")"
	default:
		return "", fmt.Errorf("metric %q has unsupported aggregation %q", metric.Name, metric.Aggregation)
	}
	return expr, nil
}

func rawAggregateMetricExpr(model *semanticmodel.Model, metric resolvedAggregateMetric, aliases map[string]tableAlias) (string, error) {
	if metric.InputField != "" {
		dimension, err := model.ResolveDimension(metric.InputField)
		if err != nil {
			return "", err
		}
		return dimensionExpr(dimension, aliases), nil
	}
	if metric.InputExpr == "" {
		return "", fmt.Errorf("metric %q has no raw input", metric.Name)
	}
	var expression semanticmodel.Expression
	if metric.InputExpression != nil {
		expression = *metric.InputExpression
	} else {
		var err error
		expression, err = semanticmodel.ParseExpression(metric.InputExpr)
		if err != nil {
			return "", err
		}
	}
	return expression.SQL(func(ref string) (string, error) {
		dimension, err := model.ResolveDimension(ref)
		if err != nil {
			return "", err
		}
		return dimensionExpr(dimension, aliases), nil
	})
}
