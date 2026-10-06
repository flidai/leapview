// Package explorehandoff projects the subset of compiled dashboard visual
// queries that Data Explorer can reproduce without changing query semantics.
package explorehandoff

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

// SpecForVisual returns a canonical Data Explorer spec only for a standard,
// single-query aggregate or records query. Matrix, pivot, spatial, and
// statistical aggregate queries cannot be represented faithfully.
func SpecForVisual(definition visualizationdefinition.Definition, model *semanticmodel.Model) (exploration.ExplorationSpec, bool) {
	query := definition.Query
	if query.Kind == visualizationdefinition.QueryDetail {
		return recordsSpecForVisual(definition, model)
	}
	if query.Kind != visualizationdefinition.QueryAggregate || query.Aggregate == nil || len(definition.SecondaryQueries) != 0 || model == nil {
		return exploration.ExplorationSpec{}, false
	}
	aggregate := query.Aggregate
	if aggregate.Histogram != nil || aggregate.Distribution != nil || aggregate.Limit < 1 || aggregate.Limit > 1000 {
		return exploration.ExplorationSpec{}, false
	}

	// Query.DatasetID names the visualization's local dataset slot (usually
	// "primary"). Explorer expects the semantic dataset key from the compiled
	// aggregate binding instead.
	datasetID := strings.TrimSpace(aggregate.TableID)
	modelID := strings.TrimSpace(query.ModelID)
	if datasetID == "" || modelID == "" {
		return exploration.ExplorationSpec{}, false
	}
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       modelID,
		DatasetID:     &datasetID,
		Dimensions:    make([]exploration.ExplorationDimensionRef, 0, len(aggregate.Dimensions)+1),
		Metrics:       make([]exploration.ExplorationMetricRef, 0, len(aggregate.Metrics)),
		Filters:       []exploration.ExplorationFilter{},
		Sort:          make([]exploration.ExplorationSort, 0, len(aggregate.Sort)),
		Limit:         int32(aggregate.Limit),
	}
	fieldTargets := make(map[string]string, len(aggregate.Dimensions)*2+len(aggregate.Metrics)*2+2)
	ambiguousTargets := map[string]bool{}
	rememberTarget := func(source, target string) {
		source = strings.TrimSpace(source)
		if source == "" {
			return
		}
		if previous, exists := fieldTargets[source]; exists && previous != target {
			ambiguousTargets[source] = true
			return
		}
		fieldTargets[source] = target
	}
	appendDimension := func(field visualizationdefinition.FieldBinding) bool {
		if !validFieldBinding(field) {
			return false
		}
		canonical, ok := canonicalExplorerDimensionID(model, datasetID, field.FieldID)
		if !ok {
			return false
		}
		reference := dimensionRef(field)
		reference.Field = canonical
		spec.Dimensions = append(spec.Dimensions, reference)
		rememberTarget(field.FieldID, canonical)
		rememberTarget(field.Alias, canonical)
		return true
	}
	for _, field := range aggregate.Dimensions {
		if !appendDimension(field) {
			return exploration.ExplorationSpec{}, false
		}
	}
	if aggregate.Series != nil {
		if !appendDimension(*aggregate.Series) {
			return exploration.ExplorationSpec{}, false
		}
	}
	for _, field := range aggregate.Metrics {
		metric, err := model.ResolveMetric(strings.TrimSpace(field.FieldID))
		if !validFieldBinding(field) || err != nil || metric.Hidden {
			return exploration.ExplorationSpec{}, false
		}
		spec.Metrics = append(spec.Metrics, exploration.ExplorationMetricRef{Field: field.FieldID, Alias: optionalAlias(field.Alias)})
		rememberTarget(field.FieldID, field.FieldID)
		rememberTarget(field.Alias, field.FieldID)
	}
	if aggregate.Time != nil {
		field := visualizationdefinition.FieldBinding{FieldID: aggregate.Time.FieldID, Alias: aggregate.Time.Alias}
		if !validFieldBinding(field) {
			return exploration.ExplorationSpec{}, false
		}
		canonical, ok := canonicalExplorerDimensionID(model, datasetID, field.FieldID)
		if !ok {
			return exploration.ExplorationSpec{}, false
		}
		grain := exploration.ExplorationTimeGrain(aggregate.Time.Grain)
		spec.Time = &exploration.ExplorationTimeSelection{Field: canonical, Alias: optionalAlias(aggregate.Time.Alias), Grain: grain}
		rememberTarget(field.FieldID, canonical)
		rememberTarget(field.Alias, canonical)
	}
	for _, sort := range aggregate.Sort {
		if strings.TrimSpace(sort.FieldID) == "" || (sort.Direction != "asc" && sort.Direction != "desc") {
			return exploration.ExplorationSpec{}, false
		}
		field := strings.TrimSpace(sort.FieldID)
		if ambiguousTargets[field] {
			return exploration.ExplorationSpec{}, false
		}
		if canonical, ok := fieldTargets[field]; ok {
			field = canonical
		}
		spec.Sort = append(spec.Sort, exploration.ExplorationSort{Field: field, Direction: exploration.ExplorationSortDirection(sort.Direction)})
	}
	if err := exploration.ValidateShape(&spec); err != nil {
		return exploration.ExplorationSpec{}, false
	}
	return spec, true
}

