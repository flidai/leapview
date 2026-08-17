package query

import (
	"fmt"
	"math"
	"sort"
)

// ProjectScalarFromGrouped resolves a grouped result against an already
// activated planner. Request paths should use this variant so compilation is
// performed once at serving-state activation, not once per projection.
func (p *Planner) ProjectScalarFromGrouped(grouped, scalar Request, rows Rows, complete bool) (Rows, bool, error) {
	if p == nil || p.compiled == nil || !complete || grouped.Offset != 0 || scalar.Offset != 0 {
		return nil, false, nil
	}
	if len(grouped.Dimensions) == 0 && grouped.Time.Field == "" {
		return nil, false, nil
	}
	if len(scalar.Dimensions) != 0 || scalar.Time.Field != "" || len(scalar.Metrics) != 1 {
		return nil, false, nil
	}
	if grouped.Table != scalar.Table {
		return nil, false, nil
	}
	groupedScope, scalarScope := grouped.Table, scalar.Table
	var err error
	if len(grouped.Filters) != 0 || len(scalar.Filters) != 0 {
		groupedResolved, resolveErr := p.resolveAggregate(grouped)
		if resolveErr != nil {
			return nil, false, nil
		}
		scalarResolved, resolveErr := p.resolveAggregate(scalar)
		if resolveErr != nil {
			return nil, false, nil
		}
		groupedScope, err = p.bundleScopeFingerprint(grouped, groupedResolved)
		if err != nil {
			return nil, false, nil
		}
		scalarScope, err = p.bundleScopeFingerprint(scalar, scalarResolved)
		if err != nil {
			return nil, false, nil
		}
	}
	groupedMasks, err := columnMaskFingerprint(grouped.ColumnMasks)
	if err != nil {
		return nil, false, nil
	}
	scalarMasks, err := columnMaskFingerprint(scalar.ColumnMasks)
	if err != nil {
		return nil, false, nil
	}
	if groupedScope != scalarScope || groupedMasks != scalarMasks {
		return nil, false, nil
	}

	target := scalar.Metrics[0]
	dependencyNames, ok, err := p.AdditiveMetricDependencies(target.Field)
	if err != nil || !ok {
		return nil, ok, err
	}
	dependencies := make(map[string]struct{}, len(dependencyNames))
	for _, dependency := range dependencyNames {
		dependencies[dependency] = struct{}{}
	}
	aliases := make(map[string]string, len(grouped.Metrics))
	for _, member := range grouped.Metrics {
		node, atomic := p.compiled.metric(member.Field)
		if !atomic || node.Type != "aggregate" {
			continue
		}
		if member.Alias == "" {
			return nil, false, nil
		}
		if existing, exists := aliases[member.Field]; exists && existing != member.Alias {
			return nil, false, nil
		}
		aliases[member.Field] = member.Alias
	}
	for dependency := range dependencies {
		if aliases[dependency] == "" {
			return nil, false, nil
		}
	}

	values := make(map[string]any, len(dependencies))
	for dependency := range dependencies {
		node, exists := p.compiled.metric(dependency)
		if !exists || node.Type != "aggregate" {
			return nil, false, fmt.Errorf("unknown additive aggregate metric %q", dependency)
		}
		value, err := recombineAdditive(rows, aliases[dependency], node.Empty)
		if err != nil {
			return nil, false, err
		}
		values[dependency] = value
	}
	value, err := evaluateCompiledAggregateMember(p.compiled, target.Field, values, map[string]bool{})
	if err != nil {
		return nil, false, err
	}
	alias := target.Alias
	if alias == "" {
		alias = target.Field
	}
	return Rows{{alias: value}}, true, nil
}

