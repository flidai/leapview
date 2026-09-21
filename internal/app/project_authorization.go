package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	manageddata "github.com/flidai/leapview/internal/manageddata"
	manageddatacontrol "github.com/flidai/leapview/internal/manageddata/control"
	uitransport "github.com/flidai/leapview/internal/platform/web/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
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

type connectionAuthorization func(context.Context, string, string, string, access.Capability) (bool, error)

const managedDataTusMutationTimeout = 5 * time.Minute

func authoringDevelopmentBypass(ctx context.Context, principalID string) bool {
	principal, ok := accessmodule.PrincipalFromContext(ctx)
	return ok && principal.DevBypass && principal.ID == strings.TrimSpace(principalID)
}

func authorizeAuthoringResource(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resource access.ResourceRef,
	capability access.Capability,
) (bool, error) {
	if authoringDevelopmentBypass(ctx, principalID) {
		return true, nil
	}
	return authorizeProjectResources(ctx, accessModule, runtimeHost, principalID, projectID, []access.ResourceRef{resource}, capability)
}

func authorizeAuthoringProject(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	capability access.Capability,
) (bool, error) {
	if authoringDevelopmentBypass(ctx, principalID) {
		return true, nil
	}
	return authorizeProjectRole(ctx, accessModule, runtimeHost, principalID, projectID, capability)
}

// bootstrapAwareConnectionAuthorization permits managed-data handlers to
// consume the opaque request marker emitted by the APIGen staging guard. The
// guard has already bound the credential to the durable project claim, target,
// principal, and capability. This also covers a successor connection that is
// intentionally absent from the active predecessor snapshot.
func bootstrapAwareConnectionAuthorization(
	snapshot connectionAuthorization,
) connectionAuthorization {
	return func(ctx context.Context, principalID, projectID, connectionID string, capability access.Capability) (bool, error) {
		if marker, ok := accessmodule.ManagedDataStagingAuthorizationFromContext(ctx); ok && marker.PrincipalID == strings.TrimSpace(principalID) && marker.ProjectID.String() == strings.TrimSpace(projectID) && marker.ConnectionID.String() == strings.TrimSpace(connectionID) && marker.Capability == capability {
			return true, nil
		}
		if snapshot == nil {
			return false, fmt.Errorf("active authorization snapshot is unavailable")
		}
		return snapshot(ctx, principalID, projectID, connectionID, capability)
	}
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
	return authorizeProjectResourcesWithCapability(ctx, accessModule, runtimeHost, principalID, projectID, resources, false, func(access.ResourceRef) access.Capability {
		return capability
	})
}

func authorizeDeliveryProjectResources(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	capability access.Capability,
) (bool, error) {
	return authorizeProjectResourcesWithCapability(ctx, accessModule, runtimeHost, principalID, projectID, resources, true, func(resource access.ResourceRef) access.Capability {
		return deliveryResourceCapability(resource, capability)
	})
}

