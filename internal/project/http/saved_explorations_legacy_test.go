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

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

type savedExplorationTestStore struct {
	scope   projectview.SavedExplorationScope
	items   map[string]projectview.SavedExplorationRecord
	created int
}

func (s *savedExplorationTestStore) CreateSavedExploration(_ context.Context, scope projectview.SavedExplorationScope, id, title, commandJSON string) (projectview.SavedExplorationRecord, error) {
	s.scope = scope
	s.created++
	if s.items == nil {
		s.items = map[string]projectview.SavedExplorationRecord{}
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	item := projectview.SavedExplorationRecord{ID: id, Title: title, CommandJSON: commandJSON, CreatedAt: now, UpdatedAt: now}
	s.items[id] = item
	return item, nil
}

func (s *savedExplorationTestStore) ListSavedExplorations(_ context.Context, scope projectview.SavedExplorationScope) ([]projectview.SavedExplorationRecord, error) {
	if scope != s.scope {
		return []projectview.SavedExplorationRecord{}, nil
	}
	items := make([]projectview.SavedExplorationRecord, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, item)
	}
	return items, nil
}

func (s *savedExplorationTestStore) GetSavedExploration(_ context.Context, scope projectview.SavedExplorationScope, id string) (projectview.SavedExplorationRecord, error) {
	item, ok := s.items[id]
	if !ok || scope != s.scope {
		return projectview.SavedExplorationRecord{}, projectview.ErrSavedExplorationNotFound
	}
	return item, nil
}

