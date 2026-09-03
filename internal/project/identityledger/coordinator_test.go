package identityledger

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type coordinatorFake struct {
	transition   Transition
	observed     string
	outcomes     []Outcome
	events       []string
	activate     []Candidate
	restores     []Restore
	rollbacks    []Rollback
	plans        []Candidate
	advanceError error
}

func (f *coordinatorFake) PrepareTransition(_ context.Context, input Transition) (Transition, error) {
	f.events = append(f.events, "prepare")
	if f.transition.TransitionID == "" {
		f.transition = input
		f.transition.Phase = PhasePrepared
		f.transition.Error = ""
	}
	if !SameTransitionEvidence(f.transition, input) {
		return Transition{}, ErrTransitionConflict
	}
	return f.transition, nil
}

func (f *coordinatorFake) LoadTransition(_ context.Context, _, _ string) (Transition, error) {
	f.events = append(f.events, "load")
	return f.transition, nil
}

func (f *coordinatorFake) AdvanceTransition(_ context.Context, _, _ string, expected, next TransitionPhase, phaseError string) (Transition, error) {
	f.events = append(f.events, "advance:"+string(expected)+"->"+string(next))
	if f.advanceError != nil {
		return Transition{}, f.advanceError
	}
	if f.transition.Phase != expected && f.transition.Phase != next {
		return Transition{}, ErrPhaseConflict
	}
	f.transition.Phase = next
	f.transition.Error = phaseError
	return f.transition, nil
}

func (f *coordinatorFake) Plan(_ context.Context, candidate Candidate) (Plan, error) {
	f.events = append(f.events, "plan")
	f.plans = append(f.plans, candidate)
	return Plan{InstanceID: candidate.InstanceID, ObservedBundleID: f.observed, CandidateBundleID: candidate.BundleID, Outcomes: f.outcomes}, nil
}

func (f *coordinatorFake) Activate(_ context.Context, candidate Candidate) (Plan, error) {
	f.events = append(f.events, "activate")
	f.activate = append(f.activate, candidate)
	f.observed = candidate.BundleID
	return Plan{InstanceID: candidate.InstanceID, ObservedBundleID: candidate.ExpectedBundleID, CandidateBundleID: candidate.BundleID}, nil
}

func (f *coordinatorFake) RestoreAndActivate(_ context.Context, request Restore) (Plan, error) {
	f.events = append(f.events, "restore")
	f.restores = append(f.restores, request)
	f.observed = request.Candidate.BundleID
	return Plan{InstanceID: request.Candidate.InstanceID, ObservedBundleID: request.Candidate.ExpectedBundleID, CandidateBundleID: request.Candidate.BundleID}, nil
}

func (f *coordinatorFake) Rollback(_ context.Context, request Rollback) (Plan, error) {
	f.events = append(f.events, "rollback")
	f.rollbacks = append(f.rollbacks, request)
	f.observed = request.BundleID
	return Plan{InstanceID: request.InstanceID, ObservedBundleID: request.ExpectedBundleID, CandidateBundleID: request.BundleID}, nil
}

func coordinatorTestTransition(operation TransitionOperation) Transition {
	transition := Transition{
		TransitionID: "transition-1", Operation: operation, InstanceID: "instance-1",
		CandidateID: "candidate-1", BundleID: "bundle-next", ExpectedBundleID: "bundle-current",
		ActorID: "actor-1", Reason: "coordinator test", GraphDigest: "sha256:" + strings.Repeat("a", 64),
		Resources: []Resource{{AuthoredID: "orders", Kind: projectgraph.KindSource}},
	}
	if operation == OperationRestore {
		transition.ApprovedAuthoredIDs = []projectgraph.ResourceID{"orders"}
	}
	return transition
}

