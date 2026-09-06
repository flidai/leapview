package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// WithSemanticAccess binds one target-qualified FAI-639 policy to a planner.
// Principal values and authority snapshots stay outside the serving graph; the
// provider is consulted when a final plan is admitted.
func WithSemanticAccess(policy *CompiledSemanticAccessPolicy, provider func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error)) PlannerOption {
	return func(planner *Planner) error {
		if planner == nil {
			return fmt.Errorf("planner is required")
		}
		if policy == nil || policy.Digest() == "" {
			return fmt.Errorf("compiled semantic access policy is required")
		}
		if provider == nil {
			return fmt.Errorf("semantic access provider is required")
		}
		planner.semanticAccessPolicy = policy
		planner.semanticAccessProvider = provider
		return nil
	}
}

type semanticAccessMemberRef struct {
	Kind    string
	Name    string
	Dataset string
}

type semanticAccessAdmission struct {
	Policies       map[string]planir.SecurityPolicy
	DecisionDigest string
}

// securePlanGraph is the one query-side admission boundary. It validates the
// target-qualified policy against the immutable serving model and the current
// registry, evaluates one authority snapshot, admits all referenced members,
// then seals every protected scan through PlanIR.
func (p *Planner) securePlanGraph(graph *planir.Graph, members ...semanticAccessMemberRef) (semanticAccessAdmission, error) {
	if graph == nil {
		return semanticAccessAdmission{}, fmt.Errorf("plan graph is nil")
	}
	if p == nil || p.compiled == nil {
		return semanticAccessAdmission{}, fmt.Errorf("compiled semantic model snapshot is required")
	}
	model := p.compiled.SourceModel()
	if model == nil {
		return semanticAccessAdmission{}, fmt.Errorf("compiled semantic model snapshot is required")
	}
	if model.AccessPolicy.Empty() {
		return semanticAccessAdmission{}, nil
	}
	if p.semanticAccessPolicy == nil || p.semanticAccessPolicy.Digest() == "" || p.semanticAccessProvider == nil {
		return semanticAccessAdmission{}, fmt.Errorf("policy-bearing semantic model requires semantic access authority")
	}
	snapshot, current, err := p.semanticAccessProvider()
	if err != nil {
		return semanticAccessAdmission{}, fmt.Errorf("semantic access authority: %w", err)
	}
	registry := current.Registry
	qualified, err := CompileSemanticAccessPolicy(
		p.semanticAccessPolicy.TargetInstanceID(),
		p.semanticAccessPolicy.SemanticModelID(),
		p.semanticAccessPolicy.SemanticGeneration(),
		model,
		p.compiled,
		registry,
	)
	if err != nil {
		return semanticAccessAdmission{}, fmt.Errorf("compile semantic access policy: %w", err)
	}
	if qualified.Digest() != p.semanticAccessPolicy.Digest() {
		return semanticAccessAdmission{}, fmt.Errorf("semantic access policy digest is stale or inconsistent")
	}
	decision, err := EvaluateSemanticAccess(qualified, snapshot, current)
	if err != nil {
		return semanticAccessAdmission{}, fmt.Errorf("evaluate semantic access: %w", err)
	}

	refs := append([]semanticAccessMemberRef(nil), members...)
	refs = append(refs, graphMemberRefs(p, graph)...)
	refs = expandUnqualifiedSemanticMembers(p, refs)
	refs = canonicalSemanticMemberRefs(refs)
	if err := admitSemanticMembers(p, decision, refs); err != nil {
		return semanticAccessAdmission{}, err
	}
	datasets := graphDatasets(graph)
	policies := make(map[string]planir.SecurityPolicy)
	for _, dataset := range datasets {
		if _, exists := p.compiled.dataset(dataset); !exists {
			return semanticAccessAdmission{}, fmt.Errorf("plan graph references unknown dataset %q", dataset)
		}
		spec, ok := qualified.Dataset(dataset)
		if !ok {
			return semanticAccessAdmission{}, fmt.Errorf("compiled semantic access policy has no dataset %q", dataset)
		}
		object, ok := decision.Dataset(dataset)
		if !ok || !object.Allowed {
			return semanticAccessAdmission{}, fmt.Errorf("semantic access denied for dataset %q", dataset)
		}
		if len(spec.AccessFilters) > 0 && object.Predicate == nil {
			return semanticAccessAdmission{}, fmt.Errorf("semantic access filter predicate is missing for dataset %q", dataset)
		}
		var predicate *planir.Predicate
		if object.Predicate != nil {
			value := *object.Predicate
			predicate = &value
		}
		policies[dataset] = planir.SecurityPolicy{PolicyDigest: qualified.Digest(), DecisionDigest: decision.IdentityDigest, Predicate: predicate}
	}
	if len(policies) == 0 {
		return semanticAccessAdmission{DecisionDigest: decision.IdentityDigest}, nil
	}
	if err := planir.ApplySecurityBarriers(graph, policies); err != nil {
		return semanticAccessAdmission{}, fmt.Errorf("apply semantic access barriers: %w", err)
	}
	return semanticAccessAdmission{Policies: policies, DecisionDigest: decision.IdentityDigest}, nil
}

