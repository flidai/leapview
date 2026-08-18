// Package signals owns dashboard browser contract projections.
package signals

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
)

func optionalValue[T comparable](value T) *T {
	var zero T
	if value == zero {
		return nil
	}
	return &value
}

func optionalSlice[T any](value []T) *[]T {
	if len(value) == 0 {
		return nil
	}
	copyValue := append([]T(nil), value...)
	return &copyValue
}

func optionalMap[K comparable, V any](value map[K]V) *map[K]V {
	if len(value) == 0 {
		return nil
	}
	copyValue := make(map[K]V, len(value))
	for key, item := range value {
		copyValue[key] = item
	}
	return &copyValue
}

func Pointer[T any](value T) *T {
	return &value
}

func Optional[T comparable](value T) *T {
	return optionalValue(value)
}

func OptionalSlice[T any](value []T) *[]T {
	return optionalSlice(value)
}

func ValueOrZero[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

func DashboardPageCanvasFromDashboard(value dashboard.PageCanvas) DashboardPageCanvas {
	return DashboardPageCanvas{Width: int64(value.Width), Height: int64(value.Height)}
}

func DashboardPageGridFromDashboard(value dashboard.PageGrid) DashboardPageGrid {
	return DashboardPageGrid{Columns: int64(value.Columns), RowHeight: int64(value.RowHeight), Gap: int64(value.Gap), Padding: int64(value.Padding)}
}

func DashboardPagePlacementFromDashboard(value dashboard.PagePlacement) DashboardPagePlacement {
	return DashboardPagePlacement{Col: int64(value.Col), Row: int64(value.Row), ColSpan: int64(value.ColSpan), RowSpan: int64(value.RowSpan)}
}

func DashboardStatusFromDashboard(value dashboard.Status) DashboardStatus {
	return DashboardStatus{
		Loading: value.Loading, Error: value.Error, RefreshID: value.RefreshID, Generation: value.Generation, LastUpdated: value.LastUpdated,
		SetupRequired:   value.SetupRequired,
		ProgressPercent: ValueOrZero(dashboard.NormalizeProgressPercent(value.ProgressPercent, value.Loading)),
	}
}

func DashboardFilterExpressionFromDomain(value dashboardfilter.Expression) DashboardFilterExpression {
	bytes, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode compiled filter expression: %v", err))
	}
	var out DashboardFilterExpression
	if err := json.Unmarshal(bytes, &out); err != nil {
		panic(fmt.Sprintf("convert compiled filter expression: %v", err))
	}
	return out
}

func DashboardFilterValueFromDomain(value dashboardfilter.Value) DashboardFilterValue {
	bytes, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encode compiled filter value: %v", err))
	}
	var out DashboardFilterValue
	if err := json.Unmarshal(bytes, &out); err != nil {
		panic(fmt.Sprintf("convert compiled filter value: %v", err))
	}
	return out
}

func DashboardFilterStateFromDomain(value dashboardfilter.State) DashboardFilterState {
	applied := make(map[string]DashboardAppliedFilterState, len(value.AppliedControls))
	for key, item := range value.AppliedControls {
		var evaluatedAt *string
		if item.EvaluatedAt != nil {
			text := item.EvaluatedAt.UTC().Format(time.RFC3339Nano)
			evaluatedAt = &text
		}
		applied[key] = DashboardAppliedFilterState{
			Expression:         DashboardFilterExpressionFromDomain(item.Expression),
			ResolvedExpression: DashboardFilterExpressionFromDomain(item.ResolvedExpression),
			EvaluatedAt:        evaluatedAt,
		}
	}
	drafts := make(map[string]DashboardFilterExpression, len(value.DraftControls))
	for key, expression := range value.DraftControls {
		drafts[key] = DashboardFilterExpressionFromDomain(expression)
	}
	return DashboardFilterState{
		Revision: int64(value.Revision), AppliedControls: applied, DraftControls: drafts,
		DirtyBindings: append([]string{}, value.DirtyBindings...), DefaultsRevision: value.DefaultsRevision,
	}
}

