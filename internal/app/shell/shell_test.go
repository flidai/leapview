package shell

import (
	"testing"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
)

func TestProviderOwnsGlobalNavigationAndAgentHistory(t *testing.T) {
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
	if len(chrome.Sidebar.Groups) != 1 || len(chrome.Sidebar.Groups[0].Items) != 6 {
		t.Fatalf("navigation = %#v", chrome.Sidebar.Groups)
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

func TestGlobalNavigationIncludesInstancePipelines(t *testing.T) {
	items := globalNavigation()
	for _, item := range items {
		if item.ID == "pipelines" {
			if item.Href != "/pipelines" || item.Label != "Pipelines" || item.Icon != "workflow" {
				t.Fatalf("pipelines navigation = %#v", item)
			}
			return
		}
	}
	t.Fatal("global navigation does not include pipelines")
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
		}{{label: "General", icon: "settings"}, {label: "Workspaces", icon: "catalog"}}},
		{label: "Access", items: []struct {
			label string
			icon  string
		}{{label: "Principals", icon: "users"}, {label: "Groups", icon: "users-round"}, {label: "Service accounts", icon: "bot"}, {label: "Authentication", icon: "system"}}},
		{label: "Data & sharing", items: []struct {
			label string
			icon  string
		}{{label: "Connections", icon: "data"}, {label: "Storage", icon: "database"}, {label: "Publications", icon: "globe"}}},
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

func TestProviderFiltersAdminNavigationByPrivileges(t *testing.T) {
	provider := Provider(Config{
		Presentation: webpage.Presentation{ProductName: "LeapView"},
		AdminAccess:  &AdminNavigationAccess{ManageGrants: true, ViewConnections: true},
	})
	layout := provider(webpage.Context{Active: "admin", PageID: "principals"})
	chrome := layout.Signal.(Chrome)
	got := map[string]bool{}
	for _, group := range chrome.Sidebar.Groups {
		for _, item := range group.Items {
			got[item.ID] = true
		}
	}
	for _, id := range []string{"profile", "security", "api-tokens", "principals", "groups", "connections"} {
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

func TestProviderPlacesConnectionsInAdminSettingsNavigation(t *testing.T) {
	provider := Provider(Config{Presentation: webpage.Presentation{ProductName: "LeapView"}})
	layout := provider(webpage.Context{Active: "connections"})
	chrome := layout.Signal.(Chrome)
	if !chrome.Sidebar.Admin || chrome.Sidebar.Active != "connections" {
		t.Fatalf("sidebar = %#v, want admin connections navigation", chrome.Sidebar)
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
	t.Fatal("admin navigation did not contain Connections")
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
