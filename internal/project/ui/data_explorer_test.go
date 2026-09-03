package ui

import (
	"net/url"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerBootstrapProjectsAgentExplorationContext(t *testing.T) {
	explorer := uisignals.DataExplorerSignal{Explore: uisignals.DataExploreSignal{Command: uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "commerce", DatasetID: uisignals.Pointer("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}}}}
	page := uisignals.DataExplorerPageSignal{Context: uisignals.DataExplorerContextSignal{Active: true, Environment: "production", GenerationID: "generation-1", ProjectID: "sales"}}
	signals := DataExplorerBootstrapSignalsWithAgent(catalogFixture(), page, explorer, DataExplorerAgentBootstrap{})
	context, ok := signals["agentContext"].(uisignals.AgentContextSignal)
	if !ok {
		t.Fatalf("agent context = %#v", signals["agentContext"])
	}
	if context.Surface != "data" || context.ModelID != "commerce" || uisignals.ValueOrZero(context.DatasetID) != "orders" {
		t.Fatalf("agent context = %#v", context)
	}
	if context.Exploration == nil || len(context.Exploration.Dimensions) != 1 || context.Exploration.Metrics[0].Field != "order_count" {
		t.Fatalf("agent exploration = %#v", context.Exploration)
	}
	if signals["agent"] == nil || signals["agentVisuals"] == nil {
		t.Fatalf("agent bootstrap = %#v", signals)
	}
}

func TestDataExplorerUpdatesURLPreservesDurableExplorationState(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), RequestSeq: 80, ResetVersion: 9, Explore: &uisignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: uisignals.Pointer("orders"),
			Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.month"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}},
			Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{{Field: "revenue", Direction: "desc"}},
			Time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: "month"}, Limit: 250},
		RequestSeq: 81, ResetVersion: 10,
	}}
	updates, err := url.Parse(dataExplorerUpdatesURL(command))
	if err != nil {
		t.Fatal(err)
	}
	values := updates.Query()
	if values.Get("route") != "data" || values.Get("surface") != "explore" || values.Get("mode") != "explore" || values.Get("v") != "2" || values.Get("state") == "" {
		t.Fatalf("routing values = %#v", values)
	}
	if values.Get("state") != `{"datasetId":"orders","dimensions":[{"field":"orders.month"}],"filters":[],"limit":250,"metrics":[{"field":"revenue"}],"modelId":"semantic:sales","schemaVersion":1,"sort":[{"direction":"desc","field":"revenue"}],"time":{"field":"orders.created_at","grain":"month"}}` {
		t.Fatalf("state is not canonical JSON: %s", values.Get("state"))
	}
	if values.Has("dimension") || values.Has("metric") || values.Has("limit") {
		t.Fatalf("legacy exploration values = %#v", values)
	}
	if values.Has("requestSeq") || values.Has("resetVersion") {
		t.Fatalf("runtime state leaked into updates URL: %#v", values)
	}
}

func catalogFixture() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: "sales", Title: "Sales"}}
}
