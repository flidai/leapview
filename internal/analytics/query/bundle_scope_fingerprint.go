package query

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// bundleScopeFingerprint is deliberately narrower than a branch output
// fingerprint: bundles may differ in dimensions, projections, and metric
// expressions, but every branch must execute under the same governed row
// scope. The normalized graph still carries roots, traversals, filter phases,
// predicate provenance, a projection dependency set, and a typed aggregate
// boundary so compatibility is decided by PlanIR rather than Go equality.
func (p *Planner) bundleScopeFingerprint(request Request, resolved aggregateResolution) (string, error) {
	full, err := p.buildAggregatePlanIR(request, resolved)
	if err != nil {
		return "", err
	}
	scope := &planir.Graph{Nodes: map[string]planir.Node{}}
	for factIndex, fact := range resolved.Facts {
		aggregateID := fmt.Sprintf("aggregate_%d", factIndex)
		aggregate, ok := full.Nodes[aggregateID]
		if !ok {
			return "", fmt.Errorf("aggregate %q missing from plan IR", aggregateID)
		}
		inputID := aggregate.Inputs()[0]
		chain := planIRInputChain(full, inputID)
		requiredEdges := map[string]bool{}
		bindings, err := p.factFilterFields(request.Filters, resolved, fact)
		if err != nil {
			return "", err
		}
		for _, binding := range bindings {
			for _, relationship := range binding.Path {
				requiredEdges[relationship.ID] = true
			}
		}
		mapped := map[string]string{}
		fields := planIRScopeFields(full, chain)
		if len(fields) == 0 {
			fields = []planir.Field{{Name: "__scope_input", Type: "integer"}}
		}
		for chainIndex, oldID := range chain {
			node := full.Nodes[oldID]
			if traverse, ok := node.(planir.TraverseRelationship); ok && !requiredEdges[traverse.Path.Name] {
				// Metric-local dimensions and named where routes are not part of
				// shared row scope. Keep the input mapping contiguous so a later
				// request filter attaches to the last governed scope node.
				if len(traverse.Inputs()) == 1 {
					mapped[oldID] = mapped[traverse.Input]
				}
				continue
			}
			if traverse, ok := node.(*planir.TraverseRelationship); ok && traverse != nil && !requiredEdges[traverse.Path.Name] {
				mapped[oldID] = mapped[traverse.Input]
				continue
			}
			id := fmt.Sprintf("scope_%d_%d", factIndex, chainIndex)
			meta := planIRScopeMeta(node.Meta(), id, fields, planIRScopeRoutes(full, chain, requiredEdges))
			switch value := node.(type) {
			case planir.ScanDataset:
				value.NodeMeta = meta
				scope.Nodes[id] = value
				scope.Roots = append(scope.Roots, id)
			case *planir.ScanDataset:
				copy := *value
				copy.NodeMeta = meta
				scope.Nodes[id] = copy
				scope.Roots = append(scope.Roots, id)
			case planir.TraverseRelationship:
				copy := value
				copy.NodeMeta = meta
				copy.Input = mapped[value.Input]
				if copy.Input == "" {
					return "", fmt.Errorf("scope traversal input %q is missing", value.Input)
				}
				scope.Nodes[id] = copy
			case *planir.TraverseRelationship:
				copy := *value
				copy.NodeMeta = meta
				copy.Input = mapped[value.Input]
				if copy.Input == "" {
					return "", fmt.Errorf("scope traversal input %q is missing", value.Input)
				}
				scope.Nodes[id] = copy
			case planir.FilterRows:
				copy := value
				copy.NodeMeta = meta
				copy.Input = mapped[value.Input]
				if copy.Input == "" {
					return "", fmt.Errorf("scope filter input %q is missing", value.Input)
				}
				scope.Nodes[id] = copy
			case *planir.FilterRows:
				copy := *value
				copy.NodeMeta = meta
				copy.Input = mapped[value.Input]
				if copy.Input == "" {
					return "", fmt.Errorf("scope filter input %q is missing", value.Input)
				}
				scope.Nodes[id] = copy
			default:
				return "", fmt.Errorf("scope contains unsupported source node %q", node.Kind())
			}
			mapped[oldID] = id
		}
		mappedInput := mapped[inputID]
		if mappedInput == "" {
			return "", fmt.Errorf("scope aggregate input %q is missing", inputID)
		}
		metricInput := fields[0].Name
		aggID := fmt.Sprintf("scope_aggregate_%d", factIndex)
		aggMeta := planir.NodeMeta{NodeID: aggID, OutputGrain: planir.Grain{}, AvailableFields: nil, AvailableMetrics: []planir.Metric{{Name: "__scope_count", Type: "integer"}}, RootDatasets: []string{fact}, FilterPhase: planir.FilterPhaseAggregate, RelationshipRoutes: planIRScopeRoutes(full, chain, requiredEdges)}
		scope.Nodes[aggID] = planir.AggregateMetrics{NodeMeta: aggMeta, Input: mappedInput, Metrics: []planir.MetricSpec{{Name: "__scope_count", Type: "integer", Aggregation: "COUNT", Input: metricInput}}}
	}

	aggregateIDs := make([]string, 0, len(resolved.Facts))
	for factIndex := range resolved.Facts {
		aggregateIDs = append(aggregateIDs, fmt.Sprintf("scope_aggregate_%d", factIndex))
	}
	if len(aggregateIDs) == 1 {
		scope.Output = aggregateIDs[0]
		scope.NodeMeta = scope.Nodes[scope.Output].Meta()
	} else {
		meta := planir.NodeMeta{NodeID: "scope_bundle", OutputGrain: planir.Grain{}, RootDatasets: append([]string(nil), resolved.Facts...), FilterPhase: planir.FilterPhaseAggregate}
		branches := make([]planir.BundleBranch, len(aggregateIDs))
		for i, input := range aggregateIDs {
			branches[i] = planir.BundleBranch{ID: resolved.Facts[i], Ordinal: i, Input: input}
		}
		scope.Nodes["scope_bundle"] = planir.BundleBranches{NodeMeta: meta, Branches: branches}
		scope.Output = "scope_bundle"
		scope.NodeMeta = meta
	}
	if err := scope.Validate(); err != nil {
		return "", fmt.Errorf("validate bundle scope plan IR: %w", err)
	}
	return scope.Fingerprint()
}

