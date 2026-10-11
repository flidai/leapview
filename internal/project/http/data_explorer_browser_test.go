package http

import (
	"context"
	"errors"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	projectui "github.com/flidai/leapview/internal/project/ui"
	"github.com/flidai/leapview/pkg/pagestream"
	stdhtml "html"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

func TestDataExplorerSemanticDatasetDeepLinksHydrateDistinctModelBindings(t *testing.T) {
	h, executor := dataExplorerAgentBrowserFixture(t)
	const semanticModelID = "semantic:sales"
	const modelID = "model:orders"

	for _, datasetID := range []string{"orders", "order_history"} {
		query := url.Values{
			"v":             {"1"},
			"mode":          {"explore"},
			"semanticModel": {semanticModelID},
			"dataset":       {datasetID},
			"dimension":     {datasetID + ".status"},
		}
		recorder := httptest.NewRecorder()
		_, explorer, ok := h.dataExplorerSignals(recorder, httptest.NewRequest(stdhttp.MethodGet, "/explore?"+query.Encode(), nil))
		if !ok {
			t.Fatalf("dataset %q deep link failed: status=%d body=%s", datasetID, recorder.Code, recorder.Body.String())
		}
		if len(explorer.Objects) != 2 {
			t.Fatalf("dataset %q objects = %#v, want both alias bindings", datasetID, explorer.Objects)
		}
		if explorer.SelectedObject == nil {
			t.Fatalf("dataset %q selected object is nil", datasetID)
		}
		selected := explorer.SelectedObject
		wantKey := explorerModelObjectKey(modelID, semanticModelID, datasetID)
		if selected.Key != wantKey || projectsignals.ValueOrZero(selected.DatasetID) != datasetID || selected.ResourceID != modelID {
			t.Fatalf("dataset %q selected object = %#v, want distinct binding key, alias, and backing Model", datasetID, selected)
		}
		if projectsignals.ValueOrZero(explorer.SelectedKey) != wantKey || projectsignals.ValueOrZero(explorer.Command.ObjectKey) != wantKey {
			t.Fatalf("dataset %q selection state = %#v/%#v, want hydrated binding key", datasetID, explorer.SelectedKey, explorer.Command.ObjectKey)
		}
		if projectsignals.ValueOrZero(explorer.Explore.Command.SemanticModelID) != semanticModelID || projectsignals.ValueOrZero(explorer.Explore.Command.DatasetID) != datasetID {
			t.Fatalf("dataset %q explore command = %#v, want canonical semantic target", datasetID, explorer.Explore.Command)
		}
		if executor.query.ModelID != semanticModelID || executor.query.Target != datasetID {
			t.Fatalf("dataset %q governed query = %#v, want semantic model and alias target", datasetID, executor.query)
		}
	}
}

func TestDataExploreCommandFromQueryRoundTripsDurableState(t *testing.T) {
	values, err := url.ParseQuery("v=1&mode=explore&semanticModel=semantic%3Asales&dataset=orders&dimension=orders.month&dimension=customers.state&metric=revenue&filter=%7B%22field%22%3A%22customers.state%22%2C%22operator%22%3A%22equals%22%2C%22values%22%3A%5B%22CA%22%5D%7D&sort=%7B%22field%22%3A%22revenue%22%2C%22direction%22%3A%22desc%22%7D&time=%7B%22field%22%3A%22orders.created_at%22%2C%22grain%22%3A%22month%22%7D&limit=250")
	if err != nil {
		t.Fatal(err)
	}
	command, err := dataExploreCommandFromQuery(values)
	if err != nil {
		t.Fatal(err)
	}
	if projectsignals.ValueOrZero(command.SemanticModelID) != "semantic:sales" || projectsignals.ValueOrZero(command.DatasetID) != "orders" {
		t.Fatalf("target = %q/%q", projectsignals.ValueOrZero(command.SemanticModelID), projectsignals.ValueOrZero(command.DatasetID))
	}
	if !reflect.DeepEqual(command.Dimensions, []string{"orders.month", "customers.state"}) || !reflect.DeepEqual(command.Metrics, []string{"revenue"}) {
		t.Fatalf("fields = %#v / %#v", command.Dimensions, command.Metrics)
	}
	if len(command.Filters) != 1 || command.Filters[0].Field != "customers.state" || command.Filters[0].Values[0] != "CA" {
		t.Fatalf("filters = %#v", command.Filters)
	}
	if len(command.Sort) != 1 || command.Sort[0].Field != "revenue" || command.Sort[0].Direction != "desc" {
		t.Fatalf("sort = %#v", command.Sort)
	}
	if command.Time == nil || command.Time.Field != "orders.created_at" || command.Time.Grain != "month" || command.Limit != 250 {
		t.Fatalf("time/limit = %#v / %d", command.Time, command.Limit)
	}
	if command.RequestSeq != 0 || command.ResetVersion != 0 {
		t.Fatalf("runtime state leaked into URL command: %#v", command)
	}
}

func dataExplorerAgentBrowserFixture(t *testing.T) (*BrowserHandler, *browserDataQueryStub) {
	t.Helper()
	const projectID = "project:test"
	const semanticModelID = "semantic:sales"
	const modelID = "model:orders"
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":        {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
			"order_history": {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Label: "Status"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":        {Model: "orders"},
			"order_history": {Model: "orders"},
		},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	executor := &browserDataQueryStub{}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: modelID, ProjectID: projectID, ServingStateID: "state", Type: "model", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: semanticModelID, ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.ResourceManifest{
			Models:         map[string]semanticmodel.Table{modelID: {ModelName: "orders"}},
			SemanticModels: map[string]*semanticmodel.Model{semanticModelID: model},
			NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": modelID}},
		}, compiled: map[string]*semanticquery.CompiledModel{semanticModelID: compiled}},
		QueryExecutor:           executor,
		ExplorationQueryLowerer: testDataExplorerQueryLowerer,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectID, nil
		},
		Environment: "dev",
		CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}

	return h, executor
}

