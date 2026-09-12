package module

import (
	"context"
	"testing"
	"time"

	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestPublishSemanticModelRefreshUsesPrivateAndPublicationScopedBrokers(t *testing.T) {
	privateBroker := dashboardstream.NewDeliveryBroker()
	publicBroker := dashboardstream.NewDeliveryBroker()
	registry := dashboardstream.NewRegistry()
	defer registry.Close()
	ctx := context.Background()
	registry.Ensure("private-stream", ctx, func(dashboardstream.RefreshEvent) {})
	registry.Ensure("public-stream", ctx, func(dashboardstream.RefreshEvent) {})
	registry.Bind("private-stream", projectgraph.ResourceID("project"), "prod", "orders", func() {})
	registry.BindForPublication("public-stream", projectgraph.ResourceID("project"), "prod", "orders", "publication-1", func() {})

	privateUpdates, stopPrivate := privateBroker.Subscribe("private-stream")
	defer stopPrivate()
	publicUpdates, stopPublic := publicBroker.SubscribeForPublication("publication-1", "public-stream")
	defer stopPublic()
	crossUpdates, stopCross := publicBroker.SubscribeForPublication("publication-2", "public-stream")
	defer stopCross()

	mod := &Module{
		coordinators: registry,
		handler:      dashboardhttp.Handler{Broker: privateBroker},
		publicBroker: publicBroker,
	}
	mod.PublishSemanticModelRefresh(projectgraph.ResourceID("project"), "prod", "orders", "2026-09-11T00:00:00Z")

	select {
	case patch := <-privateUpdates:
		if patch["status"].(map[string]any)["lastUpdated"] != "2026-09-11T00:00:00Z" {
			t.Fatalf("private status = %#v", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("private broker did not receive refresh status")
	}
	select {
	case patch := <-publicUpdates:
		if patch["status"].(map[string]any)["lastUpdated"] != "2026-09-11T00:00:00Z" {
			t.Fatalf("public status = %#v", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("matching publication broker did not receive refresh status")
	}
	select {
	case patch := <-crossUpdates:
		t.Fatalf("cross-publication status = %#v", patch)
	case <-time.After(100 * time.Millisecond):
	}
}
