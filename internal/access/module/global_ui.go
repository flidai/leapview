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
	if capabilities, err := m.RequestEffectiveCapabilities(r.Context(), r, principal.ID); err == nil {
		for _, capability := range capabilities {
			if capability == access.CapabilityProjectAdmin {
				return "Platform admin"
			}
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
	capabilities, err := m.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
	if err != nil {
		return AdminNavigationAccess{}
	}
	admin := false
	for _, capability := range capabilities {
		if capability == access.CapabilityProjectAdmin {
			admin = true
			break
		}
	}
	if !admin {
		return AdminNavigationAccess{}
	}
	return allAdminNavigationAccess()
}

func allAdminNavigationAccess() AdminNavigationAccess {
	return AdminNavigationAccess{
		PlatformAdmin: true, ManageIdentity: true, ViewAudit: true,
	}
}
