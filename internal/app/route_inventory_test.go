package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	"github.com/go-chi/chi/v5"
)

// TestRouteInventory is the migration contract for moving route ownership out
// of process composition. Generated public API routes come from the TypeSpec
// contract; UI and operational routes are deliberately enumerated here.
func TestRouteInventory(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{})
	server.runtime.persistenceConfigured = true
	routes, ok := server.Routes().(chi.Routes)
	if !ok {
		t.Fatal("application handler does not expose chi routes")
	}
	got := map[string]int{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if method != "*" {
			got[method+" "+route]++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]struct{}{}
	metadata := map[string]routeMetadata{}
	for _, route := range strings.Split(strings.TrimSpace(nonAPIRouteInventory), "\n") {
		key := strings.TrimSpace(route)
		want[key] = struct{}{}
		method, path, ok := strings.Cut(key, " ")
		if !ok {
			t.Fatalf("invalid route inventory row %q", key)
		}
		contract, ok := nonAPIRouteMetadata(method, path)
		if !ok {
			t.Fatalf("%s has no owner/access/privilege contract", key)
		}
		metadata[key] = contract
	}
	for _, contract := range apiaggregate.GetAPIGenOperationContracts() {
		key := contract.Method + " " + contract.Path
		want[key] = struct{}{}
		owner, ok := apiOwner(contract.Tags)
		if !ok {
			t.Fatalf("%s has no capability owner for tags %v", contract.OperationID, contract.Tags)
		}
		privilege := ""
		if authz, ok := contract.Extensions["x-authz"].(map[string]any); ok {
			privilege, _ = authz["privilege"].(string)
		}
		metadata[key] = routeMetadata{owner: owner, access: contract.AuthzMode, privilege: privilege}
	}

	var problems []string
	for route, count := range got {
		if count != 1 {
			problems = append(problems, route+" registered more than once")
		}
		if _, ok := want[route]; !ok {
			problems = append(problems, "unexpected "+route)
		}
	}
	for route := range want {
		if got[route] == 0 {
			problems = append(problems, "missing "+route)
		}
	}
	sort.Strings(problems)
	if len(problems) != 0 {
		t.Fatalf("route inventory changed:\n%s", strings.Join(problems, "\n"))
	}
	rows := make([]string, 0, len(metadata))
	for key, contract := range metadata {
		rows = append(rows, fmt.Sprintf("%s|%s|%s|%s", key, contract.owner, contract.access, contract.privilege))
	}
	sort.Strings(rows)
	const expectedRouteContractDigest = "a5270d0cd631e5324b4113d31c42fd0604e740b08384826996d134580570afb2"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(rows, "\n"))))
	if digest != expectedRouteContractDigest {
		t.Fatalf("route ownership/auth contract changed: got digest %s\n%s", digest, strings.Join(rows, "\n"))
	}
}

type routeMetadata struct {
	owner     string
	access    string
	privilege string
}