func DashboardFilterContractFromDefinition(definition dashboarddefinition.Definition) DashboardFilterContract {
	definitions := make(map[string]DashboardCompiledFilterDefinition, len(definition.FilterDefinitions))
	for id, item := range definition.FilterDefinitions {
		predicates := make([]DashboardFilterPredicatePolicy, len(item.Predicates))
		for index, predicate := range item.Predicates {
			operators := make([]string, len(predicate.Operators))
			for operatorIndex, operator := range predicate.Operators {
				operators[operatorIndex] = string(operator)
			}
			predicates[index] = DashboardFilterPredicatePolicy{Kind: string(predicate.Kind), Operators: operators}
		}
		optionKind := string(item.Options.Kind)
		if optionKind == "" {
			optionKind = "none"
		}
		staticOptions := make([]DashboardFilterStaticOption, len(item.Options.Values))
		for index, option := range item.Options.Values {
			staticOptions[index] = DashboardFilterStaticOption{
				Value: DashboardFilterValueFromDomain(option.Value), Label: option.Label,
			}
		}
		definitions[id] = DashboardCompiledFilterDefinition{
			ID: id, Label: item.Label, Description: optionalValue(item.Description), Field: item.Field,
			Dataset: optionalValue(item.Dataset), ValueKind: string(item.ValueKind), Predicates: predicates,
			Options:       DashboardFilterOptionSource{Kind: optionKind, Limit: int32(item.Options.Limit), IncludeNull: item.Options.IncludeNull, Values: staticOptions},
			FormatPattern: optionalValue(item.Formatting.Pattern), FormatUnit: optionalValue(item.Formatting.Unit),
			Timezone: item.Time.Timezone, Calendar: item.Time.Calendar, WeekStart: item.Time.WeekStart,
		}
	}
	bindings := make(map[string]DashboardCompiledFilterBinding)
	for key, item := range definition.CompiledFilterBindings() {
		dependencies := make([]DashboardFilterBindingRef, len(item.OptionDependencies))
		for index, dependency := range item.OptionDependencies {
			dependencies[index] = DashboardFilterBindingRef{Scope: string(dependency.Scope), ID: dependency.ID}
		}
		bindings[key] = DashboardCompiledFilterBinding{
			Key: key, ID: item.ID, Filter: item.Filter, Scope: string(item.Scope), PageID: optionalValue(item.PageID),
			Default: DashboardFilterExpressionFromDomain(item.Default), SelectionMode: string(item.Selection.Mode), Required: item.Required,
			MaxSelectedValues: int32(item.Selection.MaxSelectedValues), ReaderEditable: item.Editable(),
			URLParam: optionalValue(item.URL.Param), PaneVisible: item.Pane.IsVisible(), PaneOrder: int32(item.Pane.Order),
			PaneLabel: optionalValue(item.Pane.Label), Targets: append([]string(nil), item.Targets...),
			OptionDependencies: dependencies,
		}
	}
	return DashboardFilterContract{
		ApplicationMode: string(definition.FilterApplication.WithDefaults().Mode),
		Definitions:     definitions, Bindings: bindings,
	}
}

func DashboardInteractionCommandFromDashboard(value dashboard.InteractionCommand) DashboardInteractionCommand {
	mappings := make([]DashboardInteractionCommandMapping, len(value.Mappings))
	for index, mapping := range value.Mappings {
		mappings[index] = DashboardInteractionCommandMapping{
			Field: mapping.Field, Dataset: optionalValue(mapping.Dataset), Grain: optionalValue(mapping.Grain),
			Value: mapping.Value, Label: optionalValue(mapping.Label),
		}
	}
	return DashboardInteractionCommand{
		SourceKind: value.SourceKind, SourceID: value.SourceID, InteractionKind: value.InteractionKind,
		Action: value.Action, Toggle: value.Toggle, Mappings: mappings,
		SpecRevision: value.SpecRevision, DataRevision: value.DataRevision,
		ServingStateID: value.ServingStateID, FilterRevision: value.FilterRevision,
		InteractionRevision: value.InteractionRevision,
	}
}

func DashboardVisualWindowRequestFromDashboard(value dashboard.TableRequest) visualizationir.VisualizationWindowRequest {
	direction := visualizationir.VisualizationSortDirectionAscending
	if value.Sort.Direction == "desc" {
		direction = visualizationir.VisualizationSortDirectionDescending
	}
	return visualizationir.VisualizationWindowRequest{
		VisualID: value.Table, RequestSeq: int64(value.RequestSeq), ResetVersion: int64(value.ResetVersion),
		Start: int64(value.Start), Limit: int64(value.Count), BlockID: value.Block,
		Sort: []visualizationir.VisualizationSort{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: value.Sort.Key}, Direction: direction}},
	}
}

