package preview

import (
	"github.com/flidai/leapview/internal/dashboard"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// normalizeWindowResetVersions binds windowed visuals in a full page preview
// to the current compiled filter revision. Individual window responses keep
// the runtime's table reset identity because it advances independently for
// sorting and paging.
func normalizeWindowResetVersions(patch dashboard.Patch) dashboard.Patch {
	if patch.Visuals == nil {
		return patch
	}
	resetVersion := int64(0)
	if patch.Filters.CompiledState != nil {
		resetVersion = int64(patch.Filters.CompiledState.Revision)
	}
	for visualID, envelope := range patch.Visuals {
		window, ok := envelope.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
		if !ok || window == nil {
			continue
		}
		copyWindow := *window
		copyWindow.Blocks = make(map[string]visualizationir.VisualizationWindowBlock, len(window.Blocks))
		for blockID, block := range window.Blocks {
			block.ResetVersion = resetVersion
			copyWindow.Blocks[blockID] = block
		}
		copyWindow.ResetVersion = resetVersion
		envelope.DataState.Value = &copyWindow
		patch.Visuals[visualID] = envelope
	}
	return patch
}
