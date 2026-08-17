package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// RepresentativePlan is a deterministic, non-executing deployment check for
// one semantic route. The SQL is prepared by the governed planner only; no
// authored SQL is evaluated by this verifier.
type RepresentativePlan struct {
	Route string
	Plan  Plan
}

// PrepareRepresentativePlans validates discovered schemas and prepares a
// representative governed plan for every metric dependency, semantic binding,
// reachable filter, and safe relationship route. Plans are returned in stable
// route order so deployment diagnostics do not depend on map iteration order.
func PrepareRepresentativePlans(model *semanticmodel.Model, relation TableRelation) ([]RepresentativePlan, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic verification: semantic model is required")
	}
	if err := model.ValidateDiscoveredSchemas(); err != nil {
		return nil, fmt.Errorf("semantic verification: discovered schema: %w", err)
	}
	options := []PlannerOption{}
	if relation != nil {
		options = append(options, WithTableRelation(relation))
	}
	planner, err := NewCompiledPlanner(model, options...)
	if err != nil {
		return nil, fmt.Errorf("semantic verification: compile planner: %w", err)
	}
	prepared := make([]RepresentativePlan, 0)
	add := func(route string, request Request) error {
		plan, err := planner.Plan(request)
		if err != nil {
			return fmt.Errorf("semantic verification route %s: %w", route, err)
		}
		prepared = append(prepared, RepresentativePlan{Route: route, Plan: plan})
		return nil
	}

	metricNames := sortedMetricNames(model)
	for _, metricName := range metricNames {
		analysis, err := planner.AnalyzeAggregate(Request{Metrics: []Field{{Field: metricName}}})
		if err != nil {
			return nil, fmt.Errorf("semantic verification metric %q dependencies: %w", metricName, err)
		}
		if err := add("metric:"+metricName, Request{Metrics: []Field{{Field: metricName}}}); err != nil {
			return nil, err
		}
		for _, dimensionName := range sortedDimensionNames(model) {
			if !dimensionCompatibleWithFacts(model, dimensionName, analysis.Facts) {
				continue
			}
			if err := add("metric:"+metricName+"/dimension:"+dimensionName, Request{
				Metrics: []Field{{Field: metricName}}, Dimensions: []Field{{Field: dimensionName, Alias: verificationAlias(dimensionName)}},
			}); err != nil {
				return nil, err
			}
		}
	}

	// Bindings are checked independently of metrics so a newly-authored safe
	// route cannot remain unplanned simply because no dashboard selected it yet.
	for _, dimensionName := range sortedSemanticDimensionNames(model) {
		dimension := model.Dimensions[dimensionName]
		facts := make([]string, 0, len(dimension.Bindings))
		for fact := range dimension.Bindings {
			facts = append(facts, fact)
		}
		sort.Strings(facts)
		for _, fact := range facts {
			if err := add("binding:"+dimensionName+"@"+fact, Request{
				Table: fact, Dimensions: []Field{{Field: dimensionName}},
			}); err != nil {
				return nil, err
			}
		}
	}

	// Every reusable filter is compiled and planned against each fact from
	// which its validated route is reachable. A filter can therefore fail
	// deployment even when it is not currently referenced by a metric.
	filterNames := make([]string, 0, len(model.Filters))
	for name := range model.Filters {
		filterNames = append(filterNames, name)
	}
	sort.Strings(filterNames)
	facts := model.FactNames()
	sort.Strings(facts)
	metricFacts := map[string][]string{}
	for _, metricName := range metricNames {
		analysis, analysisErr := planner.AnalyzeAggregate(Request{Metrics: []Field{{Field: metricName}}})
		if analysisErr == nil {
			metricFacts[metricName] = analysis.Facts
		}
	}
	for _, filterName := range filterNames {
		spec := model.Filters[filterName]
		compiled, err := compileSemanticFilter(model, spec)
		if err != nil {
			return nil, fmt.Errorf("semantic verification filter %q: %w", filterName, err)
		}
		planned := false
		for _, fact := range facts {
			if !semanticFilterReachable(model, spec, fact) {
				continue
			}
			request := Request{Table: fact, Filters: []Filter{compiled}}
			for _, metricName := range metricNames {
				if len(metricFacts[metricName]) == 1 && containsString(metricFacts[metricName], fact) {
					request.Metrics = []Field{{Field: metricName}}
					break
				}
			}
			if len(request.Metrics) == 0 {
				// A deterministic local grain dimension keeps this route a
				// governed aggregate even when no metric is rooted in the fact.
				for _, dimensionName := range sortedDimensionNames(model) {
					if strings.HasPrefix(dimensionName, fact+".") {
						request.Dimensions = []Field{{Field: dimensionName}}
						break
					}
				}
			}
			if err := add("filter:"+filterName+"@"+fact, request); err != nil {
				return nil, err
			}
			planned = true
		}
		if !planned {
			return nil, fmt.Errorf("semantic verification filter %q has no reachable fact route", filterName)
		}
	}

	// Explicit relationship declarations are validated as routes as well. If
	// an endpoint key is exposed as a dimension, prepare a query that forces the
	// relationship join; entity-only keys are still covered by schema and key
	// validation without inventing a queryable field.
	for _, relationship := range sortedRelationships(model.Relationships) {
		if relationship.FromDataset == "" || relationship.ToDataset == "" || len(relationship.ToFields) == 0 {
			continue
		}
		path := []semanticmodel.Relationship{relationship}
		if _, err := model.ResolveDimension(relationship.ToDataset + "." + relationship.ToFields[0]); err == nil {
			if _, err := model.ResolveBindingPath(relationship.FromDataset, semanticmodel.DimensionBinding{Field: relationship.ToDataset + "." + relationship.ToFields[0], Path: []string{relationship.ID}}); err != nil {
				return nil, fmt.Errorf("semantic verification relationship %q path: %w", relationship.ID, err)
			}
		}
		plan, err := prepareExplicitRelationshipPlan(model, relation, relationship.FromDataset, relationship.ToDataset, relationship.ToFields[0], path)
		if err != nil {
			return nil, fmt.Errorf("semantic verification route relationship:%s: %w", relationship.ID, err)
		}
		prepared = append(prepared, RepresentativePlan{Route: "relationship:" + relationship.ID, Plan: plan})
	}
	sort.SliceStable(prepared, func(i, j int) bool { return prepared[i].Route < prepared[j].Route })
	return prepared, nil
}

