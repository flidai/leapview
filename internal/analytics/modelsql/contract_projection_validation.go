package modelsql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/duckdbsql"
)

// ValidateCanonicalProjection validates the closed SQL AST representation
// emitted by CanonicalProjection. The input is deliberately validated as
// bytes, rather than normalized, because a projection is an identity surface:
// only the exact encoding produced by the DTO producer is admissible.
func ValidateCanonicalProjection(value string) error {
	if value == "" {
		return fmt.Errorf("SQL projection is empty")
	}

	decoder := json.NewDecoder(bytes.NewReader([]byte(value)))
	decoder.DisallowUnknownFields()
	var ast sqlAST
	if err := decoder.Decode(&ast); err != nil {
		return fmt.Errorf("decode canonical SQL projection: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode canonical SQL projection: trailing JSON value")
		}
		return fmt.Errorf("decode canonical SQL projection: %w", err)
	}

	// json.Marshal is the producer's encoding. This catches whitespace, key
	// reordering, duplicate keys, explicit nulls, and non-canonical omission of
	// empty values in addition to protecting the deterministic field order.
	encoded, err := json.Marshal(ast)
	if err != nil {
		return fmt.Errorf("encode canonical SQL projection: %w", err)
	}
	if !bytes.Equal(encoded, []byte(value)) {
		return fmt.Errorf("canonical SQL projection bytes differ from producer encoding")
	}
	if ast.Statements == nil {
		return fmt.Errorf("canonical SQL projection: statements are required")
	}
	if len(ast.Statements) == 0 {
		return fmt.Errorf("canonical SQL projection: statements must not be empty")
	}
	if len(ast.Statements) != 1 {
		return fmt.Errorf("canonical SQL projection: exactly one statement is required")
	}

	for index := range ast.Statements {
		if err := validateSQLStatementNode(ast.Statements[index], fmt.Sprintf("statements[%d]", index), nil); err != nil {
			return err
		}
	}
	return nil
}

type sqlNodeRole uint8

const (
	sqlStatementRole sqlNodeRole = iota + 1
	sqlRelationRole
	sqlExpressionRole
)

var sqlNodeCommonFields = map[string]struct{}{
	"kind": {}, "class": {}, "type": {}, "alias": {},
}

var sqlNodeFieldsByKind = map[string]map[string]struct{}{
	"select":              mergeSQLNodeFields("aggregateHandling", "modifiers", "ctes", "select", "from", "where", "groups", "groupSets", "having", "qualify"),
	"set_operation":       mergeSQLNodeFields("setOperation", "setOperationAll", "modifiers", "ctes", "left", "right", "children"),
	"base_table":          mergeSQLNodeFields("name", "schema", "catalog", "columnAliases", "resourceID", "resourceKind"),
	"empty":               mergeSQLNodeFields(),
	"subquery_relation":   mergeSQLNodeFields("query", "columnAliases"),
	"join":                mergeSQLNodeFields("joinType", "joinReferenceType", "usingColumns", "delimFlipped", "implicit", "left", "right", "condition", "duplicateEliminatedColumns", "rankingExpression", "nearestCount", "nearestOrderType", "nearestApprox"),
	"expression_list":     mergeSQLNodeFields("names", "types", "rows"),
	"constant":            mergeSQLNodeFields("value", "logicalType"),
	"column":              mergeSQLNodeFields("names", "resourceID", "resourceKind"),
	"star":                mergeSQLNodeFields(),
	"function":            mergeSQLNodeFields("functionName", "schema", "catalog", "children", "filter", "orders", "distinct", "isOperator", "exportState"),
	"operator":            mergeSQLNodeFields("children"),
	"comparison":          mergeSQLNodeFields("left", "right"),
	"conjunction":         mergeSQLNodeFields("children"),
	"cast":                mergeSQLNodeFields("child", "logicalType", "tryCast"),
	"case":                mergeSQLNodeFields("checks", "else"),
	"window":              mergeSQLNodeFields("functionName", "schema", "catalog", "partitions", "orders", "windowStart", "windowEnd", "startExpression", "endExpression", "children", "filter", "offsetExpression", "defaultExpression", "ignoreNulls", "excludeClause", "distinct", "argumentOrders"),
	"subquery_expression": mergeSQLNodeFields("subqueryType", "comparisonType", "query", "child"),
	"between":             mergeSQLNodeFields("child", "lower", "upper"),
}

