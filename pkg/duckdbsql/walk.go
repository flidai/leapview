package duckdbsql

import "fmt"

// WalkCallbacks receives generic AST nodes without exposing serialized DuckDB
// JSON. Nil callbacks are ignored.
type WalkCallbacks struct {
	Statement  func(Statement) error
	Relation   func(Relation) error
	Expression func(Expression) error
	CTE        func(CTE) error
}

func Walk(query Query, callbacks WalkCallbacks) error {
	for _, statement := range query.Statements {
		if err := walkStatement(statement, callbacks); err != nil {
			return err
		}
	}
	return nil
}

func walkStatement(statement Statement, c WalkCallbacks) error {
	if statement == nil {
		return fmt.Errorf("duckdbsql: nil statement")
	}
	if c.Statement != nil {
		if err := c.Statement(statement); err != nil {
			return err
		}
	}
	switch s := statement.(type) {
	case *SelectStatement:
		for _, cte := range s.CTEs {
			if c.CTE != nil {
				if err := c.CTE(cte); err != nil {
					return err
				}
			}
			if err := walkStatement(cte.Query, c); err != nil {
				return err
			}
		}
		for _, m := range s.Modifiers {
			if err := walkModifier(m, c); err != nil {
				return err
			}
		}
		for _, e := range s.SelectList {
			if err := walkExpression(e, c); err != nil {
				return err
			}
		}
		if err := walkRelation(s.From, c); err != nil {
			return err
		}
		for _, e := range []Expression{s.Where, s.Having, s.Qualify} {
			if err := walkExpression(e, c); err != nil {
				return err
			}
		}
		for _, e := range s.GroupExpressions {
			if err := walkExpression(e, c); err != nil {
				return err
			}
		}
	case *SetOperationStatement:
		for _, cte := range s.CTEs {
			if c.CTE != nil {
				if err := c.CTE(cte); err != nil {
					return err
				}
			}
			if err := walkStatement(cte.Query, c); err != nil {
				return err
			}
		}
		for _, m := range s.Modifiers {
			if err := walkModifier(m, c); err != nil {
				return err
			}
		}
		if err := walkStatement(s.Left, c); err != nil {
			return err
		}
		if err := walkStatement(s.Right, c); err != nil {
			return err
		}
		for _, child := range s.Children {
			if err := walkStatement(child, c); err != nil {
				return err
			}
		}
	case *RecursiveCTEStatement:
		for _, cte := range s.CTEs {
			if c.CTE != nil {
				if err := c.CTE(cte); err != nil {
					return err
				}
			}
			if err := walkStatement(cte.Query, c); err != nil {
				return err
			}
		}
		for _, modifier := range s.Modifiers {
			if err := walkModifier(modifier, c); err != nil {
				return err
			}
		}
		if err := walkStatement(s.Left, c); err != nil {
			return err
		}
		if err := walkStatement(s.Right, c); err != nil {
			return err
		}
		for _, target := range s.KeyTargets {
			if err := walkExpression(target, c); err != nil {
				return err
			}
		}
	case *CTENodeStatement:
		for _, cte := range s.CTEs {
			if c.CTE != nil {
				if err := c.CTE(cte); err != nil {
					return err
				}
			}
			if err := walkStatement(cte.Query, c); err != nil {
				return err
			}
		}
		for _, modifier := range s.Modifiers {
			if err := walkModifier(modifier, c); err != nil {
				return err
			}
		}
		if err := walkStatement(s.Query, c); err != nil {
			return err
		}
		return walkStatement(s.Child, c)
	default:
		return fmt.Errorf("duckdbsql: unknown statement type %T", statement)
	}
	return nil
}

func walkModifier(m Modifier, c WalkCallbacks) error {
	switch v := m.(type) {
	case *DistinctModifier:
		for _, target := range v.DistinctOnTargets {
			if err := walkExpression(target, c); err != nil {
				return err
			}
		}
		return nil
	case *OrderModifier:
		for _, o := range v.Orders {
			if err := walkExpression(o.Expression, c); err != nil {
				return err
			}
		}
	case *LimitModifier:
		if err := walkExpression(v.Limit, c); err != nil {
			return err
		}
		return walkExpression(v.Offset, c)
	case *LimitPercentModifier:
		if err := walkExpression(v.Limit, c); err != nil {
			return err
		}
		return walkExpression(v.Offset, c)
	default:
		return fmt.Errorf("duckdbsql: unknown modifier type %T", m)
	}
	return nil
}

