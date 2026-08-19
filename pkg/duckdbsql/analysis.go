package duckdbsql

import (
	"fmt"
	"strings"
)

type RelationKind string

const (
	RelationBase          RelationKind = "base_table"
	RelationCTE           RelationKind = "cte"
	RelationSubquery      RelationKind = "subquery"
	RelationTableFunction RelationKind = "table_function"
	RelationOther         RelationKind = "other"
)

type RelationRef struct {
	Kind                         RelationKind
	Catalog, Schema, Name, Alias string
	Span                         Span
	CTE                          bool
	CTEDeclarationIndex          int
	CTEDepth                     int
	CTERecursive                 bool
	CTEForward                   bool
}

type FunctionRef struct {
	Catalog, Schema, Name string
	Span                  Span
	Arguments             []Expression
	Filter                Expression
	Orders                []Order
	Window                bool
}

type ColumnRef struct {
	Names []string
	Span  Span
}

type CTERef struct {
	Name             string
	DeclarationIndex int
	Depth            int
	Recursive        bool
}

type Analysis struct {
	Relations []RelationRef
	Functions []FunctionRef
	Columns   []ColumnRef
	CTEs      []CTERef
}

type cteBinding struct {
	index     int
	depth     int
	recursive bool
}

type analysisWalker struct {
	result *Analysis
	depth  int
	scopes []map[string]cteBinding
	future []map[string]bool
}

func Analyze(query Query) (Analysis, error) {
	result := Analysis{}
	w := &analysisWalker{result: &result}
	for _, statement := range query.Statements {
		if err := w.statement(statement); err != nil {
			return Analysis{}, err
		}
	}
	return result, nil
}

func (w *analysisWalker) currentScope() map[string]cteBinding {
	if len(w.scopes) == 0 {
		return nil
	}
	return w.scopes[len(w.scopes)-1]
}

func (w *analysisWalker) pushScope(scope map[string]cteBinding) func() {
	w.scopes = append(w.scopes, scope)
	return func() { w.scopes = w.scopes[:len(w.scopes)-1] }
}

func cloneScope(scope map[string]cteBinding) map[string]cteBinding {
	result := make(map[string]cteBinding, len(scope))
	for name, binding := range scope {
		result[name] = binding
	}
	return result
}

func cteKey(name string) string { return strings.ToLower(name) }

func (w *analysisWalker) nested(statement Statement) error {
	w.depth++
	err := w.statement(statement)
	w.depth--
	return err
}

func (w *analysisWalker) statement(statement Statement) error {
	if statement == nil {
		return fmt.Errorf("duckdbsql: nil statement")
	}
	switch value := statement.(type) {
	case *SelectStatement:
		return w.selectStatement(value)
	case *SetOperationStatement:
		base := cloneScope(w.currentScope())
		done := w.pushScope(base)
		defer done()
		if err := w.ctes(value.CTEs); err != nil {
			return err
		}
		if err := w.modifiers(value.Modifiers); err != nil {
			return err
		}
		if err := w.nested(value.Left); err != nil {
			return err
		}
		if err := w.nested(value.Right); err != nil {
			return err
		}
		for _, child := range value.Children {
			if err := w.nested(child); err != nil {
				return err
			}
		}
		return nil
	case *RecursiveCTEStatement:
		doneBase := w.pushScope(cloneScope(w.currentScope()))
		if err := w.ctes(value.CTEs); err != nil {
			doneBase()
			return err
		}
		if err := w.modifiers(value.Modifiers); err != nil {
			doneBase()
			return err
		}
		index := len(w.result.CTEs)
		w.result.CTEs = append(w.result.CTEs, CTERef{Name: value.Name, DeclarationIndex: index, Depth: w.depth, Recursive: true})
		if err := w.nested(value.Left); err != nil {
			doneBase()
			return err
		}
		scope := cloneScope(w.currentScope())
		scope[cteKey(value.Name)] = cteBinding{index: index, depth: w.depth, recursive: true}
		done := w.pushScope(scope)
		err := w.nested(value.Right)
		done()
		if err != nil {
			doneBase()
			return err
		}
		for _, target := range value.KeyTargets {
			if err := w.expression(target); err != nil {
				doneBase()
				return err
			}
		}
		doneBase()
		return nil
	case *CTENodeStatement:
		doneBase := w.pushScope(cloneScope(w.currentScope()))
		if err := w.ctes(value.CTEs); err != nil {
			doneBase()
			return err
		}
		if err := w.modifiers(value.Modifiers); err != nil {
			doneBase()
			return err
		}
		index := len(w.result.CTEs)
		w.result.CTEs = append(w.result.CTEs, CTERef{Name: value.Name, DeclarationIndex: index, Depth: w.depth})
		if err := w.nested(value.Query); err != nil {
			doneBase()
			return err
		}
		scope := cloneScope(w.currentScope())
		scope[cteKey(value.Name)] = cteBinding{index: index, depth: w.depth}
		done := w.pushScope(scope)
		err := w.nested(value.Child)
		done()
		doneBase()
		return err
	default:
		return fmt.Errorf("duckdbsql: unknown statement type %T", statement)
	}
}