func planIRInputChain(graph *planir.Graph, id string) []string {
	chain := []string{}
	for id != "" {
		node, ok := graph.Nodes[id]
		if !ok || node == nil {
			break
		}
		chain = append(chain, id)
		inputs := node.Inputs()
		if len(inputs) == 0 {
			break
		}
		id = inputs[0]
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	return chain
}

func planIRScopeFields(graph *planir.Graph, chain []string) []planir.Field {
	seen := map[string]planir.Field{}
	for _, id := range chain {
		node := graph.Nodes[id]
		for _, field := range node.Meta().AvailableFields {
			for _, filterField := range nodeFilterFields(node) {
				if field.Name == filterField {
					seen[field.Name] = field
				}
			}
		}
	}
	out := make([]planir.Field, 0, len(seen))
	for _, field := range seen {
		out = append(out, field)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) == 0 {
		return []planir.Field{{Name: "__scope_input", Type: "integer"}}
	}
	return out
}

func nodeFilterFields(node planir.Node) []string {
	filter, ok := node.(planir.FilterRows)
	if !ok {
		if pointer, ok := node.(*planir.FilterRows); ok && pointer != nil {
			return pointer.Fields
		}
		return nil
	}
	return filter.Fields
}

func planIRScopeMeta(meta planir.NodeMeta, id string, fields []planir.Field, routes []planir.RelationshipRoute) planir.NodeMeta {
	return planir.NodeMeta{NodeID: id, OutputGrain: planir.Grain{}, AvailableFields: append([]planir.Field(nil), fields...), RootDatasets: append([]string(nil), meta.RootDatasets...), FilterPhase: meta.FilterPhase, PhysicalLineage: nil, RelationshipRoutes: routes}
}

func planIRScopeRoutes(graph *planir.Graph, chain []string, required map[string]bool) []planir.RelationshipRoute {
	edges := []planir.RelationshipPath{}
	for _, id := range chain {
		switch node := graph.Nodes[id].(type) {
		case planir.TraverseRelationship:
			if !required[node.Path.Name] {
				continue
			}
			edges = append(edges, node.Path)
		case *planir.TraverseRelationship:
			if node != nil {
				if !required[node.Path.Name] {
					continue
				}
				edges = append(edges, node.Path)
			}
		}
	}
	if len(edges) == 0 {
		return nil
	}
	route := planir.RelationshipRoute{RootDataset: edges[0].FromDataset, Edges: edges}
	return []planir.RelationshipRoute{route}
}