func authorizeProjectResourcesWithCapability(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	_ bool,
	capabilityFor func(access.ResourceRef) access.Capability,
) (bool, error) {
	if accessModule == nil || runtimeHost == nil {
		return false, fmt.Errorf("authorization modules are required")
	}
	if capabilityFor == nil {
		return false, fmt.Errorf("resource capability resolver is required")
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
	if lease == nil {
		return false, fmt.Errorf("runtime host returned a nil lease")
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
	if snapshot.Identity() != lease.Identity() {
		return false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
	}
	for _, resource := range resources {
		capability := capabilityFor(resource)
		// Project-scoped browser operations use resource capabilities from an
		// explicit project role bundle, just like APIGen. The project kind only
		// accepts PROJECT_ADMIN as a direct grant, so calling snapshot.Allows for
		// RESOURCE_EDIT/USE/MANAGE would otherwise reject a valid contributor,
		// editor, or data-deployer before the feature service can authorize it.
		if handled, roleAllowed := projectRootRoleDecision(snapshot, subjects, resource, capability); handled {
			if !roleAllowed {
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

// authorizeManagedDataConnection returns found=false only when the active,
// identity-bound snapshot proves that the exact connection is absent. In that
// case allowed reports whether a project role in the same snapshot grants the
// requested capability. Every lease, identity, snapshot, and subject failure
// remains an error so callers cannot mistake infrastructure failure for a
// staging opportunity.
func authorizeManagedDataConnection(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resource access.ResourceRef,
	capability access.Capability,
) (found, allowed bool, err error) {
	if accessModule == nil || runtimeHost == nil {
		return false, false, fmt.Errorf("authorization modules are required")
	}
	lease, err := runtimeHost.Acquire(ctx)
	if err != nil {
		return false, false, err
	}
	if lease == nil {
		return false, false, fmt.Errorf("runtime host returned a nil lease")
	}
	defer lease.Release()
	if lease.Identity().ProjectID != projectID {
		return false, false, fmt.Errorf("runtime project %q does not match requested project %q", lease.Identity().ProjectID, projectID)
	}
	authorizedLease, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return false, false, fmt.Errorf("active runtime lease does not expose authorization snapshot")
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	if snapshot.Identity() != lease.Identity() {
		return false, false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return false, false, err
	}
	subjects, err := accessModule.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		return false, false, err
	}
	graphResource, exists := snapshot.Project().Resource(resource.ID())
	if !exists || graphResource.Kind != resource.Kind() {
		return false, accesssnapshot.RoleAllowsCapability(snapshot, subjects, capability), nil
	}
	for _, subject := range subjects {
		candidate, candidateErr := snapshot.Allows(subject, resource, capability)
		if candidateErr != nil {
			return true, false, candidateErr
		}
		if candidate {
			return true, true, nil
		}
	}
	return true, false, nil
}

// authorizeProjectRole evaluates a project-wide role binding against the
// exact leased generation. Delivery publication uses this only when a plan's
// graph-impact evidence is empty; a direct grant on an unrelated resource
// must never widen that no-impact fallback.
func authorizeProjectRole(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	capability access.Capability,
) (bool, error) {
	if accessModule == nil || runtimeHost == nil {
		return false, fmt.Errorf("authorization modules are required")
	}
	if err := projectID.Validate(); err != nil {
		return false, err
	}
	lease, err := runtimeHost.Acquire(ctx)
	if err != nil {
		return false, err
	}
	if lease == nil {
		return false, fmt.Errorf("runtime host returned a nil lease")
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
	if snapshot.Identity() != lease.Identity() {
		return false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
	}
	return accesssnapshot.RoleAllowsCapability(snapshot, subjects, capability), nil
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
			uitransport.WriteBrowserAuthorizationError(w, r, http.StatusForbidden)
			return
		}
		next(w, r)
	})).ServeHTTP
}

type repositoryDashboardAuthorizer interface {
	AuthorizeDashboardEdit(context.Context, projectgraph.ResourceID, string, authoring.DashboardID) error
	AuthorizeDashboardManage(context.Context, projectgraph.ResourceID, string, authoring.DashboardID) error
}

// protectProjectAuthoringResource authenticates and authorizes builder routes
// against the durable dashboard lifecycle. This is intentionally separate
// from protectProjectResources, whose immutable graph snapshot cannot contain
// a freshly created private draft.
func protectProjectAuthoringResource(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	authorizer repositoryDashboardAuthorizer,
	capability access.Capability,
	next http.HandlerFunc,
) http.HandlerFunc {
	return protectProjectAuthoringResourceWithSelector(accessModule, runtimeHost, authorizer, capability, func(r *http.Request) string {
		return chi.URLParam(r, "dashboard")
	}, next)
}

func protectProjectAuthoringResourceWithSelector(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	authorizer repositoryDashboardAuthorizer,
	capability access.Capability,
	dashboardIDForRequest func(*http.Request) string,
	next http.HandlerFunc,
) http.HandlerFunc {
	if accessModule == nil || runtimeHost == nil || authorizer == nil || dashboardIDForRequest == nil || (capability != access.CapabilityResourceEdit && capability != access.CapabilityResourceManage) {
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
		dashboardID := strings.TrimSpace(dashboardIDForRequest(r))
		if err := authoring.ValidateDashboardID(authoring.DashboardID(dashboardID)); err != nil {
			http.NotFound(w, r)
			return
		}
		if principal.DevBypass {
			next(w, r)
			return
		}
		var err error
		if capability == access.CapabilityResourceManage {
			err = authorizer.AuthorizeDashboardManage(r.Context(), projectID, principal.ID, authoring.DashboardID(dashboardID))
		} else {
			err = authorizer.AuthorizeDashboardEdit(r.Context(), projectID, principal.ID, authoring.DashboardID(dashboardID))
		}
		if err != nil {
			if errors.Is(err, access.ErrForbidden) {
				uitransport.WriteBrowserAuthorizationError(w, r, http.StatusForbidden)
				return
			}
			if errors.Is(err, authoring.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		next(w, r)
	})).ServeHTTP
}

// activeProjectResource binds project-level actions to the active serving
// generation's exact project identity.
func activeProjectResource(_ *http.Request, projectID projectgraph.ResourceID) []access.ResourceRef {
	resource, err := access.NewResourceRef(projectID, projectgraph.KindProjectNamespace)
	if err != nil {
		return nil
	}
	return []access.ResourceRef{resource}
}

type candidatePreviewBootstrapAuthorization struct {
	ProjectID   projectgraph.ResourceID
	PrincipalID string
	Capability  access.Capability
}

type candidatePreviewBootstrapAuthorizationKey struct{}

func withCandidatePreviewBootstrapAuthorization(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	principalID string,
	capability access.Capability,
) context.Context {
	return context.WithValue(ctx, candidatePreviewBootstrapAuthorizationKey{}, candidatePreviewBootstrapAuthorization{
		ProjectID: projectID, PrincipalID: strings.TrimSpace(principalID), Capability: capability,
	})
}

func candidatePreviewBootstrapAuthorized(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	principalID string,
	capability access.Capability,
) bool {
	authorization, ok := ctx.Value(candidatePreviewBootstrapAuthorizationKey{}).(candidatePreviewBootstrapAuthorization)
	return ok && authorization.ProjectID == projectID &&
		authorization.PrincipalID == strings.TrimSpace(principalID) &&
		authorization.Capability == capability
}

// protectCandidateProjectResources keeps candidate preview owner checks on
// their durable candidate/runtime path while retaining the normal immutable
// project authorization whenever a canonical serving generation exists. A
// fresh target has no serving-state snapshot yet, so requiring one here would
// prevent candidatePreview/candidateDashboard from reaching
// ResolveOwnedCandidate and EnsureNativeCandidateRuntime. The candidate
// handlers remain responsible for proving owner identity and candidate scope.
// Fresh-target authority is request-local and preserves credential attenuation.
func protectCandidateProjectResources(
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
		if err := runtimeHost.ProjectID().Validate(); err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		active, err := candidatePreviewServingGenerationActive(r.Context(), runtimeHost)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		if !active {
			platformAdmin, ok := accessModule.(interface {
				RequestPlatformAdmin(context.Context, *http.Request, string) (bool, error)
			})
			if !ok {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			allowed, err := platformAdmin.RequestPlatformAdmin(r.Context(), r, principal.ID)
			if err != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if !allowed {
				uitransport.WriteBrowserAuthorizationError(w, r, http.StatusForbidden)
				return
			}
			marked := r.WithContext(withCandidatePreviewBootstrapAuthorization(
				r.Context(), runtimeHost.ProjectID(), principal.ID, capability,
			))
			next(w, marked)
			return
		}
		// Delegate the active path to the canonical guard so project role,
		// group, snapshot identity, and dev-bypass behavior remain identical
		// to every other authenticated project route.
		protectProjectResources(accessModule, runtimeHost, capability, resolve, next).ServeHTTP(w, r)
	})).ServeHTTP
}