// canonicalExplorerDimensionID maps a compiled dashboard semantic dimension
// to the identifier Data Explorer publishes for its authorized field. Local
// and conformed semantic dimensions are projected as their bound physical
// field when that binding is available; explicit physical references are
// preserved. Bare physical references are scoped to the compiled dataset.
func canonicalExplorerDimensionID(model *semanticmodel.Model, datasetID, fieldID string) (string, bool) {
	fieldID = strings.TrimSpace(fieldID)
	if fieldID == "" || model == nil {
		return "", false
	}
	if _, err := model.ResolveDimension(fieldID); err == nil {
		return fieldID, true
	}
	if semantic, err := model.ResolveSemanticDimension(fieldID); err == nil {
		// The Explorer projection suppresses semantic aliases when their
		// physical binding is present. Protected aliases also have a
		// principal-specific projection that this model-only helper cannot
		// prove. Preserve the semantic ID when temporal rules or grants
		// distinguish it from its underlying physical binding.
		if semanticDimensionHasNonPhysicalTimeSemantics(semantic) || !model.AccessPolicy.Empty() {
			_, bound := semantic.Bindings[datasetID]
			return fieldID, bound
		}
		binding, exists := semantic.Bindings[datasetID]
		if !exists {
			return "", false
		}
		physical := strings.TrimSpace(binding.Field)
		if physical == "" {
			return "", false
		}
		if !strings.Contains(physical, ".") {
			physical = datasetID + "." + physical
		}
		if _, err := model.ResolveDimension(physical); err != nil {
			return "", false
		}
		return physical, true
	}
	qualified := datasetID + "." + fieldID
	if _, err := model.ResolveDimension(qualified); err != nil {
		return "", false
	}
	return qualified, true
}

func semanticDimensionHasNonPhysicalTimeSemantics(dimension semanticmodel.SemanticDimension) bool {
	datatype := strings.ToLower(string(dimension.Datatype))
	if datatype == "" {
		datatype = strings.ToLower(strings.TrimSpace(dimension.Type))
	}
	switch datatype {
	case "date", "datetime", "datetimetz", "timestamp":
	default:
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(dimension.Timezone), "UTC") ||
		!strings.EqualFold(strings.TrimSpace(dimension.Calendar), "gregorian") ||
		!strings.EqualFold(strings.TrimSpace(dimension.WeekStart), "sunday")
}

func validFieldBinding(field visualizationdefinition.FieldBinding) bool {
	return strings.TrimSpace(field.FieldID) != "" && strings.TrimSpace(field.Alias) != ""
}

func optionalAlias(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func dimensionRef(field visualizationdefinition.FieldBinding) exploration.ExplorationDimensionRef {
	result := exploration.ExplorationDimensionRef{Field: field.FieldID, Alias: optionalAlias(field.Alias)}
	if grain := strings.TrimSpace(field.Grain); grain != "" {
		value := exploration.ExplorationTimeGrain(grain)
		result.Grain = &value
	}
	return result
}

// RouteHref binds a visual handoff to the live dashboard session. The route
// re-reads that session before redirecting, so the destination always carries
// the currently applied filters and representable interactions.
func RouteHref(basePath, dashboardID, pageID, visualID, clientID, streamInstanceID string) (string, bool) {
	// Candidate/preview dashboards live under a scoped route prefix, but that
	// router intentionally does not expose the authenticated Explorer route.
	// Do not publish a dead handoff affordance from those projections.
	if strings.TrimSpace(basePath) != "" {
		return "", false
	}
	for _, value := range []string{dashboardID, pageID, visualID, clientID, streamInstanceID} {
		if strings.TrimSpace(value) == "" {
			return "", false
		}
	}
	basePath = strings.TrimSuffix(strings.TrimSpace(basePath), "/")
	path := basePath + "/dashboards/" + url.PathEscape(dashboardID) + "/pages/" + url.PathEscape(pageID) + "/visuals/" + url.PathEscape(visualID) + "/explore"
	query := url.Values{}
	query.Set("clientId", clientID)
	query.Set("streamInstanceId", streamInstanceID)
	return path + "?" + query.Encode(), true
}

// TargetHref builds the canonical v2 Explorer URL and a same-origin return
// path to the source visual.
func TargetHref(basePath, dashboardID, pageID, visualID string, spec exploration.ExplorationSpec) (string, error) {
	state, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode exploration state: %w", err)
	}
	basePath = strings.TrimSuffix(strings.TrimSpace(basePath), "/")
	returnTo := basePath + "/dashboards/" + url.PathEscape(dashboardID) + "/pages/" + url.PathEscape(pageID)
	query := url.Values{}
	query.Set("mode", "explore")
	query.Set("v", "2")
	query.Set("state", string(state))
	query.Set("returnTo", returnTo)
	query.Set("sourceVisual", visualID)
	return basePath + "/explore?" + query.Encode(), nil
}