func admitSemanticMembers(p *Planner, decision *SemanticAccessDecision, refs []semanticAccessMemberRef) error {
	seen := map[string]bool{}
	for _, ref := range refs {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		key := ref.Kind + ":" + name + ":" + ref.Dataset

		switch ref.Kind {
		case "metric":
			if seen[key] {
				continue
			}
			if _, known := p.compiled.metric(name); !known {
				// Qualified physical references are not semantic members. Their
				// governing semantic lineage is admitted separately below.
				continue
			}
			object, ok := decision.Metric(name)
			if !ok {
				return fmt.Errorf("semantic access decision is missing metric %q", name)
			}
			seen[key] = true
			if !object.Allowed {
				return fmt.Errorf("semantic access denied for metric %q", name)
			}
		case "dimension":
			if seen[key] {
				continue
			}
			if _, known := p.compiled.SemanticDimension(name); !known {
				// Qualified physical references are not semantic members. Their
				// governing semantic lineage is admitted separately below.
				continue
			}
			if ref.Dataset != "" {
				if _, exists := p.compiled.dataset(ref.Dataset); !exists {
					return fmt.Errorf("unknown dataset %q for dimension %q", ref.Dataset, name)
				}
			}
			object, ok := decision.Dimension(name, ref.Dataset)
			if !ok {
				return fmt.Errorf("semantic access decision is missing dimension %q for dataset %q", name, ref.Dataset)
			}
			seen[key] = true
			if !object.Allowed {
				return fmt.Errorf("semantic access denied for dimension %q", name)
			}
		}
	}
	return nil
}

func graphDatasets(graph *planir.Graph) []string {
	seen := map[string]bool{}
	for _, node := range graph.Nodes {
		for _, dataset := range node.Meta().RootDatasets {
			if dataset != "" {
				seen[dataset] = true
			}
		}
		for _, route := range node.Meta().RelationshipRoutes {
			for _, edge := range route.Edges {
				seen[edge.FromDataset] = true
				seen[edge.ToDataset] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for dataset := range seen {
		result = append(result, dataset)
	}
	sort.Strings(result)
	return result
}

func graphMemberRefs(p *Planner, graph *planir.Graph) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{}
	for _, node := range graph.Nodes {
		meta := node.Meta()
		for _, metric := range meta.AvailableMetrics {
			if _, ok := p.compiled.metric(metric.Name); ok {
				refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: metric.Name})
			}
		}
		for _, lineage := range meta.PhysicalLineage {
			for name, bindings := range p.compiled.dimensionBindings {
				for dataset, binding := range bindings {
					if lineage.Dataset != binding.Physical.Table ||
						(lineage.Field != binding.Physical.Name && lineage.Field != binding.Physical.Field) ||
						!sameSemanticAccessRoute(lineage.Route, planIRRouteNames(binding.Path)) {
						continue
					}
					refs = append(refs, semanticAccessMemberRef{Kind: "dimension", Name: name, Dataset: dataset})
				}
			}
		}
	}
	return refs
}

