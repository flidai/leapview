package query

// Bundle and spatial plans lower their governed relational shape into PlanIR.
// This file contains the small amount of graph plumbing needed to share source
// branches while preserving independently shaped aggregate outputs.

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// buildBundlePlanIR merges branch graphs over common scan/traversal/filter
// nodes. Aggregate, compute, and sort nodes remain branch-local because their
// projections and grouping sets are intentionally allowed to differ.
func (p *Planner) buildBundlePlanIR(requests []BundleRequest, resolutions []aggregateResolution) (*planir.Graph, error) {
	if len(requests) == 0 || len(requests) != len(resolutions) {
		return nil, fmt.Errorf("bundle plan IR requires matching non-empty branches")
	}
	graphs := make([]*planir.Graph, len(requests))
	for i := range requests {
		graph, err := p.buildAggregatePlanIR(requests[i].Request, resolutions[i])
		if err != nil {
			return nil, fmt.Errorf("bundle branch %q: %w", requests[i].ID, err)
		}
		if err := graph.Validate(); err != nil {
			return nil, fmt.Errorf("bundle branch %q plan IR: %w", requests[i].ID, err)
		}
		graphs[i] = graph
	}
	merged := &planir.Graph{Nodes: map[string]planir.Node{}}
	sharedScans := map[string]string{}
	sharedTraverses := map[string]string{}
	sharedFilters := map[string]string{}
	branchOutputs := make([]string, 0, len(graphs))
	for branchIndex, graph := range graphs {
		mapping := map[string]string{}
		for _, oldID := range planIRTopologicalIDs(graph) {
			node := graph.Nodes[oldID]
			switch value := node.(type) {
			case planir.ScanDataset, *planir.ScanDataset:
				scan := planIRScan(value)
				if existing, ok := sharedScans[scan.Dataset]; ok {
					mapping[oldID] = existing
					merged.Nodes[existing] = withMergedPlanIRMeta(merged.Nodes[existing], scan.NodeMeta)
					continue
				}
				newID := fmt.Sprintf("bundle_scan_%d", len(sharedScans))
				scan.NodeID = newID
				sharedScans[scan.Dataset] = newID
				mapping[oldID] = newID
				merged.Nodes[newID] = scan
				merged.Roots = append(merged.Roots, newID)
			case planir.TraverseRelationship, *planir.TraverseRelationship:
				traverse := planIRTraverse(value)
				input := mapping[traverse.Input]
				if input == "" {
					return nil, fmt.Errorf("bundle branch %q traversal %q has no mapped input", requests[branchIndex].ID, oldID)
				}
				key := planIRRelationshipKey(input, traverse.Path)
				if existing, ok := sharedTraverses[key]; ok {
					mapping[oldID] = existing
					merged.Nodes[existing] = withMergedPlanIRMeta(merged.Nodes[existing], traverse.NodeMeta)
					continue
				}
				newID := fmt.Sprintf("bundle_traverse_%d", len(sharedTraverses))
				traverse.NodeID = newID
				traverse.Input = input
				sharedTraverses[key] = newID
				mapping[oldID] = newID
				merged.Nodes[newID] = traverse
			case planir.FilterRows, *planir.FilterRows:
				filter := planIRFilter(value)
				input := mapping[filter.Input]
				if input == "" {
					return nil, fmt.Errorf("bundle branch %q filter %q has no mapped input", requests[branchIndex].ID, oldID)
				}
				filter.Input = input
				key := planIRFilterKey(filter)
				if existing, ok := sharedFilters[key]; ok {
					mapping[oldID] = existing
					merged.Nodes[existing] = withMergedPlanIRMeta(merged.Nodes[existing], filter.NodeMeta)
					continue
				}
				newID := fmt.Sprintf("bundle_filter_%d", len(sharedFilters))
				filter.NodeID = newID
				sharedFilters[key] = newID
				mapping[oldID] = newID
				merged.Nodes[newID] = filter
			default:
				cloned, err := clonePlanIRNode(node, "bundle_"+fmt.Sprint(branchIndex)+"_"+oldID, mapping)
				if err != nil {
					return nil, err
				}
				mapping[oldID] = cloned.Meta().NodeID
				merged.Nodes[cloned.Meta().NodeID] = cloned
			}
		}
		output := mapping[graph.Output]
		if output == "" {
			return nil, fmt.Errorf("bundle branch %q has no mapped output", requests[branchIndex].ID)
		}
		branchOutputs = append(branchOutputs, output)
	}

	branchesMeta := planir.NodeMeta{NodeID: "bundle_branches", FilterPhase: planir.FilterPhasePostAggregate}
	for _, output := range branchOutputs {
		branchesMeta = mergePlanIRMeta(branchesMeta, merged.Nodes[output].Meta())
	}
	branchesMeta.NodeID = "bundle_branches"
	branches := make([]planir.BundleBranch, len(branchOutputs))
	for i, output := range branchOutputs {
		branches[i] = planir.BundleBranch{ID: requests[i].ID, Ordinal: i, Input: output}
	}
	branchesMeta.OutputGrain = planir.Grain{}
	branchesMeta.AvailableFields = nil
	branchesMeta.AvailableMetrics = nil
	branchesMeta.PhysicalLineage = nil
	merged.Nodes[branchesMeta.NodeID] = planir.BundleBranches{NodeMeta: branchesMeta, Branches: branches}
	merged.Output = branchesMeta.NodeID
	merged.NodeMeta = branchesMeta
	if err := merged.Validate(); err != nil {
		return nil, fmt.Errorf("validate bundle plan IR: %w", err)
	}
	return merged, nil
}

