package ui

import (
	"html"
	"strings"
	"testing"

	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	appshell "github.com/flidai/leapview/internal/app/shell"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	workspaceview "github.com/flidai/leapview/internal/workspace"
)

func TestAdminBootstrapSignalsUseAdminOwnedContracts(t *testing.T) {
	provider := appshell.Provider(appshell.Config{
		Presentation: webpage.Presentation{ProductName: "LeapView"}, RoleLabel: "Platform admin",
		ActiveConversationID: "conversation-1",
		Conversations: []appshell.Conversation{{
			ID: "conversation-1", Title: "Analysis", TitlePending: uisignals.Pointer(true),
		}},
	})
	signals := AdminBootstrapSignals("profile",
		AdminData{
			Workspace:         workspaceview.WorkspaceView{ID: "platform", Title: "Platform"},
			AuthConfigured:    true,
			AccessConfigured:  true,
			AccessStatusLabel: "Configured",
		}, provider,
	)

	chrome, ok := signals["chrome"].(appshell.Chrome)
	if !ok {
		t.Fatalf("chrome = %T, want admin signal contract", signals["chrome"])
	}
	if chrome.Sidebar.Active != "profile" || !chrome.Sidebar.Compact || chrome.Sidebar.History != nil || len(chrome.Sidebar.Groups) != 5 {
		t.Fatalf("chrome = %#v", chrome)
	}
	page, ok := signals["page"].(uisignals.AdminPageSignal)
	if !ok || page.Kind != uisignals.RouteAdmin || page.Active != "profile" || page.HeaderDetail != "Manage your photo and display name." {
		t.Fatalf("page = %#v", signals["page"])
	}
	runtime, ok := signals["runtime"].(uisignals.RouteRuntimeSignal)
	if !ok || runtime.Kind != uisignals.RouteAdmin {
		t.Fatalf("runtime = %#v", signals["runtime"])
	}
}

func TestStorageUsesStorageReadModelOnASettingsListPage(t *testing.T) {
	signals := AdminBootstrapSignals("storage", AdminData{Storage: AdminStorageData{
		TableCount:         1,
		DataFileCount:      3,
		TotalDataSizeLabel: "36 KiB",
		Tables:             []AdminStorageTable{{Schema: "model", Name: "orders", Type: "table"}},
	}})
	page, ok := signals["page"].(uisignals.AdminPageSignal)
	if !ok {
		t.Fatalf("page = %T, want AdminPageSignal", signals["page"])
	}
	if page.Active != "storage" || page.HeaderTitle != "Storage" || page.Storage == nil || len(page.Storage.Tables) != 1 {
		t.Fatalf("storage page = %#v", page)
	}
	if page.Metrics == nil {
		t.Fatal("storage metrics are missing")
	}
	metrics := *page.Metrics
	if len(metrics) != 1 || metrics[0].Label != "Total data size" || metrics[0].Value != "36 KiB" || metrics[0].Detail == nil || *metrics[0].Detail != "3 active files" {
		t.Fatalf("storage metrics = %#v", page.Metrics)
	}
	if _, legacyStream := signals["adminStorage"]; legacyStream {
		t.Fatalf("storage should render only from the shared page signal: %#v", signals)
	}
}

func TestStorageTableDetailFocusesOnStorageAndActiveFiles(t *testing.T) {
	data := AdminData{Storage: AdminStorageData{Tables: []AdminStorageTable{{
		Schema: "model", Name: "orders", Type: "table",
		TableUUID: "table-uuid", DuckLakePath: "model/orders/", BeginSnapshot: 7,
		RowCount: 1000, RowCountLabel: "1,000", FileCount: 1, SizeBytes: 12582912, SizeLabel: "12 MiB",
		Files: []AdminStorageFile{{
			Path: "model/orders/file.parquet", Format: "parquet", RecordCount: 1000,
			RecordCountLabel: "1,000", SizeBytes: 12582912, SizeLabel: "12 MiB", BeginSnapshot: 7,
		}},
	}}}}

	signals := AdminBootstrapSignals("storage-detail", data)
	page, ok := signals["page"].(uisignals.AdminPageSignal)
	if !ok {
		t.Fatalf("page = %T, want AdminPageSignal", signals["page"])
	}
	if page.Active != "storage-detail" || page.HeaderTitle != "Storage / model.orders" || page.Storage != nil {
		t.Fatalf("storage detail page = %#v", page)
	}
	if page.Metrics == nil || page.Sections == nil {
		t.Fatalf("storage detail content is missing: %#v", page)
	}
	metrics := *page.Metrics
	sections := *page.Sections
	if len(metrics) != 4 || metrics[0].Label != "Data size" || metrics[0].Value != "12 MiB" || metrics[2].Value != "1,000" {
		t.Fatalf("storage detail metrics = %#v", page.Metrics)
	}
	if len(sections) != 2 || sections[0].Title != "Storage" || sections[0].Facts == nil || sections[1].Table == nil {
		t.Fatalf("storage detail sections = %#v", page.Sections)
	}
	files := sections[1].Table
	if len(files.Rows) != 1 || files.Rows[0]["path"] != "model/orders/file.parquet" || files.Rows[0]["format"] != "PARQUET" {
		t.Fatalf("active files table = %#v", files)
	}

	var output strings.Builder
	if err := AdminPage("storage-detail", data, nil).Render(&output); err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(output.String())
	if !strings.Contains(rendered, `/updates?route=admin&schema=model&section=storage-detail&table=orders`) {
		t.Fatalf("storage detail missing scoped updates URL:\n%s", rendered)
	}
}

