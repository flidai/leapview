package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/filter"
)

func TestKeyRequiresExactlyOneProjectOrPublicationScope(t *testing.T) {
	base := Key{PrincipalOrClient: "user-1", DashboardID: "executive", ServingStateID: "ss-1", StreamInstanceID: "tab-1"}
	if err := base.Validate(); err == nil {
		t.Fatal("unscoped session key validation = nil")
	}
	projectAndPublication := base
	projectAndPublication.ProjectID = "sales"
	projectAndPublication.PublicationID = "pub-1"
	if err := projectAndPublication.Validate(); err == nil {
		t.Fatal("project/publication session key validation = nil")
	}
}

func TestMemoryStoreCompareAndSwapAllowsOneConcurrentWriter(t *testing.T) {
	store := NewMemoryStore()
	key := Key{
		ProjectID: "sales", PrincipalOrClient: "user-1",
		DashboardID: "executive", ServingStateID: "ss-1", StreamInstanceID: "tab-1",
	}
	created, err := store.Create(context.Background(), key, State{ActivePage: "overview", StreamGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			<-start
			next := created.State
			next.StreamGeneration++
			_, err := store.CompareAndSwap(context.Background(), key, created.Version, next)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected CAS error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
}

func TestServiceNavigateUsesFilterRevisionAndIdempotentCAS(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	key := Key{
		ProjectID: "sales", PrincipalOrClient: "user-1",
		DashboardID: "executive", ServingStateID: "ss-1", StreamInstanceID: "tab-1",
	}
	machine := filter.NewMachine(filter.ApplicationImmediate, map[string]filter.BindingSpec{})
	if _, err := store.Create(ctx, key, NewState("overview", machine.Snapshot())); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: store}
	command := NavigationCommand{
		PageID: "details", BaseFilterRevision: machine.State().Revision, ClientMutationID: "nav-1",
	}
	first, err := service.Navigate(ctx, key, command)
	if err != nil {
		t.Fatal(err)
	}
	if first.ActivePage != "details" || first.StreamGeneration != 2 || first.Duplicate {
		t.Fatalf("first navigation = %#v", first)
	}
	second, err := service.Navigate(ctx, key, command)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.ActivePage != "details" || second.StreamGeneration != 2 {
		t.Fatalf("duplicate navigation = %#v", second)
	}
	stale := command
	stale.ClientMutationID = "nav-2"
	stale.BaseFilterRevision = 0
	if _, err := service.Navigate(ctx, key, stale); !errors.Is(err, filter.ErrStaleRevision) {
		t.Fatalf("stale navigation error = %v", err)
	}
}

func TestServiceUpdateSelectionsPreservesFilterRevision(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	key := Key{
		ProjectID: "sales", PrincipalOrClient: "user-1",
		DashboardID: "executive", ServingStateID: "ss-1", StreamInstanceID: "tab-1",
	}
	machine := filter.NewMachine(filter.ApplicationImmediate, map[string]filter.BindingSpec{})
	if _, err := store.Create(ctx, key, NewState("overview", machine.Snapshot())); err != nil {
		t.Fatal(err)
	}
	state, err := (Service{Store: store}).UpdateSelections(
		ctx, key,
		[]map[string]any{{"sourceId": "orders"}},
		[]map[string]any{{"visualID": "map"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Filters.State.Revision != machine.State().Revision || state.StreamGeneration != 2 {
		t.Fatalf("updated state = %#v", state)
	}
	if len(state.InteractionSelections) != 1 || len(state.SpatialSelections) != 1 {
		t.Fatalf("selection state = %#v", state)
	}
}

func TestServicePersistsFilterIdempotencyAndKeepsIndependentSelectionRoots(t *testing.T) {
	specs := map[string]filter.BindingSpec{
		"fb_state": {
			ValueKind: filter.ValueString, Editable: true,
			Default: filter.Expression{Kind: filter.ExpressionUnfiltered},
		},
	}
	service := Service{
		Store: NewMemoryStore(), ApplicationMode: filter.ApplicationImmediate,
		Bindings: specs,
	}
	key := Key{
		ProjectID: "sales", PrincipalOrClient: "user-1",
		DashboardID: "executive", ServingStateID: "ss-1", StreamInstanceID: "tab-1",
	}
	initial := NewState("overview", filter.NewMachine(filter.ApplicationImmediate, specs).Snapshot())
	initial.InteractionSelections = []map[string]any{{"sourceId": "chart"}}
	initial.SpatialSelections = []map[string]any{{"visualId": "map"}}
	if _, err := service.Store.Create(context.Background(), key, initial); err != nil {
		t.Fatal(err)
	}
	command := filter.Command{
		Kind: filter.CommandMutate, BaseRevision: 1, ClientMutationID: "m-1",
		BindingKey: "fb_state", Operation: filter.MutationClear,
	}
	first, err := service.ExecuteFilterCommand(context.Background(), key, command)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := service.ExecuteFilterCommand(context.Background(), key, command)
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate || retry.FilterState.Revision != first.FilterState.Revision {
		t.Fatalf("retry=%#v first=%#v", retry, first)
	}
	record, err := service.Store.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.State.InteractionSelections) != 1 || len(record.State.SpatialSelections) != 1 {
		t.Fatalf("unrelated roots changed: %#v", record.State)
	}
}
