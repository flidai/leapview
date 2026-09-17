package authz

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// authorizeTypedSemanticQuery evaluates the exact query/consume operation
// when the active generation carries typed authority. Semantic data policy is
// still applied by the caller after this permission boundary succeeds.
func authorizeTypedSemanticQuery(ctx context.Context, snapshot accesssnapshot.AuthorizationSnapshot, subjects []access.SubjectRef, request dataquery.Query, objects []access.ResourceRef) (bool, bool, error) {
	action, typed := semanticPermissionActionWithContext(ctx, request)
	if !typed {
		return false, false, nil
	}
	semanticModel, ok := canonicalResourceForObjects(objects, request.ModelID, projectgraph.KindSemanticModel)
	if !ok {
		return false, false, nil
	}
	hasTypedAuthority, allowed, err := typedSemanticPermission(snapshot, subjects, request, semanticModel, action)
	return hasTypedAuthority, allowed, err
}
