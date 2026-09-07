package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticCatalogLeaseStub struct {
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
	runtime  projectruntime.Runtime
}

func (l semanticCatalogLeaseStub) Release()                               {}
func (l semanticCatalogLeaseStub) Identity() projectgraph.ServingIdentity { return l.identity }
func (l semanticCatalogLeaseStub) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}
func (l semanticCatalogLeaseStub) Runtime() projectruntime.Runtime { return l.runtime }

type semanticCatalogRuntimeStub struct {
	definition projectmanifest.Project
	compiled   map[string]*semanticquery.CompiledModel
}

func (semanticCatalogRuntimeStub) Close() error                               { return nil }
func (r semanticCatalogRuntimeStub) ProjectManifest() projectmanifest.Project { return r.definition }
func (r semanticCatalogRuntimeStub) CompiledSemanticModel(modelID string) (*semanticquery.CompiledModel, bool) {
	compiled, ok := r.compiled[modelID]
	return compiled, ok && compiled != nil
}

type semanticCatalogAuditRecorder struct {
	events []access.CanonicalAuditEvent
	err    error
}

func (r *semanticCatalogAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func TestSemanticCatalogVisibilityAllowsMatchingProtectedPrincipal(t *testing.T) {
	model, registry, attribute := semanticCatalogFixture(t, "us")
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	identity := semanticCatalogIdentity(t)
	projectGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: identity.ProjectID, Kind: projectgraph.KindProject, Name: "demo"},
		{ID: "sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.FromControlState(identity, projectGraph, access.ControlState{InstanceID: "instance_demo", ProjectID: identity.ProjectID.String(), Revision: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := semanticCatalogLeaseStub{identity: identity, runtime: semanticCatalogRuntimeStub{
		definition: projectmanifest.Project{ID: identity.ProjectID.String(), SemanticModels: map[string]*semanticmodel.Model{"sales": model}},
		compiled:   map[string]*semanticquery.CompiledModel{"sales": compiled},
	}, snapshot: snapshot}
	resolution := access.SemanticAttributeResolution{
		Subject:  access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		Registry: registry, ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64)},
		Attributes: []access.EffectiveSemanticAttribute{attribute},
	}
	recorder := &semanticCatalogAuditRecorder{}
	ctx := dataquery.WithMetadata(context.Background(), dataquery.Metadata{RequestID: "catalog-request-1", CorrelationID: "catalog-correlation-1"})
	visible, err := SemanticCatalogVisibility(func(context.Context) (access.SemanticAttributeResolution, error) { return resolution, nil }, SemanticCatalogAuditConfig{
		Recorder: recorder, ActorFromContext: func(context.Context) (string, error) { return "principal-1", nil },
	})(ctx, lease, "sales")
	if err != nil || !visible {
		t.Fatalf("protected catalog visibility = %v, %v; want true", visible, err)
	}
	if len(recorder.events) == 0 {
		t.Fatal("protected catalog decision was not audited")
	}
	if recorder.events[0].RequestID != "catalog-request-1" || recorder.events[0].CorrelationID != "catalog-correlation-1" {
		t.Fatalf("catalog audit metadata = %#v", recorder.events[0])
	}
}

func TestSemanticCatalogVisibilityRequiresAuditForProtectedModel(t *testing.T) {
	model, registry, attribute := semanticCatalogFixture(t, "us")
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	identity := semanticCatalogIdentity(t)
	projectGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: identity.ProjectID, Kind: projectgraph.KindProject, Name: "demo"}, {ID: "sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.FromControlState(identity, projectGraph, access.ControlState{InstanceID: "instance_demo", ProjectID: identity.ProjectID.String(), Revision: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := semanticCatalogLeaseStub{identity: identity, snapshot: snapshot, runtime: semanticCatalogRuntimeStub{definition: projectmanifest.Project{ID: identity.ProjectID.String(), SemanticModels: map[string]*semanticmodel.Model{"sales": model}}, compiled: map[string]*semanticquery.CompiledModel{"sales": compiled}}}
	resolution := access.SemanticAttributeResolution{Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}, Registry: registry, ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64)}, Attributes: []access.EffectiveSemanticAttribute{attribute}}
	visible, err := SemanticCatalogVisibility(func(context.Context) (access.SemanticAttributeResolution, error) { return resolution, nil })(context.Background(), lease, "sales")
	if err != nil || visible {
		t.Fatalf("protected discovery without audit = %v, %v; want false without error", visible, err)
	}
}

func TestSemanticCatalogVisibilityFailsClosedOnAuditWriteFailure(t *testing.T) {
	model, registry, attribute := semanticCatalogFixture(t, "us")
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	identity := semanticCatalogIdentity(t)
	projectGraph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: identity.ProjectID, Kind: projectgraph.KindProject, Name: "demo"}, {ID: "sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.FromControlState(identity, projectGraph, access.ControlState{InstanceID: "instance_demo", ProjectID: identity.ProjectID.String(), Revision: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := semanticCatalogLeaseStub{identity: identity, snapshot: snapshot, runtime: semanticCatalogRuntimeStub{definition: projectmanifest.Project{ID: identity.ProjectID.String(), SemanticModels: map[string]*semanticmodel.Model{"sales": model}}, compiled: map[string]*semanticquery.CompiledModel{"sales": compiled}}}
	resolution := access.SemanticAttributeResolution{Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}, Registry: registry, ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64)}, Attributes: []access.EffectiveSemanticAttribute{attribute}}
	recorder := &semanticCatalogAuditRecorder{err: errors.New("audit store unavailable")}
	visible, err := SemanticCatalogVisibility(func(context.Context) (access.SemanticAttributeResolution, error) { return resolution, nil }, SemanticCatalogAuditConfig{Recorder: recorder, ActorFromContext: func(context.Context) (string, error) { return "principal-1", nil }})(context.Background(), lease, "sales")
	if err == nil || visible {
		t.Fatalf("audit write failure visibility = %v, %v; want false and error", visible, err)
	}
}

