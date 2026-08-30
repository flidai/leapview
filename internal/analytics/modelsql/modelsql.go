// Package modelsql owns LeapView's admission policy for authored DuckDB model
// queries. Generic parsing, AST traversal, rewriting, and plan decoding remain
// in pkg/duckdbsql; this package converts those results into application
// dependency evidence and rejects capabilities that model SQL does not admit.
package modelsql

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/pkg/duckdbsql"
)

// RelationRef is the normalized source relation evidence needed when the
// runtime lowers a governed source to its physical read query.
type RelationRef struct {
	Name  string
	Alias string
	Span  duckdbsql.Span
}

// Analysis is the application-owned result of admitting one model query. It
// intentionally contains neither DuckDB's serialized AST nor generic AST
// nodes.
type Analysis struct {
	SourceRefs      []string
	ModelRefs       []string
	SourceRelations []RelationRef
	ModelRelations  []RelationRef
	ModelColumns    []duckdbsql.ColumnRef
}

// Analyze parses sqlText with the pinned DuckDB-backed analyzer, applies the
// reviewed model-query policy, and returns normalized dependency evidence.
func Analyze(ctx context.Context, sqlText string) (Analysis, error) {
	query, err := duckdbsql.Parse(ctx, sqlText)
	if err != nil {
		return Analysis{}, err
	}
	if len(query.Statements) != 1 {
		return Analysis{}, fmt.Errorf("model SQL must contain exactly one SELECT statement")
	}
	if err := validateQuery(query); err != nil {
		return Analysis{}, err
	}

	generic, err := duckdbsql.Analyze(query)
	if err != nil {
		return Analysis{}, fmt.Errorf("analyze model SQL: %w", err)
	}
	sources := map[string]struct{}{}
	models := map[string]struct{}{}
	result := Analysis{}
	for _, cte := range generic.CTEs {
		if !validIdentifier(cte.Name) {
			return Analysis{}, fmt.Errorf("CTE name %q is invalid", cte.Name)
		}
		if cte.Recursive {
			return Analysis{}, fmt.Errorf("recursive CTE %q is not allowed in model SQL", cte.Name)
		}
	}
	for _, relation := range generic.Relations {
		switch relation.Kind {
		case duckdbsql.RelationCTE:
			if relation.Catalog != "" || relation.Schema != "" {
				return Analysis{}, fmt.Errorf("qualified CTE reference %q is not allowed in model SQL", relation.Name)
			}
			if relation.CTEForward {
				return Analysis{}, fmt.Errorf("CTE %q is referenced before its declaration", relation.Name)
			}
			if relation.CTERecursive {
				return Analysis{}, fmt.Errorf("recursive CTE %q is not allowed in model SQL", relation.Name)
			}
		case duckdbsql.RelationBase:
			if relation.CTEForward {
				return Analysis{}, fmt.Errorf("CTE %q is referenced before its declaration", relation.Name)
			}
			if relation.Catalog != "" {
				return Analysis{}, fmt.Errorf("external catalog %q is not allowed in model SQL", relation.Catalog)
			}
			if !validRelationName(relation.Name) {
				return Analysis{}, fmt.Errorf("relation %q is not a governed identifier", relation.Name)
			}
			if relation.Alias != "" && !validIdentifier(relation.Alias) {
				return Analysis{}, fmt.Errorf("relation alias %q is invalid", relation.Alias)
			}
			switch strings.ToLower(relation.Schema) {
			case "source":
				sources[relation.Name] = struct{}{}
				result.SourceRelations = append(result.SourceRelations, RelationRef{Name: relation.Name, Alias: relation.Alias, Span: relation.Span})
			case "model":
				models[relation.Name] = struct{}{}
				result.ModelRelations = append(result.ModelRelations, RelationRef{Name: relation.Name, Alias: relation.Alias, Span: relation.Span})
			case "":
				return Analysis{}, fmt.Errorf("unqualified relation %q is not governed", relation.Name)
			default:
				return Analysis{}, fmt.Errorf("relation schema %q is not governed", relation.Schema)
			}
		case duckdbsql.RelationSubquery:
			// Subqueries are admitted and their children are evaluated separately.
		case duckdbsql.RelationTableFunction:
			return Analysis{}, fmt.Errorf("table functions are not allowed in model SQL")
		default:
			return Analysis{}, fmt.Errorf("relation kind %q is not allowed in model SQL", relation.Kind)
		}
	}
	for _, function := range generic.Functions {
		if function.Catalog != "" {
			return Analysis{}, fmt.Errorf("external function catalog is not allowed")
		}
		if function.Schema != "" && !strings.EqualFold(function.Schema, "main") {
			return Analysis{}, fmt.Errorf("qualified function %q is not allowed", function.Name)
		}
		if !approvedFunction(function.Name) {
			if function.Window {
				return Analysis{}, fmt.Errorf("window function %q is not allowed", function.Name)
			}
			return Analysis{}, fmt.Errorf("function %q is not allowed in model SQL", function.Name)
		}
	}
	for _, column := range generic.Columns {
		if len(column.Names) == 0 || len(column.Names) > 3 {
			return Analysis{}, fmt.Errorf("column reference must contain one to three identifiers")
		}
		for _, name := range column.Names {
			if !validIdentifier(name) {
				return Analysis{}, fmt.Errorf("column reference identifier %q is invalid", name)
			}
		}
		if len(column.Names) == 3 {
			namespace := strings.ToLower(column.Names[0])
			if namespace != "source" && namespace != "model" {
				return Analysis{}, fmt.Errorf("column reference namespace %q is not governed", column.Names[0])
			}
			if namespace == "source" {
				return Analysis{}, fmt.Errorf("column reference %q must use a table alias; source.<name> is only valid in FROM/JOIN relations", strings.Join(column.Names, "."))
			}
			result.ModelColumns = append(result.ModelColumns, column)
		}
	}
	result.SourceRefs = sortedSet(sources)
	result.ModelRefs = sortedSet(models)
	return result, nil
}