func (p *Planner) bundleBranchDependencyProjections(requests []BundleRequest, resolutions []aggregateResolution) ([]DependencyProjection, []string, error) {
	projections := make([]DependencyProjection, len(requests))
	fingerprints := make([]string, len(requests))
	for i := range requests {
		graph, err := p.buildAggregatePlanIR(requests[i].Request, resolutions[i])
		if err != nil {
			return nil, nil, err
		}
		projections[i], err = (Plan{IR: graph}).ResultDependencies()
		if err != nil {
			return nil, nil, err
		}
		fingerprints[i], err = graph.Fingerprint()
		if err != nil {
			return nil, nil, err
		}
	}
	return projections, fingerprints, nil
}

func planIRTopologicalIDs(graph *planir.Graph) []string {
	ids := make([]string, 0, len(graph.Nodes))
	state := map[string]uint8{}
	var visit func(string)
	visit = func(id string) {
		if state[id] == 2 {
			return
		}
		if state[id] == 1 {
			return
		}
		state[id] = 1
		node := graph.Nodes[id]
		inputs := append([]string(nil), node.Inputs()...)
		sort.Strings(inputs)
		for _, input := range inputs {
			visit(input)
		}
		state[id] = 2
		ids = append(ids, id)
	}
	all := make([]string, 0, len(graph.Nodes))
	for id := range graph.Nodes {
		all = append(all, id)
	}
	sort.Strings(all)
	for _, id := range all {
		visit(id)
	}
	return ids
}

func planIRScan(node planir.Node) planir.ScanDataset {
	switch value := node.(type) {
	case planir.ScanDataset:
		return value
	case *planir.ScanDataset:
		return *value
	default:
		panic("plan IR node is not ScanDataset")
	}
}

func planIRTraverse(node planir.Node) planir.TraverseRelationship {
	switch value := node.(type) {
	case planir.TraverseRelationship:
		return value
	case *planir.TraverseRelationship:
		return *value
	default:
		panic("plan IR node is not TraverseRelationship")
	}
}

func planIRFilter(node planir.Node) planir.FilterRows {
	switch value := node.(type) {
	case planir.FilterRows:
		return value
	case *planir.FilterRows:
		return *value
	default:
		panic("plan IR node is not FilterRows")
	}
}

func planIRRelationshipKey(input string, path planir.RelationshipPath) string {
	data, _ := json.Marshal(path)
	return input + "|" + string(data)
}