func TestCreateSavedExplorationStoresScopedGovernedCommandWithoutExecutingRows(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	h.CurrentUser = func(*http.Request) (Principal, bool) { return Principal{ID: "principal:alice", DevBypass: true}, true }
	store := &savedExplorationTestStore{}
	h.LegacySavedExplorations = store
	body := `{"title":"  Revenue by status  ","explorerUrl":"/explore?v=1&mode=explore&semanticModel=semantic%3Asales&dataset=orders&dimension=orders.status&metric=revenue"}`
	recorder := httptest.NewRecorder()
	h.CreateSavedExploration(recorder, httptest.NewRequest(http.MethodPost, "/explore/saved", strings.NewReader(body)))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("save status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	if store.created != 1 {
		t.Fatalf("created rows = %d, want 1", store.created)
	}
	if store.scope.ProjectID != projectgraph.ResourceID("project:test") || store.scope.Environment != "dev" || store.scope.PrincipalID != "principal:alice" {
		t.Fatalf("server-bound scope = %#v", store.scope)
	}
	if executor.calls != 0 {
		t.Fatalf("saving executed %d analytical queries, want 0", executor.calls)
	}
	var response struct {
		Item savedExplorationResponse `json:"item"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Item.Title != "Revenue by status" || response.Item.ID == "" || response.Item.Href != savedExplorationHref(response.Item.ID) {
		t.Fatalf("save response item = %#v", response.Item)
	}
	var stored projectsignals.DataExploreCommand
	if err := json.Unmarshal([]byte(store.items[response.Item.ID].CommandJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if projectsignals.ValueOrZero(stored.SemanticModelID) != "semantic:sales" || projectsignals.ValueOrZero(stored.DatasetID) != "orders" || len(stored.Dimensions) != 1 || stored.Dimensions[0] != "orders.status" || len(stored.Metrics) != 1 || stored.Metrics[0] != "revenue" {
		t.Fatalf("stored query command = %#v", stored)
	}

	recorder = httptest.NewRecorder()
	pageURL := "/explore?saved=" + response.Item.ID
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, pageURL, nil), false)
	if !ok {
		t.Fatalf("saved exploration did not restore: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if projectsignals.ValueOrZero(explorer.Explore.Command.SemanticModelID) != "semantic:sales" || projectsignals.ValueOrZero(explorer.Explore.Command.DatasetID) != "orders" || len(explorer.Explore.Command.Dimensions) != 1 || explorer.Explore.Command.Dimensions[0] != "orders.status" {
		t.Fatalf("restored query command = %#v", explorer.Explore.Command)
	}
	if executor.calls != 0 {
		t.Fatalf("opening the saved item executed %d analytical queries, want 0", executor.calls)
	}
	recorder = httptest.NewRecorder()
	_, _, ok = h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, pageURL, nil), true)
	if !ok || executor.calls != 1 {
		t.Fatalf("saved item updates = ok:%v queries:%d status:%d, want one governed query", ok, executor.calls, recorder.Code)
	}
}

func TestSavedExplorationRejectsExternalURLAndCrossScopeOpen(t *testing.T) {
	for _, value := range []string{"https://evil.invalid/explore?mode=explore", "/explore?mode=explore&unexpected=x", "/explore?mode=browse"} {
		if _, err := savedExplorationCommandFromURL(value); err == nil {
			t.Errorf("accepted invalid saved URL %q", value)
		}
	}
	h, _ := newDataExplorerURLTestHandler(t)
	h.CurrentUser = func(*http.Request) (Principal, bool) { return Principal{ID: "principal:bob", DevBypass: true}, true }
	store := &savedExplorationTestStore{scope: projectview.SavedExplorationScope{ProjectID: "project:test", Environment: "dev", PrincipalID: "principal:alice"}, items: map[string]projectview.SavedExplorationRecord{
		"00000000-0000-7000-8000-000000000001": {ID: "00000000-0000-7000-8000-000000000001", Title: "Private", CommandJSON: `{"semanticModelId":"semantic:sales","datasetId":"orders","dimensions":["orders.status"],"metrics":[],"filters":[],"sort":[],"limit":100,"requestSeq":0,"resetVersion":0}`},
	}}
	h.LegacySavedExplorations = store
	recorder := httptest.NewRecorder()
	_, _, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/explore?saved=00000000-0000-7000-8000-000000000001", nil), false)
	if ok || recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-owner restore = ok:%v status:%d body:%s, want 404", ok, recorder.Code, recorder.Body.String())
	}
}

func TestSavedExplorationBrowserListIncludesLegacyItemsOnlyInTheirOriginalScope(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	h.SavedExplorations = savedExplorationBrowserServiceStub{}
	h.CurrentUser = func(*http.Request) (Principal, bool) { return Principal{ID: "principal:alice", DevBypass: true}, true }
	const id = "00000000-0000-7000-8000-000000000001"
	h.LegacySavedExplorations = &savedExplorationTestStore{
		scope: projectview.SavedExplorationScope{ProjectID: "project:test", Environment: "dev", PrincipalID: "principal:alice"},
		items: map[string]projectview.SavedExplorationRecord{id: {ID: id, Title: "Earlier exploration"}},
	}
	request := httptest.NewRequest(http.MethodGet, "/explore", nil)
	state := h.savedExplorationStateForBrowser(request, "", false).State
	if len(projectsignals.ValueOrZero(state.List.LegacyItems)) != 1 || projectsignals.ValueOrZero(state.List.LegacyItems)[0].ID != id || projectsignals.ValueOrZero(state.List.LegacyItems)[0].Name != "Earlier exploration" || projectsignals.ValueOrZero(state.List.LegacyItems)[0].OpenHref != savedExplorationHref(id) {
		t.Fatalf("legacy list = %#v", state.List.LegacyItems)
	}
	if len(state.List.Items) != 0 || state.Current != nil {
		t.Fatalf("legacy items acquired revisioned lifecycle state: %#v", state)
	}
	h.Environment = "prod"
	state = h.savedExplorationStateForBrowser(request, "", false).State
	if len(projectsignals.ValueOrZero(state.List.LegacyItems)) != 0 {
		t.Fatalf("cross-environment legacy items = %#v", state.List.LegacyItems)
	}
	h.Environment = "dev"
	h.CurrentUser = func(*http.Request) (Principal, bool) { return Principal{ID: "principal:bob", DevBypass: true}, true }
	state = h.savedExplorationStateForBrowser(request, "", false).State
	if len(projectsignals.ValueOrZero(state.List.LegacyItems)) != 0 {
		t.Fatalf("cross-owner legacy items = %#v", state.List.LegacyItems)
	}
}

func TestCreateSavedExplorationPreservesCanonicalSpecThroughSaveAndLoad(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	h.CurrentUser = func(*http.Request) (Principal, bool) { return Principal{ID: "principal:alice", DevBypass: true}, true }
	store := &savedExplorationTestStore{}
	h.LegacySavedExplorations = store
	var spec exploration.ExplorationSpec
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"modelId":"semantic:sales","datasetId":"orders","dimensions":[{"field":"orders.status","alias":"order_status"}],"metrics":[{"field":"revenue","alias":"total_revenue"}],"filters":[{"field":"orders.status","datasetId":"orders","expression":{"kind":"set","operator":"in","values":[{"kind":"string","value":"paid"}]}}],"sort":[{"field":"revenue","direction":"desc"}],"limit":25,"table":{"density":"compact","striped":true,"showHeader":false,"rowHeight":28}}`), &spec); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	explorerURL := "/explore?" + url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(encoded)}}.Encode()
	body, err := json.Marshal(map[string]string{"title": "Canonical agent chart", "explorerUrl": explorerURL})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	h.CreateSavedExploration(recorder, httptest.NewRequest(http.MethodPost, "/explore/saved", strings.NewReader(string(body))))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("canonical save status = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Item savedExplorationResponse `json:"item"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var stored projectsignals.DataExploreCommand
	if err := json.Unmarshal([]byte(store.items[response.Item.ID].CommandJSON), &stored); err != nil {
		t.Fatal(err)
	}
	assertSpec := func(name string, got exploration.ExplorationSpec) {
		t.Helper()
		actual, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != string(encoded) {
			t.Fatalf("%s canonical spec = %s, want %s", name, actual, encoded)
		}
	}
	assertSpec("stored", stored.Spec)
	recorder = httptest.NewRecorder()
	_, restored, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, response.Item.Href, nil), false)
	if !ok {
		t.Fatalf("canonical restore failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertSpec("restored", restored.Explore.Command.Spec)
	if store.created != 1 || executor.calls != 0 {
		t.Fatalf("canonical save/load = creates:%d queries:%d, want 1 scoped save and no row execution", store.created, executor.calls)
	}
}

func TestSavedExplorationCanonicalURLValidation(t *testing.T) {
	valid := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {`{"schemaVersion":1,"modelId":"semantic:sales","datasetId":"orders","dimensions":[{"field":"orders.status"}],"metrics":[],"filters":[],"sort":[],"limit":25}`}}
	for name, mutate := range map[string]func(url.Values){
		"duplicate state":           func(v url.Values) { v.Add("state", v.Get("state")) },
		"duplicate version":         func(v url.Values) { v.Add("v", "2") },
		"duplicate mode":            func(v url.Values) { v.Add("mode", "explore") },
		"legacy conflict":           func(v url.Values) { v.Set("dimension", "orders.status") },
		"unknown parameter":         func(v url.Values) { v.Set("unexpected", "value") },
		"unsupported state version": func(v url.Values) { v.Set("v", "1") },
		"missing state version":     func(v url.Values) { v.Del("v") },
		"unknown state field":       func(v url.Values) { v.Set("state", strings.TrimSuffix(v.Get("state"), "}")+`,"unexpected":true}`) },
	} {
		t.Run(name, func(t *testing.T) {
			values := url.Values{}
			for key, items := range valid {
				values[key] = append([]string{}, items...)
			}
			mutate(values)
			if _, err := savedExplorationCommandFromURL("/explore?" + values.Encode()); err == nil {
				t.Fatalf("accepted invalid canonical URL: %s", values.Encode())
			}
		})
	}
	if _, err := savedExplorationCommandFromURL("/explore?" + valid.Encode()); err != nil {
		t.Fatalf("rejected valid canonical URL: %v", err)
	}
	for _, prefix := range []string{"https://example.invalid", "//example.invalid"} {
		if _, err := savedExplorationCommandFromURL(prefix + "/explore?" + valid.Encode()); err == nil {
			t.Fatalf("accepted external canonical URL with prefix %q", prefix)
		}
	}
}
