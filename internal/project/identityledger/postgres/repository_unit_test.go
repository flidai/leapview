package postgres

import (
	"errors"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
)

func TestPlanOutcomesReportCrossGenerationState(t *testing.T) {
	identities := map[projectgraph.ResourceID]identityledger.Identity{
		"same": {
			AuthoredID: "same", Kind: projectgraph.KindSource,
			Lifecycle: identityledger.LifecycleActive,
		},
		"removed": {
			AuthoredID: "removed", Kind: projectgraph.KindModel,
			Lifecycle: identityledger.LifecycleActive,
		},
		"restore": {
			AuthoredID: "restore", Kind: projectgraph.KindDashboard,
			Lifecycle: identityledger.LifecycleTombstoned,
		},
		"collision": {
			AuthoredID: "collision", Kind: projectgraph.KindSource,
			Lifecycle: identityledger.LifecycleActive,
		},
	}
	outcomes := planOutcomes([]identityledger.Resource{
		{AuthoredID: "created", Kind: projectgraph.KindConnection},
		{AuthoredID: "same", Kind: projectgraph.KindSource},
		{AuthoredID: "restore", Kind: projectgraph.KindDashboard},
		{AuthoredID: "collision", Kind: projectgraph.KindModel},
	}, identities)
	want := map[projectgraph.ResourceID]identityledger.OutcomeKind{
		"collision": identityledger.OutcomeCollision,
		"created":   identityledger.OutcomeCreated,
		"removed":   identityledger.OutcomeTombstoned,
		"restore":   identityledger.OutcomeRestoreRequired,
		"same":      identityledger.OutcomeUpdated,
	}
	for _, outcome := range outcomes {
		if outcome.Outcome != want[outcome.AuthoredID] {
			t.Errorf("outcome %q = %q, want %q", outcome.AuthoredID, outcome.Outcome, want[outcome.AuthoredID])
		}
		delete(want, outcome.AuthoredID)
	}
	if len(want) != 0 {
		t.Fatalf("missing outcomes: %#v", want)
	}
}

func TestRestoreAuthorizationIsExactAndKindSafe(t *testing.T) {
	resources := []identityledger.Resource{{AuthoredID: "orders", Kind: projectgraph.KindSource}}
	identities := map[projectgraph.ResourceID]identityledger.Identity{
		"orders": {AuthoredID: "orders", Kind: projectgraph.KindSource, Lifecycle: identityledger.LifecycleTombstoned},
	}
	if err := validateCandidateAgainstLedger(resources, identities, nil); !errors.Is(err, identityledger.ErrRestoreRequired) {
		t.Fatalf("implicit restore error = %v", err)
	}
	approved := map[projectgraph.ResourceID]struct{}{"orders": {}}
	if err := validateCandidateAgainstLedger(resources, identities, approved); err != nil {
		t.Fatal(err)
	}
	outcomes := planOutcomes(resources, identities)
	markRestoredOutcomes(outcomes, approved, false)
	if len(outcomes) != 1 || outcomes[0].Outcome != identityledger.OutcomeRestored {
		t.Fatalf("restore outcomes = %#v", outcomes)
	}
	if err := validateCandidateAgainstLedger(
		[]identityledger.Resource{{AuthoredID: "orders", Kind: projectgraph.KindModel}}, identities, approved,
	); !errors.Is(err, identityledger.ErrKindConflict) {
		t.Fatalf("kind-change restore error = %v", err)
	}
}

func TestRestoreReasonMustBeCanonicalBeforeDatabaseAccess(t *testing.T) {
	request := identityledger.Restore{
		Candidate:   identityledger.Candidate{InstanceID: "instance-1", BundleID: "bundle-2", ExpectedBundleID: "bundle-1"},
		AuthoredIDs: []projectgraph.ResourceID{"orders"},
		Reason:      " padded reason ",
	}
	if _, err := (&Repository{}).RestoreAndActivate(t.Context(), request); !errors.Is(err, identityledger.ErrInvalidInput) {
		t.Fatalf("padded restore reason error = %v, want invalid input", err)
	}
}
