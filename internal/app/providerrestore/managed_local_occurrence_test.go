package providerrestore

import (
	"context"
	"errors"
	"testing"
)

type localHandoffFunc func(context.Context, HandoffRequest) (ReplacementHandoff, error)

func (function localHandoffFunc) CreateHandoff(ctx context.Context, request HandoffRequest) (ReplacementHandoff, error) {
	return function(ctx, request)
}

func localCoordinatorFixture(t *testing.T, foreign bool) *coordinatorFixture {
	t.Helper()
	fixture := newCoordinatorFixture(t)
	set, handoff := managedLocalHandoffFixture(t)
	set.FrontierDigest = ""
	set, err := set.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	set.FrontierDigest, err = set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.set, fixture.sets.set = set, set
	handoff.FrontierDigest = set.FrontierDigest
	for index := range handoff.Providers {
		for _, point := range set.ClusterPoints {
			if handoff.Providers[index].Role == string(point.DatabaseRole) {
				handoff.Providers[index].Database = point.DatabaseIdentity
			}
		}
	}
	handoff.ManagedLocal.OccurrenceID = fixture.request.OccurrenceID
	if foreign {
		handoff.ManagedLocal.OccurrenceID = "foreign-occurrence"
	}
	coordinator, err := New(Dependencies{Ledger: fixture.ledger, Sets: fixture.sets, Databases: fixture.databases, Objects: fixture.objects, Verifier: fixture.verifier, Evidence: fixture.store, Handoff: localHandoffFunc(func(context.Context, HandoffRequest) (ReplacementHandoff, error) { return handoff, nil }), Now: fixture.now})
	if err != nil {
		t.Fatal(err)
	}
	fixture.coordinator = coordinator
	return fixture
}

func TestManagedLocalCoordinatorRejectsForeignProducerOccurrence(t *testing.T) {
	fixture := localCoordinatorFixture(t, true)
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if !errors.Is(err, ErrInconsistent) || report.Status != StatusFailed || fixture.ledger.completed {
		t.Fatalf("foreign producer accepted: status=%s err=%v", report.Status, err)
	}
}

func TestManagedLocalCoordinatorRejectsForeignCompletedOccurrence(t *testing.T) {
	fixture := localCoordinatorFixture(t, false)
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); err != nil {
		t.Fatal(err)
	}
	ref := fixture.ledger.occurrence.Evidence[0]
	report := fixture.store.reports[ref.URI]
	report.Handoff.ManagedLocal.OccurrenceID = "foreign-occurrence"
	fixture.store.reports[ref.URI] = report
	if _, err := fixture.coordinator.Run(t.Context(), fixture.request); !errors.Is(err, ErrInconsistent) {
		t.Fatalf("foreign completed occurrence accepted: %v", err)
	}
}

func TestManagedLocalConsumerBindsActualOccurrenceAndRejectsRemoteProfile(t *testing.T) {
	fixture := localCoordinatorFixture(t, false)
	report, err := fixture.coordinator.Run(t.Context(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	expected := HandoffExpectations{OccurrenceID: fixture.request.OccurrenceID, TargetID: fixture.request.TargetID, RecoverySetID: fixture.set.ID, FrontierDigest: fixture.set.FrontierDigest, ArtifactIdentity: testRunnableArtifact}
	if err := ValidateManagedLocalHandoffReport(report, fixture.set, expected); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHandoffReport(report, expected); err == nil {
		t.Fatal("remote consumer accepted local profile")
	}
	report.Handoff.ManagedLocal.OccurrenceID = "foreign-occurrence"
	if err := ValidateManagedLocalHandoffReport(report, fixture.set, expected); err == nil {
		t.Fatal("local consumer accepted foreign occurrence")
	}
	remote := validReplacementHandoff(fixture.set)
	report.Handoff = remote
	if err := ValidateManagedLocalHandoffReport(report, fixture.set, expected); err == nil {
		t.Fatal("local consumer accepted remote profile")
	}
}
