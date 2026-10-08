package tools

import (
	previewservice "github.com/flidai/leapview/internal/dashboard/authoring/preview"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// The builder loads its governed render payload separately. The model needs
// exact revision identities and validation outcomes, not duplicated chart data.
func dashboardPreviewModelResult(input dashboardAuthoringPreviewInput, value previewservice.Preview) map[string]any {
	errors := make(map[string]string, len(value.VisualErrors))
	for id, message := range value.VisualErrors {
		errors[id] = message
	}
	states := make(map[string]any, len(value.PagePatch.Visuals))
	for id, visual := range value.PagePatch.Visuals {
		summary := map[string]any{"status": visual.Status}
		if data, ok := visual.DataState.Value.(*visualizationir.InlineVisualizationDataState); ok && data != nil {
			datasets := make(map[string]any, len(data.Datasets))
			for _, dataset := range data.Datasets {
				counts := make(map[string]int, len(dataset.Columns))
				for index, column := range dataset.Columns {
					counts[column] = 0
					for _, row := range dataset.Rows {
						if index < len(row) && row[index] != nil {
							counts[column]++
						}
					}
				}
				datasets[dataset.ID] = map[string]any{"rows": len(dataset.Rows), "nonNullValues": counts, "completeness": dataset.Completeness}
			}
			summary["datasets"] = datasets
		}
		states[id] = summary
		if visual.Status.Kind == "error" {
			message := "Visualization query failed"
			if visual.Status.Message != nil {
				message = *visual.Status.Message
			}
			errors[id] = message
		}
	}
	result := map[string]any{"dashboardId": input.DashboardID, "draftId": input.DraftID, "page": input.Page, "revision": value.Revision, "visuals": states, "visualErrors": errors, "semanticEvidence": value.SemanticEvidence}
	if value.PagePatch.Status.Error != "" {
		result["error"] = value.PagePatch.Status.Error
	}
	return result
}
