package ui

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/analytics/exploration"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// Generated optional fields use omitempty, but recursive signal patches need
// explicit nulls to remove errors, display settings, and time selections.
// These wire projections leave generated contracts and persisted specs unchanged.
type dataExplorerWire struct {
	uisignals.DataExplorerSignal
	Command dataExplorerCommandWire `json:"command"`
	Explore dataExploreWire         `json:"explore"`
}

type dataExploreWire struct {
	uisignals.DataExploreSignal
	Command           dataExploreCommandWire      `json:"command"`
	Result            dataExploreResultWire       `json:"result"`
	Status            dataExploreStatusWire       `json:"status"`
	FilterSuggestions *dataExploreSuggestionsWire `json:"filterSuggestions"`
}

type dataExploreResultWire struct {
	uisignals.DataExploreResultSignal
	Error  *string                      `json:"error"`
	SQL    *string                      `json:"sql"`
	Plan   *string                      `json:"plan"`
	Window *uisignals.DataPreviewSignal `json:"window"`
}

type dataExploreStatusWire struct {
	uisignals.DataExploreStatusSignal
	Error           *string  `json:"error"`
	Message         *string  `json:"message"`
	ProgressPercent *float64 `json:"progressPercent"`
}

type dataExploreSuggestionsWire struct {
	uisignals.DataExploreFilterSuggestionsSignal
	Error *string `json:"error"`
}

type dataExplorerCommandWire struct {
	uisignals.DataExplorerCommand
	Explore *dataExploreCommandWire `json:"explore"`
}

type dataExploreCommandWire struct {
	uisignals.DataExploreCommand
	Spec   explorationSpecWire                 `json:"spec"`
	Time   *uisignals.DataExploreTimeSignal    `json:"time"`
	Window *uisignals.DataExploreWindowCommand `json:"window"`
}

type explorationSpecWire struct {
	exploration.ExplorationSpec
	Mode          *exploration.ExplorationQueryMode           `json:"mode"`
	Time          *explorationTimeWire                        `json:"time"`
	Table         *exploration.ExplorationTableDisplayConfig  `json:"table"`
	Visualization *exploration.ExplorationVisualizationConfig `json:"visualization"`
	Pivot         *exploration.ExplorationPivotConfig         `json:"pivot"`
}

type explorationTimeWire struct {
	exploration.ExplorationTimeSelection
	Range *explorationTimeRangeWire `json:"range"`
}

type explorationTimeRangeWire exploration.ExplorationTimeRange

func (value explorationTimeRangeWire) MarshalJSON() ([]byte, error) {
	if absolute, ok := value.Value.(*exploration.AbsoluteExplorationTimeRange); ok && absolute != nil {
		return json.Marshal(struct {
			*exploration.AbsoluteExplorationTimeRange
			Lower *exploration.ExplorationTimeBound `json:"lower"`
			Upper *exploration.ExplorationTimeBound `json:"upper"`
		}{absolute, absolute.Lower, absolute.Upper})
	}
	return json.Marshal(exploration.ExplorationTimeRange(value))
}

// DataExplorerPayload explicitly clears removed state in recursive signal patches.
func DataExplorerPayload(state uisignals.DataExplorerSignal) dataExplorerWire {
	wire := dataExplorerWire{
		DataExplorerSignal: state,
		Command:            DataExplorerCommandPayload(state.Command),
		Explore: dataExploreWire{
			DataExploreSignal: state.Explore,
			Command:           dataExploreCommandPayload(state.Explore.Command),
			Result:            dataExploreResultWire{state.Explore.Result, state.Explore.Result.Error, state.Explore.Result.SQL, state.Explore.Result.Plan, state.Explore.Result.Window},
			Status:            dataExploreStatusWire{state.Explore.Status, state.Explore.Status.Error, state.Explore.Status.Message, state.Explore.Status.ProgressPercent},
			FilterSuggestions: DataExploreSuggestionsPayload(state.Explore.FilterSuggestions),
		},
	}
	return wire
}

