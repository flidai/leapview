package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/consumer"
)

type singleProjectionPlanner struct {
	semanticDiscoveryPlannerMetrics
	calls int
}

func (m *singleProjectionPlanner) Planner(string) (consumer.Planner, bool) {
	m.calls++
	if m.calls > 1 {
		return nil, false
	}
	return m.planner, true
}

func TestSemanticModelProjectionRetainsOnePlannerAndRejectsStrippedPolicy(t *testing.T) {
	metrics, model := semanticDiscoveryFixture(t)
	provider := &singleProjectionPlanner{semanticDiscoveryPlannerMetrics: metrics.Metrics.(semanticDiscoveryPlannerMetrics)}
	metrics.Metrics = provider
	if err := metrics.AuthorizeSemanticModelProjection(context.Background(), "sales"); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("planner acquisitions = %d, want 1", provider.calls)
	}
	provider.calls = 0
	model.AccessGrants = nil
	for name, dataset := range model.Datasets {
		dataset.RequiredAccessGrants = nil
		dataset.AccessFilters = nil
		model.Datasets[name] = dataset
	}
	if err := metrics.AuthorizeSemanticModelProjection(context.Background(), "sales"); err == nil {
		t.Fatal("stripped policy exposed the visual spec")
	}
}

func TestSemanticModelProjectionUsesBoundPlannerForWholeModel(t *testing.T) {
	metrics, _ := semanticDiscoveryFixture(t)
	if err := metrics.AuthorizeSemanticModelProjection(context.Background(), "sales"); err != nil {
		t.Fatalf("authorized whole-model projection was denied: %v", err)
	}

	denied, _ := semanticDiscoveryFixture(t)
	resolution, err := denied.resolveSemanticAttributes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	region := resolution.Registry.Definitions[0]
	values, digest, err := access.CanonicalSemanticAttributeValues(region, "eu")
	if err != nil {
		t.Fatal(err)
	}
	resolution.Attributes[0].CanonicalValues = values
	resolution.Attributes[0].ValueDigest = digest
	denied.resolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) {
		return resolution, nil
	}
	if err := denied.AuthorizeSemanticModelProjection(context.Background(), "sales"); err == nil {
		t.Fatal("denied whole-model projection was authorized")
	}
}

func TestSemanticModelProjectionFailsClosedWithoutAuthority(t *testing.T) {
	metrics, _ := semanticDiscoveryFixture(t)
	metrics.resolveSemanticAttributes = nil
	err := metrics.AuthorizeSemanticModelProjection(context.Background(), "sales")
	if !errors.Is(err, ErrSemanticConsumerAuthorityUnavailable) {
		t.Fatalf("missing semantic authority error = %v, want %v", err, ErrSemanticConsumerAuthorityUnavailable)
	}
}
