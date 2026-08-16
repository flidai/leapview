package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	manageddata "github.com/flidai/leapview/internal/manageddata"
	manageddatacontrol "github.com/flidai/leapview/internal/manageddata/control"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/go-chi/chi/v5"
)

type canonicalRuntimeHost interface {
	ProjectID() projectgraph.ResourceID
	Acquire(context.Context) (runtimehost.Lease, error)
}

type canonicalAccessModule interface {
	Authenticate(http.Handler) http.Handler
	CurrentPrincipal(*http.Request) (accessmodule.Principal, bool)
	AuthorizationSubjects(context.Context, string) ([]access.SubjectRef, error)
}

// authorizeProjectResources evaluates canonical resource grants against the
// exact leased generation. Group subjects are resolved once per request and
// any matching subject is sufficient for each resource.
func authorizeProjectResources(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
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
// and capabilities are resolved before the handler runs; no alternate
// selector is accepted at this boundary.
func protectProjectResources(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	capability access.Capability,
	resolve func(*http.Request, projectgraph.ResourceID) []access.ResourceRef,
	next http.HandlerFunc,
) http.HandlerFunc {
	if accessModule == nil || runtimeHost == nil || resolve == nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		}
	}
	return accessModule.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := accessModule.CurrentPrincipal(r)
		if !ok {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
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
		if principal.DevBypass {
			next(w, r)
			return
		}
		allowed, err := authorizeProjectResources(r.Context(), accessModule, runtimeHost, principal.ID, projectID, resources, capability)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
			return
		}
		next(w, r)
	})).ServeHTTP
}

// activeProjectResource binds project-level actions to the active serving
// generation's exact project identity.
func activeProjectResource(_ *http.Request, projectID projectgraph.ResourceID) []access.ResourceRef {
	resource, err := access.NewResourceRef(projectID, projectgraph.KindProject)
	if err != nil {
		return nil
	}
	return []access.ResourceRef{resource}
}

// protectManagedDataTransport authorizes each opaque resumable-upload request
// against the exact connection resource captured by its upload session. The
// transport token itself is not a project selector; it is resolved by the
// managed-data module before the generation-bound capability decision.
func protectManagedDataTransport(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	managedData interface {
		ResolveTusTarget(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error)
	},
	next http.Handler,
) http.Handler {
	if accessModule == nil || runtimeHost == nil || managedData == nil || next == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		})
	}
	return accessModule.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodHead && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}
		uploadID := chi.URLParam(r, "*")
		if !validTusTransportID(uploadID) {
			http.NotFound(w, r)
			return
		}
		projectID, connectionID, err := managedData.ResolveTusTarget(r.Context(), uploadID)
		if err != nil {
			if errors.Is(err, manageddatacontrol.ErrNotFound) || errors.Is(err, manageddata.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		activeProjectID := runtimeHost.ProjectID()
		if err := activeProjectID.Validate(); err != nil || projectID != activeProjectID {
			http.NotFound(w, r)
			return
		}
		resource, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		principal, ok := accessModule.CurrentPrincipal(r)
		if !ok || strings.TrimSpace(principal.ID) == "" {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if principal.DevBypass {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := authorizeProjectResources(r.Context(), accessModule, runtimeHost, principal.ID, activeProjectID, []access.ResourceRef{resource}, access.CapabilityResourceEdit)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !allowed {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func validTusTransportID(value string) bool {
	if value != strings.TrimSpace(value) || len(value) != len("tus_")+hex.EncodedLen(32) || !strings.HasPrefix(value, "tus_") {
		return false
	}
	for _, char := range value[len("tus_"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
