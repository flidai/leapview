package identityledger

import (
	"errors"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestNormalizeTransitionSortsEvidenceAndValidatesGraphDigest(t *testing.T) {
	graphDigest := "sha256:" + strings.Repeat("a", 64)
	transition, err := NormalizeTransition(Transition{
		TransitionID: "transition-1", Operation: OperationPublish, InstanceID: "instance-1",
		CandidateID: "candidate-1", BundleID: "bundle-1", ActorID: "actor-1", GraphDigest: graphDigest,
		Resources: []Resource{
			{AuthoredID: "zeta", Kind: projectgraph.KindModel},
			{AuthoredID: "alpha", Kind: projectgraph.KindSource},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.Phase != PhasePrepared || transition.GraphDigest != graphDigest {
		t.Fatalf("normalized transition = %#v", transition)
	}
	if transition.Resources[0].AuthoredID != "alpha" {
		t.Fatalf("resources were not sorted: %#v", transition.Resources)
	}
	reordered := transition
	reordered.Resources = []Resource{transition.Resources[1], transition.Resources[0]}
	if err := ValidateTransition(reordered); err != nil {
		t.Fatalf("reordered equivalent evidence rejected: %v", err)
	}
	reordered.GraphDigest = "sha256:" + strings.Repeat("g", 64)
	if err := ValidateTransition(reordered); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad evidence digest error = %v", err)
	}
}

func TestTransitionPhasesOnlyAdvanceForward(t *testing.T) {
	for _, pair := range [][2]TransitionPhase{
		{PhasePrepared, PhaseIdentityPending},
		{PhaseIdentityPending, PhaseIdentityActive},
		{PhaseIdentityActive, PhaseDeliveryActive},
		{PhaseDeliveryActive, PhaseCompleted},
	} {
		if !CanAdvance(pair[0], pair[1]) {
			t.Errorf("CanAdvance(%q, %q) = false", pair[0], pair[1])
		}
	}
	if CanAdvance(PhaseDeliveryActive, PhaseIdentityActive) || CanAdvance(PhaseCompleted, PhasePrepared) {
		t.Fatal("phase graph permits backward/terminal transitions")
	}
}
