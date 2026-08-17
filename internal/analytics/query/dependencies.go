package query

import (
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// Dependencies is the shared, authorization-safe semantic resolution result.
// It intentionally contains both logical members and their physical lineage so
// callers never need to infer governance scope from user-supplied names.
type Dependencies struct {
	LogicalFields      []string
	MetricDependencies []string
	Facts              []string
	PhysicalFields     []string
	RelationshipPaths  []string
}

func ResolveDependencies(model *semanticmodel.Model, request Request) (Dependencies, error) {
	planner := NewPlanner(model)
	resolved, err := planner.resolveAggregate(request)
	if err != nil {
		return Dependencies{}, err
	}
	logical := map[string]struct{}{}
	metricDependencies := map[string]struct{}{}
	physical := map[string]struct{}{}
	paths := map[string]struct{}{}
	for _, dimension := range resolved.Dimensions {
		logical[dimension.Name] = struct{}{}
		for _, fact := range resolved.Facts {
			field, path, err := planner.aggregateDimensionBinding(fact, dimension)
			if err != nil {
				return Dependencies{}, err
			}
			physical[field] = struct{}{}
			if signature := relationshipPathSignature(path); signature != "" {
				paths[fact+":"+signature] = struct{}{}
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
			resolvedField, err := model.ResolveDimension(field)
			if err != nil {
				return Dependencies{}, err
			}
			path, err := model.SafeRelationshipPath(metric.Fact, resolvedField.Table)
			if err != nil {
				return Dependencies{}, err
			}
			if signature := relationshipPathSignature(path); signature != "" {
				paths[metric.Fact+":"+signature] = struct{}{}
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
	for _, fact := range resolved.Facts {
		bindings, err := planner.factFilterFields(request.Filters, resolved, fact)
		if err != nil {
			return Dependencies{}, err
		}
		for _, binding := range bindings {
			physical[binding.Field] = struct{}{}
			path := binding.Path
			if signature := relationshipPathSignature(path); signature != "" {
				paths[fact+":"+signature] = struct{}{}
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
		Facts:              append([]string{}, resolved.Facts...),
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
