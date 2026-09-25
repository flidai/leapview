package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDashboardBuilderVisualTypeCommandKeepsActiveFilters(t *testing.T) {
	page := "overview"
	hash := "sha256:" + strings.Repeat("a", 64)
	retainedID := "builder:draft-1:revision-1:" + hash + ":generation:generation-1"
	state := dashboardfilter.NewMachine(dashboardfilter.ApplicationImmediate, nil).Snapshot()
	state.State.Revision = 7
	store := dashboardsession.NewMemoryStore()
	key := dashboardsession.Key{ProjectID: projectgraph.ResourceID("sales"), DashboardID: projectgraph.ResourceID("revenue"), PrincipalOrClient: "principal-1:client_1", ServingStateID: retainedID, StreamInstanceID: "stream_1"}
	if _, err := store.Create(context.Background(), key, dashboardsession.NewState(page, state)); err != nil {
		t.Fatal(err)
	}
	fake := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1", Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: hash}, Pages: []uisignals.DashboardBuilderPageSignal{{ID: page, Visuals: []uisignals.DashboardBuilderVisualSignal{{ID: "revenue-component", VisualID: "revenue"}}}}},
		preview: preview.Preview{PagePatch: dashboard.Patch{Filters: dashboard.Filters{CompiledState: &state.State}}},
	}
	handler := Handler{Authoring: fake, ProjectID: "sales", SessionStore: store, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{
		"builderCommand": map[string]any{"dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": hash, "pageId": page, "visualId": "revenue-component", "type": "line", "action": "set_visual_type"},
		"runtime":        map[string]any{"clientId": "client_1", "streamInstanceId": "stream_1", "servingStateId": retainedID},
	})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "retained-filter-type-change")
	recorder := httptest.NewRecorder()
	handler.DashboardBuilderCommand(recorder, withBuilderURLParams(req, "sales", "revenue"))
	if recorder.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.previewReq.Filters.CompiledState == nil || fake.previewReq.Filters.CompiledState.Revision != 7 || fake.previewReq.Filters.ServingStateID != retainedID {
		t.Fatalf("target preview lost active filters: %#v", fake.previewReq.Filters)
	}
}

func TestDashboardBuilderVisualTypeCommandRefreshesOnlyChangedVisual(t *testing.T) {
	page := "overview"
	revisionHash := "sha256:" + strings.Repeat("a", 64)
	targetEnvelope, err := visualizationruntime.EmptyEnvelopeFromDefinition(canonicalBuilderVisualDefinition(t), 2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	targetEnvelope.VisualID = "revenue"
	fake := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{
			ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: revisionHash},
			Pages: []uisignals.DashboardBuilderPageSignal{{ID: page, Visuals: []uisignals.DashboardBuilderVisualSignal{
				{ID: "revenue-component", VisualID: "revenue"},
				{ID: "cash-component", VisualID: "cash"},
			}}},
			SelectedPageID: &page,
		},
		preview: preview.Preview{PagePatch: dashboard.Patch{Visuals: map[string]visualizationir.VisualizationEnvelope{"revenue": targetEnvelope}}},
	}
	handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{
		"builderCommand": map[string]any{
			"dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1",
			"revisionContentHash": revisionHash, "pageId": page, "visualId": "revenue-component", "type": "line", "action": "set_visual_type",
		},
		"runtime": map[string]any{"servingStateId": "builder:stable-preview"},
	})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "targeted-visual-preview-1")
	recorder := httptest.NewRecorder()
	handler.DashboardBuilderCommand(recorder, withBuilderURLParams(req, "sales", "revenue"))
	if recorder.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.previewReq.VisualID != "revenue" {
		t.Fatalf("preview visual = %q, want revenue", fake.previewReq.VisualID)
	}
	patches := ssetest.PatchSignals(t, recorder.Body.String())
	if len(patches) != 2 {
		t.Fatalf("patches = %#v", patches)
	}
	reset, ok := patches[0]["builderVisuals"].(map[string]any)
	if !ok || len(reset) != 1 || reset["revenue"] != nil {
		t.Fatalf("targeted reset = %#v", patches[0]["builderVisuals"])
	}
	replacements, ok := patches[1]["builderVisuals"].(map[string]any)
	if !ok || len(replacements) != 1 || replacements["revenue"] == nil {
		t.Fatalf("targeted replacement = %#v", patches[1]["builderVisuals"])
	}
	runtimePatch, ok := patches[1]["runtime"].(map[string]any)
	if !ok || runtimePatch["servingStateId"] != "builder:stable-preview" {
		t.Fatalf("runtime patch = %#v", patches[1]["runtime"])
	}
	revenue, ok := replacements["revenue"].(map[string]any)
	if !ok || revenue["servingStateID"] != "builder:stable-preview" {
		t.Fatalf("replacement context = %#v", replacements["revenue"])
	}
}

