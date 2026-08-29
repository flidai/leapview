package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestBuilderFilterRequestBindsExactRevisionAndBrowser(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	h := Handler{ProjectID: "sales", CurrentPrincipalID: func(*http.Request) string { return "actor-1" }, Authoring: &builderAuthoringFake{}}
	r := httptest.NewRequest(http.MethodPost, "/dashboards/revenue/draft/filter?draft=draft-7", nil)
	r = withBuilderURLParams(r, "sales", "revenue")
	signals := builderFilterSignals{
		Builder: uisignals.DashboardBuilderSignal{
			ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-7",
			Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-3", Number: 3, ContentHash: hash},
			Pages:    []uisignals.DashboardBuilderPageSignal{{ID: "overview"}},
		},
		Runtime: uisignals.RouteRuntimeSignal{ClientID: uisignals.Optional("client_1"), StreamInstanceID: uisignals.Optional("stream_1")},
	}
	got, err := h.builderFilterRequest(r, signals)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key.ServingStateID != "builder:draft-7:revision-3:"+hash || got.Key.PrincipalOrClient != "actor-1:client_1" || got.Key.StreamInstanceID != "stream_1" {
		t.Fatalf("builder filter key = %#v", got.Key)
	}
	if got.PageID != "overview" || got.Revision.Number != 3 {
		t.Fatalf("builder filter request = %#v", got)
	}
}

