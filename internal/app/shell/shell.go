package shell

import (
	"net/url"
	"strings"

	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	g "maragu.dev/gomponents"
)

type Conversation struct {
	ID           string
	Title        string
	TitlePending *bool
}

type Config struct {
	Presentation         webpage.Presentation
	Assets               staticasset.Resolver
	RoleLabel            string
	ActiveConversationID string
	Conversations        []Conversation
}

type Chrome struct {
	Sidebar Sidebar `json:"sidebar"`
}

type Sidebar struct {
	Active         string   `json:"active"`
	Admin          bool     `json:"admin,omitempty"`
	Compact        bool     `json:"compact"`
	DashboardID    *string  `json:"dashboardId,omitempty"`
	DashboardTitle string   `json:"dashboardTitle"`
	Groups         []Group  `json:"groups"`
	History        *History `json:"history,omitempty"`
	ModelID        *string  `json:"modelId,omitempty"`
	ModelTitle     *string  `json:"modelTitle,omitempty"`
	PageTitle      string   `json:"pageTitle"`
	PrimaryAction  *Action  `json:"primaryAction,omitempty"`
	UserRole       *string  `json:"userRole,omitempty"`
	WorkspaceTitle string   `json:"workspaceTitle"`
}

type Action struct {
	Href  string `json:"href"`
	Icon  string `json:"icon"`
	Label string `json:"label"`
}

type Group struct {
	Items []Item `json:"items"`
	Label string `json:"label"`
}

type Item struct {
	Active *bool   `json:"active,omitempty"`
	Href   string  `json:"href"`
	Icon   string  `json:"icon"`
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Meta   *string `json:"meta,omitempty"`
}

type History struct {
	EmptyText *string       `json:"emptyText,omitempty"`
	Items     []HistoryItem `json:"items"`
	Label     string        `json:"label"`
}

type HistoryItem struct {
	Active  bool   `json:"active"`
	Href    string `json:"href"`
	ID      string `json:"id"`
	Pending *bool  `json:"pending,omitempty"`
	Title   string `json:"title"`
}

func Provider(config Config) webpage.Provider {
	return func(context webpage.Context) webpage.Layout {
		isAdmin := context.Active == "admin" || context.Active == "connections"
		navigation := []Group{{Label: "Navigation", Items: globalNavigation()}}
		if isAdmin {
			navigation = adminNavigation()
		}
		sidebarActive := context.Active
		if isAdmin {
			sidebarActive = firstNonEmpty(context.PageID, context.Active)
		}
		sidebar := Sidebar{
			Active: sidebarActive, Admin: isAdmin, Compact: context.Compact,
			DashboardID: optional(context.SectionID), DashboardTitle: context.SectionTitle,
			ModelID: optional(context.RelatedID), ModelTitle: optional(context.RelatedTitle),
			PageTitle: context.PageTitle, UserRole: optional(config.RoleLabel),
			WorkspaceTitle: firstNonEmpty(context.ScopeTitle, context.ScopeID, config.Presentation.ProductName),
			Groups:         navigation,
		}
		if isAdmin {
			sidebar.PrimaryAction = &Action{Label: "Back to app", Href: "/", Icon: "back"}
		} else {
			sidebar.PrimaryAction = &Action{Label: "New chat", Href: "/chats/new", Icon: "plus"}
			sidebar.History = &History{
				Label: "Chats", EmptyText: optional("No chats yet."),
				Items: historyItems(config, firstNonEmpty(context.HistoryID, config.ActiveConversationID)),
			}
		}
		return webpage.Layout{
			Presentation: config.Presentation,
			Assets:       config.Assets,
			Signal:       Chrome{Sidebar: sidebar},
			Scripts:      []string{"/static/app-shell.js"},
			Mount: func(content g.Node, attrs ...g.Node) g.Node {
				return g.El("lv-app-shell", append(attrs, content)...)
			},
		}
	}
}

func globalNavigation() []Item {
	return []Item{
		{ID: "dashboards", Label: "Dashboards", Href: "/", Icon: "dashboard", Meta: optional("Reports")},
		{ID: "chat", Label: "Chats", Href: "/chats", Icon: "chat", Meta: optional("Agent interface")},
		{ID: "workspaces", Label: "Workspaces", Href: "/workspaces", Icon: "catalog", Meta: optional("Published assets")},
		{ID: "data", Label: "Data", Href: "/data", Icon: "cache", Meta: optional("Inspect rows")},
		{ID: "admin", Label: "Admin", Href: "/admin/profile", Icon: "settings", Meta: optional("Read-only administration")},
	}
}

func adminNavigation() []Group {
	return []Group{
		{
			Label: "Personal",
			Items: []Item{
				{ID: "profile", Label: "Profile", Href: "/admin/profile", Icon: "user"},
			},
		},
		{
			Label: "Access",
			Items: []Item{
				{ID: "principals", Label: "Principals", Href: "/admin/principals", Icon: "users"},
				{ID: "groups", Label: "Groups", Href: "/admin/groups", Icon: "users-round"},
			},
		},
		{
			Label: "Data",
			Items: []Item{
				{ID: "connections", Label: "Connections", Href: "/connections", Icon: "data"},
				{ID: "storage", Label: "Storage", Href: "/admin/storage", Icon: "database"},
				{ID: "publications", Label: "Publications", Href: "/admin/publications", Icon: "globe"},
			},
		},
		{
			Label: "Operations",
			Items: []Item{
				{ID: "agent", Label: "Agent", Href: "/admin/agent", Icon: "bot"},
				{ID: "queries", Label: "Query History", Href: "/admin/queries", Icon: "history"},
			},
		},
	}
}

func historyItems(config Config, activeConversationID string) []HistoryItem {
	items := make([]HistoryItem, 0, len(config.Conversations))
	for _, conversation := range config.Conversations {
		items = append(items, HistoryItem{
			ID: conversation.ID, Title: firstNonEmpty(conversation.Title, "Conversation"),
			Href:   "/chats/" + url.PathEscape(conversation.ID),
			Active: conversation.ID == activeConversationID, Pending: conversation.TitlePending,
		})
	}
	return items
}

func optional(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