func planIRFilterKey(filter planir.FilterRows) string {
	data, _ := json.Marshal(struct {
		Input     string
		Phase     planir.FilterPhase
		Source    planir.FilterSource
		Name      string
		Predicate planir.Predicate
	}{filter.Input, filter.FilterPhase, filter.Source, filter.Name, filter.Predicate})
	return string(data)
}

func clonePlanIRNode(node planir.Node, id string, mapping map[string]string) (planir.Node, error) {
	meta := node.Meta()
	meta.NodeID = id
	input := func(old string) (string, error) {
		mapped := mapping[old]
		if mapped == "" {
			return "", fmt.Errorf("plan IR node %q input %q is not mapped", id, old)
		}
		return mapped, nil
	}
	switch value := node.(type) {
	case planir.AggregateMetrics:
		mapped, err := input(value.Input)
		if err != nil {
			return nil, err
		}
		value.NodeMeta, value.Input = meta, mapped
		return value, nil
	case *planir.AggregateMetrics:
		if value == nil {
			return nil, fmt.Errorf("plan IR node %q is nil", id)
		}
		copy := *value
		return clonePlanIRNode(copy, id, mapping)
	case planir.StitchAggregates:
		inputs, err := mappedInputs(value.InputsList, mapping)
		if err != nil {
			return nil, err
		}
		value.NodeMeta, value.InputsList = meta, inputs
		return value, nil
	case *planir.StitchAggregates:
		if value == nil {
			return nil, fmt.Errorf("plan IR node %q is nil", id)
		}
		copy := *value
		return clonePlanIRNode(copy, id, mapping)
	case planir.ComputeRatio:
		mapped, err := input(value.Input)
		if err != nil {
			return nil, err
		}
		value.NodeMeta, value.Input = meta, mapped
		return value, nil
	case *planir.ComputeRatio:
		if value == nil {
			return nil, fmt.Errorf("plan IR node %q is nil", id)
		}
		copy := *value
		return clonePlanIRNode(copy, id, mapping)
	case planir.ComputeDerived:
		mapped, err := input(value.Input)
		if err != nil {
			return nil, err
		}
		value.NodeMeta, value.Input = meta, mapped
		return value, nil
	case *planir.ComputeDerived:
		if value == nil {
			return nil, fmt.Errorf("plan IR node %q is nil", id)
		}
		copy := *value
		return clonePlanIRNode(copy, id, mapping)
	case planir.SortLimit:
		mapped, err := input(value.Input)
		if err != nil {
			return nil, err
		}
		value.NodeMeta, value.Input = meta, mapped
		return value, nil
	case *planir.SortLimit:
		if value == nil {
			return nil, fmt.Errorf("plan IR node %q is nil", id)
		}
		copy := *value
		return clonePlanIRNode(copy, id, mapping)
	case planir.BundleBranches:
		branches := append([]planir.BundleBranch(nil), value.Branches...)
		for i := range branches {
			mapped, err := mappedInputs([]string{branches[i].Input}, mapping)
			if err != nil {
				return nil, err
			}
			branches[i].Input = mapped[0]
		}
		value.NodeMeta, value.Branches = meta, branches
		return value, nil
	case *planir.BundleBranches:
		if value == nil {
			return nil, fmt.Errorf("plan IR node %q is nil", id)
		}
		copy := *value
		return clonePlanIRNode(copy, id, mapping)
	default:
		return nil, fmt.Errorf("unsupported branch node %q", node.Kind())
	}
}

func mappedInputs(inputs []string, mapping map[string]string) ([]string, error) {
	out := make([]string, len(inputs))
	for i, input := range inputs {
		mapped := mapping[input]
		if mapped == "" {
			return nil, fmt.Errorf("plan IR input %q is not mapped", input)
		}
		out[i] = mapped
	}
	return out, nil
}

func withMergedPlanIRMeta(node planir.Node, meta planir.NodeMeta) planir.Node {
	merged := mergePlanIRMeta(node.Meta(), meta)
	merged.NodeID = node.Meta().NodeID
	switch value := node.(type) {
	case planir.ScanDataset:
		value.NodeMeta = merged
		return value
	case planir.TraverseRelationship:
		value.NodeMeta = merged
		return value
	case planir.FilterRows:
		value.NodeMeta = merged
		return value
	case *planir.ScanDataset:
		copy := *value
		copy.NodeMeta = merged
		return &copy
	case *planir.TraverseRelationship:
		copy := *value
		copy.NodeMeta = merged
		return &copy
	case *planir.FilterRows:
		copy := *value
		copy.NodeMeta = merged
		return &copy
	default:
		return node
	}
}

