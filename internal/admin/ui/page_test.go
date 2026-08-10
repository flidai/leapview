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
			Profile:           AdminProfile{Email: "owner@example.com", DisplayName: "Owner", Username: "owner"},
		}, provider,
	)

	chrome, ok := signals["chrome"].(appshell.Chrome)
	if !ok {
		t.Fatalf("chrome = %T, want admin signal contract", signals["chrome"])
	}
	if chrome.Sidebar.Active != "profile" || !chrome.Sidebar.Compact || chrome.Sidebar.History != nil || len(chrome.Sidebar.Groups) != 4 {
		t.Fatalf("chrome = %#v", chrome)
	}
	page, ok := signals["page"].(uisignals.AdminPageSignal)
	if !ok || page.Kind != uisignals.RouteAdmin || page.Active != "profile" || page.Profile == nil || page.Profile.Email != "owner@example.com" {
		t.Fatalf("page = %#v", signals["page"])
	}
	runtime, ok := signals["runtime"].(uisignals.RouteRuntimeSignal)
	if !ok || runtime.Kind != uisignals.RouteAdmin {
		t.Fatalf("runtime = %#v", signals["runtime"])
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

func TestAdminPageRendersAdminRouteShell(t *testing.T) {
	var output strings.Builder
	provider := appshell.Provider(appshell.Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	err := AdminPage("general", AdminData{Workspace: workspaceview.WorkspaceView{ID: "platform", Title: "Platform"}}, provider).Render(&output)
	if err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, expected := range []string{"<lv-app-shell", "<lv-admin-page", `section="profile"`, "/updates?route=admin&amp;section=profile", "/static/admin-page.js"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("admin page is missing %q:\n%s", expected, html)
		}
	}
	if strings.Contains(html, "data-signals=") {
		t.Fatalf("admin page embedded bootstrap signals:\n%s", html)
	}
}
