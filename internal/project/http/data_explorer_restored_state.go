package http

import (
	"fmt"
	"strings"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// validateRestoredDataExploreState verifies every durable URL operand against
// the authorized active-generation projection. The normal command path keeps
// its incremental normalization, but a URL restore must never turn a stale
// operand into a smaller query by dropping it.
func validateRestoredDataExploreState(command projectsignals.DataExploreCommand, projection DataExplorerProjection, model *semanticmodel.Model, compiledModels map[string]*semanticquery.CompiledModel) error {
	spec := normalizeExplorationSpec(command.Spec)
	state := dataExploreStateFromSpec(spec)
	semanticModelID := strings.TrimSpace(spec.ModelID)
	selectedSemanticModelID := strings.TrimSpace(projection.Command.Spec.ModelID)

	if semanticModelID != "" {
		if !explorerSemanticModelByID(projection.SemanticModels, semanticModelID) {
			return fmt.Errorf("semantic model %q is no longer available; choose an active semantic model", semanticModelID)
		}
		if selectedSemanticModelID != semanticModelID {
			return fmt.Errorf("semantic model %q could not be restored; choose an active semantic model", semanticModelID)
		}
	}
	if selectedSemanticModelID == "" {
		return fmt.Errorf("no active semantic model is available; choose an active semantic model")
	}
	compiled := compiledModels[selectedSemanticModelID]
	if compiled == nil || len(compiled.DatasetNames()) == 0 {
		return fmt.Errorf("semantic model %q has no active compiled definition; reload the explorer after the serving state is ready", selectedSemanticModelID)
	}
	if model != nil {
		// Legacy explore URLs historically allowed the model operand to be
		// omitted; projection selects the first visible active model in that
		// case. Validate the restored operands against that selected model while
		// retaining the authored spec unchanged for the later projection.
		validationSpec := spec
		if semanticModelID == "" {
			validationSpec.ModelID = selectedSemanticModelID
		}
		if err := exploration.ValidateAgainstModel(model, &validationSpec); err != nil {
			return fmt.Errorf("exploration state: %w; remove it from the URL or choose an active field", err)
		}
	}

	datasetID := strings.TrimSpace(projectsignals.ValueOrZero(spec.DatasetID))
	if datasetID != "" {
		if !explorerDatasetByID(projection.Datasets, datasetID) || !compiledDataset(compiled, datasetID) {
			return fmt.Errorf("dataset %q is no longer available in semantic model %q; choose an active dataset", datasetID, selectedSemanticModelID)
		}
	}

	fieldByID := make(map[string]projectsignals.DataExploreFieldSignal, len(projection.Fields))
	for _, field := range projection.Fields {
		fieldByID[field.ID] = field
	}
	for index, dimension := range spec.Dimensions {
		if dimension.Grain == nil {
			continue
		}
		field, err := restoredExploreField(dimension.Field, fmt.Sprintf("dimension %d", index+1), "dimension", fieldByID)
		if err != nil {
			return err
		}
		grain := string(*dimension.Grain)
		if !restoredTimeGrain(grain) {
			return fmt.Errorf("dimension field %q uses unsupported grain %q; choose a supported time grain", dimension.Field, grain)
		}
		if declared, supported := restoredCompiledSemanticTimeGrain(compiled, dimension.Field, grain); declared && !supported {
			return fmt.Errorf("dimension field %q does not support grain %q in the active semantic model; choose a supported grain", dimension.Field, grain)
		}
		fieldType := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(field.Type)))
		if !isRestoredTemporalFieldType(fieldType) {
			return fmt.Errorf("dimension field %q is not a date or timestamp dimension; choose a temporal field", dimension.Field)
		}
	}
	filterDatasets := restoredFilterDatasetParticipation(command, projection, model, fieldByID)
	seenFields := make(map[string]string, len(state.Dimensions)+len(state.Metrics))
	selectedReferences := make(map[string]string, len(spec.Dimensions)+len(spec.Metrics))
	for _, fieldID := range state.Dimensions {
		if err := validateRestoredExploreField(fieldID, "dimension", fieldByID); err != nil {
			return err
		}
		if previous := seenFields[fieldID]; previous != "" {
			return fmt.Errorf("dimension field %q is selected more than once; remove the duplicate from the URL", fieldID)
		}
		seenFields[fieldID] = "dimension"
	}
	for _, dimension := range spec.Dimensions {
		selectedReferences[dimension.Field] = dimension.Field
		if dimension.Alias != nil {
			selectedReferences[*dimension.Alias] = dimension.Field
		}
	}
	for _, fieldID := range state.Metrics {
		if err := validateRestoredExploreField(fieldID, "metric", fieldByID); err != nil {
			return err
		}
		if previous := seenFields[fieldID]; previous != "" {
			return fmt.Errorf("metric field %q is selected more than once; remove the duplicate from the URL", fieldID)
		}
		seenFields[fieldID] = "metric"
	}
	for _, metric := range spec.Metrics {
		selectedReferences[metric.Field] = metric.Field
		if metric.Alias != nil {
			selectedReferences[*metric.Alias] = metric.Field
		}
	}
	// Time-only selections are valid sort targets too. Keep both the authored
	// field and its alias in the same reference map used by dimensions and
	// metrics before validating the ordered sort operands.
	if spec.Time != nil {
		selectedReferences[spec.Time.Field] = spec.Time.Field
		if spec.Time.Alias != nil {
			selectedReferences[*spec.Time.Alias] = spec.Time.Field
		}
	}
	for index, filter := range state.Filters {
		_, err := restoredExploreField(filter.Field, "filter", "dimension", fieldByID)
		if err != nil {
			return fmt.Errorf("filter %d: %w", index+1, err)
		}
		if err := validateRestoredExploreFilter(index, filter); err != nil {
			return err
		}
		if filter.Dataset == nil || strings.TrimSpace(projectsignals.ValueOrZero(filter.Dataset)) == "" {
			if filter.Dataset != nil {
				return fmt.Errorf("filter %d dataset is empty; remove the stale filter or choose an active dataset", index+1)
			}
		} else {
			filterDataset := strings.TrimSpace(projectsignals.ValueOrZero(filter.Dataset))
			if !explorerDatasetByID(projection.Datasets, filterDataset) || !compiledDataset(compiled, filterDataset) {
				return fmt.Errorf("filter %d dataset %q is no longer available; remove the stale filter or choose an active dataset", index+1, filterDataset)
			}
			if !filterDatasets[filterDataset] {
				return fmt.Errorf("filter %d dataset %q does not participate in the restored query; choose the active query dataset or a selected metric root", index+1, filterDataset)
			}
		}
	}
	for index, sorting := range state.Sort {
		resolvedSortField := sorting.Field
		if selected, ok := selectedReferences[sorting.Field]; ok {
			resolvedSortField = selected
		}
		if _, err := restoredExploreField(resolvedSortField, fmt.Sprintf("sort %d", index+1), "", fieldByID); err != nil {
			return err
		}
		if sorting.Direction != "asc" && sorting.Direction != "desc" {
			return fmt.Errorf("sort %d uses unsupported direction %q; choose asc or desc", index+1, sorting.Direction)
		}
		if _, selected := selectedReferences[sorting.Field]; !selected {
			return fmt.Errorf("sort %d field %q is not selected; choose a selected dimension or metric", index+1, sorting.Field)
		}
	}
	if state.Time != nil {
		field, err := restoredExploreField(state.Time.Field, "time", "dimension", fieldByID)
		if err != nil {
			return err
		}
		if !restoredTimeGrain(state.Time.Grain) {
			return fmt.Errorf("time field %q uses unsupported grain %q; choose a supported time grain", state.Time.Field, state.Time.Grain)
		}
		if declared, supported := restoredCompiledSemanticTimeGrain(compiled, state.Time.Field, state.Time.Grain); declared && !supported {
			return fmt.Errorf("time field %q does not support grain %q in the active semantic model; choose a supported grain", state.Time.Field, state.Time.Grain)
		}
		fieldType := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(field.Type)))
		if !isRestoredTemporalFieldType(fieldType) {
			return fmt.Errorf("time field %q is not a date or timestamp dimension; choose a temporal field", state.Time.Field)
		}
	}
	return nil
}