// DataExploreSuggestionsPayload clears a previous error on successful retries
// in both full Explorer payloads and independent suggestion signal patches.
func DataExploreSuggestionsPayload(suggestions *uisignals.DataExploreFilterSuggestionsSignal) *dataExploreSuggestionsWire {
	if suggestions == nil {
		return nil
	}
	return &dataExploreSuggestionsWire{*suggestions, suggestions.Error}
}

// DataExplorerCommandPayload applies the same projection to the command signal.
func DataExplorerCommandPayload(command uisignals.DataExplorerCommand) dataExplorerCommandWire {
	wire := dataExplorerCommandWire{DataExplorerCommand: command}
	if command.Explore != nil {
		explore := dataExploreCommandPayload(*command.Explore)
		wire.Explore = &explore
	}
	return wire
}

func dataExploreCommandPayload(command uisignals.DataExploreCommand) dataExploreCommandWire {
	return dataExploreCommandWire{
		DataExploreCommand: command,
		Spec:               explorationSpecPayload(command.Spec),
		Time:               command.Time,
		Window:             command.Window,
	}
}

func explorationSpecPayload(spec exploration.ExplorationSpec) explorationSpecWire {
	wire := explorationSpecWire{ExplorationSpec: spec, Mode: spec.Mode, Table: spec.Table, Visualization: spec.Visualization, Pivot: spec.Pivot}
	if spec.Time != nil {
		wire.Time = &explorationTimeWire{
			ExplorationTimeSelection: *spec.Time,
			Range:                    (*explorationTimeRangeWire)(spec.Time.Range),
		}
	}
	return wire
}

type dataExplorerAgentContextWire struct {
	uisignals.AgentContextSignal
	DatasetID   *string              `json:"datasetId"`
	Exploration *explorationSpecWire `json:"exploration"`
}

// DataExplorerAgentContextPayload clears removed time settings in agent context.
func DataExplorerAgentContextPayload(context uisignals.AgentContextSignal) dataExplorerAgentContextWire {
	wire := dataExplorerAgentContextWire{AgentContextSignal: context, DatasetID: context.DatasetID}
	if context.Exploration != nil {
		spec := explorationSpecPayload(*context.Exploration)
		wire.Exploration = &spec
	}
	return wire
}

// DataExplorerAgentContextRefreshPayload refreshes governed context while
// retaining references attached to the browser's current chat turn.
func DataExplorerAgentContextRefreshPayload(context uisignals.AgentContextSignal) any {
	return struct {
		dataExplorerAgentContextWire
		References []uisignals.AgentReferenceSignal `json:"references,omitempty"`
	}{dataExplorerAgentContextWire: DataExplorerAgentContextPayload(context)}
}

type savedExplorationWire struct {
	uisignals.SavedExplorationStateSignal
	Current *savedExplorationCurrentWire `json:"current"`
	Command savedExplorationCommandWire  `json:"command"`
}

type savedExplorationCurrentWire struct {
	uisignals.SavedExplorationCurrentSignal
	Spec *explorationSpecWire `json:"spec"`
}

type savedExplorationCommandWire struct {
	uisignals.SavedExplorationCommandSignal
	Spec *explorationSpecWire `json:"spec"`
}

// DataExplorerSavedExplorationPayload prevents a reopened or saved spec from
// retaining the previous exploration's time selection in the browser.
func DataExplorerSavedExplorationPayload(state uisignals.SavedExplorationStateSignal) savedExplorationWire {
	wire := savedExplorationWire{
		SavedExplorationStateSignal: state,
		Command:                     savedExplorationCommandWire{SavedExplorationCommandSignal: state.Command},
	}
	if state.Current != nil {
		wire.Current = &savedExplorationCurrentWire{SavedExplorationCurrentSignal: *state.Current}
		if state.Current.Spec != nil {
			spec := explorationSpecPayload(*state.Current.Spec)
			wire.Current.Spec = &spec
		}
	}
	if state.Command.Spec != nil {
		spec := explorationSpecPayload(*state.Command.Spec)
		wire.Command.Spec = &spec
	}
	return wire
}
