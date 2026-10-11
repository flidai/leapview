package ui

import (
	"encoding/json"
	"net/url"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
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
	rawContext, err := json.Marshal(signals["agentContext"])
	if err != nil {
		t.Fatalf("marshal agent context: %v", err)
	}
	var context uisignals.AgentContextSignal
	if err := json.Unmarshal(rawContext, &context); err != nil {
		t.Fatalf("decode agent context: %v", err)
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

func TestDataExplorerCombinedBootstrapPreservesAgentAndSavedState(t *testing.T) {
	agent := DataExplorerAgentBootstrap{Agent: map[string]any{"status": map[string]any{"enabled": true}, "composer": map[string]any{"disabled": false}}, Visuals: map[string]any{"own-visual": true}}
	saved := DataExplorerSavedExplorationBootstrap{Enabled: true, State: DefaultDataExplorerSavedExplorationState(true)}
	signals := DataExplorerBootstrapSignalsWithAgentAndSavedExplorations(catalogFixture(), uisignals.DataExplorerPageSignal{}, uisignals.DataExplorerSignal{}, agent, saved)
	raw, err := json.Marshal(signals)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	state := wire["agent"].(map[string]any)
	if state["status"].(map[string]any)["enabled"] != true || state["composer"].(map[string]any)["disabled"] != false || wire["agentVisuals"].(map[string]any)["own-visual"] != true {
		t.Fatalf("agent bootstrap lost: %s", raw)
	}
	if wire["savedExplorations"].(map[string]any)["enabled"] != true || wire["agentContext"].(map[string]any)["surface"] != "data" {
		t.Fatalf("saved/exploration context lost: %s", raw)
	}
	fallback := DataExplorerBootstrapSignalsWithAgentAndSavedExplorations(catalogFixture(), uisignals.DataExplorerPageSignal{}, uisignals.DataExplorerSignal{}, DataExplorerAgentBootstrap{}, saved)
	if !fallback["agent"].(uisignals.ChatSignal).Composer.Disabled {
		t.Fatalf("missing agent must remain unavailable: %#v", fallback["agent"])
	}
}

func TestDataExplorerAgentContextRequiresGovernedSelection(t *testing.T) {
	for _, scenario := range []struct {
		name, mode, modelID, layer string
		available                  bool
	}{
		{"initial auto-selected semantic model", "browse", "semantic:sales", "", true},
		{"Rows governed dataset", "browse", "semantic:sales", "model", true},
		{"Analyze governed dataset", "explore", "semantic:sales", "model", true},
		{"empty semantic catalog", "browse", "", "", false},
		{"Rows raw source with previous semantic selection", "browse", "semantic:unrelated", "source", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: scenario.modelID, Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
			if scenario.modelID != "" {
				spec.DatasetID = uisignals.Pointer("orders")
			}
			explorer := uisignals.DataExplorerSignal{Command: uisignals.DataExplorerCommand{Mode: uisignals.Pointer(scenario.mode)}, Explore: uisignals.DataExploreSignal{Command: uisignals.DataExploreCommand{Spec: spec}}}
			if scenario.layer != "" {
				explorer.SelectedObject = &uisignals.DataExplorerObjectSignal{Layer: scenario.layer}
			}
			context := DataExplorerAgentContext(uisignals.DataExplorerPageSignal{}, explorer)
			if context.Surface != "data" {
				t.Fatalf("Explorer surface changed: %q", context.Surface)
			}
			if scenario.available {
				if context.Exploration == nil || context.ModelID != scenario.modelID || uisignals.ValueOrZero(context.DatasetID) != "orders" {
					t.Fatalf("governed data context lost: %#v", context)
				}
				if err := exploration.ValidateShape(context.Exploration); err != nil {
					t.Fatalf("projected context rejected by data-turn shape validation: %v", err)
				}
			} else if context.Exploration != nil || context.ModelID != "" || context.DatasetID != nil {
				t.Fatalf("unavailable data context retained unrelated/invalid semantic selection: %#v", context)
			}
			encoded, err := json.Marshal(DataExplorerAgentContextPayload(context))
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if !scenario.available {
				for _, key := range []string{"exploration", "datasetId"} {
					if value, present := wire[key]; !present || value != nil {
						t.Fatalf("context %s must explicitly clear stale browser value: %s", key, encoded)
					}
				}
			}
			provider := map[string]any{"status": map[string]any{"enabled": true}}
			bootstrap := DataExplorerBootstrapSignalsWithAgent(catalogFixture(), uisignals.DataExplorerPageSignal{}, explorer, DataExplorerAgentBootstrap{Agent: provider})
			if bootstrap["agent"].(map[string]any)["status"].(map[string]any)["enabled"] != true {
				t.Fatal("missing governed context must not disable provider availability")
			}
		})
	}
}
