package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDashboardBuilderVisualWindowUsesExactSessionAndReturnsSignalEnvelope(t *testing.T) {
	definition := builderWindowDefinition(t)
	visual, err := visualizationruntime.EmptyEnvelopeFromDefinition(definition.Visualizations["orders"], 2, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	generation := "generation-1"
	fake := &builderAuthoringFake{
		compilation: preview.Compilation{Definition: definition, SemanticEvidence: preview.SemanticServingStateEvidence{Identity: servingIdentityForTest(generation)}},
		preview:     preview.Preview{Definition: definition, SemanticEvidence: preview.SemanticServingStateEvidence{Identity: servingIdentityForTest(generation)}, PagePatch: dashboard.Patch{Filters: dashboard.Filters{CompiledState: &dashboardfilter.State{Revision: 1}}, Visuals: map[string]visualizationir.VisualizationEnvelope{"orders": visual}}},
	}
	store := dashboardsession.NewMemoryStore()
	h := Handler{Authoring: fake, ProjectID: "sales", SessionStore: store, CurrentPrincipalID: func(*http.Request) string { return "actor-1" }}
	servingStateID := "builder:draft-7:revision-3:" + hash + ":generation:" + generation
	request := builderRequest(http.MethodPost, "/dashboards/revenue/draft/visual-window?draft=draft-7", map[string]any{
		"builder": map[string]any{
			"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-7",
			"revision": map[string]any{"id": "revision-3", "number": 3, "contentHash": hash},
			"pages":    []map[string]any{{"id": "overview"}},
		},
		"runtime":             map[string]any{"clientId": "client_1", "streamInstanceId": "stream_1", "servingStateId": servingStateID},
		"builderFilterState":  map[string]any{"revision": 1},
		"visualWindowCommand": map[string]any{"visualID": "orders", "requestSeq": 2, "resetVersion": 42, "start": 0, "limit": 50, "blockID": "a"},
	})
	request = withBuilderURLParams(request, "sales", "revenue")
	recorder := httptest.NewRecorder()
	h.DashboardBuilderVisualWindow(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	windowKey := "window:" + servingStateID + ":overview:1:orders"
	if _, ok := response["builderVisuals"]["orders"]; ok {
		t.Fatal("window response must not overwrite the base preview")
	}
	if _, ok := response["builderVisuals"][windowKey]["dataState"]; !ok {
		t.Fatalf("response did not contain visualization signal envelope: %s", recorder.Body.String())
	}
	if got := response["builderVisuals"][windowKey]["filterRevision"]; got != float64(1) {
		t.Fatalf("response filter revision = %#v, want 1", got)
	}
	if got := response["builderVisuals"][windowKey]["servingStateID"]; got != servingStateID {
		t.Fatalf("response must retain exact draft identity: got %#v, want %q", got, servingStateID)
	}
	if fake.previewCalls != 1 || fake.previewReq.Window == nil || fake.previewReq.Filters.CompiledState == nil || fake.previewReq.Filters.CompiledState.Revision != 1 {
		t.Fatalf("preview request = %#v calls=%d", fake.previewReq, fake.previewCalls)
	}
	if fake.previewReq.Window.ResetVersion != 42 {
		t.Fatalf("preview reset version = %d, want table reset version 42", fake.previewReq.Window.ResetVersion)
	}
	key := dashboardsession.Key{ProjectID: "sales", DashboardID: "revenue", PrincipalOrClient: "actor-1:client_1", ServingStateID: servingStateID, StreamInstanceID: "stream_1"}
	if _, err := store.Load(t.Context(), key); err != nil {
		t.Fatalf("ephemeral session not bound to exact runtime identity: %v", err)
	}
}

func TestDashboardBuilderVisualWindowRejectsStaleFilterRevisionAndWrongPage(t *testing.T) {
	definition := builderWindowDefinition(t)
	hash := "sha256:" + strings.Repeat("b", 64)
	fake := &builderAuthoringFake{compilation: preview.Compilation{Definition: definition, SemanticEvidence: preview.SemanticServingStateEvidence{Identity: servingIdentityForTest("generation-1")}}}
	h := Handler{Authoring: fake, ProjectID: "sales", SessionStore: dashboardsession.NewMemoryStore(), CurrentPrincipalID: func(*http.Request) string { return "actor-1" }}
	base := map[string]any{
		"builder":             map[string]any{"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-7", "revision": map[string]any{"id": "revision-3", "number": 3, "contentHash": hash}, "pages": []map[string]any{{"id": "overview"}}},
		"runtime":             map[string]any{"clientId": "client_1", "streamInstanceId": "stream_1", "servingStateId": "builder:draft-7:revision-3:" + hash + ":generation:generation-1"},
		"builderFilterState":  map[string]any{"revision": 0},
		"visualWindowCommand": map[string]any{"visualID": "orders", "requestSeq": 2, "resetVersion": 0, "start": 0, "limit": 50, "blockID": "a"},
	}
	stale := builderRequest(http.MethodPost, "/dashboards/revenue/draft/visual-window?draft=draft-7", base)
	stale = withBuilderURLParams(stale, "sales", "revenue")
	recorder := httptest.NewRecorder()
	h.DashboardBuilderVisualWindow(recorder, stale)
	if recorder.Code != http.StatusConflict || fake.previewCalls != 0 {
		t.Fatalf("stale filter status=%d calls=%d body=%s", recorder.Code, fake.previewCalls, recorder.Body.String())
	}
	wrongPage := map[string]any{}
	for key, value := range base {
		wrongPage[key] = value
	}
	wrongPage["visualWindowCommand"] = map[string]any{"visualID": "orders", "requestSeq": 2, "resetVersion": 42, "start": 0, "limit": 50, "blockID": "a"}
	wrongPage["builder"] = map[string]any{"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-7", "revision": map[string]any{"id": "revision-3", "number": 3, "contentHash": hash}, "pages": []map[string]any{{"id": "other"}}}
	wrong := builderRequest(http.MethodPost, "/dashboards/revenue/draft/visual-window?draft=draft-7", wrongPage)
	wrong = withBuilderURLParams(wrong, "sales", "revenue")
	recorder = httptest.NewRecorder()
	h.DashboardBuilderVisualWindow(recorder, wrong)
	if recorder.Code != http.StatusBadRequest || fake.previewCalls != 0 {
		t.Fatalf("wrong page status=%d calls=%d body=%s", recorder.Code, fake.previewCalls, recorder.Body.String())
	}
}

func builderWindowDefinition(t *testing.T) dashboarddefinition.Definition {
	t.Helper()
	visual := canonicalBuilderVisualDefinition(t)
	page := dashboard.Page{ID: "overview", Visuals: []dashboard.PageVisual{{ID: "orders-component", Kind: "visual", Visual: "orders"}}}
	definition, err := dashboarddefinition.New("revenue", "Revenue", "", "model", []dashboard.Page{page}, map[string]visualizationdefinition.Definition{"orders": visual})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func servingIdentityForTest(generation string) projectgraph.ServingIdentity {
	identity, _ := projectgraph.NewServingIdentity("sales", "test", generation)
	return identity
}