func TestSemanticCatalogVisibilityDeniesNonMatchingOrMissingResolution(t *testing.T) {
	model, registry, attribute := semanticCatalogFixture(t, "us")
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	identity := semanticCatalogIdentity(t)
	lease := semanticCatalogLeaseStub{identity: identity, runtime: semanticCatalogRuntimeStub{
		definition: projectmanifest.Project{ID: identity.ProjectID.String(), SemanticModels: map[string]*semanticmodel.Model{"sales": model}},
		compiled:   map[string]*semanticquery.CompiledModel{"sales": compiled},
	}}
	base := access.SemanticAttributeResolution{
		Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}, Registry: registry,
		ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64)}, Attributes: []access.EffectiveSemanticAttribute{attribute},
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(registry.Definitions[0], "eu")
	if err != nil {
		t.Fatal(err)
	}
	base.Attributes[0].CanonicalValues, base.Attributes[0].ValueDigest = values, digest
	visible, err := SemanticCatalogVisibility(func(context.Context) (access.SemanticAttributeResolution, error) { return base, nil })(context.Background(), lease, "sales")
	if err != nil || visible {
		t.Fatalf("non-matching protected visibility = %v, %v; want false", visible, err)
	}
	visible, err = SemanticCatalogVisibility(func(context.Context) (access.SemanticAttributeResolution, error) {
		return access.SemanticAttributeResolution{Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}, Registry: registry, ControlState: base.ControlState}, nil
	})(context.Background(), lease, "sales")
	if err != nil || visible {
		t.Fatalf("missing protected attribute visibility = %v, %v; want false", visible, err)
	}
}

func TestSemanticCatalogVisibilityFailsClosedForMissingOrStaleCompiledSnapshot(t *testing.T) {
	model, registry, _ := semanticCatalogFixture(t, "us")
	identity := semanticCatalogIdentity(t)
	resolver := func(context.Context) (access.SemanticAttributeResolution, error) {
		return access.SemanticAttributeResolution{Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"}}, nil
	}
	missing := semanticCatalogLeaseStub{identity: identity, runtime: semanticCatalogRuntimeStub{definition: projectmanifest.Project{ID: identity.ProjectID.String(), SemanticModels: map[string]*semanticmodel.Model{"sales": model}}}}
	visible, err := SemanticCatalogVisibility(resolver)(context.Background(), missing, "sales")
	if err != nil || visible {
		t.Fatalf("missing compiled snapshot visibility = %v, %v; want false", visible, err)
	}
	other := model.ExecutionSnapshot()
	other.Metrics["order_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "count_distinct", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}}
	stalePlanner, err := semanticquery.CompileModelWithSemanticAccess(other, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	stale := semanticCatalogLeaseStub{identity: identity, runtime: semanticCatalogRuntimeStub{
		definition: projectmanifest.Project{ID: identity.ProjectID.String(), SemanticModels: map[string]*semanticmodel.Model{"sales": model}},
		compiled:   map[string]*semanticquery.CompiledModel{"sales": stalePlanner},
	}}
	visible, err = SemanticCatalogVisibility(resolver)(context.Background(), stale, "sales")
	if err != nil || visible {
		t.Fatalf("stale compiled snapshot visibility = %v, %v; want false", visible, err)
	}
}

func TestSemanticCatalogVisibilityRejectsStaleLeaseProject(t *testing.T) {
	model, registry, _ := semanticCatalogFixture(t, "us")
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	identity := semanticCatalogIdentity(t)
	lease := semanticCatalogLeaseStub{identity: identity, runtime: semanticCatalogRuntimeStub{
		definition: projectmanifest.Project{ID: "project:other", SemanticModels: map[string]*semanticmodel.Model{"sales": model}},
		compiled:   map[string]*semanticquery.CompiledModel{"sales": compiled},
	}}
	visible, err := SemanticCatalogVisibility(nil)(context.Background(), lease, "sales")
	if err == nil || visible {
		t.Fatalf("stale project visibility = %v, %v; want false and error", visible, err)
	}
}

func semanticCatalogIdentity(t testing.TB) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project:demo", "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func semanticCatalogFixture(t testing.TB, value string) (*semanticmodel.Model, access.SemanticAttributeRegistrySnapshot, access.EffectiveSemanticAttribute) {
	t.Helper()
	definition := access.SemanticAttributeDefinition{ID: "def-region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true}
	registry := access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:" + strings.Repeat("a", 64)}, Definitions: []access.SemanticAttributeDefinition{definition}}
	canonical, digest, err := access.CanonicalSemanticAttributeValues(definition, value)
	if err != nil {
		t.Fatalf("canonical semantic attribute value: %v", err)
	}
	attribute := access.EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape, CanonicalValues: canonical, ValueDigest: digest, Source: "direct"}
	model := &semanticmodel.Model{
		Name:         "sales",
		Datasets:     map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders", RequiredAccessGrants: []string{"region_grant"}}},
		Tables:       map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}}}},
		Metrics:      map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}}},
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}}},
	}
	return model, registry, attribute
}
