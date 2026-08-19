package filter

import (
	"errors"
	"testing"
	"time"
)

func TestMachineImmediateMutationAdvancesOneRevisionAndPreservesOtherBindings(t *testing.T) {
	machine := newTestMachine(ApplicationImmediate)
	before := machine.State()
	result, err := machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: before.Revision, ClientMutationID: "m-1",
		BindingKey: "state", Operation: MutationSet,
		Expression: &Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "CA"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Revision != before.Revision+1 {
		t.Fatalf("revision = %d, want %d", result.State.Revision, before.Revision+1)
	}
	if result.State.AppliedControls["category"].Expression.Kind != ExpressionUnfiltered {
		t.Fatalf("unrelated binding changed: %#v", result.State.AppliedControls["category"])
	}
}

func TestMachineRejectsClearingRequiredBinding(t *testing.T) {
	machine := NewMachine(ApplicationImmediate, map[string]BindingSpec{"required": {
		ValueKind: ValueString, Default: Expression{Kind: ExpressionComparison, Operator: OperatorEquals, Value: &Value{Kind: ValueString, Value: "CA"}}, Required: true, Editable: true,
		Predicates: []PredicatePolicy{{Kind: ExpressionComparison, Operators: []Operator{OperatorEquals}}},
	}})
	_, err := machine.Execute(Command{Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "clear-required", BindingKey: "required", Operation: MutationClear})
	if !errors.Is(err, ErrFilterRequired) {
		t.Fatalf("required clear error = %v", err)
	}
}

func TestMachineDeferredApplyPromotesAllDraftsInOneRevision(t *testing.T) {
	machine := newTestMachine(ApplicationDeferred)
	revision := machine.State().Revision
	for index, command := range []Command{
		{
			Kind: CommandMutate, BaseRevision: revision, ClientMutationID: "m-1",
			BindingKey: "state", Operation: MutationSet,
			Expression: &Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "CA"}}},
		},
		{
			Kind: CommandMutate, BaseRevision: revision, ClientMutationID: "m-2",
			BindingKey: "category", Operation: MutationSet,
			Expression: &Expression{Kind: ExpressionComparison, Operator: OperatorContains, Value: &Value{Kind: ValueString, Value: "books"}},
		},
	} {
		result, err := machine.Execute(command)
		if err != nil {
			t.Fatalf("draft %d: %v", index, err)
		}
		if result.State.Revision != revision {
			t.Fatalf("draft advanced applied revision to %d", result.State.Revision)
		}
	}
	result, err := machine.Execute(Command{Kind: CommandApply, BaseRevision: revision, ClientMutationID: "apply-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Revision != revision+1 || len(result.State.DirtyBindings) != 0 || len(result.State.DraftControls) != 0 {
		t.Fatalf("applied state = %#v", result.State)
	}
	if result.State.AppliedControls["state"].Expression.Kind != ExpressionSet ||
		result.State.AppliedControls["category"].Expression.Kind != ExpressionComparison {
		t.Fatalf("drafts were not promoted: %#v", result.State.AppliedControls)
	}
}

func TestMachineCancelDiscardsDraftsWithoutAdvancingRevision(t *testing.T) {
	machine := newTestMachine(ApplicationDeferred)
	revision := machine.State().Revision
	if _, err := machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: revision, ClientMutationID: "m-1",
		BindingKey: "state", Operation: MutationClear,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := machine.Execute(Command{Kind: CommandCancel, BaseRevision: revision, ClientMutationID: "cancel-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Revision != revision || len(result.State.DraftControls) != 0 || len(result.State.DirtyBindings) != 0 {
		t.Fatalf("cancel result = %#v", result.State)
	}
}

func TestMachineClearDiffersFromReset(t *testing.T) {
	machine := newTestMachine(ApplicationImmediate)
	clear, err := machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "clear-1",
		BindingKey: "state", Operation: MutationClear,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clear.State.AppliedControls["state"].Expression.Kind != ExpressionUnfiltered {
		t.Fatalf("clear state = %#v", clear.State.AppliedControls["state"])
	}
	reset, err := machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: clear.State.Revision, ClientMutationID: "reset-1",
		BindingKey: "state", Operation: MutationResetBinding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reset.State.AppliedControls["state"].Expression.Values[0].Value; got != "WA" {
		t.Fatalf("reset value = %#v, want WA", got)
	}
}

func TestMachineDeduplicatesMutationAndRejectsStaleRevision(t *testing.T) {
	machine := newTestMachine(ApplicationImmediate)
	command := Command{
		Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "m-1",
		BindingKey: "state", Operation: MutationClear,
	}
	first, err := machine.Execute(command)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := machine.Execute(command)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate || retry.State.Revision != first.State.Revision {
		t.Fatalf("retry = %#v, first = %#v", retry, first)
	}
	_, err = machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: command.BaseRevision, ClientMutationID: "m-2",
		BindingKey: "category", Operation: MutationClear,
	})
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale mutation error = %v", err)
	}
}