var sqlLogicalTypeNames = func() map[string]struct{} {
	result := make(map[string]struct{}, 64)
	for _, name := range []string{
		"INVALID", "SQLNULL", "UNKNOWN", "ANY", "UNBOUND", "TEMPLATE", "TYPE", "BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "BIGINT", "DATE", "TIME", "TIMESTAMP_SEC", "TIMESTAMP_MS", "TIMESTAMP", "TIMESTAMP_NS", "DECIMAL", "FLOAT", "DOUBLE", "CHAR", "VARCHAR", "BLOB", "INTERVAL", "UTINYINT", "USMALLINT", "UINTEGER", "UBIGINT", "TIMESTAMP_TZ", "TIME_TZ", "TIME_NS", "BIT", "STRING_LITERAL", "INTEGER_LITERAL", "BIGNUM", "UHUGEINT", "HUGEINT", "POINTER", "VALIDITY", "UUID", "GEOMETRY", "STRUCT", "LIST", "MAP", "TABLE", "ENUM", "AGGREGATE_STATE", "LAMBDA", "UNION", "ARRAY", "VARIANT",
	} {
		result[name] = struct{}{}
	}
	for _, typ := range duckdbsql.GeneratedInventorySnapshot().Types {
		if typ.TypeName != "" {
			result[typ.TypeName] = struct{}{}
		}
		if typ.LogicalType != "" {
			result[typ.LogicalType] = struct{}{}
		}
	}
	return result
}()

func mergeSQLNodeFields(fields ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(sqlNodeCommonFields)+len(fields))
	for field := range sqlNodeCommonFields {
		result[field] = struct{}{}
	}
	for _, field := range fields {
		result[field] = struct{}{}
	}
	return result
}

