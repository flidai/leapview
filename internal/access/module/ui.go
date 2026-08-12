package module

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/gorilla/csrf"
)

type AdminNavigationPrivileges struct {
	ManagePlatform     bool
	ManageGrants       bool
	ManageWorkspace    bool
	ManagePublications bool
	ViewAudit          bool
	ViewConnections    bool
}

func (m *Module) CSRFToken(r *http.Request) string {
	if m == nil || m.auth == nil {
		return ""
	}
	return csrf.Token(r)
}

func (m *Module) CurrentRoleLabel(r *http.Request) string {
	if m == nil || m.auth == nil {
		return "Local"
	}
	principal, ok := m.auth.Principal(r)
	if !ok {
		return "Signed out"
	}
	if principal.DevBypass {
		return "Platform admin"
	}
	return "Platform access"
}

func (m *Module) CurrentTheme(r *http.Request) access.ThemeMode {
	if m == nil || r == nil {
		return access.ThemeSystem
	}
	principal, ok := m.CurrentPrincipal(r)
	if !ok || strings.TrimSpace(principal.ID) == "" {
		return access.ThemeSystem
	}
	reader, ok := any(m.repositoryValue()).(access.PrincipalPreferencesReader)
	if !ok || reader == nil {
		return access.ThemeSystem
	}
	preferences, err := reader.PrincipalPreferences(r.Context(), principal.ID)
	if err != nil {
		return access.ThemeSystem
	}
	if _, valid := access.ParseThemeMode(string(preferences.Theme)); !valid {
		return access.ThemeSystem
	}
	return preferences.Theme
}

func (m *Module) AdminNavigationPrivileges(r *http.Request) AdminNavigationPrivileges {
	all := AdminNavigationPrivileges{
		ManagePlatform: true, ManageGrants: true, ManageWorkspace: true,
		ManagePublications: true, ViewAudit: true, ViewConnections: true,
	}
	if m == nil || m.auth == nil {
		return all
	}
	principal, ok := m.auth.Principal(r)
	if !ok {
		return AdminNavigationPrivileges{}
	}
	if principal.DevBypass {
		return all
	}
	repository := m.repositoryValue()
	if repository == nil {
		return AdminNavigationPrivileges{}
	}
	platform, err := repository.Authorize(r.Context(), principal.ID, access.PrivilegeManagePlatform, access.PlatformObject())
	if err != nil {
		return AdminNavigationPrivileges{}
	}
	allowed := func(privilege access.Privilege) bool {
		ok, err := m.authorizeAnyWorkspace(r.Context(), principal.ID, nil, privilege)
		return err == nil && ok
	}
	return AdminNavigationPrivileges{
		ManagePlatform: platform.Allowed, ManageGrants: allowed(access.PrivilegeManageGrants),
		ManageWorkspace: allowed(access.PrivilegeManageWorkspace), ManagePublications: allowed(access.PrivilegeManagePublications),
		ViewAudit: allowed(access.PrivilegeViewAudit), ViewConnections: allowed(access.PrivilegeViewItem),
	}
}