func TestMachineRejectsPredicateNotAllowedByCompiledDefinition(t *testing.T) {
	machine := newTestMachine(ApplicationImmediate)
	_, err := machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "m-disallowed",
		BindingKey: "state", Operation: MutationSet,
		Expression: &Expression{
			Kind: ExpressionComparison, Operator: OperatorContains,
			Value: &Value{Kind: ValueString, Value: "C"},
		},
	})
	if err == nil {
		t.Fatal("expected disallowed predicate to be rejected")
	}
}

func TestMachineSnapshotPreservesStateAndIdempotencyAcrossReplicaRestore(t *testing.T) {
	machine := newTestMachine(ApplicationImmediate)
	command := Command{
		Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "m-persisted",
		BindingKey: "state", Operation: MutationClear,
	}
	first, err := machine.Execute(command)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreMachine(ApplicationImmediate, testBindingSpecs(), machine.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	retry, err := restored.Execute(command)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate || retry.State.Revision != first.State.Revision {
		t.Fatalf("restored retry = %#v, first = %#v", retry, first)
	}
}

func TestRelativePeriodResolvesOnceAtAcceptedRevision(t *testing.T) {
	now := time.Date(2026, time.July, 23, 15, 45, 0, 0, time.UTC)
	machine := newTestMachine(ApplicationImmediate)
	machine.clock = func() time.Time { return now }
	result, err := machine.Execute(Command{
		Kind: CommandMutate, BaseRevision: machine.State().Revision, ClientMutationID: "m-relative",
		BindingKey: "purchase_date", Operation: MutationSet,
		Expression: &Expression{
			Kind: ExpressionRelativePeriod, Direction: DirectionPrevious, Count: 1,
			Unit: UnitMonth, IncludeCurrent: false, Anchor: AnchorCurrentTime,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	applied := result.State.AppliedControls["purchase_date"]
	if applied.Expression.Kind != ExpressionRelativePeriod || applied.ResolvedExpression.Kind != ExpressionRange {
		t.Fatalf("relative applied state = %#v", applied)
	}
	if applied.EvaluatedAt == nil || !applied.EvaluatedAt.Equal(now) {
		t.Fatalf("evaluatedAt = %v, want %v", applied.EvaluatedAt, now)
	}
	if got := applied.ResolvedExpression.Lower.Value.Value; got != "2026-06-01" {
		t.Fatalf("lower bound = %#v, want 2026-06-01", got)
	}
	if got := applied.ResolvedExpression.Upper.Value.Value; got != "2026-07-01" {
		t.Fatalf("upper bound = %#v, want 2026-07-01", got)
	}
}

func newTestMachine(mode ApplicationMode) *Machine {
	return NewMachine(mode, testBindingSpecs())
}

func testBindingSpecs() map[string]BindingSpec {
	return map[string]BindingSpec{
		"state": {
			ValueKind:  ValueString,
			Default:    Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "WA"}}},
			Selection:  SelectionPolicy{Mode: SelectionMultiple, MaxSelectedValues: 10},
			Editable:   true,
			Predicates: []PredicatePolicy{{Kind: ExpressionSet, Operators: []Operator{OperatorIn}}},
		},
		"category": {
			ValueKind: ValueString,
			Default:   Expression{Kind: ExpressionUnfiltered},
			Selection: SelectionPolicy{Mode: SelectionSingle},
			Editable:  true,
			Predicates: []PredicatePolicy{{
				Kind: ExpressionComparison, Operators: []Operator{OperatorContains},
			}},
		},
		"purchase_date": {
			ValueKind:  ValueDate,
			Default:    Expression{Kind: ExpressionUnfiltered},
			Editable:   true,
			Time:       TimeSemantics{Timezone: "UTC", Calendar: "gregorian", WeekStart: "monday"},
			Predicates: []PredicatePolicy{{Kind: ExpressionRelativePeriod}, {Kind: ExpressionRange}},
		},
	}
}
