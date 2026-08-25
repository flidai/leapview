package http

import (
	"bytes"
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	"github.com/go-chi/chi/v5"
)

func TestDashboardAppearanceCommandPersistsCanonicalTargetAndPatchesDetail(t *testing.T) {
	store := &dashboardAppearanceStoreStub{}
	h := &BrowserHandler{
		DashboardAppearances: store,
		ResolveProjectID:     func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:          func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
		AuthorizeDashboard: func(_ *stdhttp.Request, dashboardID string, capability access.Capability) (bool, error) {
			if dashboardID != "dashboard:executive-sales" || capability != access.CapabilityResourceManage {
				t.Fatalf("authorization target = %q %q", dashboardID, capability)
			}
			return true, nil
		},
	}
	body := bytes.NewBufferString(`{"dashboardAppearanceCommand":{"icon":"house","color":"orange"}}`)
	request := httptest.NewRequest(stdhttp.MethodPost, "/dashboards/dashboard:executive-sales/appearance", body)
	request.Header.Set("X-LeapView-Operation-ID", dashboardappearance.UpdateCommandBinding().OperationID())
	route := chi.NewRouteContext()
	route.URLParams.Add("asset", "dashboard:executive-sales")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()

	h.DashboardAppearanceCommand(recorder, request)

	if recorder.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if store.key.ProjectID != "project:test" || store.key.DashboardID != "dashboard:executive-sales" || store.actor != "principal:test" {
		t.Fatalf("stored target = %#v actor %q", store.key, store.actor)
	}
	if store.patch.Icon == nil || *store.patch.Icon != "house" || store.patch.Color == nil || *store.patch.Color != "orange" {
		t.Fatalf("stored patch = %#v", store.patch)
	}
	for _, want := range []string{`"dashboardAppearance"`, `"icon":"house"`, `"color":"orange"`, `"revision":3`} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("response = %s, want %s", recorder.Body.String(), want)
		}
	}
}

func TestDashboardAppearanceCommandAllowsLocalDeveloperBypass(t *testing.T) {
	store := &dashboardAppearanceStoreStub{}
	h := &BrowserHandler{
		DashboardAppearances: store,
		ResolveProjectID:     func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:          func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "dev", DevBypass: true}, true },
		AuthorizeDashboard: func(*stdhttp.Request, string, access.Capability) (bool, error) {
			t.Fatal("local developer bypass reached authored grant authorization")
			return false, nil
		},
	}
	body := bytes.NewBufferString(`{"dashboardAppearanceCommand":{"icon":"house","color":"orange"}}`)
	request := httptest.NewRequest(stdhttp.MethodPost, "/dashboards/dashboard:executive-sales/appearance", body)
	request.Header.Set("X-LeapView-Operation-ID", dashboardappearance.UpdateCommandBinding().OperationID())
	route := chi.NewRouteContext()
	route.URLParams.Add("asset", "dashboard:executive-sales")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()

	h.DashboardAppearanceCommand(recorder, request)

	if recorder.Code != stdhttp.StatusOK || store.actor != "dev" {
		t.Fatalf("status = %d actor = %q body = %s", recorder.Code, store.actor, recorder.Body.String())
	}
}

func TestDashboardAppearanceReloadPrefersPersistedValueOverAuthoredValue(t *testing.T) {
	authoredIcon, authoredColor := "chart-no-axes-combined", dashboarddocument.DashboardAppearanceColorPurple
	dashboardID := projectgraph.ResourceID("dashboard:executive-sales")
	store := &dashboardAppearanceStoreStub{listed: map[projectgraph.ResourceID]dashboardappearance.Record{
		dashboardID: {Key: dashboardappearance.Key{ProjectID: "project:test", DashboardID: dashboardID}, Value: dashboardappearance.Value{Icon: "house", Color: "orange"}, Revision: 5},
	}}
	h := &BrowserHandler{
		DashboardAppearances: store,
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{DashboardSources: map[string]projectmanifest.DashboardSource{
			dashboardID.String(): {Document: dashboarddocument.DashboardDocument{Metadata: dashboarddocument.DashboardMetadata{ID: dashboardID.String()}, Spec: dashboarddocument.DashboardSpec{Appearance: &dashboarddocument.DashboardAppearance{Icon: &authoredIcon, Color: &authoredColor}}}},
		}}},
	}
	catalog := projectnavigation.Catalog{Dashboards: []projectnavigation.Dashboard{{ID: dashboardID.String()}}}
	h.enrichDashboardAppearances(t.Context(), "project:test", &catalog)
	if got := catalog.Dashboards[0]; got.Appearance.Icon != "house" || got.Appearance.Color != "orange" || got.AppearanceRevision != 5 {
		t.Fatalf("reloaded appearance = %#v", got)
	}
}

type dashboardAppearanceStoreStub struct {
	key    dashboardappearance.Key
	actor  string
	patch  dashboardappearance.Patch
	listed map[projectgraph.ResourceID]dashboardappearance.Record
}

func (s *dashboardAppearanceStoreStub) ListProject(context.Context, projectgraph.ResourceID) (map[projectgraph.ResourceID]dashboardappearance.Record, error) {
	return s.listed, nil
}

func (s *dashboardAppearanceStoreStub) ApplyPatch(_ context.Context, key dashboardappearance.Key, actor string, patch dashboardappearance.Patch) (dashboardappearance.Record, error) {
	s.key, s.actor, s.patch = key, actor, patch
	return dashboardappearance.Record{Key: key, Value: dashboardappearance.Value{Icon: *patch.Icon, Color: *patch.Color}, Revision: 3}, nil
}