func sameSemanticAccessRoute(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func expandUnqualifiedSemanticMembers(p *Planner, refs []semanticAccessMemberRef) []semanticAccessMemberRef {
	if p == nil || p.compiled == nil {
		return refs
	}
	out := make([]semanticAccessMemberRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind != "dimension" || ref.Dataset != "" {
			out = append(out, ref)
			continue
		}
		bindings := p.compiled.dimensionBindings[ref.Name]
		datasets := make([]string, 0, len(bindings))
		for dataset := range bindings {
			datasets = append(datasets, dataset)
		}
		sort.Strings(datasets)
		if len(datasets) == 0 {
			out = append(out, ref)
			continue
		}
		for _, dataset := range datasets {
			ref.Dataset = dataset
			out = append(out, ref)
		}
	}
	return out
}

func canonicalSemanticMemberRefs(refs []semanticAccessMemberRef) []semanticAccessMemberRef {
	seen := map[string]semanticAccessMemberRef{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.Name) == "" {
			continue
		}
		key := ref.Kind + "\x00" + ref.Name + "\x00" + ref.Dataset
		seen[key] = ref
	}
	out := make([]semanticAccessMemberRef, 0, len(seen))
	for _, ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Dataset < out[j].Dataset
	})
	return out
}

func semanticDimensionRef(p *Planner, ref, dataset string) (semanticAccessMemberRef, bool) {
	if _, ok := p.compiled.SemanticDimension(ref); !ok {
		return semanticAccessMemberRef{}, false
	}
	return semanticAccessMemberRef{Kind: "dimension", Name: ref, Dataset: dataset}, true
}

func appendSemanticDimensionRefs(p *Planner, refs *[]semanticAccessMemberRef, field, dataset string) {
	if member, ok := semanticDimensionRef(p, strings.TrimSpace(field), dataset); ok {
		*refs = append(*refs, member)
	}
}

func appendFilterMemberRefs(p *Planner, refs *[]semanticAccessMemberRef, filters []Filter, dataset string) {
	for _, filter := range filters {
		scope := dataset
		if filter.Dataset != "" {
			scope = filter.Dataset
		}
		appendSemanticDimensionRefs(p, refs, filter.Field, scope)
		if filter.Spatial != nil {
			spatialScope := scope
			if filter.Spatial.Dataset != "" {
				spatialScope = filter.Spatial.Dataset
			}
			appendSemanticDimensionRefs(p, refs, filter.Spatial.LatitudeField, spatialScope)
			appendSemanticDimensionRefs(p, refs, filter.Spatial.LongitudeField, spatialScope)
		}
		for _, group := range filter.Groups {
			appendFilterMemberRefs(p, refs, group.Filters, scope)
		}
	}
}

func appendSortMemberRefs(p *Planner, refs *[]semanticAccessMemberRef, sorts []Sort, dataset string) {
	for _, sortSpec := range sorts {
		name := strings.TrimSpace(sortSpec.Field)
		if name == "" {
			continue
		}
		if _, ok := p.compiled.metric(name); ok {
			*refs = append(*refs, semanticAccessMemberRef{Kind: "metric", Name: name})
		}
		appendSemanticDimensionRefs(p, refs, name, dataset)
	}
}

func requestRowMemberRefs(p *Planner, request RowRequest) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{}
	for _, metric := range request.Metrics {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: metric.Field})
	}
	for _, dimension := range request.Dimensions {
		refs = append(refs, semanticAccessMemberRef{Kind: "dimension", Name: dimension.Field, Dataset: request.Dataset})
	}
	appendFilterMemberRefs(p, &refs, request.Filters, request.Dataset)
	appendSortMemberRefs(p, &refs, request.Sort, request.Dataset)
	return refs
}

