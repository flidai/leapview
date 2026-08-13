package module

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/admin/productsettings"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
)

type RouteGuard struct {
	Protect             func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectGlobal       func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectPlatform     func(access.Privilege, http.HandlerFunc) http.HandlerFunc
	ProtectAnyWorkspace func(access.Privilege, http.HandlerFunc) http.HandlerFunc
}

func (m *Module) MountAuthenticated(r chi.Router, guard RouteGuard) {
	if m == nil {
		return
	}
	h := m.handler
	r.Get("/admin", guard.Protect("", h.AdminRoot))
	r.Get("/admin/profile", guard.Protect("", h.Profile))
	r.Get("/admin/security", guard.Protect("", h.Security))
	r.Get("/admin/api-tokens", guard.Protect("", h.APITokens))
	r.Post("/admin/personal-settings/command", guard.Protect("", h.PersonalSettingsCommand))
	r.Get("/admin/general", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.General))
	r.Get("/admin/workspaces", guard.ProtectGlobal(access.PrivilegeManageWorkspace, h.Workspaces))
	r.Get("/admin/principals", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Principals))
	r.Post("/admin/principals/search", guard.ProtectGlobal(access.PrivilegeManageGrants, h.PrincipalsSearch))
	r.Get("/admin/principals/{principal}", guard.ProtectGlobal(access.PrivilegeManageGrants, h.PrincipalDetail))
	r.Get("/admin/groups", guard.ProtectGlobal(access.PrivilegeManageGrants, h.Groups))
	r.Post("/admin/groups/search", guard.ProtectGlobal(access.PrivilegeManageGrants, h.GroupsSearch))
	r.Get("/admin/groups/{group}", guard.ProtectGlobal(access.PrivilegeManageGrants, h.GroupDetail))
	r.Post("/admin/access/command", guard.ProtectGlobal(access.PrivilegeManageGrants, h.AccessAdministrationCommand))
	r.Get("/admin/service-accounts", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.ServiceAccounts))
	r.Post("/admin/service-accounts/command", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.ServiceAccountCommand))
	r.Get("/admin/authentication", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.Authentication))
	r.Post("/admin/product-settings/command", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.ProductSettingsCommand))
	r.Put("/admin/product-logo", guard.ProtectPlatform(access.PrivilegeManagePlatform, func(w http.ResponseWriter, request *http.Request) {
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
	r.Get("/product/logo/{digest}", guard.Protect("", m.GetProductLogo))
	r.Get("/admin/agent", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.Agent))
	r.Get("/admin/storage", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.Storage))
	r.Get("/admin/storage-v2", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.StorageV2))
	r.Get("/admin/storage-v2/tables/{schema}/{table}", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.StorageV2Table))
	r.Post("/admin/storage/select-table", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.StorageTableSelect))
	r.Get("/admin/queries", guard.ProtectGlobal(access.PrivilegeViewAudit, h.Queries))
	r.Post("/admin/queries/command", guard.ProtectGlobal(access.PrivilegeViewAudit, h.QueryCommand))
	r.Get("/admin/audit", guard.ProtectGlobal(access.PrivilegeViewAudit, h.Audit))
	r.Post("/admin/audit/command", guard.ProtectGlobal(access.PrivilegeViewAudit, h.AuditLogCommand))
	r.Get("/admin/system", guard.ProtectPlatform(access.PrivilegeManagePlatform, h.System))
	r.Get("/admin/publications", guard.ProtectAnyWorkspace(access.PrivilegeManagePublications, h.Publications))
	r.Post("/admin/publications/command", guard.ProtectAnyWorkspace(access.PrivilegeManagePublications, h.PublicationCommand))
}
