package modelsql

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/pkg/duckdbsql"
)

// A qualifierBinding describes the names visible to expressions in one query
// block. Governed relations without an authored alias are the only bindings
// that can provide a stable identity for a two-part column reference. All
// other bindings shield an outer scope, but deliberately remain symbolic.
type qualifierBinding struct {
	reference *resolvedSQLReference
	shield    bool
}

type qualifierScope struct {
	bindings map[string][]qualifierBinding
}

type qualifierResolver struct {
	refs   *sqlProjectionReferences
	scopes []qualifierScope
}

func resolveImplicitSQLQualifiers(query duckdbsql.Query, refs *sqlProjectionReferences) error {
	resolver := &qualifierResolver{refs: refs}
	for _, statement := range query.Statements {
		if err := resolver.statement(statement); err != nil {
			return err
		}
	}
	return nil
}

func (r *qualifierResolver) pushScope() func() {
	r.scopes = append(r.scopes, qualifierScope{bindings: make(map[string][]qualifierBinding)})
	return func() { r.scopes = r.scopes[:len(r.scopes)-1] }
}

func (r *qualifierResolver) addBinding(name string, binding qualifierBinding) {
	name = strings.ToLower(name)
	if name == "" || len(r.scopes) == 0 {
		return
	}
	current := &r.scopes[len(r.scopes)-1]
	current.bindings[name] = append(current.bindings[name], binding)
}

func (r *qualifierResolver) statement(statement duckdbsql.Statement) error {
	switch value := statement.(type) {
	case *duckdbsql.SelectStatement:
		return r.selectStatement(value)
	case *duckdbsql.SetOperationStatement:
		return r.setOperation(value)
	default:
		// validateSQLProjection rejects all other statement kinds before this
		// pass. Keep this guard so a future admitted node cannot silently skip
		// scope resolution.
		return fmt.Errorf("unsupported SQL statement for qualifier resolution: %T", statement)
	}
}

func (r *qualifierResolver) setOperation(value *duckdbsql.SetOperationStatement) error {
	done := r.pushScope()
	defer done()
	if err := r.ctes(value.CTEs); err != nil {
		return err
	}
	if err := r.statement(value.Left); err != nil {
		return err
	}
	if err := r.statement(value.Right); err != nil {
		return err
	}
	for _, child := range value.Children {
		if err := r.statement(child); err != nil {
			return err
		}
	}
	// Set-operation modifiers are attached to the combined result, while
	// DuckDB represents their table-qualified expressions using branch
	// qualifiers. Expose branch qualifiers and deduplicate equivalent stable
	// bindings; conflicting bindings remain ambiguous and fail closed.
	for _, branch := range append([]duckdbsql.Statement{value.Left, value.Right}, value.Children...) {
		for name, bindings := range r.exposedBindings(branch) {
			r.addSetBindings(name, bindings)
		}
	}
	for _, modifier := range value.Modifiers {
		if err := r.modifier(modifier); err != nil {
			return err
		}
	}
	return nil
}

func (r *qualifierResolver) addSetBinding(name string, binding qualifierBinding) {
	name = strings.ToLower(name)
	if name == "" || len(r.scopes) == 0 {
		return
	}
	current := &r.scopes[len(r.scopes)-1]
	for _, existing := range current.bindings[name] {
		if qualifierBindingsEquivalent(existing, binding) {
			return
		}
	}
	current.bindings[name] = append(current.bindings[name], binding)
}

func (r *qualifierResolver) addSetBindings(name string, bindings []qualifierBinding) {
	// Multiple matches from one branch are already ambiguous in that branch;
	// retain every one so the set-level modifier fails closed as well. A
	// single match may be deduplicated when the same qualifier is exposed by
	// another UNION branch with the same stable identity.
	if len(bindings) != 1 {
		if len(r.scopes) == 0 {
			return
		}
		name = strings.ToLower(name)
		current := &r.scopes[len(r.scopes)-1]
		current.bindings[name] = append(current.bindings[name], bindings...)
		return
	}
	r.addSetBinding(name, bindings[0])
}

