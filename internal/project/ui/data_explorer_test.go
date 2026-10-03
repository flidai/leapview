package ui

import (
	"encoding/json"
	"net/url"
	"testing"

	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerBootstrapProjectsAgentExplorationContext(t *testing.T) {
	explorer := uisignals.DataExplorerSignal{Explore: uisignals.DataExploreSignal{Command: uisignals.DataExploreCommand{
		SemanticModelID: uisignals.Pointer("commerce"), DatasetID: uisignals.Pointer("orders"),
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
	if context.Exploration == nil || len(context.Exploration.Dimensions) != 1 || context.Exploration.Metrics[0].Field != "order_count" {
		t.Fatalf("agent exploration = %#v", context.Exploration)
	}
	if signals["agent"] == nil || signals["agentVisuals"] == nil {
		t.Fatalf("agent bootstrap = %#v", signals)
	}
}

func TestDataExplorerUpdatesURLPreservesDurableExplorationState(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), ClientID: uisignals.Optional("explorer-tab-1"), RequestSeq: 80, ResetVersion: 9, Explore: &uisignals.DataExploreCommand{
		SemanticModelID: uisignals.Pointer("semantic:sales"), DatasetID: uisignals.Pointer("orders"),
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
	if values.Get("route") != "data" || values.Get("surface") != "explore" || values.Get("mode") != "explore" || values.Get("v") != "2" {
		t.Fatalf("routing values = %#v", values)
	}
	if values.Get("clientId") != "explorer-tab-1" {
		t.Fatalf("updates client identity = %q, want %q", values.Get("clientId"), "explorer-tab-1")
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(values.Get("state")), &spec); err != nil {
		t.Fatalf("state = %q: %v", values.Get("state"), err)
	}
	if spec["modelId"] != "semantic:sales" || spec["limit"] != float64(250) || values.Has("semanticModel") || values.Has("dimension") {
		t.Fatalf("canonical exploration values = %#v / %#v", values, spec)
	}
	if values.Has("requestSeq") || values.Has("resetVersion") {
		t.Fatalf("runtime state leaked into updates URL: %#v", values)
	}
}

func TestDataExplorerUpdatesURLPreservesBrowseFilters(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("browse"), ObjectKey: uisignals.Pointer("model:zip"), Explore: &uisignals.DataExploreCommand{
		SemanticModelID: uisignals.Pointer("semantic-model:visuals"), DatasetID: uisignals.Pointer("zip_geolocations"),
		Dimensions: []string{}, Metrics: []string{}, Filters: []uisignals.DataExploreFilterSignal{{Field: "zip_geolocations.state", Operator: "equals", Values: []string{"SP"}}},
		Sort: []uisignals.DataExploreSortSignal{}, Limit: 100,
	}}
	values, err := url.Parse(dataExplorerUpdatesURL(command))
	if err != nil {
		t.Fatal(err)
	}
	query := values.Query()
	if query.Get("object") != "model:zip" || query.Get("mode") != "browse" || query.Get("v") != "2" {
		t.Fatalf("browse update URL = %#v", query)
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(query.Get("state")), &spec); err != nil {
		t.Fatal(err)
	}
	filters, ok := spec["filters"].([]any)
	if !ok || len(filters) != 1 {
		t.Fatalf("browse filters lost from update URL: %#v", spec)
	}
}

func catalogFixture() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: "sales", Title: "Sales"}}
}
