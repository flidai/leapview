package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerRefreshBootstrapCannotReplaceNewerCommand(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	executor := &cancellationIgnoringSemanticExecutor{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	h.QueryExecutor = executor
	clientID := "explorer-refresh-race"
	values := url.Values{
		"route":         {"data"},
		"surface":       {"explore"},
		"clientId":      {clientID},
		"mode":          {"explore"},
		"semanticModel": {"semantic:sales"},
		"dataset":       {"orders"},
		"dimension":     {"orders.status"},
	}
	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequestWithContext(streamContext, http.MethodGet, "/updates?"+values.Encode(), nil)
	stream := &notifyingResponseRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(stream, request)
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh bootstrap did not start its semantic query")
	}

	datasetID := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	newer := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 1,
		Explore: &projectsignals.DataExploreCommand{Action: projectsignals.Optional("configure"), RequestSeq: 1, Spec: spec},
	}
	if _, _, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/explore/command", nil), newer); !ok {
		t.Fatal("newer configure command failed")
	}
	select {
	case <-executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("newer command did not cancel the refresh query")
	}
	close(executor.release)

	select {
	case <-stream.wrote:
		t.Fatalf("late refresh bootstrap emitted a stale full-state patch: %s", stream.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh update stream did not stop after cancellation")
	}
}

func TestEmbeddedDataExplorerReconnectPreservesMountedState(t *testing.T) {
	for _, assetID := range []string{"model:orders", "semantic:sales"} {
		for _, scenario := range []string{"mounted", "fresh", "missing explorer", "different page", "different command target", "access revoked", "asset hidden", "credential denied"} {
			t.Run(assetID+"/"+scenario, func(t *testing.T) {
				h, executor := newDataExplorerURLTestHandler(t)
				values := url.Values{"route": {"data"}, "surface": {"asset"}, "asset": {assetID}, "section": {"data"}}
				initialResponse := httptest.NewRecorder()
				initial, ok := h.assetBootstrap(initialResponse, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil))
				if !ok {
					t.Fatalf("initial asset bootstrap failed: %d %s", initialResponse.Code, initialResponse.Body.String())
				}
				encoded, err := json.Marshal(initial)
				if err != nil {
					t.Fatal(err)
				}
				var mounted map[string]any
				if err := json.Unmarshal(encoded, &mounted); err != nil {
					t.Fatal(err)
				}
				command := mounted["dataExplorerCommand"].(map[string]any)
				command["clientId"], command["requestSeq"] = "embedded-edited", 7
				exploreCommand := command["explore"].(map[string]any)
				exploreCommand["spec"].(map[string]any)["limit"] = 17
				retained := mounted["dataExplorer"].(map[string]any)
				retained["explore"].(map[string]any)["status"] = map[string]any{"state": "success"}
				retained["explore"].(map[string]any)["result"] = map[string]any{"rows": []any{map[string]any{"retained": "result"}}}
				catalog := &embeddedExplorerCatalog{allowed: true}
				h.Catalog = catalog
				h.CurrentUser = func(*http.Request) (Principal, bool) { return Principal{ID: "principal:owner"}, true }
				switch scenario {
				case "fresh":
					mounted = nil
				case "missing explorer":
					delete(mounted, "dataExplorer")
				case "different page":
					mounted["page"].(map[string]any)["assetId"] = "model:other"
				case "different command target":
					command["objectKey"] = "model:other"
					exploreCommand["semanticModelId"] = "semantic:other"
					exploreCommand["spec"].(map[string]any)["modelId"] = "semantic:other"
				case "access revoked":
					catalog.allowed = false
				case "asset hidden":
					catalog.hidden = true
				case "credential denied":
					h.CurrentCredential = func(*http.Request) (access.APICredential, bool) {
						return access.APICredential{Token: access.APIToken{ID: "typed-denied", PermissionProfile: access.PermissionCatalogProfile}}, true
					}
				}
				if mounted != nil {
					encoded, err = json.Marshal(mounted)
					if err != nil {
						t.Fatal(err)
					}
					values.Set("datastar", string(encoded))
				}
				executor.calls = 0
				response := embeddedExplorerUpdates(t, h, values)
				if scenario == "access revoked" || scenario == "credential denied" || scenario == "asset hidden" {
					wantStatus := http.StatusForbidden
					if scenario == "asset hidden" {
						wantStatus = http.StatusNotFound
					}
					if response.Code != wantStatus || executor.calls != 0 {
						t.Fatalf("denied reconnect status=%d queries=%d body=%s", response.Code, executor.calls, response.Body.String())
					}
					return
				}
				if response.Code != http.StatusOK || catalog.resolveCalls == 0 || catalog.listCalls == 0 {
					t.Fatalf("reconnect did not reauthorize asset: status=%d resolve=%d list=%d body=%s", response.Code, catalog.resolveCalls, catalog.listCalls, response.Body.String())
				}
				body := response.Body.String()
				if scenario == "mounted" {
					for _, field := range []string{`"dataExplorer":`, `"dataExplorerCommand":`} {
						if strings.Contains(body, field) {
							t.Fatalf("mounted reconnect replaced edited command or retained results with %s", body)
						}
					}
					if executor.calls != 0 {
						t.Fatalf("mounted reconnect reexecuted %d queries", executor.calls)
					}
				} else if !strings.Contains(body, `"dataExplorer":`) || !strings.Contains(body, `"dataExplorerCommand":`) {
					t.Fatalf("fresh or mismatched asset did not receive an authorized bootstrap: %s", body)
				}
			})
		}
	}
}

type embeddedExplorerCatalog struct {
	allowed      bool
	hidden       bool
	resolveCalls int
	listCalls    int
}

func (c *embeddedExplorerCatalog) List(context.Context, projectcatalog.ListRequest) (projectcatalog.Page, error) {
	c.listCalls++
	if !c.allowed || c.hidden {
		return projectcatalog.Page{}, nil
	}
	return projectcatalog.Page{Items: []projectcatalog.Result{
		{Ref: projectcatalog.Ref{ID: "model:orders", Kind: projectgraph.KindModel}},
		{Ref: projectcatalog.Ref{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel}},
	}}, nil
}

func (c *embeddedExplorerCatalog) Resolve(_ context.Context, principal string, ref projectcatalog.Ref, capability access.Capability, _ bool) (projectcatalog.Result, error) {
	c.resolveCalls++
	if c.allowed && principal == "principal:owner" && capability == access.CapabilityResourceRead &&
		((ref.ID == "model:orders" && ref.Kind == projectgraph.KindModel) || (ref.ID == "semantic:sales" && ref.Kind == projectgraph.KindSemanticModel)) {
		return projectcatalog.Result{Ref: ref}, nil
	}
	return projectcatalog.Result{}, projectcatalog.ErrNotFound
}

func embeddedExplorerUpdates(t *testing.T, h *BrowserHandler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	response := &notifyingResponseRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(response, httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?"+values.Encode(), nil))
	}()
	select {
	case <-response.wrote:
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("embedded explorer updates did not respond")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("embedded explorer updates did not stop")
	}
	return response.ResponseRecorder
}
