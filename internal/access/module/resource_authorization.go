package module

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (a *APIGenAuthorizer) protectResources(operationID string, capability access.Capability, resolve APIGenResourceResolver, next http.Handler) http.Handler {
	return a.module.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := a.module.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		projectID := a.runtime.ProjectID()
		if err := projectID.Validate(); err != nil {
			a.module.logger.WarnContext(r.Context(), "generated API active project identity is unavailable", "operation", operationID, "error", err)
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		resources := resolve(r, projectID)
		if len(resources) == 0 {
			http.NotFound(w, r)
			return
		}
		for _, resource := range resources {
			if err := resource.Validate(); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		if principal.DevBypass {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := a.authorizeResources(r.Context(), principal.ID, projectID, resources, capability)
		if err != nil {
			slog.Default().WarnContext(r.Context(), "generated API resource authorization failed", "capability", capability, "project", projectID, "error", err)
			if errors.Is(err, errAPIGenResourceNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		credential, hasCredential := APICredentialFromContext(r.Context())
		typedToken := hasCredential && strings.TrimSpace(credential.Token.ID) != "" &&
			(credential.Token.PermissionProfile != "" || credential.Token.Permissions != nil)
		if typedToken {
			// Typed credentials are evaluated from the generated operation's
			// exact action/resolver contract. An operation without a migrated
			// typed contract is denied until its product resolver exists; no
			// legacy capability may be inferred as a typed action.
			var pairs []access.PermissionPair
			var pairErr error
			if a.typed == nil {
				pairErr = errors.New("typed operation requirement service is unavailable")
			} else {
				pairs, pairErr = a.typed.ResolvePairs(operationID, projectID, resources...)
			}
			if pairErr != nil {
				for _, resource := range resources {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resource, capability)
				}
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			for _, pair := range pairs {
				if !access.PermissionSetAllows(credential.Token.Permissions, pair) {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
			}
		} else {
			effective, err := a.module.RequestEffectiveCapabilities(r.Context(), r, principal.ID)
			if err != nil {
				slog.Default().WarnContext(r.Context(), "generated API effective capability resolution failed", "capability", capability, "project", projectID, "error", err)
				if errors.Is(err, access.ErrForbidden) {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if !containsCapability(effective, capability) {
				a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}

func (a *APIGenAuthorizer) recordResourceAuthorizationDenial(r *http.Request, operationID string, projectID projectgraph.ResourceID, principalID string, resource access.ResourceRef, capability access.Capability) {
	if a == nil || a.module == nil || a.module.repository == nil || r == nil {
		return
	}
	repository, err := a.module.repository()
	if err != nil || repository == nil {
		a.module.logger.WarnContext(r.Context(), "generated API authorization denial audit repository unavailable", "operation", operationID, "error", err)
		return
	}
	input := authAuditInput(r, "authorization.denied", principalID, string(resource.Kind()), resource.ID().String(), capability, "denied", map[string]any{"operationId": operationID})
	input.ProjectID = projectID.String()
	if err := access.PersistAuditEvent(r.Context(), repository, input); err != nil {
		a.module.logger.WarnContext(r.Context(), "generated API authorization denial audit failed", "operation", operationID, "error", err)
	}
}

func containsCapability(capabilities []access.Capability, expected access.Capability) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}