func TestCoordinatorTransitionCases(t *testing.T) {
	tests := []struct {
		name             string
		operation        TransitionOperation
		observed         string
		phase            TransitionPhase
		outcomes         []Outcome
		commitErrors     []error
		wantErr          error
		wantPhase        TransitionPhase
		wantCommitCalls  int
		wantActivate     int
		wantRestore      int
		wantRollback     int
		wantAdvanceError string
		wantEvents       []string
	}{
		{
			name: "publish ordering", operation: OperationPublish, observed: "bundle-current",
			wantPhase: PhaseCompleted, wantCommitCalls: 1, wantActivate: 1,
			wantEvents: []string{"prepare", "load", "plan", "advance:prepared->identity_pending", "plan", "activate", "advance:identity_pending->identity_active", "plan", "delivery", "advance:identity_active->delivery_active", "advance:delivery_active->completed"},
		},
		{
			name: "rollback exact historical bundle", operation: OperationRollback, observed: "bundle-current",
			outcomes:  []Outcome{{AuthoredID: "orders", Kind: projectgraph.KindSource, Outcome: OutcomeRestoreRequired}},
			wantPhase: PhaseCompleted, wantCommitCalls: 1, wantRollback: 1,
			wantEvents: []string{"prepare", "load", "plan", "advance:prepared->identity_pending", "plan", "rollback", "advance:identity_pending->identity_active", "plan", "delivery", "advance:identity_active->delivery_active", "advance:delivery_active->completed"},
		},
		{
			name: "explicit restore", operation: OperationRestore, observed: "bundle-current",
			outcomes:  []Outcome{{AuthoredID: "orders", Kind: projectgraph.KindSource, Outcome: OutcomeRestoreRequired}},
			wantPhase: PhaseCompleted, wantCommitCalls: 1, wantRestore: 1,
			wantEvents: []string{"prepare", "load", "plan", "advance:prepared->identity_pending", "plan", "restore", "advance:identity_pending->identity_active", "plan", "delivery", "advance:identity_active->delivery_active", "advance:delivery_active->completed"},
		},
		{
			name: "publish collision rejection", operation: OperationPublish, observed: "bundle-current",
			outcomes: []Outcome{{AuthoredID: "orders", Kind: projectgraph.KindSource, Outcome: OutcomeCollision, Detail: "kind changed"}},
			wantErr:  ErrKindConflict, wantPhase: PhasePrepared, wantEvents: []string{"prepare", "load", "plan"},
		},
		{
			name: "publish tombstone rejection", operation: OperationPublish, observed: "bundle-current",
			outcomes: []Outcome{{AuthoredID: "orders", Kind: projectgraph.KindSource, Outcome: OutcomeRestoreRequired, Detail: "tombstoned"}},
			wantErr:  ErrRestoreRequired, wantPhase: PhasePrepared, wantEvents: []string{"prepare", "load", "plan"},
		},
		{
			name: "stale observed bundle", operation: OperationPublish, observed: "bundle-stale",
			wantErr: ErrActivationConflict, wantPhase: PhasePrepared, wantEvents: []string{"prepare", "load", "plan"},
		},
		{
			name: "identity commit crash recovery", operation: OperationPublish, observed: "bundle-next",
			wantPhase: PhaseCompleted, wantCommitCalls: 1, wantActivate: 0,
			wantEvents: []string{"prepare", "load", "plan", "advance:prepared->identity_pending", "plan", "advance:identity_pending->identity_active", "plan", "delivery", "advance:identity_active->delivery_active", "advance:delivery_active->completed"},
		},
		{
			name: "identity active stale delivery observation", operation: OperationPublish, observed: "bundle-other", phase: PhaseIdentityActive,
			wantErr: ErrActivationConflict, wantPhase: PhaseIdentityActive, wantCommitCalls: 0,
			wantEvents: []string{"prepare", "load", "plan"},
		},
		{
			name: "completed replay", operation: OperationPublish, observed: "bundle-next", phase: PhaseCompleted,
			wantPhase: PhaseCompleted, wantCommitCalls: 0, wantActivate: 0,
			wantEvents: []string{"prepare", "load"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := coordinatorTestTransition(test.operation)
			fake := &coordinatorFake{observed: test.observed, outcomes: test.outcomes}
			if test.phase != "" {
				fake.transition = input
				fake.transition.Phase = test.phase
			}
			commitCalls := 0
			commitErrorIndex := 0
			commit := DeliveryCommit(func(context.Context) error {
				fake.events = append(fake.events, "delivery")
				commitCalls++
				if commitErrorIndex < len(test.commitErrors) {
					err := test.commitErrors[commitErrorIndex]
					commitErrorIndex++
					return err
				}
				return nil
			})
			coordinator := NewCoordinator(fake)
			got, err := coordinator.Run(t.Context(), input, commit)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got.Phase != test.wantPhase {
				t.Fatalf("phase = %q, want %q", got.Phase, test.wantPhase)
			}
			if commitCalls != test.wantCommitCalls || len(fake.activate) != test.wantActivate || len(fake.restores) != test.wantRestore || len(fake.rollbacks) != test.wantRollback {
				t.Fatalf("calls delivery=%d activate=%d restore=%d rollback=%d", commitCalls, len(fake.activate), len(fake.restores), len(fake.rollbacks))
			}
			if !reflect.DeepEqual(fake.events, test.wantEvents) {
				t.Fatalf("events = %#v, want %#v", fake.events, test.wantEvents)
			}
			if test.operation == OperationRollback && len(fake.rollbacks) == 1 {
				request := fake.rollbacks[0]
				if request.InstanceID != input.InstanceID || request.BundleID != input.BundleID || request.ExpectedBundleID != input.ExpectedBundleID || request.ActorID != input.ActorID || request.Reason != input.Reason {
					t.Fatalf("rollback request = %#v, does not match exact transition %#v", request, input)
				}
			}
			if test.operation == OperationRestore && len(fake.restores) == 1 {
				request := fake.restores[0]
				if request.Candidate.InstanceID != input.InstanceID || request.Candidate.BundleID != input.BundleID || request.Candidate.ExpectedBundleID != input.ExpectedBundleID || request.Candidate.ActorID != input.ActorID || request.Reason != input.Reason || !reflect.DeepEqual(request.AuthoredIDs, input.ApprovedAuthoredIDs) {
					t.Fatalf("restore request = %#v, does not match exact transition %#v", request, input)
				}
			}
		})
	}

}