func TestDataExplorerAgentDocumentUsesConfiguredCommands(t *testing.T) {
	h, _ := dataExplorerAgentBrowserFixture(t)
	h.AgentBootstrap = func(*stdhttp.Request) projectui.DataExplorerAgentBootstrap {
		return projectui.DataExplorerAgentBootstrap{Agent: map[string]any{"status": map[string]any{"enabled": true}, "composer": map[string]any{"disabled": false, "placeholder": "Ask about these data"}}, Visuals: map[string]any{"configured-artifact": true}}
	}
	h.AgentCommands = projectui.DataExplorerAgentCommandBindings{CreateConversation: agentgen.GenUIActionCreateAgentConversation(), CreateRun: agentgen.GenUIActionCreateAgentRun(), CancelRun: agentgen.GenUIActionCancelAgentRun()}
	response := httptest.NewRecorder()
	h.Explore(response, httptest.NewRequest(stdhttp.MethodGet, "/explore", nil))
	body := stdhtml.UnescapeString(response.Body.String())
	for _, want := range []string{`createAgentConversation`, `createAgentRun`, `cancelAgentRun`, `/chats/restore`, `agentContext`} {
		if !strings.Contains(body, want) {
			t.Errorf("Explorer document lacks configured agent contract %q: %s", want, body)
		}
	}
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("Explorer status=%d", response.Code)
	}
}

type explorerAgentFlushRecorder struct {
	*httptest.ResponseRecorder
	onFirstFlush func()
	once         sync.Once
}

func (r *explorerAgentFlushRecorder) Flush() { r.ResponseRecorder.Flush(); r.once.Do(r.onFirstFlush) }

