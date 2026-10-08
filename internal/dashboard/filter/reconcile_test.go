package filter

import "testing"

func TestReconcileMachineRetainsSelectionsDraftsAndIdempotency(t *testing.T) {
	bindings := testBindingSpecs()
	machine := NewMachine(ApplicationDeferred, bindings)
	command := Command{Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "select-state", BindingKey: "state", Operation: MutationSet, Expression: &Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "CA"}}}}
	if _, err := machine.Execute(command); err != nil {
		t.Fatal(err)
	}
	before := machine.Snapshot()
	delete(bindings, "purchase_date")
	bindings["imported"] = bindings["category"]
	reconciled, err := ReconcileMachine(ApplicationDeferred, bindings, before)
	if err != nil {
		t.Fatal(err)
	}
	after := reconciled.State()
	if after.Revision != before.State.Revision+1 || after.DefaultsRevision == before.State.DefaultsRevision {
		t.Fatalf("reconciled revision/defaults: %#v", after)
	}
	if after.AppliedControls["state"].Expression.Values[0].Value != "WA" || after.DraftControls["state"].Values[0].Value != "CA" {
		t.Fatalf("lost applied/draft selections: %#v", after)
	}
	if len(after.DirtyBindings) != 1 || after.DirtyBindings[0] != "state" || after.AppliedControls["imported"].Expression.Kind != ExpressionUnfiltered {
		t.Fatalf("dirty/new controls: %#v", after)
	}
	if _, ok := after.AppliedControls["purchase_date"]; ok {
		t.Fatal("removed binding retained")
	}
	if result, err := reconciled.Execute(command); err != nil || !result.Duplicate || result.State.Revision != after.Revision {
		t.Fatalf("lost mutation idempotency: %#v %v", result, err)
	}
}

func TestReconcileMachineRejectsIncompatibleSelections(t *testing.T) {
	bindings := testBindingSpecs()
	snapshot := NewMachine(ApplicationImmediate, bindings).Snapshot()
	bindings["state"] = BindingSpec{ValueKind: ValueInteger, Default: Expression{Kind: ExpressionUnfiltered}, Predicates: []PredicatePolicy{{Kind: ExpressionSet, Operators: []Operator{OperatorIn}}}}
	if _, err := ReconcileMachine(ApplicationImmediate, bindings, snapshot); err == nil {
		t.Fatal("incompatible retained selection accepted")
	}
}
