package ui

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	catalog "github.com/flidai/leapview/internal/project/navigation"
	uisignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerDashboardBootstrapCarriesTypedTargetAndCommandBridge(t *testing.T) {
	page := uisignals.DataExplorerPageSignal{Title: "Data Explorer"}
	explorer := uisignals.DataExplorerSignal{Command: uisignals.DataExplorerCommand{}}
	bootstrap := DataExplorerDashboardBootstrap{
		Enabled: true, Targets: []uisignals.DataExplorerDashboardTargetSignal{{ID: "dashboard:sales", Title: "Sales", SemanticModelID: "semantic:sales", DraftID: uisignals.Pointer("draft:sales"), RevisionToken: uisignals.Pointer("opaque-token"), Pages: uisignals.Pointer([]uisignals.DataExplorerDashboardPageSignal{{ID: "overview", Title: "Overview", Placement: uisignals.DashboardPagePlacement{Col: 1, ColSpan: 6, Row: 1, RowSpan: 4}}})}},
		Command: dashboardgen.GenUIActionExecuteDashboardAuthoringCommand(), Path: "/explore/add-to-dashboard",
	}
	signals := DataExplorerBootstrapSignalsWithSavedExplorationsAndDashboard(catalogFixture(), page, explorer, DataExplorerSavedExplorationBootstrap{State: DefaultDataExplorerSavedExplorationState(true)}, bootstrap)
	value, ok := signals["dataExplorerDashboard"].(uisignals.DataExplorerDashboardSignal)
	if !ok || !value.Enabled {
		t.Fatalf("dashboard bootstrap = %#v", signals["dataExplorerDashboard"])
	}
	if len(value.Targets) != 1 || value.Targets[0].ID != "dashboard:sales" || value.Targets[0].RevisionToken == nil || *value.Targets[0].RevisionToken != "opaque-token" {
		t.Fatalf("dashboard targets = %#v", value.Targets)
	}
	var rendered bytes.Buffer
	if err := DataExplorerPageWithSavedExplorationsAndDashboard(catalogFixture(), page, explorer, DataExplorerSavedExplorationBootstrap{State: DefaultDataExplorerSavedExplorationState(true)}, bootstrap, "", testLayoutProvider()).Render(&rendered); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lv-data-explorer-add-to-dashboard", "/explore/add-to-dashboard", bootstrap.Command.OperationID()} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("rendered dashboard action missing %q: %s", want, rendered.String())
		}
	}
	if !strings.Contains(rendered.String(), `data-lv-dashboard-action="append"`) || !strings.Contains(rendered.String(), `data-on:lv-data-explorer-add-to-dashboard__document`) {
		t.Fatalf("dashboard mutation did not get an isolated document action owner: %s", rendered.String())
	}
	if !strings.Contains(rendered.String(), "requestCancellation: window.LeapViewDataExplorerTransport.requestCancellation(evt.detail)") {
		t.Fatalf("data explorer command did not use scoped browser cancellation: %s", rendered.String())
	}
	if !strings.Contains(rendered.String(), "$addExplorationToDashboard = evt.detail") || !strings.Contains(rendered.String(), "filterSignals: {include: /^(?:addExplorationToDashboard)") {
		t.Fatalf("dashboard mutation did not use the generated envelope key: %s", rendered.String())
	}
	if strings.Contains(rendered.String(), "dataExplorerAddToDashboard") {
		t.Fatalf("dashboard mutation used an unrecognized signal key: %s", rendered.String())
	}
	if strings.Contains(rendered.String(), `data-on:lv-data-explorer-add-to-dashboard="`) {
		t.Fatalf("dashboard mutation remained attached to the query host: %s", rendered.String())
	}
}

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
	saved, ok := signals["savedExplorations"].(uisignals.SavedExplorationStateSignal)
	if !ok || saved.Enabled || saved.Command.Action != "create" || saved.Save.State != "saved" || saved.List.Items == nil {
		t.Fatalf("legacy saved-exploration bootstrap = %#v, want disabled valid default", signals["savedExplorations"])
	}
}

func TestDataExplorerAgentContextOmitsAbsentDataset(t *testing.T) {
	explorer := uisignals.DataExplorerSignal{Explore: uisignals.DataExploreSignal{Command: uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "commerce",
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}}}}

	context := DataExplorerAgentContext(uisignals.DataExplorerPageSignal{}, explorer)
	if context.DatasetID != nil {
		t.Fatalf("agent context dataset id = %q, want absent", *context.DatasetID)
	}
}

func TestDataExplorerAgentContextOmitsUnselectedExploration(t *testing.T) {
	explorer := uisignals.DataExplorerSignal{Explore: uisignals.DataExploreSignal{Command: uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1,
		Dimensions:    []exploration.ExplorationDimensionRef{},
		Metrics:       []exploration.ExplorationMetricRef{},
		Filters:       []exploration.ExplorationFilter{},
		Sort:          []exploration.ExplorationSort{},
		Limit:         100,
	}}}}

	context := DataExplorerAgentContext(uisignals.DataExplorerPageSignal{}, explorer)
	if context.ModelID != "" {
		t.Fatalf("agent context model id = %q, want empty", context.ModelID)
	}
	if context.Exploration != nil {
		t.Fatalf("agent context exploration = %#v, want absent until a model is selected", context.Exploration)
	}
}

func TestDataExplorerUpdatesURLOmitsUnselectedExplorationState(t *testing.T) {
	command := uisignals.DataExplorerCommand{Mode: uisignals.Pointer("explore"), Explore: &uisignals.DataExploreCommand{Spec: exploration.ExplorationSpec{
		SchemaVersion: 1,
		Dimensions:    []exploration.ExplorationDimensionRef{},
		Metrics:       []exploration.ExplorationMetricRef{},
		Filters:       []exploration.ExplorationFilter{},
		Sort:          []exploration.ExplorationSort{},
		Limit:         100,
	}}}
	updates, err := url.Parse(dataExplorerUpdatesURL(command))
	if err != nil {
		t.Fatal(err)
	}
	values := updates.Query()
	if values.Get("route") != "data" || values.Get("surface") != "explore" || values.Get("mode") != "explore" {
		t.Fatalf("routing values = %#v, want explore mode initialization", values)
	}
	if values.Has("v") || values.Has("state") {
		t.Fatalf("unselected exploration emitted canonical URL state: %#v", values)
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
	if values.Has("semanticModel") || values.Has("model") {
		t.Fatalf("legacy semantic model values = %#v", values)
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
