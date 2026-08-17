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
func (p *Planner) buildFlatPlanIR(dataset string, dimensions, metrics []Field, filters []Filter, sorts []Sort, limit, offset int) (*planir.Graph, error) {
	if dataset == "" {
		return nil, fmt.Errorf("plan IR dataset is required")
	}
	fields := []planir.Field{}
	lineage := []planir.PhysicalLineage{}
	paths := map[string][]semanticmodel.Relationship{}
	addRef := func(ref string) error {
		if ref == "" {
			return nil
		}
		physical, path, err := p.flatPhysicalField(dataset, ref)
		if err != nil {
			return err
		}
		fieldType := planIRLogicalType(physical.Datatype, physical.Type)
		fields = appendPlanIRField(fields, planir.Field{Name: ref, Type: fieldType})
		fields = appendPlanIRField(fields, planir.Field{Name: physical.Field, Type: fieldType})
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
		if !ok || metric.Aggregate == nil {
			return nil, fmt.Errorf("metric %q is not a raw aggregate", field.Field)
		}
		if err := addRef(metric.Aggregate.InputField); err != nil {
			return nil, err
		}
		metricType := "decimal"
		aggregation := strings.ToUpper(metric.Aggregate.Aggregation)
		if aggregation == "COUNT" || aggregation == "COUNT_DISTINCT" || aggregation == "COUNT_STAR" || aggregation == "COUNT_DISTINCT_PAIR" {
			metricType = "integer"
		} else if physical, _, resolveErr := p.flatPhysicalField(dataset, metric.Aggregate.InputField); resolveErr == nil {
			metricType = planIRLogicalType(physical.Datatype, physical.Type)
			if aggregation == "AVG" && metricType == "integer" {
				metricType = "decimal"
			}
		}
		fields = appendPlanIRField(fields, planir.Field{Name: field.Field, Type: metricType})
		physical, path, _ := p.flatPhysicalField(dataset, metric.Aggregate.InputField)
		lineage = append(lineage, planir.PhysicalLineage{Logical: field.Field, Dataset: physical.Table, Field: physical.Field, Route: flatRouteNames(path)})
	}
	if len(dimensions) == 0 && len(metrics) == 0 && len(fields) == 0 {
		if table, ok := p.datasetTable(dataset); ok {
			names := make([]string, 0, len(table.Dimensions))
			for name := range table.Dimensions {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) > 0 {
				fields = append(fields, planir.Field{Name: names[0], Type: table.Dimensions[names[0]].Type})
				lineage = append(lineage, planir.PhysicalLineage{Logical: names[0], Dataset: dataset, Field: names[0]})
			}
		}
	}
	var walkFilters func([]Filter) error
	walkFilters = func(values []Filter) error {
		for _, filter := range values {
			if filter.Spatial != nil {
				if err := addRef(filter.Spatial.LatitudeField); err != nil {
					return err
				}
				if err := addRef(filter.Spatial.LongitudeField); err != nil {
					return err
				}
			}
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
	relation, err := p.physicalTable(dataset)
	if err != nil {
		return nil, err
	}
	routes, err := flatRoutes(paths, dataset, p)
	if err != nil {
		return nil, err
	}
	meta := planir.NodeMeta{NodeID: "scan", AvailableFields: fields, RootDatasets: []string{dataset}, FilterPhase: planir.FilterPhaseScan, PhysicalLineage: lineage, RelationshipRoutes: routes}
	graph.Nodes["scan"] = planir.ScanDataset{NodeMeta: meta, Dataset: dataset, Relation: relation}
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
		fieldRoutes, err := flatFilterFieldRoutes(root, paths, dataset, p)
		if err != nil {
			return nil, err
		}
		graph.Nodes[m.NodeID] = planir.FilterRows{NodeMeta: m, Input: input, Predicate: predicate, Source: planir.FilterSourceRequest, Fields: predicateFields(predicate), MatchGuard: flatFilterNeedsMatchGuard(root), FieldRoutes: fieldRoutes}
		input = m.NodeID
	}
	ordered := flatOrderedRelationships(paths, dataset)
	for index, path := range ordered {
		fromRelation, err := p.physicalTable(path.FromDataset)
		if err != nil {
			return nil, err
		}
		toRelation, err := p.physicalTable(path.ToDataset)
		if err != nil {
			return nil, err
		}
		path.FromRelation, path.ToRelation = fromRelation, toRelation
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
		fieldRoutes, err := flatFilterFieldRoutes(relationship, paths, dataset, p)
		if err != nil {
			return nil, err
		}
		graph.Nodes[m.NodeID] = planir.FilterRows{NodeMeta: m, Input: input, Predicate: predicate, Source: planir.FilterSourceRequest, Fields: predicateFields(predicate), MatchGuard: flatFilterNeedsMatchGuard(relationship), FieldRoutes: fieldRoutes}
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

func planIRLogicalType(datatype semanticmodel.LogicalDataType, fallback string) string {
	switch datatype {
	case semanticmodel.DataTypeInteger:
		return "integer"
	case semanticmodel.DataTypeDecimal:
		return "decimal"
	case semanticmodel.DataTypeFloat:
		return "float"
	default:
		return fallback
	}
}

func (p *Planner) flatPhysicalField(dataset, ref string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, error) {
	if semantic, ok := p.model.Dimensions[ref]; ok {
		binding, ok := semantic.Bindings[dataset]
		if !ok {
			return semanticmodel.MetricDimension{}, nil, fmt.Errorf("semantic dimension %q has no binding for dataset %q", ref, dataset)
		}
		physical, err := p.resolveDimension(binding.Field)
		if err != nil {
			return semanticmodel.MetricDimension{}, nil, err
		}
		path, err := p.resolveBindingPath(dataset, binding)
		return physical, path, err
	}
	physical, err := p.resolveDimension(ref)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, err
	}
	path, err := p.relationshipPath(dataset, physical.Table)
	return physical, path, err
}

func flatRouteNames(path []semanticmodel.Relationship) []string {
	out := make([]string, len(path))
	for i, item := range path {
		out[i] = item.ID
	}
	return out
}
func flatRoutes(paths map[string][]semanticmodel.Relationship, dataset string, p *Planner) ([]planir.RelationshipRoute, error) {
	seen := map[string]bool{}
	out := []planir.RelationshipRoute{}
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		edges := []planir.RelationshipPath{}
		for _, rel := range path {
			edge := planIRRelationshipPath(rel)
			fromRelation, err := p.physicalTable(edge.FromDataset)
			if err != nil {
				return nil, err
			}
			toRelation, err := p.physicalTable(edge.ToDataset)
			if err != nil {
				return nil, err
			}
			edge.FromRelation, edge.ToRelation = fromRelation, toRelation
			edges = append(edges, edge)
		}
		key := strings.Join(flatRouteNames(path), "/")
		if !seen[key] {
			seen[key] = true
			out = append(out, planir.RelationshipRoute{RootDataset: dataset, Edges: edges})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(flatRouteNamesSemantic(out[i]), "/") < strings.Join(flatRouteNamesSemantic(out[j]), "/")
	})
	return out, nil
}
func flatRouteNamesSemantic(route planir.RelationshipRoute) []string {
	out := make([]string, len(route.Edges))
	for i, e := range route.Edges {
		out[i] = e.Name
	}
	return out
}
func flatOrderedRelationships(paths map[string][]semanticmodel.Relationship, dataset string) []planir.RelationshipPath {
	_ = dataset
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
		if flatFilterHasRelationship(filter, paths) {
			relationship = append(relationship, filter)
		} else {
			root = append(root, filter)
		}
	}
	return
}