func nonAPIRouteMetadata(method, path string) (routeMetadata, bool) {
	public := routeMetadata{access: "public"}
	switch {
	case path == "/favicon.ico" || path == "/static/*":
		public.owner = "ui"
		return public, true
	case path == "/.well-known/leapview":
		public.owner = "platform"
		return public, true
	case path == "/healthz" || path == "/readyz" || path == "/metrics" || strings.HasPrefix(path, "/__dev/"):
		public.owner = "platform"
		return public, true
	case path == "/api/docs" || path == "/api/openapi.json":
		public.owner = "api"
		return public, true
	case path == "/login" || path == "/device" || strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/.well-known/"):
		public.owner = "access"
		if path == "/device" || path == "/auth/logout" || path == "/auth/local/password" ||
			path == "/auth/desktop/authorize" || path == "/auth/desktop/session" ||
			path == "/auth/desktop/disconnect" {
			public.access = "authenticated"
		}
		return public, true
	case strings.HasPrefix(path, "/profile/avatar") || strings.HasPrefix(path, "/profile/avatars/"):
		public.owner = "access"
		public.access = "authenticated"
		return public, true
	case strings.HasPrefix(path, "/public/dashboards/") || strings.HasPrefix(path, "/embed/dashboards/"):
		public.owner = "dashboard"
		return public, true
	}

	authenticated := routeMetadata{access: "authenticated"}
	switch {
	case strings.HasPrefix(path, "/product/logo/"):
		authenticated.owner = "admin"
	case path == "/admin" || path == "/admin/profile" || path == "/admin/security" || path == "/admin/api-tokens" || path == "/admin/personal-settings/command":
		authenticated.owner = "admin"
	case path == "/admin/agent" || path == "/admin/agent/config":
		authenticated.owner = "agent"
		authenticated.privilege = "MANAGE_PLATFORM"
	case strings.HasPrefix(path, "/admin/publications"):
		authenticated.owner = "admin"
		authenticated.privilege = "MANAGE_PUBLICATIONS"
	case strings.HasPrefix(path, "/admin/queries") || strings.HasPrefix(path, "/admin/audit"):
		authenticated.owner = "admin"
		authenticated.privilege = "VIEW_AUDIT"
	case path == "/admin/workspaces":
		authenticated.owner = "admin"
		authenticated.privilege = "MANAGE_WORKSPACE"
	case path == "/admin/access/command" || strings.HasPrefix(path, "/admin/principals") || strings.HasPrefix(path, "/admin/groups"):
		authenticated.owner = "admin"
		authenticated.privilege = "MANAGE_GRANTS"
	case strings.HasPrefix(path, "/admin"):
		authenticated.owner = "admin"
		authenticated.privilege = "MANAGE_PLATFORM"
	case strings.HasPrefix(path, "/chats"):
		authenticated.owner = "agent"
		switch {
		case path == "/chats/turns":
			authenticated.privilege = "USE_AGENT"
		case path == "/chats/references/search":
			authenticated.privilege = "VIEW_ITEM"
		default:
			authenticated.privilege = "VIEW_AGENT"
		}
	case strings.HasPrefix(path, "/chat"):
		authenticated.owner = "agent"
	case path == "/candidates/{candidate}":
		authenticated.owner = "deployment"
		authenticated.privilege = "AUTHOR_PROJECT"
	case strings.HasPrefix(path, "/candidates/{candidate}/"):
		authenticated.owner = "dashboard"
		authenticated.privilege = "AUTHOR_PROJECT"
	case strings.Contains(path, "/dashboards/") || strings.Contains(path, "/commands/"):
		authenticated.owner = "dashboard"
		authenticated.privilege = "VIEW_ITEM"
	case strings.Contains(path, "/assets/") && strings.HasSuffix(path, "/refresh"):
		authenticated.owner = "workspace"
		authenticated.privilege = "REFRESH_DATA"
	case strings.Contains(path, "/access/") || strings.HasSuffix(path, "/access/upsert") || strings.HasSuffix(path, "/access/remove"):
		authenticated.owner = "workspace"
		authenticated.privilege = "MANAGE_GRANTS"
	case path == "/workspaces/{workspace}/catalog/appearance":
		authenticated.owner = "workspace"
		authenticated.privilege = "MANAGE_WORKSPACE"
	case path == "/" || path == "/catalog/search" || path == "/data" || path == "/data/command" ||
		strings.HasPrefix(path, "/workspaces") || strings.HasPrefix(path, "/connections"):
		authenticated.owner = "workspace"
		authenticated.privilege = "VIEW_ITEM"
	case path == "/updates":
		authenticated.owner = "ui"
	default:
		return routeMetadata{}, false
	}
	_ = method
	return authenticated, true
}

func apiOwner(tags []string) (string, bool) {
	owners := map[string]string{
		"Access": "access", "Current User": "access", "Service Principals": "access",
		"Connections": "analytics",
		"Agent":       "agent", "BI": "dashboard", "Dashboards": "dashboard", "Publications": "dashboard",
		"Deployments": "deployment", "Managed Data": "manageddata", "Refresh": "refresh",
		"Releases": "release", "Projects": "release", "Workspaces": "workspace",
		"Instance": "platform", "System": "platform",
	}
	for _, tag := range tags {
		if owner := owners[tag]; owner != "" {
			return owner, true
		}
		switch {
		case strings.Contains(tag, "Refresh"):
			return "refresh", true
		case strings.Contains(tag, "Publication") || strings.Contains(tag, "Dashboard") || strings.Contains(tag, "Semantic"):
			return "dashboard", true
		case strings.Contains(tag, "Managed Data"):
			return "manageddata", true
		case strings.Contains(tag, "Deployment"):
			return "deployment", true
		case strings.Contains(tag, "Release") || strings.Contains(tag, "Project"):
			return "release", true
		case strings.Contains(tag, "Workspace") || strings.Contains(tag, "Search"):
			return "workspace", true
		case strings.Contains(tag, "Agent"):
			return "agent", true
		case strings.Contains(tag, "Access") || strings.Contains(tag, "Principal") ||
			strings.Contains(tag, "Grant") || strings.Contains(tag, "Policy") ||
			strings.Contains(tag, "Group") || strings.Contains(tag, "Role") ||
			strings.Contains(tag, "Audit") || strings.Contains(tag, "Authorization"):
			return "access", true
		}
	}
	return "", false
}