func TestAdminListsUseDebouncedPostSearchCommands(t *testing.T) {
	for _, active := range []string{"principals", "groups"} {
		t.Run(active, func(t *testing.T) {
			var output strings.Builder
			if err := AdminPage(active, AdminData{}, nil).Render(&output); err != nil {
				t.Fatal(err)
			}
			rendered := html.UnescapeString(output.String())
			if !strings.Contains(rendered, `data-on:lv-entity-list-query__debounce.200ms=`) {
				t.Fatalf("admin list missing debounced search bridge:\n%s", rendered)
			}
			if !strings.Contains(rendered, "@post('/admin/"+active+"/search'") {
				t.Fatalf("admin list missing POST search command:\n%s", rendered)
			}
		})
	}
}

func TestAdminListResultPatchesDoNotEchoSearchState(t *testing.T) {
	for _, active := range []string{"principals", "groups"} {
		t.Run(active, func(t *testing.T) {
			patch := AdminListResultsPatch(active, AdminData{ListQuery: "still typing", ListFilter: "all"})
			page, ok := patch["page"].(map[string]any)
			if !ok || len(patch) != 1 || len(page) != 1 {
				t.Fatalf("admin list patch must contain only result data: %#v", patch)
			}
			if _, echoed := page["listQuery"]; echoed {
				t.Fatalf("admin list patch echoed listQuery: %#v", patch)
			}
			if _, echoed := page["listFilter"]; echoed {
				t.Fatalf("admin list patch echoed listFilter: %#v", patch)
			}
		})
	}
}

func TestAdminDirectoryUsesPrincipalAvatarURL(t *testing.T) {
	signal := adminDirectoryList([]AdminPrincipal{{
		ID: "principal-1", DisplayName: "Ada Lovelace",
		ProfilePictureURL: "/profile/avatars/principal-1/avatar-digest",
		LastSeenAt:        "2026-08-09T15:30:00Z",
	}}, "")
	if len(signal.Items) != 1 {
		t.Fatalf("directory signal = %#v", signal)
	}
	avatarURL := signal.Items[0].AvatarURL
	if avatarURL == nil || *avatarURL != "/profile/avatars/principal-1/avatar-digest" {
		t.Fatalf("avatar URL = %v", avatarURL)
	}
	if got := signal.Items[0].LastSeenAt; got != "2026-08-09T15:30:00Z" {
		t.Fatalf("last seen = %q", got)
	}
}

func TestAdminDirectoryExcludesMachinePrincipals(t *testing.T) {
	signal := adminDirectoryList([]AdminPrincipal{
		{ID: "person", Kind: "user", DisplayName: "Person"},
		{ID: "service", Kind: "service_principal", DisplayName: "Service"},
		{ID: "publication", Kind: "dashboard_publication", DisplayName: "Publication"},
	}, "")
	if len(signal.Items) != 1 || signal.Items[0].ID != "person" {
		t.Fatalf("directory items = %#v, want only human principal", signal.Items)
	}
}

func TestAdminPageRendersAdminRouteShell(t *testing.T) {
	var output strings.Builder
	provider := appshell.Provider(appshell.Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	err := AdminPage("general", AdminData{Workspace: workspaceview.WorkspaceView{ID: "platform", Title: "Platform"}}, provider).Render(&output)
	if err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, expected := range []string{"<lv-app-shell", "<lv-admin-page", `section="general"`, "/updates?route=admin&amp;section=general", "/static/admin-page.js"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("admin page is missing %q:\n%s", expected, html)
		}
	}
	if strings.Contains(html, "data-signals=") {
		t.Fatalf("admin page embedded bootstrap signals:\n%s", html)
	}
}
