package module

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

func (m *Module) MountLoginPage(r chi.Router) {
	if m != nil {
		r.Get("/login", m.Login)
	}
}

func (m *Module) MountSCIM(r chi.Router, bearerToken string) error {
	if m == nil || strings.TrimSpace(bearerToken) == "" {
		return nil
	}
	handler, err := m.SCIMHandler(bearerToken)
	if err != nil {
		return fmt.Errorf("build SCIM handler: %w", err)
	}
	r.Handle("/scim/*", http.StripPrefix("/scim", handler))
	return nil
}

func (m *Module) MountAuthenticatedBrowser(r chi.Router) {
	if m == nil {
		return
	}
	deviceAuthorization := http.Handler(http.HandlerFunc(m.DeviceAuthorizationPage))
	if m.auth != nil {
		deviceAuthorization = m.auth.Middleware("", deviceAuthorization)
	}
	r.Method(http.MethodGet, "/device", deviceAuthorization)
	r.Method(http.MethodPost, "/device", deviceAuthorization)
	r.Post("/auth/logout", m.Logout)
	r.Post("/auth/local/password", m.LocalPassword)
	r.Method(http.MethodPut, "/profile/avatar", m.ProtectHandler("", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		m.handler.UploadCurrentAvatar(w, request, request.Header.Get("Content-Type"))
	})))
	r.Method(http.MethodDelete, "/profile/avatar", m.ProtectHandler("", http.HandlerFunc(m.handler.DeleteCurrentAvatar)))
	r.Method(http.MethodGet, "/profile/avatars/{principal}/{digest}", m.ProtectHandler("", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		m.handler.GetPrincipalAvatar(w, request, chi.URLParam(request, "principal"), chi.URLParam(request, "digest"))
	})))
}

func (m *Module) MountLocalLogin(r chi.Router) {
	if m != nil {
		r.Post("/auth/local/login", m.LocalLogin)
	}
}

func (m *Module) MountOAuthEndpoints(r chi.Router) {
	if m == nil {
		return
	}
	r.Get("/auth/{provider}", m.Begin)
	r.Get("/auth/{provider}/callback", m.Callback)
	r.Post("/oauth/device/code", m.AuthoringDeviceAuthorization)
	r.Post("/oauth/token", m.OAuthToken)
	r.Post("/oauth/register", m.MCPOAuthRegister)
	r.Post("/oauth/revoke", m.OAuthRevoke)
	m.MountDesktopAuth(r)
}

func (m *Module) MountOAuthMetadata(r chi.Router) {
	if m == nil {
		return
	}
	r.Get("/.well-known/oauth-protected-resource", m.MCPProtectedResourceMetadata)
	r.Get("/.well-known/oauth-protected-resource/mcp", m.MCPProtectedResourceMetadata)
	r.Get("/.well-known/oauth-authorization-server", m.MCPAuthorizationServerMetadata)
	if m.auth != nil {
		authorize := m.auth.Middleware("", http.HandlerFunc(m.MCPOAuthAuthorize))
		r.Method(http.MethodGet, "/oauth/authorize", m.CSRFMiddleware(authorize))
		r.Method(http.MethodPost, "/oauth/authorize", m.CSRFMiddleware(authorize))
	}
}
