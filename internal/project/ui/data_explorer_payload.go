package ui

import (
	"encoding/json"

	"github.com/flidai/leapview/internal/analytics/exploration"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// Generated optional fields use omitempty, but recursive signal patches need
// explicit nulls to remove time selections and bounds. These wire projections
// leave the generated contracts and persisted exploration specs unchanged.
type dataExplorerWire struct {
	uisignals.DataExplorerSignal
	Command dataExplorerCommandWire `json:"command"`
	Explore dataExploreWire         `json:"explore"`
}

type dataExploreWire struct {
	uisignals.DataExploreSignal
	Command dataExploreCommandWire `json:"command"`
}

type dataExplorerCommandWire struct {
	uisignals.DataExplorerCommand
	Explore *dataExploreCommandWire `json:"explore"`
}

type dataExploreCommandWire struct {
	uisignals.DataExploreCommand
	Spec explorationSpecWire              `json:"spec"`
	Time *uisignals.DataExploreTimeSignal `json:"time"`
}

type explorationSpecWire struct {
	exploration.ExplorationSpec
	Time *explorationTimeWire `json:"time"`
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

// DataExplorerPayload projects both command mirrors with explicit time clears.
func DataExplorerPayload(state uisignals.DataExplorerSignal) dataExplorerWire {
	return dataExplorerWire{
		DataExplorerSignal: state,
		Command:            DataExplorerCommandPayload(state.Command),
		Explore: dataExploreWire{
			DataExploreSignal: state.Explore,
			Command:           dataExploreCommandPayload(state.Explore.Command),
		},
	}
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
	}
}

func explorationSpecPayload(spec exploration.ExplorationSpec) explorationSpecWire {
	wire := explorationSpecWire{ExplorationSpec: spec}
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
	Exploration *explorationSpecWire `json:"exploration"`
}

// DataExplorerAgentContextPayload clears removed time settings in agent context.
func DataExplorerAgentContextPayload(context uisignals.AgentContextSignal) dataExplorerAgentContextWire {
	wire := dataExplorerAgentContextWire{AgentContextSignal: context}
	if context.Exploration != nil {
		spec := explorationSpecPayload(*context.Exploration)
		wire.Exploration = &spec
	}
	return wire
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
