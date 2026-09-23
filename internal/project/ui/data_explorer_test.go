package ui

import (
<<<<<<< HEAD
	"encoding/json"
=======
	"bytes"
>>>>>>> 35d780967 (Implement versioned saved explorations)
	"net/url"
	"strings"
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
	saved, ok := signals["savedExplorations"].(uisignals.SavedExplorationStateSignal)
	if !ok || saved.Enabled || saved.Command.Action != "create" || saved.Save.State != "saved" || saved.List.Items == nil {
		t.Fatalf("legacy saved-exploration bootstrap = %#v, want disabled valid default", signals["savedExplorations"])
	}
}

func TestDataExplorerUpdatesURLPreservesDurableExplorationState(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), RequestSeq: 80, ResetVersion: 9, Explore: &uisignals.DataExploreCommand{
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

func TestDataExplorerUpdatesURLPreservesSavedSelection(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), Explore: &uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: uisignals.Pointer("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}}}
	updates, err := url.Parse(dataExplorerUpdatesURL(command, "exploration:orders"))
	if err != nil {
		t.Fatal(err)
	}
	if got := updates.Query().Get("saved"); got != "exploration:orders" {
		t.Fatalf("saved selection = %q, want selected deep-link ID", got)
	}
}

func TestDataExplorerUpdatesURLIncludesArchivedOnlyForArchivedSelection(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), Explore: &uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: uisignals.Pointer("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}}}
	updates, err := url.Parse(dataExplorerUpdatesURLWithOptions(command, "exploration:archived", true))
	if err != nil {
		t.Fatal(err)
	}
	if got := updates.Query().Get("includeArchived"); got != "true" {
		t.Fatalf("includeArchived = %q, want true", got)
	}
	active, err := url.Parse(dataExplorerUpdatesURLWithOptions(command, "exploration:active", false))
	if err != nil {
		t.Fatal(err)
	}
	if active.Query().Has("includeArchived") {
		t.Fatalf("active selection unexpectedly includes archived list flag: %s", active)
	}
}

func TestDataExplorerPageRendersSavedSelectionInInitialUpdatesStream(t *testing.T) {
	selectedID := "exploration:orders"
	explorer := uisignals.DataExplorerSignal{Command: uisignals.DataExplorerCommand{
		Mode: uisignals.Pointer("explore"),
		Explore: &uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
			SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: uisignals.Pointer("orders"),
			Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
			Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		}},
	}}
	saved := DataExplorerSavedExplorationBootstrap{
		Enabled: true,
		State: uisignals.SavedExplorationStateSignal{
			List:    uisignals.SavedExplorationListSignal{Items: []uisignals.SavedExplorationListItemSignal{}, SelectedID: &selectedID},
			Command: uisignals.SavedExplorationCommandSignal{Action: "reopen"},
			Save:    uisignals.SavedExplorationSaveStateSignal{State: "saved"},
		},
	}
	var rendered bytes.Buffer
	if err := DataExplorerPageWithSavedExplorations(catalogFixture(), uisignals.DataExplorerPageSignal{}, explorer, saved, "", testLayoutProvider()).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), "/updates?mode=explore&amp;route=data&amp;saved=exploration%3Aorders&amp;state=") {
		t.Fatalf("initial updates stream omitted selected saved ID:\n%s", rendered.String())
	}
}

func TestDataExplorerPageRendersArchivedSelectionInInitialUpdatesStream(t *testing.T) {
	selectedID := "exploration:archived"
	explorer := uisignals.DataExplorerSignal{Command: uisignals.DataExplorerCommand{
		Mode: uisignals.Pointer("explore"), Explore: &uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
			SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: uisignals.Pointer("orders"),
			Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
			Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
		}},
	}}
	saved := DataExplorerSavedExplorationBootstrap{Enabled: true, State: uisignals.SavedExplorationStateSignal{
		List:    uisignals.SavedExplorationListSignal{Items: []uisignals.SavedExplorationListItemSignal{{ID: selectedID, Status: "archived"}}, SelectedID: &selectedID},
		Command: uisignals.SavedExplorationCommandSignal{Action: "reopen"}, Save: uisignals.SavedExplorationSaveStateSignal{State: "saved"},
	}}
	var rendered bytes.Buffer
	if err := DataExplorerPageWithSavedExplorations(catalogFixture(), uisignals.DataExplorerPageSignal{}, explorer, saved, "", testLayoutProvider()).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), "includeArchived=true") {
		t.Fatalf("initial updates stream omitted archived selection flag:\n%s", rendered.String())
	}
}

func catalogFixture() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: "sales", Title: "Sales"}}
}
