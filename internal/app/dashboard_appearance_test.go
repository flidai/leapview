package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	appearancesqlite "github.com/flidai/leapview/internal/workspace/appearance/sqlite"
)

func TestDashboardAppearanceAPICompletesCommandAndPersistsAudit(t *testing.T) {
	store := testStore(t)
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('test-workspace', 'Test Workspace')`); err != nil {
		t.Fatal(err)
	}
	repository := testAccessRepository(store)
	principal, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{
		Email: "appearance-admin@example.test", DisplayName: "Appearance Admin", Role: access.RolePlatformAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := repository.CreateAPIToken(t.Context(), principal.ID, "appearance-test")
	if err != nil {
		t.Fatal(err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		WorkspaceID: "test-workspace", Auth: testAuth(store, "test-workspace", AuthConfig{APITokenOnly: true}),
	}))
	request := httptest.NewRequest(http.MethodPut, "/api/v1/workspaces/test-workspace/dashboards/executive-sales/appearance", strings.NewReader(`{"icon":"chart-no-axes-combined","color":"blue"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "appearance-request")
	recorder := httptest.NewRecorder()

	server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "COMMAND_CONTRACT_NOT_EXECUTED") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Icon     string `json:"icon"`
		Color    string `json:"color"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Icon != "chart-no-axes-combined" || response.Color != "blue" || response.Revision != 1 {
		t.Fatalf("response = %#v", response)
	}
	row, err := appearancesqlite.NewRepository(store.SQLDB()).Get(t.Context(), dashboardappearance.Key{WorkspaceID: "test-workspace", DashboardID: "executive-sales"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Icon != response.Icon || row.Color != response.Color {
		t.Fatalf("persisted appearance = %#v", row.Value)
	}
	events, err := testAccessRepository(store).ListAuditEvents(t.Context(), access.AuditEventFilter{WorkspaceID: "test-workspace", Action: "dashboard.appearance.updated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].TargetType != "dashboard" || events[0].TargetID != "executive-sales" || events[0].RequestID != "appearance-request" ||
		!strings.Contains(events[0].MetadataJSON, `"fields":["icon","color"]`) {
		t.Fatalf("audit events = %#v", events)
	}
}
