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

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	reportui "github.com/flidai/leapview/internal/dashboard/ui"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type loadingBuilderFake struct {
	*builderAuthoringFake
	beforePreview func()
}

func (f *loadingBuilderFake) Preview(ctx context.Context, request preview.PreviewRequest) (preview.Preview, error) {
	f.beforePreview()
	return f.builderAuthoringFake.Preview(ctx, request)
}

func TestBuilderNavigationShellFlushesBeforePreview(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	fake := &loadingBuilderFake{builderAuthoringFake: &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{
		DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue",
		Capabilities: uisignals.DashboardBuilderCapabilitiesSignal{CanEdit: true, CanAddVisual: true},
	}}}
	fake.beforePreview = func() {
		if !rec.Flushed {
			t.Fatal("navigation shell was not flushed before data query")
		}
		patches := ssetest.PatchSignals(t, rec.Body.String())
		if len(patches) != 1 {
			t.Fatalf("initial patches = %d", len(patches))
		}
		builder := patches[0]["builder"].(map[string]any)
		if builder["capabilities"].(map[string]any)["canEdit"] != false || builder["preview"].(map[string]any)["loading"] != true {
			t.Fatalf("unsafe loading shell: %#v", builder)
		}
		if !fake.builder.Capabilities.CanEdit {
			t.Fatal("loading shell mutated authoritative capabilities")
		}
		cancel()
	}
	h := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "principal-1" }}
	h.DashboardBuilderUpdates(rec, httptest.NewRequest(http.MethodGet, "/updates?dashboard=revenue&draft=draft-7&page=overview", nil).WithContext(ctx))
	if fake.previewCalls != 1 {
		t.Fatalf("preview calls = %d, body=%s", fake.previewCalls, rec.Body.String())
	}
}

func TestDashboardBuilderRefreshPreservesAgentSignals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	bootstrapCalls := 0
	fake := &loadingBuilderFake{builderAuthoringFake: &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{
		DashboardID: "revenue", DraftID: "draft-7", Title: "Revenue",
		Capabilities: uisignals.DashboardBuilderCapabilitiesSignal{CanEdit: true, CanAddVisual: true},
	}}}
	fake.beforePreview = func() {}
	h := Handler{
		Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
		AgentBootstrap: func(*http.Request, string) reportui.AgentBootstrap {
			bootstrapCalls++
			return reportui.AgentBootstrap{}
		},
	}
	currentSignals := `{"agent":{"activeConversationId":"conversation-1","transcript":[{"id":"answer-1","kind":"assistant","text":"Keep me"}]},"agentVisuals":{"chart":{"title":"Current result"}}}`
	request := httptest.NewRequest(http.MethodGet, "/updates?dashboard=revenue&draft=draft-7&route=dashboard_builder&snapshot=1&datastar="+url.QueryEscape(currentSignals), nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.DashboardBuilderUpdates(rec, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("snapshot refresh kept the SSE connection open")
	}

	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 2 {
		t.Fatalf("builder refresh patches = %d, want preview reset then canonical bootstrap: %s", len(patches), rec.Body.String())
	}
	if previews, ok := patches[0]["builderVisuals"]; !ok || previews != nil {
		t.Fatalf("builder refresh did not clear old visual envelope: %#v", patches[0])
	}
	if _, loading := patches[0]["status"]; loading {
		t.Fatalf("builder refresh showed a loading shell: %#v", patches[0])
	}
	bootstrap := patches[len(patches)-1]
	if builder, ok := bootstrap["builder"].(map[string]any); !ok || builder["title"] != "Revenue" {
		t.Fatalf("canonical builder bootstrap = %#v", bootstrap["builder"])
	}
	if _, exists := bootstrap["agent"]; exists {
		t.Fatalf("builder refresh replaced agent transcript: %#v", bootstrap["agent"])
	}
	if _, exists := bootstrap["agentVisuals"]; exists {
		t.Fatalf("builder refresh replaced agent visuals: %#v", bootstrap["agentVisuals"])
	}
	if bootstrapCalls != 0 {
		t.Fatalf("AgentBootstrap calls = %d, want 0 when client agent state is present", bootstrapCalls)
	}
}

