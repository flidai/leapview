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
			// Typed credentials are evaluated as exact action/resource pairs. The
			// generated operation ID is part of this mapping: a generic
			// RESOURCE_READ capability must not be inferred as dashboard.read,
			// and dashboard.read must not be reused for mutations or authoring.
			for _, resource := range resources {
				action, mapped := apiGenTypedAction(operationID, resource)
				if !mapped {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resource, capability)
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
				pair, pairErr := access.NewExactPermissionPair(action, projectID, resource)
				if pairErr != nil || !access.PermissionSetAllows(credential.Token.Permissions, pair) {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resource, capability)
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

// apiGenTypedAction is the explicit operation-to-action mapping for the
// generated dashboard viewer surface. It deliberately covers only published
// dashboard reads. Other operations must add their own typed contract before
// a typed credential can reach them; falling back to a legacy capability here
// would let dashboard.read authorize mutation, authoring, or another resource
// family.
func apiGenTypedAction(operationID string, resource access.ResourceRef) (access.Action, bool) {
	if resource.Kind() != projectgraph.KindDashboard {
		return "", false
	}
	switch operationID {
	case "getDashboard", "getDashboardPage", "getDashboardFilter", "listDashboardFilterValues",
		"queryDashboardPage", "getDashboardVisual", "queryDashboardVisualData":
		return access.ActionDashboardRead, true
	default:
		return "", false
	}
}