func qualifierBindingsEquivalent(left, right qualifierBinding) bool {
	if left.shield || right.shield {
		return left.shield && right.shield && left.reference == nil && right.reference == nil
	}
	return left.reference != nil && right.reference != nil && left.reference.ID == right.reference.ID && left.reference.Kind == right.reference.Kind
}

func (r *qualifierResolver) exposedBindings(statement duckdbsql.Statement) map[string][]qualifierBinding {
	result := make(map[string][]qualifierBinding)
	add := func(name string, binding qualifierBinding) {
		name = strings.ToLower(name)
		if name == "" {
			return
		}
		result[name] = append(result[name], binding)
	}
	var relation func(duckdbsql.Relation)
	relation = func(value duckdbsql.Relation) {
		if value == nil {
			return
		}
		switch typed := value.(type) {
		case *duckdbsql.BaseTableRelation:
			if typed.Meta().Alias != "" {
				add(typed.Meta().Alias, qualifierBinding{shield: true})
			} else if reference, ok := r.refs.relations[typed]; ok {
				copy := reference
				add(typed.Name, qualifierBinding{reference: &copy})
			} else {
				add(typed.Name, qualifierBinding{shield: true})
			}
		case *duckdbsql.SubqueryRelation:
			if typed.Meta().Alias != "" {
				add(typed.Meta().Alias, qualifierBinding{shield: true})
			}
		case *duckdbsql.JoinRelation:
			if typed.Meta().Alias != "" {
				add(typed.Meta().Alias, qualifierBinding{shield: true})
				return
			}
			relation(typed.Left)
			relation(typed.Right)
		case *duckdbsql.ExpressionListRelation:
			if typed.Meta().Alias != "" {
				add(typed.Meta().Alias, qualifierBinding{shield: true})
			}
		}
	}
	var collect func(duckdbsql.Statement)
	collect = func(value duckdbsql.Statement) {
		switch typed := value.(type) {
		case *duckdbsql.SelectStatement:
			relation(typed.From)
		case *duckdbsql.SetOperationStatement:
			for _, branch := range append([]duckdbsql.Statement{typed.Left, typed.Right}, typed.Children...) {
				branchBindings := (&qualifierResolver{refs: r.refs}).exposedBindings(branch)
				mergeExposedBindings(result, branchBindings)
			}
		}
	}
	collect(statement)
	return result
}

func mergeExposedBindings(target, branch map[string][]qualifierBinding) {
	for name, bindings := range branch {
		if len(bindings) != 1 {
			target[name] = append(target[name], bindings...)
			continue
		}
		binding := bindings[0]
		duplicate := false
		for _, existing := range target[name] {
			if qualifierBindingsEquivalent(existing, binding) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			target[name] = append(target[name], binding)
		}
	}
}

func (r *qualifierResolver) selectStatement(value *duckdbsql.SelectStatement) error {
	done := r.pushScope()
	defer done()
	if err := r.ctes(value.CTEs); err != nil {
		return err
	}
	// Relation bindings must be present before any select expression is
	// visited. relation() also descends into nested FROM subqueries while the
	// preceding sibling relations are visible, which admits correlated refs
	// without leaking later sibling bindings into an inner query.
	if err := r.relation(value.From); err != nil {
		return err
	}
	for _, modifier := range value.Modifiers {
		if err := r.modifier(modifier); err != nil {
			return err
		}
	}
	for _, expression := range value.SelectList {
		if err := r.expression(expression); err != nil {
			return err
		}
	}
	for _, expression := range []duckdbsql.Expression{value.Where, value.Having, value.Qualify} {
		if err := r.expression(expression); err != nil {
			return err
		}
	}
	for _, expression := range value.GroupExpressions {
		if err := r.expression(expression); err != nil {
			return err
		}
	}
	return nil
}

