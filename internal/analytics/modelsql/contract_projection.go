package modelsql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/pkg/duckdbsql"
)

// The SQL DTO below is a closed allowlist over the subset admitted by
// modelsql. Source spans, comments, parser locations, samples, named
// parameters, and DuckDB's raw serialized AST never fit this representation.
type sqlAST struct {
	Statements []sqlNode `json:"statements"`
}

type sqlNode struct {
	Kind              string        `json:"kind"`
	Class             string        `json:"class,omitempty"`
	Type              string        `json:"type,omitempty"`
	Alias             string        `json:"alias,omitempty"`
	Name              string        `json:"name,omitempty"`
	Schema            string        `json:"schema,omitempty"`
	Catalog           string        `json:"catalog,omitempty"`
	Names             []string      `json:"names,omitempty"`
	Types             []sqlType     `json:"types,omitempty"`
	ColumnAliases     []string      `json:"columnAliases,omitempty"`
	Materialized      string        `json:"materialized,omitempty"`
	AggregateHandling string        `json:"aggregateHandling,omitempty"`
	SetOperation      string        `json:"setOperation,omitempty"`
	SetOperationAll   bool          `json:"setOperationAll,omitempty"`
	JoinType          string        `json:"joinType,omitempty"`
	JoinReferenceType string        `json:"joinReferenceType,omitempty"`
	UsingColumns      []string      `json:"usingColumns,omitempty"`
	SubqueryType      string        `json:"subqueryType,omitempty"`
	ComparisonType    string        `json:"comparisonType,omitempty"`
	FunctionName      string        `json:"functionName,omitempty"`
	Distinct          bool          `json:"distinct,omitempty"`
	IsOperator        bool          `json:"isOperator,omitempty"`
	ExportState       bool          `json:"exportState,omitempty"`
	TryCast           bool          `json:"tryCast,omitempty"`
	IgnoreNulls       bool          `json:"ignoreNulls,omitempty"`
	ExcludeClause     string        `json:"excludeClause,omitempty"`
	WindowStart       string        `json:"windowStart,omitempty"`
	WindowEnd         string        `json:"windowEnd,omitempty"`
	DelimFlipped      bool          `json:"delimFlipped,omitempty"`
	Implicit          bool          `json:"implicit,omitempty"`
	NearestCount      int64         `json:"nearestCount,omitempty"`
	NearestOrderType  string        `json:"nearestOrderType,omitempty"`
	NearestApprox     bool          `json:"nearestApprox,omitempty"`
	Value             *sqlValue     `json:"value,omitempty"`
	LogicalType       *sqlType      `json:"logicalType,omitempty"`
	Modifiers         []sqlModifier `json:"modifiers,omitempty"`
	CTEs              []sqlCTE      `json:"ctes,omitempty"`
	Select            []sqlNode     `json:"select,omitempty"`
	From              *sqlNode      `json:"from,omitempty"`
	Where             *sqlNode      `json:"where,omitempty"`
	Groups            []sqlNode     `json:"groups,omitempty"`
	GroupSets         [][]int       `json:"groupSets,omitempty"`
	Having            *sqlNode      `json:"having,omitempty"`
	Qualify           *sqlNode      `json:"qualify,omitempty"`
	Left              *sqlNode      `json:"left,omitempty"`
	Right             *sqlNode      `json:"right,omitempty"`
	Condition         *sqlNode      `json:"condition,omitempty"`
	Query             *sqlNode      `json:"query,omitempty"`
	Child             *sqlNode      `json:"child,omitempty"`
	Children          []sqlNode     `json:"children,omitempty"`
	DuplicateColumns  []sqlNode     `json:"duplicateEliminatedColumns,omitempty"`
	RankingExpression *sqlNode      `json:"rankingExpression,omitempty"`
	Checks            []sqlCase     `json:"checks,omitempty"`
	Else              *sqlNode      `json:"else,omitempty"`
	Partitions        []sqlNode     `json:"partitions,omitempty"`
	Orders            []sqlOrder    `json:"orders,omitempty"`
	ArgumentOrders    []sqlOrder    `json:"argumentOrders,omitempty"`
	Filter            *sqlNode      `json:"filter,omitempty"`
	StartExpression   *sqlNode      `json:"startExpression,omitempty"`
	EndExpression     *sqlNode      `json:"endExpression,omitempty"`
	OffsetExpression  *sqlNode      `json:"offsetExpression,omitempty"`
	DefaultExpression *sqlNode      `json:"defaultExpression,omitempty"`
	Lower             *sqlNode      `json:"lower,omitempty"`
	Upper             *sqlNode      `json:"upper,omitempty"`
	Rows              [][]sqlNode   `json:"rows,omitempty"`
}