// restoredFilterDatasetParticipation returns the datasets that an explicit
// filter scope may legally name after projection has selected/rebased the
// effective query base. A normal query has one participating base. When a
// selected metric spans multiple roots, the semantic executor clears the
// dataset target and the complete recursive root union is the safe scope.
func restoredFilterDatasetParticipation(command projectsignals.DataExploreCommand, projection DataExplorerProjection, model *semanticmodel.Model, fields map[string]projectsignals.DataExploreFieldSignal) map[string]bool {
	participating := map[string]bool{}
	effectiveDataset := strings.TrimSpace(projectsignals.ValueOrZero(projection.Command.Spec.DatasetID))
	state := dataExploreStateFromSpec(command.Spec)
	if !explorerCommandHasMultiRootMetric(state.Metrics, fields) {
		if effectiveDataset != "" {
			participating[effectiveDataset] = true
		}
		return participating
	}
	for _, metric := range state.Metrics {
		for _, root := range explorerMetricRootDatasets(model, metric) {
			participating[root] = true
		}
	}
	return participating
}

func validateRestoredExploreFilter(index int, filter dataExploreFilter) error {
	operator := strings.TrimSpace(filter.Operator)
	valueCount := len(filter.Values)
	requiresOne := false
	requiresZero := false
	switch operator {
	case "equals", "not_equals", "contains", "not_contains", "starts_with", "ends_with", "greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
		requiresOne = true
	case "in", "not_in":
		if valueCount == 0 {
			return fmt.Errorf("filter %d operator %q requires at least one value; update or remove the stale filter", index+1, operator)
		}
	case "unfiltered":
		if valueCount != 0 {
			return fmt.Errorf("filter %d unfiltered expression does not accept values; update or remove the stale filter", index+1)
		}
	case "range":
		// The canonical range bounds are validated and lowered directly from
		// the spec; the compatibility view intentionally carries no values.
	case "is_null", "is_not_null":
		requiresZero = true
	default:
		return fmt.Errorf("filter %d uses unsupported operator %q; choose a supported filter operator", index+1, filter.Operator)
	}
	if requiresOne && valueCount != 1 {
		return fmt.Errorf("filter %d operator %q requires exactly one value; update or remove the stale filter", index+1, operator)
	}
	if requiresZero && valueCount != 0 {
		return fmt.Errorf("filter %d operator %q does not accept values; update or remove the stale filter", index+1, operator)
	}
	return nil
}

