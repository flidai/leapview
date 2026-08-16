package shell

import (
	"testing"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
)

func TestProviderOwnsInsightsNavigationAndAgentHistory(t *testing.T) {
	provider := Provider(Config{
		Presentation: webpage.Presentation{ProductName: "LeapView"},
		RoleLabel:    "Owner", UserName: "Ada Lovelace", UserAvatarURL: "/profile/avatars/ada/avatar-digest",
		ActiveConversationID: "chat-1",
		Conversations:        []Conversation{{ID: "chat-1", Title: "Revenue"}},
	})
	layout := provider(webpage.Context{Active: "chat", ScopeTitle: "Sales", SectionTitle: "Workspace", PageTitle: "Published assets"})
	chrome, ok := layout.Signal.(Chrome)
	if !ok {
		t.Fatalf("signal = %T, want shell.Chrome", layout.Signal)
	}
	if chrome.Sidebar.Area != "insights" || len(chrome.Sidebar.Areas) != 2 {
		t.Fatalf("areas = %q %#v, want insights with two available areas", chrome.Sidebar.Area, chrome.Sidebar.Areas)
	}
	if chrome.Sidebar.Areas[0].ID != "insights" || chrome.Sidebar.Areas[0].Icon != "insights" {
		t.Fatalf("insights area = %#v, want a distinct insights icon", chrome.Sidebar.Areas[0])
	}
	if chrome.Sidebar.UserSettingsHref != "/admin/profile" {
		t.Fatalf("user settings href = %q, want /admin/profile", chrome.Sidebar.UserSettingsHref)
	}
	if len(chrome.Sidebar.Groups) != 1 || len(chrome.Sidebar.Groups[0].Items) != 2 {
		t.Fatalf("navigation = %#v", chrome.Sidebar.Groups)
	}
	if chrome.Sidebar.Groups[0].Items[0].ID != "dashboards" || chrome.Sidebar.Groups[0].Items[1].ID != "data-explorer" || chrome.Sidebar.Groups[0].Items[1].Label != "Data Explorer" || chrome.Sidebar.Groups[0].Items[1].Href != "/explore" {
		t.Fatalf("insights navigation = %#v", chrome.Sidebar.Groups)
	}
	if chrome.Sidebar.UserName == nil || *chrome.Sidebar.UserName != "Ada Lovelace" {
		t.Fatalf("sidebar user name = %v, want Ada Lovelace", chrome.Sidebar.UserName)
	}
	if chrome.Sidebar.UserAvatarURL == nil || *chrome.Sidebar.UserAvatarURL != "/profile/avatars/ada/avatar-digest" {
		t.Fatalf("sidebar user avatar URL = %v", chrome.Sidebar.UserAvatarURL)
	}
	if chrome.Sidebar.ProductName != "LeapView" {
		t.Fatalf("sidebar product name = %q", chrome.Sidebar.ProductName)
	}
	if chrome.Sidebar.History == nil || len(chrome.Sidebar.History.Items) != 1 || !chrome.Sidebar.History.Items[0].Active {
		t.Fatalf("history = %#v", chrome.Sidebar.History)
	}
}

func TestProviderUsesDevelopNavigationForTechnicalRoutes(t *testing.T) {
	provider := Provider(Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	for _, active := range []string{"data", "models", "semantic-models", "connections", "pipelines"} {
		t.Run(active, func(t *testing.T) {
			layout := provider(webpage.Context{Active: active})
			chrome := layout.Signal.(Chrome)
			if chrome.Sidebar.Area != "develop" || chrome.Sidebar.Admin {
				t.Fatalf("sidebar = %#v, want develop area", chrome.Sidebar)
			}
			if len(chrome.Sidebar.Groups) != 1 {
				t.Fatalf("develop navigation = %#v", chrome.Sidebar.Groups)
			}
			got := []string{}
			for _, item := range chrome.Sidebar.Groups[0].Items {
				got = append(got, item.ID)
			}
			want := []string{"data", "models", "semantic-models", "pipelines", "connections"}
			if len(got) != len(want) {
				t.Fatalf("develop navigation = %v, want %v", got, want)
			}
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("develop navigation = %v, want %v", got, want)
				}
			}
			if chrome.Sidebar.History != nil || chrome.Sidebar.PrimaryAction != nil {
				t.Fatalf("develop sidebar should not project insights actions: %#v", chrome.Sidebar)
			}
		})
	}
}