// RewriteSources replaces admitted source relations with runtime-owned read
// queries. All spans came from the pinned parser and are validated atomically
// by duckdbsql.Rewrite.
func RewriteSources(sqlText string, analysis Analysis, replacements map[string]string, aliasUnaliased bool) (string, error) {
	return rewriteRelations(sqlText, analysis.SourceRelations, nil, nil, replacements, aliasUnaliased, "")
}

// RewriteSourcesAndModels atomically rewrites governed source relations and
// authored model.<name> relations in one pass. Combining edits is important:
// source inlining can change byte offsets, so two sequential rewrites would
// otherwise apply parser spans to the wrong text. Model references are only
// rewritten when relationNamespace differs from the legacy model schema.
func RewriteSourcesAndModels(sqlText string, analysis Analysis, replacements map[string]string, relationNamespace string, aliasUnaliased bool) (string, error) {
	if err := validateRelationNamespace(relationNamespace); err != nil {
		return "", err
	}
	return rewriteRelations(sqlText, analysis.SourceRelations, analysis.ModelRelations, analysis.ModelColumns, replacements, aliasUnaliased, relationNamespace)
}

// RewriteModels rewrites only governed model relation and column references.
// It is used by the planner's isolated analysis database before source
// projections are known, and is intentionally parser-span based.
func RewriteModels(sqlText string, analysis Analysis, relationNamespace string) (string, error) {
	if err := validateRelationNamespace(relationNamespace); err != nil {
		return "", err
	}
	return rewriteRelations(sqlText, nil, analysis.ModelRelations, analysis.ModelColumns, nil, false, relationNamespace)
}

func validateRelationNamespace(value string) error {
	if !validIdentifier(value) {
		return fmt.Errorf("relation namespace %q is invalid", value)
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("relation namespace %q must be lowercase canonical", value)
	}
	if len(value) > 63 {
		return fmt.Errorf("relation namespace %q exceeds 63 bytes", value)
	}
	return nil
}

func rewriteRelations(sqlText string, sourceRelations, modelRelations []RelationRef, modelColumns []duckdbsql.ColumnRef, replacements map[string]string, aliasUnaliased bool, relationNamespace string) (string, error) {
	edits := make([]duckdbsql.Edit, 0, len(sourceRelations)+len(modelRelations)+len(modelColumns))
	for _, relation := range sourceRelations {
		replacement, ok := replacements[relation.Name]
		if !ok {
			return "", fmt.Errorf("no replacement for source %q", relation.Name)
		}
		if aliasUnaliased && relation.Alias == "" {
			replacement += " AS " + quoteIdentifier(relation.Name)
		}
		edits = append(edits, duckdbsql.Edit{Span: relation.Span, Replacement: replacement})
	}
	if relationNamespace != "" && relationNamespace != "model" {
		for _, relation := range modelRelations {
			replacement := quoteIdentifier(relationNamespace) + "." + quoteIdentifier(relation.Name)
			edits = append(edits, duckdbsql.Edit{Span: relation.Span, Replacement: replacement})
		}
		columnEdits, err := modelColumnEdits(sqlText, relationNamespace, modelColumns)
		if err != nil {
			return "", err
		}
		edits = append(edits, columnEdits...)
	}
	return duckdbsql.Rewrite(sqlText, edits)
}

