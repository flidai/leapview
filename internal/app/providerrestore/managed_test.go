package providerrestore

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/recoveryset"
)

type testPrimaryFence struct {
	err   error
	calls int
	set   recoveryset.RecoverySet
	after func()
}

func (f *testPrimaryFence) Verify(_ context.Context, set recoveryset.RecoverySet) error {
	f.calls++
	f.set = set
	if f.after != nil {
		f.after()
	}
	return f.err
}

func TestManagedPhysicalObservationCannotExtendRecoveryOwnership(t *testing.T) {
	f := &testPrimaryFence{}
	x := managedFixture(t, f)
	f.after = func() {
		if f.calls == 2 {
			x.ledger.occurrence.Fence.Generation++
		}
	}
	if _, err := x.coordinator.Run(t.Context(), x.request); err == nil {
		t.Fatal("stale owner proceeded after slow primary-fence observation")
	}
	if len(x.calls) != 0 || x.ledger.completed {
		t.Fatalf("stale owner made provider effects: %v", x.calls)
	}
}

func managedFixture(t *testing.T, f *testPrimaryFence) *coordinatorFixture {
	t.Helper()
	x := newCoordinatorFixture(t)
	var err error
	x.coordinator, err = NewManaged(x.coordinator.dependencies, f)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestManagedRecoveryRequiresPhysicalFence(t *testing.T) {
	x := newCoordinatorFixture(t)
	if _, err := NewManaged(x.coordinator.dependencies, nil); err == nil {
		t.Fatal("missing primary fence accepted")
	}
	var missing *testPrimaryFence
	if _, err := NewManaged(x.coordinator.dependencies, missing); err == nil {
		t.Fatal("typed-nil primary fence accepted")
	}
}

func TestManagedRecoveryRejectsLivePrimaryBeforeAnyEffects(t *testing.T) {
	f := &testPrimaryFence{err: errors.New("original primary is running")}
	x := managedFixture(t, f)
	if _, err := x.coordinator.Run(t.Context(), x.request); err == nil {
		t.Fatal("recovery proceeded with live original primary")
	}
	if len(x.calls) != 0 || len(x.ledger.occurrence.Evidence) != 0 || x.ledger.completed {
		t.Fatalf("unfenced recovery mutated state: %v", x.calls)
	}
	if f.set.ID != x.set.ID || f.set.FrontierDigest != x.set.FrontierDigest {
		t.Fatal("physical fence did not receive exact authoritative frontier")
	}
}

func TestManagedRecoveryRechecksFenceAfterEffectsBeforePublication(t *testing.T) {
	f := &testPrimaryFence{}
	x := managedFixture(t, f)
	x.databases.afterRestore = func() { f.err = errors.New("original writer fence lost") }
	if _, err := x.coordinator.Run(t.Context(), x.request); err == nil {
		t.Fatal("lost original writer fence accepted")
	}
	if x.ledger.completed || x.sets.set.Status == recoveryset.StatusPublished {
		t.Fatal("replacement admitted after original fence loss")
	}
	if f.calls < 2 {
		t.Fatal("primary fence not rechecked")
	}
}

func TestManagedCompletedRecoveryStillRequiresLiveFenceObservation(t *testing.T) {
	f := &testPrimaryFence{}
	x := managedFixture(t, f)
	if _, err := x.coordinator.Run(t.Context(), x.request); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("original host unavailable; cannot confirm fence")
	if _, err := x.coordinator.Run(t.Context(), x.request); err == nil {
		t.Fatal("stale completed receipt substituted for physical fence")
	}
}