func TestProviderPlacesExploreInInsightsNavigation(t *testing.T) {
	provider := Provider(Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	layout := provider(webpage.Context{Active: "data-explorer"})
	chrome := layout.Signal.(Chrome)
	if chrome.Sidebar.Admin || chrome.Sidebar.Area != "insights" || chrome.Sidebar.Active != "data-explorer" {
		t.Fatalf("sidebar = %#v, want insights Data Explorer navigation", chrome.Sidebar)
	}
	for _, group := range chrome.Sidebar.Groups {
		for _, item := range group.Items {
			if item.ID == "data-explorer" {
				if item.Label != "Data Explorer" || item.Href != "/explore" || item.Icon != "database" {
					t.Fatalf("Data Explorer item = %#v", item)
				}
				return
			}
		}
	}
	t.Fatal("Insights navigation did not contain Data Explorer")
}

func TestProviderProjectsCustomProductIdentity(t *testing.T) {
	provider := Provider(Config{
		Presentation:   webpage.Presentation{ProductName: "Northstar Analytics"},
		ProductLogoURL: "/product/logo/digest",
	})
	layout := provider(webpage.Context{})
	chrome := layout.Signal.(Chrome)
	if chrome.Sidebar.ProductName != "Northstar Analytics" || chrome.Sidebar.ProductLogoURL == nil || *chrome.Sidebar.ProductLogoURL != "/product/logo/digest" {
		t.Fatalf("sidebar product identity = %#v", chrome.Sidebar)
	}
	if layout.Presentation.ProductName != "Northstar Analytics" {
		t.Fatalf("presentation = %#v", layout.Presentation)
	}
}

func TestProviderUsesAdminNavigationAndBackAction(t *testing.T) {
	provider := Provider(Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	layout := provider(webpage.Context{Active: "admin", PageID: "principals", Compact: true})
	chrome := layout.Signal.(Chrome)
	if chrome.Sidebar.Active != "principals" || !chrome.Sidebar.Admin || !chrome.Sidebar.Compact {
		t.Fatalf("sidebar = %#v", chrome.Sidebar)
	}
	if chrome.Sidebar.Area != "" || len(chrome.Sidebar.Areas) != 0 {
		t.Fatalf("admin sidebar areas = %q %#v, want none", chrome.Sidebar.Area, chrome.Sidebar.Areas)
	}
	if len(chrome.Sidebar.Groups) != 5 {
		t.Fatalf("navigation = %#v", chrome.Sidebar.Groups)
	}
	wantGroups := []struct {
		label string
		items []struct {
			label string
			icon  string
		}
	}{
		{label: "Personal", items: []struct {
			label string
			icon  string
		}{{label: "Profile", icon: "user"}, {label: "Security & sessions", icon: "activity"}, {label: "API tokens", icon: "data"}}},
		{label: "Product", items: []struct {
			label string
			icon  string
		}{{label: "General", icon: "settings"}}},
		{label: "Access", items: []struct {
			label string
			icon  string
		}{{label: "Principals", icon: "users"}, {label: "Groups", icon: "users-round"}, {label: "Service accounts", icon: "bot"}, {label: "Authentication", icon: "system"}}},
		{label: "Data & sharing", items: []struct {
			label string
			icon  string
		}{{label: "Storage", icon: "database"}, {label: "Publications", icon: "globe"}}},
		{label: "Operations", items: []struct {
			label string
			icon  string
		}{{label: "Agent", icon: "bot"}, {label: "Query history", icon: "history"}, {label: "Audit log", icon: "activity"}, {label: "System", icon: "system"}}},
	}
	for groupIndex, wantGroup := range wantGroups {
		group := chrome.Sidebar.Groups[groupIndex]
		if group.Label != wantGroup.label || len(group.Items) != len(wantGroup.items) {
			t.Fatalf("navigation group[%d] = %#v, want %#v", groupIndex, group, wantGroup)
		}
		for itemIndex, wantItem := range wantGroup.items {
			item := group.Items[itemIndex]
			if item.Label != wantItem.label || item.Icon != wantItem.icon {
				t.Fatalf("navigation group[%d] item[%d] mismatch: got label %q/icon %q", groupIndex, itemIndex, item.Label, item.Icon)
			}
		}
	}
	if chrome.Sidebar.PrimaryAction == nil || chrome.Sidebar.PrimaryAction.Label != "Back to app" || chrome.Sidebar.PrimaryAction.Href != "/" {
		t.Fatalf("primary action = %#v", chrome.Sidebar.PrimaryAction)
	}
	if chrome.Sidebar.History != nil {
		t.Fatalf("admin sidebar history = %#v, want nil", chrome.Sidebar.History)
	}
}

func TestProviderFiltersAdminNavigationByAccess(t *testing.T) {
	provider := Provider(Config{
		Presentation: webpage.Presentation{ProductName: "LeapView"},
		AdminAccess:  &AdminNavigationAccess{ManageIdentity: true},
	})
	layout := provider(webpage.Context{Active: "admin", PageID: "principals"})
	chrome := layout.Signal.(Chrome)
	got := map[string]bool{}
	for _, group := range chrome.Sidebar.Groups {
		for _, item := range group.Items {
			got[item.ID] = true
		}
	}
	for _, id := range []string{"profile", "security", "api-tokens", "principals", "groups"} {
		if !got[id] {
			t.Fatalf("navigation is missing %q: %#v", id, chrome.Sidebar.Groups)
		}
	}
	for _, id := range []string{"general", "workspaces-admin", "service-accounts", "authentication", "storage", "publications", "agent", "queries", "audit", "system"} {
		if got[id] {
			t.Fatalf("navigation unexpectedly includes %q: %#v", id, chrome.Sidebar.Groups)
		}
	}
}

func TestProviderPlacesConnectionsInDevelopNavigation(t *testing.T) {
	provider := Provider(Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	layout := provider(webpage.Context{Active: "connections"})
	chrome := layout.Signal.(Chrome)
	if chrome.Sidebar.Admin || chrome.Sidebar.Area != "develop" || chrome.Sidebar.Active != "connections" {
		t.Fatalf("sidebar = %#v, want develop connections navigation", chrome.Sidebar)
	}
	for _, group := range chrome.Sidebar.Groups {
		for _, item := range group.Items {
			if item.ID == "connections" {
				if item.Href != "/connections" || item.Icon != "data" {
					t.Fatalf("connections item = %#v", item)
				}
				return
			}
		}
	}
	t.Fatal("develop navigation did not contain Connections")
}

func TestRouteContextSelectsActiveHistoryWithoutOwningHistory(t *testing.T) {
	provider := Provider(Config{
		Presentation:  webpage.Presentation{ProductName: "LeapView"},
		Conversations: []Conversation{{ID: "chat-1", Title: "Revenue"}},
	})
	layout := provider(webpage.Context{HistoryID: "chat-1"})
	chrome := layout.Signal.(Chrome)
	if chrome.Sidebar.History == nil || !chrome.Sidebar.History.Items[0].Active {
		t.Fatalf("history = %#v", chrome.Sidebar.History)
	}
}
