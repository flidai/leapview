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
	ProductLogoURL       string
	UserAvatarURL        string
	UserName             string
	ColorMode            string
	AdminAccess          *AdminNavigationAccess
	ActiveConversationID string
	Conversations        []Conversation
}

// AdminNavigationAccess describes the settings surfaces the signed-in
// principal may open. A nil value preserves the complete navigation for
// callers that do not provide authorization context, such as static previews.
type AdminNavigationAccess struct {
	ManagePlatform bool
	ManageIdentity bool
	ViewAudit      bool
}

type Chrome struct {
	Sidebar Sidebar `json:"sidebar"`
}

type Sidebar struct {
	Active           string   `json:"active"`
	Admin            bool     `json:"admin,omitempty"`
	Area             string   `json:"area,omitempty"`
	Areas            []Area   `json:"areas,omitempty"`
	Compact          bool     `json:"compact"`
	DashboardID      *string  `json:"dashboardId,omitempty"`
	DashboardTitle   string   `json:"dashboardTitle"`
	Groups           []Group  `json:"groups"`
	History          *History `json:"history,omitempty"`
	ModelID          *string  `json:"modelId,omitempty"`
	ModelTitle       *string  `json:"modelTitle,omitempty"`
	PageTitle        string   `json:"pageTitle"`
	PrimaryAction    *Action  `json:"primaryAction,omitempty"`
	ProductLogoURL   *string  `json:"productLogoUrl,omitempty"`
	ProductName      string   `json:"productName"`
	UserAvatarURL    *string  `json:"userAvatarUrl,omitempty"`
	UserName         *string  `json:"userName,omitempty"`
	UserRole         *string  `json:"userRole,omitempty"`
	UserSettingsHref string   `json:"userSettingsHref"`
}

type Area struct {
	Href  string `json:"href"`
	Icon  string `json:"icon"`
	ID    string `json:"id"`
	Label string `json:"label"`
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
		isAdmin := context.Active == "admin"
		area := areaForActive(context.Active)
		navigation := areaNavigation(area)
		if isAdmin {
			navigation = adminNavigation(config.AdminAccess)
			area = ""
		}
		sidebarActive := context.Active
		if isAdmin {
			sidebarActive = firstNonEmpty(context.PageID, context.Active)
		}
		sidebar := Sidebar{
			Active: sidebarActive, Admin: isAdmin, Area: area, Compact: context.Compact,
			DashboardID: optional(context.SectionID), DashboardTitle: context.SectionTitle,
			ModelID: optional(context.RelatedID), ModelTitle: optional(context.RelatedTitle),
			PageTitle: context.PageTitle, ProductLogoURL: optional(config.ProductLogoURL), ProductName: firstNonEmpty(config.Presentation.ProductName, "LeapView"),
			UserAvatarURL: optional(config.UserAvatarURL), UserName: optional(config.UserName), UserRole: optional(config.RoleLabel),
			UserSettingsHref: "/admin/profile",
			Groups:           navigation,
		}
		if isAdmin {
			sidebar.PrimaryAction = &Action{Label: "Back to app", Href: "/", Icon: "back"}
		} else {
			sidebar.Areas = productAreas()
		}
		if area == "insights" {
			sidebar.PrimaryAction = &Action{Label: "New chat", Href: "/chats/new", Icon: "plus"}
			sidebar.History = &History{
				Label: "Chats", EmptyText: optional("No chats yet."),
				Items: historyItems(config, firstNonEmpty(context.HistoryID, config.ActiveConversationID)),
			}
		}
		return webpage.Layout{
			Presentation: config.Presentation,
			Assets:       config.Assets,
			ColorMode:    config.ColorMode,
			Signal:       Chrome{Sidebar: sidebar},
			Scripts:      []string{"/static/app-shell.js"},
			Mount: func(content g.Node, attrs ...g.Node) g.Node {
				return g.El("lv-app-shell", append(attrs, content)...)
			},
		}
	}
}

func productAreas() []Area {
	return []Area{
		{ID: "insights", Label: "Insights", Href: "/", Icon: "insights"},
		{ID: "develop", Label: "Develop", Href: "/sources", Icon: "code"},
	}
}

func areaForActive(active string) string {
	switch strings.TrimSpace(active) {
	case "sources", "models", "semantic-models", "dashboard-catalog", "connections", "pipelines", "develop":
		return "develop"
	case "data-explorer", "explore":
		return "insights"
	default:
		return "insights"
	}
}

