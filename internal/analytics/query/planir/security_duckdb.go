package planir

import (
	"fmt"
	"sort"
	"strings"
)

func (r *duckRenderer) sourceSecurityBarrier(b SecurityBarrier) (sourceContext, error) {
	scan, _ := securityScan(r.graph.Nodes[b.Input])
	name := r.cteName(b.NodeID)
	if !r.done[b.NodeID] {
		datasets := make([]string, 0, len(b.Targets))
		for dataset := range b.Targets {
			datasets = append(datasets, dataset)
		}
		sort.Strings(datasets)
		for _, dataset := range datasets {
			if target, ok := securityBarrier(r.graph.Nodes[b.Targets[dataset]]); ok {
				if _, err := r.sourceSecurityBarrier(target); err != nil {
					return sourceContext{}, err
				}
			}
		}
		ctx, err := r.source(b.Input)
		if err != nil {
			return sourceContext{}, err
		}
		clauses := make([]string, 0, len(b.Predicates))
		for _, p := range b.Predicates {
			var clause string
			if route, ok := b.Routes[p.Field]; ok {
				clause, err = r.securityExists(b, p, route, ctx)
			} else {
				clause, err = renderPredicateWithResolver(p, &r.args, func(field string) (string, error) { return r.fieldExpr(field, ctx) })
			}
			if err != nil {
				return sourceContext{}, err
			}
			clauses = append(clauses, clause)
		}
		sql := "SELECT * FROM " + ctx.from
		if len(clauses) > 0 {
			sql += " WHERE " + strings.Join(clauses, " AND ")
		}
		r.ctes = append(r.ctes, name+" AS MATERIALIZED ("+sql+")")
		r.done[b.NodeID] = true
	}
	return sourceContext{from: name + " AS " + quoteName(scan.Dataset), root: scan.Dataset, aliases: map[string][]string{scan.Dataset: {scan.Dataset}}, pathAliases: map[string]string{"": scan.Dataset}, lineage: append([]PhysicalLineage(nil), b.PhysicalLineage...)}, nil
}

func (r *duckRenderer) securityExists(b SecurityBarrier, p Predicate, route RelationshipRoute, ctx sourceContext) (string, error) {
	from := ""
	conditions := []string{}
	previous := ctx.latestAlias(route.RootDataset)
	if previous == "" {
		previous = route.RootDataset
	}
	for i, edge := range route.Edges {
		alias := fmt.Sprintf("security_route_%d", i)
		target := r.graph.Nodes[b.Targets[edge.ToDataset]]
		relation := ""
		if barrier, ok := securityBarrier(target); ok {
			relation = r.cteName(barrier.NodeID)
		} else if scan, ok := securityScan(target); ok {
			relation = scan.Relation
			if relation == "" {
				relation = quoteName(scan.Dataset)
			}
		} else {
			return "", fmt.Errorf("missing governed security route target")
		}
		keys := []string{}
		for _, key := range edge.JoinKeys {
			if err := validName(key.From); err != nil {
				return "", err
			}
			if err := validName(key.To); err != nil {
				return "", err
			}
			keys = append(keys, quoteName(previous)+"."+quoteName(columnName(key.From))+" = "+quoteName(alias)+"."+quoteName(columnName(key.To)))
		}
		if i == 0 {
			from = relation + " AS " + quoteName(alias)
			conditions = append(conditions, keys...)
		} else {
			from += " JOIN " + relation + " AS " + quoteName(alias) + " ON " + strings.Join(keys, " AND ")
		}
		previous = alias
	}
	predicate, err := renderPredicateWithResolver(p, &r.args, func(field string) (string, error) {
		if err := validName(field); err != nil {
			return "", err
		}
		return quoteName(previous) + "." + quoteName(columnName(field)), nil
	})
	if err != nil {
		return "", err
	}
	conditions = append(conditions, predicate)
	return "EXISTS (SELECT 1 FROM " + from + " WHERE " + strings.Join(conditions, " AND ") + ")", nil
}
