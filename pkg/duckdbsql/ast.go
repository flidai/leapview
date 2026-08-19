package duckdbsql

// Span is a byte range in the original SQL text. End is exclusive. A zero
// range is used when DuckDB did not provide a source length for a node.
type Span struct{ Start, End int }

func (s Span) Valid(n int) bool { return s.Start >= 0 && s.End >= s.Start && s.End <= n }

type ValueKind uint8

const (
	ValueNull ValueKind = iota + 1
	ValueBool
	ValueNumber
	ValueString
	ValueArray
	ValueObject
)

// Value is a bounded, typed value used for DuckDB constants and supporting
// serialization values. It intentionally has no map[string]any escape hatch.
type Value struct {
	Kind   ValueKind
	Bool   bool
	Number string
	String string
	Array  []Value
	Object []Field
}

type Field struct {
	Name  string
	Value Value
}

type NodeMeta struct {
	Class string
	Type  string
	Alias string
	Span  Span
	// Sample preserves serialized TableRef sampling options as a typed value.
	Sample    Value
	HasSample bool
	// NamedParameters preserves the statement wrapper's named parameter map
	// without exposing serialized JSON internals.
	NamedParameters []NamedParameter
}

type NamedParameter struct {
	Name  string
	Index int64
}

type Query struct {
	Statements []Statement
}

type Statement interface {
	statementNode()
	Meta() NodeMeta
}

type SelectStatement struct {
	NodeMeta          NodeMeta
	Modifiers         []Modifier
	CTEs              []CTE
	SelectList        []Expression
	From              Relation
	Where             Expression
	GroupExpressions  []Expression
	GroupSets         [][]int
	AggregateHandling string
	Having            Expression
	Sample            Value
	HasSample         bool
	Qualify           Expression
}

func (*SelectStatement) statementNode()   {}
func (s *SelectStatement) Meta() NodeMeta { return s.NodeMeta }

type SetOperationStatement struct {
	NodeMeta  NodeMeta
	Modifiers []Modifier
	CTEs      []CTE
	SetOpType string
	SetOpAll  bool
	Left      Statement
	Right     Statement
	Children  []Statement
}

func (*SetOperationStatement) statementNode()   {}
func (s *SetOperationStatement) Meta() NodeMeta { return s.NodeMeta }

type RecursiveCTEStatement struct {
	NodeMeta    NodeMeta
	Modifiers   []Modifier
	CTEs        []CTE
	Name        string
	UnionAll    bool
	Left, Right Statement
	Aliases     []string
	KeyTargets  []Expression
}

func (*RecursiveCTEStatement) statementNode()   {}
func (s *RecursiveCTEStatement) Meta() NodeMeta { return s.NodeMeta }

type CTENodeStatement struct {
	NodeMeta     NodeMeta
	Modifiers    []Modifier
	CTEs         []CTE
	Name         string
	Query, Child Statement
	Aliases      []string
	Materialized string
}

func (*CTENodeStatement) statementNode()   {}
func (s *CTENodeStatement) Meta() NodeMeta { return s.NodeMeta }

type CTE struct {
	Name         string
	Aliases      []string
	Materialized string
	KeyTargets   []Expression
	Query        Statement
}

type Relation interface {
	relationNode()
	Meta() NodeMeta
	RelationType() string
}

type BaseTableRelation struct {
	NodeMeta      NodeMeta
	Catalog       string
	Schema        string
	Name          string
	ColumnAliases []string
	At            *AtClause
	QualifiedName []string
}

func (*BaseTableRelation) relationNode()        {}
func (r *BaseTableRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*BaseTableRelation) RelationType() string { return "BASE_TABLE" }

type EmptyRelation struct{ NodeMeta }

func (*EmptyRelation) relationNode()        {}
func (r *EmptyRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*EmptyRelation) RelationType() string { return "EMPTY" }

type JoinRelation struct {
	NodeMeta                   NodeMeta
	Left, Right                Relation
	Condition                  Expression
	JoinType, RefType          string
	UsingColumns               []string
	DelimFlipped, IsImplicit   bool
	DuplicateEliminatedColumns []Expression
	RankingExpression          Expression
	NearestCount               int64
	NearestOrderType           string
	NearestApprox              bool
}

func (*JoinRelation) relationNode()        {}
func (r *JoinRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*JoinRelation) RelationType() string { return "JOIN" }

type SubqueryRelation struct {
	NodeMeta      NodeMeta
	Query         Statement
	ColumnAliases []string
}