func mergePlanIRMeta(left, right planir.NodeMeta) planir.NodeMeta {
	out := left
	if out.NodeID == "" {
		out.NodeID = right.NodeID
	}
	if planIRGrainEmpty(out.OutputGrain) {
		out.OutputGrain = right.OutputGrain
	}
	out.AvailableFields = mergePlanIRFields(out.AvailableFields, right.AvailableFields)
	out.AvailableMetrics = mergePlanIRMetrics(out.AvailableMetrics, right.AvailableMetrics)
	out.RootDatasets = mergePlanIRStrings(out.RootDatasets, right.RootDatasets)
	out.PhysicalLineage = mergePlanIRLineage(out.PhysicalLineage, right.PhysicalLineage)
	out.RelationshipRoutes = mergePlanIRRoutes(out.RelationshipRoutes, right.RelationshipRoutes)
	if planIRPhaseRank(right.FilterPhase) > planIRPhaseRank(out.FilterPhase) {
		out.FilterPhase = right.FilterPhase
	}
	return out
}

func mergePlanIRRoutes(a, b []planir.RelationshipRoute) []planir.RelationshipRoute {
	seen := map[string]planir.RelationshipRoute{}
	for _, route := range append(append([]planir.RelationshipRoute(nil), a...), b...) {
		key := route.RootDataset + ":"
		for _, edge := range route.Edges {
			key += edge.Name + ":" + edge.FromDataset + ">" + edge.ToDataset + "/"
		}
		seen[key] = route
	}
	out := make([]planir.RelationshipRoute, 0, len(seen))
	for _, route := range seen {
		out = append(out, route)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RootDataset < out[j].RootDataset
	})
	return out
}

func mergePlanIRFields(a, b []planir.Field) []planir.Field {
	seen := map[string]planir.Field{}
	for _, field := range append(append([]planir.Field(nil), a...), b...) {
		if prior, ok := seen[field.Name]; !ok || prior.Type == "" {
			seen[field.Name] = field
		}
	}
	out := make([]planir.Field, 0, len(seen))
	for _, field := range seen {
		out = append(out, field)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func mergePlanIRMetrics(a, b []planir.Metric) []planir.Metric {
	seen := map[string]planir.Metric{}
	for _, metric := range append(append([]planir.Metric(nil), a...), b...) {
		if prior, ok := seen[metric.Name]; !ok || prior.Type == "" {
			seen[metric.Name] = metric
		}
	}
	out := make([]planir.Metric, 0, len(seen))
	for _, metric := range seen {
		out = append(out, metric)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func mergePlanIRStrings(a, b []string) []string {
	seen := map[string]bool{}
	for _, v := range append(append([]string(nil), a...), b...) {
		if v != "" {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
func mergePlanIRLineage(a, b []planir.PhysicalLineage) []planir.PhysicalLineage {
	seen := map[string]planir.PhysicalLineage{}
	for _, v := range append(append([]planir.PhysicalLineage(nil), a...), b...) {
		seen[v.Logical+"|"+v.Dataset+"|"+v.Field] = v
	}
	out := make([]planir.PhysicalLineage, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Logical+out[i].Dataset+out[i].Field < out[j].Logical+out[j].Dataset+out[j].Field
	})
	return out
}
func planIRGrainEmpty(g planir.Grain) bool { return len(g.Fields) == 0 && g.TimeGrain == "" }
func planIRPhaseRank(p planir.FilterPhase) int {
	switch p {
	case planir.FilterPhaseScan:
		return 1
	case planir.FilterPhaseRelationship:
		return 2
	case planir.FilterPhaseAggregate:
		return 3
	case planir.FilterPhasePostAggregate:
		return 4
	}
	return 0
}
