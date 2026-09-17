package authz

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func TestDashboardSemanticConsumptionRequiresSemanticModelUse(t *testing.T) {
	_, _, semantic, physical, _ := canonicalGraph(t)
	physicalOnly := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"physical", physical, access.CapabilityResourceUse}}, nil)
	metrics := canonicalMetricsWithSnapshot(t, physicalOnly, nil)
	request := dashboardSemanticConsumptionQuery(semantic.CanonicalID())
	if _, _, err := metrics.GovernDataQuery(context.Background(), request); !IsDenied(err) {
		t.Fatalf("physical-only dashboard semantic query error = %v, want denial", err)
	}

	semanticOnly := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic", semantic, access.CapabilityResourceUse}}, nil)
	if _, _, err := canonicalMetricsWithSnapshot(t, semanticOnly, nil).GovernDataQuery(context.Background(), request); err != nil {
		t.Fatalf("semantic consume grant was denied: %v", err)
	}
}

func TestDashboardReadDoesNotSubstituteForSemanticConsumption(t *testing.T) {
	_, _, semantic, _, dashboard := canonicalGraph(t)
	dashboardRead := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"dashboard", dashboard, access.CapabilityResourceRead}}, nil)
	request := dashboardSemanticConsumptionQuery(semantic.CanonicalID())
	if _, _, err := canonicalMetricsWithSnapshot(t, dashboardRead, nil).GovernDataQuery(context.Background(), request); !IsDenied(err) {
		t.Fatalf("dashboard-read-only query error = %v, want denial", err)
	}
}

func TestDashboardPreviewRequiresSemanticConsumptionInAdditionToRead(t *testing.T) {
	_, _, semantic, _, _ := canonicalGraph(t)
	semanticReadOnly := canonicalSnapshot(t, []struct {
		id         string
		resource   access.ResourceRef
		capability access.Capability
	}{{"semantic-read", semantic, access.CapabilityResourceRead}}, nil)
	request := dashboardSemanticConsumptionQuery(semantic.CanonicalID())
	request.Surface = dataquery.SurfaceAPI
	request.Operation = dataquery.OperationAPIPreview
	if _, _, err := canonicalMetricsWithSnapshot(t, semanticReadOnly, nil).GovernDataQuery(context.Background(), request); !IsDenied(err) {
		t.Fatalf("read-only semantic preview error = %v, want denial", err)
	}
}

func dashboardSemanticConsumptionQuery(modelID string) dataquery.Query {
	return dataquery.Query{
		ProjectID: canonicalProject,
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardRows,
		ModelID:   modelID,
		Kind:      dataquery.KindSemanticRows,
		Target:    "orders",
	}
}