func flatFilterHasRelationship(filter Filter, paths map[string][]semanticmodel.Relationship) bool {
	if filter.Spatial != nil {
		if len(paths[filter.Spatial.LatitudeField]) > 0 || len(paths[filter.Spatial.LongitudeField]) > 0 {
			return true
		}
	}
	if filter.Field != "" && len(paths[filter.Field]) > 0 {
		return true
	}
	for _, group := range filter.Groups {
		for _, child := range group.Filters {
			if flatFilterHasRelationship(child, paths) {
				return true
			}
		}
	}
	return false
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

func flatFilterFieldRoutes(filters []Filter, paths map[string][]semanticmodel.Relationship, dataset string, p *Planner) (map[string][]planir.RelationshipRoute, error) {
	out := map[string][]planir.RelationshipRoute{}
	var routeErr error
	var walk func([]Filter)
	walk = func(values []Filter) {
		if routeErr != nil {
			return
		}
		for _, filter := range values {
			if filter.Spatial != nil {
				for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
					path := paths[field]
					if len(path) == 0 {
						continue
					}
					edges := make([]planir.RelationshipPath, 0, len(path))
					for _, relation := range path {
						edge := planIRRelationshipPath(relation)
						fromRelation, err := p.physicalTable(edge.FromDataset)
						if err != nil {
							routeErr = err
							return
						}
						toRelation, err := p.physicalTable(edge.ToDataset)
						if err != nil {
							routeErr = err
							return
						}
						edge.FromRelation, edge.ToRelation = fromRelation, toRelation
						edges = append(edges, edge)
					}
					out[field] = []planir.RelationshipRoute{{RootDataset: dataset, Edges: edges}}
				}
			}
			if filter.Field != "" {
				path := paths[filter.Field]
				if len(path) > 0 {
					edges := make([]planir.RelationshipPath, 0, len(path))
					for _, relation := range path {
						edge := planIRRelationshipPath(relation)
						fromRelation, err := p.physicalTable(edge.FromDataset)
						if err != nil {
							routeErr = err
							return
						}
						toRelation, err := p.physicalTable(edge.ToDataset)
						if err != nil {
							routeErr = err
							return
						}
						edge.FromRelation, edge.ToRelation = fromRelation, toRelation
						edges = append(edges, edge)
					}
					out[filter.Field] = []planir.RelationshipRoute{{RootDataset: dataset, Edges: edges}}
				}
			}
			for _, group := range filter.Groups {
				walk(group.Filters)
			}
		}
	}
	walk(filters)
	return out, routeErr
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