func (r *qualifierResolver) ctes(values []duckdbsql.CTE) error {
	for _, value := range values {
		// A CTE declaration is not itself a relation binding: only a CTE
		// consumed by FROM can shield a correlated outer qualifier. The
		// relation walker adds that shield when it sees the CTE reference.
		if err := r.statement(value.Query); err != nil {
			return err
		}
		for _, target := range value.KeyTargets {
			if err := r.expression(target); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *qualifierResolver) modifier(value duckdbsql.Modifier) error {
	switch typed := value.(type) {
	case *duckdbsql.DistinctModifier:
		for _, expression := range typed.DistinctOnTargets {
			if err := r.expression(expression); err != nil {
				return err
			}
		}
	case *duckdbsql.OrderModifier:
		for _, order := range typed.Orders {
			if err := r.expression(order.Expression); err != nil {
				return err
			}
		}
	case *duckdbsql.LimitModifier:
		if err := r.expression(typed.Limit); err != nil {
			return err
		}
		return r.expression(typed.Offset)
	case *duckdbsql.LimitPercentModifier:
		if err := r.expression(typed.Limit); err != nil {
			return err
		}
		return r.expression(typed.Offset)
	default:
		return fmt.Errorf("unsupported SQL modifier for qualifier resolution: %T", value)
	}
	return nil
}

func (r *qualifierResolver) relation(value duckdbsql.Relation) error {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case *duckdbsql.BaseTableRelation:
		if typed.Meta().Alias != "" {
			// An explicit alias shields only that alias. The original table
			// name is not added, allowing a genuinely correlated outer
			// reference with the same spelling to resolve correctly.
			r.addBinding(typed.Meta().Alias, qualifierBinding{shield: true})
		} else if reference, ok := r.refs.relations[typed]; ok {
			r.addBinding(typed.Name, qualifierBinding{reference: &reference})
		} else {
			// CTEs and future non-governed relation forms remain symbolic and
			// shield an outer governed relation with the same qualifier.
			r.addBinding(typed.Name, qualifierBinding{shield: true})
		}
		if typed.At != nil {
			return r.expression(typed.At.Expression)
		}
		return nil
	case *duckdbsql.EmptyRelation:
		return nil
	case *duckdbsql.SubqueryRelation:
		// The derived-table alias is not visible inside its own query.
		if err := r.statement(typed.Query); err != nil {
			return err
		}
		if typed.Meta().Alias != "" {
			r.addBinding(typed.Meta().Alias, qualifierBinding{shield: true})
		}
		return nil
	case *duckdbsql.JoinRelation:
		if typed.Meta().Alias != "" {
			// An authored join alias exposes the join as one derived
			// relation. Keep child bindings in a temporary scope so ON and
			// ranking expressions can still resolve them, then shield those
			// names from the containing query block.
			done := r.pushScope()
			if err := r.relation(typed.Left); err != nil {
				done()
				return err
			}
			if err := r.relation(typed.Right); err != nil {
				done()
				return err
			}
			if err := r.expression(typed.Condition); err != nil {
				done()
				return err
			}
			for _, expression := range typed.DuplicateEliminatedColumns {
				if err := r.expression(expression); err != nil {
					done()
					return err
				}
			}
			if err := r.expression(typed.RankingExpression); err != nil {
				done()
				return err
			}
			done()
			r.addBinding(typed.Meta().Alias, qualifierBinding{shield: true})
			return nil
		}
		if err := r.relation(typed.Left); err != nil {
			return err
		}
		if err := r.relation(typed.Right); err != nil {
			return err
		}
		if err := r.expression(typed.Condition); err != nil {
			return err
		}
		for _, expression := range typed.DuplicateEliminatedColumns {
			if err := r.expression(expression); err != nil {
				return err
			}
		}
		if err := r.expression(typed.RankingExpression); err != nil {
			return err
		}
		return nil
	case *duckdbsql.ExpressionListRelation:
		for _, row := range typed.Values {
			for _, expression := range row {
				if err := r.expression(expression); err != nil {
					return err
				}
			}
		}
		if typed.Meta().Alias != "" {
			r.addBinding(typed.Meta().Alias, qualifierBinding{shield: true})
		}
		return nil
	default:
		return fmt.Errorf("unsupported SQL relation for qualifier resolution: %T", value)
	}
}

func (r *qualifierResolver) expression(value duckdbsql.Expression) error {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case *duckdbsql.ColumnExpression:
		return r.column(typed)
	case *duckdbsql.ConstantExpression, *duckdbsql.DefaultExpression, *duckdbsql.LambdaRefExpression,
		*duckdbsql.ParameterExpression, *duckdbsql.PositionalReferenceExpression:
		return nil
	case *duckdbsql.StarExpression:
		if err := r.expression(typed.Expression); err != nil {
			return err
		}
		for _, named := range append(append([]duckdbsql.NamedExpression{}, typed.ReplaceList...), typed.RenameList...) {
			if err := r.expression(named.Expression); err != nil {
				return err
			}
		}
		return nil
	case *duckdbsql.FunctionExpression:
		for _, child := range typed.Children {
			if err := r.expression(child); err != nil {
				return err
			}
		}
		if err := r.expression(typed.Filter); err != nil {
			return err
		}
		for _, order := range typed.OrderBys {
			if err := r.expression(order.Expression); err != nil {
				return err
			}
		}
		return nil
	case *duckdbsql.OperatorExpression:
		for _, child := range typed.Children {
			if err := r.expression(child); err != nil {
				return err
			}
		}
		return nil
	case *duckdbsql.ComparisonExpression:
		if err := r.expression(typed.Left); err != nil {
			return err
		}
		return r.expression(typed.Right)
	case *duckdbsql.ConjunctionExpression:
		for _, child := range typed.Children {
			if err := r.expression(child); err != nil {
				return err
			}
		}
		return nil
	case *duckdbsql.CastExpression:
		return r.expression(typed.Child)
	case *duckdbsql.CaseExpression:
		for _, check := range typed.Checks {
			if err := r.expression(check.When); err != nil {
				return err
			}
			if err := r.expression(check.Then); err != nil {
				return err
			}
		}
		return r.expression(typed.Else)
	case *duckdbsql.WindowExpression:
		for _, expression := range typed.Partitions {
			if err := r.expression(expression); err != nil {
				return err
			}
		}
		for _, order := range typed.Orders {
			if err := r.expression(order.Expression); err != nil {
				return err
			}
		}
		for _, child := range typed.Children {
			if err := r.expression(child); err != nil {
				return err
			}
		}
		filter := typed.Filter
		if filter == nil {
			filter = typed.FilterExpression
		}
		for _, expression := range []duckdbsql.Expression{filter, typed.StartExpression, typed.EndExpression, typed.OffsetExpression, typed.DefaultExpression} {
			if err := r.expression(expression); err != nil {
				return err
			}
		}
		for _, order := range typed.ArgOrders {
			if err := r.expression(order.Expression); err != nil {
				return err
			}
		}
		return nil
	case *duckdbsql.SubqueryExpression:
		if err := r.statement(typed.Query); err != nil {
			return err
		}
		return r.expression(typed.Child)
	case *duckdbsql.BetweenExpression:
		if err := r.expression(typed.Input); err != nil {
			return err
		}
		if err := r.expression(typed.Lower); err != nil {
			return err
		}
		return r.expression(typed.Upper)
	default:
		return fmt.Errorf("unsupported SQL expression for qualifier resolution: %T", value)
	}
}

func (r *qualifierResolver) column(value *duckdbsql.ColumnExpression) error {
	if len(value.Names) != 2 {
		return nil
	}
	name := strings.ToLower(value.Names[0])
	for index := len(r.scopes) - 1; index >= 0; index-- {
		bindings, ok := r.scopes[index].bindings[name]
		if !ok {
			continue
		}
		if len(bindings) > 1 {
			return fmt.Errorf("SQL column qualifier %q is ambiguous in its query scope", value.Names[0])
		}
		if bindings[0].shield {
			return nil
		}
		if bindings[0].reference != nil {
			r.refs.columns[value] = *bindings[0].reference
		}
		return nil
	}
	return nil
}
