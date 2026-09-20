package ownership

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type testAuthority struct {
	report access.OwnershipReport
	err    error
	bound  bool
	bindDB access.OwnershipDBTX
}

type mutatingAuthority struct {
	testAuthority
	transferred bool
	tombstoned  bool
}

func (a *mutatingAuthority) TransferOwnedObjects(context.Context, string, string) (access.OwnershipReport, error) {
	a.transferred = true
	return a.report, nil
}

func (a *mutatingAuthority) TombstoneOwnedObjects(context.Context, string) (access.OwnershipReport, error) {
	a.tombstoned = true
	return a.report, nil
}

func (a *mutatingAuthority) WithOwnershipMutationDB(db access.OwnershipDBTX) access.OwnershipMutator {
	copy := *a
	copy.bound = true
	copy.bindDB = db
	return &copy
}

func (a *testAuthority) ListOwnedObjects(context.Context, string) (access.OwnershipReport, error) {
	if a.err != nil {
		return access.OwnershipReport{}, a.err
	}
	return a.report, nil
}

func (a *testAuthority) WithOwnershipDB(db access.OwnershipDBTX) access.OwnershipAuthority {
	copy := *a
	copy.bound = true
	copy.bindDB = db
	return &copy
}

func TestInventoryMergesValidatesAndOrdersDomainEvidence(t *testing.T) {
	first := &testAuthority{report: access.OwnershipReport{PrincipalID: "principal-1", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "z", OwnerPrincipalID: "principal-1", Lifecycle: "draft"}}}}
	second := &testAuthority{report: access.OwnershipReport{PrincipalID: "principal-1", Objects: []access.OwnedObject{{Kind: "agent_conversation", ID: "a", OwnerPrincipalID: "principal-1", Lifecycle: "active"}}}}
	inventory, err := New(first, second)
	if err != nil {
		t.Fatal(err)
	}
	report, err := inventory.ListOwnedObjects(context.Background(), " principal-1 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []access.OwnedObject{second.report.Objects[0], first.report.Objects[0]}
	if !reflect.DeepEqual(report.Objects, want) {
		t.Fatalf("objects = %#v, want %#v", report.Objects, want)
	}
	if err := inventory.EnsureOffboardingSafe(context.Background(), "principal-1"); !errors.Is(err, access.ErrOwnershipConflict) {
		t.Fatalf("guard error = %v, want ownership conflict", err)
	}
}

func TestInventoryBindsTransactionalAuthoritiesAndRejectsBadEvidence(t *testing.T) {
	authority := &testAuthority{report: access.OwnershipReport{PrincipalID: "principal-1", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "one", OwnerPrincipalID: "principal-1", Lifecycle: "draft"}}}}
	inventory, err := New(authority)
	if err != nil {
		t.Fatal(err)
	}
	bound := inventory.WithOwnershipDB(nil)
	boundInventory, ok := bound.(*Inventory)
	if !ok || len(boundInventory.authorities) != 1 {
		t.Fatalf("bound inventory = %#v", bound)
	}
	if got, ok := boundInventory.authorities[0].(*testAuthority); !ok || !got.bound {
		t.Fatalf("authority was not transaction-bound: %#v", boundInventory.authorities[0])
	}

	bad, err := New(&testAuthority{report: access.OwnershipReport{PrincipalID: "other", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "one", OwnerPrincipalID: "principal-1", Lifecycle: "draft"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.ListOwnedObjects(context.Background(), "principal-1"); err == nil {
		t.Fatal("inconsistent owner report was accepted")
	}

	duplicate, err := New(
		&testAuthority{report: access.OwnershipReport{PrincipalID: "principal-1", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "one", OwnerPrincipalID: "principal-1", Lifecycle: "draft"}}}},
		&testAuthority{report: access.OwnershipReport{PrincipalID: "principal-1", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "one", OwnerPrincipalID: "principal-1", Lifecycle: "draft"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.ListOwnedObjects(context.Background(), "principal-1"); err == nil {
		t.Fatal("duplicate object evidence was accepted")
	}
}

func TestNewRejectsNilAuthority(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil ownership authority was accepted")
	}
}

func TestInventoryResolvesTransferAndTombstoneThroughTransactionalAuthorities(t *testing.T) {
	transfer := &mutatingAuthority{testAuthority: testAuthority{report: access.OwnershipReport{PrincipalID: "owner", Objects: []access.OwnedObject{{Kind: "dashboard", ID: "dash", OwnerPrincipalID: "owner", Lifecycle: "draft"}}}}}
	inventory, err := New(transfer)
	if err != nil {
		t.Fatal(err)
	}
	bound := inventory.WithOwnershipMutationDB(nil)
	boundAuthority := bound.(*Inventory).authorities[0].(*mutatingAuthority)
	if !boundAuthority.bound {
		t.Fatal("mutation authority was not rebound")
	}
	if _, err := bound.TransferOwnedObjects(context.Background(), "owner", "target"); err != nil {
		t.Fatal(err)
	}
	if !boundAuthority.transferred {
		t.Fatal("transfer was not delegated")
	}
	if _, err := bound.TombstoneOwnedObjects(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	if !boundAuthority.tombstoned {
		t.Fatal("tombstone was not delegated")
	}
}

func TestInventoryFailsClosedWhenLiveAuthorityCannotResolve(t *testing.T) {
	inventory, err := New(&testAuthority{report: access.OwnershipReport{PrincipalID: "owner", Objects: []access.OwnedObject{{Kind: "semantic_attribute", ID: "attribute", OwnerPrincipalID: "owner", Lifecycle: "active"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inventory.TombstoneOwnedObjects(context.Background(), "owner"); err == nil {
		t.Fatal("unsupported live ownership was silently skipped")
	}
}
