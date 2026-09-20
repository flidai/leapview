package ui

import (
	"net/url"
	"strings"
)

func adminGroupHref(groupID string) string {
	return "/admin/groups/" + url.PathEscape(groupID)
}

func adminPrincipalHref(principalID string) string {
	return "/admin/principals/" + url.PathEscape(principalID)
}

func adminPageTitle(active string) string {
	switch active {
	case "api-tokens":
		return "API tokens"
	case "api-token-new":
		return "New personal access token"
	case "archived-chats":
		return "Archived chats"
	case "security":
		return "Security & sessions"
	case "general":
		return "General"
	case "principals":
		return "Users"
	case "profile":
		return "Profile"
	case "principal-detail":
		return "User"
	case "groups":
		return "Groups"
	case "group-detail":
		return "Group"
	case "service-accounts":
		return "Service accounts"
	case "access":
		return "Access settings"
	case "service-accounts-new":
		return "New service account"
	case "authentication":
		return "Authentication"
	case "agent":
		return "Agent"
	case "storage":
		return "Storage"
	case "storage-detail":
		return "Storage table"
	case "queries":
		return "Query history"
	case "audit":
		return "Audit log"
	case "system":
		return "System"
	case "publications":
		return "Publications"
	case "delivery":
		return "Delivery"
	default:
		return "Profile"
	}
}

func normalizeAdminSection(active string) string {
	active = strings.TrimSpace(active)
	switch active {
	case "profile", "security", "api-tokens", "api-token-new", "archived-chats", "general", "principals", "principal-detail", "groups", "group-detail", "access", "service-accounts", "service-accounts-new", "authentication", "agent", "storage", "storage-detail", "queries", "audit", "system", "publications", "delivery":
		return active
	default:
		return "profile"
	}
}
