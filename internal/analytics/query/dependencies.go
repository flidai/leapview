package query

import (
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// Dependencies is the shared, authorization-safe semantic resolution result.
// It intentionally contains both logical members and their physical lineage so
// callers never need to infer governance scope from user-supplied names.
type Dependencies struct {
	LogicalFields      []string
	MetricDependencies []string
	Datasets           []string
	PhysicalFields     []string
	RelationshipPaths  []string
}

// ResolveDependencies resolves request lineage against an already activated
// planner. Serving request paths must retain and use this planner so semantic
// metadata is never compiled outside serving-state activation.
func (p *Planner) ResolveDependencies(request Request) (Dependencies, error) {
	if p == nil || p.compiled == nil || p.model == nil {
		return Dependencies{}, fmt.Errorf("planner is not compiled")
	}
	resolved, err := p.resolveAggregate(request)
	if err != nil {
		return Dependencies{}, err
	}
	logical := map[string]struct{}{}
	metricDependencies := map[string]struct{}{}
	physical := map[string]struct{}{}
	paths := map[string]struct{}{}
	for _, dimension := range resolved.Dimensions {
		logical[dimension.Name] = struct{}{}
		for _, dataset := range resolved.Datasets {
			field, path, err := p.aggregateDimensionBinding(dataset, dimension)
			if err != nil {
				return Dependencies{}, err
			}
			physical[field] = struct{}{}
			if signature := relationshipPathSignature(path); signature != "" {
				paths[dataset+":"+signature] = struct{}{}
			}
			for _, relationship := range path {
				for _, field := range relationshipPhysicalFields(relationship) {
					physical[field] = struct{}{}
				}
			}
		}
	}
	for name, metric := range resolved.Aggregates {
		logical[name] = struct{}{}
		for _, field := range aggregateMetricPhysicalFields(metric) {
			physical[field] = struct{}{}
			resolvedField, err := p.resolveDimension(field)
			if err != nil {
				return Dependencies{}, err
			}
			path, err := p.model.SafeRelationshipPath(metric.Dataset, resolvedField.Table)
			if err != nil {
				return Dependencies{}, err
			}
			if signature := relationshipPathSignature(path); signature != "" {
				paths[metric.Dataset+":"+signature] = struct{}{}
			}
			for _, relationship := range path {
				for _, field := range relationshipPhysicalFields(relationship) {
					physical[field] = struct{}{}
				}
			}
		}
	}
	for name, expression := range resolved.Metrics {
		logical[name] = struct{}{}
		for _, ref := range expression.References() {
			metricDependencies[ref] = struct{}{}
		}
	}
	for _, dataset := range resolved.Datasets {
		bindings, err := p.datasetFilterFields(request.Filters, resolved, dataset)
		if err != nil {
			return Dependencies{}, err
		}
		for _, binding := range bindings {
			physical[binding.Field] = struct{}{}
			path := binding.Path
			if signature := relationshipPathSignature(path); signature != "" {
				paths[dataset+":"+signature] = struct{}{}
			}
			for _, relationship := range path {
				for _, field := range relationshipPhysicalFields(relationship) {
					physical[field] = struct{}{}
				}
			}
		}
	}
	for _, ref := range filterRefs(request.Filters) {
		logical[ref] = struct{}{}
	}
	return Dependencies{
		LogicalFields:      sortedSet(logical),
		MetricDependencies: sortedSet(metricDependencies),
		Datasets:           append([]string{}, resolved.Datasets...),
		PhysicalFields:     sortedSet(physical),
		RelationshipPaths:  sortedSet(paths),
	}, nil
}

func relationshipPhysicalFields(relationship semanticmodel.Relationship) []string {
	fields := []string{}
	for _, from := range []bool{true, false} {
		dataset, tuple, err := semanticmodel.RelationshipEndpoint(relationship, from)
		if err != nil {
			continue
		}
		for _, field := range tuple {
			fields = append(fields, dataset+"."+field)
		}
	}
	return fields
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