func TestDataExplorerAgentUpdatesSubscribeBeforeBootstrapAndForwardChanges(t *testing.T) {
	for _, scenario := range []struct{ name, phase, clientID string }{
		{"read-with-tab", "read", "explorer-tab"},
		{"flush-with-tab", "flush", "explorer-tab"},
		{"read-without-tab", "read", ""},
		{"flush-without-tab", "flush", ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			phase := scenario.phase
			h, _ := dataExplorerAgentBrowserFixture(t)
			broker := pagestream.NewBroker()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			subscribed, released, read := false, false, false
			publish := func() {
				broker.Publish("agent-explorer", pagestream.SignalPatch{"agent": map[string]any{"conversations": []map[string]any{{"id": "own-conversation", "title": "Agent title committed during " + phase}}}})
			}
			h.AgentSubscribe = func(_ *stdhttp.Request, clientID string) (<-chan pagestream.SignalPatch, func(), error) {
				if clientID != "explorer-browser" {
					t.Fatalf("subscription client=%q", clientID)
				}
				subscribed = true
				wake, release, err := broker.Subscribe("agent-explorer")
				return wake, func() { released = true; release() }, err
			}
			h.AgentBootstrap = func(*stdhttp.Request) projectui.DataExplorerAgentBootstrap {
				read = true
				if !subscribed {
					t.Error("agent bootstrap read occurred before subscription")
				}
				if phase == "read" {
					publish()
				}
				return projectui.DataExplorerAgentBootstrap{Agent: map[string]any{"status": map[string]any{"enabled": true}}}
			}
			response := &explorerAgentFlushRecorder{ResponseRecorder: httptest.NewRecorder(), onFirstFlush: func() {
				if phase == "flush" {
					publish()
				}
			}}
			request := httptest.NewRequestWithContext(ctx, stdhttp.MethodGet, "/updates?route=data&surface=explore&clientId="+scenario.clientID, nil)
			request.AddCookie(&stdhttp.Cookie{Name: "pagestream_client_id", Value: "explorer-browser"})
			h.Updates(response, request)
			body := response.Body.String()
			initial := strings.Index(body, `"enabled":true`)
			live := strings.Index(body, "Agent title committed during "+phase)
			if !subscribed || !released || !read || initial < 0 || live < initial {
				t.Fatalf("agent subscription/read/release=%t/%t/%t, configured bootstrap then live update missing: %s", subscribed, read, released, body)
			}
			if !strings.Contains(body, `"surface":"data"`) || !strings.Contains(body, `"dataExplorer"`) {
				t.Fatalf("Explorer context/analytical bootstrap lost: %s", body)
			}
		})
	}
}

func TestDataExplorerAgentUpdatesAuthorizeBeforeSubscribing(t *testing.T) {
	h, _ := dataExplorerAgentBrowserFixture(t)
	called := false
	h.CurrentUser = func(*stdhttp.Request) (Principal, bool) { return Principal{}, false }
	h.AgentSubscribe = func(*stdhttp.Request, string) (<-chan pagestream.SignalPatch, func(), error) {
		called = true
		return nil, nil, nil
	}
	response := httptest.NewRecorder()
	h.Updates(response, httptest.NewRequest(stdhttp.MethodGet, "/updates?route=data&surface=explore", nil))
	if called || response.Code == stdhttp.StatusOK {
		t.Fatalf("unauthorized Explorer subscribed=%t status=%d", called, response.Code)
	}
}

func TestDataExplorerAgentUpdatesFailClosedWhenSubscriptionIsUnavailable(t *testing.T) {
	h, _ := dataExplorerAgentBrowserFixture(t)
	read := false
	h.AgentBootstrap = func(*stdhttp.Request) projectui.DataExplorerAgentBootstrap {
		read = true
		return projectui.DataExplorerAgentBootstrap{}
	}
	h.AgentSubscribe = func(*stdhttp.Request, string) (<-chan pagestream.SignalPatch, func(), error) {
		return nil, nil, errors.New("fixture capacity exhausted")
	}
	response := httptest.NewRecorder()
	h.Updates(response, httptest.NewRequest(stdhttp.MethodGet, "/updates?route=data&surface=explore", nil))
	if read || response.Code != stdhttp.StatusServiceUnavailable || response.Body.String() != "agent updates are unavailable\n" {
		t.Fatalf("unavailable subscription read=%t status=%d body=%s", read, response.Code, response.Body.String())
	}
}

func TestDataExplorerAgentUpdatesPreserveNewBrowserIdentity(t *testing.T) {
	for _, query := range []string{"", "&clientId=explorer-tab"} {
		t.Run(query, func(t *testing.T) {
			h, _ := dataExplorerAgentBrowserFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			clientID := ""
			h.AgentSubscribe = func(_ *stdhttp.Request, id string) (<-chan pagestream.SignalPatch, func(), error) {
				clientID = id
				return nil, nil, nil
			}
			response := &explorerAgentFlushRecorder{ResponseRecorder: httptest.NewRecorder(), onFirstFlush: cancel}
			h.Updates(response, httptest.NewRequestWithContext(ctx, stdhttp.MethodGet, "/updates?route=data&surface=explore"+query, nil))
			cookies := response.Result().Cookies()
			if len(cookies) != 1 || clientID == "" || cookies[0].Name != "pagestream_client_id" || cookies[0].Value != clientID || clientID == "explorer-tab" {
				t.Fatalf("canonical client cookie and subscription identity differ: cookiecount=%d", len(cookies))
			}
		})
	}
}
