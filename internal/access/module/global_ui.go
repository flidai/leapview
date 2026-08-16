package module

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/gorilla/csrf"
)

// AdminNavigationAccess is the shell-facing global administration
// capability set. Project authorization is intentionally not represented here.
type AdminNavigationAccess struct {
	PlatformAdmin  bool
	ManageIdentity bool
	ViewAudit      bool
}

func (m *Module) CSRFToken(r *http.Request) string {
	if m == nil || m.auth == nil || r == nil {
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
	checker, ok := m.repositoryValue().(access.PlatformRoleReader)
	if ok {
		admin, err := checker.IsPlatformAdmin(r.Context(), principal.ID)
		if err == nil && admin {
			return "Platform admin"
		}
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

func (m *Module) AdminNavigationAccess(r *http.Request) AdminNavigationAccess {
	if m == nil || m.auth == nil {
		return AdminNavigationAccess{}
	}
	principal, ok := m.auth.Principal(r)
	if !ok {
		return AdminNavigationAccess{}
	}
	if principal.DevBypass {
		return allAdminNavigationAccess()
	}
	repository := m.repositoryValue()
	checker, ok := repository.(access.PlatformRoleReader)
	if !ok {
		return AdminNavigationAccess{}
	}
	admin, err := checker.IsPlatformAdmin(r.Context(), principal.ID)
	if err != nil || !admin {
		return AdminNavigationAccess{}
	}
	return allAdminNavigationAccess()
}

func allAdminNavigationAccess() AdminNavigationAccess {
	return AdminNavigationAccess{
		PlatformAdmin: true, ManageIdentity: true, ViewAudit: true,
	}
}
