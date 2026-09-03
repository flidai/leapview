package ui

import (
	"net/url"
	"reflect"
	"testing"

	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerBootstrapProjectsAgentExplorationContext(t *testing.T) {
	explorer := uisignals.DataExplorerSignal{Explore: uisignals.DataExploreSignal{Command: uisignals.DataExploreCommand{
		ModelID: uisignals.Pointer("commerce"), DatasetID: uisignals.Pointer("orders"),
		Dimensions: []string{"orders.status"}, Metrics: []string{"order_count"},
		Filters: []uisignals.DataExploreFilterSignal{}, Sort: []uisignals.DataExploreSortSignal{}, Limit: 100,
	}}}
	page := uisignals.DataExplorerPageSignal{Context: uisignals.DataExplorerContextSignal{Active: true, Environment: "production", GenerationID: "generation-1", ProjectID: "sales"}}
	signals := DataExplorerBootstrapSignalsWithAgent(catalogFixture(), page, explorer, DataExplorerAgentBootstrap{})
	context, ok := signals["agentContext"].(uisignals.AgentContextSignal)
	if !ok {
		t.Fatalf("agent context = %#v", signals["agentContext"])
	}
	if context.Surface != "data" || context.ModelID != "commerce" || uisignals.ValueOrZero(context.DatasetID) != "orders" {
		t.Fatalf("agent context = %#v", context)
	}
	if context.Exploration == nil || len(context.Exploration.Dimensions) != 1 || context.Exploration.Metrics[0] != "order_count" {
		t.Fatalf("agent exploration = %#v", context.Exploration)
	}
	if signals["agent"] == nil || signals["agentVisuals"] == nil {
		t.Fatalf("agent bootstrap = %#v", signals)
	}
}

func TestDataExplorerUpdatesURLPreservesDurableExplorationState(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), RequestSeq: 80, ResetVersion: 9, Explore: &uisignals.DataExploreCommand{
		ModelID: uisignals.Pointer("semantic:sales"), DatasetID: uisignals.Pointer("orders"),
		Dimensions: []string{"orders.month"}, Metrics: []string{"revenue"},
		Filters: []uisignals.DataExploreFilterSignal{{Field: "orders.state", Operator: "equals", Values: []string{"paid"}}},
		Sort:    []uisignals.DataExploreSortSignal{{Field: "revenue", Direction: "desc"}},
		Time:    &uisignals.DataExploreTimeSignal{Field: "orders.created_at", Grain: "month"}, Limit: 250,
		RequestSeq: 81, ResetVersion: 10,
	}}
	updates, err := url.Parse(dataExplorerUpdatesURL(command))
	if err != nil {
		t.Fatal(err)
	}
	values := updates.Query()
	if values.Get("route") != "data" || values.Get("surface") != "explore" || values.Get("mode") != "explore" || values.Get("v") != "1" {
		t.Fatalf("routing values = %#v", values)
	}
	if !reflect.DeepEqual(values["dimension"], []string{"orders.month"}) || !reflect.DeepEqual(values["metric"], []string{"revenue"}) || values.Get("limit") != "250" {
		t.Fatalf("exploration values = %#v", values)
	}
	if values.Has("requestSeq") || values.Has("resetVersion") {
		t.Fatalf("runtime state leaked into updates URL: %#v", values)
	}
}

func catalogFixture() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: "sales", Title: "Sales"}}
}
