package authz

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (m Metrics) authorizeDataQuery(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, principalID string, required []access.PermissionPair) (bool, error) {
	subjects, err := m.subjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	authority, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false, err
	}
	return permissionPairsAllow(authority, required), nil
}

func (m Metrics) tokenAllowsDataQuery(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, principalID string, token access.APIToken, required []access.PermissionPair) bool {
	subjects, err := m.subjects(ctx, principalID)
	if err != nil {
		return false
	}
	return m.tokenAllowsTypedPairs(snapshot, subjects, token, required)
}

func dataQueryPermissionPairs(ctx context.Context, request dataquery.Query, objects []access.ResourceRef) ([]access.PermissionPair, error) {
	var action access.Action
	var resolver access.TypedOperationResolver
	var target access.ResourceRef
	switch request.Kind {
	case dataquery.KindModelRows:
		action = access.ActionModelRead
		resolver = access.TypedOperationResolverModel
		for _, object := range objects {
			if object.Kind() == projectgraph.KindModel {
				if target.ID() != "" && target != object {
					return nil, ErrDataQueryPermissionRequirementUnavailable
				}
				target = object
			}
		}
		if target.ID() == "" {
			return nil, ErrDataQueryPermissionRequirementUnavailable
		}
	case dataquery.KindSemanticAggregate, dataquery.KindSemanticRows,
		dataquery.KindSemanticHistogram, dataquery.KindSemanticDistribution,
		dataquery.KindSemanticSpatialTile, dataquery.KindSemanticSpatialTileBudget,
		dataquery.KindSemanticSpatialMetadata:
		var err error
		action, err = semanticPermissionActionWithContext(ctx, request)
		if err != nil {
			return nil, err
		}
		resolver = access.TypedOperationResolverSemanticModel
		var ok bool
		target, ok = canonicalResourceForObjects(objects, request.ModelID, projectgraph.KindSemanticModel)
		if !ok {
			return nil, ErrDataQueryPermissionRequirementUnavailable
		}
	default:
		return nil, ErrDataQueryPermissionRequirementUnavailable
	}
	requirement, err := access.NewTypedOperationRequirementService().Requirement(action, string(resolver))
	if err != nil {
		return nil, err
	}
	return requirement.ResolvePairs(request.ProjectID, target)
}

func dataQueryActionForDenial(ctx context.Context, request dataquery.Query) access.Action {
	if request.Kind == dataquery.KindModelRows {
		return access.ActionModelRead
	}
	if action, err := semanticPermissionActionWithContext(ctx, request); err == nil {
		return action
	}
	return access.ActionSemanticQuery
}

func canonicalResourceForObjects(objects []access.ResourceRef, id string, kind projectgraph.Kind) (access.ResourceRef, bool) {
	for _, object := range objects {
		if object.Kind() == kind && object.CanonicalID() == id {
			return object, true
		}
	}
	return access.ResourceRef{}, false
}

func permissionPairsAllow(granted, required []access.PermissionPair) bool {
	if len(required) == 0 {
		return false
	}
	for _, pair := range required {
		if pair.Validate() != nil || !access.PermissionSetAllows(granted, pair) {
			return false
		}
	}
	return true
}

func (m Metrics) tokenAllowsTypedPairs(snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, token access.APIToken, required []access.PermissionPair) bool {
	if token.PermissionProfile != access.PermissionCatalogProfile || access.ValidateTypedPermissionSet(token.PermissionProfile, token.Permissions) != nil {
		return false
	}
	effective, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false
	}
	ceiling := access.IntersectPermissionPairs(token.Permissions, effective)
	return permissionPairsAllow(ceiling, required)
}

func (m Metrics) viewAsPermissionPairs(projectID projectgraph.ResourceID) ([]access.PermissionPair, error) {
	project, err := access.NewResourceRef(projectID, projectgraph.KindProjectNamespace)
	if err != nil {
		return nil, err
	}
	requirement, err := access.NewTypedOperationRequirementService().Requirement(access.ActionProjectAccessManage, string(access.TypedOperationResolverProject))
	if err != nil {
		return nil, err
	}
	return requirement.ResolvePairs(projectID, project)
}

func (m Metrics) viewAsAllowed(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, principalID string, token *access.APIToken, projectID projectgraph.ResourceID) (bool, error) {
	subjects, err := m.subjects(ctx, principalID)
	if err != nil {
		return false, err
	}
	pairs, err := m.viewAsPermissionPairs(projectID)
	if err != nil {
		return false, err
	}
	if token != nil {
		return m.tokenAllowsTypedPairs(snapshot, subjects, *token, pairs), nil
	}
	authority, err := snapshot.EffectiveTypedPermissions(subjects)
	if err != nil {
		return false, err
	}
	return permissionPairsAllow(authority, pairs), nil
}