type sqlCTE struct {
	Name         string  `json:"name"`
	Materialized string  `json:"materialized,omitempty"`
	Query        sqlNode `json:"query"`
}

type sqlModifier struct {
	Kind   string     `json:"kind"`
	Orders []sqlOrder `json:"orders,omitempty"`
	Limit  *sqlNode   `json:"limit,omitempty"`
	Offset *sqlNode   `json:"offset,omitempty"`
}

type sqlOrder struct {
	Type      string  `json:"type"`
	NullOrder string  `json:"nullOrder"`
	Value     sqlNode `json:"value"`
}

type sqlCase struct {
	When sqlNode `json:"when"`
	Then sqlNode `json:"then"`
}

type sqlType struct {
	ID        string    `json:"id"`
	Modifiers []int64   `json:"modifiers,omitempty"`
	Info      *sqlValue `json:"info,omitempty"`
}

type sqlValue struct {
	Kind   string     `json:"kind"`
	Bool   *bool      `json:"boolean,omitempty"`
	Number string     `json:"number,omitempty"`
	String string     `json:"string,omitempty"`
	Array  []sqlValue `json:"array,omitempty"`
	Object []sqlField `json:"object,omitempty"`
}

type sqlField struct {
	Name  string   `json:"name"`
	Value sqlValue `json:"value"`
}

// CanonicalProjection returns the closed contract projection of one admitted
// Model SQL statement. It is separate from execution SQL and raw parser JSON.
func CanonicalProjection(sqlText string) (*string, error) {
	if _, err := Analyze(context.Background(), sqlText); err != nil {
		return nil, err
	}
	query, err := duckdbsql.Parse(context.Background(), sqlText)
	if err != nil {
		return nil, err
	}
	if err := validateSQLProjection(query); err != nil {
		return nil, err
	}
	statements := make([]sqlNode, len(query.Statements))
	for index, statement := range query.Statements {
		projected, err := projectSQLStatement(statement)
		if err != nil {
			return nil, err
		}
		statements[index] = projected
	}
	encoded, err := json.Marshal(sqlAST{Statements: statements})
	if err != nil {
		return nil, fmt.Errorf("marshal canonical Model SQL AST: %w", err)
	}
	result := string(encoded)
	return &result, nil
}

func validateSQLProjection(query duckdbsql.Query) error {
	return duckdbsql.Walk(query, duckdbsql.WalkCallbacks{
		Statement: func(value duckdbsql.Statement) error {
			switch value.(type) {
			case *duckdbsql.SelectStatement, *duckdbsql.SetOperationStatement:
				return nil
			default:
				return fmt.Errorf("SQL statement %T has no contract projection", value)
			}
		},
		Relation: func(value duckdbsql.Relation) error {
			switch value.(type) {
			case *duckdbsql.BaseTableRelation, *duckdbsql.EmptyRelation, *duckdbsql.SubqueryRelation, *duckdbsql.JoinRelation, *duckdbsql.ExpressionListRelation:
				return nil
			default:
				return fmt.Errorf("SQL relation %T has no contract projection", value)
			}
		},
		Expression: func(value duckdbsql.Expression) error {
			switch value.(type) {
			case *duckdbsql.ConstantExpression, *duckdbsql.ColumnExpression, *duckdbsql.StarExpression, *duckdbsql.FunctionExpression,
				*duckdbsql.OperatorExpression, *duckdbsql.ComparisonExpression, *duckdbsql.ConjunctionExpression, *duckdbsql.CastExpression,
				*duckdbsql.CaseExpression, *duckdbsql.WindowExpression, *duckdbsql.SubqueryExpression, *duckdbsql.BetweenExpression:
				return nil
			default:
				return fmt.Errorf("SQL expression %T has no contract projection", value)
			}
		},
	})
}

