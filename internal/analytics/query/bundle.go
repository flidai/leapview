package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// PlanBundle lowers every aggregate branch through validated PlanIR and emits
// one deterministic DuckDB statement. Branches remain independently shaped;
// the renderer gives each branch a typed physical column namespace so unlike
// branch values cannot be coerced by a UNION.
func (p *Planner) PlanBundle(requests []BundleRequest) (BundlePlan, error) {
	if len(requests) == 0 {
		return BundlePlan{}, fmt.Errorf("aggregate bundle requires at least one request")
	}
	resolutions := make([]aggregateResolution, len(requests))
	ids := map[string]bool{}
	scopeFingerprint := ""
	heterogeneousDatasets := false
	datasetSignature := ""
	for index, item := range requests {
		if item.ID == "" || ids[item.ID] {
			return BundlePlan{}, fmt.Errorf("aggregate bundle branch IDs must be non-empty and unique")
		}
		ids[item.ID] = true
		resolved, err := p.resolveAggregate(item.Request)
		if err != nil {
			return BundlePlan{}, fmt.Errorf("bundle branch %q: %w", item.ID, err)
		}
		if err := p.validateAggregateFilters(item.Request.Filters, resolved); err != nil {
			return BundlePlan{}, fmt.Errorf("bundle branch %q: %w", item.ID, err)
		}
		if len(resolved.Datasets) == 0 {
			return BundlePlan{}, fmt.Errorf("bundle branch %q has no participating dataset", item.ID)
		}
		// Shared bundles cannot preserve per-branch authorization masks without
		// widening the typed projection. Fail closed until that contract exists.
		if len(item.Request.ColumnMasks) > 0 {
			return BundlePlan{}, fmt.Errorf("bundle branch %q has column masks and is not safely bundleable", item.ID)
		}
		branchDatasets := strings.Join(resolved.Datasets, ",")
		if datasetSignature == "" {
			datasetSignature = branchDatasets
		} else if branchDatasets != datasetSignature {
			heterogeneousDatasets = true
		}
		fingerprint, err := p.bundleScopeFingerprint(item.Request, resolved)
		if err != nil {
			return BundlePlan{}, fmt.Errorf("bundle branch %q scope fingerprint: %w", item.ID, err)
		}
		if scopeFingerprint == "" {
			scopeFingerprint = fingerprint
		} else if !heterogeneousDatasets && fingerprint != scopeFingerprint {
			return BundlePlan{}, fmt.Errorf("bundle branch %q has a different governed scope", item.ID)
		}
		resolutions[index] = resolved
	}
	return p.renderBundlePlanIR(requests, resolutions)
}

// renderBundlePlanIR is the only production lowering for aggregate bundles.
func (p *Planner) renderBundlePlanIR(requests []BundleRequest, resolutions []aggregateResolution) (BundlePlan, error) {
	irGraph, err := p.buildBundlePlanIR(requests, resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	rendered, err := planir.RenderDuckDB(irGraph)
	if err != nil {
		return BundlePlan{}, fmt.Errorf("render bundle plan IR: %w", err)
	}
	projections, fingerprints, err := p.bundleBranchDependencyProjections(requests, resolutions)
	if err != nil {
		return BundlePlan{}, err
	}
	branches := make([]BundleBranch, len(requests))
	for index, item := range requests {
		columns := make([]BundleColumn, 0, len(resolutions[index].Dimensions)+len(resolutions[index].Members))
		for _, dimension := range resolutions[index].Dimensions {
			columns = append(columns, BundleColumn{Output: dimension.Alias, Physical: fmt.Sprintf("__bundle_%d_%s", index, dimension.Alias)})
		}
		for _, member := range resolutions[index].Members {
			columns = append(columns, BundleColumn{Output: member.Alias, Physical: fmt.Sprintf("__bundle_%d_%s", index, member.Alias)})
		}
		branches[index] = BundleBranch{
			ID: item.ID, Ordinal: index, Columns: columns,
			Fingerprint:          fingerprints[index],
			DependencyProjection: projections[index],
		}
	}
	lineage, err := irGraph.Dependencies()
	if err != nil {
		return BundlePlan{}, fmt.Errorf("derive bundle dependencies: %w", err)
	}
	datasets := []string{}
	datasetSet := map[string]bool{}
	multiDataset := false
	for _, resolved := range resolutions {
		multiDataset = multiDataset || resolved.MultiDataset
		for _, dataset := range resolved.Datasets {
			if !datasetSet[dataset] {
				datasetSet[dataset] = true
				datasets = append(datasets, dataset)
			}
		}
	}
	sort.Strings(datasets)
	mode := "single_dataset"
	if multiDataset || len(datasets) > 1 {
		mode = "multi_dataset"
	}
	return BundlePlan{Plan: Plan{
		SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, Deterministic: true, Mode: mode,
		Datasets: datasets, PhysicalDependencies: uniqueStrings(append(append([]string(nil), lineage.Datasets...), lineage.PhysicalFields...)),
		RelationshipPaths: lineage.RelationshipPaths, IR: irGraph,
	}, Branches: branches}, nil
}

// Decode restores each branch's authored aliases and drops the wide physical
// columns used to preserve types across unlike branch shapes.
func (b BundlePlan) Decode(rows Rows) (map[string]Rows, error) {
	byOrdinal := map[int]BundleBranch{}
	result := map[string]Rows{}
	for _, branch := range b.Branches {
		byOrdinal[branch.Ordinal] = branch
		result[branch.ID] = Rows{}
	}
	for _, row := range rows {
		ordinal, err := integerValue(row[BundleBranchColumn])
		if err != nil {
			return nil, err
		}
		branch, ok := byOrdinal[ordinal]
		if !ok {
			return nil, fmt.Errorf("unknown bundle branch ordinal %d", ordinal)
		}
		decoded := Row{}
		for _, column := range branch.Columns {
			decoded[column.Output] = row[column.Physical]
		}
		result[branch.ID] = append(result[branch.ID], decoded)
	}
	return result, nil
}

func integerValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil
	default:
		return 0, fmt.Errorf("bundle branch ordinal has type %T", value)
	}
}