// restoredCompiledSemanticTimeGrain mirrors planner resolution: only a
// semantic-dimension reference is subject to that dimension's declared grain
// contract. A physical binding with the same authored semantic dimension is
// validated using the planner's global grain vocabulary and physical type.
func restoredCompiledSemanticTimeGrain(compiled *semanticquery.CompiledModel, field, grain string) (declared, supported bool) {
	if compiled == nil {
		return false, false
	}
	field = strings.TrimSpace(field)
	grain = strings.TrimSpace(grain)
	dimension, ok := compiled.SemanticDimension(field)
	if !ok {
		return false, false
	}
	declared = true
	for _, candidate := range dimension.Grains {
		if strings.TrimSpace(candidate) == grain {
			supported = true
		}
	}
	return declared, supported
}

func explorerSemanticModelByID(models []projectsignals.DataExploreSemanticModelSignal, id string) bool {
	for _, model := range models {
		if model.ID == id {
			return true
		}
	}
	return false
}

func explorerDatasetByID(datasets []projectsignals.DataExploreDatasetSignal, id string) bool {
	for _, dataset := range datasets {
		if dataset.ID == id {
			return true
		}
	}
	return false
}

func compiledDataset(compiled *semanticquery.CompiledModel, id string) bool {
	if compiled == nil {
		return false
	}
	_, ok := compiled.Dataset(id)
	return ok
}

func validateRestoredExploreField(fieldID, expectedKind string, fields map[string]projectsignals.DataExploreFieldSignal) error {
	_, err := restoredExploreField(fieldID, expectedKind, expectedKind, fields)
	return err
}

func restoredExploreField(fieldID, operand, expectedKind string, fields map[string]projectsignals.DataExploreFieldSignal) (projectsignals.DataExploreFieldSignal, error) {
	fieldID = strings.TrimSpace(fieldID)
	field, ok := fields[fieldID]
	if !ok {
		return projectsignals.DataExploreFieldSignal{}, fmt.Errorf("%s field %q is no longer available in the active semantic model; remove it from the URL or reload the explorer", operand, fieldID)
	}
	if expectedKind != "" && field.Kind != expectedKind {
		return projectsignals.DataExploreFieldSignal{}, fmt.Errorf("%s field %q is a %s, not a %s; choose a %s field", operand, fieldID, field.Kind, expectedKind, expectedKind)
	}
	if !field.Compatible {
		reason := strings.TrimSpace(projectsignals.ValueOrZero(field.CompatibilityReason))
		if reason == "" {
			reason = "it is incompatible with the restored dataset"
		}
		return projectsignals.DataExploreFieldSignal{}, fmt.Errorf("%s field %q is incompatible: %s; choose a compatible field or dataset", operand, fieldID, reason)
	}
	return field, nil
}

func containsRestoredExploreSelection(value string, dimensions, metrics []string) bool {
	for _, selected := range append(append([]string(nil), dimensions...), metrics...) {
		if selected == value {
			return true
		}
	}
	return false
}

func restoredTimeGrain(grain string) bool {
	switch strings.TrimSpace(grain) {
	case "second", "minute", "hour", "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}