type revisionRacingBuilderFake struct {
	*builderAuthoringFake
	latestBuilder uisignals.DashboardBuilderSignal
	builderReads  int
}

func (f *revisionRacingBuilderFake) Builder(_ context.Context, request builderview.Request) (uisignals.DashboardBuilderSignal, error) {
	f.builderReq = request
	f.builderReads++
	if f.builderReads == 1 {
		return f.builder, f.err
	}
	return f.latestBuilder, f.err
}

func TestDashboardBuilderAgentSnapshotDropsWhenManualSaveRacesPreview(t *testing.T) {
	oldBuilder := uisignals.DashboardBuilderSignal{
		ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-7",
		Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-1", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)},
	}
	newBuilder := oldBuilder
	newBuilder.Revision = uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: "sha256:" + strings.Repeat("b", 64)}
	fake := &revisionRacingBuilderFake{builderAuthoringFake: &builderAuthoringFake{builder: oldBuilder}, latestBuilder: newBuilder}
	h := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "principal-1" }}
	request := builderSnapshotRequest(t, oldBuilder, uisignals.RouteRuntimeSignal{
		ClientID: uisignals.Optional("client_1"), StreamInstanceID: uisignals.Optional("stream_1"),
	})
	request = withBuilderURLParams(request, "sales", "revenue")
	recorder := httptest.NewRecorder()
	h.DashboardBuilderUpdates(recorder, request)
	if fake.previewCalls != 1 || fake.builderReads != 2 {
		t.Fatalf("preview calls=%d builder reads=%d, want one preview and a revision fence", fake.previewCalls, fake.builderReads)
	}
	if patches := ssetest.PatchSignals(t, recorder.Body.String()); len(patches) != 0 {
		t.Fatalf("stale snapshot patches = %#v, want no update over the newer manual save", patches)
	}
}

