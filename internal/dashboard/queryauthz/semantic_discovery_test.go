package authz

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticDiscoveryMetrics struct {
	canonicalMetrics
	planner  *semanticquery.Planner
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (m semanticDiscoveryMetrics) Planner(string) (consumer.Planner, bool) {
	return m.planner, m.planner != nil
}

func (m semanticDiscoveryMetrics) SemanticPlannerSnapshot(ctx context.Context, modelID string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error) {
	return m.planner, m.snapshot, nil
}

func semanticDiscoveryFixture(t *testing.T) (Metrics, *semanticmodel.Model, *access.SemanticAttributeResolution) {
	t.Helper()
	base := canonicalMetricsWithSnapshot(t, canonicalSnapshot(t, nil, nil), nil)
	underlying := base.Metrics.(canonicalMetrics)
	model := underlying.model
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{"region_grant": {UserAttribute: "region", AllowedValues: []semanticmodel.SemanticAccessLiteral{{Kind: semanticmodel.SemanticAccessString, Text: "us"}}}},
		Datasets:     map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {RequiredAccessGrants: []string{"region_grant"}}},
	}
	definition := access.SemanticAttributeDefinition{ID: "def-region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true}
	definition.Metadata.Owner = access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, []access.SemanticAttributeDefinition{definition})
	if err != nil {
		t.Fatal(err)
	}
	values, valueDigest, err := access.CanonicalSemanticAttributeValues(definition, "us")
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	assignment := access.SemanticAttributeAssignment{ID: "assignment-region", DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: 1, Type: definition.Type, Shape: definition.Shape, Subject: subject, CanonicalValues: values, ValueDigest: valueDigest, AssignmentVersion: 1}
	controlDigest, err := access.SemanticAttributeControlDigest([]access.SemanticAttributeAssignment{assignment}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolution := &access.SemanticAttributeResolution{
		Subject: subject, Subjects: []access.SubjectRef{subject}, ObservedAt: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		Registry:   access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: registryDigest}, Definitions: []access.SemanticAttributeDefinition{definition}},
		Control:    access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: controlDigest}, Assignments: []access.SemanticAttributeAssignment{assignment}},
		Attributes: []access.EffectiveSemanticAttribute{{DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: 1, Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "direct"}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	metrics := New(semanticDiscoveryMetrics{canonicalMetrics: underlying, planner: planner, snapshot: canonicalSnapshot(t, nil, nil)}, Options{
		InstanceID:                "instance-1",
		ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) { return *resolution, nil },
		SnapshotFromContext: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
			return canonicalSnapshot(t, nil, nil), nil
		},
		PrincipalFromContext: func(context.Context) (Principal, bool) { return Principal{ID: "alice"}, true },
	})
	return metrics, model, resolution
}

func TestSemanticDiscoveryAndBindingUseTrustedAuthority(t *testing.T) {
	m, _, _ := semanticDiscoveryFixture(t)
	if err := m.AuthorizeSemanticTarget(t.Context(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	ctx, err := m.BindSemanticConsumer(t.Context(), "sales")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := semanticquery.SemanticAccessConsumerContextFromContext(ctx)
	if !ok || value.Binding.PrincipalID != "alice" || value.Binding.InstanceID != "instance-1" {
		t.Fatalf("binding = %#v", value)
	}
	if m.SemanticConsumerCacheAllowed("sales") {
		t.Fatal("protected shared reuse was allowed")
	}
}

func TestSemanticDiscoveryDoesNotTrustMutableModelOrSubject(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Metrics, *semanticmodel.Model, *access.SemanticAttributeResolution)
	}{
		{"missing resolver", func(m *Metrics, _ *semanticmodel.Model, _ *access.SemanticAttributeResolution) {
			m.resolveSemanticAttributes = nil
		}},
		{"stripped policy", func(_ *Metrics, model *semanticmodel.Model, _ *access.SemanticAttributeResolution) {
			model.AccessPolicy = semanticmodel.SemanticAccessPolicy{}
		}},
		{"wrong subject", func(_ *Metrics, _ *semanticmodel.Model, r *access.SemanticAttributeResolution) {
			r.Subject.ID = "mallory"
		}},
		{"stale digest", func(_ *Metrics, _ *semanticmodel.Model, r *access.SemanticAttributeResolution) {
			r.Control.State.Digest = "invalid"
		}},
		{"development bypass", func(m *Metrics, _ *semanticmodel.Model, _ *access.SemanticAttributeResolution) {
			m.principalFromContext = func(context.Context) (Principal, bool) { return Principal{ID: "alice", DevBypass: true}, true }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, model, resolution := semanticDiscoveryFixture(t)
			test.change(&m, model, resolution)
			if _, err := m.SemanticPlanner(t.Context(), "sales"); err == nil {
				t.Fatal("invalid semantic authority admitted")
			}
			if _, err := m.BindSemanticConsumer(t.Context(), "sales"); err == nil {
				t.Fatal("invalid semantic binding admitted")
			}
		})
	}
}

func TestSemanticDiscoveryPublicAndUnknownTargets(t *testing.T) {
	base := canonicalMetricsWithSnapshot(t, canonicalSnapshot(t, nil, nil), nil)
	if err := base.AuthorizeSemanticTarget(t.Context(), "sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := base.AuthorizeSemanticTarget(t.Context(), "sales", semanticquery.SemanticAccessTarget{Metric: "unknown"}); err == nil {
		t.Fatal("unknown public member accepted")
	}
	if _, err := base.BindSemanticConsumer(t.Context(), "sales"); err != nil {
		t.Fatal(err)
	}
}
