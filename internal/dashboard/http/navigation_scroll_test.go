package http

import (
	"context"
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
	if !commandPageIsActive(context.Background(), store, key, "overview") {
		t.Fatal("active destination page was rejected")
	}
	if commandPageIsActive(context.Background(), store, key, "tables") {
		t.Fatal("window request from the page left during navigation was accepted")
	}
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
