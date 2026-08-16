package app

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
)

// authorizeProjectResources evaluates canonical resource grants against the
// exact leased generation. Group subjects are resolved once per request and
// any matching subject is sufficient for each resource.
func authorizeProjectResources(
	ctx context.Context,
	accessModule *accessmodule.Module,
	runtimeHost *runtimehostmodule.Module,
	principalID string,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	capability access.Capability,
) (bool, error) {
	if accessModule == nil || runtimeHost == nil {
		return false, fmt.Errorf("authorization modules are required")
	}
	if err := projectID.Validate(); err != nil {
		return false, err
	}
	for _, resource := range resources {
		if err := resource.Validate(); err != nil {
			return false, err
		}
	}
	lease, err := runtimeHost.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer lease.Release()
	if lease.Identity().ProjectID != projectID {
		return false, fmt.Errorf("runtime project %q does not match requested project %q", lease.Identity().ProjectID, projectID)
	}
	authorizedLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return false, fmt.Errorf("active runtime lease does not expose authorization snapshot")
	}
	subjects, err := accessModule.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	for _, resource := range resources {
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

// protectProjectResources authorizes a browser request against the immutable
// graph-bound snapshot carried by the leased serving generation. Resource IDs
// and capabilities are resolved before the handler runs; no workspace/object
// selector is accepted at this boundary.
func protectProjectResources(
	accessModule *accessmodule.Module,
	runtimeHost *runtimehostmodule.Module,
	capability access.Capability,
	resolve func(*http.Request, projectgraph.ResourceID) []access.ResourceRef,
	next http.HandlerFunc,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if accessModule == nil || runtimeHost == nil || resolve == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		principal, ok := accessModule.CurrentPrincipal(r)
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if principal.DevBypass {
			next(w, r)
			return
		}
		projectID := runtimeHost.ProjectID()
		if err := projectID.Validate(); err != nil {
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
		allowed, err := authorizeProjectResources(r.Context(), accessModule, runtimeHost, principal.ID, projectID, resources, capability)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		if !allowed {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