func TestRestorePreviewRequiresExactTombstoneApprovalSet(t *testing.T) {
	basePlan := Plan{Outcomes: []Outcome{
		{AuthoredID: "orders", Kind: projectgraph.KindSource, Outcome: OutcomeRestoreRequired},
		{AuthoredID: "customers", Kind: projectgraph.KindSource, Outcome: OutcomeRestoreRequired},
		{AuthoredID: "active-model", Kind: projectgraph.KindModel, Outcome: OutcomeUpdated},
	}}
	tests := []struct {
		name     string
		plan     Plan
		approved []projectgraph.ResourceID
		wantErr  error
	}{
		{name: "exact", plan: basePlan, approved: []projectgraph.ResourceID{"customers", "orders"}},
		{name: "missing tombstone", plan: basePlan, approved: []projectgraph.ResourceID{"orders"}, wantErr: ErrRestoreRequired},
		{name: "active extra", plan: basePlan, approved: []projectgraph.ResourceID{"customers", "orders", "active-model"}, wantErr: ErrRestoreRequired},
		{name: "unknown extra", plan: basePlan, approved: []projectgraph.ResourceID{"customers", "orders", "not-in-candidate"}, wantErr: ErrRestoreRequired},
		{name: "collision", plan: Plan{Outcomes: []Outcome{{AuthoredID: "orders", Kind: projectgraph.KindModel, Outcome: OutcomeCollision, Detail: "immutable kind"}}}, approved: []projectgraph.ResourceID{"orders"}, wantErr: ErrKindConflict},
		{name: "no tombstones", plan: Plan{Outcomes: []Outcome{{AuthoredID: "active-model", Kind: projectgraph.KindModel, Outcome: OutcomeUpdated}}}, approved: []projectgraph.ResourceID{"active-model"}, wantErr: ErrRestoreRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := rejectRestoreBlockingOutcomes(test.plan, test.approved)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("restore preview error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("restore preview error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCoordinatorDeliveryFailureRecordsAndResumes(t *testing.T) {
	input := coordinatorTestTransition(OperationPublish)
	fake := &coordinatorFake{observed: input.ExpectedBundleID}
	coordinator := NewCoordinator(fake)
	failure := errors.New("delivery unavailable")
	firstCalls := 0
	first, err := coordinator.Run(t.Context(), input, func(context.Context) error {
		firstCalls++
		return failure
	})
	if !errors.Is(err, failure) || first.Phase != PhaseIdentityActive || first.Error != failure.Error() {
		t.Fatalf("first attempt transition=%#v error=%v", first, err)
	}
	if firstCalls != 1 || fake.transition.Phase != PhaseIdentityActive {
		t.Fatalf("first attempt calls=%d phase=%q", firstCalls, fake.transition.Phase)
	}

	secondCalls := 0
	second, err := coordinator.Run(t.Context(), input, func(context.Context) error {
		secondCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Phase != PhaseCompleted || second.Error != "" || secondCalls != 1 {
		t.Fatalf("resumed transition=%#v calls=%d", second, secondCalls)
	}
}

func TestCoordinatorRestoreResumesFromDurableIdentityActivePhase(t *testing.T) {
	input := coordinatorTestTransition(OperationRestore)
	fake := &coordinatorFake{
		observed: input.ExpectedBundleID,
		outcomes: []Outcome{{AuthoredID: "orders", Kind: projectgraph.KindSource, Outcome: OutcomeRestoreRequired}},
	}
	coordinator := NewCoordinator(fake)
	failure := errors.New("delivery unavailable")
	first, err := coordinator.Run(t.Context(), input, func(context.Context) error {
		return failure
	})
	if !errors.Is(err, failure) || first.Phase != PhaseIdentityActive {
		t.Fatalf("first restore attempt transition=%#v error=%v", first, err)
	}
	if len(fake.restores) != 1 {
		t.Fatalf("first restore attempt restore calls=%d, want 1", len(fake.restores))
	}
	second, err := coordinator.Run(t.Context(), input, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if second.Phase != PhaseCompleted || len(fake.restores) != 1 {
		t.Fatalf("resumed restore transition=%#v restore calls=%d", second, len(fake.restores))
	}
}

func TestPhaseErrorTruncationPreservesUTF8(t *testing.T) {
	message := phaseError(errors.New(strings.Repeat("界", 2000)))
	if len(message) > 4096 || !strings.HasSuffix(message, "界") {
		t.Fatalf("phase error was not safely truncated: bytes=%d", len(message))
	}
}