func (w *analysisWalker) selectStatement(value *SelectStatement) error {
	done := w.pushScope(cloneScope(w.currentScope()))
	defer done()
	if err := w.ctes(value.CTEs); err != nil {
		return err
	}
	if err := w.modifiers(value.Modifiers); err != nil {
		return err
	}
	for _, expression := range value.SelectList {
		if err := w.expression(expression); err != nil {
			return err
		}
	}
	if err := w.relation(value.From); err != nil {
		return err
	}
	for _, expression := range []Expression{value.Where, value.Having, value.Qualify} {
		if err := w.expression(expression); err != nil {
			return err
		}
	}
	for _, expression := range value.GroupExpressions {
		if err := w.expression(expression); err != nil {
			return err
		}
	}
	return nil
}

func (w *analysisWalker) ctes(ctes []CTE) error {
	future := make(map[string]bool, len(ctes))
	for _, cte := range ctes {
		future[cteKey(cte.Name)] = true
	}
	w.future = append(w.future, future)
	defer func() { w.future = w.future[:len(w.future)-1] }()
	for i, cte := range ctes {
		index := len(w.result.CTEs)
		w.result.CTEs = append(w.result.CTEs, CTERef{Name: cte.Name, DeclarationIndex: index, Depth: w.depth})
		delete(future, cteKey(cte.Name))
		if err := w.nested(cte.Query); err != nil {
			return err
		}
		for _, expression := range cte.KeyTargets {
			if err := w.expression(expression); err != nil {
				return err
			}
		}
		w.currentScope()[cteKey(cte.Name)] = cteBinding{index: index, depth: w.depth}
		_ = i
	}
	return nil
}

func (w *analysisWalker) modifiers(modifiers []Modifier) error {
	for _, modifier := range modifiers {
		if modifier == nil {
			return fmt.Errorf("duckdbsql: nil modifier")
		}
		switch value := modifier.(type) {
		case *DistinctModifier:
			for _, expression := range value.DistinctOnTargets {
				if err := w.expression(expression); err != nil {
					return err
				}
			}
		case *OrderModifier:
			for _, order := range value.Orders {
				if err := w.expression(order.Expression); err != nil {
					return err
				}
			}
		case *LimitModifier:
			if err := w.expression(value.Limit); err != nil {
				return err
			}
			if err := w.expression(value.Offset); err != nil {
				return err
			}
		case *LimitPercentModifier:
			if err := w.expression(value.Limit); err != nil {
				return err
			}
			if err := w.expression(value.Offset); err != nil {
				return err
			}
		default:
			return fmt.Errorf("duckdbsql: unknown modifier type %T", modifier)
		}
	}
	return nil
}

func (w *analysisWalker) relation(relation Relation) error {
	if relation == nil {
		return nil
	}
	switch value := relation.(type) {
	case *BaseTableRelation:
		ref := RelationRef{Kind: RelationBase, Catalog: value.Catalog, Schema: value.Schema, Name: value.Name, Alias: value.NodeMeta.Alias, Span: value.NodeMeta.Span, CTEDeclarationIndex: -1}
		if value.Schema == "" {
			if binding, ok := w.currentScope()[cteKey(value.Name)]; ok {
				ref.Kind, ref.CTE = RelationCTE, true
				ref.CTEDeclarationIndex, ref.CTEDepth, ref.CTERecursive = binding.index, binding.depth, binding.recursive
			} else if w.isFuture(value.Name) {
				ref.CTEForward = true
			}
		}
		w.result.Relations = append(w.result.Relations, ref)
		if value.At != nil {
			return w.expression(value.At.Expression)
		}
	case *EmptyRelation, *ColumnDataRelation:
		return nil
	case *SubqueryRelation:
		w.result.Relations = append(w.result.Relations, RelationRef{Kind: RelationSubquery, Alias: value.NodeMeta.Alias, Span: value.NodeMeta.Span})
		return w.nested(value.Query)
	case *TableFunctionRelation:
		w.result.Relations = append(w.result.Relations, RelationRef{Kind: RelationTableFunction, Alias: value.NodeMeta.Alias, Span: value.NodeMeta.Span})
		return w.expression(value.Function)
	case *JoinRelation:
		if err := w.relation(value.Left); err != nil {
			return err
		}
		if err := w.relation(value.Right); err != nil {
			return err
		}
		if err := w.expression(value.Condition); err != nil {
			return err
		}
		for _, expression := range value.DuplicateEliminatedColumns {
			if err := w.expression(expression); err != nil {
				return err
			}
		}
		return w.expression(value.RankingExpression)
	case *ExpressionListRelation:
		for _, row := range value.Values {
			for _, expression := range row {
				if err := w.expression(expression); err != nil {
					return err
				}
			}
		}
	case *PivotRelation:
		if err := w.relation(value.Source); err != nil {
			return err
		}
		for _, expression := range value.Aggregates {
			if err := w.expression(expression); err != nil {
				return err
			}
		}
		for _, pivot := range value.Pivots {
			for _, expression := range pivot.PivotExpressions {
				if err := w.expression(expression); err != nil {
					return err
				}
			}
			for _, entry := range pivot.Entries {
				if err := w.expression(entry.StarExpression); err != nil {
					return err
				}
			}
		}
	case *ShowRelation:
		return w.nested(value.Query)
	default:
		return fmt.Errorf("duckdbsql: unknown relation type %T", relation)
	}
	return nil
}