func TestBuilderFilterSessionIsolatedByExactRevision(t *testing.T) {
	store := dashboardsession.NewMemoryStore()
	h := Handler{SessionStore: store}
	definition, err := dashboarddefinition.New("revenue", "Revenue", "", "sales", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := dashboardsession.Key{ProjectID: projectgraph.ResourceID("sales"), DashboardID: projectgraph.ResourceID("revenue"), PrincipalOrClient: "actor:client", StreamInstanceID: "stream"}
	base.ServingStateID = "builder:draft-7:revision-1:sha256:" + strings.Repeat("a", 64)
	first, err := h.ensureBuilderFilterSession(context.Background(), base, "overview", definition)
	if err != nil {
		t.Fatal(err)
	}
	secondKey := base
	secondKey.ServingStateID = "builder:draft-7:revision-2:sha256:" + strings.Repeat("b", 64)
	second, err := h.ensureBuilderFilterSession(context.Background(), secondKey, "overview", definition)
	if err != nil {
		t.Fatal(err)
	}
	if first.Key.ID() == second.Key.ID() {
		t.Fatal("different exact draft revisions shared builder filter state")
	}
}

func TestBuilderFilterServingStateIdentityIncludesActiveGeneration(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	builder := uisignals.DashboardBuilderSignal{DraftID: "draft-7", Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-1", Number: 1, ContentHash: hash}}
	first := builderServingStateIDForGeneration(builder, "generation-1")
	second := builderServingStateIDForGeneration(builder, "generation-2")
	if first == "" || second == "" || first == second || !strings.Contains(first, ":generation:generation-1") || !strings.Contains(second, ":generation:generation-2") {
		t.Fatalf("generation-scoped builder identities = %q, %q", first, second)
	}
}

func TestBuilderFilterOptionsCreatesExactRevisionSession(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	definition, err := dashboarddefinition.New("revenue", "Revenue", "", "sales", []dashboard.Page{{ID: "overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition.FilterDefinitions = map[string]dashboardfilter.Definition{
		"status": {Label: "Status", Field: "status", ValueKind: dashboardfilter.ValueString, Options: dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceStatic, Values: []dashboardfilter.Option{{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "open"}, Label: "Open"}}}},
	}
	definition.FilterBindings = map[string]dashboardfilter.Binding{
		"status": {Key: "status", ID: "status", Filter: "status", Scope: dashboardfilter.ScopeReport, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}, Selection: dashboardfilter.SelectionPolicy{Mode: dashboardfilter.SelectionSingle}},
	}
	fake := &builderAuthoringFake{preview: preview.Preview{Definition: definition}}
	store := dashboardsession.NewMemoryStore()
	h := Handler{Authoring: fake, ProjectID: "sales", SessionStore: store, CurrentPrincipalID: func(*http.Request) string { return "actor-1" }}
	request := builderRequest(http.MethodPost, "/dashboards/revenue/draft/filter-options?draft=draft-7", map[string]any{
		"builder":                    map[string]any{"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-7", "revision": map[string]any{"id": "revision-3", "number": 3, "contentHash": hash}, "pages": []map[string]any{{"id": "overview"}}},
		"runtime":                    map[string]any{"clientId": "client_1", "streamInstanceId": "stream_1"},
		"builderFilterOptionRequest": map[string]any{"bindingKey": "status", "servingStateID": "builder:draft-7:revision-3:" + hash, "filterRevision": 1, "requestGeneration": 1},
	})
	request = withBuilderURLParams(request, "sales", "revenue")
	recorder := httptest.NewRecorder()
	h.DashboardBuilderFilterOptions(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["builderFilterOptionPages"]; !ok || fake.previewCalls == 0 {
		t.Fatalf("response=%v previewCalls=%d", response, fake.previewCalls)
	}
	key := dashboardsession.Key{ProjectID: "sales", DashboardID: "revenue", PrincipalOrClient: "actor-1:client_1", ServingStateID: "builder:draft-7:revision-3:" + hash, StreamInstanceID: "stream_1"}
	if _, err := store.Load(context.Background(), key); err != nil {
		t.Fatalf("exact revision session was not created: %v", err)
	}
}

func TestBuilderFilterOptionsScopesSessionToActiveServingGeneration(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	definition, err := dashboarddefinition.New("revenue", "Revenue", "", "sales", []dashboard.Page{{ID: "overview"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition.FilterDefinitions = map[string]dashboardfilter.Definition{
		"status": {Label: "Status", Field: "status", ValueKind: dashboardfilter.ValueString, Options: dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceStatic, Values: []dashboardfilter.Option{{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "open"}, Label: "Open"}}}},
	}
	definition.FilterBindings = map[string]dashboardfilter.Binding{
		"status": {Key: "status", ID: "status", Filter: "status", Scope: dashboardfilter.ScopeReport, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}, Selection: dashboardfilter.SelectionPolicy{Mode: dashboardfilter.SelectionSingle}},
	}
	fake := &builderAuthoringFake{preview: preview.Preview{Definition: definition, SemanticEvidence: preview.SemanticServingStateEvidence{Identity: projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation-9"}}}}
	store := dashboardsession.NewMemoryStore()
	h := Handler{Authoring: fake, ProjectID: "sales", SessionStore: store, CurrentPrincipalID: func(*http.Request) string { return "actor-1" }}
	builder := map[string]any{"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-7", "revision": map[string]any{"id": "revision-3", "number": 3, "contentHash": hash}, "pages": []map[string]any{{"id": "overview"}}}
	runtimeID := "builder:draft-7:revision-3:" + hash + ":generation:generation-9"
	request := builderRequest(http.MethodPost, "/dashboards/revenue/draft/filter-options?draft=draft-7", map[string]any{
		"builder": builder, "runtime": map[string]any{"clientId": "client_1", "streamInstanceId": "stream_1", "servingStateId": runtimeID},
		"builderFilterOptionRequest": map[string]any{"bindingKey": "status", "servingStateID": runtimeID, "filterRevision": 1, "requestGeneration": 1},
	})
	request = withBuilderURLParams(request, "sales", "revenue")
	recorder := httptest.NewRecorder()
	h.DashboardBuilderFilterOptions(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	key := dashboardsession.Key{ProjectID: "sales", DashboardID: "revenue", PrincipalOrClient: "actor-1:client_1", ServingStateID: runtimeID, StreamInstanceID: "stream_1"}
	if _, err := store.Load(context.Background(), key); err != nil {
		t.Fatalf("generation-scoped session was not created: %v", err)
	}
}

// Keep compile-time coverage that the builder transport still depends on the
// narrow authoring application surface rather than published dashboard APIs.
var _ = builderview.Request{}
var _ = preview.PreviewRequest{}
var _ = authoring.RevisionToken{}
var _ = dashboardfilter.State{}
