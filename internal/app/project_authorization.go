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
// consume the opaque request marker emitted by the APIGen bootstrap guard.
// The marker is accepted only while the durable serving-state repository has
// no active generation; all active requests continue through the snapshot
// authorizer, including when snapshot acquisition fails for another reason.
func bootstrapAwareConnectionAuthorization(
	snapshot connectionAuthorization,
	active func(context.Context) (bool, error),
) connectionAuthorization {
	return func(ctx context.Context, principalID, projectID, connectionID string, capability access.Capability) (bool, error) {
		if marker, ok := accessmodule.BootstrapAuthorizationFromContext(ctx); ok && marker.PrincipalID == strings.TrimSpace(principalID) && marker.ProjectID.String() == strings.TrimSpace(projectID) && marker.Capability == capability {
			isActive, err := active(ctx)
			if err != nil {
				return false, err
			}
			if !isActive {
				return true, nil
			}
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

func authorizeProjectResourcesWithTypedAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	capability access.Capability,
	action access.Action,
) (bool, error) {
	return authorizeProjectResourcesWithCapability(ctx, accessModule, runtimeHost, principalID, projectID, resources, false, func(access.ResourceRef) access.Capability {
		return capability
	}, func(access.ResourceRef) (access.Action, bool) {
		return action, true
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
	typedActionFor ...func(access.ResourceRef) (access.Action, bool),
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
	var typedActionResolver func(access.ResourceRef) (access.Action, bool)
	if len(typedActionFor) > 1 {
		return false, fmt.Errorf("at most one typed action resolver is supported")
	}
	if len(typedActionFor) == 1 {
		typedActionResolver = typedActionFor[0]
	}
	var typed, allowed bool
	if typedActionResolver != nil {
		typed, allowed = typedPermissionDecision(ctx, projectID, resources, typedActionResolver)
	} else {
		typed, allowed = typedDashboardReadDecision(ctx, projectID, resources, capabilityFor)
	}
	if typed && !allowed {
		return false, nil
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

func typedPermissionPair(action access.Action, projectID projectgraph.ResourceID, resource access.ResourceRef) (access.PermissionPair, error) {
	definition, ok := access.Permission(action)
	if !ok {
		return access.PermissionPair{}, access.ErrUnknownPermissionAction
	}
	checkKind := false
	for _, kind := range definition.CheckKinds {
		if kind == resource.Kind() {
			checkKind = true
			break
		}
	}
	if !checkKind {
		return access.PermissionPair{}, fmt.Errorf("typed action %q cannot check resource kind %q", action, resource.Kind())
	}
	if definition.Scope == access.PermissionScopeProject {
		return access.NewProjectPermissionPair(action, projectID)
	}
	if definition.Scope == access.PermissionScopeResource {
		return access.NewExactPermissionPair(action, projectID, resource)
	}
	return access.PermissionPair{}, fmt.Errorf("typed action %q has unsupported scope %q", action, definition.Scope)
}

func typedPermissionDecision(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	actionFor func(access.ResourceRef) (access.Action, bool),
) (typed bool, allowed bool) {
	credential, found := accessmodule.APICredentialFromContext(ctx)
	if !found || strings.TrimSpace(credential.Token.ID) == "" ||
		(credential.Token.PermissionProfile == "" && credential.Token.Permissions == nil) {
		return false, false
	}
	if len(resources) == 0 || actionFor == nil {
		return true, false
	}
	for _, resource := range resources {
		action, mapped := actionFor(resource)
		if !mapped {
			return true, false
		}
		pair, err := typedPermissionPair(action, projectID, resource)
		if err != nil || !access.PermissionSetAllows(credential.Token.Permissions, pair) {
			return true, false
		}
	}
	return true, true
}

// typedDashboardReadDecision binds the dashboard Viewer operation to the
// typed dashboard.read action. It is intentionally narrow: this helper is
// used by the browser/API dashboard resource-read callback, and does not
// infer typed authority for mutations or unrelated resource families.
// Legacy API tokens and browser sessions retain their existing snapshot
// capability path until their operation contracts are migrated explicitly.
func typedDashboardReadDecision(
	ctx context.Context,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	capabilityFor func(access.ResourceRef) access.Capability,
) (typed bool, allowed bool) {
	return typedPermissionDecision(ctx, projectID, resources, func(resource access.ResourceRef) (access.Action, bool) {
		if resource.Kind() != projectgraph.KindDashboard || capabilityFor == nil || capabilityFor(resource) != access.CapabilityResourceRead {
			return "", false
		}
		return access.ActionDashboardRead, true
	})
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
	return protectProjectResourcesWithTypedAction(accessModule, runtimeHost, capability, "", resolve, next)
}

func protectProjectResourcesWithTypedAction(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	capability access.Capability,
	action access.Action,
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
		var allowed bool
		var err error
		if action == "" {
			allowed, err = authorizeProjectResources(r.Context(), accessModule, runtimeHost, principal.ID, projectID, resources, capability)
		} else {
			allowed, err = authorizeProjectResourcesWithCapability(r.Context(), accessModule, runtimeHost, principal.ID, projectID, resources, false, func(access.ResourceRef) access.Capability {
				return capability
			}, func(access.ResourceRef) (access.Action, bool) { return action, true })
		}
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
	return protectProjectAuthoringResourceWithTypedAction(accessModule, runtimeHost, authorizer, capability, "", next)
}

func protectProjectAuthoringResourceWithTypedAction(
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	authorizer repositoryDashboardAuthorizer,
	capability access.Capability,
	typedAction access.Action,
	next http.HandlerFunc,
) http.HandlerFunc {
	if accessModule == nil || runtimeHost == nil || authorizer == nil || (capability != access.CapabilityResourceEdit && capability != access.CapabilityResourceManage) {
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
		dashboardID := strings.TrimSpace(chi.URLParam(r, "dashboard"))
		if err := authoring.ValidateDashboardID(authoring.DashboardID(dashboardID)); err != nil {
			http.NotFound(w, r)
			return
		}
		if principal.DevBypass {
			next(w, r)
			return
		}
		if typedAction != "" {
			resource, resourceErr := access.NewResourceRef(projectgraph.ResourceID(dashboardID), projectgraph.KindDashboard)
			if resourceErr != nil {
				http.NotFound(w, r)
				return
			}
			typed, allowed := typedPermissionDecision(r.Context(), projectID, []access.ResourceRef{resource}, func(access.ResourceRef) (access.Action, bool) {
				return typedAction, true
			})
			if typed && !allowed {
				uitransport.WriteBrowserAuthorizationError(w, r, http.StatusForbidden)
				return
			}
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
				bootstrapModule, bootstrapModuleOK := accessModule.(interface {
					AuthorizeBootstrapRequest(context.Context, *http.Request, access.Capability) (bool, error)
				})
				if !bootstrapModuleOK {
					http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
					return
				}
				bootstrapAccess, accessErr := bootstrapModule.AuthorizeBootstrapRequest(r.Context(), r, access.CapabilityResourceEdit)
				if accessErr != nil {
					http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
					return
				}
				if !bootstrapAccess {
					http.NotFound(w, r)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
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
