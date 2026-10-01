package authz

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

var ErrDataQueryPermissionRequirementUnavailable = errors.New("data query permission requirement is unavailable")

// semanticPermissionAction selects the typed action for one semantic query.
// A normal dashboard execution consumes an approved query shape. Draft
// previews and non-dashboard query surfaces construct a new shape and require
// semantic.query, including its catalog-defined semantic.consume prerequisite.
func semanticPermissionAction(request dataquery.Query) (access.Action, error) {
	return semanticPermissionActionWithContext(context.Background(), request)
}

func semanticPermissionActionWithContext(ctx context.Context, request dataquery.Query) (access.Action, error) {
	if !isSemanticQueryKind(request.Kind) || request.Surface == dataquery.SurfacePublicDashboard {
		return "", ErrDataQueryPermissionRequirementUnavailable
	}
	if _, candidate := candidateQueryCapabilityFromContext(ctx); candidate {
		return access.ActionSemanticQuery, nil
	}
	if request.Surface == dataquery.SurfaceDashboard {
		if request.Operation == dataquery.OperationDashboardDraftPreview {
			return access.ActionSemanticQuery, nil
		}
		if isDashboardQueryOperation(request.Operation) {
			return access.ActionSemanticConsume, nil
		}
		return "", ErrDataQueryPermissionRequirementUnavailable
	}
	switch request.Surface {
	case dataquery.SurfaceAPI, dataquery.SurfaceAgent, dataquery.SurfaceCLI, dataquery.SurfaceDataExplorer:
		return access.ActionSemanticQuery, nil
	default:
		return "", ErrDataQueryPermissionRequirementUnavailable
	}
}

func isDashboardQueryOperation(operation string) bool {
	switch operation {
	case dataquery.OperationDashboardAggregate, dataquery.OperationDashboardRows,
		dataquery.OperationDashboardCount, dataquery.OperationDashboardHistogram,
		dataquery.OperationDashboardDistribution, dataquery.OperationDashboardFilterOptions,
		dataquery.OperationDashboardSpatialTile, dataquery.OperationDashboardSpatialTileBudget,
		dataquery.OperationDashboardSpatialMetadata:
		return true
	default:
		return false
	}
}

func isSemanticQueryKind(kind dataquery.Kind) bool {
	switch kind {
	case dataquery.KindSemanticAggregate, dataquery.KindSemanticRows,
		dataquery.KindSemanticHistogram, dataquery.KindSemanticDistribution,
		dataquery.KindSemanticSpatialTile, dataquery.KindSemanticSpatialTileBudget,
		dataquery.KindSemanticSpatialMetadata:
		return true
	default:
		return false
	}
}
