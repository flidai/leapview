package ui

import "strings"

func adminPageTitle(active string) string {
	switch active {
	case "api-tokens":
		return "API tokens"
	case "api-token-new":
		return "New personal access token"
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
	default:
		return "Profile"
	}
}

func normalizeAdminSection(active string) string {
	switch strings.TrimSpace(active) {
	case "profile", "security", "api-tokens", "api-token-new", "general", "principals", "principal-detail", "groups", "group-detail", "service-accounts", "service-accounts-new", "authentication", "agent", "storage", "storage-detail", "queries", "audit", "system", "publications":
		return strings.TrimSpace(active)
	default:
		return "profile"
	}
}