type candidatePreviewServingStateReader interface {
	ActiveArtifact(context.Context) (servingstate.State, servingstate.Artifact, error)
}

func candidatePreviewServingGenerationActive(ctx context.Context, runtimeHost canonicalRuntimeHost) (bool, error) {
	reader, ok := runtimeHost.(candidatePreviewServingStateReader)
	if !ok {
		return false, fmt.Errorf("runtime host does not expose canonical active serving-state lookup")
	}
	_, _, err := reader.ActiveArtifact(ctx)
	if errors.Is(err, servingstate.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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
	return protectManagedDataTransportWithBootstrap(accessModule, runtimeHost, managedData, nil, next)
}

// protectManagedDataTransportWithBootstrap extends the normal generation-bound
// TUS guard with the same exact durable bootstrap decision used by generated
// project operations. Only the local managed-data staging operation is
// admitted before the first serving generation; all active-generation traffic
// remains on the immutable snapshot path.
func protectManagedDataTransportWithBootstrap(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	managedData interface {
		ResolveTusTarget(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error)
	},
	bootstrap accessmodule.APIGenBootstrapAuthorizer,
	next http.Handler,
) http.Handler {
	return protectManagedDataTransportWithBootstrapTimeout(accessModule, runtimeHost, managedData, bootstrap, managedDataTusMutationTimeout, next)
}

func protectManagedDataTransportWithBootstrapTimeout(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	managedData interface {
		ResolveTusTarget(context.Context, string) (projectgraph.ResourceID, projectgraph.ResourceID, error)
	},
	bootstrap accessmodule.APIGenBootstrapAuthorizer,
	mutationTimeout time.Duration,
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
		if r.Method == http.MethodPatch {
			if mutationTimeout <= 0 {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), mutationTimeout)
			defer cancel()
			r = r.WithContext(ctx)
			deadline, _ := ctx.Deadline()
			if err := http.NewResponseController(w).SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
		}
		fencedRuntime, ok := runtimeHost.(interface {
			AcquireCutoverFence(context.Context) (func(), error)
		})
		if !ok {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		releaseFence, fenceErr := fencedRuntime.AcquireCutoverFence(r.Context())
		if fenceErr != nil || releaseFence == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}
		defer releaseFence()
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
		if bootstrap != nil {
			decision, decisionErr := bootstrap(r.Context(), r, "managedDataTusTransport", projectID, access.CapabilityResourceEdit)
			if decisionErr != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if decision.Handled {
				if !decision.Allowed {
					http.NotFound(w, r)
					return
				}
				var bootstrapAccess bool
				if authoringModule, ok := accessModule.(interface {
					AuthorizeAuthoringBootstrapRequest(context.Context, *http.Request, string, access.Capability) (bool, error)
				}); ok {
					bootstrapAccess, err = authoringModule.AuthorizeAuthoringBootstrapRequest(r.Context(), r, projectID.String(), access.CapabilityResourceEdit)
					if err != nil {
						http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
						return
					}
				}
				bootstrapModule, bootstrapModuleOK := accessModule.(interface {
					AuthorizeBootstrapRequest(context.Context, *http.Request, access.Capability) (bool, error)
				})
				if !bootstrapAccess && !bootstrapModuleOK {
					http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
					return
				}
				if !bootstrapAccess {
					bootstrapAccess, err = bootstrapModule.AuthorizeBootstrapRequest(r.Context(), r, access.CapabilityResourceEdit)
					if err != nil {
						http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
						return
					}
				}
				if !bootstrapAccess {
					http.NotFound(w, r)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			activeProjectID := runtimeHost.ProjectID()
			if err := activeProjectID.Validate(); err != nil || projectID != activeProjectID {
				http.NotFound(w, r)
				return
			}
			if principal.DevBypass {
				next.ServeHTTP(w, r)
				return
			}
			found, allowed, authErr := authorizeManagedDataConnection(r.Context(), accessModule, runtimeHost, principal.ID, activeProjectID, resource, access.CapabilityResourceEdit)
			if authErr != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if found {
				if !allowed {
					http.NotFound(w, r)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if !decision.AllowMissingResource {
				http.NotFound(w, r)
				return
			}
			if !allowed {
				http.NotFound(w, r)
				return
			}
			authoringModule, ok := accessModule.(interface {
				AuthorizeManagedDataStagingRequest(context.Context, *http.Request, string, access.Capability) (bool, error)
			})
			if !ok {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			authorized, authoringErr := authoringModule.AuthorizeManagedDataStagingRequest(r.Context(), r, projectID.String(), access.CapabilityResourceEdit)
			if authoringErr != nil {
				http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
				return
			}
			if !authorized {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		activeProjectID := runtimeHost.ProjectID()
		if err := activeProjectID.Validate(); err != nil || projectID != activeProjectID {
			http.NotFound(w, r)
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
