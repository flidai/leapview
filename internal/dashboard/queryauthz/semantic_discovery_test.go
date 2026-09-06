package authz

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticDiscoveryPlannerMetrics struct {
	canonicalMetrics
	planner *semanticquery.Planner
}

func (m semanticDiscoveryPlannerMetrics) Planner(string) (consumer.Planner, bool) {
	return m.planner, m.planner != nil
}

func semanticDiscoveryFixture(t *testing.T) (Metrics, *semanticmodel.Model) {
	t.Helper()
	base := canonicalMetricsWithSnapshot(t, canonicalSnapshot(t, nil, nil), nil)
	underlying := base.Metrics.(canonicalMetrics)
	model := underlying.model
	region := access.SemanticAttributeDefinition{
		ID: "def-region", Name: "region", Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
		DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	model.AccessGrants = map[string]semanticmodel.SemanticAccessGrantSpec{
		"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
	}
	orders := model.Datasets["orders"]
	orders.RequiredAccessGrants = []string{"region_grant"}
	model.Datasets["orders"] = orders
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:registry"},
		Definitions: []access.SemanticAttributeDefinition{region},
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(region, "us")
	if err != nil {
		t.Fatal(err)
	}
	control := access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:control"}
	attribute := access.EffectiveSemanticAttribute{
		DefinitionID: region.ID, DefinitionName: region.Name, DefinitionVersion: region.DefinitionVersion,
		Type: region.Type, Shape: region.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
	}
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	planner, err := semanticquery.NewSemanticAccessPlanner(compiled, semanticquery.SemanticAccessEvaluationContext{})
	if err != nil {
		t.Fatal(err)
	}
	underlying.model = model
	metrics := New(semanticDiscoveryPlannerMetrics{canonicalMetrics: underlying, planner: planner}, Options{
		ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
			subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
			return access.SemanticAttributeResolution{Subject: subject, Registry: registry, ControlState: control, Attributes: []access.EffectiveSemanticAttribute{attribute}}, err
		},
		SnapshotFromContext: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
			return canonicalSnapshot(t, nil, nil), nil
		},
	})
	return metrics, model
}

func TestSemanticDiscoveryUsesCompiledPolicyWhenAuthoredModelMutates(t *testing.T) {
	metrics, model := semanticDiscoveryFixture(t)
	if err := metrics.AuthorizeSemanticTarget(context.Background(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatalf("valid compiled protected discovery was denied: %v", err)
	}
	if _, err := metrics.SemanticPlanner(context.Background(), "sales"); err != nil {
		t.Fatalf("valid semantic consumer planner was denied: %v", err)
	}
	model.AccessGrants = nil
	orders := model.Datasets["orders"]
	orders.RequiredAccessGrants = nil
	orders.AccessFilters = nil
	model.Datasets["orders"] = orders
	if metrics.SemanticConsumerCacheAllowed("sales") {
		t.Fatal("stale authored model enabled shared semantic cache")
	}
	if err := metrics.AuthorizeSemanticTarget(context.Background(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err == nil {
		t.Fatal("stale authored model bypassed compiled protected discovery")
	}
}

func TestSemanticPlannerFailsClosedWithoutConsumerAuthority(t *testing.T) {
	metrics, _ := semanticDiscoveryFixture(t)
	metrics.resolveSemanticAttributes = nil
	if _, err := metrics.SemanticPlanner(context.Background(), "sales"); err == nil {
		t.Fatal("missing semantic consumer authority was accepted")
	}

	stale, _ := semanticDiscoveryFixture(t)
	staleResolution := semanticDiscoveryStaleResolution(t)
	stale.resolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) {
		return staleResolution, nil
	}
	if _, err := stale.SemanticPlanner(context.Background(), "sales"); err == nil {
		t.Fatal("stale semantic context was accepted")
	}
}

func semanticDiscoveryStaleResolution(t *testing.T) access.SemanticAttributeResolution {
	t.Helper()
	region := access.SemanticAttributeDefinition{
		ID: "def-region", Name: "region", Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
		DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:stale"},
		Definitions: []access.SemanticAttributeDefinition{region},
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(region, "us")
	if err != nil {
		t.Fatal(err)
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
	if err != nil {
		t.Fatal(err)
	}
	return access.SemanticAttributeResolution{
		Subject: subject, Registry: registry,
		ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:control"},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: region.ID, DefinitionName: region.Name, DefinitionVersion: region.DefinitionVersion,
			Type: region.Type, Shape: region.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
}

func TestSemanticDiscoveryHandlesOrdinaryNilPolicyAndNilMetrics(t *testing.T) {
	if got := (Metrics{}).SemanticConsumerCacheAllowed("sales"); got {
		t.Fatal("nil metrics enabled semantic cache")
	}
	if err := (Metrics{}).AuthorizeSemanticTarget(context.Background(), "sales", semanticquery.SemanticAccessTarget{}); err == nil {
		t.Fatal("nil metrics authorized semantic target")
	}

	base := canonicalMetricsWithSnapshot(t, canonicalSnapshot(t, nil, nil), nil)
	underlying := base.Metrics.(canonicalMetrics)
	planner, err := semanticquery.NewCompiledPlanner(underlying.model)
	if err != nil {
		t.Fatal(err)
	}
	metrics := New(semanticDiscoveryPlannerMetrics{canonicalMetrics: underlying, planner: planner}, Options{})
	if _, err := metrics.SemanticPlanner(context.Background(), "sales"); err != nil {
		t.Fatalf("ordinary planner with nil policy: %v", err)
	}
}
