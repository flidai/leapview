package authz

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

// DashboardPublicationCapability is installed in a server-created context.
// Its private context key makes it impossible for an HTTP client, cookie, API
// token, or authored query payload to manufacture this execution authority.
type DashboardPublicationCapability struct {
	WorkspaceID        string
	Publication        string
	Dashboard          string
	ModelID            string
	DependencyAssetIDs []string
}

type dashboardPublicationCapabilityKey struct{}

func WithDashboardPublicationCapability(ctx context.Context, capability DashboardPublicationCapability) context.Context {
	return context.WithValue(ctx, dashboardPublicationCapabilityKey{}, capability)
}

func dashboardPublicationCapabilityFromContext(ctx context.Context) (DashboardPublicationCapability, bool) {
	capability, ok := ctx.Value(dashboardPublicationCapabilityKey{}).(DashboardPublicationCapability)
	return capability, ok
}

func validateDashboardPublicationQuery(capability DashboardPublicationCapability, request dataquery.Query, objects []access.ObjectRef) error {
	if strings.TrimSpace(capability.WorkspaceID) == "" || strings.TrimSpace(capability.Publication) == "" || strings.TrimSpace(capability.Dashboard) == "" || strings.TrimSpace(capability.ModelID) == "" {
		return fmt.Errorf("dashboard publication capability is incomplete")
	}
	if request.WorkspaceID != capability.WorkspaceID {
		return fmt.Errorf("public query workspace %q is outside publication workspace %q", request.WorkspaceID, capability.WorkspaceID)
	}
	if request.Surface != dataquery.SurfacePublicDashboard {
		return fmt.Errorf("public query surface %q is not allowed", request.Surface)
	}
	if request.ModelID != capability.ModelID {
		return fmt.Errorf("public query model %q is outside publication model %q", request.ModelID, capability.ModelID)
	}
	switch request.Operation {
	case dataquery.OperationDashboardAggregate,
		dataquery.OperationDashboardRows,
		dataquery.OperationDashboardCount,
		dataquery.OperationDashboardHistogram,
		dataquery.OperationDashboardDistribution,
		dataquery.OperationDashboardFilterOptions:
	default:
		return fmt.Errorf("public query operation %q is not allowed", request.Operation)
	}
	switch request.Kind {
	case dataquery.KindSemanticAggregate, dataquery.KindSemanticRows, dataquery.KindSemanticHistogram, dataquery.KindSemanticDistribution, dataquery.KindSemanticSpatial, dataquery.KindSemanticSpatialTile, dataquery.KindSemanticSpatialMetadata:
	default:
		return fmt.Errorf("public query kind %q is not allowed", request.Kind)
	}
	closure := make(map[string]struct{}, len(capability.DependencyAssetIDs))
	for _, id := range capability.DependencyAssetIDs {
		closure[id] = struct{}{}
	}
	wantDashboard := "dashboard:" + capability.WorkspaceID + "." + capability.Dashboard
	if _, ok := closure[wantDashboard]; !ok {
		return fmt.Errorf("publication closure omits dashboard %q", capability.Dashboard)
	}
	for _, object := range objects {
		if object.WorkspaceID != "" && object.WorkspaceID != capability.WorkspaceID {
			return fmt.Errorf("public query object %q is outside publication workspace", object.CanonicalID())
		}
		var assetID string
		switch object.Type {
		case access.SecurableWorkspace:
			continue
		case access.SecurableSemanticModel:
			assetID = "semantic_model:" + capability.WorkspaceID + "." + object.ObjectID
		case access.SecurableDataset:
			assetID = "semantic_table:" + capability.WorkspaceID + "." + strings.ReplaceAll(object.ObjectID, "/", ".")
		case access.SecurableColumn:
			assetID = "field:" + capability.WorkspaceID + "." + strings.ReplaceAll(object.ObjectID, "/", ".")
		case access.SecurableSemanticField:
			path := strings.ReplaceAll(object.ObjectID, "/", ".")
			for _, prefix := range []string{"field:", "measure:"} {
				if _, ok := closure[prefix+capability.WorkspaceID+"."+path]; ok {
					assetID = prefix + capability.WorkspaceID + "." + path
					break
				}
			}
			if assetID == "" {
				return fmt.Errorf("public query semantic field %q is outside publication closure", object.ObjectID)
			}
		default:
			return fmt.Errorf("public query object type %q is not allowed", object.Type)
		}
		if _, ok := closure[assetID]; !ok {
			return fmt.Errorf("public query dependency %q is outside publication closure", assetID)
		}
	}
	return nil
}
