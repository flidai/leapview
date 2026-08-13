package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/platform"
	workspacegen "github.com/flidai/leapview/internal/workspace/api/gen"
	appearancesqlite "github.com/flidai/leapview/internal/workspace/appearance/sqlite"
	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
	catalog "github.com/flidai/leapview/internal/workspace/navigation"
	"github.com/go-chi/chi/v5"
)

func TestDashboardAppearanceAPICompletesGeneratedCommand(t *testing.T) {
	module, repository, audits := testDashboardAppearanceModule(t)
	invocation := workspacegen.GenUpdateDashboardAppearanceCommandInvocation{
		Surface: apigencommand.SurfaceAPI, Workspace: "sales", RequestID: "request-1", CorrelationID: "correlation-1",
	}
	ctx, guard, err := workspacegen.BeginGenUpdateDashboardAppearanceCommand(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/sales/dashboards/executive/appearance", strings.NewReader(`{"icon":"chart-no-axes-combined","color":"blue"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "request-1")
	request.Header.Set("X-Correlation-ID", "correlation-1")
	recorder := httptest.NewRecorder()

	module.UpdateDashboardAppearance(recorder, request, "sales", "executive", false)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !guard.Completed() {
		t.Fatal("generated dashboard appearance command was not completed")
	}
	row, err := repository.Get(t.Context(), dashboardappearance.Key{WorkspaceID: "sales", DashboardID: "executive"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Icon != "chart-no-axes-combined" || row.Color != "blue" {
		t.Fatalf("stored appearance = %#v", row.Value)
	}
	if len(*audits) != 1 {
		t.Fatalf("audit events = %#v", *audits)
	}
	audit := (*audits)[0]
	if audit.Action != "dashboard.appearance.updated" || audit.WorkspaceID != "sales" || audit.TargetType != "dashboard" || audit.TargetID != "executive" ||
		audit.PrincipalID != "principal-1" || audit.Privilege != access.PrivilegeManageWorkspace || audit.RequestID != "request-1" || audit.CorrelationID != "correlation-1" ||
		!strings.Contains(audit.MetadataJSON, `"payloadSchema":"DashboardAppearanceUpdatedAuditPayload"`) || !strings.Contains(audit.MetadataJSON, `"fields":["icon","color"]`) {
		t.Fatalf("audit event = %#v", audit)
	}
}

func TestDashboardAppearanceAPIUsesDeclaredInvalidFailure(t *testing.T) {
	module, _, audits := testDashboardAppearanceModule(t)
	invocation := workspacegen.GenUpdateDashboardAppearanceCommandInvocation{Surface: apigencommand.SurfaceAPI, Workspace: "sales"}
	ctx, _, err := workspacegen.BeginGenUpdateDashboardAppearanceCommand(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/sales/dashboards/executive/appearance", strings.NewReader(`{"icon":"not-a-lucide-icon"}`)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	module.UpdateDashboardAppearance(recorder, request, "sales", "executive", false)

	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "INVALID_DASHBOARD_APPEARANCE") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(*audits) != 0 {
		t.Fatalf("invalid mutation emitted audit events: %#v", *audits)
	}
}

func TestDashboardAppearanceUIUsesGeneratedSurfaceAndAudit(t *testing.T) {
	module, repository, audits := testDashboardAppearanceModule(t)
	color := "orange"
	request := httptest.NewRequest(http.MethodPost, "/catalog/appearance", nil)
	request.Header.Set("X-Request-ID", "ui-request")

	if _, err := module.saveDashboardAppearance(request, "sales", "executive", dashboardappearance.Patch{Color: &color}, apigencommand.SurfaceUI); err != nil {
		t.Fatal(err)
	}
	row, err := repository.Get(t.Context(), dashboardappearance.Key{WorkspaceID: "sales", DashboardID: "executive"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Color != "orange" {
		t.Fatalf("stored appearance = %#v", row.Value)
	}
	if len(*audits) != 1 || (*audits)[0].Action != "dashboard.appearance.updated" || (*audits)[0].RequestID != "ui-request" ||
		!strings.Contains((*audits)[0].MetadataJSON, `"fields":["color"]`) {
		t.Fatalf("UI audit events = %#v", *audits)
	}
}

func TestDashboardAppearanceUIRequiresRouteWorkspaceMatch(t *testing.T) {
	module, repository, audits := testDashboardAppearanceModule(t)
	invocation := workspacegen.GenUpdateDashboardAppearanceCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: "sales"}
	ctx, _, err := workspacegen.BeginGenUpdateDashboardAppearanceCommand(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("workspace", "sales")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	request := httptest.NewRequest(http.MethodPost, "/workspaces/sales/catalog/appearance", strings.NewReader(`{"dashboardAppearanceCommand":{"workspaceId":"other","dashboardId":"executive","color":"orange"}}`)).WithContext(ctx)
	request.Header.Set("X-LeapView-Operation-ID", workspacegen.GenOperationUpdateDashboardAppearance)
	recorder := httptest.NewRecorder()

	module.UpdateDashboardAppearanceFromUI(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := repository.Get(t.Context(), dashboardappearance.Key{WorkspaceID: "sales", DashboardID: "executive"}); err == nil {
		t.Fatal("mismatched workspace command persisted an appearance")
	}
	if len(*audits) != 0 {
		t.Fatalf("mismatched workspace command emitted audit events: %#v", *audits)
	}
}

func TestDashboardAppearanceUIUsesRouteWorkspace(t *testing.T) {
	module, repository, audits := testDashboardAppearanceModule(t)
	invocation := workspacegen.GenUpdateDashboardAppearanceCommandInvocation{Surface: apigencommand.SurfaceUI, Workspace: "sales"}
	ctx, _, err := workspacegen.BeginGenUpdateDashboardAppearanceCommand(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("workspace", "sales")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	request := httptest.NewRequest(http.MethodPost, "/workspaces/sales/catalog/appearance", strings.NewReader(`{"dashboardAppearanceCommand":{"workspaceId":"sales","dashboardId":"executive","color":"orange"}}`)).WithContext(ctx)
	request.Header.Set("X-LeapView-Operation-ID", workspacegen.GenOperationUpdateDashboardAppearance)
	recorder := httptest.NewRecorder()

	module.UpdateDashboardAppearanceFromUI(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	row, err := repository.Get(t.Context(), dashboardappearance.Key{WorkspaceID: "sales", DashboardID: "executive"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Color != "orange" {
		t.Fatalf("stored appearance = %#v", row.Value)
	}
	if len(*audits) != 1 || (*audits)[0].WorkspaceID != "sales" {
		t.Fatalf("UI audit events = %#v", *audits)
	}
}

func testDashboardAppearanceModule(t *testing.T) (*Module, *appearancesqlite.Repository, *[]access.AuditEventInput) {
	t.Helper()
	store, err := platform.Open(t.Context(), t.TempDir()+"/control.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	repository := appearancesqlite.NewRepository(store.SQLDB())
	audits := []access.AuditEventInput{}
	module := &Module{
		appearance: repository,
		recordAudit: func(_ context.Context, input access.AuditEventInput) error {
			audits = append(audits, input)
			return nil
		},
		currentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-1"}, true
		},
		handler: workspacehttp.Handler{ReadModel: workspacehttp.ReadModel{CatalogForWorkspace: func(workspaceID string) catalog.Catalog {
			return catalog.Catalog{Workspace: catalog.Workspace{ID: workspaceID}, Dashboards: []catalog.Dashboard{{ID: "executive", Title: "Executive"}}}
		}}},
	}
	return module, repository, &audits
}
