package http

import (
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

type dashboardStreamResume struct {
	Runtime               dashboard.Runtime      `json:"runtime"`
	FilterState           *dashboardfilter.State `json:"filterState"`
	InteractionSelections []map[string]any       `json:"interactionSelections"`
	SpatialSelections     []map[string]any       `json:"spatialSelections"`
}

// Re-evaluate browser expressions against today's authorized bindings. Never
// restore browser-provided resolved predicates, defaults, or locked controls.
func resumeDashboardFilters(definition dashboarddefinition.Definition, input dashboardfilter.State) (dashboardfilter.MachineSnapshot, error) {
	specs := definition.FilterBindingSpecs()
	machine := dashboardfilter.NewMachine(dashboardfilter.ApplicationImmediate, specs)
	keys := make([]string, 0, len(input.AppliedControls))
	for key := range input.AppliedControls {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		spec, ok := specs[key]
		if !ok {
			return dashboardfilter.MachineSnapshot{}, fmt.Errorf("unknown filter binding %q", key)
		}
		if !spec.Editable {
			continue
		}
		expression := input.AppliedControls[key].Expression
		if _, err := machine.Execute(dashboardfilter.Command{
			Kind: dashboardfilter.CommandMutate, BaseRevision: machine.State().Revision,
			ClientMutationID: "resume:applied:" + key, BindingKey: key,
			Operation: dashboardfilter.MutationSet, Expression: &expression,
		}); err != nil {
			return dashboardfilter.MachineSnapshot{}, err
		}
	}
	machine, err := dashboardfilter.RestoreMachine(definition.FilterApplication.WithDefaults().Mode, specs, machine.Snapshot())
	if err != nil {
		return dashboardfilter.MachineSnapshot{}, err
	}
	keys = keys[:0]
	for key := range input.DraftControls {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		expression := input.DraftControls[key]
		if _, err := machine.Execute(dashboardfilter.Command{
			Kind: dashboardfilter.CommandMutate, BaseRevision: machine.State().Revision,
			ClientMutationID: "resume:draft:" + key, BindingKey: key,
			Operation: dashboardfilter.MutationSet, Expression: &expression,
		}); err != nil {
			return dashboardfilter.MachineSnapshot{}, err
		}
	}
	return machine.Snapshot(), nil
}