func (*SubqueryRelation) relationNode()        {}
func (r *SubqueryRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*SubqueryRelation) RelationType() string { return "SUBQUERY" }

type TableFunctionRelation struct {
	NodeMeta       NodeMeta
	Function       Expression
	ColumnAliases  []string
	WithOrdinality string
}

func (*TableFunctionRelation) relationNode()        {}
func (r *TableFunctionRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*TableFunctionRelation) RelationType() string { return "TABLE_FUNCTION" }

type ExpressionListRelation struct {
	NodeMeta      NodeMeta
	ExpectedNames []string
	ExpectedTypes []LogicalType
	Values        [][]Expression
}

func (*ExpressionListRelation) relationNode()        {}
func (r *ExpressionListRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*ExpressionListRelation) RelationType() string { return "EXPRESSION_LIST" }

type PivotRelation struct {
	NodeMeta      NodeMeta
	Source        Relation
	Aggregates    []Expression
	UnpivotNames  []string
	Pivots        []PivotColumn
	Groups        []string
	ColumnAliases []string
	IncludeNulls  bool
}

type PivotColumn struct {
	PivotExpressions []Expression
	UnpivotNames     []string
	Entries          []PivotColumnEntry
	PivotEnum        string
}

type PivotColumnEntry struct {
	Values         []Value
	StarExpression Expression
	Alias          string
}

func (*PivotRelation) relationNode()        {}
func (r *PivotRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*PivotRelation) RelationType() string { return "PIVOT" }

type ShowRelation struct {
	NodeMeta                        NodeMeta
	Catalog, Schema, Name, ShowType string
	Query                           Statement
}

func (*ShowRelation) relationNode()        {}
func (r *ShowRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*ShowRelation) RelationType() string { return "SHOW_REF" }

type ColumnDataRelation struct {
	NodeMeta      NodeMeta
	ExpectedNames []string
}

func (*ColumnDataRelation) relationNode()        {}
func (r *ColumnDataRelation) Meta() NodeMeta     { return r.NodeMeta }
func (*ColumnDataRelation) RelationType() string { return "COLUMN_DATA" }

type AtClause struct {
	Unit       string
	Expression Expression
}

type Expression interface {
	expressionNode()
	Meta() NodeMeta
	ExpressionType() string
}

type ConstantExpression struct {
	NodeMeta
	Value       Value
	LogicalType LogicalType
}

func (*ConstantExpression) expressionNode()          {}
func (e *ConstantExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *ConstantExpression) ExpressionType() string { return e.NodeMeta.Type }

type ColumnExpression struct {
	NodeMeta
	Names []string
}

func (*ColumnExpression) expressionNode()          {}
func (e *ColumnExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *ColumnExpression) ExpressionType() string { return e.NodeMeta.Type }

type StarExpression struct {
	NodeMeta
	RelationName         string
	ExcludeList          []string
	ReplaceList          []NamedExpression
	Columns              bool
	Unpacked             bool
	Expression           Expression
	QualifiedExcludeList []string
	RenameList           []NamedExpression
}

func (*StarExpression) expressionNode()          {}
func (e *StarExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *StarExpression) ExpressionType() string { return e.NodeMeta.Type }

type NamedExpression struct {
	Name       string
	Expression Expression
}

type FunctionExpression struct {
	NodeMeta
	Name, Schema, Catalog             string
	Children                          []Expression
	Filter                            Expression
	OrderBys                          []Order
	Distinct, IsOperator, ExportState bool
}

func (*FunctionExpression) expressionNode()          {}
func (e *FunctionExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *FunctionExpression) ExpressionType() string { return e.NodeMeta.Type }

type OperatorExpression struct {
	NodeMeta
	Children []Expression
}

func (*OperatorExpression) expressionNode()          {}
func (e *OperatorExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *OperatorExpression) ExpressionType() string { return e.NodeMeta.Type }

type ComparisonExpression struct {
	NodeMeta
	Left, Right Expression
}

func (*ComparisonExpression) expressionNode()          {}
func (e *ComparisonExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *ComparisonExpression) ExpressionType() string { return e.NodeMeta.Type }

type ConjunctionExpression struct {
	NodeMeta
	Children []Expression
}

func (*ConjunctionExpression) expressionNode()          {}
func (e *ConjunctionExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *ConjunctionExpression) ExpressionType() string { return e.NodeMeta.Type }

type CastExpression struct {
	NodeMeta
	Child    Expression
	CastType LogicalType
	TryCast  bool
}

