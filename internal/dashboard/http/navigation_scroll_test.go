package http

import (
	"context"
	"errors"
	"testing"
	"time"

	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	"github.com/flidai/leapview/pkg/pagestream"
)

func TestCommandPageIsActiveRejectsWindowFromPageLeftDuringNavigation(t *testing.T) {
	store := dashboardsession.NewMemoryStore()
	key := dashboardsession.Key{
		ProjectID: "workspace", PrincipalOrClient: "client", DashboardID: "dash",
		ServingStateID: "serving", StreamInstanceID: "stream",
	}
	state := dashboardsession.NewState("overview", dashboardfilter.NewMachine(
		dashboardfilter.ApplicationImmediate, nil,
	).Snapshot())
	if _, err := store.Create(context.Background(), key, state); err != nil {
		t.Fatal(err)
	}
	active, err := commandPageIsActive(context.Background(), store, key, "overview")
	if err != nil || !active {
		t.Fatal("active destination page was rejected")
	}
	active, err = commandPageIsActive(context.Background(), store, key, "tables")
	if err != nil || active {
		t.Fatal("window request from the page left during navigation was accepted")
	}
}

func TestCommandPageIsActivePreservesSessionLoadErrors(t *testing.T) {
	want := errors.New("session database unavailable")
	store := failingDashboardSessionStore{Store: dashboardsession.NewMemoryStore(), err: want}
	key := dashboardsession.Key{
		ProjectID: "workspace", PrincipalOrClient: "client", DashboardID: "dash",
		ServingStateID: "serving", StreamInstanceID: "stream",
	}
	active, err := commandPageIsActive(context.Background(), store, key, "overview")
	if active || !errors.Is(err, want) {
		t.Fatalf("active=%t error=%v, want false and %v", active, err, want)
	}
}

func TestNavigationMutationStaysCurrentAcrossUnrelatedGenerationChanges(t *testing.T) {
	state := dashboardsession.State{
		ActivePage:            "details",
		StreamGeneration:      7,
		NavigationMutationIDs: []string{"nav-1"},
	}
	record := dashboardsession.Record{State: state}
	if generation, current := navigationRecordGeneration(record, "details", "nav-1"); !current || generation != 7 {
		t.Fatalf("navigation generation = %d/%t, want 7/true after an unrelated session generation change", generation, current)
	}
	state.NavigationMutationIDs = append(state.NavigationMutationIDs, "nav-2")
	if generation, current := navigationRecordGeneration(dashboardsession.Record{State: state}, "details", "nav-1"); current || generation != 0 {
		t.Fatal("superseded navigation was accepted after a later navigation")
	}
}

type failingDashboardSessionStore struct {
	dashboardsession.Store
	err error
}

func (store failingDashboardSessionStore) Load(context.Context, dashboardsession.Key) (dashboardsession.Record, error) {
	return dashboardsession.Record{}, store.err
}

func TestNavigationPatchSurvivesNewerTableWindowGeneration(t *testing.T) {
	broker := dashboardstream.NewDeliveryBrokerWithPendingLimit(4)
	updates, unsubscribe := broker.Subscribe("client:page")
	defer unsubscribe()

	broker.PublishEnvelope("client:page", dashboardstream.Envelope{
		Signals: pagestream.SignalPatch{"window": "rows"},
		Delivery: dashboardstream.DeliveryMetadata{
			Generation: 20,
			Boundary:   true,
		},
	})
	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("window generation was not delivered")
	}

	broker.PublishEnvelope("client:page", navigationPatchEnvelope(
		pagestream.SignalPatch{"page": map[string]any{"pageId": "overview"}},
		21,
	))
	select {
	case patch := <-updates:
		page, ok := patch["page"].(map[string]any)
		if !ok || page["pageId"] != "overview" {
			t.Fatalf("navigation patch = %#v", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("navigation patch was dropped behind table window generations")
	}
}
