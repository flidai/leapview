package compiler

import (
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type dashboardCompileContext struct {
	model   *semanticmodel.Model
	modelID string
}

func (ctx dashboardCompileContext) compileVisuals(authored map[string]document.DashboardVisual) (map[string]visualizationdefinition.Definition, error) {
	visuals := make(map[string]visualizationdefinition.Definition, len(authored))
	visualIDs := make([]string, 0, len(authored))
	for visualID := range authored {
		visualIDs = append(visualIDs, visualID)
	}
	sort.Strings(visualIDs)
	for _, visualID := range visualIDs {
		visual := authored[visualID]
		query, err := LowerDashboardQuery(visual.Query, ctx.model, ctx.modelID)
		if err != nil {
			return nil, fmt.Errorf("visual %q query: %w", visualID, err)
		}
		if err := lowerCanonicalVisualSeries(&query, visual.Type); err != nil {
			return nil, fmt.Errorf("visual %q query: %w", visualID, err)
		}
		presentation, err := LowerCanonicalDashboardPresentationForQuery(visual.Presentation, visual.Type, query)
		if err != nil {
			return nil, fmt.Errorf("visual %q presentation: %w", visualID, err)
		}
		if err := validateCanonicalVisualResultReferences(visual, query); err != nil {
			return nil, fmt.Errorf("visual %q references: %w", visualID, err)
		}
		if err := validateDerivedResultAliases(query, visual.Type); err != nil {
			return nil, fmt.Errorf("visual %q result aliases: %w", visualID, err)
		}
		if err := validateCanonicalInteractionKinds(visual); err != nil {
			return nil, fmt.Errorf("visual %q interactions: %w", visualID, err)
		}
		if visual.Type == document.DashboardVisualTypeMap {
			geographic, _ := visual.Presentation.Value.(*document.GeographicDashboardPresentation)
			query.Binding, err = canonicalSpatialBinding(query.Binding, geographic, visual.Query)
			if err != nil {
				return nil, fmt.Errorf("visual %q geographic delivery: %w", visualID, err)
			}
		}
		adjustCanonicalResultShape(&query.Binding, visual.Type)
		secondary := make(map[string]visualizationdefinition.QueryBinding)
		secondarySchemas := make([]visualizationir.VisualizationDatasetSchema, 0)
		if visual.Datasets != nil {
			datasetIDs := make([]string, 0, len(*visual.Datasets))
			for datasetID := range *visual.Datasets {
				datasetIDs = append(datasetIDs, datasetID)
			}
			sort.Strings(datasetIDs)
			for _, datasetID := range datasetIDs {
				datasetQuery := (*visual.Datasets)[datasetID]
				secondaryQuery, lowerErr := LowerDashboardQuery(datasetQuery, ctx.model, ctx.modelID)
				if lowerErr != nil {
					return nil, fmt.Errorf("visual %q dataset %q query: %w", visualID, datasetID, lowerErr)
				}
				// LowerDashboardQuery produces a primary binding by default. A
				// context dataset is a distinct result frame and must carry its
				// authored dataset ID through validation and runtime execution.
				secondaryQuery.Binding.DatasetID = datasetID
				secondary[datasetID] = secondaryQuery.Binding
				secondarySchemas = append(secondarySchemas, visualizationir.VisualizationDatasetSchema{ID: datasetID, Fields: canonicalResultFields(secondaryQuery, ctx.model)})
			}
		}
		spec, err := canonicalVisualizationSpec(visualID, visual, query, presentation, secondarySchemas, ctx.model)
		if err != nil {
			return nil, fmt.Errorf("visual %q: %w", visualID, err)
		}
		if err := lowerCanonicalDecisionContext(&spec, visual.Presentation, visual.Type, query); err != nil {
			return nil, fmt.Errorf("visual %q: %w", visualID, err)
		}
		if err := appendCanonicalCalculationOutputs(&spec); err != nil {
			return nil, fmt.Errorf("visual %q calculations: %w", visualID, err)
		}
		if err := visualizationir.ValidateSpec(spec); err != nil {
			return nil, fmt.Errorf("visual %q IR: %w", visualID, err)
		}
		definitionBinding := query.Binding
		if visual.Type == document.DashboardVisualTypeMatrix && definitionBinding.Pivot != nil {
			pivot := definitionBinding.Pivot
			definitionBinding.Kind = visualizationdefinition.QueryMatrix
			definitionBinding.ResultShape = visualizationdefinition.ResultMatrixWindow
			definitionBinding.Matrix = &visualizationdefinition.MatrixQueryBinding{TableID: pivot.TableID, Rows: pivot.Rows, Columns: pivot.Columns, Metrics: pivot.Metrics, Limit: pivot.Limit}
			definitionBinding.Pivot = nil
		}
		compiled, err := visualizationdefinition.NewWithSecondaryQueries(visualID, spec, definitionBinding, secondary)
		if err != nil {
			return nil, fmt.Errorf("visual %q definition: %w", visualID, err)
		}
		visuals[visualID] = compiled
	}
	if err := completeVisualizationInteractionGraph(visuals); err != nil {
		return nil, fmt.Errorf("complete dashboard interaction graph: %w", err)
	}
	return visuals, nil
}
