package ui

import (
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

func catalogFixture() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: "sales", Title: "Sales"}}
}
