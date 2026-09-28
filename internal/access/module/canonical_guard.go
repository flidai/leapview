package module

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/snapshot"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ConnectionAuthorizerFromSnapshot adapts the active serving-generation
// snapshot and identity-layer subject resolver to the narrow connection
// authorization port used by managed-data and connection catalog transports.
// The port accepts only typed Connection actions. REST tokens and authoring
// credentials can only attenuate the same exact pair, and authoring scopes are
// additionally bound to this runtime's instance ID.
// Both providers are mandatory: an unavailable snapshot or subject resolver
// fails closed instead of falling back to mutable access tables.
func ConnectionAuthorizerFromSnapshot(
	instanceID string,
	snapshotProvider func(context.Context) (snapshot.AuthorizationSnapshot, error),
	subjectsProvider func(context.Context, string) ([]access.SubjectRef, error),
) func(context.Context, string, string, string, access.Action) (bool, error) {
	instanceID = strings.TrimSpace(instanceID)
	return func(ctx context.Context, principalID, projectID, connectionID string, action access.Action) (bool, error) {
		if snapshotProvider == nil || subjectsProvider == nil {
			return false, fmt.Errorf("active authorization snapshot is unavailable")
		}
		if principalID == "" {
			return false, nil
		}
		project, err := projectgraph.NewResourceID(projectID)
		if err != nil {
			return false, err
		}
		connection, err := projectgraph.NewResourceID(connectionID)
		if err != nil {
			return false, err
		}
		leased, err := snapshotProvider(ctx)
		if err != nil {
			return false, err
		}
		if leased.Identity().ProjectID != project {
			return false, fmt.Errorf("authorization snapshot project %q does not match requested project %q", leased.Identity().ProjectID, project)
		}
		resource, err := access.NewResourceRef(connection, projectgraph.KindConnection)
		if err != nil {
			return false, err
		}
		switch action {
		case access.ActionConnectionRead, access.ActionConnectionUse, access.ActionConnectionManage:
		default:
			return false, nil
		}
		pair, err := access.NewExactPermissionPair(action, project, resource)
		if err != nil {
			return false, err
		}
		if err := leased.ValidateBound(); err != nil {
			return false, err
		}
		graphResource, exists := leased.Project().Resource(connection)
		if !exists || graphResource.Kind != projectgraph.KindConnection {
			return false, nil
		}
		credential, hasCredential := APICredentialFromContext(ctx)
		if hasCredential {
			if credential.Authoring != nil {
				authoring := *credential.Authoring
				principal := Principal{ID: credential.Principal.ID, Kind: credential.Principal.Kind}
				if instanceID == "" || authoring.Scope.TargetID != instanceID || credential.Token.ID == "" ||
					credential.Token.PrincipalID != principalID || credential.Principal.ID != principalID ||
					validateAuthoringBootstrapCredential(credential, authoring, principal, projectID) != nil ||
					authoring.Scope.AuthorizePairs(instanceID, projectID, []access.PermissionPair{pair}) != nil {
					return false, nil
				}
			} else {
				if credential.Token.ID == "" || credential.Token.PermissionProfile != access.PermissionCatalogProfile || credential.Token.Permissions == nil ||
					credential.Token.PrincipalID == "" || credential.Token.PrincipalID != principalID ||
					(credential.Principal.ID != "" && credential.Principal.ID != principalID) {
					return false, nil
				}
				if err := access.ValidatePermissionPairs(credential.Token.Permissions); err != nil {
					return false, nil
				}
				if !access.PermissionSetAllows(credential.Token.Permissions, pair) {
					return false, nil
				}
			}
		}
		subjects, err := subjectsProvider(ctx, principalID)
		if err != nil {
			return false, err
		}
		granted, err := leased.EffectiveTypedPermissions(subjects)
		if err != nil {
			return false, err
		}
		return access.PermissionSetAllows(granted, pair), nil
	}
}
