package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// NewSemanticAccessPlanner binds a compiled policy to one detached evaluation
// context. All decisions remain owned by FAI-639; this boundary places them.
func NewSemanticAccessPlanner(compiled *CompiledModel, context SemanticAccessEvaluationContext, options ...PlannerOption) (*Planner, error) {
	if compiled == nil || compiled.semanticAccess == nil {
		return nil, fmt.Errorf("registry-aware compiled semantic policy is required")
	}
	context.Attributes = append(context.Attributes[:0:0], context.Attributes...)
	for i := range context.Attributes {
		context.Attributes[i].CanonicalValues = append([]string(nil), context.Attributes[i].CanonicalValues...)
	}
	p := &Planner{compiled: compiled, semanticAccessContext: &context}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("planner option is required")
		}
		if err := option(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// securePlanGraph runs after relational lowering/sharing and before rendering.
// It protects explicit roots and makes implicit relationship-side scans explicit.
func (p *Planner) securePlanGraph(g *planir.Graph) error {
	policy := p.compiled.semanticAccess
	if policy == nil || !policy.protected {
		return nil
	}
	if p.semanticAccessContext == nil {
		return fmt.Errorf("protected plan requires semantic access context")
	}
	if err := g.Validate(); err != nil {
		return err
	}
	for _, node := range g.Nodes {
		if node.Kind() == planir.KindSecurityBarrier {
			return g.SealSecurity()
		}
	}
	context := *p.semanticAccessContext
	evaluate := func(target SemanticAccessTarget) (SemanticAccessDecision, error) {
		d, err := policy.Evaluate(target, context)
		if err != nil {
			return d, err
		}
		if !d.Allowed {
			return d, fmt.Errorf("semantic access denied: %s", d.Reason)
		}
		return d, nil
	}
	ids := planIRTopologicalIDs(g)
	// Request-derived lineage and metric facts retain the authored dependencies,
	// including members whose final output uses a caller-selected alias.
	checked := map[string]bool{}
	for _, id := range ids {
		meta := g.Nodes[id].Meta()
		for _, metric := range meta.AvailableMetrics {
			if _, ok := policy.metrics[metric.Name]; ok && !checked["metric:"+metric.Name] {
				if _, err := evaluate(SemanticAccessTarget{Metric: metric.Name}); err != nil {
					return err
				}
				checked["metric:"+metric.Name] = true
			}
		}
		for _, entry := range meta.PhysicalLineage {
			if _, ok := policy.metrics[entry.Logical]; ok && !checked["metric:"+entry.Logical] {
				if _, err := evaluate(SemanticAccessTarget{Metric: entry.Logical}); err != nil {
					return err
				}
				checked["metric:"+entry.Logical] = true
			}
			for _, dimension := range sortedSecurityKeys(p.compiled.dimensionBindings) {
				bindings := p.compiled.dimensionBindings[dimension]
				for _, dataset := range sortedSecurityKeys(bindings) {
					binding := bindings[dataset]
					if binding.Physical.Field != entry.Field && binding.Physical.Field != entry.Dataset+"."+entry.Field && dimension != entry.Logical {
						continue
					}
					key := "dimension:" + dataset + ":" + dimension
					if checked[key] {
						continue
					}
					if _, err := evaluate(SemanticAccessTarget{Dataset: dataset, Dimension: dimension}); err != nil {
						return err
					}
					checked[key] = true
				}
			}
		}
	}
	sequence := 0
	var protect func(planir.ScanDataset, map[string]bool) (string, error)
	protect = func(scan planir.ScanDataset, visiting map[string]bool) (string, error) {
		if visiting[scan.Dataset] {
			return "", fmt.Errorf("security relationship requirements cycle at dataset %q", scan.Dataset)
		}
		next := map[string]bool{}
		for name := range visiting {
			next[name] = true
		}
		next[scan.Dataset] = true
		aliases := map[string]struct{}{}
		appendCompiledDatasetAliases(p.compiled, aliases, scan.Dataset)
		names := sortedKeys(aliases)
		predicates := []planir.Predicate{}
		routes := map[string]planir.RelationshipRoute{}
		targets := map[string]string{}
		for _, alias := range names {
			decision, err := evaluate(SemanticAccessTarget{Dataset: alias})
			if err != nil {
				return "", err
			}
			filters := policy.Filters(alias)
			if len(filters) != len(decision.Predicates) {
				return "", fmt.Errorf("semantic access filter evidence is incomplete")
			}
			for i, filter := range filters {
				predicate := decision.Predicates[i]
				route := planir.RelationshipRoute{RootDataset: scan.Dataset}
				if len(filter.Route) > 1 {
					return "", fmt.Errorf("ambiguous security filter route")
				}
				if len(filter.Route) == 1 {
					route = filter.Route[0]
					route.RootDataset = scan.Dataset
					route.Edges = append([]planir.RelationshipPath(nil), route.Edges...)
					if len(route.Edges) > 0 {
						route.Edges[0].FromDataset = scan.Dataset
					}
				}
				if len(route.Edges) == 0 {
					predicate.Field = scan.Dataset + "." + strings.TrimPrefix(predicate.Field, alias+".")
					scan.AvailableFields = appendPlanIRField(scan.AvailableFields, planir.Field{Name: predicate.Field, Type: string(filter.Type)})
				} else {
					if previous, ok := routes[predicate.Field]; ok && planIRRelationshipSequenceSignature(previous.Edges) != planIRRelationshipSequenceSignature(route.Edges) {
						return "", fmt.Errorf("security field has ambiguous routes")
					}
					routes[predicate.Field] = route
					for _, edge := range route.Edges {
						if targets[edge.ToDataset] != "" {
							continue
						}
						target, err := p.securityScan(edge.ToDataset, fmt.Sprintf("security_route_scan_%d", sequence))
						sequence++
						if err != nil {
							return "", err
						}
						targetID, err := protect(target, next)
						if err != nil {
							return "", err
						}
						targets[edge.ToDataset] = targetID
					}
				}
				predicates = append(predicates, predicate)
			}
		}
		id := scan.NodeID + "_security"
		identity := policy.registryState.Profile + ":" + fmt.Sprint(policy.registryState.Revision) + ":" + policy.registryState.Digest + ":" + scan.Dataset
		secured, barrier, err := planir.NewRoutedSecurityBarrier(scan, id, identity, predicates, routes, targets)
		if err != nil {
			return "", err
		}
		g.Nodes[scan.NodeID] = secured
		g.Nodes[id] = barrier
		return id, nil
	}
	mapping := map[string]string{}
	for _, id := range ids {
		mapping[id] = id
	}
	for _, id := range ids {
		node := g.Nodes[id]
		switch node.(type) {
		case planir.ScanDataset, *planir.ScanDataset:
			replacement, err := protect(planIRScan(node), nil)
			if err != nil {
				return err
			}
			mapping[id] = replacement
		case planir.TraverseRelationship, *planir.TraverseRelationship:
			n := planIRTraverse(node)
			n.Input = mapping[n.Input]
			target, err := p.securityScan(n.Path.ToDataset, fmt.Sprintf("security_target_scan_%d", sequence))
			sequence++
			if err != nil {
				return err
			}
			targetID, err := protect(target, nil)
			if err != nil {
				return err
			}
			n.TargetInput = targetID
			g.Nodes[id] = n
		case planir.FilterRows, *planir.FilterRows:
			n := planIRFilter(node)
			n.Input = mapping[n.Input]
			g.Nodes[id] = n
		default:
			clone, err := clonePlanIRNode(node, id, mapping)
			if err != nil {
				return err
			}
			g.Nodes[id] = clone
		}
	}
	g.Roots = nil
	for id, node := range g.Nodes {
		if len(node.Inputs()) == 0 {
			g.Roots = append(g.Roots, id)
		}
	}
	sort.Strings(g.Roots)
	return g.SealSecurity()
}

func sortedSecurityKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (p *Planner) securityScan(dataset, id string) (planir.ScanDataset, error) {
	table, ok := p.datasetTable(dataset)
	if !ok {
		return planir.ScanDataset{}, fmt.Errorf("unknown security dataset %q", dataset)
	}
	relation, err := p.physicalTable(dataset)
	if err != nil {
		return planir.ScanDataset{}, err
	}
	names := make([]string, 0, len(table.Dimensions))
	for name := range table.Dimensions {
		names = append(names, name)
	}
	sort.Strings(names)
	fields := []planir.Field{}
	for _, name := range names {
		field := table.Dimensions[name]
		fields = append(fields, planir.Field{Name: dataset + "." + name, Type: planIRLogicalType(field.Datatype, field.Type)})
	}
	return planir.ScanDataset{NodeMeta: planir.NodeMeta{NodeID: id, RootDatasets: []string{dataset}, FilterPhase: planir.FilterPhaseScan, AvailableFields: fields}, Dataset: dataset, Relation: relation}, nil
}
