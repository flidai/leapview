package module

import (
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
)

// protectInstance applies the platform role baseline to an instance-scoped
// operation and then attenuates it with the generated typed pair when the
// caller supplied a typed API token. Instance authority is durable and
// instance-audience based; it is never read from a project snapshot.
func (a *APIGenAuthorizer) protectInstance(operationID string, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := a.module.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if principal.DevBypass {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := a.module.IsPlatformAdmin(r.Context(), principal.ID)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		credential, hasCredential := a.module.requestCredential(r)
		if !hasCredential {
			next.ServeHTTP(w, r)
			return
		}
		// Authoring credentials and legacy capability-only API tokens cannot
		// invoke an operation that has migrated to instance typed authority.
		if credential.Authoring != nil || strings.TrimSpace(credential.Token.ID) == "" {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		instanceID := a.instance(r)
		pairs, err := a.typed.ResolveInstancePairs(operationID, instanceID)
		if err != nil || !permissionPairsAllowAll(credential.Token.Permissions, pairs) {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