func projectSQLStatement(value duckdbsql.Statement) (sqlNode, error) {
	switch typed := value.(type) {
	case *duckdbsql.SelectStatement:
		result := sqlMeta("select", typed.Meta())
		result.AggregateHandling = typed.AggregateHandling
		result.Modifiers = projectSQLModifiers(typed.Modifiers)
		result.CTEs = projectSQLCTEs(typed.CTEs)
		result.Select = projectSQLExpressions(typed.SelectList)
		result.From = projectSQLRelationOptional(typed.From)
		result.Where = projectSQLExpressionOptional(typed.Where)
		result.Groups = projectSQLExpressions(typed.GroupExpressions)
		result.GroupSets = typed.GroupSets
		result.Having = projectSQLExpressionOptional(typed.Having)
		result.Qualify = projectSQLExpressionOptional(typed.Qualify)
		return result, nil
	case *duckdbsql.SetOperationStatement:
		result := sqlMeta("set_operation", typed.Meta())
		result.SetOperation = typed.SetOpType
		result.SetOperationAll = typed.SetOpAll
		result.Modifiers = projectSQLModifiers(typed.Modifiers)
		result.CTEs = projectSQLCTEs(typed.CTEs)
		left, err := projectSQLStatement(typed.Left)
		if err != nil {
			return sqlNode{}, err
		}
		right, err := projectSQLStatement(typed.Right)
		if err != nil {
			return sqlNode{}, err
		}
		result.Left, result.Right = &left, &right
		for _, child := range typed.Children {
			projected, err := projectSQLStatement(child)
			if err != nil {
				return sqlNode{}, err
			}
			result.Children = append(result.Children, projected)
		}
		return result, nil
	default:
		return sqlNode{}, fmt.Errorf("unsupported admitted SQL statement %T", value)
	}
}

func projectSQLRelation(value duckdbsql.Relation) (sqlNode, error) {
	switch typed := value.(type) {
	case *duckdbsql.BaseTableRelation:
		result := sqlMeta("base_table", typed.Meta())
		result.Catalog, result.Schema, result.Name = typed.Catalog, typed.Schema, typed.Name
		result.ColumnAliases = append([]string(nil), typed.ColumnAliases...)
		return result, nil
	case *duckdbsql.EmptyRelation:
		return sqlMeta("empty", typed.Meta()), nil
	case *duckdbsql.SubqueryRelation:
		result := sqlMeta("subquery_relation", typed.Meta())
		query, err := projectSQLStatement(typed.Query)
		if err != nil {
			return sqlNode{}, err
		}
		result.Query = &query
		result.ColumnAliases = append([]string(nil), typed.ColumnAliases...)
		return result, nil
	case *duckdbsql.JoinRelation:
		result := sqlMeta("join", typed.Meta())
		left, err := projectSQLRelation(typed.Left)
		if err != nil {
			return sqlNode{}, err
		}
		right, err := projectSQLRelation(typed.Right)
		if err != nil {
			return sqlNode{}, err
		}
		result.Left, result.Right = &left, &right
		result.Condition = projectSQLExpressionOptional(typed.Condition)
		result.JoinType, result.JoinReferenceType = typed.JoinType, typed.RefType
		result.UsingColumns = append([]string(nil), typed.UsingColumns...)
		result.DelimFlipped, result.Implicit = typed.DelimFlipped, typed.IsImplicit
		result.DuplicateColumns = projectSQLExpressions(typed.DuplicateEliminatedColumns)
		result.RankingExpression = projectSQLExpressionOptional(typed.RankingExpression)
		result.NearestCount, result.NearestOrderType, result.NearestApprox = typed.NearestCount, typed.NearestOrderType, typed.NearestApprox
		return result, nil
	case *duckdbsql.ExpressionListRelation:
		result := sqlMeta("expression_list", typed.Meta())
		result.Names = append([]string(nil), typed.ExpectedNames...)
		for _, expectedType := range typed.ExpectedTypes {
			result.Types = append(result.Types, projectSQLType(expectedType))
		}
		for _, row := range typed.Values {
			result.Rows = append(result.Rows, projectSQLExpressions(row))
		}
		return result, nil
	default:
		return sqlNode{}, fmt.Errorf("unsupported admitted SQL relation %T", value)
	}
}