type sqlIdentifierToken struct {
	start, end int
	value      string
}

// modelColumnEdits handles the one parser limitation relevant to candidate
// writes: DuckDB's generic analyzer currently reports zero-width spans for
// three-part column references. This small lexer skips quoted literals and
// comments, then recognizes only model.<table>.<column> token sequences; it
// cannot rewrite arbitrary SQL text or alter strings/comments.
func modelColumnEdits(sqlText, relationNamespace string, expected []duckdbsql.ColumnRef) ([]duckdbsql.Edit, error) {
	tokens := scanSQLIdentifiers(sqlText)
	edits := []duckdbsql.Edit{}
	want := map[string]int{}
	for _, column := range expected {
		if len(column.Names) != 3 || !strings.EqualFold(column.Names[0], "model") {
			continue
		}
		want[modelColumnKey(column.Names[1], column.Names[2])]++
	}
	found := map[string]int{}
	for index := 0; index+2 < len(tokens); index++ {
		modelToken := tokens[index]
		if !strings.EqualFold(modelToken.value, "model") {
			continue
		}
		tableToken, columnToken := tokens[index+1], tokens[index+2]
		if !onlyDotBetween(sqlText, modelToken.end, tableToken.start) || !onlyDotBetween(sqlText, tableToken.end, columnToken.start) {
			continue
		}
		if !validIdentifier(tableToken.value) || !validIdentifier(columnToken.value) {
			continue
		}
		key := modelColumnKey(tableToken.value, columnToken.value)
		if want[key] == 0 {
			continue
		}
		found[key]++
		edits = append(edits, duckdbsql.Edit{
			Span:        duckdbsql.Span{Start: modelToken.start, End: columnToken.end},
			Replacement: quoteIdentifier(relationNamespace) + "." + quoteIdentifier(tableToken.value) + "." + quoteIdentifier(columnToken.value),
		})
	}
	for key, count := range want {
		if found[key] != count {
			return nil, fmt.Errorf("model column reference %q was not rewritten in relation namespace %q", key, relationNamespace)
		}
	}
	return edits, nil
}

func modelColumnKey(table, column string) string { return table + "\x00" + column }

func onlyDotBetween(sqlText string, start, end int) bool {
	value := strings.TrimSpace(sqlText[start:end])
	return value == "."
}

func scanSQLIdentifiers(sqlText string) []sqlIdentifierToken {
	tokens := []sqlIdentifierToken{}
	for index := 0; index < len(sqlText); {
		switch {
		case sqlText[index] == '\'':
			index = skipSQLQuoted(sqlText, index, sqlText[index])
		case sqlText[index] == '"':
			start := index
			index++
			for index < len(sqlText) {
				if sqlText[index] != '"' {
					index++
					continue
				}
				if index+1 < len(sqlText) && sqlText[index+1] == '"' {
					index += 2
					continue
				}
				index++
				value := strings.ReplaceAll(sqlText[start+1:index-1], `""`, `"`)
				tokens = append(tokens, sqlIdentifierToken{start: start, end: index, value: value})
				break
			}
		case sqlText[index] == '-' && index+1 < len(sqlText) && sqlText[index+1] == '-':
			index = skipSQLLineComment(sqlText, index+2)
		case sqlText[index] == '/' && index+1 < len(sqlText) && sqlText[index+1] == '*':
			index = skipSQLBlockComment(sqlText, index+2)
		case isSQLIdentifierStart(sqlText[index]):
			start := index
			index++
			for index < len(sqlText) && isSQLIdentifierPart(sqlText[index]) {
				index++
			}
			tokens = append(tokens, sqlIdentifierToken{start: start, end: index, value: sqlText[start:index]})
		default:
			index++
		}
	}
	return tokens
}

func skipSQLQuoted(sqlText string, index int, quote byte) int {
	index++
	for index < len(sqlText) {
		if sqlText[index] != quote {
			index++
			continue
		}
		if index+1 < len(sqlText) && sqlText[index+1] == quote {
			index += 2
			continue
		}
		return index + 1
	}
	return len(sqlText)
}

