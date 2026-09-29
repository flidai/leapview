package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	h.SavedExplorations = store
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
	h.SavedExplorations = store
	recorder := httptest.NewRecorder()
	_, _, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/explore?saved=00000000-0000-7000-8000-000000000001", nil), false)
	if ok || recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-owner restore = ok:%v status:%d body:%s, want 404", ok, recorder.Code, recorder.Body.String())
	}
}