const nonAPIRouteInventory = `
CONNECT /metrics
CONNECT /static/*
DELETE /profile/avatar
DELETE /metrics
DELETE /static/*
GET /
GET /.well-known/leapview
GET /.well-known/oauth-authorization-server
GET /.well-known/oauth-protected-resource
GET /.well-known/oauth-protected-resource/mcp
GET /__dev/pagestream/signals
GET /__dev/pagestream/traces
GET /admin
GET /admin/api-tokens
GET /admin/agent
GET /admin/audit
GET /admin/authentication
GET /admin/general
GET /admin/groups
GET /admin/groups/{group}
GET /admin/principals
GET /admin/principals/{principal}
GET /admin/profile
GET /admin/publications
GET /admin/queries
GET /admin/security
GET /admin/service-accounts
GET /admin/storage
GET /admin/storage-v2
GET /admin/system
GET /admin/workspaces
GET /api/docs
GET /api/openapi.json
GET /auth/{provider}
GET /auth/{provider}/callback
GET /auth/desktop/authorize
GET /auth/desktop/session
GET /chat
GET /chat/*
GET /chat/updates
GET /chats
GET /chats/new
GET /chats/references/search
GET /chats/restore
GET /chats/{conversation}
GET /candidates/{candidate}
GET /candidates/{candidate}/workspaces/{workspace}/dashboards/{dashboard}
GET /candidates/{candidate}/workspaces/{workspace}/dashboards/{dashboard}/pages/{page}
GET /candidates/{candidate}/workspaces/{workspace}/updates
GET /connections
GET /connections/{asset}
GET /connections/{asset}/{section}
GET /connections/{connection}/sources/{source}
GET /connections/{connection}/sources/{source}/{section}
GET /data
GET /device
GET /embed/dashboards/{publicId}
GET /embed/dashboards/{publicId}/pages/{page}
GET /favicon.ico
GET /healthz
GET /login
GET /profile/avatars/{principal}/{digest}
GET /product/logo/{digest}
GET /metrics
GET /public/dashboards/{publicId}
GET /public/dashboards/{publicId}/pages/{page}
GET /public/dashboards/{publicId}/updates
GET /public/dashboards/{publicId}/visuals/{visual}/tiles/{revision}/{z}/{x}/{y}.mvt
GET /readyz
GET /static/*
GET /updates
GET /workspaces
GET /workspaces/{workspace}
GET /workspaces/{workspace}/access/search
GET /workspaces/{workspace}/assets/{asset}
GET /workspaces/{workspace}/assets/{asset}/{section}
GET /workspaces/{workspace}/dashboards/{dashboard}
GET /workspaces/{workspace}/dashboards/{dashboard}/pages/{page}
GET /workspaces/{workspace}/dashboards/{dashboard}/visuals/{visual}/tiles/{revision}/{z}/{x}/{y}.mvt
GET /workspaces/{workspace}/data
HEAD /metrics
HEAD /static/*
OPTIONS /metrics
OPTIONS /static/*
PATCH /admin/agent/config
PATCH /metrics
PATCH /static/*
POST /admin/audit/command
POST /admin/access/command
POST /admin/personal-settings/command
POST /admin/product-settings/command
POST /admin/publications/command
POST /admin/groups/search
POST /admin/principals/search
POST /admin/queries/command
POST /admin/service-accounts/command
POST /admin/storage/select-table
POST /auth/desktop/disconnect
POST /auth/desktop/redeem
POST /auth/local/login
POST /auth/local/password
POST /auth/logout
POST /chat/turns
POST /chats/turns
POST /candidates/{candidate}/workspaces/{workspace}/commands/{command}
POST /workspaces/{workspace}/catalog/appearance
POST /catalog/search
POST /connections/search
POST /data/command
POST /device
POST /metrics
POST /oauth/register
POST /oauth/device/code
POST /oauth/revoke
POST /oauth/token
PUT /profile/avatar
PUT /admin/product-logo
POST /public/dashboards/{publicId}/commands/clear-selection
POST /public/dashboards/{publicId}/commands/filter
POST /public/dashboards/{publicId}/commands/filter-options
POST /public/dashboards/{publicId}/commands/navigate
POST /public/dashboards/{publicId}/commands/select
POST /public/dashboards/{publicId}/commands/spatial-select
POST /public/dashboards/{publicId}/commands/visual-window
POST /static/*
POST /workspaces/{workspace}/access/remove
POST /workspaces/{workspace}/access/upsert
POST /workspaces/{workspace}/assets/{asset}/access/remove
POST /workspaces/{workspace}/assets/{asset}/access/upsert
POST /workspaces/{workspace}/assets/{asset}/refresh
POST /workspaces/{workspace}/commands/clear-selection
POST /workspaces/{workspace}/commands/filter
POST /workspaces/{workspace}/commands/filter-options
POST /workspaces/{workspace}/commands/navigate
POST /workspaces/{workspace}/commands/select
POST /workspaces/{workspace}/commands/spatial-select
POST /workspaces/{workspace}/commands/visual-window
POST /workspaces/search
POST /workspaces/{workspace}/search
PUT /metrics
PUT /static/*
TRACE /metrics
TRACE /static/*
`
