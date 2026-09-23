package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

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

// typedPermissionDecisionForSnapshot evaluates a migrated operation against
// the immutable serving-state authority for the complete principal/group
// subject closure. A typed bearer token is never an authority source: it can
// only attenuate the already-resolved typed assignment set.
func typedPermissionDecisionForSnapshot(
	ctx context.Context,
	principalID string,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	actionFor func(access.ResourceRef) (access.Action, bool),
	snapshot accesssnapshot.AuthorizationSnapshot,
	subjects []access.SubjectRef,
) (typed bool, allowed bool, err error) {
	if len(resources) == 0 || actionFor == nil {
		return true, false, nil
	}
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return true, false, nil
	}
	if err := projectID.Validate(); err != nil {
		return false, false, err
	}
	if err := snapshot.ValidateBound(); err != nil {
		return false, false, err
	}
	if snapshot.Identity().ProjectID != projectID {
		return true, false, nil
	}
	granted, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false, false, err
	}
	credential, hasCredential := accessmodule.APICredentialFromContext(ctx)
	// Browser and project operations always have a typed contract. Absence of
	// a typed assignment is a denial; it never selects a legacy capability
	// decision. An API credential, when present, is an additional ceiling.
	typed = true
	for _, resource := range resources {
		if resource.Kind() != projectgraph.KindProjectNamespace {
			graphResource, exists := snapshot.Project().Resource(resource.ID())
			if !exists || graphResource.Kind != resource.Kind() {
				return true, false, nil
			}
		}
		action, mapped := actionFor(resource)
		if !mapped {
			return true, false, nil
		}
		pair, pairErr := typedPermissionPair(action, projectID, resource)
		if pairErr != nil || !access.PermissionSetAllows(granted, pair) {
			// A malformed or unmapped typed route is a closed authorization
			// contract. Treat it as a denial at the request boundary rather than
			// exposing catalog details as a transient service failure.
			return true, false, nil
		}
	}
	if !hasCredential {
		if len(granted) == 0 {
			return true, false, nil
		}
		return true, true, nil
	}
	// A token reaching a migrated browser operation must be a catalog token
	// bound to this principal. Its pair set is an attenuation ceiling, not a
	// replacement for the principal/group assignment set above.
	if strings.TrimSpace(credential.Token.ID) == "" || credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil ||
		strings.TrimSpace(credential.Token.PrincipalID) == "" || credential.Token.PrincipalID != strings.TrimSpace(principalID) ||
		(credential.Principal.ID != "" && credential.Principal.ID != strings.TrimSpace(principalID)) {
		return true, false, nil
	}
	if err := access.ValidatePermissionPairs(credential.Token.Permissions); err != nil {
		return true, false, nil
	}
	for _, resource := range resources {
		action, mapped := actionFor(resource)
		if !mapped {
			return true, false, nil
		}
		pair, pairErr := typedPermissionPair(action, projectID, resource)
		if pairErr != nil || !access.PermissionSetAllows(credential.Token.Permissions, pair) {
			return true, false, nil
		}
	}
	return true, true, nil
}

// authorizeTypedDashboardAction evaluates the exact dashboard action against
// the immutable principal/group assignment set and then applies an optional
// API-token ceiling. It is used by body-dependent authoring commands, whose
// generated transport route is intentionally authenticated-only because the
// action is selected by the closed command union.
func authorizeTypedDashboardAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID, dashboardID projectgraph.ResourceID,
	action access.Action,
) (bool, error) {
	resource, err := access.NewResourceRef(dashboardID, projectgraph.KindDashboard)
	if err != nil {
		return false, err
	}
	typed, allowed, err := authorizeTypedResourceAction(ctx, accessModule, runtimeHost, principalID, projectID, []access.ResourceRef{resource}, action)
	if err != nil {
		return false, err
	}
	return typed && allowed, nil
}

// authorizeTypedResourceAction evaluates the exact action/resource operation
// against the leased principal/group assignments. An API credential can only
// further attenuate that resolved typed authority.
func authorizeTypedResourceAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resources []access.ResourceRef,
	action access.Action,
) (bool, bool, error) {
	if accessModule == nil || runtimeHost == nil || strings.TrimSpace(principalID) == "" {
		return false, false, fmt.Errorf("typed authorization modules are required")
	}
	if err := projectID.Validate(); err != nil {
		return false, false, err
	}
	for _, resource := range resources {
		if err := resource.Validate(); err != nil {
			return false, false, err
		}
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
	subjects, err := accessModule.AuthorizationSubjects(ctx, principalID)
	if err != nil {
		return false, false, err
	}
	snapshot := authorizedLease.AuthorizationSnapshot()
	if snapshot.Identity() != lease.Identity() {
		return false, false, fmt.Errorf("authorization snapshot identity does not match leased serving generation")
	}
	if err := snapshot.ValidateBound(); err != nil {
		return false, false, err
	}
	return typedPermissionDecisionForSnapshot(ctx, principalID, projectID, resources, func(access.ResourceRef) (access.Action, bool) {
		return action, true
	}, snapshot, subjects)
}

func authorizeTypedAuthoringResourceAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resource access.ResourceRef,
	action authoring.AuthorizationAction,
) (bool, bool, error) {
	typedAction, err := typedAuthoringAction(action)
	if err != nil {
		return false, false, err
	}
	return authorizeTypedResourceAction(ctx, accessModule, runtimeHost, principalID, projectID, []access.ResourceRef{resource}, typedAction)
}

func authorizeTypedAuthoringProjectAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	action access.Action,
) (bool, bool, error) {
	resource, err := access.NewResourceRef(projectID, projectgraph.KindProjectNamespace)
	if err != nil {
		return false, false, err
	}
	return authorizeTypedResourceAction(ctx, accessModule, runtimeHost, principalID, projectID, []access.ResourceRef{resource}, action)
}

func authorizeAuthoringResourceAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	resource access.ResourceRef,
	action authoring.AuthorizationAction,
) (bool, bool, error) {
	return authorizeTypedAuthoringResourceAction(ctx, accessModule, runtimeHost, principalID, projectID, resource, action)
}

func authorizeAuthoringProjectAction(
	ctx context.Context,
	accessModule canonicalAccessModule,
	runtimeHost canonicalRuntimeHost,
	principalID string,
	projectID projectgraph.ResourceID,
	action access.Action,
) (bool, bool, error) {
	return authorizeTypedAuthoringProjectAction(ctx, accessModule, runtimeHost, principalID, projectID, action)
}

func typedAuthoringAction(action authoring.AuthorizationAction) (access.Action, error) {
	switch action {
	case authoring.AuthorizationActionView:
		return access.ActionDashboardRead, nil
	case authoring.AuthorizationActionEdit:
		return access.ActionDashboardUpdate, nil
	case authoring.AuthorizationActionPublish:
		return access.ActionDashboardPublish, nil
	case authoring.AuthorizationActionArchive:
		return access.ActionDashboardDelete, nil
	default:
		return "", fmt.Errorf("unsupported authoring action %q", action)
	}
}