func requestRawValueMemberRefs(p *Planner, request RawValueRequest) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{{Kind: "metric", Name: request.Metric.Field}}
	for _, dimension := range request.Dimensions {
		refs = append(refs, semanticAccessMemberRef{Kind: "dimension", Name: dimension.Field, Dataset: request.Dataset})
	}
	appendFilterMemberRefs(p, &refs, request.Filters, request.Dataset)
	appendSortMemberRefs(p, &refs, request.Sort, request.Dataset)
	return refs
}

func requestCountMemberRefs(p *Planner, request CountRequest) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{}
	appendFilterMemberRefs(p, &refs, request.Filters, request.Dataset)
	return refs
}

func spatialTileMemberRefs(p *Planner, request SpatialTileRequest) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{{Kind: "dimension", Name: request.Latitude.Field, Dataset: request.Dataset}, {Kind: "dimension", Name: request.Longitude.Field, Dataset: request.Dataset}}
	for _, metric := range request.Metrics {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: metric.Field})
	}
	appendFilterMemberRefs(p, &refs, request.Filters, request.Dataset)
	return refs
}

func spatialRawMemberRefs(p *Planner, request SpatialTileRawRequest) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{{Kind: "dimension", Name: request.Latitude.Field, Dataset: request.Dataset}, {Kind: "dimension", Name: request.Longitude.Field, Dataset: request.Dataset}}
	for _, dimension := range request.Dimensions {
		refs = append(refs, semanticAccessMemberRef{Kind: "dimension", Name: dimension.Field, Dataset: request.Dataset})
	}
	for _, metric := range request.Metrics {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: metric.Field})
	}
	appendFilterMemberRefs(p, &refs, request.Filters, request.Dataset)
	return refs
}

func spatialBudgetMemberRefs(p *Planner, request SpatialTileBudgetRequest) []semanticAccessMemberRef {
	return spatialRawMemberRefs(p, SpatialTileRawRequest{Dataset: request.Dataset, Dimensions: request.Dimensions, Metrics: request.Metrics, Filters: request.Filters, Latitude: request.Latitude, Longitude: request.Longitude})
}

func spatialMetadataMemberRefs(p *Planner, request SpatialMetadataRequest) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{{Kind: "dimension", Name: request.Latitude.Field, Dataset: request.Dataset}, {Kind: "dimension", Name: request.Longitude.Field, Dataset: request.Dataset}}
	for _, metric := range request.Metrics {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: metric.Field})
	}
	appendFilterMemberRefs(p, &refs, request.Filters, request.Dataset)
	return refs
}

func aggregateMemberRefs(p *Planner, request Request, resolved aggregateResolution) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{}
	for _, member := range resolved.Members {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: member.Name})
	}
	for name := range resolved.Aggregates {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: name})
	}
	for name := range resolved.Metrics {
		refs = append(refs, semanticAccessMemberRef{Kind: "metric", Name: name})
	}
	dataset := request.Dataset
	if dataset == "" && len(resolved.Datasets) == 1 {
		dataset = resolved.Datasets[0]
	}
	for _, dimension := range resolved.Dimensions {
		refs = append(refs, semanticAccessMemberRef{Kind: "dimension", Name: dimension.Name, Dataset: dataset})
	}
	appendFilterMemberRefs(p, &refs, request.Filters, dataset)
	appendSortMemberRefs(p, &refs, request.Sort, dataset)
	return refs
}

func bundleMemberRefs(p *Planner, requests []BundleRequest, resolutions []aggregateResolution) []semanticAccessMemberRef {
	refs := []semanticAccessMemberRef{}
	for i, request := range requests {
		refs = append(refs, aggregateMemberRefs(p, request.Request, resolutions[i])...)
	}
	return refs
}
