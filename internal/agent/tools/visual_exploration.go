package tools

import (
	"fmt"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	explorationadapter "github.com/flidai/leapview/internal/dashboard/authoring/explorationadapter"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

// agentVisualExploration lowers the authored query_visual document to the
// canonical Explorer contract. The input is the original authored visual,
// never the rendered envelope or its rows. Runtime metadata is intentionally
// not part of the handoff; the destination resolves a fresh authorized view.
func agentVisualExploration(input agentVisualInput, originalModel, model *semanticmodel.Model, compiled *semanticquery.CompiledModel, definition visualizationdefinition.Definition) (*exploration.ExplorationSpec, error) {
	if originalModel == nil || model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	if compiled == nil || !compiled.MatchesModel(originalModel) || !compiled.MatchesModel(model) {
		return nil, fmt.Errorf("compiled semantic model is stale or unavailable")
	}
	visual, err := normalizeAgentVisualLimit(input.Visual, definition)
	if err != nil {
		return nil, err
	}
	input.Visual = visual
	visualID := definition.ID
	if visualID == "" {
		return nil, fmt.Errorf("compiled visual identity is required")
	}
	doc := agentVisualDocument(input, visualID, input.Model)
	options, err := explorationadapter.ReverseOptionsForActiveModelAtTarget(doc, visualID, "page/visual", model, compiled)
	if err != nil {
		return nil, err
	}
	spec, err := explorationadapter.FromDashboardDocument(doc, visualID, options)
	if err != nil {
		return nil, err
	}
	if err := exploration.ValidateAgainstModel(model, &spec); err != nil {
		return nil, fmt.Errorf("canonical exploration model validation: %w", err)
	}
	return &spec, nil
}

// normalizeAgentVisualLimit keeps the handoff within the same effective
// bound that was used for query_visual. A missing authored limit receives the
// agent cap; a declared data budget can further narrow it. The cloned query
// ensures canonical Explorer state never silently widens the executed view.
func normalizeAgentVisualLimit(value dashboarddocument.DashboardVisual, definition visualizationdefinition.Definition) (dashboarddocument.DashboardVisual, error) {
	effective := agentDefinitionLimit(definition)
	if effective <= 0 || effective > maxVisualRows {
		return dashboarddocument.DashboardVisual{}, fmt.Errorf("visual query limit is outside agent bounds")
	}
	switch query := value.Query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		if query == nil {
			return dashboarddocument.DashboardVisual{}, fmt.Errorf("aggregate query is nil")
		}
		if query.Limit != nil && *query.Limit > 0 && int(*query.Limit) < effective {
			effective = int(*query.Limit)
		}
	case *dashboarddocument.PivotDashboardQuery:
		if query == nil {
			return dashboarddocument.DashboardVisual{}, fmt.Errorf("pivot query is nil")
		}
		if query.Window != nil && query.Window.Limit > 0 && int(query.Window.Limit) < effective {
			effective = int(query.Window.Limit)
		}
	}
	if value.DataBudget != nil && value.DataBudget.MaxRows > 0 && int(value.DataBudget.MaxRows) < effective {
		effective = int(value.DataBudget.MaxRows)
	}
	if effective <= 0 {
		return dashboarddocument.DashboardVisual{}, fmt.Errorf("visual data budget must be positive")
	}
	result := value
	limit := int32(effective)
	switch query := value.Query.Value.(type) {
	case *dashboarddocument.AggregateDashboardQuery:
		if query == nil {
			return dashboarddocument.DashboardVisual{}, fmt.Errorf("aggregate query is nil")
		}
		clone := *query
		clone.Limit = &limit
		result.Query.Value = &clone
	case *dashboarddocument.PivotDashboardQuery:
		if query == nil {
			return dashboarddocument.DashboardVisual{}, fmt.Errorf("pivot query is nil")
		}
		clone := *query
		if clone.Window == nil {
			clone.Window = &dashboarddocument.DashboardPivotWindow{Limit: limit}
		} else {
			window := *clone.Window
			window.Limit = limit
			clone.Window = &window
		}
		result.Query.Value = &clone
	default:
		return result, nil
	}
	if result.DataBudget != nil {
		budget := *result.DataBudget
		budget.MaxRows = limit
		result.DataBudget = &budget
	}
	return result, nil
}