func validateSQLStatementNode(node sqlNode, path string, outerCTEs map[string]struct{}) error {
	if err := validateSQLNodeFields(node, sqlStatementRole, path); err != nil {
		return err
	}
	scope := cloneCTEScope(outerCTEs)
	if scope == nil {
		scope = make(map[string]struct{})
	}
	switch node.Kind {
	case "select":
		if node.Class != "" || node.Type != "SELECT_NODE" {
			return fmt.Errorf("%s: invalid select metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if !approvedAggregateHandling(node.AggregateHandling) {
			return fmt.Errorf("%s.aggregateHandling: unsupported mode %q", path, node.AggregateHandling)
		}
		if len(node.Select) == 0 {
			return fmt.Errorf("%s.select: required expression list is empty", path)
		}
		if node.From == nil {
			return fmt.Errorf("%s.from: required relation node is missing", path)
		}
		if err := validateSQLCTEs(node.CTEs, path+".ctes", scope); err != nil {
			return err
		}
		for index := range node.Select {
			if err := validateSQLExpressionNode(node.Select[index], fmt.Sprintf("%s.select[%d]", path, index), scope); err != nil {
				return err
			}
		}
		if err := validateOptionalSQLRelation(node.From, path+".from", scope); err != nil {
			return err
		}
		for _, field := range []struct {
			name  string
			value *sqlNode
		}{
			{"where", node.Where}, {"having", node.Having}, {"qualify", node.Qualify},
		} {
			if field.value != nil {
				if err := validateSQLExpressionNode(*field.value, path+"."+field.name, scope); err != nil {
					return err
				}
			}
		}
		for index := range node.Groups {
			if err := validateSQLExpressionNode(node.Groups[index], fmt.Sprintf("%s.groups[%d]", path, index), scope); err != nil {
				return err
			}
		}
		for setIndex, groupSet := range node.GroupSets {
			for memberIndex, groupIndex := range groupSet {
				if groupIndex < 0 || groupIndex >= len(node.Groups) {
					return fmt.Errorf("%s.groupSets[%d][%d]: index %d exceeds group expression count", path, setIndex, memberIndex, groupIndex)
				}
			}
		}
		for index := range node.Modifiers {
			if err := validateSQLModifier(node.Modifiers[index], fmt.Sprintf("%s.modifiers[%d]", path, index), scope); err != nil {
				return err
			}
		}
	case "set_operation":
		if node.Class != "" || node.Type != "SET_OPERATION_NODE" {
			return fmt.Errorf("%s: invalid set-operation metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if node.SetOperation != "UNION" {
			return fmt.Errorf("%s.setOperation: unsupported operation %q", path, node.SetOperation)
		}
		if node.Left == nil || node.Right == nil {
			return fmt.Errorf("%s: set operation requires left and right statements", path)
		}
		if err := validateSQLCTEs(node.CTEs, path+".ctes", scope); err != nil {
			return err
		}
		if err := validateSQLStatementNode(*node.Left, path+".left", scope); err != nil {
			return err
		}
		if err := validateSQLStatementNode(*node.Right, path+".right", scope); err != nil {
			return err
		}
		for index := range node.Modifiers {
			if err := validateSQLModifier(node.Modifiers[index], fmt.Sprintf("%s.modifiers[%d]", path, index), scope); err != nil {
				return err
			}
		}
		for index := range node.Children {
			if err := validateSQLStatementNode(node.Children[index], fmt.Sprintf("%s.children[%d]", path, index), scope); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: invalid statement node kind %q", path, node.Kind)
	}
	return nil
}

func validateSQLCTEs(values []sqlCTE, path string, scope map[string]struct{}) error {
	seen := make(map[string]struct{}, len(values))
	for index := range values {
		cte := values[index]
		ctePath := fmt.Sprintf("%s[%d]", path, index)
		if cte.Name == "" || !validIdentifier(cte.Name) {
			return fmt.Errorf("%s.name: invalid CTE name %q", ctePath, cte.Name)
		}
		key := strings.ToLower(cte.Name)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%s.name: duplicate CTE name %q", ctePath, cte.Name)
		}
		seen[key] = struct{}{}
		switch cte.Materialized {
		case "", "CTE_MATERIALIZE_DEFAULT", "CTE_MATERIALIZE_ALWAYS", "CTE_MATERIALIZE_NEVER":
		default:
			return fmt.Errorf("%s.materialized: invalid mode %q", ctePath, cte.Materialized)
		}
		if err := validateSQLStatementNode(cte.Query, ctePath+".query", scope); err != nil {
			return err
		}
		if scope == nil {
			scope = make(map[string]struct{})
		}
		scope[key] = struct{}{}
	}
	return nil
}

func validateSQLRelationNode(node sqlNode, path string, scope map[string]struct{}) error {
	if err := validateSQLNodeFields(node, sqlRelationRole, path); err != nil {
		return err
	}
	if node.Alias != "" && !validIdentifier(node.Alias) {
		return fmt.Errorf("%s.alias: invalid relation alias %q", path, node.Alias)
	}
	switch node.Kind {
	case "base_table":
		if node.Class != "" || node.Type != "BASE_TABLE" {
			return fmt.Errorf("%s: invalid base-table metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if err := validateSQLBaseTable(node, path, scope); err != nil {
			return err
		}
		return validateIdentifierSlice(node.ColumnAliases, path+".columnAliases")
	case "empty":
		if node.Class != "" || node.Type != "EMPTY" {
			return fmt.Errorf("%s: invalid empty-relation metadata class=%q type=%q", path, node.Class, node.Type)
		}
		return nil
	case "subquery_relation":
		if node.Class != "" || node.Type != "SUBQUERY" {
			return fmt.Errorf("%s: invalid subquery-relation metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if node.Query == nil {
			return fmt.Errorf("%s.query: required subquery statement is missing", path)
		}
		if err := validateSQLStatementNode(*node.Query, path+".query", scope); err != nil {
			return err
		}
		return validateIdentifierSlice(node.ColumnAliases, path+".columnAliases")
	case "join":
		if node.Class != "" || node.Type != "JOIN" {
			return fmt.Errorf("%s: invalid join metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if !approvedJoinType(node.JoinType) {
			return fmt.Errorf("%s.joinType: unsupported join type %q", path, node.JoinType)
		}
		if !approvedJoinRefType(node.JoinReferenceType) {
			return fmt.Errorf("%s.joinReferenceType: unsupported join reference type %q", path, node.JoinReferenceType)
		}
		if node.NearestOrderType != "" && !validSQLOrderType(node.NearestOrderType) {
			return fmt.Errorf("%s.nearestOrderType: invalid order type %q", path, node.NearestOrderType)
		}
		if node.Left == nil || node.Right == nil {
			return fmt.Errorf("%s: join requires left and right relations", path)
		}
		if err := validateSQLRelationNode(*node.Left, path+".left", scope); err != nil {
			return err
		}
		if err := validateSQLRelationNode(*node.Right, path+".right", scope); err != nil {
			return err
		}
		if err := validateUniqueSQLIdentifiers(node.UsingColumns, path+".usingColumns"); err != nil {
			return err
		}
		if node.Condition != nil {
			if err := validateSQLExpressionNode(*node.Condition, path+".condition", scope); err != nil {
				return err
			}
		}
		if node.RankingExpression != nil {
			if err := validateSQLExpressionNode(*node.RankingExpression, path+".rankingExpression", scope); err != nil {
				return err
			}
		}
		for index := range node.DuplicateColumns {
			if err := validateSQLExpressionNode(node.DuplicateColumns[index], fmt.Sprintf("%s.duplicateEliminatedColumns[%d]", path, index), scope); err != nil {
				return err
			}
		}
	case "expression_list":
		if node.Class != "" || node.Type != "EXPRESSION_LIST" {
			return fmt.Errorf("%s: invalid expression-list metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if err := validateIdentifierSlice(node.Names, path+".names"); err != nil {
			return err
		}
		for index := range node.Types {
			if err := validateSQLTypeRequiredID(node.Types[index], fmt.Sprintf("%s.types[%d]", path, index)); err != nil {
				return err
			}
		}
		if len(node.Rows) == 0 {
			return fmt.Errorf("%s.rows: required rows are empty", path)
		}
		for rowIndex := range node.Rows {
			if len(node.Rows[rowIndex]) == 0 {
				return fmt.Errorf("%s.rows[%d]: row is empty", path, rowIndex)
			}
			for columnIndex := range node.Rows[rowIndex] {
				if err := validateSQLExpressionNode(node.Rows[rowIndex][columnIndex], fmt.Sprintf("%s.rows[%d][%d]", path, rowIndex, columnIndex), scope); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("%s: invalid relation node kind %q", path, node.Kind)
	}
	return nil
}

func validateSQLBaseTable(node sqlNode, path string, scope map[string]struct{}) error {
	if node.ResourceID != "" || node.ResourceKind != "" {
		if node.ResourceID == "" || node.ResourceKind == "" {
			return fmt.Errorf("%s: resourceID and resourceKind must be supplied together", path)
		}
		if _, err := projectgraph.NewResourceID(node.ResourceID); err != nil {
			return fmt.Errorf("%s.resourceID: %w", path, err)
		}
		if node.ResourceKind != string(projectgraph.KindSource) && node.ResourceKind != string(projectgraph.KindModel) {
			return fmt.Errorf("%s.resourceKind: unsupported resource kind %q", path, node.ResourceKind)
		}
		if node.Catalog != "" || node.Schema != "" || node.Name != "" {
			return fmt.Errorf("%s: resource-backed relation cannot retain catalog, schema, or name", path)
		}
		return nil
	}
	if node.Catalog != "" {
		return fmt.Errorf("%s.catalog: external catalogs are not admitted", path)
	}
	if node.Name == "" || !validRelationName(node.Name) {
		return fmt.Errorf("%s.name: invalid relation name %q", path, node.Name)
	}
	switch {
	case strings.EqualFold(node.Schema, "source"), strings.EqualFold(node.Schema, "model"):
		return nil
	case node.Schema == "":
		if _, ok := scope[strings.ToLower(node.Name)]; !ok {
			return fmt.Errorf("%s: unqualified relation %q is not a declared CTE", path, node.Name)
		}
		return nil
	default:
		return fmt.Errorf("%s.schema: relation schema %q is not governed", path, node.Schema)
	}
}

func validateOptionalSQLRelation(node *sqlNode, path string, scope map[string]struct{}) error {
	if node == nil {
		return nil
	}
	return validateSQLRelationNode(*node, path, scope)
}

func validateSQLExpressionNode(node sqlNode, path string, scope map[string]struct{}) error {
	if err := validateSQLNodeFields(node, sqlExpressionRole, path); err != nil {
		return err
	}
	switch node.Kind {
	case "constant":
		if node.Class != "CONSTANT" || node.Type != "VALUE_CONSTANT" {
			return fmt.Errorf("%s: invalid constant metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if node.Value == nil || node.LogicalType == nil {
			return fmt.Errorf("%s: constant requires value and logicalType", path)
		}
		if err := validateSQLValue(*node.Value, path+".value"); err != nil {
			return err
		}
		return validateSQLType(*node.LogicalType, path+".logicalType")
	case "column":
		if node.Class != "COLUMN_REF" || node.Type != "COLUMN_REF" {
			return fmt.Errorf("%s: invalid column metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if len(node.Names) == 0 || len(node.Names) > 3 {
			return fmt.Errorf("%s.names: column reference must contain one to three identifiers", path)
		}
		if node.ResourceID != "" || node.ResourceKind != "" {
			if node.ResourceID == "" || node.ResourceKind != string(projectgraph.KindModel) && node.ResourceKind != string(projectgraph.KindSource) {
				return fmt.Errorf("%s: model resourceID/resourceKind are invalid", path)
			}
			if _, err := projectgraph.NewResourceID(node.ResourceID); err != nil {
				return fmt.Errorf("%s.resourceID: %w", path, err)
			}
			if len(node.Names) != 2 || node.Names[0] != node.ResourceID || !validIdentifier(node.Names[1]) {
				return fmt.Errorf("%s.names: resource-backed column must be [resourceID, column]", path)
			}
			return nil
		}
		if node.ResourceKind != "" {
			return fmt.Errorf("%s: resourceKind requires resourceID", path)
		}
		for index, name := range node.Names {
			valid := validIdentifier(name)
			if len(node.Names) == 3 && index == 1 {
				valid = validRelationName(name)
			}
			if !valid {
				return fmt.Errorf("%s.names[%d]: invalid identifier %q", path, index, name)
			}
		}
		if len(node.Names) == 3 && !strings.EqualFold(node.Names[0], "model") {
			return fmt.Errorf("%s.names: three-part column namespace must be model", path)
		}
	case "star":
		if node.Class != "STAR" || node.Type != "STAR" {
			return fmt.Errorf("%s: invalid star metadata class=%q type=%q", path, node.Class, node.Type)
		}
	case "function":
		if node.Class != "FUNCTION" || node.Type != "FUNCTION" {
			return fmt.Errorf("%s: invalid function metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if !approvedFunction(node.FunctionName) {
			return fmt.Errorf("%s.functionName: function %q is not admitted", path, node.FunctionName)
		}
		if node.Catalog != "" || node.Schema != "" && !strings.EqualFold(node.Schema, "main") {
			return fmt.Errorf("%s: qualified function is not admitted", path)
		}
		if err := validateSQLExpressionSlice(node.Children, path+".children", scope); err != nil {
			return err
		}
		if node.Filter != nil {
			if err := validateSQLExpressionNode(*node.Filter, path+".filter", scope); err != nil {
				return err
			}
		}
		return validateSQLOrderSlice(node.Orders, path+".orders", scope)
	case "operator":
		if node.Class != "OPERATOR" {
			return fmt.Errorf("%s: invalid operator metadata class=%q", path, node.Class)
		}
		if !approvedOperator(node.Type) {
			return fmt.Errorf("%s.type: operator %q is not admitted", path, node.Type)
		}
		switch node.Type {
		case "OPERATOR_NOT", "OPERATOR_IS_NULL", "OPERATOR_IS_NOT_NULL":
			if len(node.Children) != 1 {
				return fmt.Errorf("%s.children: %s requires exactly one operand", path, node.Type)
			}
		default:
			if len(node.Children) == 0 {
				return fmt.Errorf("%s.children: operator requires at least one operand", path)
			}
		}
		return validateSQLExpressionSlice(node.Children, path+".children", scope)
	case "comparison":
		if node.Class != "COMPARISON" {
			return fmt.Errorf("%s: invalid comparison metadata class=%q", path, node.Class)
		}
		if !approvedComparison(node.Type) {
			return fmt.Errorf("%s.type: comparison %q is not admitted", path, node.Type)
		}
		if node.Left == nil || node.Right == nil {
			return fmt.Errorf("%s: comparison requires left and right expressions", path)
		}
		if err := validateSQLExpressionNode(*node.Left, path+".left", scope); err != nil {
			return err
		}
		return validateSQLExpressionNode(*node.Right, path+".right", scope)
	case "conjunction":
		if node.Class != "CONJUNCTION" {
			return fmt.Errorf("%s: invalid conjunction metadata class=%q", path, node.Class)
		}
		if node.Type != "CONJUNCTION_AND" && node.Type != "CONJUNCTION_OR" {
			return fmt.Errorf("%s.type: conjunction %q is not admitted", path, node.Type)
		}
		if len(node.Children) < 2 {
			return fmt.Errorf("%s.children: conjunction requires at least two operands", path)
		}
		return validateSQLExpressionSlice(node.Children, path+".children", scope)
	case "cast":
		if node.Class != "CAST" || node.Type != "OPERATOR_CAST" {
			return fmt.Errorf("%s: invalid cast metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if node.Child == nil || node.LogicalType == nil {
			return fmt.Errorf("%s: cast requires child and logicalType", path)
		}
		if err := validateSQLExpressionNode(*node.Child, path+".child", scope); err != nil {
			return err
		}
		return validateSQLTypeRequiredID(*node.LogicalType, path+".logicalType")
	case "case":
		if node.Class != "CASE" || node.Type != "CASE_EXPR" {
			return fmt.Errorf("%s: invalid case metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if len(node.Checks) == 0 {
			return fmt.Errorf("%s.checks: CASE requires at least one check", path)
		}
		if node.Else == nil {
			return fmt.Errorf("%s.else: CASE requires an explicit projected ELSE expression", path)
		}
		for index, check := range node.Checks {
			checkPath := fmt.Sprintf("%s.checks[%d]", path, index)
			if err := validateSQLExpressionNode(check.When, checkPath+".when", scope); err != nil {
				return err
			}
			if err := validateSQLExpressionNode(check.Then, checkPath+".then", scope); err != nil {
				return err
			}
		}
		if node.Else != nil {
			return validateSQLExpressionNode(*node.Else, path+".else", scope)
		}
	case "window":
		if node.Class != "WINDOW" {
			return fmt.Errorf("%s: invalid window metadata class=%q", path, node.Class)
		}
		if !approvedWindowType(node.Type) {
			return fmt.Errorf("%s.type: window %q is not admitted", path, node.Type)
		}
		if !validSQLWindowBoundary(node.WindowStart) || !validSQLWindowBoundary(node.WindowEnd) || !validSQLWindowExclude(node.ExcludeClause) {
			return fmt.Errorf("%s: invalid window frame or exclusion enum", path)
		}
		if !approvedFunction(node.FunctionName) || node.Catalog != "" || node.Schema != "" && !strings.EqualFold(node.Schema, "main") {
			return fmt.Errorf("%s: qualified or unapproved window function %q", path, node.FunctionName)
		}
		if err := validateSQLExpressionSlice(node.Partitions, path+".partitions", scope); err != nil {
			return err
		}
		if err := validateSQLOrderSlice(node.Orders, path+".orders", scope); err != nil {
			return err
		}
		if err := validateSQLExpressionSlice(node.Children, path+".children", scope); err != nil {
			return err
		}
		if err := validateWindowBoundaryExpression(node.WindowStart, node.StartExpression, path+".startExpression", scope); err != nil {
			return err
		}
		if err := validateWindowBoundaryExpression(node.WindowEnd, node.EndExpression, path+".endExpression", scope); err != nil {
			return err
		}
		if err := validateOptionalExpressionFields(node, path, scope); err != nil {
			return err
		}
		return validateSQLOrderSlice(node.ArgumentOrders, path+".argumentOrders", scope)
	case "subquery_expression":
		if node.Class != "SUBQUERY" || node.Type != "SUBQUERY" {
			return fmt.Errorf("%s: invalid subquery metadata class=%q type=%q", path, node.Class, node.Type)
		}
		if node.Query == nil {
			return fmt.Errorf("%s.query: required subquery statement is missing", path)
		}
		if !validSQLSubqueryType(node.SubqueryType) {
			return fmt.Errorf("%s.subqueryType: invalid subquery type %q", path, node.SubqueryType)
		}
		switch node.SubqueryType {
		case "ANY", "ALL":
			if node.Child == nil {
				return fmt.Errorf("%s.child: %s subquery requires child expression", path, node.SubqueryType)
			}
			if node.ComparisonType == "" || node.ComparisonType == "INVALID" || !approvedComparison(node.ComparisonType) {
				return fmt.Errorf("%s.comparisonType: %s subquery requires an admitted comparison type", path, node.SubqueryType)
			}
		default:
			if node.Child != nil {
				return fmt.Errorf("%s.child: %s subquery cannot carry a child expression", path, node.SubqueryType)
			}
			if node.ComparisonType != "INVALID" {
				return fmt.Errorf("%s.comparisonType: %s subquery requires INVALID comparison type", path, node.SubqueryType)
			}
		}
		if err := validateSQLStatementNode(*node.Query, path+".query", scope); err != nil {
			return err
		}
		if node.Child != nil {
			return validateSQLExpressionNode(*node.Child, path+".child", scope)
		}
	case "between":
		if node.Class != "BETWEEN" {
			return fmt.Errorf("%s: invalid between metadata class=%q", path, node.Class)
		}
		if node.Type != "COMPARE_BETWEEN" && node.Type != "COMPARE_NOT_BETWEEN" {
			return fmt.Errorf("%s.type: between %q is not admitted", path, node.Type)
		}
		if node.Child == nil || node.Lower == nil || node.Upper == nil {
			return fmt.Errorf("%s: BETWEEN requires child, lower, and upper expressions", path)
		}
		if err := validateSQLExpressionNode(*node.Child, path+".child", scope); err != nil {
			return err
		}
		if err := validateSQLExpressionNode(*node.Lower, path+".lower", scope); err != nil {
			return err
		}
		return validateSQLExpressionNode(*node.Upper, path+".upper", scope)
	default:
		return fmt.Errorf("%s: invalid expression node kind %q", path, node.Kind)
	}
	return nil
}

func validateOptionalExpressionFields(node sqlNode, path string, scope map[string]struct{}) error {
	for _, field := range []struct {
		name  string
		value *sqlNode
	}{
		{"filter", node.Filter}, {"offsetExpression", node.OffsetExpression}, {"defaultExpression", node.DefaultExpression},
	} {
		if field.value != nil {
			if err := validateSQLExpressionNode(*field.value, path+"."+field.name, scope); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateWindowBoundaryExpression(boundary string, expression *sqlNode, path string, scope map[string]struct{}) error {
	requiresExpression := strings.HasPrefix(boundary, "EXPR_")
	if requiresExpression && expression == nil {
		return fmt.Errorf("%s: %s window boundary requires an expression", path, boundary)
	}
	if !requiresExpression && expression != nil {
		return fmt.Errorf("%s: %s window boundary cannot carry an expression", path, boundary)
	}
	if expression != nil {
		if err := validateSQLExpressionNode(*expression, path, scope); err != nil {
			return err
		}
	}
	return nil
}

func validateSQLExpressionSlice(values []sqlNode, path string, scope map[string]struct{}) error {
	for index := range values {
		if err := validateSQLExpressionNode(values[index], fmt.Sprintf("%s[%d]", path, index), scope); err != nil {
			return err
		}
	}
	return nil
}

func validateSQLOrderSlice(values []sqlOrder, path string, scope map[string]struct{}) error {
	for index := range values {
		order := values[index]
		orderPath := fmt.Sprintf("%s[%d]", path, index)
		if !validSQLOrderType(order.Type) || !validSQLOrderNullType(order.NullOrder) {
			return fmt.Errorf("%s: invalid order type or null order", orderPath)
		}
		if err := validateSQLExpressionNode(order.Value, orderPath+".value", scope); err != nil {
			return err
		}
	}
	return nil
}

func validateSQLModifier(value sqlModifier, path string, scope map[string]struct{}) error {
	if err := validateSQLModifierFields(value, path); err != nil {
		return err
	}
	switch value.Kind {
	case "distinct":
		return nil
	case "order":
		if len(value.Orders) == 0 {
			return fmt.Errorf("%s.orders: ORDER modifier requires orders", path)
		}
		return validateSQLOrderSlice(value.Orders, path+".orders", scope)
	case "limit":
		if value.Limit == nil && value.Offset == nil {
			return fmt.Errorf("%s: LIMIT modifier requires limit or offset expression", path)
		}
		if value.Limit != nil {
			if err := validateSQLExpressionNode(*value.Limit, path+".limit", scope); err != nil {
				return err
			}
		}
		if value.Offset != nil {
			return validateSQLExpressionNode(*value.Offset, path+".offset", scope)
		}
		return nil
	default:
		return fmt.Errorf("%s.kind: invalid modifier kind %q", path, value.Kind)
	}
}

func validateSQLModifierFields(value sqlModifier, path string) error {
	allowed := map[string]struct{}{"kind": {}}
	switch value.Kind {
	case "distinct":
	case "order":
		allowed["orders"] = struct{}{}
	case "limit":
		allowed["limit"] = struct{}{}
		allowed["offset"] = struct{}{}
	default:
		return nil // the kind-specific switch reports the invalid discriminator.
	}
	for name := range encodedSQLModifierFields(value) {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s: field %q is not allowed for modifier kind %q", path, name, value.Kind)
		}
	}
	return nil
}

func encodedSQLModifierFields(value sqlModifier) map[string]struct{} {
	result := map[string]struct{}{"kind": {}}
	rv := reflect.ValueOf(value)
	rt := rv.Type()
	for index := 0; index < rv.NumField(); index++ {
		field := rt.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || name == "kind" || sqlFieldIsEmpty(rv.Field(index)) {
			continue
		}
		result[name] = struct{}{}
	}
	return result
}

func validateSQLType(value sqlType, path string) error {
	if !validSQLLogicalType(value.ID) {
		return fmt.Errorf("%s.id: invalid logical type ID %q", path, value.ID)
	}
	if value.Info != nil {
		return validateSQLValue(*value.Info, path+".info")
	}
	return nil
}

func validateSQLTypeRequiredID(value sqlType, path string) error {
	if value.ID == "" {
		return fmt.Errorf("%s.id: logical type ID is required", path)
	}
	return validateSQLType(value, path)
}

func validateSQLValue(value sqlValue, path string) error {
	switch value.Kind {
	case "null":
		if value.Bool != nil || value.Number != "" || value.String != "" || len(value.Array) != 0 || len(value.Object) != 0 {
			return fmt.Errorf("%s: null value has payload", path)
		}
	case "boolean":
		if value.Bool == nil || value.Number != "" || value.String != "" || len(value.Array) != 0 || len(value.Object) != 0 {
			return fmt.Errorf("%s: boolean value has invalid payload", path)
		}
	case "number":
		if !validSQLNumber(value.Number) || value.Bool != nil || value.String != "" || len(value.Array) != 0 || len(value.Object) != 0 {
			return fmt.Errorf("%s: number value has invalid payload", path)
		}
	case "string":
		if value.Bool != nil || value.Number != "" || len(value.Array) != 0 || len(value.Object) != 0 {
			return fmt.Errorf("%s: string value has invalid payload", path)
		}
	case "array":
		if value.Bool != nil || value.Number != "" || value.String != "" || len(value.Object) != 0 {
			return fmt.Errorf("%s: array value has invalid payload", path)
		}
		for index := range value.Array {
			if err := validateSQLValue(value.Array[index], fmt.Sprintf("%s.array[%d]", path, index)); err != nil {
				return err
			}
		}
	case "object":
		if value.Bool != nil || value.Number != "" || value.String != "" || len(value.Array) != 0 {
			return fmt.Errorf("%s: object value has invalid payload", path)
		}
		for index := range value.Object {
			field := value.Object[index]
			if err := validateSQLValue(field.Value, fmt.Sprintf("%s.object[%d].value", path, index)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s.kind: invalid value kind %q", path, value.Kind)
	}
	return nil
}

func validSQLNumber(value string) bool {
	if value == "" || !json.Valid([]byte(value)) {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return false
	}
	number, ok := decoded.(json.Number)
	if !ok || number.String() != value {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func validSQLLogicalType(value string) bool {
	if value == "" { // Current DuckDB parser leaves literal logical IDs empty.
		return true
	}
	_, ok := sqlLogicalTypeNames[value]
	return ok
}

func validSQLOrderType(value string) bool {
	switch value {
	case "ORDER_DEFAULT", "ASCENDING", "DESCENDING":
		return true
	default:
		return false
	}
}

func validSQLOrderNullType(value string) bool {
	switch value {
	case "ORDER_DEFAULT", "NULLS_FIRST", "NULLS_LAST":
		return true
	default:
		return false
	}
}

func validSQLWindowBoundary(value string) bool {
	switch value {
	case "", "UNBOUNDED_PRECEDING", "UNBOUNDED_FOLLOWING", "CURRENT_ROW_RANGE", "CURRENT_ROW_ROWS", "EXPR_PRECEDING_ROWS", "EXPR_FOLLOWING_ROWS", "EXPR_PRECEDING_RANGE", "EXPR_FOLLOWING_RANGE", "CURRENT_ROW_GROUPS", "EXPR_PRECEDING_GROUPS", "EXPR_FOLLOWING_GROUPS":
		return true
	default:
		return false
	}
}

func validSQLWindowExclude(value string) bool {
	switch value {
	case "", "NO_OTHER", "CURRENT_ROW", "GROUP", "TIES":
		return true
	default:
		return false
	}
}

func validSQLSubqueryType(value string) bool {
	switch value {
	case "SCALAR", "EXISTS", "NOT_EXISTS", "ANY", "ALL", "ARRAY":
		return true
	default:
		return false
	}
}

func validateSQLNodeFields(node sqlNode, role sqlNodeRole, path string) error {
	allowed, ok := sqlNodeFieldsByKind[node.Kind]
	if !ok {
		return nil // role-specific validators report the useful invalid-kind error.
	}
	for name := range encodedSQLNodeFields(node) {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%s: field %q is not allowed for node kind %q", path, name, node.Kind)
		}
	}
	switch role {
	case sqlStatementRole:
		if node.Kind != "select" && node.Kind != "set_operation" {
			return fmt.Errorf("%s: invalid statement node kind %q", path, node.Kind)
		}
	case sqlRelationRole:
		switch node.Kind {
		case "base_table", "empty", "subquery_relation", "join", "expression_list":
		default:
			return fmt.Errorf("%s: invalid relation node kind %q", path, node.Kind)
		}
	case sqlExpressionRole:
		switch node.Kind {
		case "constant", "column", "star", "function", "operator", "comparison", "conjunction", "cast", "case", "window", "subquery_expression", "between":
		default:
			return fmt.Errorf("%s: invalid expression node kind %q", path, node.Kind)
		}
	}
	return nil
}

func encodedSQLNodeFields(node sqlNode) map[string]struct{} {
	result := map[string]struct{}{"kind": {}}
	value := reflect.ValueOf(node)
	typeOfValue := value.Type()
	for index := 0; index < value.NumField(); index++ {
		field := typeOfValue.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || name == "kind" || sqlFieldIsEmpty(value.Field(index)) {
			continue
		}
		result[name] = struct{}{}
	}
	return result
}

func sqlFieldIsEmpty(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Bool:
		return !value.Bool()
	case reflect.String:
		return value.String() == ""
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int() == 0
	case reflect.Slice, reflect.Map:
		return value.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return value.IsNil()
	default:
		return value.IsZero()
	}
}

func validateIdentifierSlice(values []string, path string) error {
	for index, value := range values {
		if !validIdentifier(value) {
			return fmt.Errorf("%s[%d]: invalid identifier %q", path, index, value)
		}
	}
	return nil
}

func validateUniqueSQLIdentifiers(values []string, path string) error {
	if err := validateIdentifierSlice(values, path); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s: duplicate identifier %q", path, value)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func cloneCTEScope(scope map[string]struct{}) map[string]struct{} {
	if len(scope) == 0 {
		return nil
	}
	result := make(map[string]struct{}, len(scope))
	for key := range scope {
		result[key] = struct{}{}
	}
	return result
}
