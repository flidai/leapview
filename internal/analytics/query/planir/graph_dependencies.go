package planir

import (
	"sort"
	"strings"
)

func (g *Graph) Dependencies() (Dependencies, error) {
	if err := g.Validate(); err != nil {
		return Dependencies{}, err
	}
	datasets := map[string]struct{}{}
	physical := map[string]struct{}{}
	paths := map[string]struct{}{}
	for _, id := range g.reachableIDs() {
		node := g.Nodes[id]
		meta := node.Meta()
		for _, root := range meta.RootDatasets {
			if root != "" {
				datasets[root] = struct{}{}
			}
		}
		for _, item := range meta.PhysicalLineage {
			if item.Dataset != "" {
				datasets[item.Dataset] = struct{}{}
			}
			if item.Dataset != "" && item.Field != "" {
				physical[item.Dataset+"."+item.Field] = struct{}{}
			}
		}
		for _, route := range meta.RelationshipRoutes {
			for _, relation := range route.Edges {
				if relation.FromDataset != "" {
					datasets[relation.FromDataset] = struct{}{}
				}
				if relation.ToDataset != "" {
					datasets[relation.ToDataset] = struct{}{}
				}
				for _, key := range relation.JoinKeys {
					physical[relation.FromDataset+"."+key.From] = struct{}{}
					physical[relation.ToDataset+"."+key.To] = struct{}{}
				}
			}
			if len(route.Edges) > 0 {
				names := make([]string, len(route.Edges))
				for i, edge := range route.Edges {
					names[i] = edge.Name
				}
				paths[route.RootDataset+":"+strings.Join(names, ",")] = struct{}{}
			}
		}
	}
	return Dependencies{Datasets: sortedSet(datasets), PhysicalFields: sortedSet(physical), RelationshipPaths: sortedSet(paths)}, nil
}

func (g *Graph) reachableIDs() []string {
	seen := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		if node, ok := g.Nodes[id]; ok && node != nil {
			for _, input := range node.Inputs() {
				visit(input)
			}
		}
	}
	visit(g.Output)
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// Explain returns deterministic, human-readable plan output intended for
// logs, query audit records, and debugging. It validates before explaining.
