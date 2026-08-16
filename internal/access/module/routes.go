package module

import (
	"fmt"
	"net/http"
	"strings"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
)

// CSRFMiddleware preserves browser-session CSRF protection for state-changing
// form endpoints. Bearer-authenticated requests are exempted by Auth itself.
func (m *Module) CSRFMiddleware(next http.Handler) http.Handler {
	if m == nil || m.auth == nil {
		return next
	}
	return m.auth.CSRFMiddleware(next)
}

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
	deviceAuthorization := m.Authenticate(http.HandlerFunc(m.DeviceAuthorizationPage))
	r.Method(http.MethodGet, "/device", deviceAuthorization)
	r.Method(http.MethodPost, "/device", deviceAuthorization)
	r.Method(http.MethodPost, "/auth/logout", m.Authenticate(http.HandlerFunc(m.Logout)))
	r.Method(http.MethodPost, "/auth/local/password", m.Authenticate(http.HandlerFunc(m.LocalPassword)))
	r.Method(http.MethodPut, "/profile/avatar", m.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started, _, err := accessgen.BeginGenUploadCurrentAvatarCommand(request.Context(), accessgen.GenUploadCurrentAvatarCommandInvocation{
			Surface: apigencommand.SurfaceUI, RequestID: strings.TrimSpace(request.Header.Get("X-Request-ID")),
			CorrelationID: strings.TrimSpace(request.Header.Get("X-Correlation-ID")),
		})
		if claimErr := uicommand.VerifyClaim(uicommand.OperationClaims(request), accessgen.GenUIActionUploadCurrentAvatar().OperationID()); claimErr != nil {
			http.Error(w, claimErr.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		request = request.WithContext(started)
		m.handler.UploadCurrentAvatar(w, request, request.Header.Get("Content-Type"))
	})))
	r.Method(http.MethodDelete, "/profile/avatar", m.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(request), accessgen.GenUIActionDeleteCurrentAvatar().OperationID()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		started, _, err := accessgen.BeginGenDeleteCurrentAvatarCommand(request.Context(), accessgen.GenDeleteCurrentAvatarCommandInvocation{
			Surface: apigencommand.SurfaceUI, RequestID: strings.TrimSpace(request.Header.Get("X-Request-ID")),
			CorrelationID: strings.TrimSpace(request.Header.Get("X-Correlation-ID")),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.handler.DeleteCurrentAvatar(w, request.WithContext(started))
	})))
	r.Method(http.MethodGet, "/profile/avatars/{principal}/{digest}", m.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
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
		authorize := m.Authenticate(http.HandlerFunc(m.MCPOAuthAuthorize))
		r.Method(http.MethodGet, "/oauth/authorize", m.CSRFMiddleware(authorize))
		r.Method(http.MethodPost, "/oauth/authorize", m.CSRFMiddleware(authorize))
	}
}
