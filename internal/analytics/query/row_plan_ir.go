package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// buildFlatPlanIR lowers row/raw/count reads into the same closed PlanIR
// source/filter/projection boundary as aggregate reads. Raw SQL builders may
// remain equivalence oracles in tests, but are not production execution paths.
func (p *Planner) buildFlatPlanIR(fact string, dimensions, metrics []Field, filters []Filter, sorts []Sort, limit, offset int) (*planir.Graph, error) {
	if fact == "" {
		return nil, fmt.Errorf("plan IR fact is required")
	}
	fields := []planir.Field{}
	lineage := []planir.PhysicalLineage{}
	paths := map[string][]semanticmodel.Relationship{}
	addRef := func(ref string) error {
		if ref == "" {
			return nil
		}
		physical, path, err := p.flatPhysicalField(fact, ref)
		if err != nil {
			return err
		}
		fields = appendPlanIRField(fields, planir.Field{Name: ref, Type: physical.Type})
		fields = appendPlanIRField(fields, planir.Field{Name: physical.Field, Type: physical.Type})
		paths[ref] = path
		lineage = append(lineage, planir.PhysicalLineage{Logical: ref, Dataset: physical.Table, Field: physical.Field, Route: flatRouteNames(path)})
		return nil
	}
	for _, field := range dimensions {
		if err := addRef(field.Field); err != nil {
			return nil, err
		}
	}
	for _, field := range metrics {
		metric, ok := p.compiled.metric(field.Field)
		if !ok || metric.Type != "aggregate" {
			return nil, fmt.Errorf("metric %q is not a raw aggregate", field.Field)
		}
		if err := addRef(metric.InputField); err != nil {
			return nil, err
		}
		fields = appendPlanIRField(fields, planir.Field{Name: field.Field, Type: "decimal"})
		physical, path, _ := p.flatPhysicalField(fact, metric.InputField)
		lineage = append(lineage, planir.PhysicalLineage{Logical: field.Field, Dataset: physical.Table, Field: physical.Field, Route: flatRouteNames(path)})
	}
	if len(dimensions) == 0 && len(metrics) == 0 && len(fields) == 0 {
		if table, ok := p.model.Tables[fact]; ok {
			names := make([]string, 0, len(table.Dimensions))
			for name := range table.Dimensions {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) > 0 {
				fields = append(fields, planir.Field{Name: names[0], Type: table.Dimensions[names[0]].Type})
				lineage = append(lineage, planir.PhysicalLineage{Logical: names[0], Dataset: fact, Field: names[0]})
			}
		}
	}
	var walkFilters func([]Filter) error
	walkFilters = func(values []Filter) error {
		for _, filter := range values {
			if filter.Field != "" {
				if err := addRef(filter.Field); err != nil {
					return err
				}
			}
			for _, group := range filter.Groups {
				if err := walkFilters(group.Filters); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walkFilters(filters); err != nil {
		return nil, err
	}
	for _, field := range dimensions {
		if _, ok := paths[field.Field]; !ok {
			paths[field.Field] = nil
		}
	}

	graph := &planir.Graph{Nodes: map[string]planir.Node{}}
	relation, err := p.physicalTable(fact)
	if err != nil {
		return nil, err
	}
	routes := flatRoutes(paths, fact, p)
	meta := planir.NodeMeta{NodeID: "scan", AvailableFields: fields, RootDatasets: []string{fact}, FilterPhase: planir.FilterPhaseScan, PhysicalLineage: lineage, RelationshipRoutes: routes}
	graph.Nodes["scan"] = planir.ScanDataset{NodeMeta: meta, Dataset: fact, Relation: relation}
	graph.Roots = []string{"scan"}
	input := "scan"
	root, relationship := flatFilterPhases(filters, paths)
	if len(root) > 0 {
		predicate, err := flatAndPredicate(root)
		if err != nil {
			return nil, err
		}
		m := meta
		m.NodeID = "filter_scan"
		m.PhysicalLineage = append([]planir.PhysicalLineage(nil), lineage...)
		graph.Nodes[m.NodeID] = planir.FilterRows{NodeMeta: m, Input: input, Predicate: predicate, Source: planir.FilterSourceRequest, Fields: predicateFields(predicate), MatchGuard: flatFilterNeedsMatchGuard(root), FieldRoutes: flatFilterFieldRoutes(root, paths, fact, p)}
		input = m.NodeID
	}
	ordered := flatOrderedRelationships(paths, fact)
	for index, path := range ordered {
		path.FromRelation, _ = p.physicalTable(path.FromDataset)
		path.ToRelation, _ = p.physicalTable(path.ToDataset)
		m := meta
		m.NodeID = fmt.Sprintf("traverse_%d", index)
		m.FilterPhase = planir.FilterPhaseRelationship
		graph.Nodes[m.NodeID] = planir.TraverseRelationship{NodeMeta: m, Input: input, Path: path}
		input = m.NodeID
	}
	if len(relationship) > 0 {
		predicate, err := flatAndPredicate(relationship)
		if err != nil {
			return nil, err
		}
		m := meta
		m.NodeID = "filter_relationship"
		m.FilterPhase = planir.FilterPhaseRelationship
		graph.Nodes[m.NodeID] = planir.FilterRows{NodeMeta: m, Input: input, Predicate: predicate, Source: planir.FilterSourceRequest, Fields: predicateFields(predicate), MatchGuard: flatFilterNeedsMatchGuard(relationship), FieldRoutes: flatFilterFieldRoutes(relationship, paths, fact, p)}
		input = m.NodeID
	}
	projection := []planir.Projection{}
	for _, field := range dimensions {
		alias, err := outputAlias(field)
		if err != nil {
			return nil, err
		}
		projection = append(projection, planir.Projection{Name: alias, Source: field.Field})
	}
	for _, field := range metrics {
		alias, err := outputAlias(field)
		if err != nil {
			return nil, err
		}
		projection = append(projection, planir.Projection{Name: alias, Source: field.Field})
	}
	if len(projection) == 0 {
		countMeta := meta
		countMeta.NodeID = "count"
		countMeta.OutputGrain = planir.Grain{}
		countMeta.AvailableFields = nil
		countMeta.AvailableMetrics = []planir.Metric{{Name: "value", Type: "integer"}}
		countMeta.FilterPhase = planir.FilterPhaseAggregate
		countMeta.PhysicalLineage = nil
		graph.Nodes[countMeta.NodeID] = planir.AggregateMetrics{NodeMeta: countMeta, Input: input, Metrics: []planir.MetricSpec{{Name: "value", Type: "integer", Aggregation: "COUNT_STAR"}}}
		input = countMeta.NodeID
		projection = append(projection, planir.Projection{Name: "value", Source: "value"})
	}
	available := []planir.Field{}
	for _, item := range projection {
		available = append(available, planir.Field{Name: item.Name})
	}
	sortKeys := []planir.SortKey{}
	for _, item := range sorts {
		alias, err := outputAlias(Field{Field: item.Field})
		if err != nil {
			return nil, err
		}
		sortKeys = append(sortKeys, planir.SortKey{Field: alias, Descending: strings.EqualFold(item.Direction, "desc")})
	}
	outMeta := meta
	outMeta.NodeID = "sort_limit"
	outMeta.FilterPhase = planir.FilterPhasePostAggregate
	outMeta.AvailableFields = available
	outMeta.AvailableMetrics = nil
	outMeta.PhysicalLineage = nil
	outMeta.RelationshipRoutes = nil
	graph.Nodes[outMeta.NodeID] = planir.SortLimit{NodeMeta: outMeta, Input: input, Sort: sortKeys, Projection: projection, Limit: limit, Offset: offset}
	graph.Output = outMeta.NodeID
	graph.NodeMeta = outMeta
	if err := graph.Validate(); err != nil {
		return nil, err
	}
	return graph, nil
}

func (p *Planner) flatPhysicalField(fact, ref string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	if semantic, ok := p.model.Dimensions[ref]; ok {
		binding, ok := semantic.Bindings[fact]
		if !ok {
			return semanticmodel.MetricDimension{}, nil, fmt.Errorf("semantic dimension %q has no binding for fact %q", ref, fact)
		}
		physical, err := p.model.ResolveDimension(binding.Field)
		if err != nil {
			return semanticmodel.MetricDimension{}, nil, err
		}
		path, err := p.model.ResolveBindingPath(fact, binding)
		return physical, path, err
	}
	physical, err := p.model.ResolveDimension(ref)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, err
	}
	path, err := p.relationshipPath(fact, physical.Table)
	return physical, path, err
}

func flatRouteNames(path []semanticmodel.Relationship) []string {
	out := make([]string, len(path))
	for i, item := range path {
		out[i] = item.ID
	}
	return out
}
func flatRoutes(paths map[string][]semanticmodel.Relationship, fact string, p *Planner) []planir.RelationshipRoute {
	seen := map[string]bool{}
	out := []planir.RelationshipRoute{}
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		edges := []planir.RelationshipPath{}
		for _, rel := range path {
			edge := planIRRelationshipPath(rel)
			edge.FromRelation, _ = p.physicalTable(edge.FromDataset)
			edge.ToRelation, _ = p.physicalTable(edge.ToDataset)
			edges = append(edges, edge)
		}
		key := strings.Join(flatRouteNames(path), "/")
		if !seen[key] {
			seen[key] = true
			out = append(out, planir.RelationshipRoute{RootDataset: fact, Edges: edges})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(flatRouteNamesSemantic(out[i]), "/") < strings.Join(flatRouteNamesSemantic(out[j]), "/")
	})
	return out
}
func flatRouteNamesSemantic(route planir.RelationshipRoute) []string {
	out := make([]string, len(route.Edges))
	for i, e := range route.Edges {
		out[i] = e.Name
	}
	return out
}
func flatOrderedRelationships(paths map[string][]semanticmodel.Relationship, fact string) []planir.RelationshipPath {
	_ = fact
	byID := map[string]planir.RelationshipPath{}
	routes := make([][]planir.RelationshipPath, 0, len(paths))
	for _, path := range paths {
		converted := make([]planir.RelationshipPath, 0, len(path))
		for _, rel := range path {
			edge := planIRRelationshipPath(rel)
			byID[planIRRelationshipSequenceSignature([]planir.RelationshipPath{edge})] = edge
			converted = append(converted, edge)
		}
		if len(converted) > 0 {
			routes = append(routes, converted)
		}
	}
	return orderedPlanIRRelationships(byID, routes)
}
func flatFilterPhases(filters []Filter, paths map[string][]semanticmodel.Relationship) (root, relationship []Filter) {
	for _, filter := range filters {
		path := paths[filter.Field]
		if len(path) > 0 {
			relationship = append(relationship, filter)
		} else {
			root = append(root, filter)
		}
	}
	return
}

func flatFilterNeedsMatchGuard(filters []Filter) bool {
	for _, filter := range filters {
		if filter.MatchGuard || filter.RequireMatch {
			return true
		}
		for _, group := range filter.Groups {
			if flatFilterNeedsMatchGuard(group.Filters) {
				return true
			}
		}
	}
	return false
}

func flatFilterFieldRoutes(filters []Filter, paths map[string][]semanticmodel.Relationship, fact string, p *Planner) map[string][]planir.RelationshipRoute {
	out := map[string][]planir.RelationshipRoute{}
	var walk func([]Filter)
	walk = func(values []Filter) {
		for _, filter := range values {
			if filter.Field != "" {
				path := paths[filter.Field]
				if len(path) > 0 {
					edges := make([]planir.RelationshipPath, 0, len(path))
					for _, relation := range path {
						edge := planIRRelationshipPath(relation)
						edge.FromRelation, _ = p.physicalTable(edge.FromDataset)
						edge.ToRelation, _ = p.physicalTable(edge.ToDataset)
						edges = append(edges, edge)
					}
					out[filter.Field] = []planir.RelationshipRoute{{RootDataset: fact, Edges: edges}}
				}
			}
			for _, group := range filter.Groups {
				walk(group.Filters)
			}
		}
	}
	walk(filters)
	return out
}
func flatAndPredicate(filters []Filter) (planir.Predicate, error) {
	children := make([]planir.Predicate, 0, len(filters))
	for _, filter := range filters {
		predicate, err := planIRPredicate(filter)
		if err != nil {
			return planir.Predicate{}, err
		}
		children = append(children, predicate)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return planir.Predicate{Kind: planir.PredicateAnd, Children: children}, nil
}