func (w *analysisWalker) expression(expression Expression) error {
	if expression == nil {
		return nil
	}
	switch value := expression.(type) {
	case *ConstantExpression, *DefaultExpression, *LambdaRefExpression, *ParameterExpression, *PositionalReferenceExpression:
		return nil
	case *ColumnExpression:
		w.result.Columns = append(w.result.Columns, ColumnRef{Names: append([]string(nil), value.Names...), Span: value.Span})
	case *FunctionExpression:
		w.result.Functions = append(w.result.Functions, FunctionRef{Catalog: value.Catalog, Schema: value.Schema, Name: value.Name, Span: value.Span, Arguments: value.Children, Filter: value.Filter, Orders: value.OrderBys})
		for _, child := range value.Children {
			if err := w.expression(child); err != nil {
				return err
			}
		}
		if err := w.expression(value.Filter); err != nil {
			return err
		}
		for _, order := range value.OrderBys {
			if err := w.expression(order.Expression); err != nil {
				return err
			}
		}
		return nil
	case *WindowExpression:
		filter := value.Filter
		if value.FilterExpression != nil {
			filter = value.FilterExpression
		}
		w.result.Functions = append(w.result.Functions, FunctionRef{Name: value.FunctionName, Schema: value.Schema, Catalog: value.Catalog, Span: value.Span, Arguments: value.Children, Filter: filter, Orders: value.Orders, Window: true})
		for _, child := range value.Children {
			if err := w.expression(child); err != nil {
				return err
			}
		}
		for _, child := range value.Partitions {
			if err := w.expression(child); err != nil {
				return err
			}
		}
		for _, order := range value.Orders {
			if err := w.expression(order.Expression); err != nil {
				return err
			}
		}
		for _, order := range value.ArgOrders {
			if err := w.expression(order.Expression); err != nil {
				return err
			}
		}
		for _, child := range []Expression{value.Filter, value.FilterExpression, value.StartExpression, value.EndExpression, value.OffsetExpression, value.DefaultExpression} {
			if child == value.Filter && value.FilterExpression != nil {
				continue
			}
			if err := w.expression(child); err != nil {
				return err
			}
		}
		return nil
	case *SubqueryExpression:
		if err := w.nested(value.Query); err != nil {
			return err
		}
		return w.expression(value.Child)
	case *StarExpression:
		if err := w.expression(value.Expression); err != nil {
			return err
		}
		for _, named := range append(value.ReplaceList, value.RenameList...) {
			if err := w.expression(named.Expression); err != nil {
				return err
			}
		}
	case *OperatorExpression:
		for _, child := range value.Children {
			if err := w.expression(child); err != nil {
				return err
			}
		}
	case *ConjunctionExpression:
		for _, child := range value.Children {
			if err := w.expression(child); err != nil {
				return err
			}
		}
	case *ComparisonExpression:
		if err := w.expression(value.Left); err != nil {
			return err
		}
		return w.expression(value.Right)
	case *CastExpression:
		return w.expression(value.Child)
	case *CaseExpression:
		for _, check := range value.Checks {
			if err := w.expression(check.When); err != nil {
				return err
			}
			if err := w.expression(check.Then); err != nil {
				return err
			}
		}
		return w.expression(value.Else)
	case *BetweenExpression:
		if err := w.expression(value.Input); err != nil {
			return err
		}
		if err := w.expression(value.Lower); err != nil {
			return err
		}
		return w.expression(value.Upper)
	case *CollateExpression:
		return w.expression(value.Child)
	case *LambdaExpression:
		if err := w.expression(value.LHS); err != nil {
			return err
		}
		return w.expression(value.Expr)
	case *TypeExpression:
		for _, child := range value.Children {
			if err := w.expression(child); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("duckdbsql: unknown expression type %T", expression)
	}
	return nil
}

func (w *analysisWalker) isFuture(name string) bool {
	name = cteKey(name)
	for i := len(w.future) - 1; i >= 0; i-- {
		if w.future[i][name] {
			return true
		}
	}
	return false
}
