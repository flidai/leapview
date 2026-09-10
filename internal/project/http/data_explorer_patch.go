package http

import (
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
)

// Ordinary command responses carry canonical specs, not partial edits.
// Datastar recursively merges objects, so omitted optional selections would
// resurrect previously cleared state. Explicit nulls remove those signal keys
// without changing the persisted/generated ExplorationSpec JSON contract.
func dataExplorerSignalPatch(explorer projectsignals.DataExplorerSignal) pagestream.SignalPatch {
	command := explorerCommandPatch(explorer.Command)
	return pagestream.SignalPatch{
		"dataExplorerCommand": command,
		"dataExplorer": struct {
			projectsignals.DataExplorerSignal
			Command any `json:"command"`
			Explore any `json:"explore"`
		}{explorer, command, struct {
			projectsignals.DataExploreSignal
			Command any `json:"command"`
		}{explorer.Explore, exploreCommandPatch(explorer.Explore.Command)}},
	}
}

func explorerCommandPatch(command projectsignals.DataExplorerCommand) any {
	var explore any
	if command.Explore != nil {
		explore = exploreCommandPatch(*command.Explore)
	}
	return struct {
		projectsignals.DataExplorerCommand
		Explore any `json:"explore"`
	}{command, explore}
}

func exploreCommandPatch(command projectsignals.DataExploreCommand) any {
	return struct {
		projectsignals.DataExploreCommand
		Spec any `json:"spec"`
	}{command, explorationSpecPatch(command.Spec)}
}

func explorationSpecPatch(spec exploration.ExplorationSpec) any {
	var time any
	if spec.Time != nil {
		time = struct {
			exploration.ExplorationTimeSelection
			Alias *string                           `json:"alias"`
			Range *exploration.ExplorationTimeRange `json:"range"`
		}{*spec.Time, spec.Time.Alias, spec.Time.Range}
	}
	return struct {
		exploration.ExplorationSpec
		DatasetID     *string                                     `json:"datasetId"`
		Time          any                                         `json:"time"`
		Pivot         *exploration.ExplorationPivotConfig         `json:"pivot"`
		Table         *exploration.ExplorationTableDisplayConfig  `json:"table"`
		Visualization *exploration.ExplorationVisualizationConfig `json:"visualization"`
	}{spec, spec.DatasetID, time, spec.Pivot, spec.Table, spec.Visualization}
}