func DashboardTabularVisualFromDefinitionAtRevision(definition visualizationdefinition.Definition, value dashboard.Table, dataRevision, generation int64) visualizationir.VisualizationEnvelope {
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definition, value, dataRevision, generation)
	if err != nil {
		panic(fmt.Sprintf("compiled tabular visualization %q reached the signal boundary with invalid data: %v", definition.ID, err))
	}
	return envelope
}

func DashboardVisualizationSignalFromIR(value visualizationir.VisualizationEnvelope) DashboardVisualizationSignal {
	transport, err := visualizationir.EncodeDataStateTransport(value.DataState)
	if err != nil {
		panic(fmt.Sprintf("encode dashboard visualization data-state transport: %v", err))
	}
	return DashboardVisualizationSignal{
		SchemaVersion:    value.SchemaVersion,
		VisualID:         value.VisualID,
		RendererID:       value.RendererID,
		SpecRevision:     value.SpecRevision,
		Spec:             value.Spec,
		DataRevision:     value.DataRevision,
		DataState:        visualizationDataStateTransport(transport),
		Selection:        value.Selection,
		Highlights:       value.Highlights,
		SpatialSelection: value.SpatialSelection,
		Status:           value.Status,
		Diagnostics:      value.Diagnostics,
	}
}

func dashboardSpatialSelections(values []dashboard.SpatialInteractionSelection) []visualizationir.VisualizationSpatialSelectionState {
	out := make([]visualizationir.VisualizationSpatialSelectionState, len(values))
	for index, value := range values {
		out[index] = visualizationir.VisualizationSpatialSelectionState{VisualID: value.VisualID, InteractionID: value.InteractionID, Geometry: value.Geometry}
	}
	return out
}

func DashboardSpatialSelectionsFromDashboard(values []dashboard.SpatialInteractionSelection) []visualizationir.VisualizationSpatialSelectionState {
	return dashboardSpatialSelections(values)
}

func visualizationDataStateTransport(value visualizationir.EncodedDataStateTransport) visualizationir.VisualizationDataStateTransport {
	return visualizationir.VisualizationDataStateTransport{
		SchemaVersion: value.SchemaVersion,
		Encoding:      visualizationir.VisualizationDataStateTransportEncoding(value.Encoding),
		Kind:          visualizationir.VisualizationDataStateKind(value.Kind),
		SpecRevision:  value.SpecRevision,
		DataRevision:  value.DataRevision,
		Generation:    value.Generation,
		Payload:       value.Payload,
	}
}

func dashboardInteractionSelections(values []dashboard.InteractionSelection) []DashboardInteractionSelection {
	out := make([]DashboardInteractionSelection, len(values))
	for index, value := range values {
		out[index] = DashboardInteractionSelection{
			ID: value.ID, SourceKind: value.SourceKind, SourceID: value.SourceID, InteractionKind: value.InteractionKind,
			Entries: dashboardInteractionSelectionEntries(value.Entries), Label: value.Label, Order: int64(value.Order),
		}
	}
	return out
}

func DashboardInteractionSelectionsFromDashboard(values []dashboard.InteractionSelection) []DashboardInteractionSelection {
	return dashboardInteractionSelections(values)
}

func dashboardInteractionSelectionEntries(values []dashboard.InteractionSelectionEntry) []DashboardInteractionSelectionEntry {
	out := make([]DashboardInteractionSelectionEntry, len(values))
	for index, value := range values {
		mappings := make([]DashboardInteractionSelectionMapping, len(value.Mappings))
		for mappingIndex, mapping := range value.Mappings {
			mappings[mappingIndex] = DashboardInteractionSelectionMapping{
				Field: mapping.Field, Dataset: optionalValue(mapping.Dataset), Grain: optionalValue(mapping.Grain),
				Value: mapping.Value, Label: optionalValue(mapping.Label),
			}
		}
		out[index] = DashboardInteractionSelectionEntry{Mappings: mappings, Label: optionalValue(value.Label)}
	}
	return out
}
