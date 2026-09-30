package module

import (
	"context"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type semanticCatalogSearchRuntime interface {
	ProjectManifest() projectmanifest.ResourceManifest
	CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
}

// SemanticCatalogSearchTerms projects names and labels from the active
// semantic model into catalog search. It uses the catalog lease and excludes
// hidden members and members protected by semantic access grants.
func SemanticCatalogSearchTerms() projectcatalog.SemanticModelSearchTerms {
	return func(_ context.Context, lease projectcatalog.Lease, _ string, modelID projectgraph.ResourceID) ([]string, error) {
		port, ok := lease.(interface{ Runtime() projectruntime.Runtime })
		if !ok || port.Runtime() == nil {
			return nil, projectcatalog.ErrUnavailable
		}
		runtime, ok := port.Runtime().(semanticCatalogSearchRuntime)
		if !ok {
			return nil, projectcatalog.ErrUnavailable
		}
		identity := lease.Identity()
		if err := identity.Validate(); err != nil {
			return nil, err
		}
		snapshot := lease.AuthorizationSnapshot()
		if snapshot.Identity() != identity || snapshot.ValidateBound() != nil {
			return nil, projectcatalog.ErrSnapshotChanged
		}
		model := runtime.ProjectManifest().SemanticModels[modelID.String()]
		compiled, available := runtime.CompiledSemanticModel(modelID.String())
		if model == nil || !available || compiled == nil || !compiled.MatchesModel(model) {
			return nil, projectcatalog.ErrUnavailable
		}
		return semanticSearchTerms(model, compiled), nil
	}
}

func semanticSearchTerms(model *semanticmodel.Model, compiled *semanticquery.CompiledModel) []string {
	if model == nil || compiled == nil || !compiled.MatchesModel(model) {
		return nil
	}
	protectedDimensions := make(map[string]struct{})
	for name, dimension := range model.Dimensions {
		if semanticDimensionSearchProtected(model, compiled, name, dimension) {
			protectedDimensions[name] = struct{}{}
		}
	}
	protectedMetrics := make(map[string]struct{})
	for name, metric := range model.Metrics {
		if metric.Hidden || len(model.AccessPolicy.Metrics[name]) > 0 || semanticMetricAccessProtected(model, compiled, name, metric, protectedDimensions) {
			protectedMetrics[name] = struct{}{}
		}
	}
	// Protected requirements flow through metric dependencies. Resolve the
	// closure before emitting any metric name or label.
	for changed := true; changed; {
		changed = false
		for name := range model.Metrics {
			if _, protected := protectedMetrics[name]; protected {
				continue
			}
			compiledMetric, ok := compiled.Metric(name)
			if !ok {
				protectedMetrics[name] = struct{}{}
				changed = true
				continue
			}
			for _, dependency := range compiledMetric.Dependencies {
				if _, protected := protectedMetrics[dependency]; protected {
					protectedMetrics[name] = struct{}{}
					changed = true
					break
				}
			}
		}
	}
	terms := make([]string, 0)
	for name, dimension := range model.Dimensions {
		if _, protected := protectedDimensions[name]; protected {
			continue
		}
		terms = appendSemanticSearchTerms(terms, name, dimension.Label, dimension.Description)
	}
	for name, metric := range model.Metrics {
		if _, protected := protectedMetrics[name]; protected {
			continue
		}
		terms = appendSemanticSearchTerms(terms, name, metric.Label, metric.Description)
	}
	return terms
}

func semanticMetricAccessProtected(model *semanticmodel.Model, compiled *semanticquery.CompiledModel, name string, metric semanticmodel.Metric, protectedDimensions map[string]struct{}) bool {
	compiledMetric, ok := compiled.Metric(name)
	if !ok {
		return true
	}
	if semanticDatasetSearchProtected(model, metric.Dataset) {
		return true
	}
	if compiledMetric.Aggregate != nil && semanticNameProtected(protectedDimensions, compiledMetric.Aggregate.TimeDimension) {
		return true
	}
	for _, filterName := range metric.Where {
		filter, ok := model.Filters[filterName]
		if !ok || semanticFilterUsesProtectedDimension(filter, model, metric.Dataset, protectedDimensions) {
			return true
		}
	}
	for _, dataset := range compiledMetric.RootDatasets {
		if semanticDatasetSearchProtected(model, dataset) {
			return true
		}
	}
	for _, lineage := range compiledMetric.Lineage.Entries {
		if semanticPathSearchProtected(model, lineage.Path) {
			return true
		}
	}
	return false
}

func semanticDatasetSearchProtected(model *semanticmodel.Model, dataset string) bool {
	policy, ok := model.AccessPolicy.Datasets[dataset]
	return ok && len(policy.RequiredAccessGrants) > 0
}

func semanticDimensionSearchProtected(model *semanticmodel.Model, compiled *semanticquery.CompiledModel, name string, dimension semanticmodel.SemanticDimension) bool {
	if len(model.AccessPolicy.Dimensions[name]) > 0 {
		return true
	}
	for dataset := range dimension.Bindings {
		if semanticDatasetSearchProtected(model, dataset) {
			return true
		}
		binding, ok := compiled.DimensionBinding(name, dataset)
		if !ok || semanticPathSearchProtected(model, binding.Path) {
			return true
		}
	}
	return false
}

func semanticPathSearchProtected(model *semanticmodel.Model, path []semanticmodel.Relationship) bool {
	for _, relationship := range path {
		if semanticDatasetSearchProtected(model, relationship.FromDataset) || semanticDatasetSearchProtected(model, relationship.ToDataset) {
			return true
		}
	}
	return false
}

func semanticFilterUsesProtectedDimension(filter semanticmodel.SemanticFilterSpec, model *semanticmodel.Model, dataset string, protectedDimensions map[string]struct{}) bool {
	if filter.Field != "" {
		_, semanticDimension := model.Dimensions[filter.Field]
		if !semanticDimension {
			if _, err := model.ResolveDimension(filter.Field); err != nil {
				return true
			}
		}
		if semanticNameProtected(protectedDimensions, filter.Field) {
			return true
		}
		for name := range protectedDimensions {
			binding, ok := model.Dimensions[name].Bindings[dataset]
			if ok && binding.Field == filter.Field && equalSemanticPath(binding.Path, filter.Path) {
				return true
			}
		}
	}
	for _, child := range filter.All {
		if semanticFilterUsesProtectedDimension(child, model, dataset, protectedDimensions) {
			return true
		}
	}
	for _, child := range filter.Any {
		if semanticFilterUsesProtectedDimension(child, model, dataset, protectedDimensions) {
			return true
		}
	}
	if filter.Not != nil && semanticFilterUsesProtectedDimension(*filter.Not, model, dataset, protectedDimensions) {
		return true
	}
	return false
}

func semanticNameProtected(protected map[string]struct{}, name string) bool {
	_, ok := protected[name]
	return ok
}

func equalSemanticPath(left, right []string) bool {
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

func appendSemanticSearchTerms(terms []string, values ...string) []string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			terms = append(terms, value)
		}
	}
	return terms
}