func walkRelation(r Relation, c WalkCallbacks) error {
	if r == nil {
		return nil
	}
	if c.Relation != nil {
		if err := c.Relation(r); err != nil {
			return err
		}
	}
	switch v := r.(type) {
	case *BaseTableRelation:
		if v.At != nil {
			return walkExpression(v.At.Expression, c)
		}
		return nil
	case *EmptyRelation, *ColumnDataRelation:
		return nil
	case *JoinRelation:
		if err := walkRelation(v.Left, c); err != nil {
			return err
		}
		if err := walkRelation(v.Right, c); err != nil {
			return err
		}
		if err := walkExpression(v.Condition, c); err != nil {
			return err
		}
		for _, e := range v.DuplicateEliminatedColumns {
			if err := walkExpression(e, c); err != nil {
				return err
			}
		}
		return walkExpression(v.RankingExpression, c)
	case *SubqueryRelation:
		return walkStatement(v.Query, c)
	case *TableFunctionRelation:
		return walkExpression(v.Function, c)
	case *ExpressionListRelation:
		for _, row := range v.Values {
			for _, e := range row {
				if err := walkExpression(e, c); err != nil {
					return err
				}
			}
		}
		return nil
	case *PivotRelation:
		if err := walkRelation(v.Source, c); err != nil {
			return err
		}
		for _, e := range v.Aggregates {
			if err := walkExpression(e, c); err != nil {
				return err
			}
		}
		for _, pivot := range v.Pivots {
			for _, expr := range pivot.PivotExpressions {
				if err := walkExpression(expr, c); err != nil {
					return err
				}
			}
			for _, entry := range pivot.Entries {
				if err := walkExpression(entry.StarExpression, c); err != nil {
					return err
				}
			}
		}
		return nil
	case *ShowRelation:
		return walkStatement(v.Query, c)
	default:
		return fmt.Errorf("duckdbsql: unknown relation type %T", r)
	}
}

func walkExpression(e Expression, c WalkCallbacks) error {
	if e == nil {
		return nil
	}
	if c.Expression != nil {
		if err := c.Expression(e); err != nil {
			return err
		}
	}
	switch v := e.(type) {
	case *ConstantExpression, *ColumnExpression, *DefaultExpression, *LambdaRefExpression, *ParameterExpression, *PositionalReferenceExpression:
		return nil
	case *StarExpression:
		if err := walkExpression(v.Expression, c); err != nil {
			return err
		}
		for _, n := range v.ReplaceList {
			if err := walkExpression(n.Expression, c); err != nil {
				return err
			}
		}
		for _, n := range v.RenameList {
			if err := walkExpression(n.Expression, c); err != nil {
				return err
			}
		}
		return nil
	case *FunctionExpression:
		for _, x := range v.Children {
			if err := walkExpression(x, c); err != nil {
				return err
			}
		}
		if err := walkExpression(v.Filter, c); err != nil {
			return err
		}
		for _, o := range v.OrderBys {
			if err := walkExpression(o.Expression, c); err != nil {
				return err
			}
		}
		return nil
	case *OperatorExpression:
		for _, x := range v.Children {
			if err := walkExpression(x, c); err != nil {
				return err
			}
		}
		return nil
	case *ComparisonExpression:
		if err := walkExpression(v.Left, c); err != nil {
			return err
		}
		return walkExpression(v.Right, c)
	case *ConjunctionExpression:
		for _, x := range v.Children {
			if err := walkExpression(x, c); err != nil {
				return err
			}
		}
		return nil
	case *CastExpression:
		return walkExpression(v.Child, c)
	case *CaseExpression:
		for _, x := range v.Checks {
			if err := walkExpression(x.When, c); err != nil {
				return err
			}
			if err := walkExpression(x.Then, c); err != nil {
				return err
			}
		}
		return walkExpression(v.Else, c)
	case *WindowExpression:
		for _, x := range v.Partitions {
			if err := walkExpression(x, c); err != nil {
				return err
			}
		}
		for _, o := range v.Orders {
			if err := walkExpression(o.Expression, c); err != nil {
				return err
			}
		}
		for _, x := range v.Children {
			if err := walkExpression(x, c); err != nil {
				return err
			}
		}
		filter := v.Filter
		if filter == nil {
			filter = v.FilterExpression
		}
		if err := walkExpression(filter, c); err != nil {
			return err
		}
		if err := walkExpression(v.StartExpression, c); err != nil {
			return err
		}
		if err := walkExpression(v.EndExpression, c); err != nil {
			return err
		}
		if err := walkExpression(v.OffsetExpression, c); err != nil {
			return err
		}
		if err := walkExpression(v.DefaultExpression, c); err != nil {
			return err
		}
		for _, o := range v.ArgOrders {
			if err := walkExpression(o.Expression, c); err != nil {
				return err
			}
		}
		return nil
	case *SubqueryExpression:
		if err := walkStatement(v.Query, c); err != nil {
			return err
		}
		return walkExpression(v.Child, c)
	case *BetweenExpression:
		if err := walkExpression(v.Input, c); err != nil {
			return err
		}
		if err := walkExpression(v.Lower, c); err != nil {
			return err
		}
		return walkExpression(v.Upper, c)
	case *CollateExpression:
		return walkExpression(v.Child, c)
	case *LambdaExpression:
		if err := walkExpression(v.LHS, c); err != nil {
			return err
		}
		return walkExpression(v.Expr, c)
	case *TypeExpression:
		for _, x := range v.Children {
			if err := walkExpression(x, c); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("duckdbsql: unknown expression type %T", e)
	}
}
