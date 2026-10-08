package filter

import "fmt"

// ReconcileMachine carries selections across authored binding additions and
// removals. New controls use compiled defaults; incompatible retained selections
// still fail validation. The revision advances so older commands cannot overwrite
// the new contract, while duplicate mutation IDs remain recognized.
func ReconcileMachine(mode ApplicationMode, bindings map[string]BindingSpec, snapshot MachineSnapshot) (*Machine, error) {
	if snapshot.Version != MachineSnapshotVersion || snapshot.State.Revision == 0 {
		return nil, fmt.Errorf("invalid filter machine snapshot")
	}
	next := NewMachine(mode, bindings).Snapshot()
	next.State.Revision = snapshot.State.Revision + 1
	next.Seen, next.SeenOrder = snapshot.Seen, snapshot.SeenOrder
	for key, binding := range bindings {
		if applied, ok := snapshot.State.AppliedControls[key]; ok {
			next.State.AppliedControls[key] = applied
		}
		if draft, ok := snapshot.State.DraftControls[key]; ok {
			if _, err := Canonicalize(draft, binding.ValueKind); err != nil || !predicateAllowed(draft, binding.Predicates) {
				return nil, fmt.Errorf("restored binding %q has an incompatible draft", key)
			}
			next.State.DraftControls[key] = draft
		}
	}
	for _, key := range snapshot.State.DirtyBindings {
		if _, ok := bindings[key]; ok {
			next.State.DirtyBindings = append(next.State.DirtyBindings, key)
		}
	}
	return RestoreMachine(mode, bindings, next)
}
