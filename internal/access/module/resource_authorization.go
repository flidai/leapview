package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
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
		snapshot, subjects, release, err := a.authorizationSnapshot(r.Context(), principal.ID, projectID)
		if release != nil {
			defer release()
		}
		if err != nil {
			slog.Default().WarnContext(r.Context(), "generated API resource authorization failed", "capability", capability, "project", projectID, "error", err)
			if errors.Is(err, errAPIGenResourceNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		credential, hasCredential := APICredentialFromContext(r.Context())
		typedToken := hasCredential && strings.TrimSpace(credential.Token.ID) != "" &&
			(credential.Token.PermissionProfile != "" || credential.Token.Permissions != nil)
		requirement, typedOperation := a.typedRequirement(operationID)
		if typedOperation {
			// A migrated operation is evaluated against typed principal/group
			// authority first. Legacy capability rows are intentionally ignored;
			// this permits a typed-only grant to work and prevents a typed token
			// from silently falling back to a broad capability.
			pairs, pairErr := requirement.ResolvePairs(projectID, resources...)
			if pairErr != nil || !snapshotAllowsTyped(snapshot, subjects, pairs) {
				for _, resource := range resources {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resource, capability)
				}
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
			if typedToken {
				// Authentication normally binds these identities before this
				// middleware runs, but keep the typed operation boundary
				// self-contained for injected credentials and alternate transports.
				// A token's pair set is an attenuation ceiling for the current
				// principal; it is never a substitute for that principal's
				// immutable typed assignment.
				if credential.Principal.ID != principal.ID || credential.Token.PrincipalID != principal.ID ||
					credential.Token.PermissionProfile != access.PermissionCatalogProfile || pairErr != nil || !permissionPairsAllowAll(credential.Token.Permissions, pairs) {
					a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
					http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
					return
				}
			} else if hasCredential && strings.TrimSpace(credential.Token.ID) != "" {
				// A legacy bearer token cannot invoke a migrated typed operation.
				a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}
		} else if typedToken {
			// Typed credentials reaching an unmigrated operation must fail closed;
			// no action/resolver pair exists from which to derive an exact target.
			a.recordResourceAuthorizationDenial(r, operationID, projectID, principal.ID, resources[0], capability)
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		} else {
			allowed, legacyErr := snapshotAllowsLegacy(snapshot, subjects, resources, capability)
			if legacyErr != nil {
				slog.Default().WarnContext(r.Context(), "generated API legacy resource authorization failed", "capability", capability, "project", projectID, "error", legacyErr)
				if errors.Is(legacyErr, access.ErrResourceNotFound) {
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

func (a *APIGenAuthorizer) typedRequirement(operationID string) (access.TypedOperationRequirement, bool) {
	if a == nil || a.typed == nil {
		return access.TypedOperationRequirement{}, false
	}
	requirement, ok := a.typed.Requirement(operationID)
	return requirement, ok
}

func permissionPairsAllowAll(granted, requested []access.PermissionPair) bool {
	for _, pair := range requested {
		if !access.PermissionSetAllows(granted, pair) {
			return false
		}
	}
	return true
}

func snapshotAllowsTyped(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, requested []access.PermissionPair) bool {
	if len(requested) == 0 {
		return false
	}
	// EffectiveTypedPermissions unions only typed assignments for the complete
	// principal/group subject set. The requested list includes the primary pair
	// and its catalog prerequisites, so every dependency remains independently
	// required while authority may be split across principal and group grants.
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	return err == nil && permissionPairsAllowAll(granted, requested)
}

func snapshotAllowsLegacy(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, resources []access.ResourceRef, capability access.Capability) (bool, error) {
	for _, resource := range resources {
		if resource.Kind() == projectgraph.KindProjectNamespace && !access.SupportsCapability(resource.Kind(), capability) {
			if !accesssnapshot.RoleAllowsCapability(snapshot, subjects, capability) {
				return false, nil
			}
			continue
		}
		allowed := false
		for _, subject := range subjects {
			candidate, err := snapshot.Allows(subject, resource, capability)
			if err != nil {
				return false, err
			}
			if candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}

// authorizationSnapshot acquires one immutable lease and resolves the full
// principal/group subject set. Keeping both values together prevents typed
// and legacy checks from observing different serving generations.
func (a *APIGenAuthorizer) authorizationSnapshot(ctx context.Context, principalID string, projectID projectgraph.ResourceID) (accesssnapshot.AuthorizationSnapshot, []access.SubjectRef, func(), error) {
	lease, err := a.runtime.Acquire(ctx)
	if err != nil {
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, err
	}
	if lease == nil {
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, errors.New("runtime host returned a nil lease")
	}
	release := lease.Release
	if lease.Identity().ProjectID != projectID {
		release()
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, fmt.Errorf("runtime project %q does not match requested project %q", lease.Identity().ProjectID, projectID)
	}
	authorizedLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		release()
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, errors.New("active runtime lease does not expose authorization snapshot")
	}
	subjects, err := a.module.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		release()
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, err
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	if snapshot.Identity() != lease.Identity() {
		release()
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, errors.New("authorization snapshot identity does not match leased serving generation")
	}
	if err := snapshot.ValidateBound(); err != nil {
		release()
		return accesssnapshot.AuthorizationSnapshot{}, nil, nil, err
	}
	return snapshot, subjects, release, nil
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