func verificationAlias(field string) string {
	field = strings.NewReplacer(".", "_", "-", "_").Replace(field)
	return "__verify_" + field
}

func prepareExplicitRelationshipPlan(model *semanticmodel.Model, relation TableRelation, from, to, field string, path []semanticmodel.Relationship) (Plan, error) {
	// Add a temporary physical dimension for entity-only endpoints so this
	// verification query still exercises the exact relationship path without
	// changing the authored model.
	candidate := *model
	tables := make(map[string]semanticmodel.Table, len(model.Tables))
	for name, table := range model.Tables {
		tableCopy := table
		tableCopy.Dimensions = make(map[string]semanticmodel.MetricDimension, len(table.Dimensions)+1)
		for dimensionName, dimension := range table.Dimensions {
			tableCopy.Dimensions[dimensionName] = dimension
		}
		if name == to {
			if _, ok := tableCopy.Dimensions[field]; !ok {
				tableCopy.Dimensions[field] = semanticmodel.MetricDimension{Field: to + "." + field, Table: to, Name: field, Type: "string"}
			}
		}
		tables[name] = tableCopy
	}
	candidate.Tables = tables
	dimensionName := "__verify_relationship_" + path[0].ID
	candidate.Dimensions = make(map[string]semanticmodel.SemanticDimension, len(model.Dimensions)+1)
	for name, dimension := range model.Dimensions {
		candidate.Dimensions[name] = dimension
	}
	candidate.Dimensions[dimensionName] = semanticmodel.SemanticDimension{Bindings: map[string]semanticmodel.DimensionBinding{
		from: {Field: to + "." + field, Path: relationshipIDs(path)},
	}}
	options := []PlannerOption{}
	if relation != nil {
		options = append(options, WithTableRelation(relation))
	}
	planner, err := NewCompiledPlanner(&candidate, options...)
	if err != nil {
		return Plan{}, err
	}
	return planner.Plan(Request{Table: from, Dimensions: []Field{{Field: dimensionName}}})
}

func relationshipIDs(path []semanticmodel.Relationship) []string {
	ids := make([]string, len(path))
	for index, relationship := range path {
		ids[index] = relationship.ID
	}
	return ids
}

func semanticFilterReachable(model *semanticmodel.Model, spec semanticmodel.SemanticFilterSpec, fact string) bool {
	for _, child := range spec.All {
		if !semanticFilterReachable(model, child, fact) {
			return false
		}
	}
	for _, child := range spec.Any {
		if semanticFilterReachable(model, child, fact) {
			return true
		}
	}
	if len(spec.Any) > 0 {
		return false
	}
	if spec.Not != nil {
		return semanticFilterReachable(model, *spec.Not, fact)
	}
	if spec.Field == "" {
		return true
	}
	dimension, err := model.ResolveDimension(spec.Field)
	if err != nil {
		return false
	}
	if len(spec.Path) > 0 {
		_, err = model.ResolveBindingPath(fact, semanticmodel.DimensionBinding{Field: spec.Field, Path: append([]string(nil), spec.Path...)})
		return err == nil
	}
	_, err = model.SafeRelationshipPath(fact, dimension.Table)
	return err == nil
}

// VerifyRepresentativePlans performs preparation and discards the SQL. It is
// the deployment-facing convenience API when callers only need validation.
func VerifyRepresentativePlans(model *semanticmodel.Model, relation TableRelation) error {
	_, err := PrepareRepresentativePlans(model, relation)
	return err
}

func sortedMetricNames(model *semanticmodel.Model) []string {
	names := make([]string, 0, len(model.Metrics))
	for name := range model.Metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedDimensionNames(model *semanticmodel.Model) []string {
	seen := map[string]struct{}{}
	for name := range model.Dimensions {
		seen[name] = struct{}{}
	}
	for tableName, table := range model.Tables {
		for field := range table.Dimensions {
			seen[tableName+"."+field] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedSemanticDimensionNames(model *semanticmodel.Model) []string {
	names := make([]string, 0, len(model.Dimensions))
	for name := range model.Dimensions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func dimensionCompatibleWithFacts(model *semanticmodel.Model, name string, facts []string) bool {
	if dimension, ok := model.Dimensions[name]; ok {
		for _, fact := range facts {
			binding, bound := dimension.Bindings[fact]
			if !bound {
				return false
			}
			if _, err := model.ResolveBindingPath(fact, binding); err != nil {
				return false
			}
		}
		return true
	}
	for _, fact := range facts {
		dimension, err := model.ResolveDimension(name)
		if err != nil {
			return false
		}
		if _, err := model.SafeRelationshipPath(fact, dimension.Table); err != nil {
			return false
		}
	}
	return len(facts) > 0
}

func sortedRelationships(relationships []semanticmodel.Relationship) []semanticmodel.Relationship {
	result := append([]semanticmodel.Relationship(nil), relationships...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