// AdditiveMetricDependencies expands a metric to atomic count/sum members.
// The boolean is false for any non-additive dependency.
func (p *Planner) AdditiveMetricDependencies(member string) ([]string, bool, error) {
	if p == nil || p.compiled == nil {
		return nil, false, fmt.Errorf("planner is not compiled")
	}
	dependencies := map[string]struct{}{}
	visiting := map[string]bool{}
	var visit func(string) (bool, error)
	visit = func(name string) (bool, error) {
		node, ok := p.compiled.metric(name)
		if ok && node.Type == "aggregate" {
			if node.Aggregation != "count" && node.Aggregation != "sum" {
				return false, nil
			}
			dependencies[name] = struct{}{}
			return true, nil
		}
		if !ok {
			return false, fmt.Errorf("unknown aggregate member %q", name)
		}
		if visiting[name] {
			return false, fmt.Errorf("metric dependency cycle includes %q", name)
		}
		visiting[name] = true
		expression, err := p.metricExpression(name)
		if err != nil {
			return false, err
		}
		for _, ref := range expression.References() {
			additive, err := visit(ref)
			if err != nil || !additive {
				delete(visiting, name)
				return additive, err
			}
		}
		delete(visiting, name)
		return true, nil
	}
	ok, err := visit(member)
	if err != nil || !ok {
		return nil, ok, err
	}
	out := make([]string, 0, len(dependencies))
	for dependency := range dependencies {
		out = append(out, dependency)
	}
	sort.Strings(out)
	return out, true, nil
}

func recombineAdditive(rows Rows, alias, empty string) (any, error) {
	var integerTotal int64
	var floatTotal float64
	integer := true
	seen := false
	addInteger := func(value int64) error {
		if integer {
			if value > 0 && integerTotal > math.MaxInt64-value || value < 0 && integerTotal < math.MinInt64-value {
				return fmt.Errorf("grouped aggregate column %q overflows int64", alias)
			}
			integerTotal += value
		} else {
			floatTotal += float64(value)
		}
		return nil
	}
	for _, row := range rows {
		value, exists := row[alias]
		if !exists {
			return nil, fmt.Errorf("grouped aggregate row is missing additive column %q", alias)
		}
		if value == nil {
			continue
		}
		seen = true
		switch typed := value.(type) {
		case int:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case int8:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case int16:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case int32:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case int64:
			if err := addInteger(typed); err != nil {
				return nil, err
			}
		case uint:
			if uint64(typed) > math.MaxInt64 {
				return nil, fmt.Errorf("grouped aggregate column %q overflows int64", alias)
			}
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case uint8:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case uint16:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case uint32:
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case uint64:
			if typed > uint64(^uint64(0)>>1) {
				return nil, fmt.Errorf("grouped aggregate column %q overflows int64", alias)
			}
			if err := addInteger(int64(typed)); err != nil {
				return nil, err
			}
		case float32:
			if integer {
				floatTotal = float64(integerTotal)
				integer = false
			}
			floatTotal += float64(typed)
		case float64:
			if integer {
				floatTotal = float64(integerTotal)
				integer = false
			}
			floatTotal += typed
		case interface{ Float64() float64 }:
			if integer {
				floatTotal = float64(integerTotal)
				integer = false
			}
			floatTotal += typed.Float64()
		default:
			return nil, fmt.Errorf("grouped aggregate column %q has non-numeric value %T", alias, value)
		}
	}
	if !seen {
		if empty == "zero" {
			return int64(0), nil
		}
		return nil, nil
	}
	if integer {
		return integerTotal, nil
	}
	return floatTotal, nil
}

func evaluateCompiledAggregateMember(compiled *CompiledModel, member string, values map[string]any, visiting map[string]bool) (any, error) {
	node, ok := compiled.metric(member)
	if ok && node.Type == "aggregate" {
		return values[member], nil
	}
	if !ok {
		return nil, fmt.Errorf("unknown aggregate member %q", member)
	}
	if visiting[member] {
		return nil, fmt.Errorf("metric dependency cycle includes %q", member)
	}
	visiting[member] = true
	defer delete(visiting, member)
	return node.Expression.Evaluate(func(ref string) (any, error) {
		return evaluateCompiledAggregateMember(compiled, ref, values, visiting)
	})
}