func skipSQLLineComment(sqlText string, index int) int {
	for index < len(sqlText) && sqlText[index] != '\n' {
		index++
	}
	return index
}

func skipSQLBlockComment(sqlText string, index int) int {
	for index+1 < len(sqlText) {
		if sqlText[index] == '*' && sqlText[index+1] == '/' {
			return index + 2
		}
		index++
	}
	return len(sqlText)
}

func isSQLIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isSQLIdentifierPart(value byte) bool {
	return isSQLIdentifierStart(value) || value >= '0' && value <= '9'
}

func validateQuery(query duckdbsql.Query) error {
	return duckdbsql.Walk(query, duckdbsql.WalkCallbacks{
		Statement: func(statement duckdbsql.Statement) error {
			if statement.Meta().HasSample {
				return fmt.Errorf("samples are not supported in model SQL")
			}
			if len(statement.Meta().NamedParameters) > 0 {
				return fmt.Errorf("parameters are not supported in model SQL")
			}
			switch value := statement.(type) {
			case *duckdbsql.SelectStatement:
				if value.HasSample {
					return fmt.Errorf("samples are not supported in model SQL")
				}
				if !approvedAggregateHandling(value.AggregateHandling) {
					return fmt.Errorf("aggregate handling %q is not supported in model SQL", value.AggregateHandling)
				}
				return validateModifiers(value.Modifiers)
			case *duckdbsql.SetOperationStatement:
				if value.SetOpType != "UNION" {
					return fmt.Errorf("set operation %q is not allowed in model SQL", value.SetOpType)
				}
				return validateModifiers(value.Modifiers)
			case *duckdbsql.RecursiveCTEStatement:
				return fmt.Errorf("recursive CTEs are not allowed in model SQL")
			case *duckdbsql.CTENodeStatement:
				return fmt.Errorf("CTE node statements are not allowed in model SQL")
			default:
				return fmt.Errorf("statement type %T is not allowed in model SQL", statement)
			}
		},
		Relation: func(relation duckdbsql.Relation) error {
			if relation.Meta().HasSample {
				return fmt.Errorf("samples are not supported in model SQL")
			}
			if alias := relation.Meta().Alias; alias != "" && !validIdentifier(alias) {
				return fmt.Errorf("relation alias %q is invalid", alias)
			}
			switch value := relation.(type) {
			case *duckdbsql.BaseTableRelation:
				if value.Catalog != "" {
					return fmt.Errorf("external catalog %q is not allowed in model SQL", value.Catalog)
				}
				if len(value.QualifiedName) > 2 {
					return fmt.Errorf("external catalog qualification is not allowed in model SQL")
				}
				if value.At != nil {
					return fmt.Errorf("temporal relation clauses are not supported in model SQL")
				}
				if err := validateIdentifiers("relation column alias", value.ColumnAliases); err != nil {
					return err
				}
				return nil
			case *duckdbsql.SubqueryRelation:
				return validateIdentifiers("subquery column alias", value.ColumnAliases)
			case *duckdbsql.EmptyRelation, *duckdbsql.ExpressionListRelation:
				return nil
			case *duckdbsql.JoinRelation:
				if !approvedJoinType(value.JoinType) {
					return fmt.Errorf("join type %q is not allowed in model SQL", value.JoinType)
				}
				if !approvedJoinRefType(value.RefType) {
					return fmt.Errorf("join reference type %q is not allowed in model SQL", value.RefType)
				}
				if err := validateUniqueIdentifiers("JOIN USING column", value.UsingColumns); err != nil {
					return err
				}
				return nil
			case *duckdbsql.TableFunctionRelation:
				return fmt.Errorf("table functions are not allowed in model SQL")
			case *duckdbsql.PivotRelation:
				return fmt.Errorf("PIVOT relations are not supported in model SQL")
			case *duckdbsql.ShowRelation:
				return fmt.Errorf("SHOW relations are not supported in model SQL")
			case *duckdbsql.ColumnDataRelation:
				return fmt.Errorf("column-data relations are not supported in model SQL")
			default:
				return fmt.Errorf("relation type %T is not allowed in model SQL", relation)
			}
		},
		Expression: func(expression duckdbsql.Expression) error {
			meta := expression.Meta()
			switch expression.(type) {
			case *duckdbsql.ConstantExpression:
				return requireExpressionType(meta, "VALUE_CONSTANT")
			case *duckdbsql.ColumnExpression:
				return requireExpressionType(meta, "COLUMN_REF")
			case *duckdbsql.StarExpression:
				star := expression.(*duckdbsql.StarExpression)
				if err := requireExpressionType(meta, "STAR"); err != nil {
					return err
				}
				if star.RelationName != "" || len(star.ExcludeList) > 0 || len(star.ReplaceList) > 0 || star.Columns || star.Unpacked || star.Expression != nil || len(star.QualifiedExcludeList) > 0 || len(star.RenameList) > 0 {
					return fmt.Errorf("extended star expressions are not supported in model SQL")
				}
				return nil
			case *duckdbsql.FunctionExpression:
				return requireExpressionType(meta, "FUNCTION")
			case *duckdbsql.OperatorExpression:
				if !approvedOperator(meta.Type) {
					return fmt.Errorf("operator %q is not allowed in model SQL", meta.Type)
				}
				return nil
			case *duckdbsql.ComparisonExpression:
				if !approvedComparison(meta.Type) {
					return fmt.Errorf("comparison %q is not allowed in model SQL", meta.Type)
				}
				return nil
			case *duckdbsql.ConjunctionExpression:
				if meta.Type != "CONJUNCTION_AND" && meta.Type != "CONJUNCTION_OR" {
					return fmt.Errorf("conjunction %q is not allowed in model SQL", meta.Type)
				}
				return nil
			case *duckdbsql.CastExpression:
				return requireExpressionType(meta, "OPERATOR_CAST")
			case *duckdbsql.CaseExpression:
				return requireExpressionType(meta, "CASE_EXPR")
			case *duckdbsql.WindowExpression:
				if !approvedWindowType(meta.Type) {
					return fmt.Errorf("window expression %q is not allowed in model SQL", meta.Type)
				}
				return nil
			case *duckdbsql.SubqueryExpression:
				return requireExpressionType(meta, "SUBQUERY")
			case *duckdbsql.BetweenExpression:
				if meta.Type != "COMPARE_BETWEEN" && meta.Type != "COMPARE_NOT_BETWEEN" {
					return fmt.Errorf("between expression %q is not allowed in model SQL", meta.Type)
				}
				return nil
			case *duckdbsql.CollateExpression:
				return fmt.Errorf("COLLATE expressions are not supported in model SQL")
			case *duckdbsql.DefaultExpression, *duckdbsql.LambdaExpression, *duckdbsql.LambdaRefExpression,
				*duckdbsql.ParameterExpression, *duckdbsql.PositionalReferenceExpression, *duckdbsql.TypeExpression:
				return fmt.Errorf("expression type %T is not supported in model SQL", expression)
			default:
				return fmt.Errorf("expression type %T is not allowed in model SQL", expression)
			}
		},
		CTE: func(cte duckdbsql.CTE) error {
			if !validIdentifier(cte.Name) {
				return fmt.Errorf("CTE name %q is invalid", cte.Name)
			}
			switch cte.Materialized {
			case "", "CTE_MATERIALIZE_DEFAULT", "CTE_MATERIALIZE_ALWAYS", "CTE_MATERIALIZE_NEVER":
			default:
				return fmt.Errorf("CTE %q materialized has an unknown mode", cte.Name)
			}
			if len(cte.Aliases) > 0 {
				return fmt.Errorf("CTE column aliases are not supported in model SQL")
			}
			if len(cte.KeyTargets) > 0 {
				return fmt.Errorf("CTE key targets are not supported in model SQL")
			}
			return nil
		},
	})
}