func projectSQLExpression(value duckdbsql.Expression) (sqlNode, error) {
	switch typed := value.(type) {
	case *duckdbsql.ConstantExpression:
		result := sqlMeta("constant", typed.Meta())
		projected := projectSQLValue(typed.Value)
		result.Value = &projected
		logicalType := projectSQLType(typed.LogicalType)
		result.LogicalType = &logicalType
		return result, nil
	case *duckdbsql.ColumnExpression:
		result := sqlMeta("column", typed.Meta())
		result.Names = append([]string(nil), typed.Names...)
		return result, nil
	case *duckdbsql.StarExpression:
		return sqlMeta("star", typed.Meta()), nil
	case *duckdbsql.FunctionExpression:
		result := sqlMeta("function", typed.Meta())
		result.FunctionName, result.Schema, result.Catalog = typed.Name, typed.Schema, typed.Catalog
		result.Children = projectSQLExpressions(typed.Children)
		result.Filter = projectSQLExpressionOptional(typed.Filter)
		result.Orders = projectSQLOrders(typed.OrderBys)
		result.Distinct, result.IsOperator, result.ExportState = typed.Distinct, typed.IsOperator, typed.ExportState
		return result, nil
	case *duckdbsql.OperatorExpression:
		result := sqlMeta("operator", typed.Meta())
		result.Children = projectSQLExpressions(typed.Children)
		return result, nil
	case *duckdbsql.ComparisonExpression:
		result := sqlMeta("comparison", typed.Meta())
		result.Left = projectSQLExpressionOptional(typed.Left)
		result.Right = projectSQLExpressionOptional(typed.Right)
		return result, nil
	case *duckdbsql.ConjunctionExpression:
		result := sqlMeta("conjunction", typed.Meta())
		result.Children = projectSQLExpressions(typed.Children)
		return result, nil
	case *duckdbsql.CastExpression:
		result := sqlMeta("cast", typed.Meta())
		result.Child = projectSQLExpressionOptional(typed.Child)
		logicalType := projectSQLType(typed.CastType)
		result.LogicalType, result.TryCast = &logicalType, typed.TryCast
		return result, nil
	case *duckdbsql.CaseExpression:
		result := sqlMeta("case", typed.Meta())
		for _, check := range typed.Checks {
			when, err := projectSQLExpression(check.When)
			if err != nil {
				return sqlNode{}, err
			}
			then, err := projectSQLExpression(check.Then)
			if err != nil {
				return sqlNode{}, err
			}
			result.Checks = append(result.Checks, sqlCase{When: when, Then: then})
		}
		result.Else = projectSQLExpressionOptional(typed.Else)
		return result, nil
	case *duckdbsql.WindowExpression:
		result := sqlMeta("window", typed.Meta())
		result.FunctionName, result.Schema, result.Catalog = typed.FunctionName, typed.Schema, typed.Catalog
		result.Partitions, result.Orders = projectSQLExpressions(typed.Partitions), projectSQLOrders(typed.Orders)
		result.WindowStart, result.WindowEnd = typed.Start, typed.End
		result.StartExpression, result.EndExpression = projectSQLExpressionOptional(typed.StartExpression), projectSQLExpressionOptional(typed.EndExpression)
		result.Children = projectSQLExpressions(typed.Children)
		result.Filter = projectSQLExpressionOptional(typed.FilterExpression)
		result.OffsetExpression, result.DefaultExpression = projectSQLExpressionOptional(typed.OffsetExpression), projectSQLExpressionOptional(typed.DefaultExpression)
		result.IgnoreNulls, result.ExcludeClause, result.Distinct = typed.IgnoreNulls, typed.ExcludeClause, typed.Distinct
		result.ArgumentOrders = projectSQLOrders(typed.ArgOrders)
		return result, nil
	case *duckdbsql.SubqueryExpression:
		result := sqlMeta("subquery_expression", typed.Meta())
		result.SubqueryType, result.ComparisonType = typed.SubqueryType, typed.ComparisonType
		query, err := projectSQLStatement(typed.Query)
		if err != nil {
			return sqlNode{}, err
		}
		result.Query, result.Child = &query, projectSQLExpressionOptional(typed.Child)
		return result, nil
	case *duckdbsql.BetweenExpression:
		result := sqlMeta("between", typed.Meta())
		result.Child = projectSQLExpressionOptional(typed.Input)
		result.Lower, result.Upper = projectSQLExpressionOptional(typed.Lower), projectSQLExpressionOptional(typed.Upper)
		return result, nil
	default:
		return sqlNode{}, fmt.Errorf("unsupported admitted SQL expression %T", value)
	}
}