func TestDashboardBuilderAgentSnapshotPreservesAppliedFiltersAndSelection(t *testing.T) {
	const pageID = "details"
	const generation = "generation-11"
	oldHash := "sha256:" + strings.Repeat("a", 64)
	currentHash := "sha256:" + strings.Repeat("b", 64)
	definition, err := dashboarddefinition.New("revenue", "Revenue", "", "sales", []dashboard.Page{{ID: pageID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition.FilterDefinitions = map[string]dashboardfilter.Definition{
		"status": {
			Label: "Status", Field: "status", ValueKind: dashboardfilter.ValueString,
			Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionComparison, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorEquals}}},
		},
	}
	definition.FilterBindings = map[string]dashboardfilter.Binding{
		"status": {Key: "status", ID: "status", Filter: "status", Scope: dashboardfilter.ScopeReport, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}, Selection: dashboardfilter.SelectionPolicy{Mode: dashboardfilter.SelectionSingle}},
	}
	machine := dashboardfilter.NewMachine(definition.FilterApplication.WithDefaults().Mode, definition.FilterBindingSpecs())
	initialState := machine.State()
	selectedExpression := dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorEquals,
		Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "open"},
	}
	selected, err := machine.Execute(dashboardfilter.Command{
		Kind: dashboardfilter.CommandMutate, BaseRevision: initialState.Revision, ClientMutationID: "select-open",
		BindingKey: "status", Operation: dashboardfilter.MutationSet, Expression: &selectedExpression,
	})
	if err != nil {
		t.Fatal(err)
	}
	activeServingStateID := "builder:draft-7:revision-1:" + oldHash + ":generation:" + generation
	store := dashboardsession.NewMemoryStore()
	key := dashboardsession.Key{
		ProjectID: "sales", DashboardID: "revenue", PrincipalOrClient: "principal-1:client_1",
		ServingStateID: activeServingStateID, StreamInstanceID: "stream_1",
	}
	if _, err := store.Create(context.Background(), key, dashboardsession.NewState(pageID, machine.Snapshot())); err != nil {
		t.Fatal(err)
	}
	selectedVisual := "chart-1"
	selectedPage := pageID
	builder := uisignals.DashboardBuilderSignal{
		ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-7",
		Revision:       uisignals.DashboardBuilderRevisionSignal{ID: "revision-2", Number: 2, ContentHash: currentHash},
		SelectedPageID: &selectedPage, SelectedVisualID: &selectedVisual,
		Pages: []uisignals.DashboardBuilderPageSignal{{ID: pageID, Visuals: []uisignals.DashboardBuilderVisualSignal{{ID: selectedVisual, VisualID: "chart"}}}},
	}
	evidence := preview.SemanticServingStateEvidence{Identity: projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: generation}}
	fake := &builderAuthoringFake{
		builder:     builder,
		compilation: preview.Compilation{Definition: definition, SemanticEvidence: evidence},
		preview:     preview.Preview{Definition: definition, SemanticEvidence: evidence, PagePatch: dashboard.Patch{}},
	}
	h := Handler{Authoring: fake, ProjectID: "sales", SessionStore: store, CurrentPrincipalID: func(*http.Request) string { return "principal-1" }}
	runtime := uisignals.RouteRuntimeSignal{
		ClientID: uisignals.Optional("client_1"), StreamInstanceID: uisignals.Optional("stream_1"),
		ServingStateID: uisignals.Optional(activeServingStateID), PageID: uisignals.Optional(pageID),
	}
	request := builderSnapshotRequest(t, builder, runtime)
	request = withBuilderURLParams(request, "sales", "revenue")
	recorder := httptest.NewRecorder()
	h.DashboardBuilderUpdates(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fake.builderReq.SelectedPageID != pageID || fake.builderReq.SelectedVisualID != selectedVisual {
		t.Fatalf("builder projection selection = %#v, want page %q and visual %q", fake.builderReq, pageID, selectedVisual)
	}
	if fake.previewReq.Filters.CompiledState == nil || fake.previewReq.Filters.CompiledState.Revision != selected.State.Revision || fake.previewReq.Filters.ServingStateID != activeServingStateID || fake.previewReq.Filters.ActivePageID != pageID {
		t.Fatalf("snapshot preview filters = %#v, want active session revision %d", fake.previewReq.Filters, selected.State.Revision)
	}
	patches := ssetest.PatchSignals(t, recorder.Body.String())
	if len(patches) != 2 {
		t.Fatalf("snapshot patches=%d, want visual clear and bootstrap: %s", len(patches), recorder.Body.String())
	}
	bootstrap := patches[1]
	filterState, ok := bootstrap["builderFilterState"].(map[string]any)
	if !ok || filterState["revision"] != float64(selected.State.Revision) {
		t.Fatalf("snapshot filter state = %#v, want retained revision %d", bootstrap["builderFilterState"], selected.State.Revision)
	}
	applied, ok := filterState["appliedControls"].(map[string]any)["status"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot applied filter = %#v", filterState["appliedControls"])
	}
	expression, ok := applied["expression"].(map[string]any)
	if !ok || expression["kind"] != string(dashboardfilter.ExpressionComparison) {
		t.Fatalf("snapshot applied expression = %#v", applied["expression"])
	}
	value, ok := expression["value"].(map[string]any)
	if !ok || value["value"] != "open" {
		t.Fatalf("snapshot applied filter value = %#v, want open", expression["value"])
	}
	runtimePatch := bootstrap["runtime"].(map[string]any)
	if runtimePatch["servingStateId"] != activeServingStateID {
		t.Fatalf("snapshot serving state = %#v, want retained session identity", runtimePatch["servingStateId"])
	}
}

func builderSnapshotRequest(t *testing.T, builder uisignals.DashboardBuilderSignal, runtime uisignals.RouteRuntimeSignal) *http.Request {
	t.Helper()
	refreshContext := map[string]any{"dashboardId": builder.DashboardID}
	if builder.SelectedPageID != nil {
		refreshContext["pageId"] = *builder.SelectedPageID
	}
	if builder.SelectedVisualID != nil {
		refreshContext["visualId"] = *builder.SelectedVisualID
	}
	signals, err := json.Marshal(map[string]any{
		"builderRefresh": refreshContext,
		"runtime":        runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{
		"dashboard": {builder.DashboardID}, "draft": {builder.DraftID}, "route": {"dashboard_builder"}, "snapshot": {"1"},
		"datastar": {string(signals)},
	}
	return httptest.NewRequest(http.MethodGet, "/updates?"+query.Encode(), nil)
}