func validateModifiers(modifiers []duckdbsql.Modifier) error {
	for _, modifier := range modifiers {
		switch value := modifier.(type) {
		case *duckdbsql.DistinctModifier:
			if len(value.DistinctOnTargets) > 0 {
				return fmt.Errorf("DISTINCT ON is not supported in model SQL")
			}
		case *duckdbsql.OrderModifier, *duckdbsql.LimitModifier:
			// Explicitly admitted query features.
		case *duckdbsql.LimitPercentModifier:
			return fmt.Errorf("LIMIT PERCENT is not supported in model SQL")
		default:
			return fmt.Errorf("query modifier %T is not allowed in model SQL", modifier)
		}
	}
	return nil
}

func approvedAggregateHandling(value string) bool {
	switch value {
	case "", "STANDARD_HANDLING", "NO_AGGREGATES_ALLOWED", "FORCE_AGGREGATES":
		return true
	default:
		return false
	}
}

func approvedJoinType(value string) bool {
	switch value {
	case "INNER", "LEFT", "RIGHT", "FULL", "SEMI", "ANTI", "MARK", "ASOF", "POSITIONAL", "LEFT_DELIM", "RIGHT_DELIM", "FULL_DELIM":
		return true
	default:
		return false
	}
}

func approvedJoinRefType(value string) bool {
	switch value {
	case "", "REGULAR", "NATURAL", "POSITIONAL", "ASOF":
		return true
	default:
		return false
	}
}