func areaNavigation(area string) []Group {
	items := insightsNavigation()
	label := "Insights"
	if area == "develop" {
		items = developNavigation()
		label = "Develop"
	}
	return []Group{{Label: label, Items: items}}
}

func insightsNavigation() []Item {
	return []Item{
		{ID: "dashboards", Label: "Dashboards", Href: "/", Icon: "dashboard"},
		{ID: "data-explorer", Label: "Data Explorer", Href: "/explore", Icon: "database"},
	}
}

func developNavigation() []Item {
	return []Item{
		{ID: "sources", Label: "Sources", Href: "/sources", Icon: "database"},
		{ID: "models", Label: "Models", Href: "/models", Icon: "boxes"},
		{ID: "semantic-models", Label: "Semantic models", Href: "/semantic-models", Icon: "waypoints"},
		{ID: "dashboard-catalog", Label: "Dashboards", Href: "/dashboards", Icon: "dashboard"},
		{ID: "pipelines", Label: "Pipelines", Href: "/pipelines", Icon: "workflow"},
		{ID: "connections", Label: "Connections", Href: "/connections", Icon: "data"},
	}
}

func adminNavigation(access *AdminNavigationAccess) []Group {
	allowed := AdminNavigationAccess{
		ManagePlatform: true, ManageIdentity: true, ViewAudit: true,
	}
	if access != nil {
		allowed = *access
	}
	groups := []Group{
		{
			Label: "Personal",
			Items: []Item{
				{ID: "profile", Label: "Profile", Href: "/admin/profile", Icon: "user"},
				{ID: "security", Label: "Security & sessions", Href: "/admin/security", Icon: "activity"},
				{ID: "api-tokens", Label: "API tokens", Href: "/admin/api-tokens", Icon: "data"},
			},
		},
		{
			Label: "Product",
			Items: filterItems([]conditionalItem{
				{allowed: allowed.ManagePlatform, item: Item{ID: "general", Label: "General", Href: "/admin/general", Icon: "settings"}},
			}),
		},
		{
			Label: "Access",
			Items: filterItems([]conditionalItem{
				{allowed: allowed.ManageIdentity, item: Item{ID: "principals", Label: "Principals", Href: "/admin/principals", Icon: "users"}},
				{allowed: allowed.ManageIdentity, item: Item{ID: "groups", Label: "Groups", Href: "/admin/groups", Icon: "users-round"}},
				{allowed: allowed.ManagePlatform, item: Item{ID: "service-accounts", Label: "Service accounts", Href: "/admin/service-accounts", Icon: "bot"}},
				{allowed: allowed.ManagePlatform, item: Item{ID: "authentication", Label: "Authentication", Href: "/admin/authentication", Icon: "system"}},
			}),
		},
		{
			Label: "Data & sharing",
			Items: filterItems([]conditionalItem{
				{allowed: allowed.ManagePlatform, item: Item{ID: "storage", Label: "Storage", Href: "/admin/storage", Icon: "database"}},
				{allowed: allowed.ManagePlatform, item: Item{ID: "publications", Label: "Publications", Href: "/admin/publications", Icon: "globe"}},
			}),
		},
		{
			Label: "Operations",
			Items: filterItems([]conditionalItem{
				{allowed: allowed.ManagePlatform, item: Item{ID: "agent", Label: "Agent", Href: "/admin/agent", Icon: "bot"}},
				{allowed: allowed.ViewAudit, item: Item{ID: "queries", Label: "Query history", Href: "/admin/queries", Icon: "history"}},
				{allowed: allowed.ViewAudit, item: Item{ID: "audit", Label: "Audit log", Href: "/admin/audit", Icon: "activity"}},
				{allowed: allowed.ManagePlatform, item: Item{ID: "system", Label: "System", Href: "/admin/system", Icon: "system"}},
			}),
		},
	}
	visible := groups[:0]
	for _, group := range groups {
		if len(group.Items) > 0 {
			visible = append(visible, group)
		}
	}
	return visible
}

type conditionalItem struct {
	allowed bool
	item    Item
}

func filterItems(values []conditionalItem) []Item {
	items := make([]Item, 0, len(values))
	for _, value := range values {
		if value.allowed {
			items = append(items, value.item)
		}
	}
	return items
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
