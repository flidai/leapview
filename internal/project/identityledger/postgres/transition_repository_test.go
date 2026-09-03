package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
)

func TestPrepareTransitionExactlyReplaysImmutableEvidence(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	input := testTransition("instance-replay", "transition-replay", "bundle-replay")

	first, err := repo.PrepareTransition(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.PrepareTransition(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertTransitionEvidence(t, second, first)
	if !second.PreparedAt.Equal(first.PreparedAt) || !second.PhaseAt.Equal(first.PhaseAt) || !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("exact prepare replay changed timestamps: first=%#v replay=%#v", first, second)
	}

	advanced, err := repo.AdvanceTransitionPhase(ctx, input.InstanceID, input.TransitionID,
		identityledger.PhasePrepared, identityledger.PhaseIdentityPending, "")
	if err != nil {
		t.Fatal(err)
	}
	replayedAfterAdvance, err := repo.PrepareTransition(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	assertTransitionEvidence(t, replayedAfterAdvance, first)
	if replayedAfterAdvance.Phase != identityledger.PhaseIdentityPending || !replayedAfterAdvance.PhaseAt.Equal(advanced.PhaseAt) {
		t.Fatalf("prepare replay did not return advanced row: %#v", replayedAfterAdvance)
	}
}

func TestPrepareTransitionChangedGraphResourcesOrBundleConflicts(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	base := testTransition("instance-conflict", "transition-conflict", "bundle-conflict")
	if _, err := repo.PrepareTransition(ctx, base); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(identityledger.Transition) identityledger.Transition{
		"graph digest": func(input identityledger.Transition) identityledger.Transition {
			input.GraphDigest = "sha256:" + strings.Repeat("b", 64)
			return input
		},
		"resources": func(input identityledger.Transition) identityledger.Transition {
			input.Resources = append(input.Resources, resource("customers", projectgraph.KindModel))
			return input
		},
		"bundle": func(input identityledger.Transition) identityledger.Transition {
			input.BundleID = "bundle-conflict-changed"
			return input
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := repo.PrepareTransition(ctx, mutate(base)); !errors.Is(err, identityledger.ErrTransitionConflict) {
				t.Fatalf("changed transition error = %v, want conflict", err)
			}
		})
	}
}

func TestPrepareTransitionIDIsScopedToInstance(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	firstInput := testTransition("instance-one", "shared-transition-id", "bundle-one")
	secondInput := firstInput
	secondInput.InstanceID = "instance-two"
	secondInput.BundleID = "bundle-two"

	first, err := repo.PrepareTransition(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.PrepareTransition(ctx, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.TransitionID != second.TransitionID || first.InstanceID == second.InstanceID {
		t.Fatalf("same transition ID was not independently stored: first=%#v second=%#v", first, second)
	}
	for _, instanceID := range []string{firstInput.InstanceID, secondInput.InstanceID} {
		loaded, err := repo.LoadTransition(ctx, instanceID, firstInput.TransitionID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.InstanceID != instanceID {
			t.Fatalf("loaded transition instance = %q, want %q", loaded.InstanceID, instanceID)
		}
	}
}

func TestAdvanceTransitionPhaseCASProgressesToCompleted(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	input := testTransition("instance-phases", "transition-phases", "bundle-phases")
	if _, err := repo.PrepareTransition(ctx, input); err != nil {
		t.Fatal(err)
	}

	steps := [][2]identityledger.TransitionPhase{
		{identityledger.PhasePrepared, identityledger.PhaseIdentityPending},
		{identityledger.PhaseIdentityPending, identityledger.PhaseIdentityActive},
		{identityledger.PhaseIdentityActive, identityledger.PhaseDeliveryActive},
		{identityledger.PhaseDeliveryActive, identityledger.PhaseCompleted},
	}
	for _, step := range steps {
		got, err := repo.AdvanceTransitionPhase(ctx, input.InstanceID, input.TransitionID, step[0], step[1], "")
		if err != nil {
			t.Fatalf("advance %s -> %s: %v", step[0], step[1], err)
		}
		if got.Phase != step[1] || got.PhaseAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatalf("advance %s -> %s returned %#v", step[0], step[1], got)
		}
		if step[1] == identityledger.PhaseCompleted {
			if got.CompletedAt == nil || got.CompletedAt.IsZero() {
				t.Fatalf("completed transition has no completion timestamp: %#v", got)
			}
		} else if got.CompletedAt != nil {
			t.Fatalf("nonterminal transition unexpectedly completed: %#v", got)
		}
	}

	completed, err := repo.LoadTransition(ctx, input.InstanceID, input.TransitionID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Phase != identityledger.PhaseCompleted || completed.CompletedAt == nil {
		t.Fatalf("loaded completed transition = %#v", completed)
	}
}

func TestAdvanceTransitionPhaseRejectsStaleAndBackwardProgress(t *testing.T) {
	repo, admin := newLedgerDatabase(t)
	ctx := t.Context()
	input := testTransition("instance-phase-conflict", "transition-phase-conflict", "bundle-phase-conflict")
	if _, err := repo.PrepareTransition(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AdvanceTransitionPhase(ctx, input.InstanceID, input.TransitionID,
		identityledger.PhasePrepared, identityledger.PhaseIdentityPending, ""); err != nil {
		t.Fatal(err)
	}

	// A worker holding the prepared fencing token cannot skip over the phase
	// already claimed by another worker.
	if _, err := repo.AdvanceTransitionPhase(ctx, input.InstanceID, input.TransitionID,
		identityledger.PhasePrepared, identityledger.PhaseIdentityPending, "stale worker evidence"); !errors.Is(err, identityledger.ErrPhaseConflict) {
		t.Fatalf("stale phase error = %v, want phase conflict", err)
	}
	if _, err := repo.AdvanceTransitionPhase(ctx, input.InstanceID, input.TransitionID,
		identityledger.PhaseIdentityPending, identityledger.PhasePrepared, ""); !errors.Is(err, identityledger.ErrInvalidTransition) {
		t.Fatalf("backward phase error = %v, want invalid transition", err)
	}

	// The database guard remains authoritative if a caller bypasses the Go
	// repository validation.
	if _, err := admin.Exec(ctx, `
		UPDATE project.identity_activation_transition
		SET phase='prepared'
		WHERE instance_id='instance-phase-conflict' AND transition_id='transition-phase-conflict'`); err == nil {
		t.Fatal("database accepted backward transition phase")
	}
}

func TestListNonTerminalTransitionsExcludesCompletedRows(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	instanceID := "instance-list"
	terminal := testTransition(instanceID, "transition-terminal", "bundle-terminal")
	nonterminal := testTransition(instanceID, "transition-nonterminal", "bundle-nonterminal")
	for _, input := range []identityledger.Transition{terminal, nonterminal} {
		if _, err := repo.PrepareTransition(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range [][2]identityledger.TransitionPhase{
		{identityledger.PhasePrepared, identityledger.PhaseIdentityPending},
		{identityledger.PhaseIdentityPending, identityledger.PhaseIdentityActive},
		{identityledger.PhaseIdentityActive, identityledger.PhaseDeliveryActive},
		{identityledger.PhaseDeliveryActive, identityledger.PhaseCompleted},
	} {
		if _, err := repo.AdvanceTransitionPhase(ctx, terminal.InstanceID, terminal.TransitionID, step[0], step[1], ""); err != nil {
			t.Fatalf("complete terminal transition %s -> %s: %v", step[0], step[1], err)
		}
	}

	rows, err := repo.ListNonTerminalTransitions(ctx, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TransitionID != nonterminal.TransitionID || rows[0].Phase == identityledger.PhaseCompleted {
		t.Fatalf("nonterminal transitions = %#v", rows)
	}
}

func TestTransitionCutoverOwnershipIsSerializedPerInstance(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	first := testTransition("instance-cutover", "transition-first", "bundle-first")
	second := testTransition("instance-cutover", "transition-second", "bundle-second")
	for _, input := range []identityledger.Transition{first, second} {
		if _, err := repo.PrepareTransition(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.AdvanceTransition(ctx, first.InstanceID, first.TransitionID,
		identityledger.PhasePrepared, identityledger.PhaseIdentityPending, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AdvanceTransition(ctx, second.InstanceID, second.TransitionID,
		identityledger.PhasePrepared, identityledger.PhaseIdentityPending, ""); !errors.Is(err, identityledger.ErrPhaseConflict) {
		t.Fatalf("second cutover claim error = %v, want phase conflict", err)
	}
	inFlight, err := repo.ListInFlightTransitions(ctx, first.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inFlight) != 1 || inFlight[0].TransitionID != first.TransitionID {
		t.Fatalf("in-flight cutovers = %#v", inFlight)
	}
}

func TestRollbackTransitionsMayReuseHistoricalBundle(t *testing.T) {
	repo, _ := newLedgerDatabase(t)
	ctx := t.Context()
	first := testTransition("instance-repeat-rollback", "rollback-first", "bundle-history")
	first.Operation = identityledger.OperationRollback
	second := first
	second.TransitionID = "rollback-second"
	for _, input := range []identityledger.Transition{first, second} {
		if _, err := repo.PrepareTransition(ctx, input); err != nil {
			t.Fatalf("prepare repeated rollback target: %v", err)
		}
	}
}

func TestTransitionJournalDatabaseRejectsImmutableUpdateDeleteAndTruncate(t *testing.T) {
	repo, admin := newLedgerDatabase(t)
	ctx := t.Context()
	input := testTransition("instance-immutable", "transition-immutable", "bundle-immutable")
	if _, err := repo.PrepareTransition(ctx, input); err != nil {
		t.Fatal(err)
	}

	if _, err := admin.Exec(ctx, `
		UPDATE project.identity_activation_transition
		SET bundle_id='bundle-mutated'
		WHERE instance_id='instance-immutable' AND transition_id='transition-immutable'`); err == nil {
		t.Fatal("database accepted immutable transition parameter update")
	}
	if _, err := admin.Exec(ctx, `
		DELETE FROM project.identity_activation_transition
		WHERE instance_id='instance-immutable' AND transition_id='transition-immutable'`); err == nil {
		t.Fatal("database accepted transition deletion")
	}
	if _, err := admin.Exec(ctx, `TRUNCATE project.identity_activation_transition`); err == nil {
		t.Fatal("database accepted transition truncation")
	}

	loaded, err := repo.LoadTransition(ctx, input.InstanceID, input.TransitionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BundleID != input.BundleID || loaded.Phase != identityledger.PhasePrepared {
		t.Fatalf("immutable row changed after rejected mutations: %#v", loaded)
	}
}

func TestCompletedTransitionRejectsProgressMutation(t *testing.T) {
	repo, admin := newLedgerDatabase(t)
	ctx := t.Context()
	input := testTransition("instance-terminal", "transition-terminal-immutable", "bundle-terminal-immutable")
	if _, err := repo.PrepareTransition(ctx, input); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]identityledger.TransitionPhase{
		{identityledger.PhasePrepared, identityledger.PhaseIdentityPending},
		{identityledger.PhaseIdentityPending, identityledger.PhaseIdentityActive},
		{identityledger.PhaseIdentityActive, identityledger.PhaseDeliveryActive},
		{identityledger.PhaseDeliveryActive, identityledger.PhaseCompleted},
	} {
		if _, err := repo.AdvanceTransition(ctx, input.InstanceID, input.TransitionID, step[0], step[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `
		UPDATE project.identity_activation_transition SET phase_error='tampered'
		WHERE instance_id='instance-terminal' AND transition_id='transition-terminal-immutable'`); err == nil {
		t.Fatal("database accepted completed transition mutation")
	}
}

func assertTransitionEvidence(t *testing.T, got, want identityledger.Transition) {
	t.Helper()
	if got.TransitionID != want.TransitionID || got.Operation != want.Operation || got.InstanceID != want.InstanceID ||
		got.CandidateID != want.CandidateID || got.BundleID != want.BundleID || got.ExpectedBundleID != want.ExpectedBundleID ||
		got.ActorID != want.ActorID || got.Reason != want.Reason || got.GraphDigest != want.GraphDigest ||
		!reflect.DeepEqual(got.Resources, want.Resources) {
		t.Fatalf("transition evidence = %#v, want %#v", got, want)
	}
}

func testTransition(instanceID, transitionID, bundleID string) identityledger.Transition {
	return identityledger.Transition{
		TransitionID: transitionID,
		Operation:    identityledger.OperationPublish,
		InstanceID:   instanceID,
		CandidateID:  "candidate-" + transitionID,
		BundleID:     bundleID,
		ActorID:      "publisher",
		Reason:       "activation transition test",
		Resources: []identityledger.Resource{
			resource("orders-model", projectgraph.KindModel),
			resource("orders", projectgraph.KindSource),
		},
		GraphDigest: "sha256:" + strings.Repeat("a", 64),
	}
}