func TestDashboardBuilderInvalidVisualTypePreviewKeepsSavedRevisionAndClearsOnlyTarget(t *testing.T) {
	page := "overview"
	oldHash := "sha256:" + strings.Repeat("a", 64)
	newHash := "sha256:" + strings.Repeat("b", 64)
	visualError := `visual "orders" query: table requires at least 1 detail column(s), got 0`
	definition := dashboarddefinition.Definition{ID: "revenue", SemanticModel: "sales_model"}
	fake := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{
			ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1",
			Revision:       uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: newHash},
			SelectedPageID: &page,
			Pages: []uisignals.DashboardBuilderPageSignal{{ID: page, Visuals: []uisignals.DashboardBuilderVisualSignal{
				{ID: "orders-component", VisualID: "orders", Type: "table", Title: "Orders", PreviewError: &visualError},
				{ID: "revenue-component", VisualID: "revenue-chart", Type: "bar", Title: "Revenue"},
			}}},
		},
		preview: preview.Preview{
			Definition:   definition,
			PagePatch:    dashboard.Patch{Filters: dashboard.Filters{}, Visuals: map[string]visualizationir.VisualizationEnvelope{}},
			VisualErrors: map[string]string{"orders": visualError},
		},
		compilation: preview.Compilation{Definition: definition},
	}
	handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	request := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{
		"builderCommand": map[string]any{
			"dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1",
			"revisionContentHash": oldHash, "pageId": page, "visualId": "orders-component", "type": "table", "action": "set_visual_type",
		},
		"runtime": map[string]any{"servingStateId": "builder:stable-preview"},
	})
	request.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	request.Header.Set("X-Request-ID", "invalid-target-visual-preview")
	recorder := httptest.NewRecorder()
	handler.DashboardBuilderCommand(recorder, withBuilderURLParams(request, "sales", "revenue"))
	if recorder.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	patches := ssetest.PatchSignals(t, recorder.Body.String())
	if len(patches) != 2 {
		t.Fatalf("patches = %#v, want targeted clear then saved builder patch", patches)
	}
	reset, ok := patches[0]["builderVisuals"].(map[string]any)
	if !ok || len(reset) != 1 || reset["orders"] != nil {
		t.Fatalf("targeted reset = %#v, want only orders cleared", patches[0]["builderVisuals"])
	}
	if got, ok := patches[1]["builderVisuals"].(map[string]any); !ok || len(got) != 0 {
		t.Fatalf("invalid target produced a visualization envelope: %#v", patches[1]["builderVisuals"])
	}
	builderPatch, ok := patches[1]["builder"].(map[string]any)
	if !ok {
		t.Fatalf("builder patch = %#v", patches[1]["builder"])
	}
	revision, ok := builderPatch["revision"].(map[string]any)
	if !ok || revision["id"] != "revision-2" || revision["number"] != float64(2) || revision["contentHash"] != newHash {
		t.Fatalf("saved revision patch = %#v, want revision-2", builderPatch["revision"])
	}
	previewState, ok := builderPatch["preview"].(map[string]any)
	if !ok || previewState["error"] != "1 visual unavailable" {
		t.Fatalf("preview failure state = %#v", builderPatch["preview"])
	}
	pages, ok := builderPatch["pages"].([]any)
	if !ok || len(pages) != 1 {
		t.Fatalf("builder pages = %#v", builderPatch["pages"])
	}
	pagePatch, ok := pages[0].(map[string]any)
	if !ok {
		t.Fatalf("page patch = %#v", pages[0])
	}
	visuals, ok := pagePatch["visuals"].([]any)
	if !ok || len(visuals) != 2 {
		t.Fatalf("builder visuals = %#v", pagePatch["visuals"])
	}
	var target map[string]any
	for _, value := range visuals {
		visual, _ := value.(map[string]any)
		if visual["visualId"] == "orders" {
			target = visual
		}
	}
	if target == nil || target["previewError"] != visualError || target["type"] != "table" {
		t.Fatalf("invalid target builder visual = %#v", target)
	}
	if fake.previewReq.VisualID != "orders" || !fake.previewReq.BestEffortVisuals {
		t.Fatalf("preview request = %#v", fake.previewReq)
	}
	if fake.executed.SetVisualType == nil || string(fake.executed.SetVisualType.Type) != "table" {
		t.Fatalf("executed visual type = %#v", fake.executed.SetVisualType)
	}
}
