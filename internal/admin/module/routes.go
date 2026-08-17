package module

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/admin/productsettings"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	Authenticate         func(http.Handler) http.Handler
	RequirePlatformAdmin func(http.Handler) http.Handler
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil {
		return
	}
	h := m.handler
	r.Get("/admin", authenticated(guard, h.AdminRoot))
	r.Get("/admin/profile", authenticated(guard, h.Profile))
	r.Get("/admin/security", authenticated(guard, h.Security))
	r.Get("/admin/api-tokens", authenticated(guard, h.APITokens))
	r.Post("/admin/personal-settings/command", authenticated(guard, h.PersonalSettingsCommand))
	r.Get("/admin/general", platformAdmin(guard, h.General))
	r.Get("/admin/principals", platformAdmin(guard, h.Principals))
	r.Post("/admin/principals/search", platformAdmin(guard, h.PrincipalsSearch))
	r.Get("/admin/principals/{principal}", platformAdmin(guard, h.PrincipalDetail))
	r.Get("/admin/groups", platformAdmin(guard, h.Groups))
	r.Post("/admin/groups/search", platformAdmin(guard, h.GroupsSearch))
	r.Get("/admin/groups/{group}", platformAdmin(guard, h.GroupDetail))
	r.Post("/admin/access/command", platformAdmin(guard, h.AccessAdministrationCommand))
	r.Get("/admin/service-accounts", platformAdmin(guard, h.ServiceAccounts))
	r.Post("/admin/service-accounts/command", platformAdmin(guard, h.ServiceAccountCommand))
	r.Get("/admin/authentication", platformAdmin(guard, h.Authentication))
	r.Post("/admin/product-settings/command", platformAdmin(guard, h.ProductSettingsCommand))
	r.Put("/admin/product-logo", platformAdmin(guard, func(w http.ResponseWriter, request *http.Request) {
		binding, err := m.productCommands.Binding(productsettings.CommandUploadLogo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := uicommand.VerifyClaim(uicommand.OperationClaims(request), binding.OperationID()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		started, err := m.productCommands.BeginInvocation(request.Context(), productsettings.CommandUploadLogo, productsettings.CommandInvocation{
			ConcurrencyToken: strings.TrimSpace(request.Header.Get("If-Match")),
			RequestID:        strings.TrimSpace(request.Header.Get("X-Request-ID")), CorrelationID: strings.TrimSpace(request.Header.Get("X-Correlation-ID")),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m.UploadProductLogo(w, request.WithContext(started))
	}))
	r.Get("/product/logo/{digest}", authenticated(guard, m.GetProductLogo))
	r.Get("/admin/agent", platformAdmin(guard, h.Agent))
	r.Get("/admin/storage", platformAdmin(guard, h.Storage))
	r.Get("/admin/storage/tables/{schema}/{table}", platformAdmin(guard, h.StorageTable))
	r.Get("/admin/queries", platformAdmin(guard, h.Queries))
	r.Post("/admin/queries/command", platformAdmin(guard, h.QueryCommand))
	r.Get("/admin/audit", platformAdmin(guard, h.Audit))
	r.Post("/admin/audit/command", platformAdmin(guard, h.AuditLogCommand))
	r.Get("/admin/system", platformAdmin(guard, h.System))
	r.Get("/admin/publications", platformAdmin(guard, h.Publications))
	r.Post("/admin/publications/command", platformAdmin(guard, h.PublicationCommand))
}

func authenticated(guard RouteGuard, next http.HandlerFunc) http.HandlerFunc {
	if guard.Authenticate == nil {
		return unavailable
	}
	return guard.Authenticate(http.HandlerFunc(next)).ServeHTTP
}

func platformAdmin(guard RouteGuard, next http.HandlerFunc) http.HandlerFunc {
	if guard.RequirePlatformAdmin == nil {
		return unavailable
	}
	return guard.RequirePlatformAdmin(http.HandlerFunc(next)).ServeHTTP
}

func unavailable(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
}