func requireExpressionType(meta duckdbsql.NodeMeta, allowed string) error {
	if meta.Type != allowed {
		return fmt.Errorf("expression type %q is not allowed in model SQL", meta.Type)
	}
	return nil
}

func approvedOperator(value string) bool {
	switch value {
	case "OPERATOR_COALESCE", "OPERATOR_IS_NULL", "OPERATOR_IS_NOT_NULL", "OPERATOR_NOT", "CONJUNCTION_AND", "CONJUNCTION_OR":
		return true
	default:
		return false
	}
}

func approvedComparison(value string) bool {
	switch value {
	case "COMPARE_EQUAL", "COMPARE_NOTEQUAL", "COMPARE_LESSTHAN", "COMPARE_GREATERTHAN", "COMPARE_LESSTHANOREQUALTO", "COMPARE_GREATERTHANOREQUALTO", "COMPARE_IN", "COMPARE_NOT_IN", "COMPARE_DISTINCT_FROM", "COMPARE_BETWEEN", "COMPARE_NOT_BETWEEN", "COMPARE_NOT_DISTINCT_FROM":
		return true
	default:
		return false
	}
}

func approvedWindowType(value string) bool {
	switch value {
	case "WINDOW_AGGREGATE", "WINDOW_RANK", "WINDOW_RANK_DENSE", "WINDOW_NTILE", "WINDOW_PERCENT_RANK", "WINDOW_CUME_DIST", "WINDOW_ROW_NUMBER", "WINDOW_FIRST_VALUE", "WINDOW_LAST_VALUE", "WINDOW_LEAD", "WINDOW_LAG", "WINDOW_NTH_VALUE":
		return true
	default:
		return false
	}
}

func approvedFunction(value string) bool {
	switch strings.ToLower(value) {
	case "+", "-", "*", "/", "%", "||", "and", "or", "not", "coalesce", "nullif", "if", "greatest", "least",
		"abs", "ceil", "ceiling", "floor", "round", "trunc", "sign", "sqrt", "pow", "power", "exp", "ln", "log", "log10",
		"lower", "upper", "trim", "ltrim", "rtrim", "replace", "regexp_replace", "regexp_matches", "regexp_extract", "length", "len",
		"left", "right", "substr", "substring", "concat", "concat_ws", "split_part", "printf", "lpad", "list_contains", "str_split",
		"date_trunc", "date_part", "date_diff", "datediff", "strftime", "to_timestamp", "make_date", "make_timestamp", "year", "month", "day", "hour", "minute", "second",
		"hash", "md5", "sha256", "try_cast", "count", "count_star", "sum", "avg", "min", "max", "median", "mode", "quantile", "quantile_cont", "quantile_disc",
		"stddev", "stddev_pop", "stddev_samp", "variance", "var_pop", "var_samp", "bool_and", "bool_or", "string_agg", "array_agg", "list", "list_value",
		"row_number", "rank", "dense_rank", "percent_rank", "cume_dist", "ntile", "lag", "lead", "first_value", "last_value":
		return true
	default:
		return false
	}
}

func validIdentifier(value string) bool {
	if value == "" || !(value[0] == '_' || value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func validateIdentifiers(kind string, values []string) error {
	for _, value := range values {
		if !validIdentifier(value) {
			return fmt.Errorf("%s %q is invalid", kind, value)
		}
	}
	return nil
}

func validateUniqueIdentifiers(kind string, values []string) error {
	if err := validateIdentifiers(kind, values); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s %q is duplicated", kind, value)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validRelationName(value string) bool {
	if value == "" || !(value[0] == '_' || value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !(char == '_' || char == '.' || char == '-' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func quoteIdentifier(value string) string {
	if validIdentifier(value) {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