func sqlMeta(kind string, meta duckdbsql.NodeMeta) sqlNode {
	return sqlNode{Kind: kind, Class: meta.Class, Type: meta.Type, Alias: meta.Alias}
}

func projectSQLRelationOptional(value duckdbsql.Relation) *sqlNode {
	if value == nil {
		return nil
	}
	projected, err := projectSQLRelation(value)
	if err != nil {
		return nil
	}
	return &projected
}

func projectSQLExpressionOptional(value duckdbsql.Expression) *sqlNode {
	if value == nil {
		return nil
	}
	projected, err := projectSQLExpression(value)
	if err != nil {
		return nil
	}
	return &projected
}

func projectSQLExpressions(values []duckdbsql.Expression) []sqlNode {
	result := make([]sqlNode, 0, len(values))
	for _, value := range values {
		projected, err := projectSQLExpression(value)
		if err == nil {
			result = append(result, projected)
		}
	}
	return result
}

func projectSQLCTEs(values []duckdbsql.CTE) []sqlCTE {
	result := make([]sqlCTE, 0, len(values))
	for _, value := range values {
		query, err := projectSQLStatement(value.Query)
		if err == nil {
			result = append(result, sqlCTE{Name: value.Name, Materialized: value.Materialized, Query: query})
		}
	}
	return result
}

func projectSQLModifiers(values []duckdbsql.Modifier) []sqlModifier {
	result := make([]sqlModifier, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case *duckdbsql.DistinctModifier:
			result = append(result, sqlModifier{Kind: "distinct"})
		case *duckdbsql.OrderModifier:
			result = append(result, sqlModifier{Kind: "order", Orders: projectSQLOrders(typed.Orders)})
		case *duckdbsql.LimitModifier:
			result = append(result, sqlModifier{Kind: "limit", Limit: projectSQLExpressionOptional(typed.Limit), Offset: projectSQLExpressionOptional(typed.Offset)})
		}
	}
	return result
}

func projectSQLOrders(values []duckdbsql.Order) []sqlOrder {
	result := make([]sqlOrder, 0, len(values))
	for _, value := range values {
		projected, err := projectSQLExpression(value.Expression)
		if err == nil {
			result = append(result, sqlOrder{Type: value.Type, NullOrder: value.NullOrder, Value: projected})
		}
	}
	return result
}

func projectSQLType(value duckdbsql.LogicalType) sqlType {
	result := sqlType{ID: value.ID, Modifiers: append([]int64(nil), value.Modifiers...)}
	if value.Info.Kind != 0 {
		info := projectSQLValue(value.Info)
		result.Info = &info
	}
	return result
}

func projectSQLValue(value duckdbsql.Value) sqlValue {
	result := sqlValue{Number: value.Number, String: value.String}
	switch value.Kind {
	case duckdbsql.ValueNull:
		result.Kind = "null"
	case duckdbsql.ValueBool:
		result.Kind = "boolean"
		boolean := value.Bool
		result.Bool = &boolean
	case duckdbsql.ValueNumber:
		result.Kind = "number"
	case duckdbsql.ValueString:
		result.Kind = "string"
	case duckdbsql.ValueArray:
		result.Kind = "array"
		for _, item := range value.Array {
			result.Array = append(result.Array, projectSQLValue(item))
		}
	case duckdbsql.ValueObject:
		result.Kind = "object"
		for _, field := range value.Object {
			result.Object = append(result.Object, sqlField{Name: field.Name, Value: projectSQLValue(field.Value)})
		}
	}
	return result
}
