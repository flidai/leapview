package authz

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// DashboardPublicationCapability is installed by trusted publication handlers.
// ProjectID and every dependency ID are canonical graph identities; no
// workspace/path metadata is accepted as an authorization boundary.
type DashboardPublicationCapability struct {
	ProjectID          projectgraph.ResourceID
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

func validateDashboardPublicationQuery(capability DashboardPublicationCapability, request dataquery.Query, objects []access.ResourceRef) error {
	if err := capability.ProjectID.Validate(); err != nil || strings.TrimSpace(capability.ProjectID.String()) != capability.ProjectID.String() {
		return fmt.Errorf("dashboard publication project identity is invalid")
	}
	if strings.TrimSpace(capability.Publication) == "" || strings.TrimSpace(capability.Dashboard) == "" || strings.TrimSpace(capability.ModelID) == "" {
		return fmt.Errorf("dashboard publication capability is incomplete")
	}
	if request.ProjectID != capability.ProjectID {
		return fmt.Errorf("public query project %q is outside publication project %q", request.ProjectID, capability.ProjectID)
	}
	if request.Surface != dataquery.SurfacePublicDashboard {
		return fmt.Errorf("public query surface %q is not allowed", request.Surface)
	}
	if request.ModelID != capability.ModelID {
		return fmt.Errorf("public query model %q is outside publication model %q", request.ModelID, capability.ModelID)
	}
	switch request.Operation {
	case dataquery.OperationDashboardAggregate, dataquery.OperationDashboardRows, dataquery.OperationDashboardCount,
		dataquery.OperationDashboardHistogram, dataquery.OperationDashboardDistribution, dataquery.OperationDashboardFilterOptions,
		dataquery.OperationDashboardSpatialTile, dataquery.OperationDashboardSpatialTileBudget, dataquery.OperationDashboardSpatialMetadata:
	default:
		return fmt.Errorf("public query operation %q is not allowed", request.Operation)
	}
	switch request.Kind {
	case dataquery.KindSemanticAggregate, dataquery.KindSemanticRows, dataquery.KindSemanticHistogram,
		dataquery.KindSemanticDistribution, dataquery.KindSemanticSpatialTile, dataquery.KindSemanticSpatialTileBudget,
		dataquery.KindSemanticSpatialMetadata:
	default:
		return fmt.Errorf("public query kind %q is not allowed", request.Kind)
	}
	closure := make(map[string]struct{}, len(capability.DependencyAssetIDs))
	for _, id := range capability.DependencyAssetIDs {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return fmt.Errorf("publication dependency identity is invalid")
		}
		closure[id] = struct{}{}
	}
	// Dashboard graph IDs are canonical resource IDs. Publications may store
	// either the complete ID or the authored dashboard name; normalize only the
	// latter at this trusted boundary.
	dashboardID := capability.Dashboard
	if !strings.Contains(dashboardID, ":") {
		dashboardID = "dashboard:" + dashboardID
	}
	if _, ok := closure[dashboardID]; !ok {
		return fmt.Errorf("publication closure omits dashboard %q", capability.Dashboard)
	}
	for _, object := range objects {
		if err := object.Validate(); err != nil {
			return fmt.Errorf("public query object is invalid: %w", err)
		}
		assetID := object.CanonicalID()
		if _, ok := closure[assetID]; !ok {
			return fmt.Errorf("public query dependency %q is outside publication closure", assetID)
		}
	}
	return nil
}
