package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/session"
	"github.com/flidai/leapview/internal/platform"
)

func TestStoreSharesCASStateAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	platformStore, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = platformStore.Close() })
	first, second := NewStore(platformStore.SQLDB()), NewStore(platformStore.SQLDB())
	key := session.Key{
		ProjectID: "sales", PrincipalOrClient: "reader",
		DashboardID: "executive", ServingStateID: "ss-1", StreamInstanceID: "tab",
	}
	state := session.NewState("overview", filter.NewMachine(filter.ApplicationImmediate, map[string]filter.BindingSpec{}).Snapshot())
	record, err := first.Create(ctx, key, state)
	if err != nil {
		t.Fatal(err)
	}
	next := record.State
	next.ActivePage = "details"
	if _, err := second.CompareAndSwap(ctx, key, record.Version, next); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CompareAndSwap(ctx, key, record.Version, state); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("stale CAS error = %v, want conflict", err)
	}
	loaded, err := first.Load(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.ActivePage != "details" || loaded.Version != 2 {
		t.Fatalf("loaded record = %#v", loaded)
	}
}