func (*CastExpression) expressionNode()          {}
func (e *CastExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *CastExpression) ExpressionType() string { return e.NodeMeta.Type }

type CaseExpression struct {
	NodeMeta
	Checks []CaseCheck
	Else   Expression
}

func (*CaseExpression) expressionNode()          {}
func (e *CaseExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *CaseExpression) ExpressionType() string { return e.NodeMeta.Type }

type CaseCheck struct{ When, Then Expression }

type WindowExpression struct {
	NodeMeta
	FunctionName                        string
	Schema, Catalog                     string
	Partitions                          []Expression
	Orders                              []Order
	Start, End                          string
	StartExpression, EndExpression      Expression
	Children                            []Expression
	Filter                              Expression
	FilterExpression                    Expression
	OffsetExpression, DefaultExpression Expression
	IgnoreNulls                         bool
	ExcludeClause                       string
	Distinct                            bool
	ArgOrders                           []Order
}

func (*WindowExpression) expressionNode()          {}
func (e *WindowExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *WindowExpression) ExpressionType() string { return e.NodeMeta.Type }

type SubqueryExpression struct {
	NodeMeta
	SubqueryType, ComparisonType string
	Query                        Statement
	Child                        Expression
}

func (*SubqueryExpression) expressionNode()          {}
func (e *SubqueryExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *SubqueryExpression) ExpressionType() string { return e.NodeMeta.Type }

type BetweenExpression struct {
	NodeMeta
	Input, Lower, Upper Expression
}

func (*BetweenExpression) expressionNode()          {}
func (e *BetweenExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *BetweenExpression) ExpressionType() string { return e.NodeMeta.Type }

type CollateExpression struct {
	NodeMeta
	Child     Expression
	Collation string
}

func (*CollateExpression) expressionNode()          {}
func (e *CollateExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *CollateExpression) ExpressionType() string { return e.NodeMeta.Type }

type DefaultExpression struct{ NodeMeta }

func (*DefaultExpression) expressionNode()          {}
func (e *DefaultExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *DefaultExpression) ExpressionType() string { return e.NodeMeta.Type }

type LambdaExpression struct {
	NodeMeta
	LHS, Expr  Expression
	SyntaxType string
}

func (*LambdaExpression) expressionNode()          {}
func (e *LambdaExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *LambdaExpression) ExpressionType() string { return e.NodeMeta.Type }

type LambdaRefExpression struct {
	NodeMeta
	LambdaIndex int64
	ColumnName  string
}

func (*LambdaRefExpression) expressionNode()          {}
func (e *LambdaRefExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *LambdaRefExpression) ExpressionType() string { return e.NodeMeta.Type }

type ParameterExpression struct {
	NodeMeta
	Identifier string
}

func (*ParameterExpression) expressionNode()          {}
func (e *ParameterExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *ParameterExpression) ExpressionType() string { return e.NodeMeta.Type }

type PositionalReferenceExpression struct {
	NodeMeta
	Index int64
}

func (*PositionalReferenceExpression) expressionNode()          {}
func (e *PositionalReferenceExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *PositionalReferenceExpression) ExpressionType() string { return e.NodeMeta.Type }

type TypeExpression struct {
	NodeMeta
	Catalog, Schema, Name string
	Children              []Expression
}

func (*TypeExpression) expressionNode()          {}
func (e *TypeExpression) Meta() NodeMeta         { return e.NodeMeta }
func (e *TypeExpression) ExpressionType() string { return e.NodeMeta.Type }

type LogicalType struct {
	ID        string
	Modifiers []int64
	Info      Value
}

type Order struct {
	Type, NullOrder string
	Expression      Expression
}

type Modifier interface {
	modifierNode()
	ModifierType() string
}
type DistinctModifier struct{ DistinctOnTargets []Expression }

func (*DistinctModifier) modifierNode()        {}
func (*DistinctModifier) ModifierType() string { return "DISTINCT_MODIFIER" }

type OrderModifier struct{ Orders []Order }

func (*OrderModifier) modifierNode()        {}
func (*OrderModifier) ModifierType() string { return "ORDER_MODIFIER" }

type LimitModifier struct{ Limit, Offset Expression }

func (*LimitModifier) modifierNode()        {}
func (*LimitModifier) ModifierType() string { return "LIMIT_MODIFIER" }

type LimitPercentModifier struct{ Limit, Offset Expression }

func (*LimitPercentModifier) modifierNode()        {}
func (*LimitPercentModifier) ModifierType() string { return "LIMIT_PERCENT_MODIFIER" }
